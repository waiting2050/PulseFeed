package logger

import (
	"log/slog"
	"os"
)

func New(appEnv string) *slog.Logger {
	level := slog.LevelDebug
	if appEnv == "prod" {
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{Level: level}
	var handler slog.Handler
	if appEnv == "prod" {
		handler = slog.NewJSONHandler(os.Stdout, opts)
	} else {
		handler = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(handler)
}
