// Package api contains HTTP transport glue shared by all feature handlers:
// JSON encoding/decoding and the uniform error envelope.
package api

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/epicbyte/epicpanel/backend/internal/apierror"
	"github.com/epicbyte/epicpanel/backend/internal/httpx"
)

const maxDecodeBytes = 1 << 20

// causeChain renders the full error unwrap chain so 5xx logs carry the true
// underlying failure (e.g. pgx driver detail) instead of the generic envelope.
func causeChain(err error) string {
	var parts []string
	for err != nil {
		parts = append(parts, err.Error())
		err = errors.Unwrap(err)
	}
	return strings.Join(parts, " <- ")
}

// JSON writes a success payload.
func JSON(w http.ResponseWriter, r *http.Request, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if v == nil {
		w.WriteHeader(status)
		return
	}
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

// Error maps an error onto the standard { "error": {...} } envelope.
// Server-side failures (5xx) are logged with the original error for triage;
// clients only ever see the generic message plus the request ID.
func Error(w http.ResponseWriter, r *http.Request, err error) {
	ae := apierror.From(err)

	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		ae = apierror.New(http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Request body exceeds the allowed size")
	}
	ae = apierror.WithRequestID(ae, httpx.RequestIDFrom(r))

	if ae.Status >= 500 {
		slog.Error("request failed",
			"code", ae.Code,
			"request_id", httpx.RequestIDFrom(r),
			"path", r.URL.Path,
			"cause", causeChain(err),
		)
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(ae.Status)
	_ = json.NewEncoder(w).Encode(map[string]apierror.APIError{"error": *ae})
}

// Decode parses a JSON body strictly into dst.
func Decode(r *http.Request, dst any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxDecodeBytes+1))
	if err != nil {
		return apierror.New(http.StatusBadRequest, "INVALID_BODY", "Unable to read request body")
	}
	if len(body) > maxDecodeBytes {
		return apierror.New(http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "Request body exceeds the allowed size")
	}
	if len(body) == 0 {
		return apierror.New(http.StatusBadRequest, "INVALID_BODY", "A JSON body is required")
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return apierror.New(http.StatusBadRequest, "INVALID_JSON", "Malformed JSON body")
	}
	return nil
}
