// Package apierror defines the panel's uniform error model.
//
// Every error surfaced to clients has the shape:
//	{ "error": { "code": "AUTH_INVALID_CREDENTIALS", "message": "...", "request_id": "..." } }
// Internal details are never leaked; unknown errors collapse to a generic 500.
//
// Errors from cooperating packages (e.g. the agent transport) can implement
// HTTPMappable so their status/code/message survive instead of collapsing to
// a generic 500.
package apierror

import (
	"errors"
	"fmt"
	"net/http"
)

type APIError struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	cause   error
}

func (e *APIError) Error() string { return fmt.Sprintf("%s (%s): %s", e.Message, e.Code, http.StatusText(e.Status)) }

// Unwrap preserves the underlying cause for server-side logging chains.
func (e *APIError) Unwrap() error { return e.cause }

func New(status int, code, message string) *APIError {
	return &APIError{Status: status, Code: code, Message: message}
}

// HTTPMappable is implemented by error types that can translate themselves
// into a client-visible APIError, keeping their real status/code/message
// instead of collapsing to a generic 500.
type HTTPMappable interface {
	APIError() *APIError
}

func Wrap(status int, code, message string, err error) *APIError {
	return &APIError{Status: status, Code: code, Message: message}
}

func From(err error) *APIError {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae
	}
	if m, ok := err.(HTTPMappable); ok {
		return m.APIError()
	}
	wrapped := New(http.StatusInternalServerError, "INTERNAL_ERROR", "An internal error occurred")
	wrapped.cause = err
	return wrapped
}

func WithRequestID(e *APIError, requestID string) *APIError {
	if e == nil {
		return nil
	}
	c := *e
	if c.Code == "INTERNAL_ERROR" {
		c.Message = "An internal error occurred. Request ID: " + requestID
	}
	return &c
}

// Common constructors keep call sites terse and codes consistent.
var (
	NotFound            = func(what string) *APIError { return New(http.StatusNotFound, "NOT_FOUND", what+" not found") }
	BadRequest          = func(msg string) *APIError { return New(http.StatusBadRequest, "VALIDATION_ERROR", msg) }
	Unauthorized        = New(http.StatusUnauthorized, "AUTH_REQUIRED", "Authentication required")
	InvalidCredentials  = New(http.StatusUnauthorized, "AUTH_INVALID_CREDENTIALS", "Invalid credentials")
	Forbidden           = New(http.StatusForbidden, "FORBIDDEN", "You do not have permission to perform this action")
	CSRF                = New(http.StatusForbidden, "CSRF_TOKEN_INVALID", "Missing or invalid CSRF token")
	SessionExpired      = New(http.StatusUnauthorized, "SESSION_EXPIRED", "Your session has expired")
	RateLimited         = New(http.StatusTooManyRequests, "RATE_LIMITED", "Too many requests, slow down")
	AccountLocked       = New(http.StatusLocked, "ACCOUNT_LOCKED", "Account temporarily locked due to failed attempts")
	InstallerLocked     = New(http.StatusForbidden, "INSTALLER_LOCKED", "The installer is no longer accessible after installation is complete")
	LicenseRequired     = New(http.StatusPaymentRequired, "LICENSE_NOT_ACTIVE", "A valid license is required for this operation")
	Conflict            = func(msg string) *APIError { return New(http.StatusConflict, "CONFLICT", msg) }
)
