package httpapi

import (
	"log/slog"
	"net/http"
)

type APIError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func (e *APIError) Error() string { return e.Code + ": " + e.Message }

func ErrValidation(msg string) *APIError {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: "validation_error", Message: msg}
}

func ErrValidationDetails(msg string, details any) *APIError {
	return &APIError{Status: http.StatusUnprocessableEntity, Code: "validation_error", Message: msg, Details: details}
}

func ErrUnauthorized(msg string) *APIError {
	return &APIError{Status: http.StatusUnauthorized, Code: "unauthorized", Message: msg}
}

func ErrForbidden(msg string) *APIError {
	return &APIError{Status: http.StatusForbidden, Code: "forbidden", Message: msg}
}

func ErrNotFound(msg string) *APIError {
	return &APIError{Status: http.StatusNotFound, Code: "not_found", Message: msg}
}

func ErrConflict(msg string) *APIError {
	return &APIError{Status: http.StatusConflict, Code: "conflict", Message: msg}
}

func ErrInternal(err error) *APIError {
	// The response stays generic; the cause goes to the server log so
	// operators can diagnose without leaking internals to clients.
	slog.Error("internal error", "err", err)
	return &APIError{Status: http.StatusInternalServerError, Code: "internal_error", Message: "an internal error occurred"}
}

var _ error = (*APIError)(nil)
