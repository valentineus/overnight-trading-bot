package signal

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
)

const (
	ReasonDisabled        = "instrument_disabled"
	ReasonQuarantine      = "instrument_quarantine"
	ReasonMetadataInvalid = "metadata_invalid"
	ReasonTradingStatus   = "trading_status_not_normal"
	ReasonCommission      = "commission_nonzero"
	ReasonMuShort         = "mu_on_60_non_positive"
	ReasonMuLong          = "mu_on_252_non_positive"
	ReasonSigmaZero       = "sigma_on_60_zero"
	ReasonTStat           = "tstat_on_60_below_threshold"
	ReasonWinRate         = "win_on_60_below_threshold"
	ReasonNetEdge         = "net_edge_bps_below_threshold"
	ReasonSpread          = "spread_bps_above_limit"
	ReasonTick            = "tick_bps_above_limit"
	ReasonADV             = "adv_20_below_limit"
	ReasonFreeOrders      = "free_order_budget_insufficient"
	ReasonMaxPositions    = "max_positions_reached"
)

type Config struct {
	MinTStat60              decimal.Decimal
	MinWinRate60            decimal.Decimal
	MinNetEdgeBps           decimal.Decimal
	MinADVRUB               decimal.Decimal
	MaxSpreadBpsDefault     decimal.Decimal
	MaxSpreadBpsMoneyMarket decimal.Decimal
	MaxSpreadBpsBondFunds   decimal.Decimal
	MaxSpreadBpsEquityFunds decimal.Decimal
	MaxTickBps              decimal.Decimal
	RequireZeroCommission   bool
	MaxPositions            int
}

type Candidate struct {
	Instrument    domain.Instrument
	Features      domain.FeatureSet
	TradingStatus domain.TradingStatus
	FreeOrderOK   bool
	OpenPositions int
	TradeDate     time.Time
	ExtraContext  map[string]any
}

type Engine struct {
	cfg Config
}

func New(cfg Config) Engine {
	return Engine{cfg: cfg}
}

func (e Engine) Evaluate(c Candidate) domain.Signal {
	reason := e.firstRejectReason(c)
	decision := domain.DecisionEnter
	if reason != "" {
		decision = domain.DecisionReject
	}
	if isSkipReason(reason) {
		decision = domain.DecisionSkip
	}
	context := map[string]any{
		"ticker":         c.Instrument.Ticker,
		"fund_type":      c.Instrument.FundType,
		"trading_status": c.TradingStatus,
		"spread_limit":   e.spreadLimit(c.Instrument).String(),
	}
	for k, v := range c.ExtraContext {
		context[k] = v
	}
	raw, _ := json.Marshal(context)
	return domain.Signal{
		TradeDate:     c.TradeDate,
		InstrumentUID: c.Instrument.InstrumentUID,
		Decision:      decision,
		Score:         c.Features.NetEdgeBps,
		NetEdgeBps:    c.Features.NetEdgeBps,
		RejectReason:  reason,
		ContextJSON:   string(raw),
		CreatedAt:     time.Now().UTC(),
	}
}

func isSkipReason(reason string) bool {
	return reason == ReasonFreeOrders || reason == ReasonMaxPositions
}

func (e Engine) firstRejectReason(c Candidate) string {
	instr := c.Instrument
	features := c.Features
	switch {
	case !instr.Enabled:
		return ReasonDisabled
	case instr.Quarantine:
		return ReasonQuarantine
	case !instr.MetadataValid():
		return ReasonMetadataInvalid
	case c.TradingStatus != domain.TradingStatusNormal:
		return ReasonTradingStatus
	case e.cfg.RequireZeroCommission && instr.ExpectedCommissionBpsPerSide.IsPositive():
		return ReasonCommission
	case !features.MuOn60.IsPositive():
		return ReasonMuShort
	case !features.MuOn252.IsPositive():
		return ReasonMuLong
	case !features.SigmaOn60.IsPositive():
		return ReasonSigmaZero
	case features.TStatOn60.LessThan(e.cfg.MinTStat60):
		return ReasonTStat
	case features.WinOn60.LessThan(e.cfg.MinWinRate60):
		return ReasonWinRate
	case features.NetEdgeBps.LessThan(e.cfg.MinNetEdgeBps):
		return ReasonNetEdge
	case features.SpreadBps.GreaterThan(e.spreadLimit(instr)):
		return ReasonSpread
	case features.TickBps.GreaterThan(e.cfg.MaxTickBps):
		return ReasonTick
	case features.ADV20.LessThan(e.cfg.MinADVRUB):
		return ReasonADV
	case !c.FreeOrderOK:
		return ReasonFreeOrders
	case e.cfg.MaxPositions > 0 && c.OpenPositions >= e.cfg.MaxPositions:
		return ReasonMaxPositions
	default:
		return ""
	}
}

func (e Engine) spreadLimit(instr domain.Instrument) decimal.Decimal {
	fundType := strings.ToLower(instr.FundType)
	switch {
	case strings.Contains(fundType, "money"):
		return e.cfg.MaxSpreadBpsMoneyMarket
	case strings.Contains(fundType, "bond"):
		return e.cfg.MaxSpreadBpsBondFunds
	case strings.Contains(fundType, "equity"):
		return e.cfg.MaxSpreadBpsEquityFunds
	default:
		return e.cfg.MaxSpreadBpsDefault
	}
}
