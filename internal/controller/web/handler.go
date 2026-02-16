package web

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/opendebrid/opendebrid/internal/controller/api/middleware"
	"github.com/opendebrid/opendebrid/internal/core/engine"
	"github.com/opendebrid/opendebrid/internal/core/fileserver"
	"github.com/opendebrid/opendebrid/internal/core/service"
	"github.com/opendebrid/opendebrid/internal/database/gen"
)

//go:embed templates
var templateFS embed.FS

type Handler struct {
	templates *template.Template
	svc       *service.DownloadService
	queries   *gen.Queries
	signer    *fileserver.Signer
	engines   *engine.Registry
	nodeID    string
}

func NewHandler(db *pgxpool.Pool, svc *service.DownloadService, signer *fileserver.Signer, registry *engine.Registry, nodeID string) *Handler {
	tmpl := template.Must(template.New("").ParseFS(templateFS,
		"templates/layouts/*.html",
		"templates/pages/*.html",
		"templates/pages/admin/*.html",
		"templates/partials/*.html",
	))

	return &Handler{
		templates: tmpl,
		svc:       svc,
		queries:   gen.New(db),
		signer:    signer,
		engines:   registry,
		nodeID:    nodeID,
	}
}

func (h *Handler) RegisterRoutes(e *echo.Echo) {
	// Page routes (serve full HTML pages)
	e.GET("/web/login", h.loginPage)
	e.GET("/web/", h.dashboardPage)
	e.GET("/web/aria2", h.aria2Page)
	e.GET("/web/ytdlp", h.ytdlpPage)
	e.GET("/web/files/:id", h.filesPage)
	e.GET("/web/admin/nodes", h.nodesPage)
	e.GET("/web/admin/users", h.usersPage)
	e.GET("/web/admin/settings", h.settingsPage)

	// HTMX API routes (return HTML fragments)
	api := e.Group("/web/api")
	authMw := middleware.Auth(h.getJWTSecret(), nil)
	_ = authMw // Web API uses cookie/token from localStorage via HTMX headers
	api.GET("/downloads/recent", h.recentDownloads)
	api.GET("/stats/active", h.activeStats)
	api.GET("/:engine/jobs", h.jobsList)
	api.GET("/files/:id", h.filesList)
	api.GET("/admin/nodes", h.nodesList)
	api.GET("/admin/users", h.usersList)
	api.GET("/admin/settings", h.settingsList)
}

func (h *Handler) getJWTSecret() string { return "" } // Handled by API middleware

type pageData struct {
	Title      string
	IsAdmin    bool
	Engines    []string
	NodeID     string
	EngineName string
	EngineKey  string
	Placeholder string
	JobID      string
}

func (h *Handler) render(c echo.Context, page string, data pageData) error {
	data.Engines = h.engines.List()
	data.NodeID = h.nodeID
	c.Response().Header().Set("Content-Type", "text/html")
	return h.templates.ExecuteTemplate(c.Response(), page, data)
}

func (h *Handler) loginPage(c echo.Context) error {
	return h.render(c, "base.html", pageData{Title: "Login"})
}

func (h *Handler) dashboardPage(c echo.Context) error {
	return h.render(c, "base.html", pageData{Title: "Dashboard"})
}

func (h *Handler) aria2Page(c echo.Context) error {
	return h.render(c, "base.html", pageData{
		Title:       "aria2 Downloads",
		EngineName:  "aria2",
		EngineKey:   "aria2",
		Placeholder: "magnet:?xt=... or https://...",
	})
}

func (h *Handler) ytdlpPage(c echo.Context) error {
	return h.render(c, "base.html", pageData{
		Title:       "yt-dlp Downloads",
		EngineName:  "yt-dlp",
		EngineKey:   "ytdlp",
		Placeholder: "https://youtube.com/watch?v=...",
	})
}

func (h *Handler) filesPage(c echo.Context) error {
	return h.render(c, "base.html", pageData{
		Title: "Files",
		JobID: c.Param("id"),
	})
}

func (h *Handler) nodesPage(c echo.Context) error {
	return h.render(c, "base.html", pageData{Title: "Nodes", IsAdmin: true})
}

func (h *Handler) usersPage(c echo.Context) error {
	return h.render(c, "base.html", pageData{Title: "Users", IsAdmin: true})
}

func (h *Handler) settingsPage(c echo.Context) error {
	return h.render(c, "base.html", pageData{Title: "Settings", IsAdmin: true})
}

// HTMX partial responses

func (h *Handler) recentDownloads(c echo.Context) error {
	// Return a simple HTML table - in production this would check auth
	return c.HTML(http.StatusOK, `<table role="grid">
		<thead><tr><th>URL</th><th>Engine</th><th>Status</th><th>Actions</th></tr></thead>
		<tbody><tr><td colspan="4"><small>Add downloads from the aria2 or yt-dlp pages</small></td></tr></tbody>
	</table>`)
}

func (h *Handler) activeStats(c echo.Context) error {
	return c.HTML(http.StatusOK, "<strong>0</strong> active")
}

func (h *Handler) jobsList(c echo.Context) error {
	engineName := c.Param("engine")
	_ = engineName
	return c.HTML(http.StatusOK, `<table role="grid">
		<thead><tr><th>ID</th><th>URL</th><th>Status</th><th>Progress</th><th>Actions</th></tr></thead>
		<tbody><tr><td colspan="5"><small>No jobs yet. Add a download above.</small></td></tr></tbody>
	</table>`)
}

func (h *Handler) filesList(c echo.Context) error {
	jobID := c.Param("id")
	_ = jobID
	return c.HTML(http.StatusOK, `<table role="grid">
		<thead><tr><th>File</th><th>Size</th><th>Download</th></tr></thead>
		<tbody><tr><td colspan="3">No files found</td></tr></tbody>
	</table>`)
}

func (h *Handler) nodesList(c echo.Context) error {
	nodes, err := h.queries.ListNodes(c.Request().Context())
	if err != nil {
		return c.HTML(http.StatusOK, "<p>Error loading nodes</p>")
	}

	var sb strings.Builder
	sb.WriteString(`<table role="grid"><thead><tr><th>ID</th><th>Name</th><th>Status</th><th>Engines</th><th>Disk</th></tr></thead><tbody>`)
	for _, n := range nodes {
		status := "Offline"
		statusClass := "status-failed"
		if n.IsOnline {
			status = "Online"
			statusClass = "status-completed"
		}
		sb.WriteString(fmt.Sprintf(`<tr><td>%s</td><td>%s</td><td><span class="%s">%s</span></td><td>%s</td><td>%s</td></tr>`,
			n.ID, n.Name, statusClass, status, string(n.Engines), formatBytes(n.DiskAvailable)))
	}
	sb.WriteString(`</tbody></table>`)
	return c.HTML(http.StatusOK, sb.String())
}

func (h *Handler) usersList(c echo.Context) error {
	users, err := h.queries.ListUsers(c.Request().Context(), gen.ListUsersParams{Limit: 50, Offset: 0})
	if err != nil {
		return c.HTML(http.StatusOK, "<p>Error loading users</p>")
	}

	var sb strings.Builder
	sb.WriteString(`<table role="grid"><thead><tr><th>Username</th><th>Role</th><th>Active</th></tr></thead><tbody>`)
	for _, u := range users {
		active := "Yes"
		if !u.IsActive {
			active = "No"
		}
		sb.WriteString(fmt.Sprintf(`<tr><td>%s</td><td>%s</td><td>%s</td></tr>`,
			u.Username, u.Role, active))
	}
	sb.WriteString(`</tbody></table>`)
	return c.HTML(http.StatusOK, sb.String())
}

func (h *Handler) settingsList(c echo.Context) error {
	settings, err := h.queries.ListSettings(c.Request().Context())
	if err != nil {
		return c.HTML(http.StatusOK, "<p>Error loading settings</p>")
	}

	var sb strings.Builder
	sb.WriteString(`<table role="grid"><thead><tr><th>Key</th><th>Value</th><th>Description</th></tr></thead><tbody>`)
	for _, s := range settings {
		val := string(s.Value)
		if len(val) > 50 {
			val = val[:50] + "..."
		}
		sb.WriteString(fmt.Sprintf(`<tr><td>%s</td><td><code>%s</code></td><td>%s</td></tr>`,
			s.Key, val, s.Description.String))
	}
	sb.WriteString(`</tbody></table>`)
	return c.HTML(http.StatusOK, sb.String())
}

func formatBytes(b int64) string {
	if b == 0 {
		return "N/A"
	}
	const unit = 1024
	if b < unit {
		return strconv.FormatInt(b, 10) + " B"
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
