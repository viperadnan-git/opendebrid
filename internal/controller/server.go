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
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/viperadnan-git/opendebrid/internal/config"
	"github.com/viperadnan-git/opendebrid/internal/controller/api"
	ctrlgrpc "github.com/viperadnan-git/opendebrid/internal/controller/grpc"
	"github.com/viperadnan-git/opendebrid/internal/controller/web"
	"github.com/viperadnan-git/opendebrid/internal/core/engine"
	"github.com/viperadnan-git/opendebrid/internal/core/engine/aria2"
	"github.com/viperadnan-git/opendebrid/internal/core/engine/ytdlp"
	"github.com/viperadnan-git/opendebrid/internal/core/event"
	"github.com/viperadnan-git/opendebrid/internal/core/fileserver"
	"github.com/viperadnan-git/opendebrid/internal/core/job"
	"github.com/viperadnan-git/opendebrid/internal/core/node"
	"github.com/viperadnan-git/opendebrid/internal/core/process"
	"github.com/viperadnan-git/opendebrid/internal/core/service"
	"github.com/viperadnan-git/opendebrid/internal/controller/scheduler"
	"github.com/viperadnan-git/opendebrid/internal/database"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
	"github.com/viperadnan-git/opendebrid/internal/mux"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"
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
	}

	adminPassword, err := ensureAdmin(ctx, pool, cfg.Auth.AdminUsername, cfg.Auth.AdminPassword)
	if err != nil {
		return fmt.Errorf("admin setup: %w", err)
	}

	bus := event.NewBus()
	registry := engine.NewRegistry()
	procMgr := process.NewManager()

	if cfg.Engines.Aria2.Enabled {
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
	}

	var ytdlpEngine *ytdlp.Engine
	if cfg.Engines.YtDlp.Enabled {
		ytdlpEngine = ytdlp.New()
		if err := ytdlpEngine.Init(ctx, engine.EngineConfig{
			DownloadDir:   cfg.Engines.YtDlp.DownloadDir,
			MaxConcurrent: cfg.Engines.YtDlp.MaxConcurrent,
			Extra: map[string]string{
				"binary":         cfg.Engines.YtDlp.Binary,
				"default_format": cfg.Engines.YtDlp.DefaultFormat,
			},
		}); err != nil {
			log.Warn().Err(err).Msg("yt-dlp engine init failed")
			ytdlpEngine = nil
		} else {
			registry.Register(ytdlpEngine)
			log.Info().Msg("yt-dlp engine registered")
		}
	}

	if err := procMgr.StartAll(ctx); err != nil {
		log.Warn().Err(err).Msg("process manager start (some daemons may not be available)")
	}

	nodeClients := map[string]node.NodeClient{
		cfg.Node.ID: node.NewLocalNodeClient(cfg.Node.ID, registry),
	}

	// Register controller as a node
	queries := gen.New(pool)
	engineNames := registry.List()
	enginesJSON, _ := encodeEngines(engineNames)
	fileEndpoint := cfg.Server.URL
	diskTotal, diskAvail := controllerDiskStats(cfg.Node.DownloadDir)
	if _, err := queries.UpsertNode(ctx, gen.UpsertNodeParams{
		ID:            cfg.Node.ID,
		Name:          cfg.Node.Name,
		FileEndpoint:  fileEndpoint,
		Engines:       enginesJSON,
		IsController:  true,
		DiskTotal:     diskTotal,
		DiskAvailable: diskAvail,
	}); err != nil {
		return fmt.Errorf("register controller node: %w", err)
	}

	// Clean up stale offline nodes from previous runs
	if err := queries.DeleteStaleNodes(ctx); err != nil {
		log.Warn().Err(err).Msg("failed to clean stale nodes")
	}

	jobManager := job.NewManager(pool, bus)
	jobManager.SetupEventHandlers()

	sched := scheduler.NewScheduler(pool, scheduler.NewRoundRobin(), cfg.Node.ID)
	downloadSvc := service.NewDownloadService(registry, nodeClients, sched, jobManager, pool, bus)
	fileSrv := fileserver.NewServer(pool, cfg.Node.DownloadDir)

	jwtExpiry, err := time.ParseDuration(cfg.Auth.JWTExpiry)
	if err != nil {
		jwtExpiry = 24 * time.Hour
	}
	linkExpiry, err := time.ParseDuration(cfg.Limits.LinkExpiry)
	if err != nil {
		linkExpiry = 60 * time.Minute
	}

	// Setup Echo — single HTTP server for API, Web UI, and file downloads
	e := echo.New()
	e.HideBanner = true

	api.SetupRouter(e, api.RouterConfig{
		DB:          pool,
		JWTSecret:   jwtSecret,
		JWTExpiry:   jwtExpiry,
		Svc:         downloadSvc,
		YtDlpEngine: ytdlpEngine,
		LinkExpiry:  linkExpiry,
		FileBaseURL: fileEndpoint,
	})

	// File download route (no auth — token-based access)
	e.GET("/dl/:token/:filename", echo.WrapHandler(http.HandlerFunc(fileSrv.ServeFile)))

	webHandler := web.NewHandler(pool, jwtSecret, registry.List(), cfg.Node.ID)
	webHandler.RegisterRoutes(e)

	// gRPC for workers
	grpcSrv := ctrlgrpc.NewServer(pool, bus, registry, workerToken, nodeClients)

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

	go procMgr.Watch(ctx)

	// Background status watcher: polls active jobs every 5s, syncs to DB via events
	heartbeatCtx, heartbeatCancel := context.WithCancel(context.Background())
	go downloadSvc.RunStatusWatcher(heartbeatCtx, 5*time.Second)

	// Periodic self-heartbeat (60s) and stale node reaper (60s)
	go controllerHeartbeat(heartbeatCtx, queries, cfg.Node.ID, cfg.Node.DownloadDir)
	go reapStaleNodes(heartbeatCtx, queries)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	heartbeatCancel()

	// Mark this controller node as offline
	if err := queries.SetNodeOffline(shutdownCtx, cfg.Node.ID); err != nil {
		log.Error().Err(err).Msg("failed to mark controller offline")
	}

	grpcSrv.Stop()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("server shutdown error")
	}
	procMgr.StopAll(shutdownCtx)
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
	queries.UpsertSetting(ctx, gen.UpsertSettingParams{
		Key:   key,
		Value: fmt.Sprintf(`"%s"`, value),
	})
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
		rand.Read(b)
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

func encodeEngines(names []string) (string, error) {
	b, err := json.Marshal(names)
	return string(b), err
}

func controllerDiskStats(dir string) (total, available int64) {
	var stat syscall.Statfs_t
	if err := syscall.Statfs(dir, &stat); err != nil {
		return 0, 0
	}
	return int64(stat.Blocks) * int64(stat.Bsize), int64(stat.Bavail) * int64(stat.Bsize)
}

func controllerHeartbeat(ctx context.Context, queries *gen.Queries, nodeID, downloadDir string) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			diskTotal, diskAvail := controllerDiskStats(downloadDir)
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
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := queries.MarkStaleNodesOffline(ctx); err != nil {
				log.Warn().Err(err).Msg("reaper: failed to mark stale nodes offline")
			}
			if err := queries.DeleteStaleNodes(ctx); err != nil {
				log.Warn().Err(err).Msg("reaper: failed to delete stale nodes")
			}
		}
	}
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
