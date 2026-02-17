package handlers

import (
	"context"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/viperadnan-git/opendebrid/internal/controller/api/middleware"
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

type RegisterInput struct {
	Body struct {
		Username string  `json:"username" minLength:"1" doc:"Username"`
		Password string  `json:"password" minLength:"1" doc:"Password"`
		Email    *string `json:"email,omitempty" format:"email" doc:"Optional email"`
	}
}

type RegisterBody struct {
	ID       string `json:"id" doc:"User ID"`
	Username string `json:"username" doc:"Username"`
}

type RegisterOutput struct {
	Body RegisterBody
}

func (h *AuthHandler) Register(ctx context.Context, input *RegisterInput) (*RegisterOutput, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Body.Password), 12)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to hash password")
	}

	var email pgtype.Text
	if input.Body.Email != nil {
		email = pgtype.Text{String: *input.Body.Email, Valid: true}
	}

	user, err := h.queries.CreateUser(ctx, gen.CreateUserParams{
		Username: input.Body.Username,
		Email:    email,
		Password: string(hash),
		Role:     "user",
	})
	if err != nil {
		return nil, huma.Error409Conflict("username already taken")
	}

	return &RegisterOutput{Body: RegisterBody{
		ID:       pgUUIDToString(user.ID),
		Username: user.Username,
	}}, nil
}

type LoginInput struct {
	Body struct {
		Username string `json:"username" minLength:"1" doc:"Username"`
		Password string `json:"password" minLength:"1" doc:"Password"`
	}
}

type LoginUser struct {
	ID       string `json:"id" doc:"User ID"`
	Username string `json:"username" doc:"Username"`
	Role     string `json:"role" doc:"User role"`
}

type LoginBody struct {
	Token     string    `json:"token" doc:"JWT token"`
	ExpiresIn int       `json:"expires_in" doc:"Token lifetime in seconds"`
	User      LoginUser `json:"user" doc:"User info"`
}

type LoginOutput struct {
	Body LoginBody
}

func (h *AuthHandler) Login(ctx context.Context, input *LoginInput) (*LoginOutput, error) {
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

	uid := pgUUIDToString(user.ID)
	token, err := h.generateJWT(uid, user.Role)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to generate token")
	}

	return &LoginOutput{Body: LoginBody{
		Token:     token,
		ExpiresIn: int(h.jwtExpiry.Seconds()),
		User:      LoginUser{ID: uid, Username: user.Username, Role: user.Role},
	}}, nil
}

type EmptyInput struct{}

type MeBody struct {
	ID       string `json:"id" doc:"User ID"`
	Username string `json:"username" doc:"Username"`
	Email    string `json:"email" doc:"Email"`
	Role     string `json:"role" doc:"User role"`
	APIKey   string `json:"api_key" doc:"API key"`
}

type MeOutput struct {
	Body MeBody
}

func (h *AuthHandler) Me(ctx context.Context, input *EmptyInput) (*MeOutput, error) {
	userID := middleware.GetUserID(ctx)
	uid := pgUUID(userID)

	user, err := h.queries.GetUserByID(ctx, uid)
	if err != nil {
		return nil, huma.Error404NotFound("user not found")
	}

	return &MeOutput{Body: MeBody{
		ID:       pgUUIDToString(user.ID),
		Username: user.Username,
		Email:    user.Email.String,
		Role:     user.Role,
		APIKey:   pgUUIDToString(user.ApiKey),
	}}, nil
}

type APIKeyBody struct {
	APIKey string `json:"api_key" doc:"New API key"`
}

type APIKeyOutput struct {
	Body APIKeyBody
}

func (h *AuthHandler) RegenerateAPIKey(ctx context.Context, input *EmptyInput) (*APIKeyOutput, error) {
	userID := middleware.GetUserID(ctx)
	uid := pgUUID(userID)

	user, err := h.queries.RegenerateAPIKey(ctx, uid)
	if err != nil {
		return nil, huma.Error500InternalServerError("failed to regenerate API key")
	}

	return &APIKeyOutput{Body: APIKeyBody{
		APIKey: pgUUIDToString(user.ApiKey),
	}}, nil
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
