package tinvest

import (
	"context"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
)

type PaperGateway struct {
	market Gateway
	fake   *FakeGateway
}

func NewPaperGateway(market Gateway) *PaperGateway {
	return &PaperGateway{market: market, fake: NewFakeGateway()}
}

func (g *PaperGateway) Fake() *FakeGateway {
	if g.fake == nil {
		g.fake = NewFakeGateway()
	}
	return g.fake
}

func (g *PaperGateway) GetInstrument(ctx context.Context, ticker, classCode string) (domain.Instrument, error) {
	if g.market != nil {
		return g.market.GetInstrument(ctx, ticker, classCode)
	}
	return g.Fake().GetInstrument(ctx, ticker, classCode)
}

func (g *PaperGateway) GetCandles(ctx context.Context, instrumentUID string, interval string, from, to time.Time) ([]domain.Candle, error) {
	if g.market != nil {
		return g.market.GetCandles(ctx, instrumentUID, interval, from, to)
	}
	return g.Fake().GetCandles(ctx, instrumentUID, interval, from, to)
}

func (g *PaperGateway) GetOrderBook(ctx context.Context, instrumentUID string, depth int32) (domain.OrderBook, error) {
	if g.market != nil {
		return g.market.GetOrderBook(ctx, instrumentUID, depth)
	}
	return g.Fake().GetOrderBook(ctx, instrumentUID, depth)
}

func (g *PaperGateway) GetTradingStatus(ctx context.Context, instrumentUID string) (domain.TradingStatus, error) {
	if g.market != nil {
		return g.market.GetTradingStatus(ctx, instrumentUID)
	}
	return g.Fake().GetTradingStatus(ctx, instrumentUID)
}

func (g *PaperGateway) PostLimitOrder(ctx context.Context, accountID, instrumentUID string, side domain.Side, lots int64, price decimal.Decimal, clientOrderID string) (domain.Order, error) {
	return g.Fake().PostLimitOrder(ctx, accountID, instrumentUID, side, lots, price, clientOrderID)
}

func (g *PaperGateway) CancelOrder(ctx context.Context, accountID, orderID string) error {
	return g.Fake().CancelOrder(ctx, accountID, orderID)
}

func (g *PaperGateway) GetOrderState(ctx context.Context, accountID, orderID string) (domain.Order, error) {
	order, err := g.Fake().GetOrderState(ctx, accountID, orderID)
	if err != nil {
		return domain.Order{}, err
	}
	if !paperOrderCanFill(order) {
		return order, nil
	}
	book, err := g.GetOrderBook(ctx, order.InstrumentUID, 20)
	if err != nil {
		return domain.Order{}, err
	}
	return g.Fake().SimulateOrderBookFill(orderID, book)
}

func (g *PaperGateway) GetActiveOrders(ctx context.Context, accountID string) ([]domain.Order, error) {
	return g.Fake().GetActiveOrders(ctx, accountID)
}

func (g *PaperGateway) GetPortfolio(ctx context.Context, accountID string) (domain.Portfolio, error) {
	return g.Fake().GetPortfolio(ctx, accountID)
}

func (g *PaperGateway) GetOperations(ctx context.Context, accountID string, from, to time.Time) ([]domain.Operation, error) {
	return g.Fake().GetOperations(ctx, accountID, from, to)
}

func (g *PaperGateway) GetServerTime(ctx context.Context) (time.Time, error) {
	if g.market != nil {
		return g.market.GetServerTime(ctx)
	}
	return g.Fake().GetServerTime(ctx)
}

func paperOrderCanFill(order domain.Order) bool {
	return order.Status == domain.OrderStatusSent ||
		order.Status == domain.OrderStatusPartiallyFilled ||
		order.Status == domain.OrderStatusNew
}
