package risk

import (
	"context"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
)

type EventSink interface {
	InsertRiskEvent(ctx context.Context, event domain.RiskEvent) error
	SaveSystemState(ctx context.Context, state domain.SystemState, mode domain.Mode, halted bool, reason string, contextJSON string) error
}

type Manager struct {
	sink EventSink
	cfg  ManagerConfig
}

type ManagerConfig struct {
	MaxDailyLossPct           decimal.Decimal
	MaxWeeklyLossPct          decimal.Decimal
	MaxMonthlyDrawdownPct     decimal.Decimal
	MaxAvgSlippageBps10Trades decimal.Decimal
	MaxOpenPositions          int
	MinTimeToClose            time.Duration
	MaxQuoteAge               time.Duration
}

type PreTradeInput struct {
	Portfolio             domain.Portfolio
	OpenPositions         int
	ClosingPosition       bool
	DailyPnL              decimal.Decimal
	WeeklyPnL             decimal.Decimal
	MonthlyDrawdownPct    decimal.Decimal
	AvgSlippageBps10      decimal.Decimal
	TradingStatus         domain.TradingStatus
	QuoteReceivedAt       time.Time
	Now                   time.Time
	MarketClose           time.Time
	ServerTimeUnavailable bool
	ServerClockDrift      time.Duration
	MaxClockDrift         time.Duration
	DatabaseUnavailable   bool
	UnknownBrokerOrder    bool
	UnknownBrokerHolding  bool
}

type PreTradeResult struct {
	Allowed bool
	Reason  string
}

func NewManager(sink EventSink, cfg ManagerConfig) Manager {
	return Manager{sink: sink, cfg: cfg}
}

func (m Manager) Halt(ctx context.Context, mode domain.Mode, eventType, reason string, instrumentUID string) error {
	if m.sink == nil {
		return nil
	}
	event := domain.RiskEvent{
		TS:            time.Now().UTC(),
		Severity:      domain.SeverityCritical,
		EventType:     eventType,
		InstrumentUID: instrumentUID,
		Message:       reason,
	}
	if err := m.sink.InsertRiskEvent(ctx, event); err != nil {
		return fmt.Errorf("insert halt risk event: %w", err)
	}
	if err := m.sink.SaveSystemState(ctx, domain.StateHalted, mode, true, reason, "{}"); err != nil {
		return fmt.Errorf("persist halt state: %w", err)
	}
	return nil
}

func (m Manager) PreTradeCheck(input PreTradeInput) PreTradeResult {
	now := input.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	switch {
	case input.DatabaseUnavailable:
		return reject("database_unavailable")
	case input.ServerTimeUnavailable:
		return reject("server_time_unavailable")
	case input.MaxClockDrift > 0 && input.ServerClockDrift > input.MaxClockDrift:
		return reject("server_clock_drift_too_high")
	case input.UnknownBrokerOrder:
		return reject("unknown_broker_order")
	case input.UnknownBrokerHolding:
		return reject("unknown_broker_position")
	case input.TradingStatus == domain.TradingStatusUnknown:
		return reject("trading_status_unknown_before_order")
	case input.TradingStatus != domain.TradingStatusNormal:
		return reject("trading_status_not_normal")
	case !input.ClosingPosition && m.cfg.MaxOpenPositions > 0 && input.OpenPositions >= m.cfg.MaxOpenPositions:
		return reject("max_open_positions")
	case DailyLossBreached(input.DailyPnL, input.Portfolio.Equity, m.cfg.MaxDailyLossPct):
		return reject("max_daily_loss")
	case DailyLossBreached(input.WeeklyPnL, input.Portfolio.Equity, m.cfg.MaxWeeklyLossPct):
		return reject("max_weekly_loss")
	case m.cfg.MaxMonthlyDrawdownPct.IsPositive() && input.MonthlyDrawdownPct.GreaterThanOrEqual(m.cfg.MaxMonthlyDrawdownPct):
		return reject("max_monthly_drawdown")
	case m.cfg.MaxAvgSlippageBps10Trades.IsPositive() && input.AvgSlippageBps10.GreaterThan(m.cfg.MaxAvgSlippageBps10Trades):
		return reject("max_avg_slippage_bps_10_trades")
	case m.cfg.MaxQuoteAge > 0 && !input.QuoteReceivedAt.IsZero() && now.Sub(input.QuoteReceivedAt) > m.cfg.MaxQuoteAge:
		return reject("quote_age_too_high")
	case m.cfg.MinTimeToClose > 0 && !input.MarketClose.IsZero() && input.MarketClose.Sub(now) < m.cfg.MinTimeToClose:
		return reject("min_time_to_close_sec")
	default:
		return PreTradeResult{Allowed: true}
	}
}

func DailyLossBreached(pnl, equity, maxLossPct decimal.Decimal) bool {
	if !equity.IsPositive() || !maxLossPct.IsPositive() {
		return false
	}
	limit := equity.Mul(maxLossPct).Neg()
	return pnl.LessThanOrEqual(limit)
}

func CommissionBreached(actualCommission decimal.Decimal, requireZero bool) bool {
	return requireZero && actualCommission.IsPositive()
}

func reject(reason string) PreTradeResult {
	return PreTradeResult{Allowed: false, Reason: reason}
}
