package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
)

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// MaxBodyBytes caps every JSON request body (memory-DoS guard; audit S13).
const MaxBodyBytes = 1 << 20 // 1 MiB

func DecodeJSON(r *http.Request, dst any) *APIError {
	dec := json.NewDecoder(io.LimitReader(r.Body, MaxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return ErrValidation("invalid JSON body: " + err.Error())
	}
	return nil
}

func RespondError(w http.ResponseWriter, err error) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		apiErr = ErrInternal(err)
	}
	if apiErr.Status == http.StatusInternalServerError {
		slog.Error("internal error", "err", err)
	}
	WriteJSON(w, apiErr.Status, map[string]any{"error": apiErr})
}

func Read(r *http.Request, dst any) *APIError { return DecodeJSON(r, dst) }
