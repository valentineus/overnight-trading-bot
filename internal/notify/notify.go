package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"overnight-trading-bot/internal/domain"
)

const (
	defaultQueueSize          = 256
	defaultRequestTimeout     = 10 * time.Second
	mustDeliverEnqueueTimeout = 2 * time.Second
)

type Notifier interface {
	Info(ctx context.Context, msg string) error
	Warn(ctx context.Context, msg string) error
	Alert(ctx context.Context, msg string) error
	Report(ctx context.Context, msg string) error
	Close() error
}

type Noop struct{}

func (Noop) Info(context.Context, string) error   { return nil }
func (Noop) Warn(context.Context, string) error   { return nil }
func (Noop) Alert(context.Context, string) error  { return nil }
func (Noop) Report(context.Context, string) error { return nil }
func (Noop) Close() error                         { return nil }

type TelegramConfig struct {
	BotToken       string
	ChatID         int64
	NotifyInfo     bool
	NotifyWarn     bool
	NotifyAlert    bool
	NotifyReport   bool
	RequestTimeout time.Duration
	QueueSize      int
	AuditSink      AuditSink
}

type AuditSink interface {
	InsertRiskEvent(ctx context.Context, event domain.RiskEvent) error
}

type Telegram struct {
	cfg        TelegramConfig
	bot        *tgbotapi.BotAPI
	log        *slog.Logger
	queue      chan outbound
	done       chan struct{}
	closed     chan struct{}
	retryDelay func(error, int) time.Duration
}

type outbound struct {
	level       domain.Severity
	text        string
	mustDeliver bool
}

func NewTelegram(cfg TelegramConfig, log *slog.Logger) (Notifier, error) {
	return newTelegramWithEndpoint(cfg, log, tgbotapi.APIEndpoint)
}

func newTelegramWithEndpoint(cfg TelegramConfig, log *slog.Logger, apiEndpoint string) (Notifier, error) {
	if cfg.BotToken == "" || cfg.ChatID == 0 {
		if log != nil {
			log.Warn("telegram notifier disabled; TELEGRAM_BOT_TOKEN or TELEGRAM_CHAT_ID is empty")
		}
		return Noop{}, nil
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = defaultRequestTimeout
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultQueueSize
	}
	bot, err := tgbotapi.NewBotAPIWithClient(cfg.BotToken, apiEndpoint, &http.Client{Timeout: cfg.RequestTimeout})
	if err != nil {
		return nil, err
	}
	t := &Telegram{
		cfg:        cfg,
		bot:        bot,
		log:        log,
		queue:      make(chan outbound, cfg.QueueSize),
		done:       make(chan struct{}),
		closed:     make(chan struct{}),
		retryDelay: telegramRetryDelay,
	}
	go t.dispatch()
	return t, nil
}

func (t *Telegram) Info(ctx context.Context, msg string) error {
	if !t.cfg.NotifyInfo {
		return nil
	}
	return t.enqueue(ctx, domain.SeverityInfo, msg, false)
}

func (t *Telegram) Warn(ctx context.Context, msg string) error {
	if !t.cfg.NotifyWarn {
		return nil
	}
	return t.enqueue(ctx, domain.SeverityWarn, msg, false)
}

func (t *Telegram) Alert(ctx context.Context, msg string) error {
	if !t.cfg.NotifyAlert {
		return nil
	}
	return t.enqueue(ctx, domain.SeverityAlert, msg, true)
}

func (t *Telegram) Report(ctx context.Context, msg string) error {
	if !t.cfg.NotifyReport {
		return nil
	}
	return t.enqueueText(ctx, domain.SeverityInfo, formatMessage("[REPORT]", msg), true)
}

func (t *Telegram) Close() error {
	close(t.done)
	<-t.closed
	return nil
}

func (t *Telegram) enqueue(ctx context.Context, level domain.Severity, msg string, mustDeliver bool) error {
	return t.enqueueText(ctx, level, formatMessage(prefix(level), msg), mustDeliver)
}

func (t *Telegram) enqueueText(ctx context.Context, level domain.Severity, text string, mustDeliver bool) error {
	item := outbound{level: level, text: text, mustDeliver: mustDeliver}
	if mustDeliver {
		timer := time.NewTimer(mustDeliverEnqueueTimeout)
		defer timer.Stop()
		select {
		case t.queue <- item:
			return nil
		case <-ctx.Done():
			t.auditNotificationFailure(context.Background(), item, "notification_context_cancelled", ctx.Err().Error())
			return ctx.Err()
		case <-timer.C:
			t.auditNotificationFailure(ctx, item, "notification_undeliverable", "telegram queue full")
			return nil
		}
	}
	select {
	case t.queue <- item:
	default:
		if t.log != nil {
			t.log.Warn("telegram queue full; dropping non-critical notification", "level", level)
		}
		if t.cfg.AuditSink != nil {
			_ = t.cfg.AuditSink.InsertRiskEvent(ctx, domain.RiskEvent{
				TS:          time.Now().UTC(),
				Severity:    domain.SeverityWarn,
				EventType:   "notification_dropped",
				Message:     fmt.Sprintf("telegram queue full; dropped %s notification", level),
				ContextJSON: "{}",
			})
		}
	}
	return nil
}

func (t *Telegram) dispatch() {
	defer close(t.closed)
	for {
		select {
		case item := <-t.queue:
			t.send(item)
		case <-t.done:
			for {
				select {
				case item := <-t.queue:
					t.send(item)
				default:
					return
				}
			}
		}
	}
}

func (t *Telegram) send(item outbound) {
	msg := tgbotapi.NewMessage(t.cfg.ChatID, item.text)
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := t.bot.Send(msg); err != nil {
			lastErr = err
			delay := t.retryDelay(err, attempt)
			if t.log != nil {
				t.log.Warn("telegram send failed", "attempt", attempt+1, "err", err, "retry_in", delay)
			}
			timer := time.NewTimer(delay)
			select {
			case <-timer.C:
			case <-t.done:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			}
			continue
		}
		return
	}
	if item.mustDeliver {
		message := "telegram send failed"
		if lastErr != nil {
			message = lastErr.Error()
		}
		t.auditNotificationFailure(context.Background(), item, "notification_undeliverable", message)
	}
}

func (t *Telegram) auditNotificationFailure(ctx context.Context, item outbound, eventType, message string) {
	if t.cfg.AuditSink == nil {
		return
	}
	severity := domain.SeverityWarn
	if item.mustDeliver {
		severity = domain.SeverityCritical
	}
	if err := t.cfg.AuditSink.InsertRiskEvent(ctx, domain.RiskEvent{
		TS:          time.Now().UTC(),
		Severity:    severity,
		EventType:   eventType,
		Message:     message,
		ContextJSON: fmt.Sprintf(`{"level":%q}`, item.level),
	}); err != nil && t.log != nil {
		t.log.Warn("telegram audit fallback failed", "err", err)
	}
}

func telegramRetryDelay(err error, attempt int) time.Duration {
	var apiErr tgbotapi.Error
	if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
		return time.Duration(apiErr.RetryAfter) * time.Second
	}
	var apiErrPtr *tgbotapi.Error
	if errors.As(err, &apiErrPtr) && apiErrPtr != nil && apiErrPtr.RetryAfter > 0 {
		return time.Duration(apiErrPtr.RetryAfter) * time.Second
	}
	return time.Duration(attempt+1) * time.Second
}

func prefix(level domain.Severity) string {
	switch level {
	case domain.SeverityInfo:
		return "[INFO]"
	case domain.SeverityWarn:
		return "[WARN]"
	case domain.SeverityAlert:
		return "[ALERT]"
	default:
		return fmt.Sprintf("[%s]", strings.ToUpper(string(level)))
	}
}

func formatMessage(prefixValue, msg string) string {
	return prefixValue + " " + msg
}
