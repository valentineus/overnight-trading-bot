package tinvest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/russianinvestments/invest-api-go-sdk/investgo"
	pb "github.com/russianinvestments/invest-api-go-sdk/proto"
	"github.com/shopspring/decimal"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/logging"
	"overnight-trading-bot/internal/money"
)

type Options struct {
	Token          string
	AccountID      string
	Endpoint       string
	AppName        string
	RequestTimeout time.Duration
	RetryCount     int
	RetryBackoff   time.Duration
	Logger         *slog.Logger
}

type RealGateway struct {
	client         *investgo.Client
	instrumentsPB  pb.InstrumentsServiceClient
	marketDataPB   pb.MarketDataServiceClient
	ordersPB       pb.OrdersServiceClient
	operationsPB   pb.OperationsServiceClient
	usersPB        pb.UsersServiceClient
	requestTimeout time.Duration
	retryAttempts  int
	retryBackoff   time.Duration
	instrumentLots sync.Map
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
	}, logging.NewSDKLogger(opts.Logger))
	if err != nil {
		return nil, err
	}
	return &RealGateway{
		client:         client,
		instrumentsPB:  pb.NewInstrumentsServiceClient(client.Conn),
		marketDataPB:   pb.NewMarketDataServiceClient(client.Conn),
		ordersPB:       pb.NewOrdersServiceClient(client.Conn),
		operationsPB:   pb.NewOperationsServiceClient(client.Conn),
		usersPB:        pb.NewUsersServiceClient(client.Conn),
		requestTimeout: opts.RequestTimeout,
		retryAttempts:  opts.RetryCount,
		retryBackoff:   opts.RetryBackoff,
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
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (*pb.EtfResponse, error) {
		return retryValue(callCtx, g.retryAttempts, g.retryBackoff, func() (*pb.EtfResponse, error) {
			return g.instrumentsPB.EtfBy(callCtx, &pb.InstrumentRequest{
				IdType:    pb.InstrumentIdType_INSTRUMENT_ID_TYPE_TICKER,
				ClassCode: &classCode,
				Id:        ticker,
			})
		})
	})
	if err != nil {
		return domain.Instrument{}, err
	}
	etf := resp.GetInstrument()
	if etf == nil {
		return domain.Instrument{}, ErrNotFound
	}
	instrument := domain.Instrument{
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
	}
	g.storeInstrumentLot(instrument)
	return instrument, nil
}

func (g *RealGateway) GetCandles(ctx context.Context, instrumentUID string, interval string, from, to time.Time) ([]domain.Candle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (*pb.GetCandlesResponse, error) {
		return retryValue(callCtx, g.retryAttempts, g.retryBackoff, func() (*pb.GetCandlesResponse, error) {
			source := pb.GetCandlesRequest_CANDLE_SOURCE_EXCHANGE
			return g.marketDataPB.GetCandles(callCtx, &pb.GetCandlesRequest{
				From:             investgo.TimeToTimestamp(from),
				To:               investgo.TimeToTimestamp(to),
				Interval:         candleInterval(interval),
				InstrumentId:     &instrumentUID,
				CandleSourceType: &source,
			})
		})
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

func (g *RealGateway) GetTradingDays(ctx context.Context, exchange string, from, to time.Time) ([]time.Time, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (*pb.TradingSchedulesResponse, error) {
		return retryValue(callCtx, g.retryAttempts, g.retryBackoff, func() (*pb.TradingSchedulesResponse, error) {
			return g.instrumentsPB.TradingSchedules(callCtx, &pb.TradingSchedulesRequest{
				Exchange: &exchange,
				From:     investgo.TimeToTimestamp(from),
				To:       investgo.TimeToTimestamp(to),
			})
		})
	})
	if err != nil {
		return nil, err
	}
	var days []time.Time
	for _, schedule := range resp.GetExchanges() {
		if !strings.EqualFold(schedule.GetExchange(), exchange) {
			continue
		}
		for _, day := range schedule.GetDays() {
			if !day.GetIsTradingDay() || day.GetDate() == nil {
				continue
			}
			days = append(days, dateOnly(day.GetDate().AsTime()))
		}
	}
	return days, nil
}

func (g *RealGateway) GetOrderBook(ctx context.Context, instrumentUID string, depth int32) (domain.OrderBook, error) {
	if err := ctx.Err(); err != nil {
		return domain.OrderBook{}, err
	}
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (*pb.GetOrderBookResponse, error) {
		return retryValue(callCtx, g.retryAttempts, g.retryBackoff, func() (*pb.GetOrderBookResponse, error) {
			return g.marketDataPB.GetOrderBook(callCtx, &pb.GetOrderBookRequest{
				Depth:        depth,
				InstrumentId: &instrumentUID,
			})
		})
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
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (*pb.GetTradingStatusResponse, error) {
		return retryValue(callCtx, g.retryAttempts, g.retryBackoff, func() (*pb.GetTradingStatusResponse, error) {
			return g.marketDataPB.GetTradingStatus(callCtx, &pb.GetTradingStatusRequest{
				InstrumentId: &instrumentUID,
			})
		})
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
	quotation, err := money.DecimalToQuotation(price)
	if err != nil {
		return domain.Order{}, err
	}
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (*pb.PostOrderResponse, error) {
		return retryValue(callCtx, g.retryAttempts, g.retryBackoff, func() (*pb.PostOrderResponse, error) {
			return g.ordersPB.PostOrder(callCtx, &pb.PostOrderRequest{
				InstrumentId: instrumentUID,
				Quantity:     lots,
				Price:        quotation,
				Direction:    direction,
				AccountId:    accountID,
				OrderType:    pb.OrderType_ORDER_TYPE_LIMIT,
				OrderId:      clientOrderID,
				TimeInForce:  pb.TimeInForceType_TIME_IN_FORCE_DAY,
				PriceType:    pb.PriceType_PRICE_TYPE_CURRENCY,
			})
		})
	})
	if err != nil {
		return domain.Order{}, err
	}
	return orderFromPostResponse(resp, accountID, clientOrderID, side, price), nil
}

func (g *RealGateway) CancelOrder(ctx context.Context, accountID, orderID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (struct{}, error) {
		return struct{}{}, withRetry(callCtx, g.retryAttempts, g.retryBackoff, func() error {
			_, err := g.ordersPB.CancelOrder(callCtx, &pb.CancelOrderRequest{
				AccountId: accountID,
				OrderId:   orderID,
			})
			return err
		})
	})
	return err
}

func (g *RealGateway) GetOrderState(ctx context.Context, accountID, orderID string) (domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return domain.Order{}, err
	}
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (*pb.OrderState, error) {
		return retryValue(callCtx, g.retryAttempts, g.retryBackoff, func() (*pb.OrderState, error) {
			return g.ordersPB.GetOrderState(callCtx, &pb.GetOrderStateRequest{
				AccountId: accountID,
				OrderId:   orderID,
				PriceType: pb.PriceType_PRICE_TYPE_CURRENCY,
			})
		})
	})
	if err != nil {
		return domain.Order{}, err
	}
	return orderFromState(resp, accountID), nil
}

func (g *RealGateway) GetActiveOrders(ctx context.Context, accountID string) ([]domain.Order, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (*pb.GetOrdersResponse, error) {
		return retryValue(callCtx, g.retryAttempts, g.retryBackoff, func() (*pb.GetOrdersResponse, error) {
			return g.ordersPB.GetOrders(callCtx, &pb.GetOrdersRequest{AccountId: accountID})
		})
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
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (*pb.PortfolioResponse, error) {
		return retryValue(callCtx, g.retryAttempts, g.retryBackoff, func() (*pb.PortfolioResponse, error) {
			currency := pb.PortfolioRequest_RUB
			return g.operationsPB.GetPortfolio(callCtx, &pb.PortfolioRequest{
				AccountId: accountID,
				Currency:  &currency,
			})
		})
	})
	if err != nil {
		return domain.Portfolio{}, err
	}
	return portfolioFromResponse(resp, func(instrumentUID string) (int64, error) {
		return g.resolveInstrumentLot(ctx, instrumentUID)
	})
}

func (g *RealGateway) GetOperations(ctx context.Context, accountID string, from, to time.Time) ([]domain.Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ops, err := g.getOperationsByCursor(ctx, accountID, from, to)
	if err == nil {
		return ops, nil
	}
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (*pb.OperationsResponse, error) {
		return retryValue(callCtx, g.retryAttempts, g.retryBackoff, func() (*pb.OperationsResponse, error) {
			return g.operationsPB.GetOperations(callCtx, &pb.OperationsRequest{
				AccountId: accountID,
				From:      investgo.TimeToTimestamp(from),
				To:        investgo.TimeToTimestamp(to),
			})
		})
	})
	if err != nil {
		return nil, err
	}
	return operationsFromResponse(resp), nil
}

func (g *RealGateway) getOperationsByCursor(ctx context.Context, accountID string, from, to time.Time) ([]domain.Operation, error) {
	limit := int32(1000)
	withoutCommissions := false
	withoutTrades := true
	withoutOvernights := false
	state := pb.OperationState_OPERATION_STATE_EXECUTED
	var cursor *string
	var out []domain.Operation
	for {
		resp, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (*pb.GetOperationsByCursorResponse, error) {
			return retryValue(callCtx, g.retryAttempts, g.retryBackoff, func() (*pb.GetOperationsByCursorResponse, error) {
				return g.operationsPB.GetOperationsByCursor(callCtx, &pb.GetOperationsByCursorRequest{
					AccountId:          accountID,
					From:               investgo.TimeToTimestamp(from),
					To:                 investgo.TimeToTimestamp(to),
					Cursor:             cursor,
					Limit:              &limit,
					State:              &state,
					WithoutCommissions: &withoutCommissions,
					WithoutTrades:      &withoutTrades,
					WithoutOvernights:  &withoutOvernights,
				})
			})
		})
		if err != nil {
			return nil, err
		}
		out = append(out, operationsFromCursorResponse(resp)...)
		if !resp.GetHasNext() || resp.GetNextCursor() == "" {
			return out, nil
		}
		next := resp.GetNextCursor()
		cursor = &next
	}
}

func operationsFromResponse(resp *pb.OperationsResponse) []domain.Operation {
	ops := resp.GetOperations()
	out := make([]domain.Operation, 0, len(ops))
	for _, op := range ops {
		payment := money.MoneyValueToDecimal(op.GetPayment())
		instrumentUID := op.GetInstrumentUid()
		commission := operationCommission(op.GetOperationType(), payment)
		childCommission := decimal.Zero
		for _, child := range op.GetChildOperations() {
			childPayment := money.MoneyValueToDecimal(child.GetPayment())
			if instrumentUID == "" {
				instrumentUID = child.GetInstrumentUid()
			}
			childCommission = childCommission.Add(operationCommission(op.GetOperationType(), childPayment))
		}
		if commission.IsZero() {
			commission = childCommission
		}
		out = append(out, domain.Operation{
			ID:            op.GetId(),
			InstrumentUID: instrumentUID,
			Type:          op.GetOperationType().String(),
			Payment:       payment,
			Commission:    commission,
			ExecutedAt:    op.GetDate().AsTime().UTC(),
		})
	}
	return out
}

func operationsFromCursorResponse(resp *pb.GetOperationsByCursorResponse) []domain.Operation {
	items := resp.GetItems()
	out := make([]domain.Operation, 0, len(items))
	for _, item := range items {
		payment := money.MoneyValueToDecimal(item.GetPayment())
		commission := money.Abs(money.MoneyValueToDecimal(item.GetCommission()))
		instrumentUID := item.GetInstrumentUid()
		childCommission := decimal.Zero
		for _, child := range item.GetChildOperations() {
			childPayment := money.Abs(money.MoneyValueToDecimal(child.GetPayment()))
			if instrumentUID == "" {
				instrumentUID = child.GetInstrumentUid()
			}
			if operationLooksLikeCommission(item.GetType(), childPayment) {
				childCommission = childCommission.Add(childPayment)
			}
		}
		if commission.IsZero() && operationLooksLikeCommission(item.GetType(), payment) {
			commission = money.Abs(payment)
		}
		if commission.IsZero() {
			commission = childCommission
		}
		out = append(out, domain.Operation{
			ID:            item.GetId(),
			InstrumentUID: instrumentUID,
			Type:          item.GetType().String(),
			Payment:       payment,
			Commission:    commission,
			ExecutedAt:    item.GetDate().AsTime().UTC(),
		})
	}
	return out
}

func portfolioFromResponse(resp *pb.PortfolioResponse, lotForInstrument func(string) (int64, error)) (domain.Portfolio, error) {
	positions := resp.GetPositions()
	holdings := make([]domain.Holding, 0, len(positions))
	for _, position := range positions {
		if portfolioPositionIgnored(position) {
			continue
		}
		lot, lotErr := portfolioPositionLot(position, lotForInstrument)
		lots, err := portfolioQuantityLots(position, lot, lotErr)
		if err != nil {
			return domain.Portfolio{}, err
		}
		holdings = append(holdings, domain.Holding{
			InstrumentUID: position.GetInstrumentUid(),
			QuantityLots:  lots,
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

func (g *RealGateway) GetServerTime(ctx context.Context) (time.Time, error) {
	if err := ctx.Err(); err != nil {
		return time.Time{}, err
	}
	header, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (metadata.MD, error) {
		return retryValue(callCtx, g.retryAttempts, g.retryBackoff, func() (metadata.MD, error) {
			var header, trailer metadata.MD
			_, err := g.usersPB.GetInfo(callCtx, &pb.GetInfoRequest{}, grpc.Header(&header), grpc.Trailer(&trailer))
			if err != nil {
				return trailer, err
			}
			return header, nil
		})
	})
	if err != nil {
		return time.Time{}, err
	}
	if serverTime, ok := serverTimeFromHeader(header); ok {
		return serverTime, nil
	}
	return time.Time{}, errors.New("server time is unavailable in response metadata")
}

func operationCommission(operationType pb.OperationType, payment decimal.Decimal) decimal.Decimal {
	if !operationTypeIsCommission(operationType) {
		return decimal.Zero
	}
	return money.Abs(payment)
}

func operationTypeIsCommission(operationType pb.OperationType) bool {
	return operationType == pb.OperationType_OPERATION_TYPE_BROKER_FEE ||
		operationType == pb.OperationType_OPERATION_TYPE_SERVICE_FEE ||
		operationType == pb.OperationType_OPERATION_TYPE_SUCCESS_FEE
}

func operationLooksLikeCommission(operationType pb.OperationType, payment decimal.Decimal) bool {
	return operationTypeIsCommission(operationType) && !payment.IsZero()
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

func portfolioPositionLot(position *pb.PortfolioPosition, lotForInstrument func(string) (int64, error)) (int64, error) {
	if position == nil || lotForInstrument == nil {
		return 0, nil
	}
	return lotForInstrument(position.GetInstrumentUid())
}

func portfolioPositionIgnored(position *pb.PortfolioPosition) bool {
	if position == nil {
		return true
	}
	if money.QuotationToDecimal(position.GetQuantity()).IsZero() {
		return true
	}
	return strings.EqualFold(position.GetInstrumentType(), "currency")
}

func portfolioQuantityLots(position *pb.PortfolioPosition, lot int64, lotErr error) (int64, error) {
	if position == nil {
		return 0, nil
	}
	if lots, ok := portfolioDeprecatedQuantityLots(position); ok {
		return lots.IntPart(), nil
	}
	if lotErr != nil {
		return 0, lotErr
	}
	quantity := money.QuotationToDecimal(position.GetQuantity())
	if lot > 0 {
		return quantity.Div(decimal.NewFromInt(lot)).IntPart(), nil
	}
	return 0, fmt.Errorf("portfolio lot size is unknown for %s", position.GetInstrumentUid())
}

func (g *RealGateway) storeInstrumentLot(instrument domain.Instrument) {
	if instrument.InstrumentUID == "" || instrument.Lot <= 0 {
		return
	}
	g.instrumentLots.Store(instrument.InstrumentUID, instrument.Lot)
}

func (g *RealGateway) lotForInstrument(instrumentUID string) int64 {
	if instrumentUID == "" {
		return 0
	}
	value, ok := g.instrumentLots.Load(instrumentUID)
	if !ok {
		return 0
	}
	lot, ok := value.(int64)
	if !ok {
		return 0
	}
	return lot
}

func (g *RealGateway) resolveInstrumentLot(ctx context.Context, instrumentUID string) (int64, error) {
	if lot := g.lotForInstrument(instrumentUID); lot > 0 {
		return lot, nil
	}
	if instrumentUID == "" {
		return 0, errors.New("portfolio instrument uid is empty")
	}
	resp, err := requestWithTimeout(ctx, g.requestTimeout, func(callCtx context.Context) (*pb.InstrumentResponse, error) {
		return retryValue(callCtx, g.retryAttempts, g.retryBackoff, func() (*pb.InstrumentResponse, error) {
			return g.instrumentsPB.GetInstrumentBy(callCtx, &pb.InstrumentRequest{
				IdType: pb.InstrumentIdType_INSTRUMENT_ID_TYPE_UID,
				Id:     instrumentUID,
			})
		})
	})
	if err != nil {
		return 0, err
	}
	instrument := resp.GetInstrument()
	if instrument == nil || instrument.GetLot() <= 0 {
		return 0, fmt.Errorf("portfolio lot size is unavailable for %s", instrumentUID)
	}
	lot := int64(instrument.GetLot())
	g.instrumentLots.Store(instrumentUID, lot)
	return lot, nil
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
	avgFillPrice := decimal.Zero
	if resp.GetLotsExecuted() > 0 {
		avgFillPrice = money.MoneyValueToDecimal(resp.GetExecutedOrderPrice())
		if !avgFillPrice.IsPositive() {
			avgFillPrice = limitPrice
		}
	}
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
		AvgFillPrice:  avgFillPrice,
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
	avgFillPrice := decimal.Zero
	if state.GetLotsExecuted() > 0 {
		avgFillPrice = money.MoneyValueToDecimal(state.GetAveragePositionPrice())
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
		AvgFillPrice:  avgFillPrice,
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
	sanitized := proto.Clone(msg)
	clearSensitiveProtoFields(sanitized.ProtoReflect())
	raw, err := protojson.Marshal(sanitized)
	if err != nil {
		fallback, _ := json.Marshal(map[string]string{"marshal_error": err.Error()})
		return string(fallback)
	}
	return string(raw)
}

func clearSensitiveProtoFields(message protoreflect.Message) {
	if !message.IsValid() {
		return
	}
	fields := message.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		field := fields.Get(i)
		if isSensitiveProtoField(field.Name()) {
			message.Clear(field)
			continue
		}
		value := message.Get(field)
		switch {
		case field.IsList():
			list := value.List()
			if field.Kind() == protoreflect.MessageKind || field.Kind() == protoreflect.GroupKind {
				for j := 0; j < list.Len(); j++ {
					clearSensitiveProtoFields(list.Get(j).Message())
				}
			}
		case field.IsMap():
			if field.MapValue().Kind() == protoreflect.MessageKind || field.MapValue().Kind() == protoreflect.GroupKind {
				value.Map().Range(func(_ protoreflect.MapKey, value protoreflect.Value) bool {
					clearSensitiveProtoFields(value.Message())
					return true
				})
			}
		case field.Kind() == protoreflect.MessageKind || field.Kind() == protoreflect.GroupKind:
			if message.Has(field) {
				clearSensitiveProtoFields(value.Message())
			}
		}
	}
}

func isSensitiveProtoField(name protoreflect.Name) bool {
	normalized := strings.ReplaceAll(strings.ToLower(string(name)), "_", "")
	return normalized == "accountid"
}
