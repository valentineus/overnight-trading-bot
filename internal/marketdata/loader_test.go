package marketdata

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
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
