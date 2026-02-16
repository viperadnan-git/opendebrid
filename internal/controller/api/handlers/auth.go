package handlers

import (
	"net/http"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/opendebrid/opendebrid/internal/controller/api/middleware"
	"github.com/opendebrid/opendebrid/internal/controller/api/response"
	"github.com/opendebrid/opendebrid/internal/database/gen"
	"golang.org/x/crypto/bcrypt"
)

type AuthHandler struct {
	queries   *gen.Queries
	jwtSecret string
	jwtExpiry time.Duration
}

func NewAuthHandler(db *pgxpool.Pool, jwtSecret string, jwtExpiry time.Duration) *AuthHandler {
	return &AuthHandler{
		queries:   gen.New(db),
		jwtSecret: jwtSecret,
		jwtExpiry: jwtExpiry,
	}
}

func (h *AuthHandler) Register(c echo.Context) error {
	var req struct {
		Username string  `json:"username"`
		Password string  `json:"password"`
		Email    *string `json:"email"`
	}
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "INVALID_INPUT", "invalid request body")
	}
	if req.Username == "" || req.Password == "" {
		return response.Error(c, http.StatusBadRequest, "MISSING_FIELDS", "username and password required")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 12)
	if err != nil {
		return response.Error(c, http.StatusInternalServerError, "HASH_ERROR", "failed to hash password")
	}

	var email pgtype.Text
	if req.Email != nil {
		email = pgtype.Text{String: *req.Email, Valid: true}
	}

	user, err := h.queries.CreateUser(c.Request().Context(), gen.CreateUserParams{
		Username: req.Username,
		Email:    email,
		Password: string(hash),
		Role:     "user",
	})
	if err != nil {
		return response.Error(c, http.StatusConflict, "USER_EXISTS", "username already taken")
	}

	return response.Success(c, http.StatusCreated, map[string]any{
		"id":       pgUUIDToString(user.ID),
		"username": user.Username,
	})
}

func (h *AuthHandler) Login(c echo.Context) error {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.Bind(&req); err != nil {
		return response.Error(c, http.StatusBadRequest, "INVALID_INPUT", "invalid request body")
	}

	user, err := h.queries.GetUserByUsername(c.Request().Context(), req.Username)
	if err != nil {
		return response.Error(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "invalid username or password")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(req.Password)); err != nil {
		return response.Error(c, http.StatusUnauthorized, "INVALID_CREDENTIALS", "invalid username or password")
	}

	if !user.IsActive {
		return response.Error(c, http.StatusForbidden, "ACCOUNT_DISABLED", "account is disabled")
	}

	uid := pgUUIDToString(user.ID)
	token, err := h.generateJWT(uid, user.Role)
	if err != nil {
		return response.Error(c, http.StatusInternalServerError, "TOKEN_ERROR", "failed to generate token")
	}

	return response.Success(c, http.StatusOK, map[string]any{
		"token":      token,
		"expires_in": int(h.jwtExpiry.Seconds()),
		"user": map[string]any{
			"id":       uid,
			"username": user.Username,
			"role":     user.Role,
		},
	})
}

func (h *AuthHandler) Me(c echo.Context) error {
	userID := middleware.GetUserID(c.Request().Context())
	uid := pgUUID(userID)

	user, err := h.queries.GetUserByID(c.Request().Context(), uid)
	if err != nil {
		return response.Error(c, http.StatusNotFound, "USER_NOT_FOUND", "user not found")
	}

	return response.Success(c, http.StatusOK, map[string]any{
		"id":       pgUUIDToString(user.ID),
		"username": user.Username,
		"email":    user.Email.String,
		"role":     user.Role,
		"api_key":  pgUUIDToString(user.ApiKey),
	})
}

func (h *AuthHandler) RegenerateAPIKey(c echo.Context) error {
	userID := middleware.GetUserID(c.Request().Context())
	uid := pgUUID(userID)

	user, err := h.queries.RegenerateAPIKey(c.Request().Context(), uid)
	if err != nil {
		return response.Error(c, http.StatusInternalServerError, "REGEN_ERROR", "failed to regenerate API key")
	}

	return response.Success(c, http.StatusOK, map[string]any{
		"api_key": pgUUIDToString(user.ApiKey),
	})
}

func (h *AuthHandler) generateJWT(userID, role string) (string, error) {
	claims := jwt.MapClaims{
		"sub":  userID,
		"role": role,
		"iat":  time.Now().Unix(),
		"exp":  time.Now().Add(h.jwtExpiry).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(h.jwtSecret))
}
