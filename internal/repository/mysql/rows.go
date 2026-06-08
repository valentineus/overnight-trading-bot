package mysql

import (
	"database/sql"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
)

type candleRow struct {
	InstrumentUID string          `db:"instrument_uid"`
	TradeDate     time.Time       `db:"trade_date"`
	Open          decimal.Decimal `db:"open"`
	High          decimal.Decimal `db:"high"`
	Low           decimal.Decimal `db:"low"`
	Close         decimal.Decimal `db:"close"`
	VolumeLots    decimal.Decimal `db:"volume_lots"`
	Source        string          `db:"source"`
	LoadedAt      time.Time       `db:"loaded_at"`
}

func candleRowFromDomain(candle domain.Candle) candleRow {
	return candleRow{
		InstrumentUID: candle.InstrumentUID,
		TradeDate:     dateOnly(candle.TradeDate),
		Open:          candle.Open,
		High:          candle.High,
		Low:           candle.Low,
		Close:         candle.Close,
		VolumeLots:    candle.VolumeLots,
		Source:        candle.Source,
		LoadedAt:      candle.LoadedAt,
	}
}

func (r candleRow) domain() domain.Candle {
	return domain.Candle{
		InstrumentUID: r.InstrumentUID,
		TradeDate:     r.TradeDate,
		Open:          r.Open,
		High:          r.High,
		Low:           r.Low,
		Close:         r.Close,
		VolumeLots:    r.VolumeLots,
		Source:        r.Source,
		LoadedAt:      r.LoadedAt,
	}
}

type featureRow struct {
	InstrumentUID       string          `db:"instrument_uid"`
	TradeDate           time.Time       `db:"trade_date"`
	ROn                 decimal.Decimal `db:"r_on"`
	RDay                decimal.Decimal `db:"r_day"`
	MuOn60              decimal.Decimal `db:"mu_on_60"`
	MuOn252             decimal.Decimal `db:"mu_on_252"`
	SigmaOn60           decimal.Decimal `db:"sigma_on_60"`
	Q05On60Abs          decimal.Decimal `db:"q05_on_60_abs"`
	TStatOn60           decimal.Decimal `db:"tstat_on_60"`
	WinOn60             decimal.Decimal `db:"win_on_60"`
	EWMAOn              decimal.Decimal `db:"ewma_on"`
	SpreadBps           decimal.Decimal `db:"spread_bps"`
	HalfSpreadBps       decimal.Decimal `db:"half_spread_bps"`
	TickBps             decimal.Decimal `db:"tick_bps"`
	ADV20               decimal.Decimal `db:"adv_20"`
	ExpectedCostBps     decimal.Decimal `db:"expected_cost_bps"`
	NetEdgeBps          decimal.Decimal `db:"net_edge_bps"`
	EntryIntervalVolume decimal.Decimal `db:"entry_interval_volume"`
	ExitIntervalVolume  decimal.Decimal `db:"exit_interval_volume"`
	CalculatedAt        time.Time       `db:"calculated_at"`
}

func featureRowFromDomain(feature domain.FeatureSet) featureRow {
	return featureRow{
		InstrumentUID:       feature.InstrumentUID,
		TradeDate:           dateOnly(feature.TradeDate),
		ROn:                 feature.ROn,
		RDay:                feature.RDay,
		MuOn60:              feature.MuOn60,
		MuOn252:             feature.MuOn252,
		SigmaOn60:           feature.SigmaOn60,
		Q05On60Abs:          feature.Q05On60Abs,
		TStatOn60:           feature.TStatOn60,
		WinOn60:             feature.WinOn60,
		EWMAOn:              feature.EWMAOn,
		SpreadBps:           feature.SpreadBps,
		HalfSpreadBps:       feature.HalfSpreadBps,
		TickBps:             feature.TickBps,
		ADV20:               feature.ADV20,
		ExpectedCostBps:     feature.ExpectedCostBps,
		NetEdgeBps:          feature.NetEdgeBps,
		EntryIntervalVolume: feature.EntryIntervalVolume,
		ExitIntervalVolume:  feature.ExitIntervalVolume,
		CalculatedAt:        feature.CalculatedAt,
	}
}

func (r featureRow) domain() domain.FeatureSet {
	return domain.FeatureSet{
		InstrumentUID:       r.InstrumentUID,
		TradeDate:           r.TradeDate,
		ROn:                 r.ROn,
		RDay:                r.RDay,
		MuOn60:              r.MuOn60,
		MuOn252:             r.MuOn252,
		SigmaOn60:           r.SigmaOn60,
		Q05On60Abs:          r.Q05On60Abs,
		TStatOn60:           r.TStatOn60,
		WinOn60:             r.WinOn60,
		EWMAOn:              r.EWMAOn,
		SpreadBps:           r.SpreadBps,
		HalfSpreadBps:       r.HalfSpreadBps,
		TickBps:             r.TickBps,
		ADV20:               r.ADV20,
		ExpectedCostBps:     r.ExpectedCostBps,
		NetEdgeBps:          r.NetEdgeBps,
		EntryIntervalVolume: r.EntryIntervalVolume,
		ExitIntervalVolume:  r.ExitIntervalVolume,
		CalculatedAt:        r.CalculatedAt,
	}
}

type signalRow struct {
	ID             int64           `db:"id"`
	TradeDate      time.Time       `db:"trade_date"`
	InstrumentUID  string          `db:"instrument_uid"`
	Decision       string          `db:"decision"`
	Score          decimal.Decimal `db:"score"`
	NetEdgeBps     decimal.Decimal `db:"net_edge_bps"`
	TargetNotional decimal.Decimal `db:"target_notional"`
	TargetLots     int64           `db:"target_lots"`
	RejectReason   sql.NullString  `db:"reject_reason"`
	ContextJSON    sql.NullString  `db:"context_json"`
	CreatedAt      time.Time       `db:"created_at"`
}

func signalRowFromDomain(signal domain.Signal) signalRow {
	return signalRow{
		ID:             signal.ID,
		TradeDate:      dateOnly(signal.TradeDate),
		InstrumentUID:  signal.InstrumentUID,
		Decision:       string(signal.Decision),
		Score:          signal.Score,
		NetEdgeBps:     signal.NetEdgeBps,
		TargetNotional: signal.TargetNotional,
		TargetLots:     signal.TargetLots,
		RejectReason:   sql.NullString{String: signal.RejectReason, Valid: signal.RejectReason != ""},
		ContextJSON:    sql.NullString{String: signal.ContextJSON, Valid: signal.ContextJSON != ""},
		CreatedAt:      signal.CreatedAt,
	}
}

func (r signalRow) domain() domain.Signal {
	return domain.Signal{
		ID:             r.ID,
		TradeDate:      r.TradeDate,
		InstrumentUID:  r.InstrumentUID,
		Decision:       domain.SignalDecision(r.Decision),
		Score:          r.Score,
		NetEdgeBps:     r.NetEdgeBps,
		TargetNotional: r.TargetNotional,
		TargetLots:     r.TargetLots,
		RejectReason:   r.RejectReason.String,
		ContextJSON:    r.ContextJSON.String,
		CreatedAt:      r.CreatedAt,
	}
}

type orderRow struct {
	ClientOrderID string          `db:"client_order_id"`
	BrokerOrderID sql.NullString  `db:"broker_order_id"`
	AccountIDHash string          `db:"account_id_hash"`
	InstrumentUID string          `db:"instrument_uid"`
	TradeDate     time.Time       `db:"trade_date"`
	Side          string          `db:"side"`
	OrderType     string          `db:"order_type"`
	LimitPrice    decimal.Decimal `db:"limit_price"`
	QuantityLots  int64           `db:"quantity_lots"`
	FilledLots    int64           `db:"filled_lots"`
	AvgFillPrice  decimal.Decimal `db:"avg_fill_price"`
	Status        string          `db:"status"`
	Commission    decimal.Decimal `db:"commission"`
	AttemptNo     int             `db:"attempt_no"`
	RawStateJSON  sql.NullString  `db:"raw_state_json"`
	CreatedAt     time.Time       `db:"created_at"`
	UpdatedAt     time.Time       `db:"updated_at"`
}

func orderRowFromDomain(order domain.Order) orderRow {
	return orderRow{
		ClientOrderID: order.ClientOrderID,
		BrokerOrderID: sql.NullString{
			String: order.BrokerOrderID,
			Valid:  order.BrokerOrderID != "",
		},
		AccountIDHash: order.AccountIDHash,
		InstrumentUID: order.InstrumentUID,
		TradeDate:     dateOnly(order.TradeDate),
		Side:          string(order.Side),
		OrderType:     string(order.OrderType),
		LimitPrice:    order.LimitPrice,
		QuantityLots:  order.QuantityLots,
		FilledLots:    order.FilledLots,
		AvgFillPrice:  order.AvgFillPrice,
		Status:        string(order.Status),
		Commission:    order.Commission,
		AttemptNo:     order.AttemptNo,
		RawStateJSON: sql.NullString{
			String: order.RawStateJSON,
			Valid:  order.RawStateJSON != "",
		},
		CreatedAt: order.CreatedAt,
		UpdatedAt: order.UpdatedAt,
	}
}

func (r orderRow) domain() domain.Order {
	return domain.Order{
		ClientOrderID: r.ClientOrderID,
		BrokerOrderID: r.BrokerOrderID.String,
		AccountIDHash: r.AccountIDHash,
		InstrumentUID: r.InstrumentUID,
		TradeDate:     r.TradeDate,
		Side:          domain.Side(r.Side),
		OrderType:     domain.OrderType(r.OrderType),
		LimitPrice:    r.LimitPrice,
		QuantityLots:  r.QuantityLots,
		FilledLots:    r.FilledLots,
		AvgFillPrice:  r.AvgFillPrice,
		Status:        domain.OrderStatus(r.Status),
		Commission:    r.Commission,
		AttemptNo:     r.AttemptNo,
		RawStateJSON:  r.RawStateJSON.String,
		CreatedAt:     r.CreatedAt,
		UpdatedAt:     r.UpdatedAt,
	}
}

type positionRow struct {
	ID              int64           `db:"id"`
	AccountIDHash   string          `db:"account_id_hash"`
	InstrumentUID   string          `db:"instrument_uid"`
	OpenTradeDate   time.Time       `db:"open_trade_date"`
	Lots            int64           `db:"lots"`
	Lot             int64           `db:"lot_size"`
	ExitFilledLots  int64           `db:"exit_filled_lots"`
	AvgBuyPrice     decimal.Decimal `db:"avg_buy_price"`
	AvgSellPrice    decimal.Decimal `db:"avg_sell_price"`
	Status          string          `db:"status"`
	GrossPnL        decimal.Decimal `db:"gross_pnl"`
	NetPnL          decimal.Decimal `db:"net_pnl"`
	CommissionTotal decimal.Decimal `db:"commission_total"`
	RealizedEdgeBps decimal.Decimal `db:"realized_edge_bps"`
	OpenedAt        sql.NullTime    `db:"opened_at"`
	ClosedAt        sql.NullTime    `db:"closed_at"`
	UpdatedAt       time.Time       `db:"updated_at"`
}

func positionRowFromDomain(position domain.Position) positionRow {
	lot := position.Lot
	if lot <= 0 {
		lot = 1
	}
	return positionRow{
		ID:              position.ID,
		AccountIDHash:   position.AccountIDHash,
		InstrumentUID:   position.InstrumentUID,
		OpenTradeDate:   dateOnly(position.OpenTradeDate),
		Lots:            position.Lots,
		Lot:             lot,
		ExitFilledLots:  position.ExitFilledLots,
		AvgBuyPrice:     position.AvgBuyPrice,
		AvgSellPrice:    position.AvgSellPrice,
		Status:          string(position.Status),
		GrossPnL:        position.GrossPnL,
		NetPnL:          position.NetPnL,
		CommissionTotal: position.CommissionTotal,
		RealizedEdgeBps: position.RealizedEdgeBps,
		OpenedAt:        nullableTime(position.OpenedAt),
		ClosedAt:        nullableTime(position.ClosedAt),
		UpdatedAt:       position.UpdatedAt,
	}
}

func (r positionRow) domain() domain.Position {
	return domain.Position{
		ID:              r.ID,
		AccountIDHash:   r.AccountIDHash,
		InstrumentUID:   r.InstrumentUID,
		OpenTradeDate:   r.OpenTradeDate,
		Lots:            r.Lots,
		Lot:             r.Lot,
		ExitFilledLots:  r.ExitFilledLots,
		AvgBuyPrice:     r.AvgBuyPrice,
		AvgSellPrice:    r.AvgSellPrice,
		Status:          domain.PositionStatus(r.Status),
		GrossPnL:        r.GrossPnL,
		NetPnL:          r.NetPnL,
		CommissionTotal: r.CommissionTotal,
		RealizedEdgeBps: r.RealizedEdgeBps,
		OpenedAt:        timePtr(r.OpenedAt),
		ClosedAt:        timePtr(r.ClosedAt),
		UpdatedAt:       r.UpdatedAt,
	}
}

type riskEventRow struct {
	TS            time.Time      `db:"ts"`
	Severity      string         `db:"severity"`
	EventType     string         `db:"event_type"`
	InstrumentUID sql.NullString `db:"instrument_uid"`
	Message       string         `db:"message"`
	ContextJSON   string         `db:"raw_context_json"`
}

func riskEventRowFromDomain(event domain.RiskEvent) riskEventRow {
	return riskEventRow{
		TS:            event.TS,
		Severity:      string(event.Severity),
		EventType:     event.EventType,
		InstrumentUID: sql.NullString{String: event.InstrumentUID, Valid: event.InstrumentUID != ""},
		Message:       event.Message,
		ContextJSON:   event.ContextJSON,
	}
}

func nullableTime(t *time.Time) sql.NullTime {
	if t == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *t, Valid: true}
}

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	return &t.Time
}
