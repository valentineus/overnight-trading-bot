package notify

import (
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"overnight-trading-bot/internal/domain"
)

func TestPrefix(t *testing.T) {
	tests := map[domain.Severity]string{
		domain.SeverityInfo:      "[INFO]",
		domain.SeverityWarn:      "[WARN]",
		domain.SeverityAlert:     "[ALERT]",
		domain.Severity("alert"): "[ALERT]",
	}
	for severity, want := range tests {
		if got := prefix(severity); got != want {
			t.Fatalf("prefix(%s)=%s, want %s", severity, got, want)
		}
	}
}

func TestFormatReportPrefix(t *testing.T) {
	if got := formatMessage("[REPORT]", "daily"); got != "[REPORT] daily" {
		t.Fatalf("message=%s", got)
	}
}

func TestTelegramRetryDelayUsesRetryAfter(t *testing.T) {
	err := &tgbotapi.Error{
		Code: 429,
		ResponseParameters: tgbotapi.ResponseParameters{
			RetryAfter: 7,
		},
	}
	if got := telegramRetryDelay(err, 0); got != 7*time.Second {
		t.Fatalf("delay=%s, want 7s", got)
	}
	if got := telegramRetryDelay(assertErr{}, 1); got != 2*time.Second {
		t.Fatalf("fallback delay=%s, want 2s", got)
	}
}

type assertErr struct{}

func (assertErr) Error() string { return "boom" }
