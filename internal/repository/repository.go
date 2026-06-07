package repository

import (
	"context"
	"time"

	"overnight-trading-bot/internal/domain"
)

type Repository interface {
	RunInTx(ctx context.Context, fn func(ctx context.Context, repo Repository) error) error

	UpsertInstrument(ctx context.Context, instrument domain.Instrument) error
	ReplaceInstrument(ctx context.Context, oldInstrumentUID string, instrument domain.Instrument) error
	ListInstruments(ctx context.Context, includeDisabled bool) ([]domain.Instrument, error)
	QuarantineInstrument(ctx context.Context, instrumentUID, reason string) error

	UpsertDailyCandles(ctx context.Context, candles []domain.Candle) error
	ListDailyCandles(ctx context.Context, instrumentUID string, from, to time.Time) ([]domain.Candle, error)
	UpsertMinuteCandles(ctx context.Context, candles []domain.Candle) error
	ListMinuteCandles(ctx context.Context, instrumentUID string, from, to time.Time) ([]domain.Candle, error)

	UpsertFeature(ctx context.Context, feature domain.FeatureSet) error
	GetFeature(ctx context.Context, instrumentUID string, tradeDate time.Time) (domain.FeatureSet, error)

	UpsertSignal(ctx context.Context, signal domain.Signal) error
	ListSignals(ctx context.Context, tradeDate time.Time) ([]domain.Signal, error)

	UpsertOrder(ctx context.Context, order domain.Order) error
	UpdateOrderStatus(ctx context.Context, clientOrderID string, status domain.OrderStatus, filledLots int64, rawJSON string) error
	ListActiveOrders(ctx context.Context, accountIDHash string) ([]domain.Order, error)
	ListOrders(ctx context.Context, accountIDHash string, from, to time.Time) ([]domain.Order, error)

	UpsertPosition(ctx context.Context, position domain.Position) error
	ListOpenPositions(ctx context.Context, accountIDHash string) ([]domain.Position, error)
	ListPositions(ctx context.Context, accountIDHash string, from, to time.Time) ([]domain.Position, error)

	InsertRiskEvent(ctx context.Context, event domain.RiskEvent) error
	GetFreeOrdersSent(ctx context.Context, tradeDate time.Time, instrumentUID string) (int, error)
	IncrementFreeOrders(ctx context.Context, tradeDate time.Time, instrumentUID string, delta int) error

	GetSystemState(ctx context.Context) (domain.SystemState, bool, string, error)
	SaveSystemState(ctx context.Context, state domain.SystemState, mode domain.Mode, halted bool, reason string, contextJSON string) error
	Unhalt(ctx context.Context, reason string) error
	WasDailyReportSent(ctx context.Context, reportDate time.Time, accountIDHash string) (bool, error)
	MarkDailyReportSent(ctx context.Context, reportDate time.Time, accountIDHash string) error

	InsertReconciliation(ctx context.Context, ts time.Time, diffJSON string, hasDiff bool) error
}
