// Package logger provides the application-wide structured slog logger.
package logger

import (
	"log/slog"
	"os"
	"strings"
)

// New builds a slog.Logger honouring the requested level and output format.
func New(level string, jsonOutput bool) *slog.Logger {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: lvl}
	var h slog.Handler
	h = slog.NewTextHandler(os.Stdout, opts)
	if jsonOutput {
		h = slog.NewJSONHandler(os.Stdout, opts)
	}
	return slog.New(h)
}
