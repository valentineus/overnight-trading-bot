package config

import (
	"errors"
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/timeutil"
)

const liveTradeAck = "I_ACCEPT_RISK"
const maxQuoteDepth = 50

const (
	minLiveReadonlyDays = 20
	minPaperDays        = 20
	minSandboxDays      = 10
)

type Config struct {
	App        AppConfig        `envPrefix:"APP_"`
	TInvest    TInvestConfig    `envPrefix:"TINVEST_"`
	DB         DBConfig         `envPrefix:"DB_"`
	Telegram   TelegramConfig   `envPrefix:"TELEGRAM_"`
	Strategy   StrategyConfig   `envPrefix:"STRATEGY_"`
	Execution  ExecutionConfig  `envPrefix:"EXEC_"`
	Risk       RiskConfig       `envPrefix:"RISK_"`
	Liquidity  LiquidityConfig  `envPrefix:"LIQ_"`
	Commission CommissionConfig `envPrefix:"COMM_"`
	Backtest   BacktestConfig   `envPrefix:"BT_"`
	Live       LiveConfig       `envPrefix:"LIVE_"`

	Location *time.Location `env:"-"`
}

type AppConfig struct {
	Mode               domain.Mode `env:"MODE,required"`
	Timezone           string      `env:"TIMEZONE" envDefault:"Europe/Moscow"`
	LogLevel           string      `env:"LOG_LEVEL" envDefault:"info"`
	HealthcheckAddr    string      `env:"HEALTHCHECK_ADDR" envDefault:":3300"`
	ShutdownTimeoutSec int         `env:"SHUTDOWN_TIMEOUT_SEC" envDefault:"30"`
}

type TInvestConfig struct {
	Token                   string `env:"TOKEN"`
	AccountID               string `env:"ACCOUNT_ID"`
	Endpoint                string `env:"ENDPOINT" envDefault:"invest-public-api.tinkoff.ru:443"`
	AppName                 string `env:"APP_NAME" envDefault:"overnight-trading-bot"`
	RequestTimeoutSec       int    `env:"REQUEST_TIMEOUT_SEC" envDefault:"10"`
	RetryCount              int    `env:"RETRY_COUNT" envDefault:"3"`
	RetryBackoffSec         int    `env:"RETRY_BACKOFF_SEC" envDefault:"2"`
	UseSandbox              bool   `env:"USE_SANDBOX" envDefault:"false"`
	TradingCalendarExchange string `env:"TRADING_CALENDAR_EXCHANGE" envDefault:"MOEX"`
}

type DBConfig struct {
	DSN                 string `env:"DSN"`
	MaxOpenConns        int    `env:"MAX_OPEN_CONNS" envDefault:"20"`
	MaxIdleConns        int    `env:"MAX_IDLE_CONNS" envDefault:"5"`
	ConnMaxLifetimeMin  int    `env:"CONN_MAX_LIFETIME_MIN" envDefault:"30"`
	MigrationsAutoApply bool   `env:"MIGRATIONS_AUTO_APPLY" envDefault:"true"`
}

type TelegramConfig struct {
	BotToken     string `env:"BOT_TOKEN"`
	ChatID       int64  `env:"CHAT_ID"`
	NotifyInfo   bool   `env:"NOTIFY_INFO" envDefault:"true"`
	NotifyWarn   bool   `env:"NOTIFY_WARN" envDefault:"true"`
	NotifyAlert  bool   `env:"NOTIFY_ALERT" envDefault:"true"`
	NotifyReport bool   `env:"NOTIFY_REPORT" envDefault:"true"`
}

type StrategyConfig struct {
	RollingShort               int             `env:"ROLLING_SHORT" envDefault:"60"`
	RollingLong                int             `env:"ROLLING_LONG" envDefault:"252"`
	EWMALambda                 float64         `env:"EWMA_LAMBDA" envDefault:"0.08"`
	AllocationMethod           string          `env:"ALLOCATION_METHOD" envDefault:"equal_weight"`
	MinTStat60                 decimal.Decimal `env:"MIN_TSTAT_60" envDefault:"1.25"`
	MinWinRate60               decimal.Decimal `env:"MIN_WIN_RATE_60" envDefault:"0.55"`
	MinNetEdgeBps              decimal.Decimal `env:"MIN_NET_EDGE_BPS" envDefault:"10"`
	RiskBufferBps              decimal.Decimal `env:"RISK_BUFFER_BPS" envDefault:"5"`
	ExpectedEntrySlippageBps   decimal.Decimal `env:"EXPECTED_ENTRY_SLIPPAGE_BPS" envDefault:"8"`
	ExpectedExitSlippageBps    decimal.Decimal `env:"EXPECTED_EXIT_SLIPPAGE_BPS" envDefault:"8"`
	IntervalVolumeLookbackDays int             `env:"INTERVAL_VOLUME_LOOKBACK_DAYS" envDefault:"20"`
	MaxPositions               int             `env:"MAX_POSITIONS" envDefault:"5"`
}

type ExecutionConfig struct {
	EntrySignalTime       timeutil.TimeOfDay `env:"ENTRY_SIGNAL_TIME" envDefault:"18:10:00"`
	EntryWindowStart      timeutil.TimeOfDay `env:"ENTRY_WINDOW_START" envDefault:"18:20:00"`
	EntryWindowEnd        timeutil.TimeOfDay `env:"ENTRY_WINDOW_END" envDefault:"18:38:30"`
	NoNewEntryAfter       timeutil.TimeOfDay `env:"NO_NEW_ENTRY_AFTER" envDefault:"18:38:30"`
	ExitWatchStart        timeutil.TimeOfDay `env:"EXIT_WATCH_START" envDefault:"09:50:00"`
	ExitNotBefore         timeutil.TimeOfDay `env:"EXIT_NOT_BEFORE" envDefault:"10:03:00"`
	ExitWindowStart       timeutil.TimeOfDay `env:"EXIT_WINDOW_START" envDefault:"10:05:00"`
	ExitWindowEnd         timeutil.TimeOfDay `env:"EXIT_WINDOW_END" envDefault:"10:25:00"`
	HardExitDeadline      timeutil.TimeOfDay `env:"HARD_EXIT_DEADLINE" envDefault:"10:45:00"`
	MarketClose           timeutil.TimeOfDay `env:"MARKET_CLOSE" envDefault:"18:50:00"`
	MinTimeToCloseSec     int                `env:"MIN_TIME_TO_CLOSE_SEC" envDefault:"90"`
	AllowMarketOrders     bool               `env:"ALLOW_MARKET_ORDERS" envDefault:"false"`
	MaxEntryOrderAttempts int                `env:"MAX_ENTRY_ORDER_ATTEMPTS" envDefault:"3"`
	MaxExitOrderAttempts  int                `env:"MAX_EXIT_ORDER_ATTEMPTS" envDefault:"3"`
	PassiveImproveTicks   int                `env:"PASSIVE_IMPROVE_TICKS" envDefault:"1"`
	QuoteDepth            int32              `env:"QUOTE_DEPTH" envDefault:"20"`
	MaxQuoteAgeSec        int                `env:"MAX_QUOTE_AGE_SEC" envDefault:"3"`
	OrderPollIntervalMS   int                `env:"ORDER_POLL_INTERVAL_MS" envDefault:"500"`
}

type RiskConfig struct {
	UseMargin                  bool            `env:"USE_MARGIN" envDefault:"false"`
	AllowShort                 bool            `env:"ALLOW_SHORT" envDefault:"false"`
	MaxTotalExposurePct        decimal.Decimal `env:"MAX_TOTAL_EXPOSURE_PCT" envDefault:"0.50"`
	MaxPositionPct             decimal.Decimal `env:"MAX_POSITION_PCT" envDefault:"0.10"`
	MaxDailyLossPct            decimal.Decimal `env:"MAX_DAILY_LOSS_PCT" envDefault:"0.01"`
	MaxWeeklyLossPct           decimal.Decimal `env:"MAX_WEEKLY_LOSS_PCT" envDefault:"0.03"`
	MaxMonthlyDrawdownPct      decimal.Decimal `env:"MAX_MONTHLY_DRAWDOWN_PCT" envDefault:"0.07"`
	MaxOpenPositions           int             `env:"MAX_OPEN_POSITIONS" envDefault:"5"`
	MaxAvgSlippageBps10Trades  decimal.Decimal `env:"MAX_AVG_SLIPPAGE_BPS_10_TRADES" envDefault:"15"`
	APIOutageHaltSec           int             `env:"API_OUTAGE_HALT_SEC" envDefault:"180"`
	MaxClockDriftSec           int             `env:"MAX_CLOCK_DRIFT_SEC" envDefault:"2"`
	ReconciliationWindowHours  int             `env:"RECONCILIATION_WINDOW_HOURS" envDefault:"72"`
	ReconciliationSkewSec      int             `env:"RECONCILIATION_SKEW_SEC" envDefault:"10"`
	CommissionToleranceRUB     decimal.Decimal `env:"COMMISSION_TOLERANCE_RUB" envDefault:"0.01"`
	CashUsageBuffer            decimal.Decimal `env:"CASH_USAGE_BUFFER" envDefault:"0.95"`
	RiskBudgetPerInstrumentPct decimal.Decimal `env:"RISK_BUDGET_PER_INSTRUMENT_PCT" envDefault:"0.005"`
	MinOrderNotionalRUB        decimal.Decimal `env:"MIN_ORDER_NOTIONAL_RUB" envDefault:"1000"`
	SizeReductionWindowTrades  int             `env:"SIZE_REDUCTION_WINDOW_TRADES" envDefault:"20"`
	SizeReductionFactor        decimal.Decimal `env:"SIZE_REDUCTION_FACTOR" envDefault:"0.5"`
	SizeReductionTriggerBps    decimal.Decimal `env:"SIZE_REDUCTION_TRIGGER_BPS" envDefault:"-10"`
}

type LiquidityConfig struct {
	MinADVRUB               decimal.Decimal `env:"MIN_ADV_RUB" envDefault:"5000000"`
	MaxParticipationRate    decimal.Decimal `env:"MAX_PARTICIPATION_RATE" envDefault:"0.01"`
	MaxSpreadBpsDefault     decimal.Decimal `env:"MAX_SPREAD_BPS_DEFAULT" envDefault:"20"`
	MaxSpreadBpsMoneyMarket decimal.Decimal `env:"MAX_SPREAD_BPS_MONEY_MARKET" envDefault:"5"`
	MaxSpreadBpsBondFunds   decimal.Decimal `env:"MAX_SPREAD_BPS_BOND_FUNDS" envDefault:"10"`
	MaxSpreadBpsEquityFunds decimal.Decimal `env:"MAX_SPREAD_BPS_EQUITY_FUNDS" envDefault:"25"`
	MaxTickBps              decimal.Decimal `env:"MAX_TICK_BPS" envDefault:"10"`
}

type CommissionConfig struct {
	RequireZeroCommission bool   `env:"REQUIRE_ZERO_COMMISSION" envDefault:"true"`
	QuarantineOnNonZero   bool   `env:"QUARANTINE_ON_NONZERO" envDefault:"true"`
	FreeOrderCountPolicy  string `env:"FREE_ORDER_COUNT_POLICY" envDefault:"submitted"`
}

type BacktestConfig struct {
	DateFrom               string          `env:"DATE_FROM"`
	DateTo                 string          `env:"DATE_TO"`
	EntrySlippageBps       decimal.Decimal `env:"ENTRY_SLIPPAGE_BPS" envDefault:"8"`
	ExitSlippageBps        decimal.Decimal `env:"EXIT_SLIPPAGE_BPS" envDefault:"8"`
	CommissionRoundtripBps decimal.Decimal `env:"COMMISSION_ROUNDTRIP_BPS" envDefault:"0"`
	UseMinuteModel         bool            `env:"USE_MINUTE_MODEL" envDefault:"false"`
	OutputDir              string          `env:"OUTPUT_DIR" envDefault:"./backtest_out"`
}

type LiveConfig struct {
	TradeAck                   string `env:"TRADE_ACK"`
	ReadonlyDays               int    `env:"READONLY_DAYS" envDefault:"0"`
	PaperDays                  int    `env:"PAPER_DAYS" envDefault:"0"`
	SandboxDays                int    `env:"SANDBOX_DAYS" envDefault:"0"`
	CommissionWhitelistChecked bool   `env:"COMMISSION_WHITELIST_CHECKED" envDefault:"false"`
	TelegramTested             bool   `env:"TELEGRAM_TESTED" envDefault:"false"`
	KillSwitchTested           bool   `env:"KILL_SWITCH_TESTED" envDefault:"false"`
	ServerTimeChecked          bool   `env:"SERVER_TIME_CHECKED" envDefault:"false"`
	SmallCapital               bool   `env:"SMALL_CAPITAL" envDefault:"false"`
}

func Load() (Config, error) {
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) Validate() error {
	if c.App.Mode == "" {
		return errors.New("APP_MODE is required")
	}
	loc, err := time.LoadLocation(c.App.Timezone)
	if err != nil {
		return fmt.Errorf("load timezone %q: %w", c.App.Timezone, err)
	}
	if c.App.Timezone != "Europe/Moscow" {
		return fmt.Errorf("APP_TIMEZONE must be Europe/Moscow, got %q", c.App.Timezone)
	}
	c.Location = loc

	if c.App.ShutdownTimeoutSec <= 0 {
		return errors.New("APP_SHUTDOWN_TIMEOUT_SEC must be positive")
	}
	if c.TInvest.RequestTimeoutSec <= 0 {
		return errors.New("TINVEST_REQUEST_TIMEOUT_SEC must be positive")
	}
	if c.TInvest.TradingCalendarExchange == "" {
		c.TInvest.TradingCalendarExchange = "MOEX"
	}
	if c.Execution.AllowMarketOrders {
		return errors.New("EXEC_ALLOW_MARKET_ORDERS must remain false: strategy is LIMIT-only")
	}
	if c.Execution.QuoteDepth <= 0 || c.Execution.QuoteDepth > maxQuoteDepth {
		return fmt.Errorf("EXEC_QUOTE_DEPTH must be between 1 and %d", maxQuoteDepth)
	}
	if c.Execution.OrderPollIntervalMS <= 0 {
		return errors.New("EXEC_ORDER_POLL_INTERVAL_MS must be positive")
	}
	if c.Risk.UseMargin {
		return errors.New("RISK_USE_MARGIN must remain false")
	}
	if c.Risk.AllowShort {
		return errors.New("RISK_ALLOW_SHORT must remain false")
	}
	if c.Risk.APIOutageHaltSec <= 0 {
		return errors.New("RISK_API_OUTAGE_HALT_SEC must be positive")
	}
	if c.Risk.ReconciliationWindowHours <= 0 {
		return errors.New("RISK_RECONCILIATION_WINDOW_HOURS must be positive")
	}
	if c.Risk.ReconciliationSkewSec < 0 {
		return errors.New("RISK_RECONCILIATION_SKEW_SEC must be non-negative")
	}
	if c.Risk.CommissionToleranceRUB.IsNegative() {
		return errors.New("RISK_COMMISSION_TOLERANCE_RUB must be non-negative")
	}
	if c.Risk.SizeReductionWindowTrades == 0 {
		c.Risk.SizeReductionWindowTrades = 20
	}
	if c.Risk.SizeReductionWindowTrades < 0 {
		return errors.New("RISK_SIZE_REDUCTION_WINDOW_TRADES must be positive")
	}
	if c.Risk.SizeReductionFactor.IsZero() {
		c.Risk.SizeReductionFactor = decimal.RequireFromString("0.5")
	}
	if !c.Risk.SizeReductionFactor.IsPositive() || c.Risk.SizeReductionFactor.GreaterThan(decimal.NewFromInt(1)) {
		return errors.New("RISK_SIZE_REDUCTION_FACTOR must be in (0, 1]")
	}
	if c.Commission.FreeOrderCountPolicy == "" {
		c.Commission.FreeOrderCountPolicy = "submitted"
	}
	switch c.Commission.FreeOrderCountPolicy {
	case "submitted", "cancel_counts":
	default:
		return fmt.Errorf("COMM_FREE_ORDER_COUNT_POLICY must be submitted or cancel_counts, got %q", c.Commission.FreeOrderCountPolicy)
	}
	if c.Strategy.AllocationMethod == "" {
		c.Strategy.AllocationMethod = "equal_weight"
	}
	if c.Strategy.AllocationMethod != "equal_weight" {
		return fmt.Errorf("STRATEGY_ALLOCATION_METHOD must be equal_weight, got %q", c.Strategy.AllocationMethod)
	}
	if c.Strategy.ExpectedEntrySlippageBps.IsNegative() {
		return errors.New("STRATEGY_EXPECTED_ENTRY_SLIPPAGE_BPS must be non-negative")
	}
	if c.Strategy.ExpectedExitSlippageBps.IsNegative() {
		return errors.New("STRATEGY_EXPECTED_EXIT_SLIPPAGE_BPS must be non-negative")
	}
	if c.Strategy.IntervalVolumeLookbackDays == 0 {
		c.Strategy.IntervalVolumeLookbackDays = 20
	}
	if c.Strategy.IntervalVolumeLookbackDays < 0 {
		return errors.New("STRATEGY_INTERVAL_VOLUME_LOOKBACK_DAYS must be positive")
	}
	if err := c.validateWindows(); err != nil {
		return err
	}
	if c.App.Mode != domain.ModeBacktest && c.DB.DSN == "" {
		return errors.New("DB_DSN is required outside backtest mode")
	}
	if (c.App.Mode == domain.ModeSandbox || c.App.Mode == domain.ModeLiveReadonly || c.App.Mode == domain.ModeLiveTrade) && c.TInvest.Token == "" {
		return fmt.Errorf("TINVEST_TOKEN is required for APP_MODE=%s", c.App.Mode)
	}
	if (c.App.Mode == domain.ModeSandbox || c.App.Mode == domain.ModeLiveReadonly || c.App.Mode == domain.ModeLiveTrade) && c.TInvest.AccountID == "" {
		return fmt.Errorf("TINVEST_ACCOUNT_ID is required for APP_MODE=%s", c.App.Mode)
	}
	if c.TInvest.UseSandbox && c.App.Mode != domain.ModeSandbox {
		return errors.New("TINVEST_USE_SANDBOX=true is only valid with APP_MODE=sandbox")
	}
	if c.App.Mode == domain.ModeLiveTrade {
		if c.Live.TradeAck != liveTradeAck {
			return fmt.Errorf("LIVE_TRADE_ACK=%s is required for APP_MODE=live_trade", liveTradeAck)
		}
		if err := c.validateLiveTradePreconditions(); err != nil {
			return err
		}
	}
	return nil
}

func (c Config) validateLiveTradePreconditions() error {
	if c.Live.ReadonlyDays < minLiveReadonlyDays {
		return fmt.Errorf("LIVE_READONLY_DAYS must be >= %d for APP_MODE=live_trade", minLiveReadonlyDays)
	}
	if c.Live.PaperDays < minPaperDays {
		return fmt.Errorf("LIVE_PAPER_DAYS must be >= %d for APP_MODE=live_trade", minPaperDays)
	}
	if c.Live.SandboxDays < minSandboxDays {
		return fmt.Errorf("LIVE_SANDBOX_DAYS must be >= %d for APP_MODE=live_trade", minSandboxDays)
	}
	if !c.Live.CommissionWhitelistChecked {
		return errors.New("LIVE_COMMISSION_WHITELIST_CHECKED=true is required for APP_MODE=live_trade")
	}
	if !c.Live.TelegramTested {
		return errors.New("LIVE_TELEGRAM_TESTED=true is required for APP_MODE=live_trade")
	}
	if !c.Live.KillSwitchTested {
		return errors.New("LIVE_KILL_SWITCH_TESTED=true is required for APP_MODE=live_trade")
	}
	if !c.Live.ServerTimeChecked {
		return errors.New("LIVE_SERVER_TIME_CHECKED=true is required for APP_MODE=live_trade")
	}
	if !c.Live.SmallCapital {
		return errors.New("LIVE_SMALL_CAPITAL=true is required for APP_MODE=live_trade")
	}
	return nil
}

func (c Config) validateWindows() error {
	if c.Execution.EntryWindowStart.Duration >= c.Execution.EntryWindowEnd.Duration ||
		c.Execution.EntryWindowEnd.Duration > c.Execution.NoNewEntryAfter.Duration {
		return errors.New("entry windows must satisfy EXEC_ENTRY_WINDOW_START < EXEC_ENTRY_WINDOW_END <= EXEC_NO_NEW_ENTRY_AFTER")
	}
	if c.Execution.ExitWatchStart.Duration > c.Execution.ExitNotBefore.Duration ||
		c.Execution.ExitNotBefore.Duration > c.Execution.ExitWindowStart.Duration ||
		c.Execution.ExitWindowStart.Duration >= c.Execution.ExitWindowEnd.Duration ||
		c.Execution.ExitWindowEnd.Duration > c.Execution.HardExitDeadline.Duration {
		return errors.New("exit windows must be monotonic from EXEC_EXIT_WATCH_START to EXEC_HARD_EXIT_DEADLINE")
	}
	if c.Execution.MarketClose.Duration > 0 &&
		(c.Execution.MarketClose.Duration <= c.Execution.NoNewEntryAfter.Duration ||
			c.Execution.MarketClose.Duration <= c.Execution.HardExitDeadline.Duration) {
		return errors.New("EXEC_MARKET_CLOSE must be after entry and exit trading windows")
	}
	return nil
}
