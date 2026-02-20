package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/config"
	"github.com/viperadnan-git/opendebrid/internal/controller/api"
	ctrlgrpc "github.com/viperadnan-git/opendebrid/internal/controller/grpc"
	"github.com/viperadnan-git/opendebrid/internal/controller/scheduler"
	"github.com/viperadnan-git/opendebrid/internal/controller/web"
	"github.com/viperadnan-git/opendebrid/internal/core/enginesetup"
	"github.com/viperadnan-git/opendebrid/internal/core/fileserver"
	"github.com/viperadnan-git/opendebrid/internal/core/job"
	"github.com/viperadnan-git/opendebrid/internal/core/node"
	"github.com/viperadnan-git/opendebrid/internal/core/service"
	"github.com/viperadnan-git/opendebrid/internal/core/statusloop"
	"github.com/viperadnan-git/opendebrid/internal/core/util"
	"github.com/viperadnan-git/opendebrid/internal/database"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
	"github.com/viperadnan-git/opendebrid/internal/mux"
	odproto "github.com/viperadnan-git/opendebrid/internal/proto"
	pb "github.com/viperadnan-git/opendebrid/internal/proto/gen"
	"golang.org/x/crypto/bcrypt"
)

const (
	defaultJWTExpiry          = 24 * time.Hour
	defaultLinkExpiry         = 60 * time.Minute
	controllerShutdownTimeout = 10 * time.Second
	statusLoopInterval        = 3 * time.Second
	selfHeartbeatInterval     = 60 * time.Second
	reapInterval              = 60 * time.Second
)

func Run(ctx context.Context, cfg *config.Config) error {
	level, err := zerolog.ParseLevel(cfg.Logging.Level)
	if err == nil {
		zerolog.SetGlobalLevel(level)
	}
	log.Debug().Str("level", cfg.Logging.Level).Msg("log level configured")

	pool, err := database.Connect(ctx, cfg.Database.URL, cfg.Database.MaxConnections)
	if err != nil {
		return fmt.Errorf("database connect: %w", err)
	}
	defer pool.Close()

	if err := database.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}

	// Use config/env values if set, otherwise auto-generate and persist to DB
	jwtSecret := cfg.Auth.JWTSecret
	if jwtSecret == "" {
		var err error
		jwtSecret, err = ensureSetting(ctx, pool, "jwt_secret", 32)
		if err != nil {
			return fmt.Errorf("jwt secret: %w", err)
		}
	}

	workerToken := cfg.Node.AuthToken
	if workerToken == "" {
		var err error
		workerToken, err = ensureSetting(ctx, pool, "auth_token", 32)
		if err != nil {
			return fmt.Errorf("worker token: %w", err)
		}
	} else if _, err := gen.New(pool).UpsertSetting(ctx, gen.UpsertSettingParams{
		Key:   "auth_token",
		Value: fmt.Sprintf(`"%s"`, workerToken),
	}); err != nil {
		return fmt.Errorf("persist worker token: %w", err)
	}

	adminPassword, err := ensureAdmin(ctx, pool, cfg.Auth.AdminUsername, cfg.Auth.AdminPassword)
	if err != nil {
		return fmt.Errorf("admin setup: %w", err)
	}

	result := enginesetup.InitEngines(ctx, enginesetup.ConfigFromAppConfig(cfg))

	nodeClients := node.NewNodeClientStore()
	localTracker := statusloop.NewTracker()
	localNodeServer := node.NewNodeServer(cfg.Node.ID, result.Registry, localTracker, cfg.Node.DownloadDir)
	nodeClients.Set(cfg.Node.ID, localNodeServer)

	// Register controller as a node
	queries := gen.New(pool)
	engineNames := result.Registry.List()
	enginesJSON, _ := encodeEngines(engineNames)
	fileEndpoint := cfg.Server.URL
	diskTotal, diskAvail := util.DiskStats(cfg.Node.DownloadDir)
	if _, err := queries.UpsertNode(ctx, gen.UpsertNodeParams{
		ID:            cfg.Node.ID,
		FileEndpoint:  fileEndpoint,
		Engines:       enginesJSON,
		IsController:  true,
		DiskTotal:     diskTotal,
		DiskAvailable: diskAvail,
	}); err != nil {
		return fmt.Errorf("register controller node: %w", err)
	}

	// Disk-verified reconciliation for the controller's own node: scan
	// local download dirs, restore only inactive jobs whose files exist,
	// fail the rest, and clean up orphan directories.
	localDirs := collectLocalDownloadDirs(cfg)
	diskKeys := util.ScanStorageKeys(localDirs)
	reconcileResult := database.ReconcileNodeOnStartup(ctx, queries, cfg.Node.ID, diskKeys)
	go func() {
		validSet := make(map[string]bool, len(reconcileResult.ValidKeys))
		for _, k := range reconcileResult.ValidKeys {
			validSet[k] = true
		}
		util.RemoveOrphanedDirs(localDirs, validSet)
	}()

	// Clean up stale offline nodes from previous runs
	if err := queries.DeleteStaleNodes(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to clean stale nodes")
	}

	jobManager := job.NewManager(pool)

	linkExpiry, err := time.ParseDuration(cfg.Limits.LinkExpiry)
	if err != nil {
		linkExpiry = defaultLinkExpiry
	}

	sched := scheduler.NewScheduler(pool, cfg.Node.ID, scheduler.NewRoundRobin())
	downloadSvc := service.NewDownloadService(result.Registry, nodeClients, sched, jobManager, pool, cfg.Node.DownloadDir, linkExpiry)
	fileSrv := fileserver.NewServer(cfg.Node.DownloadDir, fileserver.NewDBTokenResolver(gen.New(pool)))

	jwtExpiry, err := time.ParseDuration(cfg.Auth.JWTExpiry)
	if err != nil {
		jwtExpiry = defaultJWTExpiry
	}

	// Setup Echo — single HTTP server for API, Web UI, and file downloads
	e := echo.New()
	e.HideBanner = true

	api.SetupRouter(e, api.RouterConfig{
		DB:          pool,
		JWTSecret:   jwtSecret,
		JWTExpiry:   jwtExpiry,
		Svc:         downloadSvc,
		YtDlpEngine: result.YtDlpEngine,
	})

	// File download route (no auth — token-based access)
	fileSrv.RegisterRoutes(e)

	webHandler := web.NewHandler(pool, jwtSecret, jwtExpiry, result.Registry.List(), cfg.Node.ID)
	webHandler.RegisterRoutes(e)

	// gRPC for workers
	grpcSrv := ctrlgrpc.NewServer(pool, result.Registry, workerToken, nodeClients)

	// Multiplex HTTP (Echo) + gRPC on a single port via h2c
	handler := mux.NewHandler(grpcSrv.GRPCServer(), e)
	httpServer := &http.Server{
		Addr:    fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port),
		Handler: handler,
	}

	printBanner(cfg, adminPassword, workerToken)

	go func() {
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("server failed")
		}
	}()

	// Local status push loop and process lifecycle via Runner
	localSink := &controllerLocalSink{server: grpcSrv}
	runner := node.NewRunner(localNodeServer, result.ProcMgr, localSink, cfg.Node.ID, statusLoopInterval)
	if err := runner.Start(ctx); err != nil {
		log.Warn().Err(err).Msg("process manager start (some daemons may not be available)")
	}

	// Background goroutines tracked for clean shutdown.
	var bgWG sync.WaitGroup
	heartbeatCtx, heartbeatCancel := context.WithCancel(context.Background())

	bgWG.Add(3)
	go func() { defer bgWG.Done(); downloadSvc.RunReconciliation(heartbeatCtx) }()
	go func() {
		defer bgWG.Done()
		controllerHeartbeat(heartbeatCtx, queries, cfg.Node.ID, cfg.Node.DownloadDir)
	}()
	go func() { defer bgWG.Done(); reapStaleNodes(heartbeatCtx, queries) }()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), controllerShutdownTimeout)
	defer cancel()

	heartbeatCancel()

	// Mark this controller node as offline and update affected jobs.
	database.MarkNodeOffline(shutdownCtx, queries, cfg.Node.ID)

	grpcSrv.Stop()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("server shutdown error")
	}
	if err := runner.Stop(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("failed to stop daemons")
	}
	bgWG.Wait()
	nodeClients.Close()
	return nil
}

func ensureSetting(ctx context.Context, pool *pgxpool.Pool, key string, byteLen int) (string, error) {
	queries := gen.New(pool)
	setting, err := queries.GetSetting(ctx, key)
	if err == nil && setting.Value != `null` && setting.Value != `"null"` {
		val := setting.Value
		if len(val) >= 2 && val[0] == '"' {
			val = val[1 : len(val)-1]
		}
		return val, nil
	}

	b := make([]byte, byteLen)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	value := hex.EncodeToString(b)
	if _, err := queries.UpsertSetting(ctx, gen.UpsertSettingParams{
		Key:   key,
		Value: fmt.Sprintf(`"%s"`, value),
	}); err != nil {
		return "", fmt.Errorf("upsert setting %s: %w", key, err)
	}
	return value, nil
}

func ensureAdmin(ctx context.Context, pool *pgxpool.Pool, username, password string) (string, error) {
	queries := gen.New(pool)
	count, err := queries.GetUserCount(ctx)
	if err != nil {
		return "", err
	}
	if count > 0 {
		return "", nil
	}

	if password == "" {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			return "", fmt.Errorf("generate password: %w", err)
		}
		password = hex.EncodeToString(b)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return "", err
	}

	_, err = queries.CreateUser(ctx, gen.CreateUserParams{
		Username: username,
		Email:    "admin@localhost",
		Password: string(hash),
		Role:     "admin",
	})
	if err != nil {
		return "", err
	}
	return password, nil
}

// collectLocalDownloadDirs returns engine download directories for the controller's local node.
func collectLocalDownloadDirs(cfg *config.Config) []string {
	var dirs []string
	if cfg.Engines.Aria2.Enabled {
		dirs = append(dirs, cfg.Engines.Aria2.DownloadDir)
	}
	if cfg.Engines.YtDlp.Enabled {
		dirs = append(dirs, cfg.Engines.YtDlp.DownloadDir)
	}
	return dirs
}

func encodeEngines(names []string) (string, error) {
	b, err := json.Marshal(names)
	return string(b), err
}

func controllerHeartbeat(ctx context.Context, queries *gen.Queries, nodeID, downloadDir string) {
	ticker := time.NewTicker(selfHeartbeatInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			diskTotal, diskAvail := util.DiskStats(downloadDir)
			if err := queries.UpdateNodeHeartbeat(ctx, gen.UpdateNodeHeartbeatParams{
				ID:            nodeID,
				DiskTotal:     diskTotal,
				DiskAvailable: diskAvail,
			}); err != nil {
				log.Warn().Err(err).Msg("controller self-heartbeat failed")
			}
		}
	}
}

func reapStaleNodes(ctx context.Context, queries *gen.Queries) {
	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			nodeIDs, err := queries.MarkStaleNodesOffline(ctx)
			if err != nil {
				log.Warn().Err(err).Msg("reaper: failed to mark stale nodes offline")
			}
			for _, nodeID := range nodeIDs {
				database.MarkNodeJobsForOffline(ctx, queries, nodeID)
			}
			if err := queries.DeleteStaleNodes(ctx); err != nil {
				log.Warn().Err(err).Msg("reaper: failed to delete stale nodes")
			}
			// Safety net: blind-restore inactive jobs on nodes that have been online
			// for >2 min without calling ReconcileNode (e.g. old workers).
			if err := queries.RestoreStaleInactiveJobs(ctx); err != nil {
				log.Warn().Err(err).Msg("reaper: failed to restore stale inactive jobs")
			}
		}
	}
}

// controllerLocalSink implements statusloop.Sink by calling the controller's
// gRPC handler methods directly — same DB write logic as remote workers,
// without the network layer.
type controllerLocalSink struct {
	server *ctrlgrpc.Server
}

func (s *controllerLocalSink) PushStatuses(ctx context.Context, nodeID string, reports []statusloop.StatusReport) error {
	_, err := s.server.PushJobStatuses(ctx, &pb.PushJobStatusesRequest{
		NodeId:   nodeID,
		Statuses: odproto.ReportsToProto(reports),
	})
	return err
}

func printBanner(cfg *config.Config, adminPassword, workerToken string) {
	fmt.Println()
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Println("  OpenDebrid Controller started")
	fmt.Println()
	if adminPassword != "" {
		fmt.Println("  Admin credentials (save these, shown only once):")
		fmt.Printf("    Username: %s\n", cfg.Auth.AdminUsername)
		fmt.Printf("    Password: %s\n", adminPassword)
		fmt.Println()
	}
	if workerToken != "" {
		fmt.Println("  Worker auth token (use this when adding workers):")
		fmt.Printf("    Token: %s\n", workerToken)
		fmt.Println()
	}
	fmt.Printf("  Server: http://%s:%d (HTTP + gRPC)\n", cfg.Server.Host, cfg.Server.Port)
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Println()
}
