package web

import (
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/mail"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
	"golang.org/x/crypto/bcrypt"
)

//go:embed templates
var templateFS embed.FS

const sessionCookie = "od_session"

type Handler struct {
	templates map[string]*template.Template
	queries   *gen.Queries
	jwtSecret string
	engines   []string
	nodeID    string
}

func NewHandler(db *pgxpool.Pool, jwtSecret string, engines []string, nodeID string) *Handler {
	pages := []string{
		"templates/pages/login.html",
		"templates/pages/signup.html",
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
			"templates/partials/nav.html",
			page,
		))
		key := strings.TrimPrefix(page, "templates/pages/")
		key = strings.TrimSuffix(key, ".html")
		templates[key] = t
	}

	return &Handler{
		templates: templates,
		queries:   gen.New(db),
		jwtSecret: jwtSecret,
		engines:   engines,
		nodeID:    nodeID,
	}
}

func (h *Handler) RegisterRoutes(e *echo.Echo) {
	e.GET("/login", h.loginPage)
	e.POST("/login", h.loginSubmit)
	e.GET("/signup", h.signupPage)
	e.POST("/signup", h.signupSubmit)
	e.GET("/logout", h.logout)

	auth := e.Group("", h.requireAuth)
	auth.GET("/", h.dashboardPage)
	auth.GET("/downloads", h.downloadsPage)
	auth.GET("/files/:id", h.filesPage)
	auth.GET("/admin/nodes", h.nodesPage)
	auth.GET("/admin/users", h.usersPage)
	auth.GET("/admin/settings", h.settingsPage)
}

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

func (h *Handler) loginSubmit(c echo.Context) error {
	username := c.FormValue("username")
	password := c.FormValue("password")

	user, err := h.queries.GetUserByUsername(c.Request().Context(), username)
	if err != nil {
		return h.render(c, "login", pageData{Title: "Login", Error: "Invalid username or password"})
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(password)); err != nil {
		return h.render(c, "login", pageData{Title: "Login", Error: "Invalid username or password"})
	}

	if !user.IsActive {
		return h.render(c, "login", pageData{Title: "Login", Error: "Account is disabled"})
	}

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
	Title         string
	IsAdmin       bool
	Engines       []string
	NodeID        string
	JobID         string
	Error         string
	SignupEnabled bool
}

func (h *Handler) render(c echo.Context, page string, data pageData) error {
	data.Engines = h.engines
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

func (h *Handler) registrationEnabled(c echo.Context) bool {
	s, err := h.queries.GetSetting(c.Request().Context(), "registration_enabled")
	if err != nil {
		return false
	}
	return s.Value == "true"
}

func (h *Handler) loginPage(c echo.Context) error {
	return h.render(c, "login", pageData{Title: "Login", SignupEnabled: h.registrationEnabled(c)})
}

func (h *Handler) signupPage(c echo.Context) error {
	return h.render(c, "signup", pageData{Title: "Sign Up", SignupEnabled: h.registrationEnabled(c)})
}

func (h *Handler) signupSubmit(c echo.Context) error {
	if !h.registrationEnabled(c) {
		return h.render(c, "signup", pageData{Title: "Sign Up", Error: "Registration is currently disabled"})
	}

	username := c.FormValue("username")
	password := c.FormValue("password")
	confirm := c.FormValue("confirm")
	email := c.FormValue("email")

	if username == "" || password == "" || email == "" {
		return h.render(c, "signup", pageData{Title: "Sign Up", SignupEnabled: true, Error: "All fields are required"})
	}

	if _, err := mail.ParseAddress(email); err != nil {
		return h.render(c, "signup", pageData{Title: "Sign Up", SignupEnabled: true, Error: "Invalid email address"})
	}

	if password != confirm {
		return h.render(c, "signup", pageData{Title: "Sign Up", SignupEnabled: true, Error: "Passwords do not match"})
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), 12)
	if err != nil {
		return h.render(c, "signup", pageData{Title: "Sign Up", SignupEnabled: true, Error: "Internal error"})
	}

	user, err := h.queries.CreateUser(c.Request().Context(), gen.CreateUserParams{
		Username: username,
		Email:    email,
		Password: string(hash),
		Role:     "user",
	})
	if err != nil {
		return h.render(c, "signup", pageData{Title: "Sign Up", SignupEnabled: true, Error: "Username already taken"})
	}

	// Auto-login: generate JWT and set session cookie
	uid := pgUUIDToString(user.ID)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"sub":  uid,
		"role": user.Role,
	})
	tokenStr, err := token.SignedString([]byte(h.jwtSecret))
	if err != nil {
		return h.render(c, "signup", pageData{Title: "Sign Up", SignupEnabled: true, Error: "Internal error"})
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

func (h *Handler) dashboardPage(c echo.Context) error {
	return h.render(c, "dashboard", pageData{Title: "Dashboard"})
}

func (h *Handler) downloadsPage(c echo.Context) error {
	return h.render(c, "downloads", pageData{Title: "Downloads"})
}

func (h *Handler) filesPage(c echo.Context) error {
	return h.render(c, "files", pageData{Title: "Files", JobID: c.Param("id")})
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

func pgUUIDToString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
