package middleware

import (
	"context"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	"github.com/opendebrid/opendebrid/internal/database/gen"
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

// Auth middleware validates JWT or API key.
func Auth(jwtSecret string, db *pgxpool.Pool) echo.MiddlewareFunc {
	queries := gen.New(db)

	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			auth := c.Request().Header.Get("Authorization")

			// Try JWT Bearer token
			if strings.HasPrefix(auth, "Bearer ") {
				tokenStr := strings.TrimPrefix(auth, "Bearer ")
				token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (any, error) {
					return []byte(jwtSecret), nil
				})
				if err != nil || !token.Valid {
					return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid token"})
				}

				claims, ok := token.Claims.(jwt.MapClaims)
				if !ok {
					return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid claims"})
				}

				userID, _ := claims["sub"].(string)
				role, _ := claims["role"].(string)

				ctx := context.WithValue(c.Request().Context(), UserIDKey, userID)
				ctx = context.WithValue(ctx, UserRoleKey, role)
				c.SetRequest(c.Request().WithContext(ctx))
				return next(c)
			}

			// Try API key
			apiKey := c.Request().Header.Get("X-API-Key")
			if apiKey == "" {
				apiKey = c.QueryParam("api_key")
			}

			if apiKey != "" {
				uid, err := parseAPIKeyToUUID(apiKey)
				if err != nil {
					return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid api key"})
				}

				user, err := queries.GetUserByAPIKey(c.Request().Context(), uid)
				if err != nil {
					return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid api key"})
				}

				if !user.IsActive {
					return c.JSON(http.StatusForbidden, map[string]string{"error": "account disabled"})
				}

				userIDStr := pgUUIDToStr(user.ID)
				ctx := context.WithValue(c.Request().Context(), UserIDKey, userIDStr)
				ctx = context.WithValue(ctx, UserRoleKey, user.Role)
				c.SetRequest(c.Request().WithContext(ctx))
				return next(c)
			}

			// Try session cookie (web UI)
			cookie, err := c.Cookie("od_session")
			if err == nil && cookie.Value != "" {
				token, err := jwt.Parse(cookie.Value, func(t *jwt.Token) (any, error) {
					return []byte(jwtSecret), nil
				})
				if err == nil && token.Valid {
					claims, ok := token.Claims.(jwt.MapClaims)
					if ok {
						userID, _ := claims["sub"].(string)
						role, _ := claims["role"].(string)
						ctx := context.WithValue(c.Request().Context(), UserIDKey, userID)
						ctx = context.WithValue(ctx, UserRoleKey, role)
						c.SetRequest(c.Request().WithContext(ctx))
						return next(c)
					}
				}
			}

			return c.JSON(http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		}
	}
}

func parseAPIKeyToUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	s = strings.ReplaceAll(s, "-", "")
	if len(s) != 32 {
		return u, echo.ErrUnauthorized
	}
	decoded, err := hex.DecodeString(s)
	if err != nil {
		return u, err
	}
	copy(u.Bytes[:], decoded)
	u.Valid = true
	return u, nil
}

func pgUUIDToStr(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return strings.Join([]string{
		hex.EncodeToString(b[0:4]),
		hex.EncodeToString(b[4:6]),
		hex.EncodeToString(b[6:8]),
		hex.EncodeToString(b[8:10]),
		hex.EncodeToString(b[10:16]),
	}, "-")
}
