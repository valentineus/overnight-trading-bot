package tinvest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/russianinvestments/invest-api-go-sdk/investgo"
	pb "github.com/russianinvestments/invest-api-go-sdk/proto"
	"github.com/shopspring/decimal"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/logging"
	"overnight-trading-bot/internal/money"
)

type Options struct {
	Token        string
	AccountID    string
	Endpoint     string
	AppName      string
	RetryCount   int
	RetryBackoff time.Duration
	Logger       *slog.Logger
}

type RealGateway struct {
	client        *investgo.Client
	instruments   *investgo.InstrumentsServiceClient
	marketData    *investgo.MarketDataServiceClient
	orders        *investgo.OrdersServiceClient
	operations    *investgo.OperationsServiceClient
	users         *investgo.UsersServiceClient
	retryAttempts int
	retryBackoff  time.Duration
}

func NewRealGateway(ctx context.Context, opts Options) (*RealGateway, error) {
	if opts.Token == "" {
		return nil, fmt.Errorf("tinvest token is required")
	}
	client, err := investgo.NewClient(ctx, investgo.Config{
		EndPoint:   opts.Endpoint,
		Token:      opts.Token,
		AppName:    opts.AppName,
		AccountId:  opts.AccountID,
		MaxRetries: 0,
	}, logging.SDKLogger{Logger: opts.Logger})
	if err != nil {
		return nil, err
	}
	return &RealGateway{
		client:        client,
		instruments:   client.NewInstrumentsServiceClient(),
		marketData:    client.NewMarketDataServiceClient(),
		orders:        client.NewOrdersServiceClient(),
		operations:    client.NewOperationsServiceClient(),
		users:         client.NewUsersServiceClient(),
		retryAttempts: opts.RetryCount,
		retryBackoff:  opts.RetryBackoff,
	}, nil
}

func (g *RealGateway) Close() error {
	if g.client == nil || g.client.Conn == nil {
		return nil
	}
	return g.client.Conn.Close()
}

func (g *RealGateway) GetInstrument(ctx context.Context, ticker, classCode string) (domain.Instrument, error) {
	if err := ctx.Err(); err != nil {
		return domain.Instrument{}, err
	}
	resp, err := retryValue(ctx, g.retryAttempts, g.retryBackoff, func() (*investgo.EtfResponse, error) {
		return g.instruments.EtfByTicker(ticker, classCode)
	})
	if err != nil {
		return domain.Instrument{}, err
	}
	etf := resp.GetInstrument()
	if etf == nil {
		return domain.Instrument{}, ErrNotFound
	}
	return domain.Instrument{
		InstrumentUID:     etf.GetUid(),
		Figi:              etf.GetFigi(),
		Ticker:            etf.GetTicker(),
		ClassCode:         etf.GetClassCode(),
		Name:              etf.GetName(),
		Lot:               int64(etf.GetLot()),
		MinPriceIncrement: money.QuotationToDecimal(etf.GetMinPriceIncrement()),
		Currency:          strings.ToUpper(etf.GetCurrency()),
		Enabled:           etf.GetApiTradeAvailableFlag() && etf.GetBuyAvailableFlag() && etf.GetSellAvailableFlag(),
		UpdatedAt:         time.Now().UTC(),
	}, nil
}

func (g *RealGateway) GetCandles(ctx context.Context, instrumentUID string, interval string, from, to time.Time) ([]domain.Candle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := retryValue(ctx, g.retryAttempts, g.retryBackoff, func() (*investgo.GetCandlesResponse, error) {
		return g.marketData.GetCandles(instrumentUID, candleInterval(interval), from, to, pb.GetCandlesRequest_CANDLE_SOURCE_EXCHANGE, 0)
	})
	if err != nil {
		return nil, err
	}
	candles := resp.GetCandles()
	out := make([]domain.Candle, 0, len(candles))
	for _, candle := range candles {
		out = append(out, domain.Candle{
			InstrumentUID: instrumentUID,
			TradeDate:     candle.GetTime().AsTime().UTC(),
			Open:          money.QuotationToDecimal(candle.GetOpen()),
			High:          money.QuotationToDecimal(candle.GetHigh()),
			Low:           money.QuotationToDecimal(candle.GetLow()),
			Close:         money.QuotationToDecimal(candle.GetClose()),
			VolumeLots:    decimal.NewFromInt(candle.GetVolume()),
			Source:        "tinvest",
			LoadedAt:      time.Now().UTC(),
		})
	}
	return out, nil
}

func (g *RealGateway) GetOrderBook(ctx context.Context, instrumentUID string, depth int32) (domain.OrderBook, error) {
	if err := ctx.Err(); err != nil {
		return domain.OrderBook{}, err
	}
	resp, err := retryValue(ctx, g.retryAttempts, g.retryBackoff, func() (*investgo.GetOrderBookResponse, error) {
		return g.marketData.GetOrderBook(instrumentUID, depth)
	})
	if err != nil {
		return domain.OrderBook{}, err
	}
	return domain.OrderBook{
		InstrumentUID: instrumentUID,
		Bids:          orderBookLevels(resp.GetBids()),
		Asks:          orderBookLevels(resp.GetAsks()),
		Time:          resp.GetOrderbookTs().AsTime().UTC(),
		ReceivedAt:    time.Now().UTC(),
	}, nil
}

func (g *RealGateway) GetTradingStatus(ctx context.Context, instrumentUID string) (domain.TradingStatus, error) {
	if err := ctx.Err(); err != nil {
		return domain.TradingStatusUnknown, err
	}
	resp, err := retryValue(ctx, g.retryAttempts, g.retryBackoff, func() (*investgo.GetTradingStatusResponse, error) {
		return g.marketData.GetTradingStatus(instrumentUID)
	})
	if err != nil {
		return domain.TradingStatusUnknown, err
	}
	if resp.GetTradingStatus() == pb.SecurityTradingStatus_SECURITY_TRADING_STATUS_NORMAL_TRADING &&
		resp.GetLimitOrderAvailableFlag() &&
		resp.GetApiTradeAvailableFlag() {
		return domain.TradingStatusNormal, nil
	}
	return domain.TradingStatusClosed, nil
}

func (g *RealGateway) PostLimitOrder(ctx context.Context, accountID, instrumentUID string, side domain.Side, lots int64, price decimal.Decimal, clientOrderID string) (domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return domain.Order{}, err
	}
	direction := pb.OrderDirection_ORDER_DIRECTION_BUY
	if side == domain.SideSell {
		direction = pb.OrderDirection_ORDER_DIRECTION_SELL
	}
	resp, err := retryValue(ctx, g.retryAttempts, g.retryBackoff, func() (*investgo.PostOrderResponse, error) {
		return g.orders.PostOrder(&investgo.PostOrderRequest{
			InstrumentId: instrumentUID,
			Quantity:     lots,
			Price:        money.DecimalToQuotation(price),
			Direction:    direction,
			AccountId:    accountID,
			OrderType:    pb.OrderType_ORDER_TYPE_LIMIT,
			OrderId:      clientOrderID,
			TimeInForce:  pb.TimeInForceType_TIME_IN_FORCE_DAY,
			PriceType:    pb.PriceType_PRICE_TYPE_CURRENCY,
		})
	})
	if err != nil {
		return domain.Order{}, err
	}
	return orderFromPostResponse(resp.PostOrderResponse, accountID, clientOrderID, side, price), nil
}

func (g *RealGateway) CancelOrder(ctx context.Context, accountID, orderID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return withRetry(ctx, g.retryAttempts, g.retryBackoff, func() error {
		_, err := g.orders.CancelOrder(accountID, orderID, nil)
		return err
	})
}

func (g *RealGateway) GetOrderState(ctx context.Context, accountID, orderID string) (domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return domain.Order{}, err
	}
	resp, err := retryValue(ctx, g.retryAttempts, g.retryBackoff, func() (*investgo.GetOrderStateResponse, error) {
		return g.orders.GetOrderState(accountID, orderID, pb.PriceType_PRICE_TYPE_CURRENCY, nil)
	})
	if err != nil {
		return domain.Order{}, err
	}
	return orderFromState(resp.OrderState, accountID), nil
}

func (g *RealGateway) GetActiveOrders(ctx context.Context, accountID string) ([]domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := retryValue(ctx, g.retryAttempts, g.retryBackoff, func() (*investgo.GetOrdersResponse, error) {
		return g.orders.GetOrders(accountID, nil)
	})
	if err != nil {
		return nil, err
	}
	states := resp.GetOrders()
	out := make([]domain.Order, 0, len(states))
	for _, state := range states {
		out = append(out, orderFromState(state, accountID))
	}
	return out, nil
}

func (g *RealGateway) GetPortfolio(ctx context.Context, accountID string) (domain.Portfolio, error) {
	if err := ctx.Err(); err != nil {
		return domain.Portfolio{}, err
	}
	resp, err := retryValue(ctx, g.retryAttempts, g.retryBackoff, func() (*investgo.PortfolioResponse, error) {
		return g.operations.GetPortfolio(accountID, pb.PortfolioRequest_RUB)
	})
	if err != nil {
		return domain.Portfolio{}, err
	}
	positions := resp.GetPositions()
	holdings := make([]domain.Holding, 0, len(positions))
	for _, position := range positions {
		holdings = append(holdings, domain.Holding{
			InstrumentUID: position.GetInstrumentUid(),
			QuantityLots:  portfolioQuantityLots(position),
			AveragePrice:  money.MoneyValueToDecimal(position.GetAveragePositionPrice()),
			MarketValue:   money.MoneyValueToDecimal(position.GetCurrentPrice()).Mul(money.QuotationToDecimal(position.GetQuantity())),
		})
	}
	equity, err := rubMoneyValueToDecimal(resp.GetTotalAmountPortfolio())
	if err != nil {
		return domain.Portfolio{}, err
	}
	cash, err := rubMoneyValueToDecimal(resp.GetTotalAmountCurrencies())
	if err != nil {
		return domain.Portfolio{}, err
	}
	return domain.Portfolio{
		Equity:    equity,
		Cash:      cash,
		Holdings:  holdings,
		CheckedAt: time.Now().UTC(),
	}, nil
}

func (g *RealGateway) GetOperations(ctx context.Context, accountID string, from, to time.Time) ([]domain.Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := retryValue(ctx, g.retryAttempts, g.retryBackoff, func() (*investgo.OperationsResponse, error) {
		return g.operations.GetOperations(&investgo.GetOperationsRequest{
			AccountId: accountID,
			From:      from,
			To:        to,
		})
	})
	if err != nil {
		return nil, err
	}
	ops := resp.GetOperations()
	out := make([]domain.Operation, 0, len(ops))
	for _, op := range ops {
		payment := money.MoneyValueToDecimal(op.GetPayment())
		out = append(out, domain.Operation{
			ID:            op.GetId(),
			InstrumentUID: op.GetInstrumentUid(),
			Type:          op.GetOperationType().String(),
			Payment:       payment,
			Commission:    operationCommission(op.GetOperationType(), payment),
			ExecutedAt:    op.GetDate().AsTime().UTC(),
		})
	}
	return out, nil
}

func (g *RealGateway) GetServerTime(ctx context.Context) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	resp, err := retryValue(ctx, g.retryAttempts, g.retryBackoff, func() (*investgo.GetInfoResponse, error) {
		return g.users.GetInfo()
	})
	if err != nil {
		return time.Time{}, err
	}
	if serverTime, ok := serverTimeFromHeader(resp.Header); ok {
		return serverTime, nil
	}
	return time.Time{}, errors.New("server time is unavailable in response metadata")
}

func operationCommission(operationType pb.OperationType, payment decimal.Decimal) decimal.Decimal {
	if operationType != pb.OperationType_OPERATION_TYPE_BROKER_FEE &&
		operationType != pb.OperationType_OPERATION_TYPE_SERVICE_FEE &&
		operationType != pb.OperationType_OPERATION_TYPE_SUCCESS_FEE {
		return decimal.Zero
	}
	return money.Abs(payment)
}

func rubMoneyValueToDecimal(value *pb.MoneyValue) (decimal.Decimal, error) {
	if value == nil {
		return decimal.Zero, nil
	}
	if currency := strings.ToUpper(value.GetCurrency()); currency != "" && currency != "RUB" {
		return decimal.Zero, fmt.Errorf("expected RUB money value, got %s", currency)
	}
	return money.MoneyValueToDecimal(value), nil
}

func portfolioQuantityLots(position *pb.PortfolioPosition) int64 {
	if position == nil {
		return 0
	}
	if lots, ok := portfolioDeprecatedQuantityLots(position); ok {
		return lots.IntPart()
	}
	return money.QuotationToDecimal(position.GetQuantity()).IntPart()
}

func portfolioDeprecatedQuantityLots(position *pb.PortfolioPosition) (decimal.Decimal, bool) {
	message := position.ProtoReflect()
	field := message.Descriptor().Fields().ByName("quantity_lots")
	if field == nil || !message.Has(field) {
		return decimal.Zero, false
	}
	quotation, ok := message.Get(field).Message().Interface().(*pb.Quotation)
	if !ok || quotation == nil {
		return decimal.Zero, false
	}
	return money.QuotationToDecimal(quotation), true
}

func serverTimeFromHeader(header map[string][]string) (time.Time, bool) {
	for _, key := range []string{"date", "Date"} {
		values := header[key]
		if len(values) == 0 {
			continue
		}
		parsed, err := http.ParseTime(values[0])
		if err == nil {
			return parsed.UTC(), true
		}
	}
	return time.Time{}, false
}

func candleInterval(interval string) pb.CandleInterval {
	switch strings.ToLower(interval) {
	case "minute", "1m", "1min":
		return pb.CandleInterval_CANDLE_INTERVAL_1_MIN
	default:
		return pb.CandleInterval_CANDLE_INTERVAL_DAY
	}
}

func orderBookLevels(levels []*pb.Order) []domain.OrderBookLevel {
	out := make([]domain.OrderBookLevel, 0, len(levels))
	for _, level := range levels {
		out = append(out, domain.OrderBookLevel{
			Price:        money.QuotationToDecimal(level.GetPrice()),
			QuantityLots: level.GetQuantity(),
		})
	}
	return out
}

func orderFromPostResponse(resp *pb.PostOrderResponse, accountID, clientOrderID string, side domain.Side, limitPrice decimal.Decimal) domain.Order {
	if resp == nil {
		return domain.Order{}
	}
	now := time.Now().UTC()
	return domain.Order{
		ClientOrderID: clientOrderID,
		BrokerOrderID: resp.GetOrderId(),
		AccountIDHash: accountID,
		InstrumentUID: resp.GetInstrumentUid(),
		Side:          side,
		OrderType:     domain.OrderTypeLimit,
		LimitPrice:    limitPrice,
		QuantityLots:  resp.GetLotsRequested(),
		FilledLots:    resp.GetLotsExecuted(),
		AvgFillPrice:  limitPrice,
		Status:        mapOrderStatus(resp.GetExecutionReportStatus()),
		Commission:    money.MoneyValueToDecimal(resp.GetExecutedCommission()),
		RawStateJSON:  marshalProto(resp),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
}

func orderFromState(state *pb.OrderState, accountID string) domain.Order {
	if state == nil {
		return domain.Order{}
	}
	side := domain.SideBuy
	if state.GetDirection() == pb.OrderDirection_ORDER_DIRECTION_SELL {
		side = domain.SideSell
	}
	orderDate := time.Now().UTC()
	if state.GetOrderDate() != nil {
		orderDate = state.GetOrderDate().AsTime().UTC()
	}
	return domain.Order{
		ClientOrderID: state.GetOrderRequestId(),
		BrokerOrderID: state.GetOrderId(),
		AccountIDHash: accountID,
		InstrumentUID: state.GetInstrumentUid(),
		Side:          side,
		OrderType:     domain.OrderTypeLimit,
		LimitPrice:    money.MoneyValueToDecimal(state.GetInitialSecurityPrice()),
		QuantityLots:  state.GetLotsRequested(),
		FilledLots:    state.GetLotsExecuted(),
		AvgFillPrice:  money.MoneyValueToDecimal(state.GetAveragePositionPrice()),
		Status:        mapOrderStatus(state.GetExecutionReportStatus()),
		Commission:    money.MoneyValueToDecimal(state.GetExecutedCommission()),
		RawStateJSON:  marshalProto(state),
		CreatedAt:     orderDate,
		UpdatedAt:     time.Now().UTC(),
	}
}

func mapOrderStatus(status pb.OrderExecutionReportStatus) domain.OrderStatus {
	switch status {
	case pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_FILL:
		return domain.OrderStatusFilled
	case pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_PARTIALLYFILL:
		return domain.OrderStatusPartiallyFilled
	case pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_CANCELLED:
		return domain.OrderStatusCancelled
	case pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_REJECTED:
		return domain.OrderStatusRejected
	case pb.OrderExecutionReportStatus_EXECUTION_REPORT_STATUS_NEW:
		return domain.OrderStatusSent
	default:
		return domain.OrderStatusNew
	}
}

func marshalProto(msg proto.Message) string {
	if msg == nil {
		return "{}"
	}
	raw, err := protojson.Marshal(msg)
	if err != nil {
		fallback, _ := json.Marshal(map[string]string{"marshal_error": err.Error()})
		return string(fallback)
	}
	return string(raw)
}
