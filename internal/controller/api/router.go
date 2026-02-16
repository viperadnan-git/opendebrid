package api

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"github.com/opendebrid/opendebrid/internal/controller/api/handlers"
	"github.com/opendebrid/opendebrid/internal/controller/api/middleware"
	"github.com/opendebrid/opendebrid/internal/core/engine/ytdlp"
	"github.com/opendebrid/opendebrid/internal/core/service"
)

type RouterConfig struct {
	DB          *pgxpool.Pool
	JWTSecret   string
	JWTExpiry   time.Duration
	Svc         *service.DownloadService
	YtDlpEngine *ytdlp.Engine
	LinkExpiry  time.Duration
	FileBaseURL string
}

func SetupRouter(e *echo.Echo, cfg RouterConfig) {
	// Global middleware
	e.Use(echomw.Recover())
	e.Use(echomw.RequestID())
	e.Use(echomw.CORSWithConfig(echomw.CORSConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE"},
	}))
	e.Use(echomw.RateLimiter(echomw.NewRateLimiterMemoryStore(20)))

	// Health check
	e.GET("/health", func(c echo.Context) error {
		return c.JSON(200, map[string]string{"status": "ok"})
	})

	// API v1
	v1 := e.Group("/api/v1")

	// Auth (public)
	authHandler := handlers.NewAuthHandler(cfg.DB, cfg.JWTSecret, cfg.JWTExpiry)
	auth := v1.Group("/auth")
	auth.POST("/register", authHandler.Register)
	auth.POST("/login", authHandler.Login)

	// Auth middleware
	authMw := middleware.Auth(cfg.JWTSecret, cfg.DB)

	auth.GET("/me", authHandler.Me, authMw)
	auth.POST("/apikey/regenerate", authHandler.RegenerateAPIKey, authMw)

	// aria2 endpoints
	aria2Handler := handlers.NewAria2Handler(cfg.Svc)
	aria2 := v1.Group("/aria2", authMw)
	aria2.POST("/add", aria2Handler.Add)
	aria2.GET("/jobs", aria2Handler.ListJobs)
	aria2.GET("/jobs/:id", aria2Handler.GetJob)
	aria2.GET("/jobs/:id/files", aria2Handler.GetJobFiles)
	aria2.DELETE("/jobs/:id", aria2Handler.DeleteJob)

	// yt-dlp endpoints
	if cfg.YtDlpEngine != nil {
		ytdlpHandler := handlers.NewYtDlpHandler(cfg.Svc, cfg.YtDlpEngine)
		yt := v1.Group("/ytdlp", authMw)
		yt.POST("/add", ytdlpHandler.Add)
		yt.POST("/info", ytdlpHandler.Info)
		yt.GET("/jobs", ytdlpHandler.ListJobs)
		yt.GET("/jobs/:id", ytdlpHandler.GetJob)
		yt.GET("/jobs/:id/files", ytdlpHandler.GetJobFiles)
		yt.DELETE("/jobs/:id", ytdlpHandler.DeleteJob)
	}

	// Unified downloads
	downloadsHandler := handlers.NewDownloadsHandler(cfg.Svc)
	dl := v1.Group("/downloads", authMw)
	dl.GET("", downloadsHandler.List)
	dl.GET("/:id", downloadsHandler.Get)

	// File endpoints
	filesHandler := handlers.NewFilesHandler(cfg.Svc, cfg.DB, cfg.LinkExpiry, cfg.FileBaseURL)
	files := v1.Group("/files", authMw)
	files.GET("/jobs/:id/browse", filesHandler.Browse)
	files.POST("/jobs/:id/link", filesHandler.GenerateLink)

	// Admin endpoints
	adminMw := middleware.AdminOnly()
	admin := v1.Group("/admin", authMw, adminMw)

	nodesHandler := handlers.NewNodesHandler(cfg.DB)
	admin.GET("/nodes", nodesHandler.List)
	admin.GET("/nodes/:id", nodesHandler.Get)
	admin.DELETE("/nodes/:id", nodesHandler.Delete)

	usersHandler := handlers.NewUsersHandler(cfg.DB)
	admin.GET("/users", usersHandler.List)
	admin.DELETE("/users/:id", usersHandler.Delete)

	settingsHandler := handlers.NewSettingsHandler(cfg.DB)
	admin.GET("/settings", settingsHandler.List)
	admin.GET("/settings/:key", settingsHandler.Get)
	admin.PATCH("/settings/:key", settingsHandler.Update)

	cacheHandler := handlers.NewCacheHandler(cfg.DB)
	admin.GET("/cache/lru", cacheHandler.ListLRU)
	admin.POST("/cache/cleanup", cacheHandler.Cleanup)
}
