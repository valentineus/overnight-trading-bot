package tinvest

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
)

var ErrNotFound = errors.New("not found")

type Gateway interface {
	GetInstrument(ctx context.Context, ticker, classCode string) (domain.Instrument, error)
	GetCandles(ctx context.Context, instrumentUID string, interval string, from, to time.Time) ([]domain.Candle, error)
	GetTradingDays(ctx context.Context, exchange string, from, to time.Time) ([]time.Time, error)
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
	mu               sync.Mutex
	Instruments      map[string]domain.Instrument
	InstrumentErrors map[string]error
	Candles          map[string][]domain.Candle
	CandleErrors     map[string]error
	TradingDays      []time.Time
	TradingDayError  error
	OrderBooks       map[string]domain.OrderBook
	Statuses         map[string]domain.TradingStatus
	Orders           map[string]domain.Order
	Portfolio        domain.Portfolio
	Operations       []domain.Operation
	ServerTime       time.Time
	ServerTimeError  error
}

func NewFakeGateway() *FakeGateway {
	return &FakeGateway{
		Instruments:      make(map[string]domain.Instrument),
		InstrumentErrors: make(map[string]error),
		Candles:          make(map[string][]domain.Candle),
		CandleErrors:     make(map[string]error),
		OrderBooks:       make(map[string]domain.OrderBook),
		Statuses:         make(map[string]domain.TradingStatus),
		Orders:           make(map[string]domain.Order),
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
			if err := f.InstrumentErrors[instrument.InstrumentUID]; err != nil {
				return domain.Instrument{}, err
			}
			return instrument, nil
		}
	}
	return domain.Instrument{}, ErrNotFound
}

func (f *FakeGateway) GetCandles(_ context.Context, instrumentUID string, _ string, from, to time.Time) ([]domain.Candle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.CandleErrors[instrumentUID]; err != nil {
		return nil, err
	}
	var out []domain.Candle
	for _, candle := range f.Candles[instrumentUID] {
		if !candle.TradeDate.Before(from) && !candle.TradeDate.After(to) {
			out = append(out, candle)
		}
	}
	return out, nil
}

func (f *FakeGateway) GetTradingDays(_ context.Context, _ string, from, to time.Time) ([]time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.TradingDayError != nil {
		return nil, f.TradingDayError
	}
	var out []time.Time
	for _, day := range f.TradingDays {
		day = dateOnly(day)
		if !day.Before(dateOnly(from)) && !day.After(dateOnly(to)) {
			out = append(out, day)
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

func (f *FakeGateway) SimulateOrderBookFill(orderID string, book domain.OrderBook) (domain.Order, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	order, ok := f.Orders[orderID]
	if !ok {
		return domain.Order{}, ErrNotFound
	}
	if isTerminalFakeOrder(order.Status) || order.FilledLots >= order.QuantityLots {
		return order, nil
	}
	price, availableLots, ok := paperFillLevel(order, book)
	if !ok || availableLots <= 0 {
		return order, nil
	}
	remaining := order.QuantityLots - order.FilledLots
	fillLots := minInt64(remaining, availableLots)
	if fillLots <= 0 {
		return order, nil
	}
	order.AvgFillPrice = paperWeightedAvg(order.AvgFillPrice, order.FilledLots, price, fillLots)
	order.FilledLots += fillLots
	if order.FilledLots >= order.QuantityLots {
		order.Status = domain.OrderStatusFilled
	} else {
		order.Status = domain.OrderStatusPartiallyFilled
	}
	now := time.Now().UTC()
	order.UpdatedAt = now
	order.RawStateJSON = fmt.Sprintf(`{"paper_fill":true,"filled_lots":%d}`, order.FilledLots)
	f.Orders[orderID] = order
	f.recordPaperOperationLocked(order, fillLots, price, now)
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
	if f.ServerTimeError != nil {
		return time.Time{}, f.ServerTimeError
	}
	if f.ServerTime.IsZero() {
		return time.Now().UTC(), nil
	}
	return f.ServerTime, nil
}

func dateOnly(ts time.Time) time.Time {
	year, month, day := ts.UTC().Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func isTerminalFakeOrder(status domain.OrderStatus) bool {
	return status == domain.OrderStatusFilled ||
		status == domain.OrderStatusCancelled ||
		status == domain.OrderStatusRejected ||
		status == domain.OrderStatusExpired ||
		status == domain.OrderStatusFailed
}

func paperFillLevel(order domain.Order, book domain.OrderBook) (decimal.Decimal, int64, bool) {
	switch order.Side {
	case domain.SideBuy:
		if len(book.Asks) == 0 {
			return decimal.Zero, 0, false
		}
		ask := book.Asks[0]
		if ask.Price.IsPositive() && order.LimitPrice.GreaterThanOrEqual(ask.Price) {
			return ask.Price, ask.QuantityLots, true
		}
	case domain.SideSell:
		if len(book.Bids) == 0 {
			return decimal.Zero, 0, false
		}
		bid := book.Bids[0]
		if bid.Price.IsPositive() && order.LimitPrice.LessThanOrEqual(bid.Price) {
			return bid.Price, bid.QuantityLots, true
		}
	}
	return decimal.Zero, 0, false
}

func paperWeightedAvg(currentAvg decimal.Decimal, currentLots int64, fillPrice decimal.Decimal, fillLots int64) decimal.Decimal {
	if currentLots <= 0 {
		return fillPrice
	}
	totalLots := currentLots + fillLots
	if totalLots <= 0 {
		return decimal.Zero
	}
	return currentAvg.Mul(decimal.NewFromInt(currentLots)).
		Add(fillPrice.Mul(decimal.NewFromInt(fillLots))).
		Div(decimal.NewFromInt(totalLots))
}

func (f *FakeGateway) recordPaperOperationLocked(order domain.Order, fillLots int64, price decimal.Decimal, ts time.Time) {
	payment := price.Mul(decimal.NewFromInt(fillLots))
	opType := "OPERATION_TYPE_SELL"
	if order.Side == domain.SideBuy {
		payment = payment.Neg()
		opType = "OPERATION_TYPE_BUY"
	}
	f.Operations = append(f.Operations, domain.Operation{
		ID:            fmt.Sprintf("%s-%d", order.BrokerOrderID, len(f.Operations)+1),
		InstrumentUID: order.InstrumentUID,
		Type:          opType,
		Payment:       payment,
		Commission:    decimal.Zero,
		ExecutedAt:    ts,
	})
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
