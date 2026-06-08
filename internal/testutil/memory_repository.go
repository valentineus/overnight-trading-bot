package testutil

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/repository"
	"overnight-trading-bot/internal/risk"
)

var _ repository.Repository = (*MemoryRepository)(nil)

type MemoryRepository struct {
	mu sync.Mutex

	Instruments map[string]domain.Instrument
	Daily       map[string][]domain.Candle
	Minute      map[string][]domain.Candle
	Features    map[string]domain.FeatureSet
	Signals     map[string]domain.Signal
	Orders      map[string]domain.Order
	Positions   map[int64]domain.Position
	RiskEvents  []domain.RiskEvent
	FreeOrders  map[string]int
	Reports     map[string]bool

	State      domain.SystemState
	Mode       domain.Mode
	Halted     bool
	HaltReason string

	nextPositionID int64
}

func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		Instruments:    make(map[string]domain.Instrument),
		Daily:          make(map[string][]domain.Candle),
		Minute:         make(map[string][]domain.Candle),
		Features:       make(map[string]domain.FeatureSet),
		Signals:        make(map[string]domain.Signal),
		Orders:         make(map[string]domain.Order),
		Positions:      make(map[int64]domain.Position),
		FreeOrders:     make(map[string]int),
		Reports:        make(map[string]bool),
		State:          domain.StateInit,
		Mode:           domain.ModePaper,
		nextPositionID: 1,
	}
}

func (r *MemoryRepository) RunInTx(ctx context.Context, fn func(context.Context, repository.Repository) error) error {
	return fn(ctx, r)
}

func (r *MemoryRepository) UpsertInstrument(_ context.Context, instrument domain.Instrument) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Instruments[instrument.InstrumentUID] = instrument
	return nil
}

func (r *MemoryRepository) ReplaceInstrument(_ context.Context, oldInstrumentUID string, instrument domain.Instrument) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.Instruments, oldInstrumentUID)
	r.Instruments[instrument.InstrumentUID] = instrument
	return nil
}

func (r *MemoryRepository) ListInstruments(_ context.Context, includeDisabled bool) ([]domain.Instrument, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]domain.Instrument, 0, len(r.Instruments))
	for _, instrument := range r.Instruments {
		if includeDisabled || instrument.Enabled {
			out = append(out, instrument)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ticker < out[j].Ticker })
	return out, nil
}

func (r *MemoryRepository) QuarantineInstrument(_ context.Context, instrumentUID, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	instrument := r.Instruments[instrumentUID]
	instrument.Quarantine = true
	instrument.QuarantineReason = reason
	r.Instruments[instrumentUID] = instrument
	return nil
}

func (r *MemoryRepository) UpsertDailyCandles(_ context.Context, candles []domain.Candle) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, candle := range candles {
		r.Daily[candle.InstrumentUID] = upsertCandle(r.Daily[candle.InstrumentUID], candle, false)
	}
	return nil
}

func (r *MemoryRepository) ListDailyCandles(_ context.Context, instrumentUID string, from, to time.Time) ([]domain.Candle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return filterCandles(r.Daily[instrumentUID], from, to), nil
}

func (r *MemoryRepository) UpsertMinuteCandles(_ context.Context, candles []domain.Candle) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, candle := range candles {
		r.Minute[candle.InstrumentUID] = upsertCandle(r.Minute[candle.InstrumentUID], candle, true)
	}
	return nil
}

func (r *MemoryRepository) ListMinuteCandles(_ context.Context, instrumentUID string, from, to time.Time) ([]domain.Candle, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return filterCandles(r.Minute[instrumentUID], from, to), nil
}

func (r *MemoryRepository) UpsertFeature(_ context.Context, feature domain.FeatureSet) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Features[featureKey(feature.InstrumentUID, feature.TradeDate)] = feature
	return nil
}

func (r *MemoryRepository) GetFeature(_ context.Context, instrumentUID string, tradeDate time.Time) (domain.FeatureSet, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Features[featureKey(instrumentUID, tradeDate)], nil
}

func (r *MemoryRepository) UpsertSignal(_ context.Context, signal domain.Signal) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Signals[featureKey(signal.InstrumentUID, signal.TradeDate)] = signal
	return nil
}

func (r *MemoryRepository) ListSignals(_ context.Context, tradeDate time.Time) ([]domain.Signal, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Signal
	for _, signal := range r.Signals {
		if sameDate(signal.TradeDate, tradeDate) {
			out = append(out, signal)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].InstrumentUID < out[j].InstrumentUID })
	return out, nil
}

func (r *MemoryRepository) UpsertOrder(_ context.Context, order domain.Order) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Orders[order.ClientOrderID] = order
	return nil
}

func (r *MemoryRepository) UpdateOrderStatus(_ context.Context, clientOrderID string, status domain.OrderStatus, filledLots int64, rawJSON string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	order := r.Orders[clientOrderID]
	order.Status = status
	order.FilledLots = filledLots
	order.RawStateJSON = rawJSON
	r.Orders[clientOrderID] = order
	return nil
}

func (r *MemoryRepository) ListActiveOrders(_ context.Context, accountIDHash string) ([]domain.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Order
	for _, order := range r.Orders {
		if order.AccountIDHash == accountIDHash && (order.Status == domain.OrderStatusNew || order.Status == domain.OrderStatusSent || order.Status == domain.OrderStatusPartiallyFilled) {
			out = append(out, order)
		}
	}
	return out, nil
}

func (r *MemoryRepository) ListOrders(_ context.Context, accountIDHash string, from, to time.Time) ([]domain.Order, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Order
	for _, order := range r.Orders {
		if order.AccountIDHash == accountIDHash && !order.TradeDate.Before(dateOnly(from)) && !order.TradeDate.After(dateOnly(to)) {
			out = append(out, order)
		}
	}
	return out, nil
}

func (r *MemoryRepository) UpsertPosition(_ context.Context, position domain.Position) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, existing := range r.Positions {
		if existing.AccountIDHash == position.AccountIDHash &&
			existing.InstrumentUID == position.InstrumentUID &&
			sameDate(existing.OpenTradeDate, position.OpenTradeDate) {
			position.ID = id
			r.Positions[id] = position
			return nil
		}
	}
	if position.ID == 0 {
		position.ID = r.nextPositionID
		r.nextPositionID++
	}
	r.Positions[position.ID] = position
	return nil
}

func (r *MemoryRepository) ListOpenPositions(_ context.Context, accountIDHash string) ([]domain.Position, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Position
	for _, pos := range r.Positions {
		if pos.AccountIDHash == accountIDHash && pos.Status != domain.PositionNoPosition && pos.Status != domain.PositionExitFilled && pos.Status != domain.PositionQuarantine {
			out = append(out, pos)
		}
	}
	return out, nil
}

func (r *MemoryRepository) ListPositions(_ context.Context, accountIDHash string, from, to time.Time) ([]domain.Position, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []domain.Position
	for _, pos := range r.Positions {
		if pos.AccountIDHash == accountIDHash && !pos.OpenTradeDate.Before(dateOnly(from)) && !pos.OpenTradeDate.After(dateOnly(to)) {
			out = append(out, pos)
		}
	}
	return out, nil
}

func (r *MemoryRepository) InsertRiskEvent(_ context.Context, event domain.RiskEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.RiskEvents = append(r.RiskEvents, event)
	return nil
}

func (r *MemoryRepository) GetFreeOrdersSent(_ context.Context, tradeDate time.Time, instrumentUID string) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.FreeOrders[featureKey(instrumentUID, tradeDate)], nil
}

func (r *MemoryRepository) IncrementFreeOrders(_ context.Context, tradeDate time.Time, instrumentUID string, delta int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.FreeOrders[featureKey(instrumentUID, tradeDate)] += delta
	return nil
}

func (r *MemoryRepository) ReserveFreeOrders(_ context.Context, tradeDate time.Time, instrumentUID string, delta int, limit int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if delta <= 0 {
		return nil
	}
	key := featureKey(instrumentUID, tradeDate)
	if limit > 0 && r.FreeOrders[key]+delta > limit {
		return risk.ErrFreeOrderBudget
	}
	r.FreeOrders[key] += delta
	return nil
}

func (r *MemoryRepository) GetSystemState(_ context.Context) (domain.SystemState, bool, string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.State, r.Halted, r.HaltReason, nil
}

func (r *MemoryRepository) SaveSystemState(_ context.Context, state domain.SystemState, mode domain.Mode, halted bool, reason string, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.State = state
	r.Mode = mode
	r.Halted = halted
	r.HaltReason = reason
	return nil
}

func (r *MemoryRepository) Unhalt(_ context.Context, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.Halted && r.State != domain.StateHalted {
		return fmt.Errorf("system is not halted")
	}
	r.RiskEvents = append(r.RiskEvents, domain.RiskEvent{Severity: domain.SeverityInfo, EventType: "manual_unhalt", Message: reason})
	r.State = domain.StateInit
	r.Halted = false
	r.HaltReason = ""
	return nil
}

func (r *MemoryRepository) WasDailyReportSent(_ context.Context, reportDate time.Time, accountIDHash string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.Reports[accountIDHash+"|"+dateOnly(reportDate).Format("2006-01-02")], nil
}

func (r *MemoryRepository) MarkDailyReportSent(_ context.Context, reportDate time.Time, accountIDHash string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Reports[accountIDHash+"|"+dateOnly(reportDate).Format("2006-01-02")] = true
	return nil
}

func (r *MemoryRepository) InsertReconciliation(_ context.Context, _ time.Time, _ string, _ bool) error {
	return nil
}

func upsertCandle(candles []domain.Candle, candle domain.Candle, minute bool) []domain.Candle {
	for i, existing := range candles {
		if (!minute && sameDate(existing.TradeDate, candle.TradeDate)) || (minute && existing.TradeDate.Equal(candle.TradeDate)) {
			candles[i] = candle
			return candles
		}
	}
	return append(candles, candle)
}

func filterCandles(candles []domain.Candle, from, to time.Time) []domain.Candle {
	var out []domain.Candle
	for _, candle := range candles {
		if !candle.TradeDate.Before(from) && !candle.TradeDate.After(to) {
			out = append(out, candle)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TradeDate.Before(out[j].TradeDate) })
	return out
}

func featureKey(instrumentUID string, date time.Time) string {
	return instrumentUID + "|" + dateOnly(date).Format("2006-01-02")
}

func sameDate(a, b time.Time) bool {
	return dateOnly(a).Equal(dateOnly(b))
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
