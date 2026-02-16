package controller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/opendebrid/opendebrid/internal/config"
	"github.com/opendebrid/opendebrid/internal/controller/api"
	"github.com/opendebrid/opendebrid/internal/controller/cache"
	ctrlgrpc "github.com/opendebrid/opendebrid/internal/controller/grpc"
	"github.com/opendebrid/opendebrid/internal/controller/web"
	"github.com/opendebrid/opendebrid/internal/core/engine"
	"github.com/opendebrid/opendebrid/internal/core/engine/aria2"
	"github.com/opendebrid/opendebrid/internal/core/engine/ytdlp"
	"github.com/opendebrid/opendebrid/internal/core/event"
	"github.com/opendebrid/opendebrid/internal/core/fileserver"
	"github.com/opendebrid/opendebrid/internal/core/job"
	"github.com/opendebrid/opendebrid/internal/core/node"
	"github.com/opendebrid/opendebrid/internal/core/process"
	"github.com/opendebrid/opendebrid/internal/core/service"
	"github.com/opendebrid/opendebrid/internal/database"
	"github.com/opendebrid/opendebrid/internal/database/gen"
	"github.com/rs/zerolog/log"
	"golang.org/x/crypto/bcrypt"
)

func Run(ctx context.Context, cfg *config.Config) error {
	// 1. Connect to database
	pool, err := database.Connect(ctx, cfg.Database.URL, cfg.Database.MaxConnections)
	if err != nil {
		return fmt.Errorf("database connect: %w", err)
	}
	defer pool.Close()

	// 2. Run migrations
	if err := database.Migrate(ctx, pool); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}

	// 3. Auto-generate secrets on first boot
	jwtSecret, err := ensureSetting(ctx, pool, "jwt_secret", 32)
	if err != nil {
		return fmt.Errorf("jwt secret: %w", err)
	}
	if cfg.Auth.JWTSecret != "" {
		jwtSecret = cfg.Auth.JWTSecret
	}

	workerToken, err := ensureSetting(ctx, pool, "worker_auth_token", 32)
	if err != nil {
		return fmt.Errorf("worker token: %w", err)
	}

	// 4. Create admin user if none exists
	adminPassword, err := ensureAdmin(ctx, pool, cfg.Auth.AdminUsername, cfg.Auth.AdminPassword)
	if err != nil {
		return fmt.Errorf("admin setup: %w", err)
	}

	// 5. Initialize EventBus
	bus := event.NewBus()

	// 6. Initialize engine registry
	registry := engine.NewRegistry()

	// 7. Process manager
	procMgr := process.NewManager()

	// 8. Setup engines
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

	// 9. Start process manager
	if err := procMgr.StartAll(ctx); err != nil {
		log.Warn().Err(err).Msg("process manager start (some daemons may not be available)")
	}

	// 10. Node clients
	nodeClients := map[string]node.NodeClient{
		cfg.Node.ID: node.NewLocalNodeClient(cfg.Node.ID, registry),
	}

	// 11. Register controller node
	queries := gen.New(pool)
	engineNames := registry.List()
	enginesJSON, _ := encodeEngines(engineNames)
	queries.UpsertNode(ctx, gen.UpsertNodeParams{
		ID:            cfg.Node.ID,
		Name:          cfg.Node.Name,
		FileEndpoint:  fmt.Sprintf("http://%s:%d", cfg.Server.Host, cfg.Node.FileServerPort),
		Engines:       enginesJSON,
		IsController:  true,
		DiskTotal:     0,
		DiskAvailable: 0,
	})

	// 12. Job manager
	jobManager := job.NewManager(pool, bus)
	jobManager.SetupEventHandlers()

	// 13. Cache manager
	cacheMgr := cache.NewManager(pool, bus)
	cacheMgr.SetupSubscribers()

	// 14. Download service
	downloadSvc := service.NewDownloadService(registry, nodeClients, jobManager, pool, bus)

	// 15. File server
	fileBaseURL := fmt.Sprintf("http://%s:%d", cfg.Server.Host, cfg.Node.FileServerPort)
	fileSrv := fileserver.NewServer(jwtSecret, cfg.Node.DownloadDir)

	// 16. Parse durations
	jwtExpiry, err := time.ParseDuration(cfg.Auth.JWTExpiry)
	if err != nil {
		jwtExpiry = 24 * time.Hour
	}
	linkExpiry, err := time.ParseDuration(cfg.Limits.LinkExpiry)
	if err != nil {
		linkExpiry = 60 * time.Minute
	}

	// 17. Setup Echo
	e := echo.New()
	e.HideBanner = true
	api.SetupRouter(e, api.RouterConfig{
		DB:          pool,
		JWTSecret:   jwtSecret,
		JWTExpiry:   jwtExpiry,
		Svc:         downloadSvc,
		YtDlpEngine: ytdlpEngine,
		Signer:      fileSrv.GetSigner(),
		LinkExpiry:  linkExpiry,
		FileBaseURL: fileBaseURL,
	})

	// 18. Web UI
	webHandler := web.NewHandler(pool, downloadSvc, fileSrv.GetSigner(), registry, cfg.Node.ID)
	webHandler.RegisterRoutes(e)

	// 19. gRPC server for workers
	grpcSrv := ctrlgrpc.NewServer(pool, bus, registry, workerToken, nodeClients)
	go func() {
		addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.GRPCPort)
		if err := grpcSrv.Start(addr); err != nil {
			log.Fatal().Err(err).Msg("gRPC server failed")
		}
	}()

	// 20. Print banner
	printBanner(cfg, adminPassword, workerToken)

	// 21. Start API server
	go func() {
		addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
		if err := e.Start(addr); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("API server failed")
		}
	}()

	// 22. Start file server
	go func() {
		addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Node.FileServerPort)
		log.Info().Str("addr", addr).Msg("file server started")
		if err := http.ListenAndServe(addr, fileSrv.Handler()); err != nil {
			log.Fatal().Err(err).Msg("file server failed")
		}
	}()

	// 23. Watch daemons
	go procMgr.Watch(ctx)

	// 24. Wait for shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	grpcSrv.Stop()
	if err := e.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("server shutdown error")
	}
	procMgr.StopAll(shutdownCtx)
	return nil
}

func ensureSetting(ctx context.Context, pool *pgxpool.Pool, key string, byteLen int) (string, error) {
	queries := gen.New(pool)
	setting, err := queries.GetSetting(ctx, key)
	if err == nil && string(setting.Value) != `null` && string(setting.Value) != `"null"` {
		val := string(setting.Value)
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
		Value: []byte(fmt.Sprintf(`"%s"`, value)),
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
		Password: string(hash),
		Role:     "admin",
	})
	if err != nil {
		return "", err
	}
	return password, nil
}

func encodeEngines(names []string) ([]byte, error) {
	result := "["
	for i, n := range names {
		if i > 0 {
			result += ","
		}
		result += `"` + n + `"`
	}
	result += "]"
	return []byte(result), nil
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
	fmt.Printf("  API:   http://%s:%d\n", cfg.Server.Host, cfg.Server.Port)
	fmt.Printf("  Files: http://%s:%d\n", cfg.Server.Host, cfg.Node.FileServerPort)
	fmt.Printf("  gRPC:  %s:%d\n", cfg.Server.Host, cfg.Server.GRPCPort)
	fmt.Println("═══════════════════════════════════════════════════════")
	fmt.Println()
}
