package execution

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/risk"
	"overnight-trading-bot/internal/testutil"
	"overnight-trading-bot/internal/tinvest"
)

type fixedClock struct {
	now time.Time
}

func (c *fixedClock) Now() time.Time {
	return c.now
}

func (c *fixedClock) Sleep(<-chan struct{}, time.Duration) bool {
	return true
}

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

func TestPlaceEntryReservesFreeOrderBudgetAtomically(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	gateway := tinvest.NewFakeGateway()
	engine := NewEngine(domain.ModeSandbox, "account", gateway, repo)
	instrument := domain.Instrument{
		InstrumentUID:        "uid",
		Lot:                  1,
		MinPriceIncrement:    decimal.NewFromInt(1),
		FreeOrderLimitPerDay: 1,
	}
	book := domain.OrderBook{
		InstrumentUID: "uid",
		Bids:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(99), QuantityLots: 10}},
		Asks:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(101), QuantityLots: 10}},
		ReceivedAt:    time.Now().UTC(),
	}
	tradeDate := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	if _, err := engine.PlaceEntry(ctx, "hash", instrument, tradeDate, 1, book, 1, 1); err != nil {
		t.Fatal(err)
	}
	_, err := engine.PlaceEntry(ctx, "hash", instrument, tradeDate, 1, book, 1, 2)
	if !errors.Is(err, risk.ErrFreeOrderBudget) {
		t.Fatalf("expected free order budget error, got %v", err)
	}
	if got := len(gateway.Orders); got != 1 {
		t.Fatalf("broker orders=%d, want no second post", got)
	}
}

func TestRefreshPreservesLocalQuoteContext(t *testing.T) {
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
	order, err := engine.PlaceEntry(ctx, "hash", instrument, time.Now().UTC(), 1, book, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := engine.Refresh(ctx, order)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(refreshed.RawStateJSON, "local_quote") || !strings.Contains(refreshed.RawStateJSON, `"mid":"100"`) {
		t.Fatalf("raw state lost local quote context: %s", refreshed.RawStateJSON)
	}
}

func TestMonitorOnceUsesInjectedClockForDeadline(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	gateway := tinvest.NewFakeGateway()
	engine := NewEngine(domain.ModeSandbox, "account", gateway, repo)
	clock := &fixedClock{now: time.Date(2030, 1, 1, 10, 0, 0, 0, time.UTC)}
	engine.SetClock(clock)
	order, err := engine.PlaceLimit(ctx, domain.Order{
		ClientOrderID: "clocked",
		AccountIDHash: "hash",
		InstrumentUID: "uid",
		TradeDate:     clock.now,
		Side:          domain.SideBuy,
		OrderType:     domain.OrderTypeLimit,
		LimitPrice:    decimal.NewFromInt(100),
		QuantityLots:  1,
		Status:        domain.OrderStatusNew,
		AttemptNo:     1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !order.CreatedAt.Equal(clock.now) {
		t.Fatalf("created_at=%s, want injected clock %s", order.CreatedAt, clock.now)
	}
	monitored, err := engine.MonitorOnce(ctx, order, MonitorConfig{
		Deadline:     clock.now.Add(time.Minute),
		PollInterval: time.Millisecond,
		MaxAttempts:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if monitored.Status == domain.OrderStatusExpired {
		t.Fatalf("order expired before injected deadline: %+v", monitored)
	}
	clock.now = clock.now.Add(time.Minute)
	monitored, err = engine.MonitorOnce(ctx, order, MonitorConfig{
		Deadline:     clock.now,
		PollInterval: time.Millisecond,
		MaxAttempts:  1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if monitored.Status != domain.OrderStatusExpired {
		t.Fatalf("status=%s, want EXPIRED at injected deadline", monitored.Status)
	}
}

func TestPaperPlaceEntryFillsOnlyWhenOrderBookCrosses(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	paper := tinvest.NewPaperGateway(nil)
	paper.Fake().OrderBooks["uid"] = domain.OrderBook{
		InstrumentUID: "uid",
		Bids:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(99), QuantityLots: 10}},
		Asks:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(101), QuantityLots: 10}},
		ReceivedAt:    time.Now().UTC(),
	}
	engine := NewEngine(domain.ModePaper, "account", paper, repo)
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
	if order.Status != domain.OrderStatusSent || order.FilledLots != 0 || order.BrokerOrderID == "" {
		t.Fatalf("paper order=%+v, want sent unfilled broker-like order", order)
	}
	paper.Fake().OrderBooks["uid"] = domain.OrderBook{
		InstrumentUID: "uid",
		Bids:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(99), QuantityLots: 10}},
		Asks:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(100), QuantityLots: 1}},
		ReceivedAt:    time.Now().UTC(),
	}
	partial, err := engine.MonitorOnce(ctx, order, MonitorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if partial.Status != domain.OrderStatusPartiallyFilled || partial.FilledLots != 1 {
		t.Fatalf("paper partial order=%+v, want 1 lot partial fill", partial)
	}
	paper.Fake().OrderBooks["uid"] = domain.OrderBook{
		InstrumentUID: "uid",
		Bids:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(99), QuantityLots: 10}},
		Asks:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(100), QuantityLots: 10}},
		ReceivedAt:    time.Now().UTC(),
	}
	filled, err := engine.MonitorOnce(ctx, partial, MonitorConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if filled.Status != domain.OrderStatusFilled || filled.FilledLots != 2 {
		t.Fatalf("paper filled order=%+v, want full fill", filled)
	}
	sent, err := repo.GetFreeOrdersSent(ctx, tradeDate, "uid")
	if err != nil {
		t.Fatal(err)
	}
	if sent != 1 {
		t.Fatalf("free order counter=%d, want 1", sent)
	}
}

func TestCancelCountsAsFreeOrderWhenPolicyEnabled(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	gateway := tinvest.NewFakeGateway()
	engine := NewEngine(domain.ModeSandbox, "account", gateway, repo)
	engine.SetFreeOrderCountPolicy(FreeOrderPolicyCancelCounts)
	tradeDate := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	order, err := engine.PlaceLimit(ctx, domain.Order{
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
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := engine.Cancel(ctx, order); err != nil {
		t.Fatal(err)
	}
	sent, err := repo.GetFreeOrdersSent(ctx, tradeDate, "uid")
	if err != nil {
		t.Fatal(err)
	}
	if sent != 2 {
		t.Fatalf("free order counter=%d, want submit+cancel", sent)
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

func TestPlaceEntryRejectsStaleExchangeQuoteTime(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 8, 18, 20, 0, 0, time.UTC)
	engine := NewEngine(domain.ModeSandbox, "account", tinvest.NewFakeGateway(), testutil.NewMemoryRepository())
	engine.SetClock(&fixedClock{now: now})
	engine.SetMaxQuoteAge(time.Second)
	_, err := engine.PlaceEntry(ctx, "hash", domain.Instrument{
		InstrumentUID:     "uid",
		Lot:               1,
		MinPriceIncrement: decimal.NewFromInt(1),
	}, now, 1, domain.OrderBook{
		InstrumentUID: "uid",
		Time:          now.Add(-2 * time.Second),
		ReceivedAt:    now,
		Bids:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(99), QuantityLots: 10}},
		Asks:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(101), QuantityLots: 10}},
	}, 1, 1)
	if err == nil {
		t.Fatal("expected stale exchange quote timestamp error")
	}
}

func TestLiveReadonlyDoesNotPersistLocalOrder(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	engine := NewEngine(domain.ModeLiveReadonly, "account", tinvest.NewFakeGateway(), repo)
	_, err := engine.PlaceLimit(ctx, domain.Order{
		ClientOrderID: "readonly-order",
		AccountIDHash: "hash",
		InstrumentUID: "uid",
		TradeDate:     time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		Side:          domain.SideBuy,
		OrderType:     domain.OrderTypeLimit,
		LimitPrice:    decimal.NewFromInt(100),
		QuantityLots:  1,
		Status:        domain.OrderStatusNew,
		AttemptNo:     1,
	})
	if !errors.Is(err, ErrBrokerOrdersDisabled) {
		t.Fatalf("PlaceLimit err=%v, want ErrBrokerOrdersDisabled", err)
	}
	if len(repo.Orders) != 0 {
		t.Fatalf("readonly mode persisted orders: %+v", repo.Orders)
	}
}

func TestMonitorUntilRepostsAndExpiresAtDeadline(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	gateway := tinvest.NewFakeGateway()
	engine := NewEngine(domain.ModeSandbox, "account", gateway, repo)
	instrument := domain.Instrument{
		InstrumentUID:        "uid",
		Lot:                  1,
		MinPriceIncrement:    decimal.NewFromInt(1),
		FreeOrderLimitPerDay: -1,
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

func TestMonitorOnceDoesNotRepostWhenCheckRejects(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	gateway := tinvest.NewFakeGateway()
	engine := NewEngine(domain.ModeSandbox, "account", gateway, repo)
	instrument := domain.Instrument{
		InstrumentUID:        "uid",
		Lot:                  1,
		MinPriceIncrement:    decimal.NewFromInt(1),
		FreeOrderLimitPerDay: -1,
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
	order.CreatedAt = time.Now().UTC().Add(-time.Minute)
	if err := repo.UpsertOrder(ctx, order); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.MonitorOnce(ctx, order, MonitorConfig{
		Deadline:     time.Now().Add(time.Minute),
		PollInterval: time.Millisecond,
		MaxAttempts:  2,
		RepostAfter:  time.Second,
		Instrument:   instrument,
		ImproveTicks: 1,
		Quote: func(context.Context, string) (domain.OrderBook, error) {
			book.ReceivedAt = time.Now().UTC()
			return book, nil
		},
		RepostCheck: func(context.Context, domain.Order, domain.Instrument, domain.OrderBook) error {
			return context.Canceled
		},
	}); err != nil {
		t.Fatal(err)
	}
	if got := len(gateway.Orders); got != 1 {
		t.Fatalf("broker orders=%d, want no repost", got)
	}
}

func TestMonitorOnceRepostAccountsForFillsDuringCancel(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	gateway := newCancelFillGateway(2)
	engine := NewEngine(domain.ModeSandbox, "account", gateway, repo)
	instrument := domain.Instrument{
		InstrumentUID:        "uid",
		Lot:                  1,
		MinPriceIncrement:    decimal.NewFromInt(1),
		FreeOrderLimitPerDay: -1,
	}
	book := domain.OrderBook{
		InstrumentUID: "uid",
		Bids:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(99), QuantityLots: 10}},
		Asks:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(101), QuantityLots: 10}},
		ReceivedAt:    time.Now().UTC(),
	}
	tradeDate := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	order, err := engine.PlaceEntry(ctx, "hash", instrument, tradeDate, 5, book, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	order.CreatedAt = time.Now().UTC().Add(-time.Minute)
	if err := repo.UpsertOrder(ctx, order); err != nil {
		t.Fatal(err)
	}
	monitored, err := engine.MonitorOnce(ctx, order, MonitorConfig{
		Deadline:     time.Now().Add(time.Minute),
		PollInterval: time.Millisecond,
		MaxAttempts:  2,
		RepostAfter:  time.Second,
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
	if monitored.FilledLots != 2 {
		t.Fatalf("aggregate filled lots=%d, want cancel fill 2", monitored.FilledLots)
	}
	if got := len(gateway.posted); got != 2 {
		t.Fatalf("broker orders=%d, want initial+repost", got)
	}
	if got := gateway.posted[1].QuantityLots; got != 3 {
		t.Fatalf("repost quantity lots=%d, want remaining 3", got)
	}
}

func TestMonitorOnceKeepsCancelFillWhenRepostPostFails(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	gateway := newCancelFillGateway(2)
	gateway.failPostAfter = 1
	engine := NewEngine(domain.ModeSandbox, "account", gateway, repo)
	instrument := domain.Instrument{
		InstrumentUID:        "uid",
		Lot:                  1,
		MinPriceIncrement:    decimal.NewFromInt(1),
		FreeOrderLimitPerDay: -1,
	}
	book := domain.OrderBook{
		InstrumentUID: "uid",
		Bids:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(99), QuantityLots: 10}},
		Asks:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(101), QuantityLots: 10}},
		ReceivedAt:    time.Now().UTC(),
	}
	tradeDate := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	order, err := engine.PlaceEntry(ctx, "hash", instrument, tradeDate, 5, book, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	order.CreatedAt = time.Now().UTC().Add(-time.Minute)
	if err := repo.UpsertOrder(ctx, order); err != nil {
		t.Fatal(err)
	}
	monitored, err := engine.MonitorOnce(ctx, order, MonitorConfig{
		Deadline:     time.Now().Add(time.Minute),
		PollInterval: time.Millisecond,
		MaxAttempts:  2,
		RepostAfter:  time.Second,
		Instrument:   instrument,
		ImproveTicks: 1,
		Quote: func(context.Context, string) (domain.OrderBook, error) {
			book.ReceivedAt = time.Now().UTC()
			return book, nil
		},
	})
	if err == nil {
		t.Fatal("expected repost post error")
	}
	if monitored.FilledLots != 2 {
		t.Fatalf("aggregate filled lots=%d, want cancel fill 2 despite error", monitored.FilledLots)
	}
}

type cancelFillGateway struct {
	orders           map[string]domain.Order
	posted           []domain.Order
	fillLotsOnCancel int64
	failPostAfter    int
}

func newCancelFillGateway(fillLotsOnCancel int64) *cancelFillGateway {
	return &cancelFillGateway{
		orders:           make(map[string]domain.Order),
		fillLotsOnCancel: fillLotsOnCancel,
	}
}

func (g *cancelFillGateway) PostLimitOrder(_ context.Context, accountID, instrumentUID string, side domain.Side, lots int64, price decimal.Decimal, clientOrderID string) (domain.Order, error) {
	if g.failPostAfter > 0 && len(g.posted) >= g.failPostAfter {
		return domain.Order{}, errors.New("post failed")
	}
	now := time.Now().UTC()
	order := domain.Order{
		ClientOrderID: clientOrderID,
		BrokerOrderID: "broker-" + clientOrderID,
		AccountIDHash: accountID,
		InstrumentUID: instrumentUID,
		Side:          side,
		OrderType:     domain.OrderTypeLimit,
		LimitPrice:    price,
		QuantityLots:  lots,
		Status:        domain.OrderStatusSent,
		RawStateJSON:  "{}",
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	g.orders[order.BrokerOrderID] = order
	g.posted = append(g.posted, order)
	return order, nil
}

func (g *cancelFillGateway) CancelOrder(_ context.Context, _ string, orderID string) error {
	order, ok := g.orders[orderID]
	if !ok {
		return tinvest.ErrNotFound
	}
	fillLots := min(g.fillLotsOnCancel, order.QuantityLots)
	if fillLots > order.FilledLots {
		order.FilledLots = fillLots
		order.AvgFillPrice = order.LimitPrice
	}
	order.Status = domain.OrderStatusCancelled
	order.UpdatedAt = time.Now().UTC()
	g.orders[orderID] = order
	return nil
}

func (g *cancelFillGateway) GetOrderState(_ context.Context, _ string, orderID string) (domain.Order, error) {
	order, ok := g.orders[orderID]
	if !ok {
		return domain.Order{}, tinvest.ErrNotFound
	}
	return order, nil
}
