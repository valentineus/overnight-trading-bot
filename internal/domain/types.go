package domain

import (
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

type Mode string

const (
	ModeBacktest     Mode = "backtest"
	ModePaper        Mode = "paper"
	ModeSandbox      Mode = "sandbox"
	ModeLiveReadonly Mode = "live_readonly"
	ModeLiveTrade    Mode = "live_trade"
)

func ParseMode(raw string) (Mode, error) {
	mode := Mode(strings.TrimSpace(raw))
	switch mode {
	case ModeBacktest, ModePaper, ModeSandbox, ModeLiveReadonly, ModeLiveTrade:
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported app mode %q", raw)
	}
}

func (m Mode) AllowsBrokerOrders() bool {
	return m == ModeSandbox || m == ModeLiveTrade
}

func (m *Mode) UnmarshalText(text []byte) error {
	mode, err := ParseMode(string(text))
	if err != nil {
		return err
	}
	*m = mode
	return nil
}

type Side string

const (
	SideBuy  Side = "BUY"
	SideSell Side = "SELL"
)

type OrderType string

const (
	OrderTypeLimit OrderType = "LIMIT"
)

type OrderStatus string

const (
	OrderStatusNew             OrderStatus = "NEW"
	OrderStatusSent            OrderStatus = "SENT"
	OrderStatusPartiallyFilled OrderStatus = "PARTIALLY_FILLED"
	OrderStatusFilled          OrderStatus = "FILLED"
	OrderStatusCancelled       OrderStatus = "CANCELLED"
	OrderStatusRejected        OrderStatus = "REJECTED"
	OrderStatusExpired         OrderStatus = "EXPIRED"
	OrderStatusFailed          OrderStatus = "FAILED"
)

type SignalDecision string

const (
	DecisionEnter  SignalDecision = "ENTER"
	DecisionSkip   SignalDecision = "SKIP"
	DecisionReject SignalDecision = "REJECT"
)

type PositionStatus string

const (
	PositionNoPosition           PositionStatus = "NO_POSITION"
	PositionEntrySignalled       PositionStatus = "ENTRY_SIGNALLED"
	PositionEntryOrderSent       PositionStatus = "ENTRY_ORDER_SENT"
	PositionEntryPartiallyFilled PositionStatus = "ENTRY_PARTIALLY_FILLED"
	PositionEntryFilled          PositionStatus = "ENTRY_FILLED"
	PositionHoldingOvernight     PositionStatus = "HOLDING_OVERNIGHT"
	PositionExitOrderSent        PositionStatus = "EXIT_ORDER_SENT"
	PositionExitPartiallyFilled  PositionStatus = "EXIT_PARTIALLY_FILLED"
	PositionExitFilled           PositionStatus = "EXIT_FILLED"
	PositionExitFailed           PositionStatus = "EXIT_FAILED"
	PositionQuarantine           PositionStatus = "QUARANTINE"
)

type SystemState string

const (
	StateInit               SystemState = "INIT"
	StateSyncInstruments    SystemState = "SYNC_INSTRUMENTS"
	StateSyncMarketData     SystemState = "SYNC_MARKET_DATA"
	StateGenerateSignals    SystemState = "GENERATE_SIGNALS"
	StateWaitEntryWindow    SystemState = "WAIT_ENTRY_WINDOW"
	StatePlaceEntryOrders   SystemState = "PLACE_ENTRY_ORDERS"
	StateMonitorEntryOrders SystemState = "MONITOR_ENTRY_ORDERS"
	StateHoldOvernight      SystemState = "HOLD_OVERNIGHT"
	StateWaitExitWindow     SystemState = "WAIT_EXIT_WINDOW"
	StatePlaceExitOrders    SystemState = "PLACE_EXIT_ORDERS"
	StateMonitorExitOrders  SystemState = "MONITOR_EXIT_ORDERS"
	StateReconcile          SystemState = "RECONCILE"
	StateReport             SystemState = "REPORT"
	StateSleep              SystemState = "SLEEP"
	StateHalted             SystemState = "HALTED"
)

type Severity string

const (
	SeverityInfo     Severity = "INFO"
	SeverityWarn     Severity = "WARN"
	SeverityAlert    Severity = "ALERT"
	SeverityCritical Severity = "CRITICAL"
)

type TradingStatus string

const (
	TradingStatusNormal  TradingStatus = "NORMAL_TRADING"
	TradingStatusClosed  TradingStatus = "CLOSED"
	TradingStatusUnknown TradingStatus = "UNKNOWN"
)

type Instrument struct {
	InstrumentUID                string
	Figi                         string
	Ticker                       string
	ClassCode                    string
	Name                         string
	Lot                          int64
	MinPriceIncrement            decimal.Decimal
	Currency                     string
	Enabled                      bool
	FundType                     string
	ExpectedCommissionBpsPerSide decimal.Decimal
	FreeOrderLimitPerDay         int
	Quarantine                   bool
	QuarantineReason             string
	ExcludeReason                string
	UpdatedAt                    time.Time
}

func (i Instrument) MetadataValid() bool {
	return i.InstrumentUID != "" &&
		!strings.HasPrefix(i.InstrumentUID, "PENDING:") &&
		i.Lot > 0 &&
		i.MinPriceIncrement.IsPositive() &&
		strings.EqualFold(i.Currency, "RUB")
}

type Candle struct {
	InstrumentUID string
	TradeDate     time.Time
	Open          decimal.Decimal
	High          decimal.Decimal
	Low           decimal.Decimal
	Close         decimal.Decimal
	VolumeLots    decimal.Decimal
	Source        string
	LoadedAt      time.Time
}

type FeatureSet struct {
	InstrumentUID       string
	TradeDate           time.Time
	ROn                 decimal.Decimal
	RDay                decimal.Decimal
	MuOn60              decimal.Decimal
	MuOn252             decimal.Decimal
	SigmaOn60           decimal.Decimal
	TStatOn60           decimal.Decimal
	WinOn60             decimal.Decimal
	EWMAOn              decimal.Decimal
	SpreadBps           decimal.Decimal
	HalfSpreadBps       decimal.Decimal
	TickBps             decimal.Decimal
	ADV20               decimal.Decimal
	ExpectedCostBps     decimal.Decimal
	NetEdgeBps          decimal.Decimal
	EntryIntervalVolume decimal.Decimal
	ExitIntervalVolume  decimal.Decimal
	CalculatedAt        time.Time
}

type Signal struct {
	ID             int64
	TradeDate      time.Time
	InstrumentUID  string
	Decision       SignalDecision
	Score          decimal.Decimal
	NetEdgeBps     decimal.Decimal
	TargetNotional decimal.Decimal
	TargetLots     int64
	RejectReason   string
	ContextJSON    string
	CreatedAt      time.Time
}

type Order struct {
	ClientOrderID string
	BrokerOrderID string
	AccountIDHash string
	InstrumentUID string
	TradeDate     time.Time
	Side          Side
	OrderType     OrderType
	LimitPrice    decimal.Decimal
	QuantityLots  int64
	FilledLots    int64
	AvgFillPrice  decimal.Decimal
	Status        OrderStatus
	Commission    decimal.Decimal
	AttemptNo     int
	RawStateJSON  string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Position struct {
	ID              int64
	AccountIDHash   string
	InstrumentUID   string
	OpenTradeDate   time.Time
	Lots            int64
	Lot             int64
	ExitFilledLots  int64
	AvgBuyPrice     decimal.Decimal
	AvgSellPrice    decimal.Decimal
	Status          PositionStatus
	GrossPnL        decimal.Decimal
	NetPnL          decimal.Decimal
	CommissionTotal decimal.Decimal
	RealizedEdgeBps decimal.Decimal
	OpenedAt        *time.Time
	ClosedAt        *time.Time
	UpdatedAt       time.Time
}

type RiskEvent struct {
	ID            int64
	TS            time.Time
	Severity      Severity
	EventType     string
	InstrumentUID string
	Message       string
	ContextJSON   string
}

type Holding struct {
	InstrumentUID string
	QuantityLots  int64
	AveragePrice  decimal.Decimal
	MarketValue   decimal.Decimal
}

type Portfolio struct {
	Equity    decimal.Decimal
	Cash      decimal.Decimal
	Holdings  []Holding
	CheckedAt time.Time
}

type OrderBookLevel struct {
	Price        decimal.Decimal
	QuantityLots int64
}

type OrderBook struct {
	InstrumentUID string
	Bids          []OrderBookLevel
	Asks          []OrderBookLevel
	Time          time.Time
	ReceivedAt    time.Time
}

func (o OrderBook) BestBid() (decimal.Decimal, bool) {
	if len(o.Bids) == 0 {
		return decimal.Zero, false
	}
	return o.Bids[0].Price, true
}

func (o OrderBook) BestAsk() (decimal.Decimal, bool) {
	if len(o.Asks) == 0 {
		return decimal.Zero, false
	}
	return o.Asks[0].Price, true
}

type Operation struct {
	ID            string
	InstrumentUID string
	Type          string
	Payment       decimal.Decimal
	Commission    decimal.Decimal
	ExecutedAt    time.Time
}

type ReconciliationDiff struct {
	Kind          string
	InstrumentUID string
	Message       string
	Critical      bool
}
