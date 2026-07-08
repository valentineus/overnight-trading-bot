package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestNewTelegramUsesRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	start := time.Now()
	_, err := newTelegramWithEndpoint(TelegramConfig{
		BotToken:       "token",
		ChatID:         123,
		RequestTimeout: 20 * time.Millisecond,
		QueueSize:      1,
	}, nil, telegramTestEndpoint(server))
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("NewTelegram err=nil, want timeout")
	}
	if elapsed > 150*time.Millisecond {
		t.Fatalf("NewTelegram took %s, want request timeout to fire quickly", elapsed)
	}
}

func TestNewTelegramUsesConfiguredQueueSize(t *testing.T) {
	server := telegramTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		writeTelegramOK(w, r)
	})
	defer server.Close()

	notifier, err := newTelegramWithEndpoint(TelegramConfig{
		BotToken:       "token",
		ChatID:         123,
		RequestTimeout: time.Second,
		QueueSize:      3,
	}, nil, telegramTestEndpoint(server))
	if err != nil {
		t.Fatal(err)
	}
	defer notifier.Close()

	telegram, ok := notifier.(*Telegram)
	if !ok {
		t.Fatalf("notifier type=%T, want *Telegram", notifier)
	}
	if got := cap(telegram.queue); got != 3 {
		t.Fatalf("queue cap=%d, want 3", got)
	}
}

func TestTelegramSendTimeoutAuditsMustDeliverFailure(t *testing.T) {
	audit := &recordingAuditSink{events: make(chan domain.RiskEvent, 1)}
	server := telegramTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			time.Sleep(50 * time.Millisecond)
			writeTelegramOK(w, r)
			return
		}
		writeTelegramOK(w, r)
	})
	defer server.Close()

	notifier, err := newTelegramWithEndpoint(TelegramConfig{
		BotToken:       "token",
		ChatID:         123,
		NotifyAlert:    true,
		RequestTimeout: 10 * time.Millisecond,
		QueueSize:      1,
		AuditSink:      audit,
	}, nil, telegramTestEndpoint(server))
	if err != nil {
		t.Fatal(err)
	}
	telegram := notifier.(*Telegram)
	telegram.retryDelay = func(error, int) time.Duration { return time.Millisecond }
	defer notifier.Close()

	if err := notifier.Alert(context.Background(), "critical"); err != nil {
		t.Fatal(err)
	}

	select {
	case event := <-audit.events:
		if event.EventType != "notification_undeliverable" {
			t.Fatalf("event type=%s, want notification_undeliverable", event.EventType)
		}
		if event.Severity != domain.SeverityCritical {
			t.Fatalf("event severity=%s, want critical", event.Severity)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for must-deliver audit event")
	}
}

type assertErr struct{}

func (assertErr) Error() string { return "boom" }

type recordingAuditSink struct {
	events chan domain.RiskEvent
}

func (s *recordingAuditSink) InsertRiskEvent(ctx context.Context, event domain.RiskEvent) error {
	select {
	case s.events <- event:
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

func telegramTestEndpoint(server *httptest.Server) string {
	return server.URL + "/bot%s/%s"
}

func telegramTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(handler)
}

func writeTelegramOK(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	switch {
	case strings.HasSuffix(r.URL.Path, "/getMe"):
		_, _ = w.Write([]byte(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"test","username":"test_bot"}}`))
	default:
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":1,"date":0,"chat":{"id":123,"type":"private"},"text":"ok"}}`))
	}
}
