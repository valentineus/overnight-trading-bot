package tinvest

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
)

var ErrNotFound = errors.New("not found")

type Gateway interface {
	GetInstrument(ctx context.Context, ticker, classCode string) (domain.Instrument, error)
	GetCandles(ctx context.Context, instrumentUID string, interval string, from, to time.Time) ([]domain.Candle, error)
	GetOrderBook(ctx context.Context, instrumentUID string, depth int32) (domain.OrderBook, error)
	GetTradingStatus(ctx context.Context, instrumentUID string) (domain.TradingStatus, error)
	PostLimitOrder(ctx context.Context, accountID, instrumentUID string, side domain.Side, lots int64, price decimal.Decimal, clientOrderID string) (domain.Order, error)
	CancelOrder(ctx context.Context, accountID, orderID string) error
	GetOrderState(ctx context.Context, accountID, orderID string) (domain.Order, error)
	GetActiveOrders(ctx context.Context, accountID string) ([]domain.Order, error)
	GetPortfolio(ctx context.Context, accountID string) (domain.Portfolio, error)
	GetOperations(ctx context.Context, accountID string, from, to time.Time) ([]domain.Operation, error)
	GetServerTime(ctx context.Context) (time.Time, error)
}

type FakeGateway struct {
	mu          sync.Mutex
	Instruments map[string]domain.Instrument
	Candles     map[string][]domain.Candle
	OrderBooks  map[string]domain.OrderBook
	Statuses    map[string]domain.TradingStatus
	Orders      map[string]domain.Order
	Portfolio   domain.Portfolio
	Operations  []domain.Operation
	ServerTime  time.Time
}

func NewFakeGateway() *FakeGateway {
	return &FakeGateway{
		Instruments: make(map[string]domain.Instrument),
		Candles:     make(map[string][]domain.Candle),
		OrderBooks:  make(map[string]domain.OrderBook),
		Statuses:    make(map[string]domain.TradingStatus),
		Orders:      make(map[string]domain.Order),
		Portfolio: domain.Portfolio{
			Equity:    decimal.NewFromInt(100_000),
			Cash:      decimal.NewFromInt(100_000),
			CheckedAt: time.Now().UTC(),
		},
	}
}

func (f *FakeGateway) GetInstrument(_ context.Context, ticker, classCode string) (domain.Instrument, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, instrument := range f.Instruments {
		if instrument.Ticker == ticker && instrument.ClassCode == classCode {
			return instrument, nil
		}
	}
	return domain.Instrument{}, ErrNotFound
}

func (f *FakeGateway) GetCandles(_ context.Context, instrumentUID string, _ string, from, to time.Time) ([]domain.Candle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Candle
	for _, candle := range f.Candles[instrumentUID] {
		if !candle.TradeDate.Before(from) && !candle.TradeDate.After(to) {
			out = append(out, candle)
		}
	}
	return out, nil
}

func (f *FakeGateway) GetOrderBook(_ context.Context, instrumentUID string, _ int32) (domain.OrderBook, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	book, ok := f.OrderBooks[instrumentUID]
	if !ok {
		return domain.OrderBook{}, ErrNotFound
	}
	return book, nil
}

func (f *FakeGateway) GetTradingStatus(_ context.Context, instrumentUID string) (domain.TradingStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	status, ok := f.Statuses[instrumentUID]
	if !ok {
		return domain.TradingStatusNormal, nil
	}
	return status, nil
}

func (f *FakeGateway) PostLimitOrder(_ context.Context, accountID, instrumentUID string, side domain.Side, lots int64, price decimal.Decimal, clientOrderID string) (domain.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	order := domain.Order{
		ClientOrderID: clientOrderID,
		BrokerOrderID: "fake-" + clientOrderID,
		AccountIDHash: accountID,
		InstrumentUID: instrumentUID,
		Side:          side,
		OrderType:     domain.OrderTypeLimit,
		LimitPrice:    price,
		QuantityLots:  lots,
		Status:        domain.OrderStatusSent,
		RawStateJSON:  "{}",
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
	}
	f.Orders[order.BrokerOrderID] = order
	return order, nil
}

func (f *FakeGateway) CancelOrder(_ context.Context, _ string, orderID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	order, ok := f.Orders[orderID]
	if !ok {
		return ErrNotFound
	}
	order.Status = domain.OrderStatusCancelled
	order.UpdatedAt = time.Now().UTC()
	f.Orders[orderID] = order
	return nil
}

func (f *FakeGateway) GetOrderState(_ context.Context, _ string, orderID string) (domain.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	order, ok := f.Orders[orderID]
	if !ok {
		return domain.Order{}, ErrNotFound
	}
	return order, nil
}

func (f *FakeGateway) GetActiveOrders(_ context.Context, _ string) ([]domain.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]domain.Order, 0)
	for _, order := range f.Orders {
		if order.Status == domain.OrderStatusSent || order.Status == domain.OrderStatusPartiallyFilled {
			out = append(out, order)
		}
	}
	return out, nil
}

func (f *FakeGateway) GetPortfolio(_ context.Context, _ string) (domain.Portfolio, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Portfolio.CheckedAt = time.Now().UTC()
	return f.Portfolio, nil
}

func (f *FakeGateway) GetOperations(_ context.Context, _ string, from, to time.Time) ([]domain.Operation, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Operation
	for _, op := range f.Operations {
		if !op.ExecutedAt.Before(from) && !op.ExecutedAt.After(to) {
			out = append(out, op)
		}
	}
	return out, nil
}

func (f *FakeGateway) GetServerTime(context.Context) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ServerTime.IsZero() {
		return time.Now().UTC(), nil
	}
	return f.ServerTime, nil
}
