package handlers

import (
	"context"
	"net/mail"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/viperadnan-git/opendebrid/internal/controller/api/middleware"
	"github.com/viperadnan-git/opendebrid/internal/core/util"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
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

// --- Input types ---

type RegisterInput struct {
	Body struct {
		Username string `json:"username" minLength:"1" doc:"Username"`
		Password string `json:"password" minLength:"1" doc:"Password"`
		Email    string `json:"email" minLength:"1" format:"email" doc:"Email address"`
	}
}

type LoginInput struct {
	Body struct {
		Username string `json:"username" minLength:"1" doc:"Username"`
		Password string `json:"password" minLength:"1" doc:"Password"`
	}
}

type EmptyInput struct{}

// --- DTO types ---

type RegisterDTO struct {
	ID       string `json:"id" doc:"User ID"`
	Username string `json:"username" doc:"Username"`
}

type LoginUserDTO struct {
	ID       string `json:"id" doc:"User ID"`
	Username string `json:"username" doc:"Username"`
	Role     string `json:"role" doc:"User role"`
}

type LoginDTO struct {
	Token     string       `json:"token" doc:"JWT token"`
	ExpiresIn int          `json:"expires_in" doc:"Token lifetime in seconds"`
	User      LoginUserDTO `json:"user" doc:"User info"`
}

type MeDTO struct {
	ID       string `json:"id" doc:"User ID"`
	Username string `json:"username" doc:"Username"`
	Email    string `json:"email" doc:"Email"`
	Role     string `json:"role" doc:"User role"`
	APIKey   string `json:"api_key" doc:"API key"`
}

type APIKeyDTO struct {
	APIKey string `json:"api_key" doc:"New API key"`
}

// --- Handlers ---

func (h *AuthHandler) Register(ctx context.Context, input *RegisterInput) (*DataOutput[RegisterDTO], error) {
	if _, err := mail.ParseAddress(input.Body.Email); err != nil {
		return nil, huma.Error422UnprocessableEntity("invalid email address")
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(input.Body.Password), 12)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to hash password")
	}

	user, err := h.queries.CreateUser(ctx, gen.CreateUserParams{
		Username: input.Body.Username,
		Email:    input.Body.Email,
		Password: string(hash),
		Role:     "user",
	})
	if err != nil {
		return nil, huma.Error409Conflict("username already taken")
	}

	return OK(RegisterDTO{
		ID:       util.UUIDToStr(user.ID),
		Username: user.Username,
	}), nil
}

func (h *AuthHandler) Login(ctx context.Context, input *LoginInput) (*DataOutput[LoginDTO], error) {
	user, err := h.queries.GetUserByUsername(ctx, input.Body.Username)
	if err != nil {
		return nil, huma.Error401Unauthorized("invalid username or password")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(input.Body.Password)); err != nil {
		return nil, huma.Error401Unauthorized("invalid username or password")
	}

	if !user.IsActive {
		return nil, huma.Error403Forbidden("account is disabled")
	}

	uid := util.UUIDToStr(user.ID)
	token, err := middleware.GenerateJWT(uid, user.Role, h.jwtSecret, h.jwtExpiry)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to generate token")
	}

	return OK(LoginDTO{
		Token:     token,
		ExpiresIn: int(h.jwtExpiry.Seconds()),
		User:      LoginUserDTO{ID: uid, Username: user.Username, Role: user.Role},
	}), nil
}

func (h *AuthHandler) Me(ctx context.Context, _ *EmptyInput) (*DataOutput[MeDTO], error) {
	userID := middleware.GetUserID(ctx)
	uid := util.TextToUUID(userID)

	user, err := h.queries.GetUserByID(ctx, uid)
	if err != nil {
		return nil, huma.Error404NotFound("user not found")
	}

	return OK(MeDTO{
		ID:       util.UUIDToStr(user.ID),
		Username: user.Username,
		Email:    user.Email,
		Role:     user.Role,
		APIKey:   util.UUIDToStr(user.ApiKey),
	}), nil
}

func (h *AuthHandler) RegenerateAPIKey(ctx context.Context, _ *EmptyInput) (*DataOutput[APIKeyDTO], error) {
	userID := middleware.GetUserID(ctx)
	uid := util.TextToUUID(userID)

	user, err := h.queries.RegenerateAPIKey(ctx, uid)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to regenerate API key")
	}

	return OK(APIKeyDTO{APIKey: util.UUIDToStr(user.ApiKey)}), nil
}
