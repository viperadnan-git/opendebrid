package api

import (
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humaecho"
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
	e.Use(echomw.Recover())
	e.Use(echomw.RequestID())
	e.Use(echomw.CORSWithConfig(echomw.CORSConfig{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE"},
	}))
	e.Use(echomw.RateLimiter(echomw.NewRateLimiterMemoryStore(20)))

	e.GET("/health", func(c echo.Context) error {
		return c.JSON(200, map[string]string{"status": "ok"})
	})

	v1 := e.Group("/api/v1")
	config := huma.DefaultConfig("OpenDebrid API", "1.0.0")
	config.Servers = []*huma.Server{{URL: "/api/v1"}}
	config.Info.Description = "Self-hosted multi-node debrid service"
	config.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"BearerAuth": {
			Type:         "http",
			Scheme:       "bearer",
			BearerFormat: "JWT",
			Description:  "JWT Bearer token",
		},
		"ApiKeyAuth": {
			Type: "apiKey",
			In:   "header",
			Name: "X-API-Key",
			Description: "API key",
		},
	}

	api := humaecho.NewWithGroup(e, v1, config)

	authMw := middleware.Auth(cfg.JWTSecret, cfg.DB)
	adminMw := middleware.AdminOnly()

	authHandler := handlers.NewAuthHandler(cfg.DB, cfg.JWTSecret, cfg.JWTExpiry)
	huma.Register(api, huma.Operation{
		OperationID: "register",
		Method:      http.MethodPost,
		Path:        "/auth/register",
		Summary:     "Register a new user",
		Tags:        []string{"Auth"},
	}, authHandler.Register)

	huma.Register(api, huma.Operation{
		OperationID:   "login",
		Method:        http.MethodPost,
		Path:          "/auth/login",
		Summary:       "Login and get JWT token",
		Tags:          []string{"Auth"},
	}, authHandler.Login)

	huma.Register(api, huma.Operation{
		OperationID: "get-me",
		Method:      http.MethodGet,
		Path:        "/auth/me",
		Summary:     "Get current user info",
		Tags:        []string{"Auth"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw},
	}, authHandler.Me)

	huma.Register(api, huma.Operation{
		OperationID: "regenerate-api-key",
		Method:      http.MethodPost,
		Path:        "/auth/apikey/regenerate",
		Summary:     "Regenerate API key",
		Tags:        []string{"Auth"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw},
	}, authHandler.RegenerateAPIKey)

	aria2Handler := handlers.NewAria2Handler(cfg.Svc)
	huma.Register(api, huma.Operation{
		OperationID:   "aria2-add",
		Method:        http.MethodPost,
		Path:          "/aria2/add",
		Summary:       "Add aria2 download",
		Tags:          []string{"Aria2"},
		Security:      []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:    huma.Middlewares{authMw},
		DefaultStatus: http.StatusCreated,
	}, aria2Handler.Add)

	huma.Register(api, huma.Operation{
		OperationID: "aria2-list-jobs",
		Method:      http.MethodGet,
		Path:        "/aria2/jobs",
		Summary:     "List aria2 jobs",
		Tags:        []string{"Aria2"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw},
	}, aria2Handler.ListJobs)

	huma.Register(api, huma.Operation{
		OperationID: "aria2-get-job",
		Method:      http.MethodGet,
		Path:        "/aria2/jobs/{id}",
		Summary:     "Get aria2 job status",
		Tags:        []string{"Aria2"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw},
	}, aria2Handler.GetJob)

	huma.Register(api, huma.Operation{
		OperationID: "aria2-get-job-files",
		Method:      http.MethodGet,
		Path:        "/aria2/jobs/{id}/files",
		Summary:     "Get aria2 job files",
		Tags:        []string{"Aria2"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw},
	}, aria2Handler.GetJobFiles)

	huma.Register(api, huma.Operation{
		OperationID: "aria2-delete-job",
		Method:      http.MethodDelete,
		Path:        "/aria2/jobs/{id}",
		Summary:     "Cancel/delete aria2 job",
		Tags:        []string{"Aria2"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw},
	}, aria2Handler.DeleteJob)

	if cfg.YtDlpEngine != nil {
		ytdlpHandler := handlers.NewYtDlpHandler(cfg.Svc, cfg.YtDlpEngine)
		huma.Register(api, huma.Operation{
			OperationID:   "ytdlp-add",
			Method:        http.MethodPost,
			Path:          "/ytdlp/add",
			Summary:       "Add yt-dlp download",
			Tags:          []string{"YT-DLP"},
			Security:      []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
			Middlewares:    huma.Middlewares{authMw},
			DefaultStatus: http.StatusCreated,
		}, ytdlpHandler.Add)

		huma.Register(api, huma.Operation{
			OperationID: "ytdlp-info",
			Method:      http.MethodPost,
			Path:        "/ytdlp/info",
			Summary:     "Get media info without downloading",
			Tags:        []string{"YT-DLP"},
			Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
			Middlewares:  huma.Middlewares{authMw},
		}, ytdlpHandler.Info)

		huma.Register(api, huma.Operation{
			OperationID: "ytdlp-list-jobs",
			Method:      http.MethodGet,
			Path:        "/ytdlp/jobs",
			Summary:     "List yt-dlp jobs",
			Tags:        []string{"YT-DLP"},
			Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
			Middlewares:  huma.Middlewares{authMw},
		}, ytdlpHandler.ListJobs)

		huma.Register(api, huma.Operation{
			OperationID: "ytdlp-get-job",
			Method:      http.MethodGet,
			Path:        "/ytdlp/jobs/{id}",
			Summary:     "Get yt-dlp job status",
			Tags:        []string{"YT-DLP"},
			Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
			Middlewares:  huma.Middlewares{authMw},
		}, ytdlpHandler.GetJob)

		huma.Register(api, huma.Operation{
			OperationID: "ytdlp-get-job-files",
			Method:      http.MethodGet,
			Path:        "/ytdlp/jobs/{id}/files",
			Summary:     "Get yt-dlp job files",
			Tags:        []string{"YT-DLP"},
			Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
			Middlewares:  huma.Middlewares{authMw},
		}, ytdlpHandler.GetJobFiles)

		huma.Register(api, huma.Operation{
			OperationID: "ytdlp-delete-job",
			Method:      http.MethodDelete,
			Path:        "/ytdlp/jobs/{id}",
			Summary:     "Cancel/delete yt-dlp job",
			Tags:        []string{"YT-DLP"},
			Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
			Middlewares:  huma.Middlewares{authMw},
		}, ytdlpHandler.DeleteJob)
	}

	downloadsHandler := handlers.NewDownloadsHandler(cfg.Svc)
	huma.Register(api, huma.Operation{
		OperationID: "downloads-list",
		Method:      http.MethodGet,
		Path:        "/downloads",
		Summary:     "List all downloads",
		Tags:        []string{"Downloads"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw},
	}, downloadsHandler.List)

	huma.Register(api, huma.Operation{
		OperationID: "downloads-get",
		Method:      http.MethodGet,
		Path:        "/downloads/{id}",
		Summary:     "Get download status",
		Tags:        []string{"Downloads"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw},
	}, downloadsHandler.Get)

	filesHandler := handlers.NewFilesHandler(cfg.Svc, cfg.DB, cfg.LinkExpiry, cfg.FileBaseURL)
	huma.Register(api, huma.Operation{
		OperationID: "files-browse",
		Method:      http.MethodGet,
		Path:        "/files/jobs/{id}/browse",
		Summary:     "Browse job files",
		Tags:        []string{"Files"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw},
	}, filesHandler.Browse)

	huma.Register(api, huma.Operation{
		OperationID: "files-generate-link",
		Method:      http.MethodPost,
		Path:        "/files/jobs/{id}/link",
		Summary:     "Generate download link",
		Tags:        []string{"Files"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw},
	}, filesHandler.GenerateLink)

	nodesHandler := handlers.NewNodesHandler(cfg.DB)
	huma.Register(api, huma.Operation{
		OperationID: "admin-nodes-list",
		Method:      http.MethodGet,
		Path:        "/admin/nodes",
		Summary:     "List all nodes",
		Tags:        []string{"Admin - Nodes"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw, adminMw},
	}, nodesHandler.List)

	huma.Register(api, huma.Operation{
		OperationID: "admin-nodes-get",
		Method:      http.MethodGet,
		Path:        "/admin/nodes/{id}",
		Summary:     "Get node details",
		Tags:        []string{"Admin - Nodes"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw, adminMw},
	}, nodesHandler.Get)

	huma.Register(api, huma.Operation{
		OperationID: "admin-nodes-delete",
		Method:      http.MethodDelete,
		Path:        "/admin/nodes/{id}",
		Summary:     "Delete a node",
		Tags:        []string{"Admin - Nodes"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw, adminMw},
	}, nodesHandler.Delete)

	usersHandler := handlers.NewUsersHandler(cfg.DB)
	huma.Register(api, huma.Operation{
		OperationID: "admin-users-list",
		Method:      http.MethodGet,
		Path:        "/admin/users",
		Summary:     "List all users",
		Tags:        []string{"Admin - Users"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw, adminMw},
	}, usersHandler.List)

	huma.Register(api, huma.Operation{
		OperationID: "admin-users-delete",
		Method:      http.MethodDelete,
		Path:        "/admin/users/{id}",
		Summary:     "Delete a user",
		Tags:        []string{"Admin - Users"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw, adminMw},
	}, usersHandler.Delete)

	settingsHandler := handlers.NewSettingsHandler(cfg.DB)
	huma.Register(api, huma.Operation{
		OperationID: "admin-settings-list",
		Method:      http.MethodGet,
		Path:        "/admin/settings",
		Summary:     "List all settings",
		Tags:        []string{"Admin - Settings"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw, adminMw},
	}, settingsHandler.List)

	huma.Register(api, huma.Operation{
		OperationID: "admin-settings-get",
		Method:      http.MethodGet,
		Path:        "/admin/settings/{key}",
		Summary:     "Get a setting",
		Tags:        []string{"Admin - Settings"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw, adminMw},
	}, settingsHandler.Get)

	huma.Register(api, huma.Operation{
		OperationID: "admin-settings-update",
		Method:      http.MethodPatch,
		Path:        "/admin/settings/{key}",
		Summary:     "Update a setting",
		Tags:        []string{"Admin - Settings"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw, adminMw},
	}, settingsHandler.Update)

	cacheHandler := handlers.NewCacheHandler(cfg.DB)
	huma.Register(api, huma.Operation{
		OperationID: "admin-cache-lru",
		Method:      http.MethodGet,
		Path:        "/admin/cache/lru",
		Summary:     "List cache entries by LRU",
		Tags:        []string{"Admin - Cache"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw, adminMw},
	}, cacheHandler.ListLRU)

	huma.Register(api, huma.Operation{
		OperationID: "admin-cache-cleanup",
		Method:      http.MethodPost,
		Path:        "/admin/cache/cleanup",
		Summary:     "Delete least recently used cache entries",
		Tags:        []string{"Admin - Cache"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:  huma.Middlewares{authMw, adminMw},
	}, cacheHandler.Cleanup)
}
