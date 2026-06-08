package execution

import (
	"context"
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
		RawStateJSON:  "{}",
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
		RawStateJSON:  "{}",
	}, instrument.FreeOrderLimitPerDay)
}

func (e *Engine) PlaceLimit(ctx context.Context, order domain.Order) (domain.Order, error) {
	return e.placeLimit(ctx, order, 0)
}

func (e *Engine) placeLimit(ctx context.Context, order domain.Order, freeOrderLimit int) (domain.Order, error) {
	lock := e.lockFor(order.InstrumentUID)
	lock.Lock()
	defer lock.Unlock()
	if e.mode != domain.ModePaper && !e.mode.AllowsBrokerOrders() {
		return order, ErrBrokerOrdersDisabled
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
	if e.mode == domain.ModePaper {
		return e.placePaperLimit(ctx, order, freeOrderLimit)
	}
	if e.gateway == nil {
		return domain.Order{}, errors.New("gateway is nil")
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
			_ = e.store.UpsertOrder(ctx, draft)
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
	posted.CreatedAt = now
	posted.UpdatedAt = posted.CreatedAt
	if e.store != nil {
		if err := e.store.UpsertOrder(ctx, posted); err != nil {
			return domain.Order{}, err
		}
	}
	return posted, nil
}

func (e *Engine) placePaperLimit(ctx context.Context, order domain.Order, freeOrderLimit int) (domain.Order, error) {
	now := e.nowUTC()
	order.BrokerOrderID = "paper-" + order.ClientOrderID
	order.FilledLots = order.QuantityLots
	order.AvgFillPrice = order.LimitPrice
	order.Status = domain.OrderStatusFilled
	order.RawStateJSON = `{"paper_fill":true}`
	order.CreatedAt = now
	order.UpdatedAt = now
	if e.store != nil {
		if err := e.store.RunInTx(ctx, func(ctx context.Context, repo repository.Repository) error {
			if err := repo.UpsertOrder(ctx, order); err != nil {
				return fmt.Errorf("persist paper order: %w", err)
			}
			return repo.ReserveFreeOrders(ctx, order.TradeDate, order.InstrumentUID, 1, freeOrderLimit)
		}); err != nil {
			return domain.Order{}, err
		}
	}
	return order, nil
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
	aggregate := order
	seen := map[string]domain.Order{order.ClientOrderID: order}
	for {
		previous := seen[current.ClientOrderID]
		refreshed, err := e.Refresh(ctx, current)
		if err != nil {
			return aggregate, err
		}
		aggregate = mergeAggregateFill(aggregate, previous, refreshed)
		seen[current.ClientOrderID] = refreshed
		current = mergeOrderState(current, refreshed)
		aggregate.Status = current.Status
		aggregate.UpdatedAt = current.UpdatedAt
		aggregate.RawStateJSON = current.RawStateJSON
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
				aggregate = mergeAggregateFill(aggregate, previous, result.Cancelled)
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
	previous := order
	refreshed, err := e.Refresh(ctx, order)
	if err != nil {
		return order, err
	}
	aggregate := mergeAggregateFill(order, previous, refreshed)
	current := mergeOrderState(order, refreshed)
	aggregate.Status = current.Status
	aggregate.UpdatedAt = current.UpdatedAt
	aggregate.RawStateJSON = current.RawStateJSON
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
			aggregate = mergeAggregateFill(aggregate, current, result.Cancelled)
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
			aggregate.RawStateJSON = result.Current.RawStateJSON
			aggregate.UpdatedAt = result.Current.UpdatedAt
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
