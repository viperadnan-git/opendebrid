package worker

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/config"
	"github.com/viperadnan-git/opendebrid/internal/core/engine"
	"github.com/viperadnan-git/opendebrid/internal/core/engine/aria2"
	"github.com/viperadnan-git/opendebrid/internal/core/engine/ytdlp"
	"github.com/viperadnan-git/opendebrid/internal/core/event"
	"github.com/viperadnan-git/opendebrid/internal/core/fileserver"
	"github.com/viperadnan-git/opendebrid/internal/core/process"
	"github.com/viperadnan-git/opendebrid/internal/core/statusloop"
	"github.com/viperadnan-git/opendebrid/internal/mux"
	pb "github.com/viperadnan-git/opendebrid/internal/proto/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

// grpcTokenResolver implements fileserver.TokenResolver by calling the controller
// via the existing gRPC connection — no DB access needed on the worker.
type grpcTokenResolver struct {
	client pb.NodeServiceClient
}

func (r *grpcTokenResolver) ResolveToken(ctx context.Context, token string, increment bool) (string, error) {
	resp, err := r.client.ResolveDownloadToken(ctx, &pb.ResolveTokenRequest{Token: token, Increment: increment})
	if err != nil || !resp.Valid {
		return "", fmt.Errorf("invalid or expired token")
	}
	return resp.RelPath, nil
}

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
				if de, ok := engine.Engine(aria2Engine).(engine.DaemonEngine); ok {
					procMgr.Register(de.Daemon())
				}
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
			log.Warn().Str("binary", cfg.Engines.YtDlp.Binary).Msg("yt-dlp not found in PATH, skipping")
		}
	}

	if err := procMgr.StartAll(ctx); err != nil {
		log.Warn().Err(err).Msg("process manager start")
	}

	// Job tracker for status push
	tracker := statusloop.NewTracker()

	// Worker gRPC server (for controller -> worker RPCs)
	workerGRPC := newWorkerGRPCServer(registry, bus, tracker, cfg.Node.DownloadDir)
	workerGRPCSrv := grpc.NewServer()
	pb.RegisterNodeServiceServer(workerGRPCSrv, workerGRPC)

	// Connect to controller — strip scheme if present since gRPC expects host:port
	controllerTarget := cfg.Controller.URL
	if u, err := url.Parse(controllerTarget); err == nil && u.Host != "" {
		controllerTarget = u.Host
	}
	controllerConn, err := grpc.NewClient(
		controllerTarget,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithPerRPCCredentials(tokenCredentials{token: cfg.Node.AuthToken}),
	)
	if err != nil {
		return fmt.Errorf("connect to controller: %w", err)
	}
	defer func() { _ = controllerConn.Close() }()

	client := pb.NewNodeServiceClient(controllerConn)

	// File server — tokens are validated via gRPC to controller (no DB needed)
	fileSrv := fileserver.NewServer(cfg.Node.DownloadDir, &grpcTokenResolver{client: client})

	// Echo instance for HTTP — worker only exposes the /dl/ download route
	e := echo.New()
	e.HideBanner = true
	fileSrv.RegisterRoutes(e)

	// Multiplex Echo (file server) + gRPC on a single port via h2c
	handler := mux.NewHandler(workerGRPCSrv, e)
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

	sink := &grpcSink{client: client}

	fileEndpoint := cfg.Server.URL
	engineNames := registry.List()

	diskTotal, diskAvail := getDiskStats(cfg.Node.DownloadDir)

	// Retry registration up to 3 times in case the controller is briefly unavailable.
	const maxRegAttempts = 3
	var resp *pb.RegisterResponse
	for attempt := 1; attempt <= maxRegAttempts; attempt++ {
		resp, err = client.Register(ctx, &pb.RegisterRequest{
			NodeId:        cfg.Node.ID,
			FileEndpoint:  fileEndpoint,
			Engines:       engineNames,
			DiskTotal:     diskTotal,
			DiskAvailable: diskAvail,
		})
		if err == nil {
			break
		}
		if attempt < maxRegAttempts {
			log.Warn().Err(err).Int("attempt", attempt).Int("max", maxRegAttempts).Msg("registration failed, retrying in 5s")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
			}
		}
	}
	if err != nil {
		return fmt.Errorf("register with controller after %d attempts: %w", maxRegAttempts, err)
	}
	if !resp.Accepted {
		return fmt.Errorf("controller rejected registration: %s", resp.Message)
	}

	heartbeatInterval := time.Duration(resp.HeartbeatIntervalSec) * time.Second
	if heartbeatInterval == 0 {
		heartbeatInterval = 60 * time.Second
	}

	log.Info().Str("node_id", cfg.Node.ID).Msg("registered with controller")

	// Clean up orphaned download directories in background
	go func() {
		var downloadDirs []string
		if cfg.Engines.Aria2.Enabled {
			downloadDirs = append(downloadDirs, cfg.Engines.Aria2.DownloadDir)
		}
		if cfg.Engines.YtDlp.Enabled {
			downloadDirs = append(downloadDirs, cfg.Engines.YtDlp.DownloadDir)
		}
		cleanupOrphanedDirs(ctx, client, cfg.Node.ID, downloadDirs)
	}()

	offlineTimeout, err := time.ParseDuration(cfg.Controller.OfflineTimeout)
	if err != nil || offlineTimeout <= 0 {
		offlineTimeout = time.Hour
	}

	// workCtx is cancelled either by signal or when the heartbeat offline timeout fires.
	workCtx, workCancel := context.WithCancel(ctx)
	defer workCancel()

	heartbeatCtx, heartbeatCancel := context.WithCancel(workCtx)
	go runHeartbeat(heartbeatCtx, workCancel, client, cfg.Node.ID, cfg.Node.DownloadDir, registry, heartbeatInterval, offlineTimeout)
	go statusloop.Run(heartbeatCtx, sink, cfg.Node.ID, registry, tracker, 3*time.Second)
	go procMgr.Watch(workCtx)

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
	select {
	case <-quit:
		log.Info().Msg("worker shutting down (signal)...")
	case <-workCtx.Done():
		log.Info().Msg("worker shutting down (controller offline too long)...")
	}

	heartbeatCancel()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Best-effort deregister with short timeout — if the controller is already
	// gone (e.g. docker compose down), this will fail and that's fine.
	deregCtx, deregCancel := context.WithTimeout(context.Background(), 3*time.Second)
	if _, err := client.Deregister(deregCtx, &pb.DeregisterRequest{
		NodeId: cfg.Node.ID,
	}); err != nil {
		log.Info().Err(err).Msg("deregister failed (controller may already be down)")
	} else {
		log.Info().Msg("deregistered from controller")
	}
	deregCancel()

	workerGRPCSrv.GracefulStop()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("worker server shutdown error")
	}
	if err := procMgr.StopAll(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("failed to stop worker daemons")
	}
	return nil
}

func runHeartbeat(ctx context.Context, cancel context.CancelFunc, client pb.NodeServiceClient, nodeID, downloadDir string, registry *engine.Registry, interval, offlineTimeout time.Duration) {
	var disconnectedAt time.Time

	for {
		if ctx.Err() != nil {
			return
		}

		stream, err := client.Heartbeat(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if disconnectedAt.IsZero() {
				disconnectedAt = time.Now()
				log.Warn().Dur("offline_timeout", offlineTimeout).Msg("controller connection lost, will exit if not recovered in time")
			} else if time.Since(disconnectedAt) >= offlineTimeout {
				log.Error().Dur("offline_for", time.Since(disconnectedAt)).Msg("controller offline too long, shutting down worker")
				cancel()
				return
			}
			log.Error().Err(err).Msg("heartbeat stream failed, retrying in 5s")
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
			continue
		}

		if !disconnectedAt.IsZero() {
			log.Info().Dur("offline_for", time.Since(disconnectedAt)).Msg("reconnected to controller")
			disconnectedAt = time.Time{}
		}

		ticker := time.NewTicker(interval)
		ctxDone := false
		func() {
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					_ = stream.CloseSend()
					ctxDone = true
					return
				case <-ticker.C:
					engineHealth := make(map[string]bool)
					for _, name := range registry.List() {
						eng, engErr := registry.Get(name)
						if engErr == nil {
							h := eng.Health(ctx)
							engineHealth[name] = h.OK
						}
					}
					diskTotal, diskAvail := getDiskStats(downloadDir)
					if sendErr := stream.Send(&pb.HeartbeatPing{
						NodeId:        nodeID,
						DiskTotal:     diskTotal,
						DiskAvailable: diskAvail,
						EngineHealth:  engineHealth,
						Timestamp:     time.Now().Unix(),
					}); sendErr != nil {
						log.Error().Err(sendErr).Msg("heartbeat send failed")
						return
					}
					if _, recvErr := stream.Recv(); recvErr != nil {
						log.Error().Err(recvErr).Msg("heartbeat recv failed")
						return
					}
				}
			}
		}()

		if ctxDone {
			return
		}

		// Stream died — start or continue the offline timer.
		if disconnectedAt.IsZero() {
			disconnectedAt = time.Now()
			log.Warn().Dur("offline_timeout", offlineTimeout).Msg("controller connection lost, will exit if not recovered in time")
		}
		if time.Since(disconnectedAt) >= offlineTimeout {
			log.Error().Dur("offline_for", time.Since(disconnectedAt)).Msg("controller offline too long, shutting down worker")
			cancel()
			return
		}

		log.Warn().Msg("heartbeat stream lost, reconnecting in 5s")
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

// tokenCredentials implements grpc.PerRPCCredentials to send the auth token
// as metadata on every RPC call.
type tokenCredentials struct {
	token string
}

func (t tokenCredentials) GetRequestMetadata(ctx context.Context, uri ...string) (map[string]string, error) {
	return map[string]string{"authorization": t.token}, nil
}

func (t tokenCredentials) RequireTransportSecurity() bool {
	return false
}

func getDiskStats(dir string) (total, available int64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, 0
	}
	return int64(stat.Blocks) * int64(stat.Bsize), int64(stat.Bavail) * int64(stat.Bsize)
}

// cleanupOrphanedDirs removes download directories that no longer have a
// matching job in the database. Called once on startup after registration.
func cleanupOrphanedDirs(ctx context.Context, client pb.NodeServiceClient, nodeID string, downloadDirs []string) {
	resp, err := client.ListNodeStorageKeys(ctx, &pb.ListNodeStorageKeysRequest{NodeId: nodeID})
	if err != nil {
		log.Warn().Err(err).Msg("failed to fetch storage keys for cleanup")
		return
	}

	validKeys := make(map[string]bool, len(resp.StorageKeys))
	for _, k := range resp.StorageKeys {
		validKeys[k] = true
	}

	for _, dir := range downloadDirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !isStorageKeyDir(name) {
				continue
			}
			if validKeys[name] {
				continue
			}
			fullPath := filepath.Join(dir, name)
			log.Info().Str("path", fullPath).Msg("removing orphaned download directory")
			_ = os.RemoveAll(fullPath)
		}
	}
}

// isStorageKeyDir checks if a directory name looks like a storage key (32 hex chars).
func isStorageKeyDir(name string) bool {
	if len(name) != 32 {
		return false
	}
	for _, c := range name {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// grpcSink implements statusloop.Sink by forwarding updates to the controller via gRPC.
// Terminal statuses (completed/failed) are buffered on failure and retried on the next
// push; progress-only updates are dropped (they will be re-polled in ~3s).
type grpcSink struct {
	client          pb.NodeServiceClient
	pendingTerminal []statusloop.StatusReport
}

func (s *grpcSink) PushStatuses(ctx context.Context, nodeID string, reports []statusloop.StatusReport) error {
	var terminal, progress []statusloop.StatusReport
	for _, r := range reports {
		if r.Status == "completed" || r.Status == "failed" {
			terminal = append(terminal, r)
		} else {
			progress = append(progress, r)
		}
	}

	// Prepend any previously buffered terminal reports so they are retried first.
	if len(s.pendingTerminal) > 0 {
		terminal = append(s.pendingTerminal, terminal...)
		s.pendingTerminal = nil
	}

	all := append(terminal, progress...)
	if len(all) == 0 {
		return nil
	}

	statuses := make([]*pb.JobStatusReport, len(all))
	for i, r := range all {
		statuses[i] = &pb.JobStatusReport{
			JobId:          r.JobID,
			EngineJobId:    r.EngineJobID,
			Status:         r.Status,
			Progress:       r.Progress,
			Speed:          r.Speed,
			TotalSize:      r.TotalSize,
			DownloadedSize: r.DownloadedSize,
			Name:           r.Name,
			Error:          r.Error,
		}
	}

	log.Debug().Str("node_id", nodeID).Int("terminal", len(terminal)).Int("progress", len(progress)).Msg("pushing status updates to controller")
	_, err := s.client.PushJobStatuses(ctx, &pb.PushJobStatusesRequest{
		NodeId:   nodeID,
		Statuses: statuses,
	})
	if err != nil {
		// Buffer terminal reports for the next attempt; drop progress (re-polled in 3s).
		if len(terminal) > 0 {
			s.pendingTerminal = terminal
			log.Debug().Err(err).Int("buffered_terminal", len(terminal)).Msg("push failed, terminal statuses buffered for retry")
		}
		return err
	}
	return nil
}
