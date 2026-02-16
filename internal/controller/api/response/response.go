package response

import (
	"github.com/labstack/echo/v4"
)

type APIResponse struct {
	Success bool   `json:"success"`
	Data    any    `json:"data"`
	Error   *APIError `json:"error"`
	Meta    *Meta  `json:"meta,omitempty"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

type Meta struct {
	Page    int   `json:"page"`
	PerPage int   `json:"per_page"`
	Total   int64 `json:"total"`
}

func Success(c echo.Context, status int, data any) error {
	return c.JSON(status, APIResponse{
		Success: true,
		Data:    data,
	})
}

func SuccessWithMeta(c echo.Context, status int, data any, meta *Meta) error {
	return c.JSON(status, APIResponse{
		Success: true,
		Data:    data,
		Meta:    meta,
	})
}

func Error(c echo.Context, status int, code, message string) error {
	return c.JSON(status, APIResponse{
		Success: false,
		Error: &APIError{
			Code:    code,
			Message: message,
		},
	})
}

func ErrorWithDetails(c echo.Context, status int, code, message string, details any) error {
	return c.JSON(status, APIResponse{
		Success: false,
		Error: &APIError{
			Code:    code,
			Message: message,
			Details: details,
		},
	})
}
