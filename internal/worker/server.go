package worker

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/config"
	"github.com/viperadnan-git/opendebrid/internal/core/engine"
	"github.com/viperadnan-git/opendebrid/internal/core/enginesetup"
	"github.com/viperadnan-git/opendebrid/internal/core/fileserver"
	"github.com/viperadnan-git/opendebrid/internal/core/node"
	"github.com/viperadnan-git/opendebrid/internal/core/statusloop"
	"github.com/viperadnan-git/opendebrid/internal/core/util"
	"github.com/viperadnan-git/opendebrid/internal/mux"
	odproto "github.com/viperadnan-git/opendebrid/internal/proto"
	pb "github.com/viperadnan-git/opendebrid/internal/proto/gen"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	statusLoopInterval      = 3 * time.Second
	registrationRetryDelay  = 5 * time.Second
	heartbeatReconnectDelay = 5 * time.Second
	workerShutdownTimeout   = 10 * time.Second
	deregisterTimeout       = 3 * time.Second
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
	result := enginesetup.InitEngines(ctx, enginesetup.ConfigFromAppConfig(cfg))

	tracker := statusloop.NewTracker()
	nodeServer := node.NewNodeServer(cfg.Node.ID, result.Registry, tracker, cfg.Node.DownloadDir)

	// Worker gRPC server (for controller -> worker RPCs)
	workerGRPC := newWorkerGRPCServer(nodeServer)
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
	runner := node.NewRunner(nodeServer, result.ProcMgr, sink, cfg.Node.ID, statusLoopInterval)
	if err := runner.Start(ctx); err != nil {
		log.Warn().Err(err).Msg("process manager start")
	}

	fileEndpoint := cfg.Server.URL
	engineNames := result.Registry.List()
	diskTotal, diskAvail := util.DiskStats(cfg.Node.DownloadDir)

	// Retry registration up to 3 times in case the controller is briefly unavailable.
	const maxRegAttempts = 3
	var regResp *pb.RegisterResponse
	for attempt := 1; attempt <= maxRegAttempts; attempt++ {
		regResp, err = client.Register(ctx, &pb.RegisterRequest{
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
			log.Warn().Err(err).Int("attempt", attempt).Int("max", maxRegAttempts).Msg("registration failed, retrying")
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(registrationRetryDelay):
			}
		}
	}
	if err != nil {
		return fmt.Errorf("register with controller after %d attempts: %w", maxRegAttempts, err)
	}
	if !regResp.Accepted {
		return fmt.Errorf("controller rejected registration: %s", regResp.Message)
	}

	heartbeatInterval := time.Duration(regResp.HeartbeatIntervalSec) * time.Second
	if heartbeatInterval == 0 {
		heartbeatInterval = node.HeartbeatInterval
	}

	log.Info().Str("node_id", cfg.Node.ID).Msg("registered with controller")

	downloadDirs := collectDownloadDirs(cfg)
	go reconcileNode(ctx, client, cfg.Node.ID, downloadDirs)

	offlineTimeout, err := time.ParseDuration(cfg.Controller.OfflineTimeout)
	if err != nil || offlineTimeout <= 0 {
		offlineTimeout = time.Hour
	}

	// workCtx is cancelled either by signal or when the heartbeat offline timeout fires.
	workCtx, workCancel := context.WithCancel(ctx)
	defer workCancel()

	heartbeatCtx, heartbeatCancel := context.WithCancel(workCtx)
	go runHeartbeat(heartbeatCtx, workCancel, client, cfg.Node.ID, cfg.Node.DownloadDir, downloadDirs, result.Registry, heartbeatInterval, offlineTimeout)

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

	shutdownCtx, cancel := context.WithTimeout(context.Background(), workerShutdownTimeout)
	defer cancel()

	// Best-effort deregister with short timeout — if the controller is already
	// gone (e.g. docker compose down), this will fail and that's fine.
	deregCtx, deregCancel := context.WithTimeout(context.Background(), deregisterTimeout)
	if _, err := client.Deregister(deregCtx, &pb.DeregisterRequest{
		NodeId: cfg.Node.ID,
	}); err != nil {
		log.Info().Err(err).Msg("deregister failed (controller may already be down)")
	} else {
		log.Info().Msg("deregistered from controller")
	}
	deregCancel()

	// Use Stop() instead of GracefulStop() — when served via h2c (ServeHTTP),
	// the serverHandlerTransport does not implement Drain() and GracefulStop() panics.
	workerGRPCSrv.Stop()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("worker server shutdown error")
	}
	if err := runner.Stop(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("failed to stop worker daemons")
	}
	return nil
}

func runHeartbeat(ctx context.Context, cancel context.CancelFunc, client pb.NodeServiceClient, nodeID, downloadDir string, downloadDirs []string, registry *engine.Registry, interval, offlineTimeout time.Duration) {
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
			case <-time.After(heartbeatReconnectDelay):
			}
			continue
		}

		if !disconnectedAt.IsZero() {
			log.Info().Dur("offline_for", time.Since(disconnectedAt)).Msg("reconnected to controller")
			disconnectedAt = time.Time{}
			go reconcileNode(ctx, client, nodeID, downloadDirs)
		}

		ctxDone := false
		recvTimeout := 2 * interval
		func() {
			// sendPing builds and sends one heartbeat ping, then waits for ack.
			sendPing := func() error {
				engineHealth := make(map[string]bool)
				for _, name := range registry.List() {
					eng, engErr := registry.Get(name)
					if engErr == nil {
						engineHealth[name] = eng.Health(ctx).OK
					}
				}
				diskTotal, diskAvail := util.DiskStats(downloadDir)
				if err := stream.Send(&pb.HeartbeatPing{
					NodeId:        nodeID,
					DiskTotal:     diskTotal,
					DiskAvailable: diskAvail,
					EngineHealth:  engineHealth,
					Timestamp:     time.Now().Unix(),
				}); err != nil {
					return err
				}
				recvDone := make(chan error, 1)
				go func() {
					_, err := stream.Recv()
					recvDone <- err
				}()
				select {
				case err := <-recvDone:
					return err
				case <-time.After(recvTimeout):
					return fmt.Errorf("heartbeat recv timed out")
				case <-ctx.Done():
					_ = stream.CloseSend()
					return ctx.Err()
				}
			}

			// Send immediate first ping so the controller restores this
			// node quickly after a controller restart (instead of waiting
			// for the first ticker fire at 60s).
			if err := sendPing(); err != nil {
				if ctx.Err() != nil {
					ctxDone = true
				} else {
					log.Error().Err(err).Msg("heartbeat ping failed")
				}
				return
			}

			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					_ = stream.CloseSend()
					ctxDone = true
					return
				case <-ticker.C:
					if err := sendPing(); err != nil {
						if ctx.Err() != nil {
							ctxDone = true
						} else {
							log.Error().Err(err).Msg("heartbeat ping failed")
						}
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
		case <-time.After(heartbeatReconnectDelay):
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

// collectDownloadDirs returns the list of engine download directories.
func collectDownloadDirs(cfg *config.Config) []string {
	var dirs []string
	if cfg.Engines.Aria2.Enabled {
		dirs = append(dirs, cfg.Engines.Aria2.DownloadDir)
	}
	if cfg.Engines.YtDlp.Enabled {
		dirs = append(dirs, cfg.Engines.YtDlp.DownloadDir)
	}
	return dirs
}

// reconcileNode scans local disk storage keys and calls ReconcileNode on the
// controller for disk-verified restoration of inactive jobs, then removes
// orphaned download directories. Called at startup and on controller reconnect.
func reconcileNode(ctx context.Context, client pb.NodeServiceClient, nodeID string, downloadDirs []string) {
	diskKeys := util.ScanStorageKeys(downloadDirs)
	log.Info().Int("disk_keys", len(diskKeys)).Msg("scanned local storage keys for reconciliation")

	resp, err := client.ReconcileNode(ctx, &pb.ReconcileNodeRequest{
		NodeId:      nodeID,
		StorageKeys: diskKeys,
	})
	if err != nil {
		log.Warn().Err(err).Msg("ReconcileNode RPC failed, falling back to legacy cleanup")
		cleanupOrphanedDirs(ctx, client, nodeID, downloadDirs)
		return
	}

	log.Info().
		Int32("restored", resp.RestoredCount).
		Int32("failed", resp.FailedCount).
		Int("valid_keys", len(resp.ValidStorageKeys)).
		Msg("node reconciliation complete")

	validKeys := make(map[string]bool, len(resp.ValidStorageKeys))
	for _, k := range resp.ValidStorageKeys {
		validKeys[k] = true
	}
	util.RemoveOrphanedDirs(downloadDirs, validKeys)
}

// cleanupOrphanedDirs removes download directories that no longer have a
// matching job in the database. Called as fallback when ReconcileNode RPC
// is not available (old controller).
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
	util.RemoveOrphanedDirs(downloadDirs, validKeys)
}

const maxPendingTerminal = 1000

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

	statuses := odproto.ReportsToProto(all)

	log.Debug().Str("node_id", nodeID).Int("terminal", len(terminal)).Int("progress", len(progress)).Msg("pushing status updates to controller")
	_, err := s.client.PushJobStatuses(ctx, &pb.PushJobStatusesRequest{
		NodeId:   nodeID,
		Statuses: statuses,
	})
	if err != nil {
		// Buffer terminal reports for the next attempt; drop progress (re-polled in 3s).
		if len(terminal) > 0 {
			if len(terminal) > maxPendingTerminal {
				log.Warn().Int("dropped", len(terminal)-maxPendingTerminal).Msg("pending terminal buffer overflow, dropping oldest")
				terminal = terminal[len(terminal)-maxPendingTerminal:]
			}
			s.pendingTerminal = terminal
			log.Debug().Err(err).Int("buffered_terminal", len(terminal)).Msg("push failed, terminal statuses buffered for retry")
		}
		return err
	}
	return nil
}
