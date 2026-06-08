package marketdata

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/testutil"
	"overnight-trading-bot/internal/tinvest"
)

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time {
	return c.now
}

func (c fixedClock) Sleep(<-chan struct{}, time.Duration) bool {
	return true
}

func TestLatestQuoteUsesExchangeTimestampForFreshness(t *testing.T) {
	now := time.Date(2026, 6, 8, 18, 20, 0, 0, time.UTC)
	gateway := tinvest.NewFakeGateway()
	gateway.OrderBooks["uid"] = domain.OrderBook{
		InstrumentUID: "uid",
		Time:          now.Add(-2 * time.Second),
		ReceivedAt:    now,
		Bids:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(99), QuantityLots: 10}},
		Asks:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(101), QuantityLots: 10}},
	}
	loader := NewLoader(nil, gateway)
	loader.SetClock(fixedClock{now: now})
	_, err := loader.LatestQuote(context.Background(), "uid", 20, time.Second)
	if err == nil || !strings.Contains(err.Error(), "quote age") {
		t.Fatalf("LatestQuote err=%v, want stale exchange timestamp rejection", err)
	}
}

func TestBackfillDailyFailsOnAnyEligibleInstrumentError(t *testing.T) {
	ctx := context.Background()
	gateway := tinvest.NewFakeGateway()
	repo := testutil.NewMemoryRepository()
	gateway.Candles["ok"] = []domain.Candle{{
		InstrumentUID: "ok",
		TradeDate:     time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		Close:         decimal.NewFromInt(100),
	}}
	gateway.CandleErrors["bad"] = errors.New("candles unavailable")
	loader := NewLoader(repo, gateway)

	err := loader.BackfillDaily(ctx, []domain.Instrument{
		{InstrumentUID: "ok", Ticker: "OK", Enabled: true},
		{InstrumentUID: "bad", Ticker: "BAD", Enabled: true},
	}, time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC), time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("expected per-instrument backfill error")
	}
}

func TestBackfillMinuteFailsOnAnyEligibleInstrumentError(t *testing.T) {
	ctx := context.Background()
	gateway := tinvest.NewFakeGateway()
	repo := testutil.NewMemoryRepository()
	gateway.Candles["ok"] = []domain.Candle{{
		InstrumentUID: "ok",
		TradeDate:     time.Date(2026, 6, 8, 18, 10, 0, 0, time.UTC),
		Close:         decimal.NewFromInt(100),
	}}
	gateway.CandleErrors["bad"] = errors.New("minute candles unavailable")
	loader := NewLoader(repo, gateway)

	err := loader.BackfillMinute(ctx, []domain.Instrument{
		{InstrumentUID: "ok", Ticker: "OK", Enabled: true},
		{InstrumentUID: "bad", Ticker: "BAD", Enabled: true},
	}, time.Date(2026, 6, 8, 18, 0, 0, 0, time.UTC), time.Date(2026, 6, 8, 19, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("expected per-instrument minute backfill error")
	}
}
