package logging

import (
	"io"
	"log/slog"
	"os"
	"strings"
)

func New(level string, out io.Writer) *slog.Logger {
	if out == nil {
		out = os.Stdout
	}
	var slogLevel slog.Level
	switch strings.ToLower(level) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn", "warning":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}
	return slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: slogLevel}))
}

type SDKLogger struct {
	Logger *slog.Logger
}

func (l SDKLogger) Infof(template string, args ...any) {
	if l.Logger != nil {
		l.Logger.Info(template, "args", args)
	}
}

func (l SDKLogger) Errorf(template string, args ...any) {
	if l.Logger != nil {
		l.Logger.Error(template, "args", args)
	}
}

func (l SDKLogger) Fatalf(template string, args ...any) {
	if l.Logger != nil {
		l.Logger.Error(template, "args", args)
	}
}
