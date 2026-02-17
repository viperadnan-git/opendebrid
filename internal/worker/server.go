package worker

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/opendebrid/opendebrid/internal/config"
	"github.com/opendebrid/opendebrid/internal/core/engine"
	"github.com/opendebrid/opendebrid/internal/core/engine/aria2"
	"github.com/opendebrid/opendebrid/internal/core/engine/ytdlp"
	"github.com/opendebrid/opendebrid/internal/core/event"
	"github.com/opendebrid/opendebrid/internal/core/process"
	"github.com/opendebrid/opendebrid/internal/mux"
	pb "github.com/opendebrid/opendebrid/internal/proto/gen"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func Run(ctx context.Context, cfg *config.Config) error {
	bus := event.NewBus()
	registry := engine.NewRegistry()
	procMgr := process.NewManager()

	if cfg.Engines.Aria2.Enabled {
		if _, err := exec.LookPath("aria2c"); err == nil {
			aria2Engine := aria2.New()
			if err := aria2Engine.Init(ctx, engine.EngineConfig{
				DownloadDir:   cfg.Engines.Aria2.DownloadDir,
				MaxConcurrent: cfg.Engines.Aria2.MaxConcurrent,
				Extra: map[string]string{
					"rpc_url":    cfg.Engines.Aria2.RPCURL,
					"rpc_secret": cfg.Engines.Aria2.RPCSecret,
				},
			}); err != nil {
				log.Warn().Err(err).Msg("aria2 engine init failed")
			} else {
				registry.Register(aria2Engine)
				log.Info().Msg("aria2 engine registered")
			}
		} else {
			log.Info().Msg("aria2c not found in PATH, skipping")
		}
	}

	if cfg.Engines.YtDlp.Enabled {
		if _, err := exec.LookPath(cfg.Engines.YtDlp.Binary); err == nil {
			ytdlpEngine := ytdlp.New()
			if err := ytdlpEngine.Init(ctx, engine.EngineConfig{
				DownloadDir:   cfg.Engines.YtDlp.DownloadDir,
				MaxConcurrent: cfg.Engines.YtDlp.MaxConcurrent,
				Extra: map[string]string{
					"binary":         cfg.Engines.YtDlp.Binary,
					"default_format": cfg.Engines.YtDlp.DefaultFormat,
				},
			}); err != nil {
				log.Warn().Err(err).Msg("yt-dlp engine init failed")
			} else {
				registry.Register(ytdlpEngine)
				log.Info().Msg("yt-dlp engine registered")
			}
		} else {
			log.Info().Str("binary", cfg.Engines.YtDlp.Binary).Msg("yt-dlp not found in PATH, skipping")
		}
	}

	if err := procMgr.StartAll(ctx); err != nil {
		log.Warn().Err(err).Msg("process manager start")
	}

	// Worker gRPC server (for controller -> worker RPCs)
	workerGRPC := newWorkerGRPCServer(registry, bus)
	workerGRPCSrv := grpc.NewServer()
	pb.RegisterNodeServiceServer(workerGRPCSrv, workerGRPC)

	// Multiplex file server + gRPC on a single port via h2c
	fileHandler := http.FileServer(http.Dir(cfg.Node.DownloadDir))
	handler := mux.NewHandler(workerGRPCSrv, fileHandler)
	httpServer := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler: handler,
	}

	go func() {
		log.Info().Str("addr", httpServer.Addr).Msg("worker server started (HTTP + gRPC)")
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("worker server failed")
		}
	}()

	// Connect to controller
	controllerConn, err := grpc.NewClient(
		cfg.Controller.URL,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("connect to controller: %w", err)
	}
	defer controllerConn.Close()

	client := pb.NewNodeServiceClient(controllerConn)

	fileEndpoint := fmt.Sprintf("http://%s:%d", cfg.Server.Host, cfg.Server.Port)
	engineNames := registry.List()

	diskTotal, diskAvail := getDiskStats(cfg.Node.DownloadDir)

	resp, err := client.Register(ctx, &pb.RegisterRequest{
		NodeId:        cfg.Node.ID,
		Name:          cfg.Node.Name,
		FileEndpoint:  fileEndpoint,
		Engines:       engineNames,
		DiskTotal:     diskTotal,
		DiskAvailable: diskAvail,
		AuthToken:     cfg.Controller.Token,
	})
	if err != nil {
		return fmt.Errorf("register with controller: %w", err)
	}
	if !resp.Accepted {
		return fmt.Errorf("controller rejected registration: %s", resp.Message)
	}

	heartbeatInterval := time.Duration(resp.HeartbeatIntervalSec) * time.Second
	if heartbeatInterval == 0 {
		heartbeatInterval = 10 * time.Second
	}

	log.Info().Str("node_id", cfg.Node.ID).Msg("registered with controller")

	go runHeartbeat(ctx, client, cfg.Node.ID, cfg.Node.DownloadDir, registry, heartbeatInterval)
	go procMgr.Watch(ctx)

	fmt.Println()
	fmt.Println("=======================================================")
	fmt.Println("  OpenDebrid Worker started")
	fmt.Printf("  Node ID: %s\n", cfg.Node.ID)
	fmt.Printf("  Engines: %v\n", engineNames)
	fmt.Printf("  Controller: %s\n", cfg.Controller.URL)
	fmt.Printf("  Server: http://%s:%d (HTTP + gRPC)\n", cfg.Server.Host, cfg.Server.Port)
	fmt.Println("=======================================================")
	fmt.Println()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("worker shutting down...")
	workerGRPCSrv.GracefulStop()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	httpServer.Shutdown(shutdownCtx)
	procMgr.StopAll(shutdownCtx)
	return nil
}

func runHeartbeat(ctx context.Context, client pb.NodeServiceClient, nodeID string, downloadDir string, registry *engine.Registry, interval time.Duration) {
	for {
		stream, err := client.Heartbeat(ctx)
		if err != nil {
			log.Error().Err(err).Msg("heartbeat stream failed, retrying in 5s")
			time.Sleep(5 * time.Second)
			continue
		}

		ticker := time.NewTicker(interval)

		func() {
			defer ticker.Stop()

			for {
				select {
				case <-ctx.Done():
					stream.CloseSend()
					return
				case <-ticker.C:
					engineHealth := make(map[string]bool)
					for _, name := range registry.List() {
						eng, err := registry.Get(name)
						if err == nil {
							h := eng.Health(ctx)
							engineHealth[name] = h.OK
						}
					}

					diskTotal, diskAvail := getDiskStats(downloadDir)

					err := stream.Send(&pb.HeartbeatPing{
						NodeId:        nodeID,
						DiskTotal:     diskTotal,
						DiskAvailable: diskAvail,
						ActiveJobs:    0,
						EngineHealth:  engineHealth,
						Timestamp:     time.Now().Unix(),
					})
					if err != nil {
						log.Error().Err(err).Msg("heartbeat send failed")
						return
					}

					_, err = stream.Recv()
					if err != nil {
						log.Error().Err(err).Msg("heartbeat recv failed")
						return
					}
				}
			}
		}()

		select {
		case <-ctx.Done():
			return
		default:
		}

		log.Warn().Msg("heartbeat stream lost, reconnecting in 5s")
		time.Sleep(5 * time.Second)
	}
}

func getDiskStats(dir string) (total, available int64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, 0
	}
	return int64(stat.Blocks) * int64(stat.Bsize), int64(stat.Bavail) * int64(stat.Bsize)
}
