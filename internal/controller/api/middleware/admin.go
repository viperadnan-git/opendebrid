package middleware

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// AdminOnly restricts access to admin users.
func AdminOnly() echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			role := GetUserRole(c.Request().Context())
			if role != "admin" {
				return c.JSON(http.StatusForbidden, map[string]string{"error": "admin access required"})
			}
			return next(c)
		}
	}
}
