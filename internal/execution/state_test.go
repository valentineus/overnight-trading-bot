package execution

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/testutil"
	"overnight-trading-bot/internal/tinvest"
)

func TestClientOrderIDIncludesAttempt(t *testing.T) {
	date := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	first := ClientOrderID(date, "uid:TRUR", domain.SideBuy, 1)
	second := ClientOrderID(date, "uid:TRUR", domain.SideBuy, 1)
	third := ClientOrderID(date, "uid:TRUR", domain.SideBuy, 2)
	if first != second {
		t.Fatalf("client order id is not deterministic: %s != %s", first, second)
	}
	if first == third {
		t.Fatalf("attempt is not part of client order id: %s", first)
	}
}

func TestPlaceLimitSuppressesDuplicateSubmit(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	gateway := tinvest.NewFakeGateway()
	engine := NewEngine(domain.ModeSandbox, "account", gateway, repo)
	tradeDate := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	order := domain.Order{
		ClientOrderID: "order-1",
		AccountIDHash: "hash",
		InstrumentUID: "uid",
		TradeDate:     tradeDate,
		Side:          domain.SideBuy,
		OrderType:     domain.OrderTypeLimit,
		LimitPrice:    decimal.NewFromInt(100),
		QuantityLots:  1,
		Status:        domain.OrderStatusNew,
		AttemptNo:     1,
	}
	first, err := engine.PlaceLimit(ctx, order)
	if err != nil {
		t.Fatal(err)
	}
	second, err := engine.PlaceLimit(ctx, order)
	if err != nil {
		t.Fatal(err)
	}
	if first.BrokerOrderID != second.BrokerOrderID {
		t.Fatalf("duplicate submit posted a new broker order: %s != %s", first.BrokerOrderID, second.BrokerOrderID)
	}
	if got := len(gateway.Orders); got != 1 {
		t.Fatalf("broker posts=%d, want 1", got)
	}
	sent, err := repo.GetFreeOrdersSent(ctx, tradeDate, "uid")
	if err != nil {
		t.Fatal(err)
	}
	if sent != 1 {
		t.Fatalf("free order counter=%d, want 1", sent)
	}
}

func TestPaperPlaceEntryFillsAndCountsSubmittedOrder(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	engine := NewEngine(domain.ModePaper, "account", tinvest.NewFakeGateway(), repo)
	tradeDate := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	order, err := engine.PlaceEntry(ctx, "hash", domain.Instrument{
		InstrumentUID:     "uid",
		Lot:               1,
		MinPriceIncrement: decimal.NewFromInt(1),
	}, tradeDate, 2, domain.OrderBook{
		InstrumentUID: "uid",
		Bids:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(99), QuantityLots: 10}},
		Asks:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(101), QuantityLots: 10}},
		ReceivedAt:    time.Now().UTC(),
	}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if order.Status != domain.OrderStatusFilled || order.FilledLots != 2 || order.BrokerOrderID == "" {
		t.Fatalf("paper order=%+v, want filled broker-like order", order)
	}
	sent, err := repo.GetFreeOrdersSent(ctx, tradeDate, "uid")
	if err != nil {
		t.Fatal(err)
	}
	if sent != 1 {
		t.Fatalf("free order counter=%d, want 1", sent)
	}
}

func TestPlaceEntryRejectsStaleQuote(t *testing.T) {
	ctx := context.Background()
	engine := NewEngine(domain.ModeSandbox, "account", tinvest.NewFakeGateway(), testutil.NewMemoryRepository())
	engine.SetMaxQuoteAge(time.Second)
	_, err := engine.PlaceEntry(ctx, "hash", domain.Instrument{
		InstrumentUID:     "uid",
		Lot:               1,
		MinPriceIncrement: decimal.NewFromInt(1),
	}, time.Now().UTC(), 1, domain.OrderBook{
		InstrumentUID: "uid",
		Bids:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(99), QuantityLots: 10}},
		Asks:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(101), QuantityLots: 10}},
		ReceivedAt:    time.Now().UTC().Add(-2 * time.Second),
	}, 1, 1)
	if err == nil {
		t.Fatal("expected stale quote error")
	}
}

func TestMonitorUntilRepostsAndExpiresAtDeadline(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	gateway := tinvest.NewFakeGateway()
	engine := NewEngine(domain.ModeSandbox, "account", gateway, repo)
	instrument := domain.Instrument{
		InstrumentUID:     "uid",
		Lot:               1,
		MinPriceIncrement: decimal.NewFromInt(1),
	}
	book := domain.OrderBook{
		InstrumentUID: "uid",
		Bids:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(99), QuantityLots: 10}},
		Asks:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(101), QuantityLots: 10}},
		ReceivedAt:    time.Now().UTC(),
	}
	tradeDate := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	order, err := engine.PlaceEntry(ctx, "hash", instrument, tradeDate, 3, book, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	monitored, err := engine.MonitorUntil(ctx, order, MonitorConfig{
		Deadline:     time.Now().Add(20 * time.Millisecond),
		PollInterval: time.Millisecond,
		MaxAttempts:  2,
		RepostAfter:  time.Nanosecond,
		Instrument:   instrument,
		ImproveTicks: 1,
		Quote: func(context.Context, string) (domain.OrderBook, error) {
			book.ReceivedAt = time.Now().UTC()
			return book, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if monitored.Status != domain.OrderStatusExpired {
		t.Fatalf("status=%s, want EXPIRED", monitored.Status)
	}
	if got := len(gateway.Orders); got < 2 {
		t.Fatalf("broker orders=%d, want repost attempt", got)
	}
	sent, err := repo.GetFreeOrdersSent(ctx, tradeDate, "uid")
	if err != nil {
		t.Fatal(err)
	}
	if sent != 2 {
		t.Fatalf("free order counter=%d, want 2", sent)
	}
}
