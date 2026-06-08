package risk

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/money"
)

var (
	ErrNoSizingCapacity           = errors.New("no sizing capacity")
	ErrFreeOrderBudget            = errors.New("free order budget is insufficient")
	ErrFreeOrderPolicyUnspecified = errors.New("free order policy is not configured")
)

type SizingConfig struct {
	MaxPositionPct             decimal.Decimal
	MaxTotalExposurePct        decimal.Decimal
	MaxParticipationRate       decimal.Decimal
	CashUsageBuffer            decimal.Decimal
	RiskBudgetPerInstrumentPct decimal.Decimal
	MinOrderNotionalRUB        decimal.Decimal
}

type SizingInput struct {
	Portfolio           domain.Portfolio
	SelectedInstruments int
	ExistingExposure    decimal.Decimal
	ReservedCash        decimal.Decimal
	LimitPrice          decimal.Decimal
	Lot                 int64
	EntryIntervalVolume decimal.Decimal
	ExitIntervalVolume  decimal.Decimal
	Q05OvernightAbs     decimal.Decimal
}

type SizingResult struct {
	TargetNotional decimal.Decimal
	Lots           int64
	Reason         string
	Limits         map[string]decimal.Decimal
}

type Sizer struct {
	cfg        SizingConfig
	sizeFactor decimal.Decimal
}

func NewSizer(cfg SizingConfig) Sizer {
	return Sizer{cfg: cfg, sizeFactor: decimal.NewFromInt(1)}
}

func (s Sizer) WithSizeFactor(factor decimal.Decimal) Sizer {
	if !factor.IsPositive() {
		factor = decimal.NewFromInt(1)
	}
	s.sizeFactor = factor
	return s
}

func (s Sizer) Size(input SizingInput) SizingResult {
	limits := make(map[string]decimal.Decimal, 6)
	if input.SelectedInstruments <= 0 {
		input.SelectedInstruments = 1
	}
	capLimit := input.Portfolio.Equity.Mul(s.cfg.MaxPositionPct)
	totalExposureLimit := input.Portfolio.Equity.Mul(s.cfg.MaxTotalExposurePct)
	remainingExposure := totalExposureLimit.Sub(input.ExistingExposure)
	if remainingExposure.IsNegative() {
		remainingExposure = decimal.Zero
	}
	exposureLimit := remainingExposure.Div(decimal.NewFromInt(int64(input.SelectedInstruments)))
	liquidityLimit := money.Min(input.EntryIntervalVolume, input.ExitIntervalVolume).
		Mul(s.cfg.MaxParticipationRate)
	availableCash := input.Portfolio.Cash.Sub(input.ReservedCash)
	if availableCash.IsNegative() {
		availableCash = decimal.Zero
	}
	cashLimit := availableCash.Mul(s.cfg.CashUsageBuffer)
	riskLimit := capLimit
	if input.Q05OvernightAbs.IsPositive() {
		riskBudget := input.Portfolio.Equity.Mul(s.cfg.RiskBudgetPerInstrumentPct)
		riskLimit = riskBudget.Div(input.Q05OvernightAbs)
	}
	limits["cap"] = capLimit
	limits["exposure"] = exposureLimit
	limits["liquidity"] = liquidityLimit
	limits["risk"] = riskLimit
	limits["cash"] = cashLimit

	sizeFactor := s.effectiveSizeFactor()
	limits["size_factor"] = sizeFactor
	target := money.Min(capLimit, exposureLimit, liquidityLimit, riskLimit, cashLimit).Mul(sizeFactor)
	if !target.IsPositive() || !input.LimitPrice.IsPositive() || input.Lot <= 0 {
		return SizingResult{Reason: "non_positive_limit", Limits: limits}
	}
	lotNotional := input.LimitPrice.Mul(decimal.NewFromInt(input.Lot))
	lots := target.Div(lotNotional).Floor().IntPart()
	notional := lotNotional.Mul(decimal.NewFromInt(lots))
	if lots < 1 {
		return SizingResult{TargetNotional: notional, Lots: lots, Reason: "lots_below_one", Limits: limits}
	}
	if notional.LessThan(s.cfg.MinOrderNotionalRUB) {
		return SizingResult{TargetNotional: notional, Lots: 0, Reason: "min_order_notional", Limits: limits}
	}
	return SizingResult{TargetNotional: notional, Lots: lots, Limits: limits}
}

func (s Sizer) effectiveSizeFactor() decimal.Decimal {
	if !s.sizeFactor.IsPositive() {
		return decimal.NewFromInt(1)
	}
	return s.sizeFactor
}

type FreeOrderStore interface {
	GetFreeOrdersSent(ctx context.Context, tradeDate time.Time, instrumentUID string) (int, error)
	IncrementFreeOrders(ctx context.Context, tradeDate time.Time, instrumentUID string, delta int) error
}

type FreeOrderBudget struct {
	store FreeOrderStore
}

func NewFreeOrderBudget(store FreeOrderStore) FreeOrderBudget {
	return FreeOrderBudget{store: store}
}

func (b FreeOrderBudget) Check(ctx context.Context, tradeDate time.Time, instr domain.Instrument, ordersNeeded int) (int, error) {
	if instr.FreeOrderLimitPerDay < 0 {
		return 0, nil
	}
	if instr.FreeOrderLimitPerDay == 0 {
		return 0, ErrFreeOrderPolicyUnspecified
	}
	sent, err := b.store.GetFreeOrdersSent(ctx, tradeDate, instr.InstrumentUID)
	if err != nil {
		return 0, err
	}
	remaining := instr.FreeOrderLimitPerDay - sent
	if remaining < ordersNeeded {
		return remaining, ErrFreeOrderBudget
	}
	return remaining, nil
}

func (b FreeOrderBudget) Submitted(ctx context.Context, tradeDate time.Time, instrumentUID string) error {
	return b.store.IncrementFreeOrders(ctx, tradeDate, instrumentUID, 1)
}

type MemoryFreeOrderStore struct {
	mu     sync.Mutex
	counts map[string]int
}

func NewMemoryFreeOrderStore() *MemoryFreeOrderStore {
	return &MemoryFreeOrderStore{counts: make(map[string]int)}
}

func (s *MemoryFreeOrderStore) GetFreeOrdersSent(_ context.Context, tradeDate time.Time, instrumentUID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.counts[freeOrderKey(tradeDate, instrumentUID)], nil
}

func (s *MemoryFreeOrderStore) IncrementFreeOrders(_ context.Context, tradeDate time.Time, instrumentUID string, delta int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.counts[freeOrderKey(tradeDate, instrumentUID)] += delta
	return nil
}

func freeOrderKey(tradeDate time.Time, instrumentUID string) string {
	return tradeDate.Format("2006-01-02") + "|" + instrumentUID
}
