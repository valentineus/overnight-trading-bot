package notify

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"overnight-trading-bot/internal/domain"
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
	BotToken     string
	ChatID       int64
	NotifyInfo   bool
	NotifyWarn   bool
	NotifyAlert  bool
	NotifyReport bool
	AuditSink    AuditSink
}

type AuditSink interface {
	InsertRiskEvent(ctx context.Context, event domain.RiskEvent) error
}

type Telegram struct {
	cfg    TelegramConfig
	bot    *tgbotapi.BotAPI
	log    *slog.Logger
	queue  chan outbound
	done   chan struct{}
	closed chan struct{}
}

type outbound struct {
	level domain.Severity
	text  string
}

func NewTelegram(cfg TelegramConfig, log *slog.Logger) (Notifier, error) {
	if cfg.BotToken == "" || cfg.ChatID == 0 {
		return Noop{}, nil
	}
	bot, err := tgbotapi.NewBotAPI(cfg.BotToken)
	if err != nil {
		return nil, err
	}
	t := &Telegram{
		cfg:    cfg,
		bot:    bot,
		log:    log,
		queue:  make(chan outbound, 256),
		done:   make(chan struct{}),
		closed: make(chan struct{}),
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
	item := outbound{level: level, text: text}
	if mustDeliver {
		select {
		case t.queue <- item:
			return nil
		case <-ctx.Done():
			return ctx.Err()
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
	for attempt := 0; attempt < 3; attempt++ {
		if _, err := t.bot.Send(msg); err != nil {
			delay := telegramRetryDelay(err, attempt)
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
