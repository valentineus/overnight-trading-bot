package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/repository"
	"overnight-trading-bot/internal/risk"
)

var _ repository.Repository = (*Repository)(nil)

type Repository struct {
	db *sqlx.DB
	tx *sqlx.Tx
}

func NewRepository(db *sqlx.DB) *Repository {
	return &Repository{db: db}
}

func (r *Repository) RunInTx(ctx context.Context, fn func(ctx context.Context, repo repository.Repository) error) error {
	if r.tx != nil {
		return fn(ctx, r)
	}
	tx, err := r.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	txRepo := &Repository{db: r.db, tx: tx}
	if err := fn(ctx, txRepo); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return fmt.Errorf("%w; rollback: %v", err, rbErr)
		}
		return err
	}
	return tx.Commit()
}

func (r *Repository) execer() sqlx.ExtContext {
	if r.tx != nil {
		return r.tx
	}
	return r.db
}

func (r *Repository) selectContext(ctx context.Context, dest any, query string, args ...any) error {
	if r.tx != nil {
		return r.tx.SelectContext(ctx, dest, query, args...)
	}
	return r.db.SelectContext(ctx, dest, query, args...)
}

func (r *Repository) getContext(ctx context.Context, dest any, query string, args ...any) error {
	if r.tx != nil {
		return r.tx.GetContext(ctx, dest, query, args...)
	}
	return r.db.GetContext(ctx, dest, query, args...)
}

func (r *Repository) UpsertInstrument(ctx context.Context, instrument domain.Instrument) error {
	if instrument.UpdatedAt.IsZero() {
		instrument.UpdatedAt = time.Now().UTC()
	}
	_, err := sqlx.NamedExecContext(ctx, r.execer(), `
INSERT INTO instruments (
  instrument_uid, figi, ticker, class_code, name, lot, min_price_increment, currency,
  enabled, fund_type, expected_commission_bps_per_side, free_order_limit_per_day,
  quarantine, quarantine_reason, exclude_reason, updated_at
) VALUES (
  :instrument_uid, :figi, :ticker, :class_code, :name, :lot, :min_price_increment, :currency,
  :enabled, :fund_type, :expected_commission_bps_per_side, :free_order_limit_per_day,
  :quarantine, :quarantine_reason, :exclude_reason, :updated_at
) ON DUPLICATE KEY UPDATE
  instrument_uid=VALUES(instrument_uid),
  figi=VALUES(figi),
  name=VALUES(name),
  lot=VALUES(lot),
  min_price_increment=VALUES(min_price_increment),
  currency=VALUES(currency),
  enabled=VALUES(enabled),
  fund_type=VALUES(fund_type),
  expected_commission_bps_per_side=VALUES(expected_commission_bps_per_side),
  free_order_limit_per_day=VALUES(free_order_limit_per_day),
  quarantine=VALUES(quarantine),
  quarantine_reason=VALUES(quarantine_reason),
  exclude_reason=VALUES(exclude_reason),
  updated_at=VALUES(updated_at)`, instrumentRowFromDomain(instrument))
	return err
}

func (r *Repository) ReplaceInstrument(ctx context.Context, oldInstrumentUID string, instrument domain.Instrument) error {
	if oldInstrumentUID == "" || oldInstrumentUID == instrument.InstrumentUID {
		return r.UpsertInstrument(ctx, instrument)
	}
	return r.RunInTx(ctx, func(ctx context.Context, repo repository.Repository) error {
		txRepo, ok := repo.(*Repository)
		if !ok {
			return errors.New("unexpected repository implementation")
		}
		return txRepo.replaceInstrument(ctx, oldInstrumentUID, instrument)
	})
}

func (r *Repository) replaceInstrument(ctx context.Context, oldInstrumentUID string, instrument domain.Instrument) error {
	if instrument.UpdatedAt.IsZero() {
		instrument.UpdatedAt = time.Now().UTC()
	}
	exists, err := r.instrumentExists(ctx, instrument.InstrumentUID)
	if err != nil {
		return err
	}
	if exists {
		if err := r.mergeInstrumentUID(ctx, oldInstrumentUID, instrument.InstrumentUID); err != nil {
			return err
		}
		return r.UpsertInstrument(ctx, instrument)
	}
	result, err := sqlx.NamedExecContext(ctx, r.execer(), `
UPDATE instruments SET
  instrument_uid=:instrument_uid,
  figi=:figi,
  ticker=:ticker,
  class_code=:class_code,
  name=:name,
  lot=:lot,
  min_price_increment=:min_price_increment,
  currency=:currency,
  enabled=:enabled,
  fund_type=:fund_type,
  expected_commission_bps_per_side=:expected_commission_bps_per_side,
  free_order_limit_per_day=:free_order_limit_per_day,
  quarantine=:quarantine,
  quarantine_reason=:quarantine_reason,
  exclude_reason=:exclude_reason,
  updated_at=:updated_at
WHERE instrument_uid=:old_instrument_uid`, replaceInstrumentRowFromDomain(oldInstrumentUID, instrument))
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return r.UpsertInstrument(ctx, instrument)
	}
	return nil
}

func (r *Repository) instrumentExists(ctx context.Context, instrumentUID string) (bool, error) {
	var count int
	if err := r.getContext(ctx, &count, `SELECT COUNT(*) FROM instruments WHERE instrument_uid=?`, instrumentUID); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *Repository) mergeInstrumentUID(ctx context.Context, oldInstrumentUID, newInstrumentUID string) error {
	if oldInstrumentUID == newInstrumentUID {
		return nil
	}
	if err := r.mergeDailyCandles(ctx, oldInstrumentUID, newInstrumentUID); err != nil {
		return err
	}
	if err := r.mergeMinuteCandles(ctx, oldInstrumentUID, newInstrumentUID); err != nil {
		return err
	}
	if err := r.mergeFeatures(ctx, oldInstrumentUID, newInstrumentUID); err != nil {
		return err
	}
	if err := r.mergeSignals(ctx, oldInstrumentUID, newInstrumentUID); err != nil {
		return err
	}
	if err := r.mergeFreeOrders(ctx, oldInstrumentUID, newInstrumentUID); err != nil {
		return err
	}
	if _, err := r.execer().ExecContext(ctx, `UPDATE orders SET instrument_uid=? WHERE instrument_uid=?`, newInstrumentUID, oldInstrumentUID); err != nil {
		return err
	}
	if _, err := r.execer().ExecContext(ctx, `UPDATE positions SET instrument_uid=? WHERE instrument_uid=?`, newInstrumentUID, oldInstrumentUID); err != nil {
		return err
	}
	if _, err := r.execer().ExecContext(ctx, `UPDATE risk_events SET instrument_uid=? WHERE instrument_uid=?`, newInstrumentUID, oldInstrumentUID); err != nil {
		return err
	}
	_, err := r.execer().ExecContext(ctx, `DELETE FROM instruments WHERE instrument_uid=?`, oldInstrumentUID)
	return err
}

func (r *Repository) mergeDailyCandles(ctx context.Context, oldInstrumentUID, newInstrumentUID string) error {
	_, err := r.execer().ExecContext(ctx, `
INSERT INTO candles_daily (instrument_uid, trade_date, open, high, low, close, volume_lots, source, loaded_at)
SELECT ?, trade_date, open, high, low, close, volume_lots, source, loaded_at
FROM candles_daily WHERE instrument_uid=?
ON DUPLICATE KEY UPDATE
  open=VALUES(open), high=VALUES(high), low=VALUES(low), close=VALUES(close),
  volume_lots=VALUES(volume_lots), source=VALUES(source), loaded_at=VALUES(loaded_at)`, newInstrumentUID, oldInstrumentUID)
	if err != nil {
		return err
	}
	_, err = r.execer().ExecContext(ctx, `DELETE FROM candles_daily WHERE instrument_uid=?`, oldInstrumentUID)
	return err
}

func (r *Repository) mergeMinuteCandles(ctx context.Context, oldInstrumentUID, newInstrumentUID string) error {
	_, err := r.execer().ExecContext(ctx, `
INSERT INTO candles_minute (instrument_uid, ts, open, high, low, close, volume_lots, source, loaded_at)
SELECT ?, ts, open, high, low, close, volume_lots, source, loaded_at
FROM candles_minute WHERE instrument_uid=?
ON DUPLICATE KEY UPDATE
  open=VALUES(open), high=VALUES(high), low=VALUES(low), close=VALUES(close),
  volume_lots=VALUES(volume_lots), source=VALUES(source), loaded_at=VALUES(loaded_at)`, newInstrumentUID, oldInstrumentUID)
	if err != nil {
		return err
	}
	_, err = r.execer().ExecContext(ctx, `DELETE FROM candles_minute WHERE instrument_uid=?`, oldInstrumentUID)
	return err
}

func (r *Repository) mergeFeatures(ctx context.Context, oldInstrumentUID, newInstrumentUID string) error {
	_, err := r.execer().ExecContext(ctx, `
INSERT INTO features (
  instrument_uid, trade_date, r_on, r_day, mu_on_60, mu_on_252, sigma_on_60, q05_on_60_abs,
  tstat_on_60, win_on_60, ewma_on, spread_bps, half_spread_bps, tick_bps,
  adv_20, expected_cost_bps, cost_breakdown_json, net_edge_bps, entry_interval_volume,
  exit_interval_volume, calculated_at
)
SELECT
  ?, trade_date, r_on, r_day, mu_on_60, mu_on_252, sigma_on_60, q05_on_60_abs,
  tstat_on_60, win_on_60, ewma_on, spread_bps, half_spread_bps, tick_bps,
  adv_20, expected_cost_bps, cost_breakdown_json, net_edge_bps, entry_interval_volume,
  exit_interval_volume, calculated_at
FROM features WHERE instrument_uid=?
ON DUPLICATE KEY UPDATE
  r_on=VALUES(r_on), r_day=VALUES(r_day), mu_on_60=VALUES(mu_on_60),
  mu_on_252=VALUES(mu_on_252), sigma_on_60=VALUES(sigma_on_60),
  q05_on_60_abs=VALUES(q05_on_60_abs), tstat_on_60=VALUES(tstat_on_60), win_on_60=VALUES(win_on_60),
  ewma_on=VALUES(ewma_on), spread_bps=VALUES(spread_bps),
  half_spread_bps=VALUES(half_spread_bps), tick_bps=VALUES(tick_bps),
  adv_20=VALUES(adv_20), expected_cost_bps=VALUES(expected_cost_bps),
  cost_breakdown_json=VALUES(cost_breakdown_json),
  net_edge_bps=VALUES(net_edge_bps), entry_interval_volume=VALUES(entry_interval_volume),
  exit_interval_volume=VALUES(exit_interval_volume), calculated_at=VALUES(calculated_at)`, newInstrumentUID, oldInstrumentUID)
	if err != nil {
		return err
	}
	_, err = r.execer().ExecContext(ctx, `DELETE FROM features WHERE instrument_uid=?`, oldInstrumentUID)
	return err
}

func (r *Repository) mergeSignals(ctx context.Context, oldInstrumentUID, newInstrumentUID string) error {
	_, err := r.execer().ExecContext(ctx, `
INSERT INTO signals (
  trade_date, instrument_uid, decision, score, net_edge_bps, target_notional,
  target_lots, reject_reason, context_json, created_at
)
SELECT trade_date, ?, decision, score, net_edge_bps, target_notional,
  target_lots, reject_reason, context_json, created_at
FROM signals WHERE instrument_uid=?
ON DUPLICATE KEY UPDATE
  decision=VALUES(decision), score=VALUES(score), net_edge_bps=VALUES(net_edge_bps),
  target_notional=VALUES(target_notional), target_lots=VALUES(target_lots),
  reject_reason=VALUES(reject_reason), context_json=VALUES(context_json),
  created_at=VALUES(created_at)`, newInstrumentUID, oldInstrumentUID)
	if err != nil {
		return err
	}
	_, err = r.execer().ExecContext(ctx, `DELETE FROM signals WHERE instrument_uid=?`, oldInstrumentUID)
	return err
}

func (r *Repository) mergeFreeOrders(ctx context.Context, oldInstrumentUID, newInstrumentUID string) error {
	_, err := r.execer().ExecContext(ctx, `
INSERT INTO free_order_counters (trade_date, instrument_uid, orders_sent)
SELECT trade_date, ?, orders_sent FROM free_order_counters WHERE instrument_uid=?
ON DUPLICATE KEY UPDATE orders_sent=GREATEST(orders_sent, VALUES(orders_sent))`, newInstrumentUID, oldInstrumentUID)
	if err != nil {
		return err
	}
	_, err = r.execer().ExecContext(ctx, `DELETE FROM free_order_counters WHERE instrument_uid=?`, oldInstrumentUID)
	return err
}

func (r *Repository) ListInstruments(ctx context.Context, includeDisabled bool) ([]domain.Instrument, error) {
	query := `SELECT * FROM instruments`
	if !includeDisabled {
		query += ` WHERE enabled=1`
	}
	query += ` ORDER BY ticker`
	var rows []instrumentRow
	if err := r.selectContext(ctx, &rows, query); err != nil {
		return nil, err
	}
	out := make([]domain.Instrument, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.domain())
	}
	return out, nil
}

func (r *Repository) QuarantineInstrument(ctx context.Context, instrumentUID, reason string) error {
	_, err := r.execer().ExecContext(ctx, `
UPDATE instruments SET quarantine=1, quarantine_reason=?, updated_at=UTC_TIMESTAMP(3)
WHERE instrument_uid=?`, reason, instrumentUID)
	return err
}

func (r *Repository) UpsertDailyCandles(ctx context.Context, candles []domain.Candle) error {
	for _, candle := range candles {
		if candle.LoadedAt.IsZero() {
			candle.LoadedAt = time.Now().UTC()
		}
		_, err := sqlx.NamedExecContext(ctx, r.execer(), `
INSERT INTO candles_daily (
  instrument_uid, trade_date, open, high, low, close, volume_lots, source, loaded_at
) VALUES (
  :instrument_uid, :trade_date, :open, :high, :low, :close, :volume_lots, :source, :loaded_at
) ON DUPLICATE KEY UPDATE
  open=VALUES(open), high=VALUES(high), low=VALUES(low), close=VALUES(close),
  volume_lots=VALUES(volume_lots), source=VALUES(source), loaded_at=VALUES(loaded_at)`, candleRowFromDomain(candle))
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) ListDailyCandles(ctx context.Context, instrumentUID string, from, to time.Time) ([]domain.Candle, error) {
	var rows []candleRow
	if err := r.selectContext(ctx, &rows, `
SELECT * FROM candles_daily
WHERE instrument_uid=? AND trade_date BETWEEN ? AND ?
ORDER BY trade_date`, instrumentUID, dateOnly(from), dateOnly(to)); err != nil {
		return nil, err
	}
	out := make([]domain.Candle, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.domain())
	}
	return out, nil
}

func (r *Repository) UpsertMinuteCandles(ctx context.Context, candles []domain.Candle) error {
	for _, candle := range candles {
		if candle.LoadedAt.IsZero() {
			candle.LoadedAt = time.Now().UTC()
		}
		_, err := sqlx.NamedExecContext(ctx, r.execer(), `
INSERT INTO candles_minute (
  instrument_uid, ts, open, high, low, close, volume_lots, source, loaded_at
) VALUES (
  :instrument_uid, :trade_date, :open, :high, :low, :close, :volume_lots, :source, :loaded_at
) ON DUPLICATE KEY UPDATE
  open=VALUES(open), high=VALUES(high), low=VALUES(low), close=VALUES(close),
  volume_lots=VALUES(volume_lots), source=VALUES(source), loaded_at=VALUES(loaded_at)`, minuteCandleRowFromDomain(candle))
		if err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) ListMinuteCandles(ctx context.Context, instrumentUID string, from, to time.Time) ([]domain.Candle, error) {
	var rows []candleRow
	if err := r.selectContext(ctx, &rows, `
SELECT instrument_uid, ts AS trade_date, open, high, low, close, volume_lots, source, loaded_at
FROM candles_minute
WHERE instrument_uid=? AND ts BETWEEN ? AND ?
ORDER BY ts`, instrumentUID, from.UTC(), to.UTC()); err != nil {
		return nil, err
	}
	out := make([]domain.Candle, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.domain())
	}
	return out, nil
}

func (r *Repository) UpsertFeature(ctx context.Context, feature domain.FeatureSet) error {
	if feature.CalculatedAt.IsZero() {
		feature.CalculatedAt = time.Now().UTC()
	}
	_, err := sqlx.NamedExecContext(ctx, r.execer(), `
INSERT INTO features (
  instrument_uid, trade_date, r_on, r_day, mu_on_60, mu_on_252, sigma_on_60, q05_on_60_abs,
  tstat_on_60, win_on_60, ewma_on, spread_bps, half_spread_bps, tick_bps,
  adv_20, expected_cost_bps, cost_breakdown_json, net_edge_bps, entry_interval_volume,
  exit_interval_volume, calculated_at
) VALUES (
  :instrument_uid, :trade_date, :r_on, :r_day, :mu_on_60, :mu_on_252, :sigma_on_60, :q05_on_60_abs,
  :tstat_on_60, :win_on_60, :ewma_on, :spread_bps, :half_spread_bps, :tick_bps,
  :adv_20, :expected_cost_bps, :cost_breakdown_json, :net_edge_bps, :entry_interval_volume,
  :exit_interval_volume, :calculated_at
) ON DUPLICATE KEY UPDATE
  r_on=VALUES(r_on), r_day=VALUES(r_day), mu_on_60=VALUES(mu_on_60),
  mu_on_252=VALUES(mu_on_252), sigma_on_60=VALUES(sigma_on_60),
  q05_on_60_abs=VALUES(q05_on_60_abs), tstat_on_60=VALUES(tstat_on_60), win_on_60=VALUES(win_on_60),
  ewma_on=VALUES(ewma_on), spread_bps=VALUES(spread_bps),
  half_spread_bps=VALUES(half_spread_bps), tick_bps=VALUES(tick_bps),
  adv_20=VALUES(adv_20), expected_cost_bps=VALUES(expected_cost_bps),
  cost_breakdown_json=VALUES(cost_breakdown_json),
  net_edge_bps=VALUES(net_edge_bps), entry_interval_volume=VALUES(entry_interval_volume),
  exit_interval_volume=VALUES(exit_interval_volume), calculated_at=VALUES(calculated_at)`, featureRowFromDomain(feature))
	return err
}

func (r *Repository) GetFeature(ctx context.Context, instrumentUID string, tradeDate time.Time) (domain.FeatureSet, error) {
	var row featureRow
	if err := r.getContext(ctx, &row, `SELECT * FROM features WHERE instrument_uid=? AND trade_date=?`, instrumentUID, dateOnly(tradeDate)); err != nil {
		return domain.FeatureSet{}, err
	}
	return row.domain(), nil
}

func (r *Repository) UpsertSignal(ctx context.Context, signal domain.Signal) error {
	if signal.CreatedAt.IsZero() {
		signal.CreatedAt = time.Now().UTC()
	}
	if signal.ContextJSON == "" {
		signal.ContextJSON = "{}"
	}
	_, err := sqlx.NamedExecContext(ctx, r.execer(), `
INSERT INTO signals (
  trade_date, instrument_uid, decision, score, net_edge_bps, target_notional,
  target_lots, reject_reason, context_json, created_at
) VALUES (
  :trade_date, :instrument_uid, :decision, :score, :net_edge_bps, :target_notional,
  :target_lots, :reject_reason, :context_json, :created_at
) ON DUPLICATE KEY UPDATE
  decision=VALUES(decision), score=VALUES(score), net_edge_bps=VALUES(net_edge_bps),
  target_notional=VALUES(target_notional), target_lots=VALUES(target_lots),
  reject_reason=VALUES(reject_reason), context_json=VALUES(context_json),
  created_at=VALUES(created_at)`, signalRowFromDomain(signal))
	return err
}

func (r *Repository) ListSignals(ctx context.Context, tradeDate time.Time) ([]domain.Signal, error) {
	var rows []signalRow
	if err := r.selectContext(ctx, &rows, `SELECT * FROM signals WHERE trade_date=? ORDER BY id`, dateOnly(tradeDate)); err != nil {
		return nil, err
	}
	out := make([]domain.Signal, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.domain())
	}
	return out, nil
}

func (r *Repository) UpsertOrder(ctx context.Context, order domain.Order) error {
	now := time.Now().UTC()
	if order.CreatedAt.IsZero() {
		order.CreatedAt = now
	}
	if order.UpdatedAt.IsZero() {
		order.UpdatedAt = now
	}
	if order.RawStateJSON == "" {
		order.RawStateJSON = "{}"
	}
	_, err := sqlx.NamedExecContext(ctx, r.execer(), `
INSERT INTO orders (
  client_order_id, broker_order_id, account_id_hash, instrument_uid, trade_date,
  side, order_type, limit_price, quantity_lots, filled_lots, avg_fill_price,
  status, commission, attempt_no, raw_state_json, created_at, updated_at
) VALUES (
  :client_order_id, :broker_order_id, :account_id_hash, :instrument_uid, :trade_date,
  :side, :order_type, :limit_price, :quantity_lots, :filled_lots, :avg_fill_price,
  :status, :commission, :attempt_no, :raw_state_json, :created_at, :updated_at
) ON DUPLICATE KEY UPDATE
  broker_order_id=VALUES(broker_order_id), filled_lots=VALUES(filled_lots),
  avg_fill_price=VALUES(avg_fill_price), status=VALUES(status),
  commission=VALUES(commission), raw_state_json=VALUES(raw_state_json),
  updated_at=VALUES(updated_at)`, orderRowFromDomain(order))
	return err
}

func (r *Repository) UpdateOrderStatus(ctx context.Context, clientOrderID string, status domain.OrderStatus, filledLots int64, rawJSON string) error {
	if rawJSON == "" {
		rawJSON = "{}"
	}
	_, err := r.execer().ExecContext(ctx, `
UPDATE orders SET status=?, filled_lots=?, raw_state_json=?, updated_at=UTC_TIMESTAMP(3)
WHERE client_order_id=?`, status, filledLots, rawJSON, clientOrderID)
	return err
}

func (r *Repository) ListActiveOrders(ctx context.Context, accountIDHash string) ([]domain.Order, error) {
	var rows []orderRow
	if err := r.selectContext(ctx, &rows, `
SELECT * FROM orders
WHERE account_id_hash=? AND status IN ('NEW','SENT','PARTIALLY_FILLED')
ORDER BY created_at`, accountIDHash); err != nil {
		return nil, err
	}
	out := make([]domain.Order, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.domain())
	}
	return out, nil
}

func (r *Repository) ListOrders(ctx context.Context, accountIDHash string, from, to time.Time) ([]domain.Order, error) {
	var rows []orderRow
	if err := r.selectContext(ctx, &rows, `
SELECT * FROM orders
WHERE account_id_hash=? AND trade_date BETWEEN ? AND ?
ORDER BY created_at`, accountIDHash, dateOnly(from), dateOnly(to)); err != nil {
		return nil, err
	}
	out := make([]domain.Order, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.domain())
	}
	return out, nil
}

func (r *Repository) UpsertPosition(ctx context.Context, position domain.Position) error {
	if position.UpdatedAt.IsZero() {
		position.UpdatedAt = time.Now().UTC()
	}
	_, err := sqlx.NamedExecContext(ctx, r.execer(), `
INSERT INTO positions (
  id, account_id_hash, instrument_uid, open_trade_date, lots, lot_size, exit_filled_lots,
  avg_buy_price, avg_sell_price, status, gross_pnl, net_pnl, commission_total,
  realized_edge_bps, opened_at, closed_at, updated_at
) VALUES (
  NULLIF(:id, 0), :account_id_hash, :instrument_uid, :open_trade_date, :lots, :lot_size, :exit_filled_lots,
  :avg_buy_price, :avg_sell_price, :status, :gross_pnl, :net_pnl, :commission_total,
  :realized_edge_bps, :opened_at, :closed_at, :updated_at
) ON DUPLICATE KEY UPDATE
  lots=VALUES(lots), lot_size=VALUES(lot_size), exit_filled_lots=VALUES(exit_filled_lots), avg_buy_price=VALUES(avg_buy_price), avg_sell_price=VALUES(avg_sell_price),
  status=VALUES(status), gross_pnl=VALUES(gross_pnl), net_pnl=VALUES(net_pnl),
  commission_total=VALUES(commission_total), realized_edge_bps=VALUES(realized_edge_bps),
  opened_at=VALUES(opened_at), closed_at=VALUES(closed_at), updated_at=VALUES(updated_at)`, positionRowFromDomain(position))
	return err
}

func (r *Repository) ListOpenPositions(ctx context.Context, accountIDHash string) ([]domain.Position, error) {
	var rows []positionRow
	if err := r.selectContext(ctx, &rows, `
SELECT * FROM positions
WHERE account_id_hash=? AND status NOT IN ('NO_POSITION','EXIT_FILLED','QUARANTINE')
ORDER BY updated_at`, accountIDHash); err != nil {
		return nil, err
	}
	out := make([]domain.Position, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.domain())
	}
	return out, nil
}

func (r *Repository) ListPositions(ctx context.Context, accountIDHash string, from, to time.Time) ([]domain.Position, error) {
	var rows []positionRow
	if err := r.selectContext(ctx, &rows, `
SELECT * FROM positions
WHERE account_id_hash=? AND open_trade_date BETWEEN ? AND ?
ORDER BY updated_at`, accountIDHash, dateOnly(from), dateOnly(to)); err != nil {
		return nil, err
	}
	out := make([]domain.Position, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.domain())
	}
	return out, nil
}

func (r *Repository) InsertRiskEvent(ctx context.Context, event domain.RiskEvent) error {
	if event.TS.IsZero() {
		event.TS = time.Now().UTC()
	}
	if event.ContextJSON == "" {
		event.ContextJSON = "{}"
	}
	_, err := sqlx.NamedExecContext(ctx, r.execer(), `
INSERT INTO risk_events (ts, severity, event_type, instrument_uid, message, raw_context_json)
VALUES (:ts, :severity, :event_type, :instrument_uid, :message, :raw_context_json)`, riskEventRowFromDomain(event))
	return err
}

func (r *Repository) GetFreeOrdersSent(ctx context.Context, tradeDate time.Time, instrumentUID string) (int, error) {
	var sent int
	err := r.getContext(ctx, &sent, `
SELECT orders_sent FROM free_order_counters WHERE trade_date=? AND instrument_uid=?`, dateOnly(tradeDate), instrumentUID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return sent, err
}

func (r *Repository) IncrementFreeOrders(ctx context.Context, tradeDate time.Time, instrumentUID string, delta int) error {
	_, err := r.execer().ExecContext(ctx, `
INSERT INTO free_order_counters (trade_date, instrument_uid, orders_sent)
VALUES (?, ?, ?)
ON DUPLICATE KEY UPDATE orders_sent=orders_sent+VALUES(orders_sent)`, dateOnly(tradeDate), instrumentUID, delta)
	return err
}

func (r *Repository) ReserveFreeOrders(ctx context.Context, tradeDate time.Time, instrumentUID string, delta int, limit int) error {
	if delta <= 0 {
		return nil
	}
	if limit <= 0 {
		return r.IncrementFreeOrders(ctx, tradeDate, instrumentUID, delta)
	}
	return r.RunInTx(ctx, func(ctx context.Context, repo repository.Repository) error {
		txRepo, ok := repo.(*Repository)
		if !ok {
			return errors.New("unexpected repository implementation")
		}
		return txRepo.reserveFreeOrdersLocked(ctx, tradeDate, instrumentUID, delta, limit)
	})
}

func (r *Repository) reserveFreeOrdersLocked(ctx context.Context, tradeDate time.Time, instrumentUID string, delta int, limit int) error {
	tradeDay := dateOnly(tradeDate)
	if _, err := r.execer().ExecContext(ctx, `
INSERT IGNORE INTO free_order_counters (trade_date, instrument_uid, orders_sent)
VALUES (?, ?, 0)`, tradeDay, instrumentUID); err != nil {
		return err
	}
	var sent int
	if err := r.getContext(ctx, &sent, `
SELECT orders_sent FROM free_order_counters
WHERE trade_date=? AND instrument_uid=?
FOR UPDATE`, tradeDay, instrumentUID); err != nil {
		return err
	}
	remaining := limit - sent
	if remaining < delta {
		return fmt.Errorf("%w: %s remaining=%d needed=%d", risk.ErrFreeOrderBudget, instrumentUID, remaining, delta)
	}
	_, err := r.execer().ExecContext(ctx, `
UPDATE free_order_counters
SET orders_sent=orders_sent+?
WHERE trade_date=? AND instrument_uid=?`, delta, tradeDay, instrumentUID)
	return err
}

func (r *Repository) GetSystemState(ctx context.Context) (domain.SystemState, bool, string, error) {
	var row struct {
		State      string         `db:"state"`
		Halted     bool           `db:"halted"`
		HaltReason sql.NullString `db:"halt_reason"`
	}
	if err := r.getContext(ctx, &row, `SELECT state, halted, halt_reason FROM system_state WHERE id=1`); err != nil {
		return "", false, "", err
	}
	return domain.SystemState(row.State), row.Halted, row.HaltReason.String, nil
}

func (r *Repository) SaveSystemState(ctx context.Context, state domain.SystemState, mode domain.Mode, halted bool, reason string, contextJSON string) error {
	if contextJSON == "" {
		contextJSON = "{}"
	}
	_, err := r.execer().ExecContext(ctx, `
INSERT INTO system_state (id, state, mode, halted, halt_reason, last_heartbeat, context_json)
VALUES (1, ?, ?, ?, ?, UTC_TIMESTAMP(3), ?)
ON DUPLICATE KEY UPDATE
  state=IF(halted=1 AND VALUES(halted)=0, state, VALUES(state)),
  mode=VALUES(mode),
  halted=IF(halted=1 AND VALUES(halted)=0, halted, VALUES(halted)),
  halt_reason=IF(halted=1 AND VALUES(halted)=0, halt_reason, VALUES(halt_reason)),
  last_heartbeat=VALUES(last_heartbeat),
  context_json=VALUES(context_json)`, state, mode, halted, nullableString(reason), contextJSON)
	if err != nil {
		return err
	}
	return r.insertSystemStateHistory(ctx, state, mode, halted, reason, contextJSON)
}

func (r *Repository) forceSaveSystemState(ctx context.Context, state domain.SystemState, mode domain.Mode, halted bool, reason string, contextJSON string) error {
	if contextJSON == "" {
		contextJSON = "{}"
	}
	_, err := r.execer().ExecContext(ctx, `
INSERT INTO system_state (id, state, mode, halted, halt_reason, last_heartbeat, context_json)
VALUES (1, ?, ?, ?, ?, UTC_TIMESTAMP(3), ?)
ON DUPLICATE KEY UPDATE
  state=VALUES(state), mode=VALUES(mode), halted=VALUES(halted),
  halt_reason=VALUES(halt_reason), last_heartbeat=VALUES(last_heartbeat),
  context_json=VALUES(context_json)`, state, mode, halted, nullableString(reason), contextJSON)
	if err != nil {
		return err
	}
	return r.insertSystemStateHistory(ctx, state, mode, halted, reason, contextJSON)
}

func (r *Repository) insertSystemStateHistory(ctx context.Context, state domain.SystemState, mode domain.Mode, halted bool, reason string, contextJSON string) error {
	_, err := r.execer().ExecContext(ctx, `
INSERT INTO system_state_history (ts, state, mode, halted, halt_reason, context_json)
VALUES (UTC_TIMESTAMP(3), ?, ?, ?, ?, ?)`, state, mode, halted, nullableString(reason), contextJSON)
	if isMissingTableError(err) {
		return nil
	}
	return err
}

func isMissingTableError(err error) bool {
	var mysqlErr *mysql.MySQLError
	return errors.As(err, &mysqlErr) && mysqlErr.Number == 1146
}

func (r *Repository) Unhalt(ctx context.Context, reason string) error {
	return r.RunInTx(ctx, func(ctx context.Context, repo repository.Repository) error {
		state, halted, haltReason, err := repo.GetSystemState(ctx)
		if err != nil {
			return err
		}
		if !halted && state != domain.StateHalted {
			return fmt.Errorf("system is not halted")
		}
		if err := repo.InsertRiskEvent(ctx, domain.RiskEvent{
			TS:        time.Now().UTC(),
			Severity:  domain.SeverityInfo,
			EventType: "manual_unhalt",
			Message:   fmt.Sprintf("%s (previous halt: %s)", reason, haltReason),
		}); err != nil {
			return err
		}
		mode := domain.ModePaper
		if txRepo, ok := repo.(*Repository); ok {
			currentMode, err := txRepo.getSystemMode(ctx)
			if err != nil {
				return err
			}
			mode = currentMode
		}
		if txRepo, ok := repo.(*Repository); ok {
			return txRepo.forceSaveSystemState(ctx, domain.StateInit, mode, false, "", `{"manual_unhalt":true}`)
		}
		return repo.SaveSystemState(ctx, domain.StateInit, mode, false, "", `{"manual_unhalt":true}`)
	})
}

func (r *Repository) getSystemMode(ctx context.Context) (domain.Mode, error) {
	var raw string
	if err := r.getContext(ctx, &raw, `SELECT mode FROM system_state WHERE id=1`); err != nil {
		return "", err
	}
	mode, err := domain.ParseMode(raw)
	if err != nil {
		return "", err
	}
	return mode, nil
}

func (r *Repository) GetSystemMode(ctx context.Context) (domain.Mode, error) {
	return r.getSystemMode(ctx)
}

func (r *Repository) WasDailyReportSent(ctx context.Context, reportDate time.Time, accountIDHash string) (bool, error) {
	var count int
	if err := r.getContext(ctx, &count, `
SELECT COUNT(*) FROM daily_reports WHERE report_date=? AND account_id_hash=?`, dateOnly(reportDate), accountIDHash); err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *Repository) MarkDailyReportSent(ctx context.Context, reportDate time.Time, accountIDHash string) error {
	_, err := r.execer().ExecContext(ctx, `
INSERT INTO daily_reports (report_date, account_id_hash, sent_at)
VALUES (?, ?, UTC_TIMESTAMP(3))
ON DUPLICATE KEY UPDATE sent_at=sent_at`, dateOnly(reportDate), accountIDHash)
	return err
}

func (r *Repository) InsertReconciliation(ctx context.Context, ts time.Time, diffJSON string, hasDiff bool) error {
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	if diffJSON == "" {
		diffJSON = "[]"
	}
	_, err := r.execer().ExecContext(ctx, `
INSERT INTO reconciliations (ts, has_diff, diff_json)
VALUES (?, ?, ?)`, ts, hasDiff, diffJSON)
	return err
}

func dateOnly(t time.Time) time.Time {
	y, m, d := t.UTC().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

type instrumentRow struct {
	InstrumentUID                string          `db:"instrument_uid"`
	Figi                         sql.NullString  `db:"figi"`
	Ticker                       string          `db:"ticker"`
	ClassCode                    string          `db:"class_code"`
	Name                         string          `db:"name"`
	Lot                          int64           `db:"lot"`
	MinPriceIncrement            decimal.Decimal `db:"min_price_increment"`
	Currency                     string          `db:"currency"`
	Enabled                      bool            `db:"enabled"`
	FundType                     string          `db:"fund_type"`
	ExpectedCommissionBpsPerSide decimal.Decimal `db:"expected_commission_bps_per_side"`
	FreeOrderLimitPerDay         int             `db:"free_order_limit_per_day"`
	Quarantine                   bool            `db:"quarantine"`
	QuarantineReason             sql.NullString  `db:"quarantine_reason"`
	ExcludeReason                sql.NullString  `db:"exclude_reason"`
	UpdatedAt                    time.Time       `db:"updated_at"`
}

func instrumentRowFromDomain(instrument domain.Instrument) instrumentRow {
	return instrumentRow{
		InstrumentUID:                instrument.InstrumentUID,
		Figi:                         sql.NullString{String: instrument.Figi, Valid: instrument.Figi != ""},
		Ticker:                       instrument.Ticker,
		ClassCode:                    instrument.ClassCode,
		Name:                         instrument.Name,
		Lot:                          instrument.Lot,
		MinPriceIncrement:            instrument.MinPriceIncrement,
		Currency:                     instrument.Currency,
		Enabled:                      instrument.Enabled,
		FundType:                     instrument.FundType,
		ExpectedCommissionBpsPerSide: instrument.ExpectedCommissionBpsPerSide,
		FreeOrderLimitPerDay:         instrument.FreeOrderLimitPerDay,
		Quarantine:                   instrument.Quarantine,
		QuarantineReason:             sql.NullString{String: instrument.QuarantineReason, Valid: instrument.QuarantineReason != ""},
		ExcludeReason:                sql.NullString{String: instrument.ExcludeReason, Valid: instrument.ExcludeReason != ""},
		UpdatedAt:                    instrument.UpdatedAt,
	}
}

func replaceInstrumentRowFromDomain(oldInstrumentUID string, instrument domain.Instrument) map[string]any {
	row := instrumentRowFromDomain(instrument)
	return map[string]any{
		"instrument_uid":                   row.InstrumentUID,
		"figi":                             row.Figi,
		"ticker":                           row.Ticker,
		"class_code":                       row.ClassCode,
		"name":                             row.Name,
		"lot":                              row.Lot,
		"min_price_increment":              row.MinPriceIncrement,
		"currency":                         row.Currency,
		"enabled":                          row.Enabled,
		"fund_type":                        row.FundType,
		"expected_commission_bps_per_side": row.ExpectedCommissionBpsPerSide,
		"free_order_limit_per_day":         row.FreeOrderLimitPerDay,
		"quarantine":                       row.Quarantine,
		"quarantine_reason":                row.QuarantineReason,
		"exclude_reason":                   row.ExcludeReason,
		"updated_at":                       row.UpdatedAt,
		"old_instrument_uid":               oldInstrumentUID,
	}
}

func (r instrumentRow) domain() domain.Instrument {
	return domain.Instrument{
		InstrumentUID:                r.InstrumentUID,
		Figi:                         r.Figi.String,
		Ticker:                       r.Ticker,
		ClassCode:                    r.ClassCode,
		Name:                         r.Name,
		Lot:                          r.Lot,
		MinPriceIncrement:            r.MinPriceIncrement,
		Currency:                     r.Currency,
		Enabled:                      r.Enabled,
		FundType:                     r.FundType,
		ExpectedCommissionBpsPerSide: r.ExpectedCommissionBpsPerSide,
		FreeOrderLimitPerDay:         r.FreeOrderLimitPerDay,
		Quarantine:                   r.Quarantine,
		QuarantineReason:             r.QuarantineReason.String,
		ExcludeReason:                r.ExcludeReason.String,
		UpdatedAt:                    r.UpdatedAt,
	}
}
