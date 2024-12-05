package logger

import (
	"log/slog"
	"os"
)

var (
	level  slog.LevelVar
	logger *slog.Logger
)

func init() {
	level.Set(slog.LevelInfo)
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: &level,
	}))
}

func L() *slog.Logger {
	return logger
}

func SetLevel(l slog.Level) {
	level.Set(l)
}
