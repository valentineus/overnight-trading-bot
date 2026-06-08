package reconciliation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/money"
	"overnight-trading-bot/internal/repository"
	"overnight-trading-bot/internal/timeutil"
	"overnight-trading-bot/internal/tinvest"
)

var defaultCommissionTolerance = decimal.RequireFromString("0.01")

type Engine struct {
	mu                    *sync.Mutex
	repo                  repository.Repository
	gateway               tinvest.Gateway
	accountID             string
	accountIDHash         string
	window                time.Duration
	inFlightGrace         time.Duration
	commissionTolerance   decimal.Decimal
	requireZeroCommission bool
	quarantineOnNonZero   bool
	clock                 timeutil.Clock
}

func New(repo repository.Repository, gateway tinvest.Gateway, accountID, accountIDHash string) Engine {
	return Engine{
		mu:                  &sync.Mutex{},
		repo:                repo,
		gateway:             gateway,
		accountID:           accountID,
		accountIDHash:       accountIDHash,
		window:              72 * time.Hour,
		commissionTolerance: defaultCommissionTolerance,
		clock:               timeutil.RealClock{},
	}
}

func (e Engine) WithWindow(window time.Duration) Engine {
	if window > 0 {
		e.window = window
	}
	return e
}

func (e Engine) WithInFlightGrace(grace time.Duration) Engine {
	if grace >= 0 {
		e.inFlightGrace = grace
	}
	return e
}

func (e Engine) WithCommissionPolicy(requireZero, quarantineOnNonZero bool, tolerance decimal.Decimal) Engine {
	e.requireZeroCommission = requireZero
	e.quarantineOnNonZero = quarantineOnNonZero
	if !tolerance.IsNegative() {
		e.commissionTolerance = tolerance
	}
	return e
}

func (e Engine) WithClock(clock timeutil.Clock) Engine {
	if clock != nil {
		e.clock = clock
	}
	return e
}

func (e Engine) Run(ctx context.Context) ([]domain.ReconciliationDiff, error) {
	if e.mu != nil {
		e.mu.Lock()
		defer e.mu.Unlock()
	}
	localOrders, err := e.repo.ListActiveOrders(ctx, e.accountIDHash)
	if err != nil {
		return nil, err
	}
	brokerOrders, err := e.gateway.GetActiveOrders(ctx, e.accountID)
	if err != nil {
		return nil, err
	}
	now := e.nowUTC()
	localByBroker := make(map[string]domain.Order, len(localOrders))
	brokerByID := make(map[string]domain.Order, len(brokerOrders))
	for _, order := range localOrders {
		if order.BrokerOrderID != "" {
			localByBroker[order.BrokerOrderID] = order
		}
	}
	var diffs []domain.ReconciliationDiff
	for _, brokerOrder := range brokerOrders {
		brokerByID[brokerOrder.BrokerOrderID] = brokerOrder
		if _, ok := localByBroker[brokerOrder.BrokerOrderID]; !ok {
			diffs = append(diffs, domain.ReconciliationDiff{
				Kind:          "unknown_active_order",
				InstrumentUID: brokerOrder.InstrumentUID,
				Message:       fmt.Sprintf("broker order %s is not known locally", brokerOrder.BrokerOrderID),
				Critical:      true,
			})
		}
	}
	for _, localOrder := range localOrders {
		if e.isInFlight(localOrder, now) {
			continue
		}
		if localOrder.BrokerOrderID == "" {
			diffs = append(diffs, domain.ReconciliationDiff{
				Kind:          "local_order_without_broker_id",
				InstrumentUID: localOrder.InstrumentUID,
				Message:       fmt.Sprintf("local order %s is active without broker order id", localOrder.ClientOrderID),
				Critical:      true,
			})
			continue
		}
		if _, ok := brokerByID[localOrder.BrokerOrderID]; !ok {
			diffs = append(diffs, domain.ReconciliationDiff{
				Kind:          "missing_local_order",
				InstrumentUID: localOrder.InstrumentUID,
				Message:       fmt.Sprintf("local active order %s/%s is not active at broker", localOrder.ClientOrderID, localOrder.BrokerOrderID),
				Critical:      true,
			})
		}
	}
	localPositions, err := e.repo.ListOpenPositions(ctx, e.accountIDHash)
	if err != nil {
		return nil, err
	}
	portfolio, err := e.gateway.GetPortfolio(ctx, e.accountID)
	if err != nil {
		return nil, err
	}
	brokerLots := make(map[string]int64, len(portfolio.Holdings))
	for _, holding := range portfolio.Holdings {
		brokerLots[holding.InstrumentUID] += holding.QuantityLots
	}
	for _, pos := range localPositions {
		if brokerLots[pos.InstrumentUID] != pos.Lots {
			diffs = append(diffs, domain.ReconciliationDiff{
				Kind:          "position_lots_mismatch",
				InstrumentUID: pos.InstrumentUID,
				Message:       fmt.Sprintf("local lots=%d broker lots=%d", pos.Lots, brokerLots[pos.InstrumentUID]),
				Critical:      true,
			})
		}
	}
	localLots := make(map[string]int64, len(localPositions))
	for _, pos := range localPositions {
		localLots[pos.InstrumentUID] += pos.Lots
	}
	for instrumentUID, lots := range brokerLots {
		if lots > 0 && localLots[instrumentUID] == 0 {
			diffs = append(diffs, domain.ReconciliationDiff{
				Kind:          "unknown_broker_position",
				InstrumentUID: instrumentUID,
				Message:       fmt.Sprintf("broker holds %d lots but local position is absent", lots),
				Critical:      true,
			})
		}
	}
	diffs = append(diffs, compareCash(localPositions, portfolio, e.commissionTolerance)...)
	from := now.Add(-e.window)
	recentOrders, err := e.repo.ListOrders(ctx, e.accountIDHash, from, now)
	if err != nil {
		return nil, err
	}
	operations, err := e.gateway.GetOperations(ctx, e.accountID, from, now)
	if err != nil {
		return nil, err
	}
	diffs = append(diffs, compareOperationsWithPolicy(recentOrders, operations, e.requireZeroCommission, e.commissionTolerance)...)
	if e.requireZeroCommission && e.quarantineOnNonZero {
		for _, diff := range diffs {
			if diff.Kind != "actual_commission_nonzero" || diff.InstrumentUID == "" {
				continue
			}
			if err := e.repo.QuarantineInstrument(ctx, diff.InstrumentUID, diff.Message); err != nil {
				_ = e.repo.InsertRiskEvent(ctx, domain.RiskEvent{
					TS:            now,
					Severity:      domain.SeverityCritical,
					EventType:     "quarantine_failed",
					InstrumentUID: diff.InstrumentUID,
					Message:       err.Error(),
					ContextJSON:   fmt.Sprintf(`{"reconciliation_diff":%q}`, diff.Message),
				})
			}
		}
	}
	raw, _ := json.Marshal(diffs)
	if err := e.repo.InsertReconciliation(ctx, now, string(raw), len(diffs) > 0); err != nil {
		return nil, err
	}
	return diffs, nil
}

func (e Engine) nowUTC() time.Time {
	if e.clock == nil {
		return time.Now().UTC()
	}
	return e.clock.Now().UTC()
}

func (e Engine) isInFlight(order domain.Order, now time.Time) bool {
	if e.inFlightGrace <= 0 || order.CreatedAt.IsZero() {
		return false
	}
	return order.CreatedAt.After(now.Add(-e.inFlightGrace))
}

func HasCritical(diffs []domain.ReconciliationDiff) bool {
	for _, diff := range diffs {
		if diff.Critical {
			return true
		}
	}
	return false
}

func compareOperations(orders []domain.Order, operations []domain.Operation) []domain.ReconciliationDiff {
	return compareOperationsWithPolicy(orders, operations, false, defaultCommissionTolerance)
}

func compareCash(localPositions []domain.Position, portfolio domain.Portfolio, tolerance decimal.Decimal) []domain.ReconciliationDiff {
	if tolerance.IsNegative() {
		tolerance = decimal.Zero
	}
	expectedCash, ok := expectedCashFromLocalPositions(localPositions, portfolio)
	if !ok {
		return nil
	}
	diff := money.Abs(expectedCash.Sub(portfolio.Cash))
	if diff.LessThanOrEqual(tolerance) {
		return nil
	}
	return []domain.ReconciliationDiff{{
		Kind:     "cash_mismatch",
		Message:  fmt.Sprintf("expected cash=%s broker cash=%s diff=%s", expectedCash.StringFixed(2), portfolio.Cash.StringFixed(2), diff.StringFixed(2)),
		Critical: true,
	}}
}

func expectedCashFromLocalPositions(localPositions []domain.Position, portfolio domain.Portfolio) (decimal.Decimal, bool) {
	if !portfolio.Equity.IsPositive() {
		return decimal.Zero, false
	}
	if len(localPositions) == 0 {
		if len(portfolio.Holdings) != 0 {
			return decimal.Zero, false
		}
		return portfolio.Equity, true
	}
	holdingByInstrument := make(map[string]domain.Holding, len(portfolio.Holdings))
	for _, holding := range portfolio.Holdings {
		holdingByInstrument[holding.InstrumentUID] = holding
	}
	positionMarketValue := decimal.Zero
	for _, pos := range localPositions {
		if pos.Lots <= 0 {
			continue
		}
		holding, ok := holdingByInstrument[pos.InstrumentUID]
		if !ok || holding.QuantityLots <= 0 || !holding.MarketValue.IsPositive() {
			return decimal.Zero, false
		}
		positionMarketValue = positionMarketValue.Add(holding.MarketValue.
			Mul(decimal.NewFromInt(pos.Lots)).
			Div(decimal.NewFromInt(holding.QuantityLots)))
	}
	return portfolio.Equity.Sub(positionMarketValue), true
}

func compareOperationsWithPolicy(orders []domain.Order, operations []domain.Operation, requireZeroCommission bool, commissionTolerance decimal.Decimal) []domain.ReconciliationDiff {
	var diffs []domain.ReconciliationDiff
	if commissionTolerance.IsNegative() {
		commissionTolerance = decimal.Zero
	}
	localCommissionByInstrument := make(map[string]decimal.Decimal)
	localTraded := make(map[string]bool)
	for _, order := range orders {
		if order.Status == domain.OrderStatusFilled || order.Status == domain.OrderStatusPartiallyFilled {
			localCommissionByInstrument[order.InstrumentUID] = localCommissionByInstrument[order.InstrumentUID].Add(order.Commission)
			localTraded[order.InstrumentUID] = true
		}
	}
	brokerCommissionByInstrument := make(map[string]decimal.Decimal)
	brokerTraded := make(map[string]bool)
	for _, op := range operations {
		if !op.Commission.IsZero() {
			brokerCommissionByInstrument[op.InstrumentUID] = brokerCommissionByInstrument[op.InstrumentUID].Add(op.Commission)
		}
		if isTradeOperation(op.Type) {
			brokerTraded[op.InstrumentUID] = true
		}
	}
	instruments := make(map[string]struct{}, len(localCommissionByInstrument)+len(brokerCommissionByInstrument))
	for instrumentUID := range localCommissionByInstrument {
		instruments[instrumentUID] = struct{}{}
	}
	for instrumentUID := range brokerCommissionByInstrument {
		instruments[instrumentUID] = struct{}{}
	}
	for instrumentUID := range instruments {
		localCommission := localCommissionByInstrument[instrumentUID]
		brokerCommission := brokerCommissionByInstrument[instrumentUID]
		if requireZeroCommission && brokerCommission.IsPositive() {
			diffs = append(diffs, domain.ReconciliationDiff{
				Kind:          "actual_commission_nonzero",
				InstrumentUID: instrumentUID,
				Message:       fmt.Sprintf("broker commission=%s", brokerCommission.StringFixed(2)),
				Critical:      true,
			})
		}
		if diff := money.Abs(localCommission.Sub(brokerCommission)); diff.GreaterThan(commissionTolerance) {
			diffs = append(diffs, domain.ReconciliationDiff{
				Kind:          "commission_mismatch",
				InstrumentUID: instrumentUID,
				Message:       fmt.Sprintf("local commission=%s broker commission=%s", localCommission.StringFixed(2), brokerCommission.StringFixed(2)),
				Critical:      true,
			})
		}
	}
	for instrumentUID := range brokerTraded {
		if instrumentUID != "" && !localTraded[instrumentUID] {
			diffs = append(diffs, domain.ReconciliationDiff{
				Kind:          "unknown_broker_operation",
				InstrumentUID: instrumentUID,
				Message:       "broker has executed operation without local filled order",
				Critical:      true,
			})
		}
	}
	for instrumentUID := range localTraded {
		if !brokerTraded[instrumentUID] {
			diffs = append(diffs, domain.ReconciliationDiff{
				Kind:          "missing_broker_operation",
				InstrumentUID: instrumentUID,
				Message:       "local filled order has no matching broker operation in reconciliation window",
				Critical:      true,
			})
		}
	}
	return diffs
}

func isTradeOperation(raw string) bool {
	raw = strings.ToUpper(raw)
	return strings.Contains(raw, "OPERATION_TYPE_BUY") || strings.Contains(raw, "OPERATION_TYPE_SELL")
}
