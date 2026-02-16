package worker

import (
	"context"
	"fmt"
	"net"
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
	"github.com/opendebrid/opendebrid/internal/core/fileserver"
	"github.com/opendebrid/opendebrid/internal/core/process"
	pb "github.com/opendebrid/opendebrid/internal/proto/gen"
	"github.com/rs/zerolog/log"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

func Run(ctx context.Context, cfg *config.Config) error {
	// 1. Init event bus
	bus := event.NewBus()

	// 2. Init engine registry
	registry := engine.NewRegistry()

	// 3. Process manager
	procMgr := process.NewManager()

	// 4. Auto-detect and setup engines
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

	// 5. Start process manager
	if err := procMgr.StartAll(ctx); err != nil {
		log.Warn().Err(err).Msg("process manager start")
	}

	// 6. Start file server
	fileSrv := fileserver.NewServer("", cfg.Node.DownloadDir)
	go func() {
		addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Node.FileServerPort)
		log.Info().Str("addr", addr).Msg("worker file server started")
		if err := http.ListenAndServe(addr, fileSrv.Handler()); err != nil {
			log.Fatal().Err(err).Msg("file server failed")
		}
	}()

	// 7. Connect to controller
	controllerConn, err := grpc.NewClient(
		cfg.Controller.GRPCEndpoint,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("connect to controller: %w", err)
	}
	defer controllerConn.Close()

	client := pb.NewNodeServiceClient(controllerConn)

	// 8. Register with controller
	fileEndpoint := fmt.Sprintf("http://%s:%d", cfg.Server.Host, cfg.Node.FileServerPort)
	engineNames := registry.List()

	// Get disk stats
	var diskTotal, diskAvail int64
	// Simple disk stats - best effort

	resp, err := client.Register(ctx, &pb.RegisterRequest{
		NodeId:        cfg.Node.ID,
		Name:          cfg.Node.Name,
		FileEndpoint:  fileEndpoint,
		Engines:       engineNames,
		DiskTotal:     diskTotal,
		DiskAvailable: diskAvail,
		AuthToken:     cfg.Controller.AuthToken,
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

	// 9. Start worker gRPC server (for controller -> worker RPCs)
	workerGRPC := newWorkerGRPCServer(registry, bus)
	workerGRPCSrv := grpc.NewServer()
	go func() {
		addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.GRPCPort)
		lis, err := net.Listen("tcp", addr)
		if err != nil {
			log.Fatal().Err(err).Msg("worker gRPC listen failed")
		}
		pb.RegisterNodeServiceServer(workerGRPCSrv, workerGRPC)
		log.Info().Str("addr", addr).Msg("worker gRPC server started")
		if err := workerGRPCSrv.Serve(lis); err != nil {
			log.Fatal().Err(err).Msg("worker gRPC server failed")
		}
	}()

	// 10. Start heartbeat stream
	go runHeartbeat(ctx, client, cfg.Node.ID, registry, heartbeatInterval)

	// 11. Watch daemons
	go procMgr.Watch(ctx)

	// Print banner
	fmt.Println()
	fmt.Println("=======================================================")
	fmt.Println("  OpenDebrid Worker started")
	fmt.Printf("  Node ID: %s\n", cfg.Node.ID)
	fmt.Printf("  Engines: %v\n", engineNames)
	fmt.Printf("  Controller: %s\n", cfg.Controller.GRPCEndpoint)
	fmt.Printf("  Files: http://%s:%d\n", cfg.Server.Host, cfg.Node.FileServerPort)
	fmt.Println("=======================================================")
	fmt.Println()

	// 12. Wait for shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("worker shutting down...")
	workerGRPCSrv.GracefulStop()
	procMgr.StopAll(ctx)
	return nil
}

func runHeartbeat(ctx context.Context, client pb.NodeServiceClient, nodeID string, registry *engine.Registry, interval time.Duration) {
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

					err := stream.Send(&pb.HeartbeatPing{
						NodeId:       nodeID,
						ActiveJobs:   0, // TODO: track active jobs
						EngineHealth: engineHealth,
						Timestamp:    time.Now().Unix(),
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
