package config

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/timeutil"
)

func TestValidateRequiresAccountIDForBrokerModes(t *testing.T) {
	cfg := minimalBrokerConfig(domain.ModeSandbox)
	cfg.TInvest.AccountID = ""
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "TINVEST_ACCOUNT_ID") {
		t.Fatalf("Validate err=%v, want TINVEST_ACCOUNT_ID requirement", err)
	}
}

func TestValidateAllowsCancelCountsFreeOrderPolicy(t *testing.T) {
	cfg := minimalBrokerConfig(domain.ModeSandbox)
	cfg.Commission.FreeOrderCountPolicy = "cancel_counts"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate cancel_counts: %v", err)
	}
}

func TestValidateLiveTradeRequiresPreconditions(t *testing.T) {
	cfg := minimalBrokerConfig(domain.ModeLiveTrade)
	cfg.Live.TradeAck = liveTradeAck
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "LIVE_READONLY_DAYS") {
		t.Fatalf("Validate err=%v, want live_trade readonly precondition", err)
	}
}

func TestValidateLiveTradeAcceptsAllPreconditions(t *testing.T) {
	cfg := minimalBrokerConfig(domain.ModeLiveTrade)
	cfg.Live = validLiveTradeConfig()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate live_trade preconditions: %v", err)
	}
}

func TestLoadKeepsStrategyExpectedSlippageSeparateFromBacktest(t *testing.T) {
	t.Setenv("APP_MODE", "backtest")
	t.Setenv("STRATEGY_EXPECTED_ENTRY_SLIPPAGE_BPS", "2")
	t.Setenv("STRATEGY_EXPECTED_EXIT_SLIPPAGE_BPS", "3")
	t.Setenv("BT_ENTRY_SLIPPAGE_BPS", "11")
	t.Setenv("BT_EXIT_SLIPPAGE_BPS", "13")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Strategy.ExpectedEntrySlippageBps.Equal(decimal.NewFromInt(2)) || !cfg.Strategy.ExpectedExitSlippageBps.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("strategy slippage entry=%s exit=%s, want 2/3", cfg.Strategy.ExpectedEntrySlippageBps, cfg.Strategy.ExpectedExitSlippageBps)
	}
	if !cfg.Backtest.EntrySlippageBps.Equal(decimal.NewFromInt(11)) || !cfg.Backtest.ExitSlippageBps.Equal(decimal.NewFromInt(13)) {
		t.Fatalf("backtest slippage entry=%s exit=%s, want 11/13", cfg.Backtest.EntrySlippageBps, cfg.Backtest.ExitSlippageBps)
	}
}

func TestLoadSchedulerKnobsFromEnv(t *testing.T) {
	t.Setenv("APP_MODE", "backtest")
	t.Setenv("STRATEGY_INTERVAL_VOLUME_LOOKBACK_DAYS", "12")
	t.Setenv("RISK_SIZE_REDUCTION_WINDOW_TRADES", "7")
	t.Setenv("RISK_SIZE_REDUCTION_FACTOR", "0.25")
	t.Setenv("RISK_SIZE_REDUCTION_TRIGGER_BPS", "-5")
	t.Setenv("TINVEST_TRADING_CALENDAR_EXCHANGE", "MOEX_FOND")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Strategy.IntervalVolumeLookbackDays != 12 || cfg.Risk.SizeReductionWindowTrades != 7 {
		t.Fatalf("window config strategy=%d risk=%d, want 12/7", cfg.Strategy.IntervalVolumeLookbackDays, cfg.Risk.SizeReductionWindowTrades)
	}
	if !cfg.Risk.SizeReductionFactor.Equal(decimal.RequireFromString("0.25")) || !cfg.Risk.SizeReductionTriggerBps.Equal(decimal.NewFromInt(-5)) {
		t.Fatalf("size reduction factor=%s trigger=%s, want 0.25/-5", cfg.Risk.SizeReductionFactor, cfg.Risk.SizeReductionTriggerBps)
	}
	if cfg.TInvest.TradingCalendarExchange != "MOEX_FOND" {
		t.Fatalf("calendar exchange=%q, want MOEX_FOND", cfg.TInvest.TradingCalendarExchange)
	}
}

func minimalBrokerConfig(mode domain.Mode) Config {
	return Config{
		App: AppConfig{
			Mode:               mode,
			Timezone:           "Europe/Moscow",
			ShutdownTimeoutSec: 30,
		},
		TInvest: TInvestConfig{
			Token:             "token",
			AccountID:         "account",
			RequestTimeoutSec: 10,
		},
		DB: DBConfig{DSN: "user:pass@tcp(localhost:3306)/bot"},
		Execution: ExecutionConfig{
			EntrySignalTime:     mustTOD("18:10:00"),
			EntryWindowStart:    mustTOD("18:20:00"),
			EntryWindowEnd:      mustTOD("18:38:30"),
			NoNewEntryAfter:     mustTOD("18:38:30"),
			ExitWatchStart:      mustTOD("09:50:00"),
			ExitNotBefore:       mustTOD("10:03:00"),
			ExitWindowStart:     mustTOD("10:05:00"),
			ExitWindowEnd:       mustTOD("10:25:00"),
			HardExitDeadline:    mustTOD("10:45:00"),
			QuoteDepth:          20,
			OrderPollIntervalMS: 500,
		},
		Strategy: StrategyConfig{AllocationMethod: "equal_weight"},
		Risk: RiskConfig{
			APIOutageHaltSec:          180,
			ReconciliationWindowHours: 72,
			ReconciliationSkewSec:     10,
			CommissionToleranceRUB:    decimal.NewFromFloat(0.01),
		},
		Commission: CommissionConfig{FreeOrderCountPolicy: "submitted"},
	}
}

func validLiveTradeConfig() LiveConfig {
	return LiveConfig{
		TradeAck:                   liveTradeAck,
		ReadonlyDays:               minLiveReadonlyDays,
		PaperDays:                  minPaperDays,
		SandboxDays:                minSandboxDays,
		CommissionWhitelistChecked: true,
		TelegramTested:             true,
		KillSwitchTested:           true,
		ServerTimeChecked:          true,
		SmallCapital:               true,
	}
}

func mustTOD(raw string) timeutil.TimeOfDay {
	tod, err := timeutil.ParseTimeOfDay(raw)
	if err != nil {
		panic(err)
	}
	return tod
}
