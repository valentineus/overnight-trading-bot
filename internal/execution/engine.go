package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/repository"
	"overnight-trading-bot/internal/risk"
	"overnight-trading-bot/internal/timeutil"
)

var ErrBrokerOrdersDisabled = errors.New("broker orders are disabled for current mode")
var ErrEmptyOrderBook = errors.New("order book has no usable bid/ask")

const (
	FreeOrderPolicySubmitted    = "submitted"
	FreeOrderPolicyCancelCounts = "cancel_counts"
)

type Gateway interface {
	PostLimitOrder(ctx context.Context, accountID, instrumentUID string, side domain.Side, lots int64, price decimal.Decimal, clientOrderID string) (domain.Order, error)
	CancelOrder(ctx context.Context, accountID, orderID string) error
	GetOrderState(ctx context.Context, accountID, orderID string) (domain.Order, error)
}

type Engine struct {
	mode                 domain.Mode
	accountID            string
	gateway              Gateway
	store                repository.Repository
	maxQuoteAge          time.Duration
	freeOrderCountPolicy string
	clock                timeutil.Clock
	mu                   sync.Map
}

type MonitorConfig struct {
	Deadline     time.Time
	PollInterval time.Duration
	MaxAttempts  int
	RepostAfter  time.Duration
	Instrument   domain.Instrument
	ImproveTicks int
	Quote        func(ctx context.Context, instrumentUID string) (domain.OrderBook, error)
	RepostCheck  func(ctx context.Context, order domain.Order, instrument domain.Instrument, book domain.OrderBook) error
}

type repostResult struct {
	Current   domain.Order
	Changed   bool
	Cancelled domain.Order
}

func NewEngine(mode domain.Mode, accountID string, gateway Gateway, store repository.Repository) Engine {
	return Engine{
		mode:                 mode,
		accountID:            accountID,
		gateway:              gateway,
		store:                store,
		freeOrderCountPolicy: FreeOrderPolicySubmitted,
		clock:                timeutil.RealClock{},
	}
}

func (e *Engine) SetMode(mode domain.Mode) {
	e.mode = mode
}

func (e *Engine) SetMaxQuoteAge(maxQuoteAge time.Duration) {
	e.maxQuoteAge = maxQuoteAge
}

func (e *Engine) SetClock(clock timeutil.Clock) {
	if clock != nil {
		e.clock = clock
	}
}

func (e *Engine) SetFreeOrderCountPolicy(policy string) {
	switch policy {
	case FreeOrderPolicyCancelCounts:
		e.freeOrderCountPolicy = policy
	default:
		e.freeOrderCountPolicy = FreeOrderPolicySubmitted
	}
}

func (e *Engine) PlaceEntry(ctx context.Context, accountIDHash string, instrument domain.Instrument, tradeDate time.Time, lots int64, book domain.OrderBook, improveTicks int, attempt int) (domain.Order, error) {
	if err := e.checkQuoteFresh(book); err != nil {
		return domain.Order{}, err
	}
	bid, ask, err := bestBidAsk(book)
	if err != nil {
		return domain.Order{}, err
	}
	price, err := LimitBuyPrice(bid, ask, instrument.MinPriceIncrement, improveTicks)
	if err != nil {
		return domain.Order{}, err
	}
	return e.placeLimit(ctx, domain.Order{
		ClientOrderID: ClientOrderID(tradeDate, instrument.InstrumentUID, domain.SideBuy, attempt),
		AccountIDHash: accountIDHash,
		InstrumentUID: instrument.InstrumentUID,
		TradeDate:     tradeDate,
		Side:          domain.SideBuy,
		OrderType:     domain.OrderTypeLimit,
		LimitPrice:    price,
		QuantityLots:  lots,
		Status:        domain.OrderStatusNew,
		AttemptNo:     attempt,
		RawStateJSON:  orderContextJSON(book),
	}, instrument.FreeOrderLimitPerDay)
}

func (e *Engine) PlaceExit(ctx context.Context, accountIDHash string, instrument domain.Instrument, tradeDate time.Time, lots int64, book domain.OrderBook, improveTicks int, attempt int) (domain.Order, error) {
	if err := e.checkQuoteFresh(book); err != nil {
		return domain.Order{}, err
	}
	bid, ask, err := bestBidAsk(book)
	if err != nil {
		return domain.Order{}, err
	}
	price, err := LimitSellPrice(bid, ask, instrument.MinPriceIncrement, improveTicks)
	if err != nil {
		return domain.Order{}, err
	}
	return e.placeLimit(ctx, domain.Order{
		ClientOrderID: ClientOrderID(tradeDate, instrument.InstrumentUID, domain.SideSell, attempt),
		AccountIDHash: accountIDHash,
		InstrumentUID: instrument.InstrumentUID,
		TradeDate:     tradeDate,
		Side:          domain.SideSell,
		OrderType:     domain.OrderTypeLimit,
		LimitPrice:    price,
		QuantityLots:  lots,
		Status:        domain.OrderStatusNew,
		AttemptNo:     attempt,
		RawStateJSON:  orderContextJSON(book),
	}, instrument.FreeOrderLimitPerDay)
}

func (e *Engine) PlaceLimit(ctx context.Context, order domain.Order) (domain.Order, error) {
	return e.placeLimit(ctx, order, 0)
}

func (e *Engine) placeLimit(ctx context.Context, order domain.Order, freeOrderLimit int) (domain.Order, error) {
	lock := e.lockFor(order.InstrumentUID)
	lock.Lock()
	defer lock.Unlock()
	if !e.mode.AllowsBrokerOrders() && e.mode != domain.ModePaper {
		return order, ErrBrokerOrdersDisabled
	}
	if e.gateway == nil {
		return domain.Order{}, errors.New("gateway is nil")
	}
	if e.store != nil {
		existing, err := e.findExisting(ctx, order)
		if err != nil {
			return domain.Order{}, err
		}
		if existing.ClientOrderID != "" {
			return existing, nil
		}
	}

	now := e.nowUTC()
	draft := order
	draft.Status = domain.OrderStatusSent
	draft.CreatedAt = now
	draft.UpdatedAt = now
	if draft.RawStateJSON == "" {
		draft.RawStateJSON = "{}"
	}
	if e.store != nil {
		if err := e.store.RunInTx(ctx, func(ctx context.Context, repo repository.Repository) error {
			if err := repo.UpsertOrder(ctx, draft); err != nil {
				return fmt.Errorf("persist draft order: %w", err)
			}
			return repo.ReserveFreeOrders(ctx, order.TradeDate, order.InstrumentUID, 1, freeOrderLimit)
		}); err != nil {
			return domain.Order{}, err
		}
	}
	posted, err := e.gateway.PostLimitOrder(ctx, e.accountID, order.InstrumentUID, order.Side, order.QuantityLots, order.LimitPrice, order.ClientOrderID)
	if err != nil {
		draft.Status = domain.OrderStatusFailed
		if e.store != nil {
			if persistErr := e.store.RunInTx(ctx, func(ctx context.Context, repo repository.Repository) error {
				if err := repo.UpsertOrder(ctx, draft); err != nil {
					return fmt.Errorf("persist failed order: %w", err)
				}
				return repo.IncrementFreeOrders(ctx, order.TradeDate, order.InstrumentUID, -1)
			}); persistErr != nil {
				return domain.Order{}, errors.Join(err, fmt.Errorf("rollback failed order reservation: %w", persistErr))
			}
		}
		return domain.Order{}, err
	}
	posted.ClientOrderID = order.ClientOrderID
	posted.AccountIDHash = order.AccountIDHash
	posted.InstrumentUID = order.InstrumentUID
	posted.Side = order.Side
	posted.OrderType = order.OrderType
	posted.LimitPrice = order.LimitPrice
	posted.QuantityLots = order.QuantityLots
	posted.AttemptNo = order.AttemptNo
	posted.TradeDate = order.TradeDate
	posted.RawStateJSON = mergeRawStateJSON(order.RawStateJSON, posted.RawStateJSON)
	posted.CreatedAt = now
	posted.UpdatedAt = posted.CreatedAt
	if e.store != nil {
		if err := e.store.UpsertOrder(ctx, posted); err != nil {
			return domain.Order{}, err
		}
	}
	return posted, nil
}

func (e *Engine) findExisting(ctx context.Context, order domain.Order) (domain.Order, error) {
	orders, err := e.store.ListOrders(ctx, order.AccountIDHash, order.TradeDate, order.TradeDate)
	if err != nil {
		return domain.Order{}, err
	}
	for _, existing := range orders {
		if existing.ClientOrderID == order.ClientOrderID {
			return existing, nil
		}
	}
	return domain.Order{}, nil
}

func (e *Engine) Refresh(ctx context.Context, order domain.Order) (domain.Order, error) {
	if e.gateway == nil {
		return domain.Order{}, errors.New("gateway is nil")
	}
	lock := e.lockFor(order.InstrumentUID)
	lock.Lock()
	defer lock.Unlock()
	state, err := e.gateway.GetOrderState(ctx, e.accountID, order.BrokerOrderID)
	if err != nil {
		return domain.Order{}, err
	}
	state.ClientOrderID = order.ClientOrderID
	state.AccountIDHash = order.AccountIDHash
	state.InstrumentUID = order.InstrumentUID
	state.TradeDate = order.TradeDate
	state.Side = order.Side
	state.OrderType = order.OrderType
	state.LimitPrice = order.LimitPrice
	state.QuantityLots = order.QuantityLots
	state.AttemptNo = order.AttemptNo
	state.RawStateJSON = mergeRawStateJSON(localRawStateJSON(order.RawStateJSON), state.RawStateJSON)
	if e.store != nil {
		if err := e.store.UpsertOrder(ctx, state); err != nil {
			return domain.Order{}, err
		}
	}
	return state, nil
}

func (e *Engine) Cancel(ctx context.Context, order domain.Order) error {
	if e.gateway == nil {
		return errors.New("gateway is nil")
	}
	lock := e.lockFor(order.InstrumentUID)
	lock.Lock()
	defer lock.Unlock()
	if err := e.gateway.CancelOrder(ctx, e.accountID, order.BrokerOrderID); err != nil {
		return err
	}
	if e.store != nil {
		return e.store.RunInTx(ctx, func(ctx context.Context, repo repository.Repository) error {
			if err := repo.UpdateOrderStatus(ctx, order.ClientOrderID, domain.OrderStatusCancelled, order.FilledLots, order.RawStateJSON); err != nil {
				return err
			}
			if e.cancelCountsAsFreeOrder() {
				return repo.IncrementFreeOrders(ctx, order.TradeDate, order.InstrumentUID, 1)
			}
			return nil
		})
	}
	return nil
}

func (e *Engine) MonitorUntil(ctx context.Context, order domain.Order, cfg MonitorConfig) (domain.Order, error) {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 1
	}
	lastPost := e.nowUTC()
	current := order
	aggregate := AggregatedOrderFill(order)
	seen := map[string]domain.Order{order.ClientOrderID: order}
	for {
		previous := seen[current.ClientOrderID]
		refreshed, err := e.Refresh(ctx, current)
		if err != nil {
			return aggregate, err
		}
		if delta := fillDeltaLots(previous, refreshed); delta > 0 {
			refreshed.RawStateJSON = e.captureFillQuote(ctx, refreshed.RawStateJSON, refreshed, cfg, delta)
		}
		aggregate = mergeAggregateFill(aggregate, previous, refreshed)
		seen[current.ClientOrderID] = refreshed
		current = mergeOrderState(current, refreshed)
		aggregate.Status = current.Status
		aggregate.UpdatedAt = current.UpdatedAt
		current.RawStateJSON = withMonitorAggregate(current.RawStateJSON, aggregate)
		aggregate.RawStateJSON = current.RawStateJSON
		if err := e.persistOrderMonitorState(ctx, current); err != nil {
			return aggregate, err
		}
		if aggregate.FilledLots >= aggregate.QuantityLots {
			aggregate.Status = domain.OrderStatusFilled
			return aggregate, nil
		}
		if isTerminal(current.Status) {
			return aggregate, nil
		}
		if !cfg.Deadline.IsZero() && !e.nowUTC().Before(cfg.Deadline) {
			if err := e.Cancel(ctx, current); err != nil {
				return aggregate, err
			}
			aggregate.Status = domain.OrderStatusExpired
			if e.store != nil {
				if err := e.store.UpdateOrderStatus(ctx, current.ClientOrderID, aggregate.Status, current.FilledLots, current.RawStateJSON); err != nil {
					return aggregate, err
				}
			}
			return aggregate, nil
		}
		shouldRepost := cfg.RepostAfter > 0 &&
			e.nowUTC().Sub(lastPost) >= cfg.RepostAfter &&
			current.AttemptNo < cfg.MaxAttempts &&
			aggregate.FilledLots < aggregate.QuantityLots &&
			cfg.Quote != nil
		if shouldRepost {
			result, err := e.repost(ctx, current, cfg, aggregate.QuantityLots-aggregate.FilledLots)
			if result.Cancelled.ClientOrderID != "" {
				previous := seen[result.Cancelled.ClientOrderID]
				if delta := fillDeltaLots(previous, result.Cancelled); delta > 0 {
					result.Cancelled.RawStateJSON = e.captureFillQuote(ctx, result.Cancelled.RawStateJSON, result.Cancelled, cfg, delta)
				}
				aggregate = mergeAggregateFill(aggregate, previous, result.Cancelled)
				result.Cancelled.RawStateJSON = withMonitorAggregate(result.Cancelled.RawStateJSON, aggregate)
				aggregate.RawStateJSON = result.Cancelled.RawStateJSON
				if persistErr := e.persistOrderMonitorState(ctx, result.Cancelled); persistErr != nil {
					return aggregate, persistErr
				}
				seen[result.Cancelled.ClientOrderID] = result.Cancelled
				if aggregate.FilledLots >= aggregate.QuantityLots {
					aggregate.Status = domain.OrderStatusFilled
					return aggregate, nil
				}
			}
			if err != nil {
				return aggregate, err
			}
			if result.Changed {
				current = result.Current
				current.RawStateJSON = carryFillQuotes(current.RawStateJSON, aggregate.RawStateJSON)
				current.RawStateJSON = withMonitorAggregate(current.RawStateJSON, aggregate)
				if persistErr := e.persistOrderMonitorState(ctx, current); persistErr != nil {
					return aggregate, persistErr
				}
				seen[current.ClientOrderID] = current
				aggregate.Status = current.Status
				aggregate.UpdatedAt = current.UpdatedAt
				aggregate.RawStateJSON = current.RawStateJSON
			}
			lastPost = e.nowUTC()
			continue
		}
		if !e.sleep(ctx, cfg.PollInterval) {
			return aggregate, ctx.Err()
		}
	}
}

func (e *Engine) MonitorOnce(ctx context.Context, order domain.Order, cfg MonitorConfig) (domain.Order, error) {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 500 * time.Millisecond
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = 1
	}
	aggregate := AggregatedOrderFill(order)
	previous := order
	refreshed, err := e.Refresh(ctx, order)
	if err != nil {
		return order, err
	}
	if delta := fillDeltaLots(previous, refreshed); delta > 0 {
		refreshed.RawStateJSON = e.captureFillQuote(ctx, refreshed.RawStateJSON, refreshed, cfg, delta)
	}
	aggregate = mergeAggregateFill(aggregate, previous, refreshed)
	current := mergeOrderState(order, refreshed)
	aggregate.Status = current.Status
	aggregate.UpdatedAt = current.UpdatedAt
	current.RawStateJSON = withMonitorAggregate(current.RawStateJSON, aggregate)
	aggregate.RawStateJSON = current.RawStateJSON
	if err := e.persistOrderMonitorState(ctx, current); err != nil {
		return aggregate, err
	}
	if aggregate.FilledLots >= aggregate.QuantityLots {
		aggregate.Status = domain.OrderStatusFilled
		return aggregate, nil
	}
	if isTerminal(current.Status) {
		return aggregate, nil
	}
	if !cfg.Deadline.IsZero() && !e.nowUTC().Before(cfg.Deadline) {
		if err := e.Cancel(ctx, current); err != nil {
			return aggregate, err
		}
		aggregate.Status = domain.OrderStatusExpired
		if e.store != nil {
			if err := e.store.UpdateOrderStatus(ctx, current.ClientOrderID, aggregate.Status, current.FilledLots, current.RawStateJSON); err != nil {
				return aggregate, err
			}
		}
		return aggregate, nil
	}
	shouldRepost := cfg.RepostAfter > 0 &&
		e.repostDue(current, cfg.RepostAfter) &&
		current.AttemptNo < cfg.MaxAttempts &&
		aggregate.FilledLots < aggregate.QuantityLots &&
		cfg.Quote != nil
	if shouldRepost {
		result, err := e.repost(ctx, current, cfg, aggregate.QuantityLots-aggregate.FilledLots)
		if result.Cancelled.ClientOrderID != "" {
			if delta := fillDeltaLots(current, result.Cancelled); delta > 0 {
				result.Cancelled.RawStateJSON = e.captureFillQuote(ctx, result.Cancelled.RawStateJSON, result.Cancelled, cfg, delta)
			}
			aggregate = mergeAggregateFill(aggregate, current, result.Cancelled)
			result.Cancelled.RawStateJSON = withMonitorAggregate(result.Cancelled.RawStateJSON, aggregate)
			aggregate.RawStateJSON = result.Cancelled.RawStateJSON
			if persistErr := e.persistOrderMonitorState(ctx, result.Cancelled); persistErr != nil {
				return aggregate, persistErr
			}
			if aggregate.FilledLots >= aggregate.QuantityLots {
				aggregate.Status = domain.OrderStatusFilled
				return aggregate, nil
			}
		}
		if err != nil {
			return aggregate, err
		}
		if result.Changed {
			aggregate.BrokerOrderID = result.Current.BrokerOrderID
			aggregate.ClientOrderID = result.Current.ClientOrderID
			aggregate.Status = result.Current.Status
			aggregate.UpdatedAt = result.Current.UpdatedAt
			result.Current.RawStateJSON = carryFillQuotes(result.Current.RawStateJSON, aggregate.RawStateJSON)
			result.Current.RawStateJSON = withMonitorAggregate(result.Current.RawStateJSON, aggregate)
			aggregate.RawStateJSON = result.Current.RawStateJSON
			if persistErr := e.persistOrderMonitorState(ctx, result.Current); persistErr != nil {
				return aggregate, persistErr
			}
		}
	}
	return aggregate, nil
}

func (e *Engine) repost(ctx context.Context, order domain.Order, cfg MonitorConfig, remaining int64) (repostResult, error) {
	if err := e.ensureRepostBudget(ctx, order, cfg.Instrument); err != nil {
		return repostResult{}, err
	}
	if !cfg.Deadline.IsZero() && !e.nowUTC().Before(cfg.Deadline) {
		return repostResult{Current: order}, nil
	}
	book, err := cfg.Quote(ctx, order.InstrumentUID)
	if err != nil {
		return repostResult{}, err
	}
	if cfg.RepostCheck != nil {
		if err := cfg.RepostCheck(ctx, order, cfg.Instrument, book); err != nil {
			return repostResult{Current: order}, nil
		}
	}
	if err := e.Cancel(ctx, order); err != nil {
		return repostResult{}, err
	}
	cancelled, err := e.waitTerminal(ctx, order, cfg)
	if err != nil {
		return repostResult{}, err
	}
	result := repostResult{Current: cancelled, Changed: true, Cancelled: cancelled}
	additionalFilled := cancelled.FilledLots - order.FilledLots
	if additionalFilled > 0 {
		remaining -= additionalFilled
	}
	if remaining <= 0 {
		return result, nil
	}
	if !cfg.Deadline.IsZero() && !e.nowUTC().Before(cfg.Deadline) {
		return result, nil
	}
	book, err = cfg.Quote(ctx, order.InstrumentUID)
	if err != nil {
		return result, err
	}
	if cfg.RepostCheck != nil {
		if err := cfg.RepostCheck(ctx, cancelled, cfg.Instrument, book); err != nil {
			return result, nil
		}
	}
	attempt := order.AttemptNo + 1
	var next domain.Order
	switch order.Side {
	case domain.SideBuy:
		next, err = e.PlaceEntry(ctx, order.AccountIDHash, cfg.Instrument, order.TradeDate, remaining, book, cfg.ImproveTicks, attempt)
	case domain.SideSell:
		next, err = e.PlaceExit(ctx, order.AccountIDHash, cfg.Instrument, order.TradeDate, remaining, book, cfg.ImproveTicks, attempt)
	default:
		return result, fmt.Errorf("unsupported side %s", order.Side)
	}
	if err != nil {
		return result, err
	}
	result.Current = next
	return result, nil
}

func (e *Engine) waitTerminal(ctx context.Context, order domain.Order, cfg MonitorConfig) (domain.Order, error) {
	current := order
	for {
		refreshed, err := e.Refresh(ctx, current)
		if err != nil {
			return domain.Order{}, err
		}
		current = mergeOrderState(current, refreshed)
		if isTerminal(current.Status) {
			return current, nil
		}
		if !cfg.Deadline.IsZero() && !e.nowUTC().Before(cfg.Deadline) {
			return current, nil
		}
		if !e.sleep(ctx, cfg.PollInterval) {
			return domain.Order{}, ctx.Err()
		}
	}
}

func (e *Engine) repostDue(order domain.Order, after time.Duration) bool {
	if after <= 0 {
		return false
	}
	basis := order.CreatedAt
	if basis.IsZero() {
		basis = order.UpdatedAt
	}
	if basis.IsZero() {
		return true
	}
	return e.nowUTC().Sub(basis) >= after
}

func (e *Engine) ensureRepostBudget(ctx context.Context, order domain.Order, instrument domain.Instrument) error {
	if e.store == nil || instrument.FreeOrderLimitPerDay < 0 {
		return nil
	}
	if instrument.FreeOrderLimitPerDay == 0 {
		return risk.ErrFreeOrderPolicyUnspecified
	}
	sent, err := e.store.GetFreeOrdersSent(ctx, order.TradeDate, instrument.InstrumentUID)
	if err != nil {
		return err
	}
	needed := 1
	if e.cancelCountsAsFreeOrder() {
		needed = 2
	}
	remaining := instrument.FreeOrderLimitPerDay - sent
	if remaining < needed {
		return fmt.Errorf("%w: %s remaining=%d needed=%d", risk.ErrFreeOrderBudget, instrument.InstrumentUID, remaining, needed)
	}
	return nil
}

func (e *Engine) cancelCountsAsFreeOrder() bool {
	return e.freeOrderCountPolicy == FreeOrderPolicyCancelCounts
}

func (e *Engine) checkQuoteFresh(book domain.OrderBook) error {
	if e.maxQuoteAge <= 0 {
		return nil
	}
	quoteTs := quoteTimestamp(book)
	if quoteTs.IsZero() {
		return fmt.Errorf("quote timestamp is missing")
	}
	age := e.nowUTC().Sub(quoteTs)
	if age > e.maxQuoteAge {
		return fmt.Errorf("quote age %s exceeds %s", age, e.maxQuoteAge)
	}
	return nil
}

func quoteTimestamp(book domain.OrderBook) time.Time {
	if !book.Time.IsZero() {
		return book.Time.UTC()
	}
	return book.ReceivedAt.UTC()
}

func orderContextJSON(book domain.OrderBook) string {
	bid, ask, err := bestBidAsk(book)
	if err != nil {
		return "{}"
	}
	mid := bid.Add(ask).Div(decimal.NewFromInt(2))
	context := map[string]any{
		"local_quote": map[string]string{
			"best_bid": bid.String(),
			"best_ask": ask.String(),
			"mid":      mid.String(),
		},
	}
	if ts := quoteTimestamp(book); !ts.IsZero() {
		context["local_quote"].(map[string]string)["quote_ts"] = ts.UTC().Format(time.RFC3339Nano)
	}
	raw, err := json.Marshal(context)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func mergeRawStateJSON(localRaw, brokerRaw string) string {
	local := decodeRawJSON(localRaw)
	broker := decodeRawJSON(brokerRaw)
	raw, err := json.Marshal(map[string]any{
		"local":  local,
		"broker": broker,
	})
	if err != nil {
		return brokerRaw
	}
	return string(raw)
}

func decodeRawJSON(raw string) any {
	if raw == "" {
		return map[string]any{}
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return raw
	}
	return value
}

func localRawStateJSON(raw string) string {
	var object map[string]any
	if err := json.Unmarshal([]byte(raw), &object); err != nil {
		return raw
	}
	if local, ok := object["local"]; ok {
		encoded, err := json.Marshal(local)
		if err == nil {
			return string(encoded)
		}
	}
	if quote, ok := object["local_quote"]; ok {
		encoded, err := json.Marshal(map[string]any{"local_quote": quote})
		if err == nil {
			return string(encoded)
		}
	}
	return raw
}

func AggregatedOrderFill(order domain.Order) domain.Order {
	aggregate := order
	state, ok := monitorAggregateFromRaw(order.RawStateJSON)
	if !ok {
		return aggregate
	}
	if state.QuantityLots > 0 {
		aggregate.QuantityLots = state.QuantityLots
	}
	aggregate.FilledLots = state.FilledLots
	aggregate.AvgFillPrice = state.AvgFillPrice
	aggregate.Commission = state.Commission
	return aggregate
}

type monitorAggregateState struct {
	QuantityLots int64
	FilledLots   int64
	AvgFillPrice decimal.Decimal
	Commission   decimal.Decimal
}

func monitorAggregateFromRaw(raw string) (monitorAggregateState, bool) {
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return monitorAggregateState{}, false
	}
	if local, ok := root["local"].(map[string]any); ok {
		if state, ok := monitorAggregateFromContainer(local); ok {
			return state, true
		}
	}
	return monitorAggregateFromContainer(root)
}

func monitorAggregateFromContainer(container map[string]any) (monitorAggregateState, bool) {
	raw, ok := container["monitor_aggregate"].(map[string]any)
	if !ok {
		return monitorAggregateState{}, false
	}
	quantityLots, quantityOK := int64FromAny(raw["quantity_lots"])
	filledLots, filledOK := int64FromAny(raw["filled_lots"])
	avgFillPrice, avgOK := decimalFromAny(raw["avg_fill_price"])
	commission, commissionOK := decimalFromAny(raw["commission"])
	if !quantityOK || !filledOK {
		return monitorAggregateState{}, false
	}
	if !avgOK {
		avgFillPrice = decimal.Zero
	}
	if !commissionOK {
		commission = decimal.Zero
	}
	return monitorAggregateState{
		QuantityLots: quantityLots,
		FilledLots:   filledLots,
		AvgFillPrice: avgFillPrice,
		Commission:   commission,
	}, true
}

func withMonitorAggregate(raw string, aggregate domain.Order) string {
	root := rawStateObject(raw)
	local := localObjectForMutation(root)
	local["monitor_aggregate"] = map[string]any{
		"quantity_lots":  aggregate.QuantityLots,
		"filled_lots":    aggregate.FilledLots,
		"avg_fill_price": aggregate.AvgFillPrice.String(),
		"commission":     aggregate.Commission.String(),
	}
	encoded, err := json.Marshal(root)
	if err != nil {
		return raw
	}
	return string(encoded)
}

func carryFillQuotes(raw, sourceRaw string) string {
	source := rawStateObject(sourceRaw)
	sourceLocal := source
	if local, ok := source["local"].(map[string]any); ok {
		sourceLocal = local
	}
	quotes, ok := sourceLocal["fill_quotes"].([]any)
	if !ok || len(quotes) == 0 {
		return raw
	}
	root := rawStateObject(raw)
	local := localObjectForMutation(root)
	local["fill_quotes"] = quotes
	encoded, err := json.Marshal(root)
	if err != nil {
		return raw
	}
	return string(encoded)
}

func (e *Engine) captureFillQuote(ctx context.Context, raw string, order domain.Order, cfg MonitorConfig, deltaLots int64) string {
	if deltaLots <= 0 || cfg.Quote == nil {
		return raw
	}
	book, err := cfg.Quote(ctx, order.InstrumentUID)
	if err != nil {
		return raw
	}
	bid, ask, err := bestBidAsk(book)
	if err != nil {
		return raw
	}
	root := rawStateObject(raw)
	local := localObjectForMutation(root)
	quotes, _ := local["fill_quotes"].([]any)
	fillQuote := map[string]any{
		"best_bid":          bid.String(),
		"best_ask":          ask.String(),
		"mid":               bid.Add(ask).Div(decimal.NewFromInt(2)).String(),
		"recorded_at":       e.nowUTC().Format(time.RFC3339Nano),
		"filled_lots_delta": deltaLots,
	}
	if ts := quoteTimestamp(book); !ts.IsZero() {
		fillQuote["quote_ts"] = ts.UTC().Format(time.RFC3339Nano)
	}
	local["fill_quotes"] = append(quotes, fillQuote)
	encoded, err := json.Marshal(root)
	if err != nil {
		return raw
	}
	return string(encoded)
}

func rawStateObject(raw string) map[string]any {
	var root map[string]any
	if err := json.Unmarshal([]byte(raw), &root); err != nil || root == nil {
		return map[string]any{}
	}
	return root
}

func localObjectForMutation(root map[string]any) map[string]any {
	if local, ok := root["local"].(map[string]any); ok {
		return local
	}
	if _, hasBroker := root["broker"]; hasBroker {
		local := map[string]any{}
		root["local"] = local
		return local
	}
	return root
}

func decimalFromAny(value any) (decimal.Decimal, bool) {
	switch typed := value.(type) {
	case string:
		parsed, err := decimal.NewFromString(typed)
		return parsed, err == nil
	case float64:
		return decimal.NewFromFloat(typed), true
	default:
		return decimal.Zero, false
	}
}

func int64FromAny(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case string:
		parsed, err := decimal.NewFromString(typed)
		if err != nil {
			return 0, false
		}
		return parsed.IntPart(), true
	default:
		return 0, false
	}
}

func fillDeltaLots(previous, current domain.Order) int64 {
	delta := current.FilledLots - previous.FilledLots
	if delta < 0 {
		return 0
	}
	return delta
}

func (e *Engine) persistOrderMonitorState(ctx context.Context, order domain.Order) error {
	if e.store == nil {
		return nil
	}
	return e.store.UpdateOrderStatus(ctx, order.ClientOrderID, order.Status, order.FilledLots, order.RawStateJSON)
}

func (e *Engine) lockFor(instrumentUID string) *sync.Mutex {
	value, _ := e.mu.LoadOrStore(instrumentUID, &sync.Mutex{})
	lock, ok := value.(*sync.Mutex)
	if !ok {
		lock = &sync.Mutex{}
		e.mu.Store(instrumentUID, lock)
	}
	return lock
}

func (e *Engine) nowUTC() time.Time {
	if e.clock == nil {
		return time.Now().UTC()
	}
	return e.clock.Now().UTC()
}

func (e *Engine) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	if e.clock == nil {
		return timeutil.RealClock{}.Sleep(ctx.Done(), d)
	}
	return e.clock.Sleep(ctx.Done(), d)
}

func bestBidAsk(book domain.OrderBook) (decimal.Decimal, decimal.Decimal, error) {
	bid, ok := book.BestBid()
	if !ok {
		return decimal.Zero, decimal.Zero, ErrEmptyOrderBook
	}
	ask, ok := book.BestAsk()
	if !ok {
		return decimal.Zero, decimal.Zero, ErrEmptyOrderBook
	}
	return bid, ask, nil
}

func isTerminal(status domain.OrderStatus) bool {
	switch status {
	case domain.OrderStatusFilled, domain.OrderStatusCancelled, domain.OrderStatusRejected, domain.OrderStatusExpired, domain.OrderStatusFailed:
		return true
	default:
		return false
	}
}

func mergeOrderState(base, state domain.Order) domain.Order {
	base.BrokerOrderID = state.BrokerOrderID
	base.FilledLots = state.FilledLots
	base.AvgFillPrice = state.AvgFillPrice
	base.Status = state.Status
	base.Commission = state.Commission
	base.RawStateJSON = state.RawStateJSON
	base.UpdatedAt = state.UpdatedAt
	return base
}

func mergeAggregateFill(aggregate, previous, current domain.Order) domain.Order {
	deltaLots := current.FilledLots - previous.FilledLots
	if deltaLots > 0 {
		deltaAvg := fillDeltaAvg(previous, current, deltaLots)
		previousValue := aggregate.AvgFillPrice.Mul(decimal.NewFromInt(aggregate.FilledLots))
		deltaValue := deltaAvg.Mul(decimal.NewFromInt(deltaLots))
		aggregate.FilledLots += deltaLots
		aggregate.AvgFillPrice = previousValue.Add(deltaValue).Div(decimal.NewFromInt(aggregate.FilledLots))
	}
	deltaCommission := current.Commission.Sub(previous.Commission)
	if deltaCommission.IsPositive() {
		aggregate.Commission = aggregate.Commission.Add(deltaCommission)
	}
	return aggregate
}

func fillDeltaAvg(previous, current domain.Order, deltaLots int64) decimal.Decimal {
	if deltaLots <= 0 {
		return decimal.Zero
	}
	if previous.FilledLots <= 0 {
		if current.AvgFillPrice.IsPositive() {
			return current.AvgFillPrice
		}
		return current.LimitPrice
	}
	currentValue := current.AvgFillPrice.Mul(decimal.NewFromInt(current.FilledLots))
	previousValue := previous.AvgFillPrice.Mul(decimal.NewFromInt(previous.FilledLots))
	if currentValue.GreaterThan(previousValue) {
		return currentValue.Sub(previousValue).Div(decimal.NewFromInt(deltaLots))
	}
	if current.AvgFillPrice.IsPositive() {
		return current.AvgFillPrice
	}
	return current.LimitPrice
}
