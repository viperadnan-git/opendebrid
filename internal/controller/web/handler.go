package web

import (
	"embed"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"crypto/rand"
	"encoding/hex"
	"path/filepath"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"golang.org/x/crypto/bcrypt"
	"github.com/opendebrid/opendebrid/internal/core/engine"
	"github.com/opendebrid/opendebrid/internal/core/node"
	"github.com/opendebrid/opendebrid/internal/core/service"
	"github.com/opendebrid/opendebrid/internal/database/gen"
)

//go:embed templates
var templateFS embed.FS

const sessionCookie = "od_session"

type Handler struct {
	templates   map[string]*template.Template
	svc         *service.DownloadService
	queries     *gen.Queries
	engines     *engine.Registry
	nodeClients map[string]node.NodeClient
	nodeID      string
	jwtSecret   string
	fileBaseURL string
}

func NewHandler(db *pgxpool.Pool, svc *service.DownloadService, registry *engine.Registry, nodeID, jwtSecret, fileBaseURL string, nodeClients map[string]node.NodeClient) *Handler {
	pages := []string{
		"templates/pages/login.html",
		"templates/pages/dashboard.html",
		"templates/pages/downloads.html",
		"templates/pages/files.html",
		"templates/pages/admin/nodes.html",
		"templates/pages/admin/users.html",
		"templates/pages/admin/settings.html",
	}

	templates := make(map[string]*template.Template)
	for _, page := range pages {
		t := template.Must(template.New("").ParseFS(templateFS,
			"templates/layouts/base.html",
			page,
		))
		// Extract a short key from path: "templates/pages/admin/users.html" -> "admin/users"
		key := strings.TrimPrefix(page, "templates/pages/")
		key = strings.TrimSuffix(key, ".html")
		templates[key] = t
	}

	return &Handler{
		templates:   templates,
		svc:         svc,
		queries:     gen.New(db),
		engines:     registry,
		nodeClients: nodeClients,
		nodeID:      nodeID,
		jwtSecret:   jwtSecret,
		fileBaseURL: fileBaseURL,
	}
}

func (h *Handler) RegisterRoutes(e *echo.Echo) {
	// Public routes
	e.GET("/login", h.loginPage)
	e.POST("/login", h.loginSubmit)
	e.GET("/logout", h.logout)

	// Authenticated page routes
	auth := e.Group("", h.requireAuth)
	auth.GET("/", h.dashboardPage)
	auth.GET("/downloads", h.downloadsPage)
	auth.GET("/files/:id", h.filesPage)
	auth.GET("/admin/nodes", h.nodesPage)
	auth.GET("/admin/users", h.usersPage)
	auth.GET("/admin/settings", h.settingsPage)

	// Authenticated action routes
	auth.DELETE("/jobs/:id", h.deleteJob)

	// Authenticated HTMX partial routes
	htmx := e.Group("/htmx", h.requireAuth)
	htmx.GET("/downloads/recent", h.recentDownloads)
	htmx.GET("/stats/active", h.activeStats)
	htmx.GET("/jobs", h.jobsList)
	htmx.GET("/files/:id", h.filesList)
	htmx.GET("/admin/nodes", h.nodesList)
	htmx.GET("/admin/users", h.usersList)
	htmx.GET("/admin/settings", h.settingsList)
}

// requireAuth validates the session cookie and injects user info into context.
func (h *Handler) requireAuth(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		cookie, err := c.Cookie(sessionCookie)
		if err != nil || cookie.Value == "" {
			return c.Redirect(http.StatusFound, "/login")
		}

		token, err := jwt.Parse(cookie.Value, func(t *jwt.Token) (any, error) {
			return []byte(h.jwtSecret), nil
		})
		if err != nil || !token.Valid {
			return c.Redirect(http.StatusFound, "/login")
		}

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			return c.Redirect(http.StatusFound, "/login")
		}

		c.Set("user_id", claims["sub"])
		c.Set("user_role", claims["role"])
		return next(c)
	}
}

// loginSubmit handles the login form POST, sets the session cookie.
func (h *Handler) loginSubmit(c echo.Context) error {
	username := c.FormValue("username")
	password := c.FormValue("password")

	user, err := h.queries.GetUserByUsername(c.Request().Context(), username)
	if err != nil {
		return h.render(c, "login", pageData{Title: "Login", Error: "Invalid username or password"})
	}

	// Verify password using bcrypt
	if err := bcryptCompare(user.Password, password); err != nil {
		return h.render(c, "login", pageData{Title: "Login", Error: "Invalid username or password"})
	}

	if !user.IsActive {
		return h.render(c, "login", pageData{Title: "Login", Error: "Account is disabled"})
	}

	// Generate JWT
	uid := pgUUIDToString(user.ID)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  uid,
		"role": user.Role,
	})
	tokenStr, err := token.SignedString([]byte(h.jwtSecret))
	if err != nil {
		return h.render(c, "login", pageData{Title: "Login", Error: "Internal error"})
	}

	c.SetCookie(&http.Cookie{
		Name:     sessionCookie,
		Value:    tokenStr,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	return c.Redirect(http.StatusFound, "/")
}

func (h *Handler) logout(c echo.Context) error {
	c.SetCookie(&http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	return c.Redirect(http.StatusFound, "/login")
}

type pageData struct {
	Title   string
	IsAdmin bool
	Engines []string
	NodeID  string
	JobID   string
	Error   string
	APIKey  string
}

func (h *Handler) render(c echo.Context, page string, data pageData) error {
	data.Engines = h.engines.List()
	data.NodeID = h.nodeID
	if role, ok := c.Get("user_role").(string); ok && role == "admin" {
		data.IsAdmin = true
	}
	t, ok := h.templates[page]
	if !ok {
		return echo.NewHTTPError(http.StatusInternalServerError, "template not found: "+page)
	}
	c.Response().Header().Set("Content-Type", "text/html")
	return t.ExecuteTemplate(c.Response(), "base.html", data)
}

func (h *Handler) loginPage(c echo.Context) error {
	return h.render(c, "login", pageData{Title: "Login"})
}

func (h *Handler) dashboardPage(c echo.Context) error {
	return h.render(c, "dashboard", pageData{Title: "Dashboard"})
}

func (h *Handler) downloadsPage(c echo.Context) error {
	return h.render(c, "downloads", pageData{Title: "Downloads"})
}

func (h *Handler) filesPage(c echo.Context) error {
	return h.render(c, "files", pageData{
		Title: "Files",
		JobID: c.Param("id"),
	})
}

func (h *Handler) nodesPage(c echo.Context) error {
	return h.render(c, "admin/nodes", pageData{Title: "Nodes"})
}

func (h *Handler) usersPage(c echo.Context) error {
	return h.render(c, "admin/users", pageData{Title: "Users"})
}

func (h *Handler) settingsPage(c echo.Context) error {
	return h.render(c, "admin/settings", pageData{Title: "Settings"})
}

func (h *Handler) deleteJob(c echo.Context) error {
	jobID := c.Param("id")
	job, err := h.queries.GetJob(c.Request().Context(), strToUUID(jobID))
	if err != nil {
		return c.HTML(http.StatusOK, `<p>Job not found</p>`)
	}

	// Cancel if active
	if job.Status == "active" || job.Status == "queued" {
		if client, ok := h.nodeClients[job.NodeID]; ok && job.EngineJobID.Valid {
			client.CancelJob(c.Request().Context(), jobID, job.EngineJobID.String)
		}
	}

	// Delete from DB
	h.queries.DeleteJob(c.Request().Context(), strToUUID(jobID))

	// Return empty response for HTMX (row removed)
	return c.HTML(http.StatusOK, "")
}

// HTMX partial responses

func (h *Handler) recentDownloads(c echo.Context) error {
	userID, _ := c.Get("user_id").(string)
	jobs, err := h.queries.ListJobsByUser(c.Request().Context(), gen.ListJobsByUserParams{
		UserID: strToUUID(userID),
		Limit:  20,
		Offset: 0,
	})
	if err != nil {
		return c.HTML(http.StatusOK, "<p>Error loading downloads</p>")
	}

	var sb strings.Builder
	sb.WriteString(`<table role="grid"><thead><tr><th>URL</th><th>Engine</th><th>Status</th><th>Actions</th></tr></thead><tbody>`)
	if len(jobs) == 0 {
		sb.WriteString(`<tr><td colspan="4"><small>Add downloads from the aria2 or yt-dlp pages</small></td></tr>`)
	}
	for _, j := range jobs {
		statusClass := statusToClass(j.Status)
		shortURL := truncate(j.Url, 60)
		jobID := pgUUIDToString(j.ID)
		actions := fmt.Sprintf(`<a href="/files/%s">Files</a> <button class="outline secondary" style="padding:0.2rem 0.5rem;margin:0" hx-delete="/jobs/%s" hx-target="closest tr" hx-swap="outerHTML" hx-confirm="Delete this download?">Delete</button>`, jobID, jobID)
		sb.WriteString(fmt.Sprintf(`<tr><td title="%s">%s</td><td>%s</td><td><span class="%s">%s</span></td><td>%s</td></tr>`,
			html.EscapeString(j.Url), html.EscapeString(shortURL), j.Engine, statusClass, j.Status, actions))
	}
	sb.WriteString(`</tbody></table>`)
	return c.HTML(http.StatusOK, sb.String())
}

func (h *Handler) activeStats(c echo.Context) error {
	userID, _ := c.Get("user_id").(string)
	count, err := h.queries.CountActiveJobsByUser(c.Request().Context(), strToUUID(userID))
	if err != nil {
		count = 0
	}
	return c.HTML(http.StatusOK, fmt.Sprintf("<strong>%d</strong> active", count))
}

func (h *Handler) jobsList(c echo.Context) error {
	userID, _ := c.Get("user_id").(string)
	engineFilter := c.QueryParam("engine")

	var jobs []gen.Job
	var err error

	if engineFilter != "" {
		jobs, err = h.queries.ListJobsByUserAndEngine(c.Request().Context(), gen.ListJobsByUserAndEngineParams{
			UserID: strToUUID(userID),
			Engine: engineFilter,
			Limit:  50,
			Offset: 0,
		})
	} else {
		jobs, err = h.queries.ListJobsByUser(c.Request().Context(), gen.ListJobsByUserParams{
			UserID: strToUUID(userID),
			Limit:  50,
			Offset: 0,
		})
	}
	if err != nil {
		return c.HTML(http.StatusOK, "<p>Error loading jobs</p>")
	}

	showEngine := engineFilter == ""
	cols := "4"
	if showEngine {
		cols = "5"
	}

	var sb strings.Builder
	sb.WriteString(`<table role="grid"><thead><tr><th>URL</th>`)
	if showEngine {
		sb.WriteString(`<th>Engine</th>`)
	}
	sb.WriteString(`<th>Status</th><th>Progress</th><th>Actions</th></tr></thead><tbody>`)
	if len(jobs) == 0 {
		sb.WriteString(fmt.Sprintf(`<tr><td colspan="%s"><small>No jobs yet. Add a download above.</small></td></tr>`, cols))
	}
	for _, j := range jobs {
		jobID := pgUUIDToString(j.ID)
		statusClass := statusToClass(j.Status)
		shortURL := truncate(j.Url, 60)

		progress := ""
		if j.Status == "active" || j.Status == "queued" {
			if status, err := h.svc.StatusByID(c.Request().Context(), jobID); err == nil {
				progress = fmt.Sprintf("%.1f%% (%s/s)", status.Progress*100, formatBytes(status.Speed))
				if status.State == engine.StateCompleted {
					j.Status = "completed"
				}
			}
		}
		if j.Status == "completed" {
			progress = "Done"
		} else if j.Status == "failed" {
			progress = j.ErrorMessage.String
		}

		engineCol := ""
		if showEngine {
			engineCol = fmt.Sprintf(`<td>%s</td>`, j.Engine)
		}

		actions := fmt.Sprintf(`<a href="/files/%s">Files</a> <button class="outline secondary" style="padding:0.2rem 0.5rem;margin:0" hx-delete="/jobs/%s" hx-target="closest tr" hx-swap="outerHTML" hx-confirm="Delete this download?">Delete</button>`, jobID, jobID)
		sb.WriteString(fmt.Sprintf(`<tr><td title="%s">%s</td>%s<td><span class="%s">%s</span></td><td>%s</td><td>%s</td></tr>`,
			html.EscapeString(j.Url), html.EscapeString(shortURL), engineCol, statusClass, j.Status, html.EscapeString(progress), actions))
	}
	sb.WriteString(`</tbody></table>`)
	return c.HTML(http.StatusOK, sb.String())
}

func (h *Handler) filesList(c echo.Context) error {
	jobID := c.Param("id")

	// Sync status first (ensures GID is updated for followed torrents)
	h.svc.StatusByID(c.Request().Context(), jobID)

	job, err := h.queries.GetJob(c.Request().Context(), strToUUID(jobID))
	if err != nil {
		return c.HTML(http.StatusOK, `<p>Job not found</p>`)
	}

	// Get files from the node using the (now-synced) engine job ID
	var files []engine.FileInfo
	if client, ok := h.nodeClients[job.NodeID]; ok && job.EngineJobID.Valid {
		files, _ = client.GetJobFiles(c.Request().Context(), jobID, job.EngineJobID.String)
	}

	var sb strings.Builder
	sb.WriteString(`<table role="grid"><thead><tr><th>File</th><th>Size</th><th>Download</th></tr></thead><tbody>`)
	if len(files) == 0 {
		sb.WriteString(`<tr><td colspan="3">No files found</td></tr>`)
	}
	userID, _ := c.Get("user_id").(string)
	for _, f := range files {
		downloadLink := ""
		// StorageURI is like "file://./tmp/downloads/aria2/filename.iso"
		// Strip "file://" prefix to get the filesystem path for the download_links table
		storagePath := strings.TrimPrefix(f.StorageURI, "file://")
		token, err := generateToken()
		if err == nil {
			expiry := time.Now().Add(60 * time.Minute)
			link, err := h.queries.CreateDownloadLink(c.Request().Context(), gen.CreateDownloadLinkParams{
				UserID:    strToUUID(userID),
				JobID:     strToUUID(jobID),
				FilePath:  storagePath,
				Token:     token,
				ExpiresAt: pgtype.Timestamptz{Time: expiry, Valid: true},
			})
			if err == nil {
				filename := filepath.Base(f.Path)
				downloadLink = h.fileBaseURL + "/dl/" + link.Token + "/" + filename
			}
		}
		sb.WriteString(fmt.Sprintf(`<tr><td>%s</td><td>%s</td><td><a href="%s" target="_blank">Download</a></td></tr>`,
			html.EscapeString(f.Path), formatBytes(f.Size), html.EscapeString(downloadLink)))
	}
	sb.WriteString(`</tbody></table>`)
	return c.HTML(http.StatusOK, sb.String())
}

func (h *Handler) nodesList(c echo.Context) error {
	nodes, err := h.queries.ListNodes(c.Request().Context())
	if err != nil {
		return c.HTML(http.StatusOK, "<p>Error loading nodes</p>")
	}

	var sb strings.Builder
	sb.WriteString(`<table role="grid"><thead><tr><th>ID</th><th>Name</th><th>Status</th><th>Engines</th><th>Disk (Free / Total)</th></tr></thead><tbody>`)
	for _, n := range nodes {
		status := "Offline"
		statusClass := "status-failed"
		if n.IsOnline {
			status = "Online"
			statusClass = "status-completed"
		}
		disk := "N/A"
		if n.DiskTotal > 0 {
			disk = fmt.Sprintf("%s / %s", formatBytes(n.DiskAvailable), formatBytes(n.DiskTotal))
		}
		sb.WriteString(fmt.Sprintf(`<tr><td>%s</td><td>%s</td><td><span class="%s">%s</span></td><td>%s</td><td>%s</td></tr>`,
			n.ID, n.Name, statusClass, status, string(n.Engines), disk))
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

func bcryptCompare(hash, password string) error {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
}

func pgUUIDToString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func statusToClass(status string) string {
	switch status {
	case "active":
		return "status-active"
	case "completed":
		return "status-completed"
	case "failed":
		return "status-failed"
	case "queued":
		return "status-queued"
	default:
		return ""
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func strToUUID(s string) pgtype.UUID {
	var u pgtype.UUID
	cleaned := strings.ReplaceAll(s, "-", "")
	if len(cleaned) != 32 {
		return u
	}
	for i := 0; i < 16; i++ {
		u.Bytes[i] = hexVal(cleaned[i*2])<<4 | hexVal(cleaned[i*2+1])
	}
	u.Valid = true
	return u
}

func hexVal(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	}
	return 0
}

func generateToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
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
