package marketdata

import (
	"context"
	"fmt"
	"time"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/repository"
	"overnight-trading-bot/internal/timeutil"
	"overnight-trading-bot/internal/tinvest"
)

type Loader struct {
	repo    repository.Repository
	gateway tinvest.Gateway
	clock   timeutil.Clock
}

func NewLoader(repo repository.Repository, gateway tinvest.Gateway) Loader {
	return Loader{repo: repo, gateway: gateway, clock: timeutil.RealClock{}}
}

func (l *Loader) SetClock(clock timeutil.Clock) {
	if clock != nil {
		l.clock = clock
	}
}

func (l Loader) BackfillDaily(ctx context.Context, instruments []domain.Instrument, from, to time.Time) error {
	for _, instrument := range instruments {
		if !instrument.Enabled || instrument.Quarantine {
			continue
		}
		candles, err := l.gateway.GetCandles(ctx, instrument.InstrumentUID, "day", from, to)
		if err != nil {
			return fmt.Errorf("load candles %s: %w", instrument.Ticker, err)
		}
		if err := l.repo.UpsertDailyCandles(ctx, candles); err != nil {
			return fmt.Errorf("persist candles %s: %w", instrument.Ticker, err)
		}
	}
	return nil
}

func (l Loader) BackfillMinute(ctx context.Context, instruments []domain.Instrument, from, to time.Time) error {
	for _, instrument := range instruments {
		if !instrument.Enabled || instrument.Quarantine {
			continue
		}
		candles, err := l.gateway.GetCandles(ctx, instrument.InstrumentUID, "minute", from, to)
		if err != nil {
			return fmt.Errorf("load minute candles %s: %w", instrument.Ticker, err)
		}
		if err := l.repo.UpsertMinuteCandles(ctx, candles); err != nil {
			return fmt.Errorf("persist minute candles %s: %w", instrument.Ticker, err)
		}
	}
	return nil
}

func (l Loader) LatestQuote(ctx context.Context, instrumentUID string, depth int32, maxAge time.Duration) (domain.OrderBook, error) {
	book, err := l.gateway.GetOrderBook(ctx, instrumentUID, depth)
	if err != nil {
		return domain.OrderBook{}, err
	}
	if book.ReceivedAt.IsZero() {
		return domain.OrderBook{}, fmt.Errorf("quote received timestamp is missing")
	}
	age := l.nowUTC().Sub(book.ReceivedAt)
	if maxAge > 0 && age > maxAge {
		return domain.OrderBook{}, fmt.Errorf("quote age %s exceeds %s", age, maxAge)
	}
	return book, nil
}

func (l Loader) nowUTC() time.Time {
	if l.clock == nil {
		return time.Now().UTC()
	}
	return l.clock.Now().UTC()
}
