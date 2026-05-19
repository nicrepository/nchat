// Package log provides structured logging helpers.
package log

import (
	"log/slog"
	"os"
)

type Logger = slog.Logger

func New(service string, env string) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, nil)
	return slog.New(handler).With("service", service, "env", env)
}
