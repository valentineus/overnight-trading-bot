package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"regexp"
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
	return slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{
		Level:       slogLevel,
		ReplaceAttr: redactAttr,
	}))
}

type SDKLogger struct {
	Logger *slog.Logger
}

func (l SDKLogger) Infof(template string, args ...any) {
	if l.Logger != nil {
		l.Logger.Info(RedactString(template), "args", redactArgs(args))
	}
}

func (l SDKLogger) Errorf(template string, args ...any) {
	if l.Logger != nil {
		l.Logger.Error(RedactString(template), "args", redactArgs(args))
	}
}

func (l SDKLogger) Fatalf(template string, args ...any) {
	if l.Logger != nil {
		l.Logger.Error(RedactString(template), "args", redactArgs(args))
	}
}

var sensitiveStringPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)((?:account[_-]?id|token)\s*[:=]\s*)("[^"]+"|'[^']+'|[^\s,}]+)`),
	regexp.MustCompile(`(?i)("(?:accountId|account_id|token)"\s*:\s*)("[^"]*"|null)`),
}

func redactAttr(_ []string, attr slog.Attr) slog.Attr {
	if attr.Value.Kind() == slog.KindString {
		attr.Value = slog.StringValue(RedactString(attr.Value.String()))
	}
	return attr
}

func redactArgs(args []any) []any {
	out := make([]any, len(args))
	for i, arg := range args {
		out[i] = redactAny(arg)
	}
	return out
}

func redactAny(value any) any {
	switch typed := value.(type) {
	case string:
		return RedactString(typed)
	case []string:
		out := make([]string, len(typed))
		for i, item := range typed {
			out[i] = RedactString(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = redactAny(item)
		}
		return out
	case fmt.Stringer:
		return RedactString(typed.String())
	default:
		return value
	}
}

func RedactString(raw string) string {
	redacted := raw
	for _, pattern := range sensitiveStringPatterns {
		redacted = pattern.ReplaceAllString(redacted, `${1}"[REDACTED]"`)
	}
	return redacted
}
