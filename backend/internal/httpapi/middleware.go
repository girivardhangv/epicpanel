package httpapi

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"
)

type ctxKey string

const (
	ctxRequestID ctxKey = "request_id"
	ctxUser      ctxKey = "user"
)

type User struct {
	ID    string
	Email string
	Role  string // platform role: "user" | "admin"
}

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxRequestID, id)
}

func RequestID(ctx context.Context) string {
	v, _ := ctx.Value(ctxRequestID).(string)
	return v
}

func WithUser(ctx context.Context, u *User) context.Context {
	return context.WithValue(ctx, ctxUser, u)
}

func UserFrom(ctx context.Context) (*User, bool) {
	u, ok := ctx.Value(ctxUser).(*User)
	return u, ok
}

func RequireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := UserFrom(r.Context()); !ok {
			RespondError(w, ErrUnauthorized("authentication required"))
			return
		}
		next(w, r)
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

// Flush supports streaming responses (SSE, chunked).
func (sr *statusRecorder) Flush() {
	if f, ok := sr.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack supports WebSocket upgrades (gorilla/websocket requires it).
func (sr *statusRecorder) Hijack() (rwc net.Conn, writeBuf *bufio.ReadWriter, err error) {
	hj, ok := sr.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not support Hijack")
	}
	return hj.Hijack()
}

func RequestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		id := uuid.NewString()
		ctx := WithRequestID(r.Context(), id)
		w.Header().Set("X-Request-Id", id)

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r.WithContext(ctx))

		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start).String(),
			"request_id", id,
		)
	})
}
