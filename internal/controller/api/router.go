package api

import (
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humaecho"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
	"github.com/viperadnan-git/opendebrid/internal/controller/api/handlers"
	"github.com/viperadnan-git/opendebrid/internal/controller/api/middleware"
	"github.com/viperadnan-git/opendebrid/internal/core/engine/ytdlp"
	"github.com/viperadnan-git/opendebrid/internal/core/service"
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
	handlers.InitErrors()

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
			Type:        "apiKey",
			In:          "header",
			Name:        "X-API-Key",
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
		OperationID: "login",
		Method:      http.MethodPost,
		Path:        "/auth/login",
		Summary:     "Login and get JWT token",
		Tags:        []string{"Auth"},
	}, authHandler.Login)

	huma.Register(api, huma.Operation{
		OperationID: "get-me",
		Method:      http.MethodGet,
		Path:        "/auth/me",
		Summary:     "Get current user info",
		Tags:        []string{"Auth"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares: huma.Middlewares{authMw},
	}, authHandler.Me)

	huma.Register(api, huma.Operation{
		OperationID: "regenerate-api-key",
		Method:      http.MethodPost,
		Path:        "/auth/apikey/regenerate",
		Summary:     "Regenerate API key",
		Tags:        []string{"Auth"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares: huma.Middlewares{authMw},
	}, authHandler.RegenerateAPIKey)

	statsHandler := handlers.NewStatsHandler(cfg.DB)
	huma.Register(api, huma.Operation{
		OperationID: "get-stats",
		Method:      http.MethodGet,
		Path:        "/stats",
		Summary:     "Get dashboard statistics",
		Tags:        []string{"Stats"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares: huma.Middlewares{authMw},
	}, statsHandler.Get)

	dlHandler := handlers.NewDownloadsHandler(cfg.Svc, cfg.DB, cfg.LinkExpiry, cfg.FileBaseURL)
	huma.Register(api, huma.Operation{
		OperationID:   "downloads-add",
		Method:        http.MethodPost,
		Path:          "/downloads",
		Summary:       "Add download",
		Tags:          []string{"Downloads"},
		Security:      []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares:   huma.Middlewares{authMw},
		DefaultStatus: http.StatusCreated,
	}, dlHandler.Add)

	huma.Register(api, huma.Operation{
		OperationID: "downloads-list",
		Method:      http.MethodGet,
		Path:        "/downloads",
		Summary:     "List downloads",
		Tags:        []string{"Downloads"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares: huma.Middlewares{authMw},
	}, dlHandler.List)

	huma.Register(api, huma.Operation{
		OperationID: "downloads-get",
		Method:      http.MethodGet,
		Path:        "/downloads/{id}",
		Summary:     "Get download status",
		Tags:        []string{"Downloads"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares: huma.Middlewares{authMw},
	}, dlHandler.Get)

	huma.Register(api, huma.Operation{
		OperationID: "downloads-files",
		Method:      http.MethodGet,
		Path:        "/downloads/{id}/files",
		Summary:     "Browse download files",
		Tags:        []string{"Downloads"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares: huma.Middlewares{authMw},
	}, dlHandler.Files)

	huma.Register(api, huma.Operation{
		OperationID: "downloads-generate-link",
		Method:      http.MethodPost,
		Path:        "/downloads/{id}/link",
		Summary:     "Generate download link",
		Tags:        []string{"Downloads"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares: huma.Middlewares{authMw},
	}, dlHandler.GenerateLink)

	huma.Register(api, huma.Operation{
		OperationID: "downloads-delete",
		Method:      http.MethodDelete,
		Path:        "/downloads/{id}",
		Summary:     "Delete download",
		Tags:        []string{"Downloads"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares: huma.Middlewares{authMw},
	}, dlHandler.Delete)

	if cfg.YtDlpEngine != nil {
		ytdlpHandler := handlers.NewYtDlpHandler(cfg.YtDlpEngine)
		huma.Register(api, huma.Operation{
			OperationID: "ytdlp-info",
			Method:      http.MethodPost,
			Path:        "/engine/ytdlp/info",
			Summary:     "Get media info without downloading",
			Tags:        []string{"Engine - YT-DLP"},
			Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
			Middlewares: huma.Middlewares{authMw},
		}, ytdlpHandler.Info)
	}

	nodesHandler := handlers.NewNodesHandler(cfg.DB)
	huma.Register(api, huma.Operation{
		OperationID: "admin-nodes-list",
		Method:      http.MethodGet,
		Path:        "/admin/nodes",
		Summary:     "List all nodes",
		Tags:        []string{"Admin - Nodes"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares: huma.Middlewares{authMw, adminMw},
	}, nodesHandler.List)

	huma.Register(api, huma.Operation{
		OperationID: "admin-nodes-get",
		Method:      http.MethodGet,
		Path:        "/admin/nodes/{id}",
		Summary:     "Get node details",
		Tags:        []string{"Admin - Nodes"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares: huma.Middlewares{authMw, adminMw},
	}, nodesHandler.Get)

	huma.Register(api, huma.Operation{
		OperationID: "admin-nodes-delete",
		Method:      http.MethodDelete,
		Path:        "/admin/nodes/{id}",
		Summary:     "Delete a node",
		Tags:        []string{"Admin - Nodes"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares: huma.Middlewares{authMw, adminMw},
	}, nodesHandler.Delete)

	usersHandler := handlers.NewUsersHandler(cfg.DB, cfg.Svc)
	huma.Register(api, huma.Operation{
		OperationID: "admin-users-list",
		Method:      http.MethodGet,
		Path:        "/admin/users",
		Summary:     "List all users",
		Tags:        []string{"Admin - Users"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares: huma.Middlewares{authMw, adminMw},
	}, usersHandler.List)

	huma.Register(api, huma.Operation{
		OperationID: "admin-users-delete",
		Method:      http.MethodDelete,
		Path:        "/admin/users/{id}",
		Summary:     "Delete a user",
		Tags:        []string{"Admin - Users"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares: huma.Middlewares{authMw, adminMw},
	}, usersHandler.Delete)

	settingsHandler := handlers.NewSettingsHandler(cfg.DB)
	huma.Register(api, huma.Operation{
		OperationID: "admin-settings-list",
		Method:      http.MethodGet,
		Path:        "/admin/settings",
		Summary:     "List all settings",
		Tags:        []string{"Admin - Settings"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares: huma.Middlewares{authMw, adminMw},
	}, settingsHandler.List)

	huma.Register(api, huma.Operation{
		OperationID: "admin-settings-get",
		Method:      http.MethodGet,
		Path:        "/admin/settings/{key}",
		Summary:     "Get a setting",
		Tags:        []string{"Admin - Settings"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares: huma.Middlewares{authMw, adminMw},
	}, settingsHandler.Get)

	huma.Register(api, huma.Operation{
		OperationID: "admin-settings-update",
		Method:      http.MethodPatch,
		Path:        "/admin/settings/{key}",
		Summary:     "Update a setting",
		Tags:        []string{"Admin - Settings"},
		Security:    []map[string][]string{{"BearerAuth": {}}, {"ApiKeyAuth": {}}},
		Middlewares: huma.Middlewares{authMw, adminMw},
	}, settingsHandler.Update)

}
