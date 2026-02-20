package middleware

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humaecho"
	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
	"github.com/viperadnan-git/opendebrid/internal/core/util"
	"github.com/viperadnan-git/opendebrid/internal/database/gen"
)

type contextKey string

const (
	UserIDKey   contextKey = "user_id"
	UserRoleKey contextKey = "user_role"
)

func GetUserID(ctx context.Context) string {
	v, _ := ctx.Value(UserIDKey).(string)
	return v
}

func GetUserRole(ctx context.Context) string {
	v, _ := ctx.Value(UserRoleKey).(string)
	return v
}

func Auth(jwtSecret string, db *pgxpool.Pool) func(ctx huma.Context, next func(huma.Context)) {
	queries := gen.New(db)

	return func(ctx huma.Context, next func(huma.Context)) {
		echoCtx := humaecho.Unwrap(ctx)
		auth := ctx.Header("Authorization")
		log.Trace().Str("method", ctx.Method()).Str("path", ctx.URL().Path).Bool("has_bearer", strings.HasPrefix(auth, "Bearer ")).Bool("has_api_key", ctx.Header("X-API-Key") != "").Msg("auth middleware")

		setCtx := func(userID, role string) {
			r := echoCtx.Request()
			newCtx := context.WithValue(r.Context(), UserIDKey, userID)
			newCtx = context.WithValue(newCtx, UserRoleKey, role)
			echoCtx.SetRequest(r.WithContext(newCtx))
		}

		// Try JWT Bearer token
		if strings.HasPrefix(auth, "Bearer ") {
			tokenStr := strings.TrimPrefix(auth, "Bearer ")
			token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
				return []byte(jwtSecret), nil
			})
			if err != nil || !token.Valid {
				writeUnauthorized(ctx, "invalid token")
				return
			}

			claims, ok := token.Claims.(jwt.MapClaims)
			if !ok {
				writeUnauthorized(ctx, "invalid claims")
				return
			}

			userID, _ := claims["sub"].(string)
			role, _ := claims["role"].(string)
			setCtx(userID, role)
			next(ctx)
			return
		}

		// Try API key
		apiKey := ctx.Header("X-API-Key")
		if apiKey == "" {
			apiKey = ctx.Query("api_key")
		}

		if apiKey != "" {
			uid, err := parseAPIKeyToUUID(apiKey)
			if err != nil {
				writeUnauthorized(ctx, "invalid api key")
				return
			}

			user, err := queries.GetUserByAPIKey(echoCtx.Request().Context(), uid)
			if err != nil {
				writeUnauthorized(ctx, "invalid api key")
				return
			}

			if !user.IsActive {
				writeForbidden(ctx, "account disabled")
				return
			}

			setCtx(util.UUIDToStr(user.ID), user.Role)
			next(ctx)
			return
		}

		// Try session cookie
		cookie, err := echoCtx.Cookie("od_session")
		if err == nil && cookie.Value != "" {
			token, err := jwt.Parse(cookie.Value, func(t *jwt.Token) (any, error) {
				return []byte(jwtSecret), nil
			})
			if err == nil && token.Valid {
				claims, ok := token.Claims.(jwt.MapClaims)
				if ok {
					userID, _ := claims["sub"].(string)
					role, _ := claims["role"].(string)
					log.Trace().Str("user_id", userID).Str("role", role).Msg("authenticated via session cookie")
					setCtx(userID, role)
					next(ctx)
					return
				}
			}
		}

		log.Debug().Str("path", ctx.URL().Path).Msg("authentication failed - no valid credentials")
		writeUnauthorized(ctx, "authentication required")
	}
}

func AdminOnly() func(ctx huma.Context, next func(huma.Context)) {
	return func(ctx huma.Context, next func(huma.Context)) {
		echoCtx := humaecho.Unwrap(ctx)
		role := GetUserRole(echoCtx.Request().Context())
		if role != "admin" {
			writeForbidden(ctx, "admin access required")
			return
		}
		next(ctx)
	}
}

func writeUnauthorized(ctx huma.Context, msg string) {
	ctx.SetStatus(http.StatusUnauthorized)
	ctx.SetHeader("Content-Type", "application/json")
	_ = json.NewEncoder(ctx.BodyWriter()).Encode(huma.ErrorModel{
		Title:  http.StatusText(http.StatusUnauthorized),
		Status: http.StatusUnauthorized,
		Detail: msg,
	})
}

func writeForbidden(ctx huma.Context, msg string) {
	ctx.SetStatus(http.StatusForbidden)
	ctx.SetHeader("Content-Type", "application/json")
	_ = json.NewEncoder(ctx.BodyWriter()).Encode(huma.ErrorModel{
		Title:  http.StatusText(http.StatusForbidden),
		Status: http.StatusForbidden,
		Detail: msg,
	})
}

func parseAPIKeyToUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 32 {
		return u, huma.Error401Unauthorized("invalid api key")
	}
	decoded, err := hex.DecodeString(s)
	if err != nil {
		return u, err
	}
	copy(u.Bytes[:], decoded)
	u.Valid = true
	return u, nil
}

// GenerateJWT creates a signed JWT token with the given user ID, role, and expiry.
func GenerateJWT(userID, role, secret string, expiry time.Duration) (string, error) {
	claims := jwt.MapClaims{
		"sub":  userID,
		"role": role,
		"iat":  time.Now().Unix(),
		"exp":  time.Now().Add(expiry).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(secret))
}
