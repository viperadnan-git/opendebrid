package handlers

import (
	"strings"

	"github.com/danielgtaylor/huma/v2"
)

// APIError is the custom error type for all API error responses.
// Implements huma.StatusError so huma serializes it as the response body.
type APIError struct {
	status  int
	Success bool   `json:"success"`
	Err     string `json:"error"`
}

func (e *APIError) Error() string  { return e.Err }
func (e *APIError) GetStatus() int { return e.status }

// InitErrors overrides huma's default error factory so all error responses
// use the unified {success, error} format.
func InitErrors() {
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		detail := msg
		if len(errs) > 0 {
			parts := make([]string, len(errs))
			for i, e := range errs {
				parts[i] = e.Error()
			}
			detail = msg + ": " + strings.Join(parts, "; ")
		}
		return &APIError{status: status, Success: false, Err: detail}
	}
}

// DataBody is the success response body containing data.
type DataBody[T any] struct {
	Success bool `json:"success"`
	Data    T    `json:"data"`
}

// DataOutput is the huma output wrapper for data responses.
type DataOutput[T any] struct {
	Body DataBody[T]
}

// OK creates a success response wrapping the given data.
func OK[T any](data T) *DataOutput[T] {
	return &DataOutput[T]{Body: DataBody[T]{Success: true, Data: data}}
}

// MsgBody is the success response body containing a message (no data).
type MsgBody struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// MsgOutput is the huma output wrapper for message-only responses.
type MsgOutput struct {
	Body MsgBody
}

// Msg creates a success response with a message string.
func Msg(message string) *MsgOutput {
	return &MsgOutput{Body: MsgBody{Success: true, Message: message}}
}
