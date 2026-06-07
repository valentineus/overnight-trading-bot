package scheduler

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/execution"
	"overnight-trading-bot/internal/features"
	"overnight-trading-bot/internal/instruments"
	"overnight-trading-bot/internal/marketdata"
	"overnight-trading-bot/internal/money"
	"overnight-trading-bot/internal/notify"
	"overnight-trading-bot/internal/position"
	"overnight-trading-bot/internal/reconciliation"
	"overnight-trading-bot/internal/report"
	"overnight-trading-bot/internal/repository"
	"overnight-trading-bot/internal/risk"
	"overnight-trading-bot/internal/signal"
	"overnight-trading-bot/internal/statemachine"
	"overnight-trading-bot/internal/timeutil"
	"overnight-trading-bot/internal/tinvest"
)

const (
	sizeReductionWindowTrades  = 20
	sizeReductionFactor        = 0.5
	intervalVolumeLookbackDays = 20
)

type Config struct {
	Mode                   domain.Mode
	Location               *time.Location
	RollingLong            int
	TickInterval           time.Duration
	EntrySignalTime        timeutil.TimeOfDay
	EntryWindowStart       timeutil.TimeOfDay
	EntryWindowEnd         timeutil.TimeOfDay
	NoNewEntryAfter        timeutil.TimeOfDay
	ExitWatchStart         timeutil.TimeOfDay
	ExitWindowStart        timeutil.TimeOfDay
	ExitWindowEnd          timeutil.TimeOfDay
	HardExitDeadline       timeutil.TimeOfDay
	QuoteDepth             int32
	MaxQuoteAge            time.Duration
	OrderPollInterval      time.Duration
	PassiveImproveTicks    int
	MaxEntryOrderAttempts  int
	MaxExitOrderAttempts   int
	MinTimeToClose         time.Duration
	MaxClockDrift          time.Duration
	APIOutageHalt          time.Duration
	RequireZeroCommission  bool
	QuarantineOnNonZero    bool
	ReconciliationInterval time.Duration
}

type Services struct {
	Repo          repository.Repository
	Gateway       tinvest.Gateway
	Registry      instruments.Registry
	MarketData    marketdata.Loader
	Features      features.Pipeline
	Signals       signal.Engine
	Sizer         risk.Sizer
	FreeOrders    risk.FreeOrderBudget
	Risk          risk.Manager
	Execution     *execution.Engine
	Positions     position.Manager
	Reconcile     reconciliation.Engine
	Notifier      notify.Notifier
	AccountID     string
	AccountIDHash string
	Log           *slog.Logger
}

type Scheduler struct {
	clock timeutil.Clock
	sm    statemachine.System
	cfg   Config
	svc   Services

	infraFailedSince time.Time
	lastReconciledAt time.Time
}

func New(clock timeutil.Clock, sm statemachine.System, cfg Config, svc Services) Scheduler {
	if cfg.TickInterval <= 0 {
		cfg.TickInterval = 30 * time.Second
	}
	if cfg.Location == nil {
		cfg.Location = time.UTC
	}
	if cfg.ReconciliationInterval <= 0 {
		cfg.ReconciliationInterval = 5 * time.Minute
	}
	return Scheduler{clock: clock, sm: sm, cfg: cfg, svc: svc}
}

func (s *Scheduler) Run(ctx context.Context) error {
	for {
		if err := s.Step(ctx); err != nil {
			if errors.Is(err, statemachine.ErrSystemHalted) {
				s.logWarn("scheduler paused in HALT", "err", err)
			} else if err := s.halt(ctx, "scheduler_error", err.Error(), ""); err != nil {
				return err
			}
		}
		if !s.clock.Sleep(ctx.Done(), s.cfg.TickInterval) {
			return ctx.Err()
		}
	}
}

func (s *Scheduler) Step(ctx context.Context) error {
	if err := s.checkInfrastructure(ctx); err != nil {
		return err
	}
	now := s.clock.Now().In(s.cfg.Location)
	phase := s.phase(now)
	switch phase {
	case domain.StateWaitExitWindow:
		return s.waitExit(ctx, now)
	case domain.StatePlaceExitOrders:
		return s.placeExitOrders(ctx, now)
	case domain.StateMonitorExitOrders:
		return s.monitorExitOrders(ctx, now)
	case domain.StateReconcile:
		return s.failOpenPositionsAtHardDeadline(ctx)
	case domain.StateGenerateSignals:
		return s.prepareSignals(ctx, now)
	case domain.StatePlaceEntryOrders:
		return s.placeEntryOrders(ctx, now)
	case domain.StateMonitorEntryOrders:
		return s.monitorEntryOrders(ctx, now)
	case domain.StateHoldOvernight:
		return s.holdOvernight(ctx)
	default:
		return s.sm.Heartbeat(ctx, domain.StateSleep)
	}
}

func (s Scheduler) phase(now time.Time) domain.SystemState {
	tod := sinceMidnight(now)
	switch {
	case tod >= s.cfg.ExitWatchStart.Duration && tod < s.cfg.ExitWindowStart.Duration:
		return domain.StateWaitExitWindow
	case tod >= s.cfg.ExitWindowStart.Duration && tod < s.cfg.ExitWindowEnd.Duration:
		return domain.StatePlaceExitOrders
	case tod >= s.cfg.ExitWindowEnd.Duration && tod < s.cfg.HardExitDeadline.Duration:
		return domain.StateMonitorExitOrders
	case tod >= s.cfg.HardExitDeadline.Duration && tod < s.cfg.EntrySignalTime.Duration:
		return domain.StateReconcile
	case tod >= s.cfg.EntrySignalTime.Duration && tod < s.cfg.EntryWindowStart.Duration:
		return domain.StateGenerateSignals
	case tod >= s.cfg.EntryWindowStart.Duration && tod < s.cfg.NoNewEntryAfter.Duration:
		return domain.StatePlaceEntryOrders
	case tod >= s.cfg.NoNewEntryAfter.Duration:
		return domain.StateHoldOvernight
	default:
		return domain.StateSleep
	}
}

func (s *Scheduler) prepareSignals(ctx context.Context, now time.Time) error {
	if err := s.transitionSequence(ctx,
		domain.StateInit,
		domain.StateSyncInstruments,
		domain.StateSyncMarketData,
		domain.StateGenerateSignals,
	); err != nil {
		return err
	}
	if err := s.svc.Registry.SyncMetadata(ctx); err != nil {
		return err
	}
	tradeDate := tradingDate(now)
	instrumentsList, err := s.svc.Repo.ListInstruments(ctx, false)
	if err != nil {
		return err
	}
	if err := s.svc.MarketData.BackfillDaily(ctx, instrumentsList, tradeDate.AddDate(0, 0, -s.cfg.RollingLong-10), tradeDate); err != nil {
		return err
	}
	minuteFrom := s.cfg.EntryWindowStart.On(tradeDate.AddDate(0, 0, -intervalVolumeLookbackDays), s.cfg.Location)
	minuteTo := s.cfg.ExitWindowEnd.On(tradeDate, s.cfg.Location)
	if err := s.svc.MarketData.BackfillMinute(ctx, instrumentsList, minuteFrom, minuteTo); err != nil {
		s.logWarn("minute backfill failed; liquidity will fall back to ADV", "err", err)
	}
	if err := s.applySizeReductionRule(ctx, tradeDate, false); err != nil {
		return err
	}
	portfolio, err := s.svc.Gateway.GetPortfolio(ctx, s.svc.AccountID)
	if err != nil {
		return err
	}
	openPositions, err := s.svc.Repo.ListOpenPositions(ctx, s.svc.AccountIDHash)
	if err != nil {
		return err
	}
	for _, instrument := range instrumentsList {
		if err := s.generateInstrumentSignal(ctx, now, tradeDate, portfolio, len(openPositions), instrument); err != nil {
			return err
		}
	}
	return s.transitionTo(ctx, domain.StateWaitEntryWindow)
}

func (s Scheduler) generateInstrumentSignal(ctx context.Context, now, tradeDate time.Time, portfolio domain.Portfolio, openPositionCount int, instrument domain.Instrument) error {
	book, err := s.svc.MarketData.LatestQuote(ctx, instrument.InstrumentUID, s.cfg.QuoteDepth, s.cfg.MaxQuoteAge)
	if err != nil {
		return s.saveRejectedSignal(ctx, tradeDate, instrument, "quote_unavailable", err)
	}
	spread, err := spreadFromBook(book, instrument.MinPriceIncrement)
	if err != nil {
		return s.saveRejectedSignal(ctx, tradeDate, instrument, "spread_unavailable", err)
	}
	tradingStatus, err := s.svc.Gateway.GetTradingStatus(ctx, instrument.InstrumentUID)
	if err != nil {
		tradingStatus = domain.TradingStatusUnknown
	}
	feature, err := s.svc.Features.Recompute(ctx, instrument, tradeDate, spread)
	if err != nil {
		return s.saveRejectedSignal(ctx, tradeDate, instrument, "features_unavailable", err)
	}
	remaining, err := s.svc.FreeOrders.Check(ctx, tradeDate, instrument, s.maxOrderAttemptsPerTrade())
	freeOrderOK := err == nil
	sig := s.svc.Signals.Evaluate(signal.Candidate{
		Instrument:    instrument,
		Features:      feature,
		TradingStatus: tradingStatus,
		FreeOrderOK:   freeOrderOK,
		OpenPositions: openPositionCount,
		TradeDate:     tradeDate,
		ExtraContext: map[string]any{
			"free_orders_remaining": remaining,
			"quote_time":            book.Time.Format(time.RFC3339),
			"spread_bps":            spread.SpreadBps.String(),
		},
	})
	if sig.Decision == domain.DecisionEnter {
		sized, sizingErr := s.sizeSignal(ctx, portfolio, instrument, feature, book, 1)
		switch {
		case sizingErr != nil:
			sig.Decision = domain.DecisionReject
			sig.RejectReason = sizingErr.Error()
		case sized.Lots <= 0:
			sig.Decision = domain.DecisionReject
			if isSizingSkipReason(sized.Reason) {
				sig.Decision = domain.DecisionSkip
			}
			sig.RejectReason = sized.Reason
		default:
			sig.TargetLots = sized.Lots
			sig.TargetNotional = sized.TargetNotional
		}
	}
	if err := s.svc.Repo.UpsertSignal(ctx, sig); err != nil {
		return err
	}
	return s.notifySignal(ctx, now, sig)
}

func (s Scheduler) saveRejectedSignal(ctx context.Context, tradeDate time.Time, instrument domain.Instrument, reason string, cause error) error {
	sig := domain.Signal{
		TradeDate:     tradeDate,
		InstrumentUID: instrument.InstrumentUID,
		Decision:      domain.DecisionReject,
		RejectReason:  reason,
		ContextJSON:   fmt.Sprintf(`{"error":%q}`, cause.Error()),
		CreatedAt:     s.nowUTC(),
	}
	return s.svc.Repo.UpsertSignal(ctx, sig)
}

func (s Scheduler) sizeSignal(_ context.Context, portfolio domain.Portfolio, instrument domain.Instrument, feature domain.FeatureSet, book domain.OrderBook, selected int) (risk.SizingResult, error) {
	bid, ask, err := bestBidAsk(book)
	if err != nil {
		return risk.SizingResult{}, err
	}
	price, err := execution.LimitBuyPrice(bid, ask, instrument.MinPriceIncrement, s.cfg.PassiveImproveTicks)
	if err != nil {
		return risk.SizingResult{}, err
	}
	return s.svc.Sizer.Size(risk.SizingInput{
		Portfolio:           portfolio,
		SelectedInstruments: selected,
		LimitPrice:          price,
		Lot:                 instrument.Lot,
		EntryIntervalVolume: feature.EntryIntervalVolume,
		ExitIntervalVolume:  feature.ExitIntervalVolume,
		Q05OvernightAbs:     money.Abs(feature.SigmaOn60).Mul(decimal.NewFromFloat(1.65)),
	}), nil
}

func (s *Scheduler) placeEntryOrders(ctx context.Context, now time.Time) error {
	if err := s.transitionTo(ctx, domain.StatePlaceEntryOrders); err != nil {
		return err
	}
	tradeDate := tradingDate(now)
	entryDeadline := s.cfg.NoNewEntryAfter.On(now, s.cfg.Location).UTC()
	if !s.nowUTC().Before(entryDeadline) {
		return s.closeEntryWindow(ctx)
	}
	signals, err := s.svc.Repo.ListSignals(ctx, tradeDate)
	if err != nil {
		return err
	}
	existing, err := s.svc.Repo.ListOrders(ctx, s.svc.AccountIDHash, tradeDate, tradeDate)
	if err != nil {
		return err
	}
	openPositions, err := s.svc.Repo.ListOpenPositions(ctx, s.svc.AccountIDHash)
	if err != nil {
		return err
	}
	instrumentByUID, err := s.instrumentMap(ctx)
	if err != nil {
		return err
	}
	for _, sig := range signals {
		if sig.Decision != domain.DecisionEnter || sig.TargetLots <= 0 || hasOrder(existing, sig.InstrumentUID, domain.SideBuy) {
			continue
		}
		instrument, ok := instrumentByUID[sig.InstrumentUID]
		if !ok {
			return fmt.Errorf("instrument %s is not in registry", sig.InstrumentUID)
		}
		if !s.nowUTC().Before(entryDeadline) {
			return s.closeEntryWindow(ctx)
		}
		if _, err := s.svc.FreeOrders.Check(ctx, tradeDate, instrument, s.maxOrderAttemptsPerTrade()); err != nil {
			if insertErr := s.svc.Repo.InsertRiskEvent(ctx, domain.RiskEvent{
				Severity:      domain.SeverityWarn,
				EventType:     "pre_trade_reject",
				InstrumentUID: sig.InstrumentUID,
				Message:       err.Error(),
				ContextJSON:   `{"reason":"free_order_budget_insufficient"}`,
			}); insertErr != nil {
				return insertErr
			}
			continue
		}
		book, err := s.svc.MarketData.LatestQuote(ctx, sig.InstrumentUID, s.cfg.QuoteDepth, s.cfg.MaxQuoteAge)
		if err != nil {
			return err
		}
		tradingStatus, err := s.svc.Gateway.GetTradingStatus(ctx, sig.InstrumentUID)
		if err != nil {
			tradingStatus = domain.TradingStatusUnknown
		}
		portfolio, err := s.svc.Gateway.GetPortfolio(ctx, s.svc.AccountID)
		if err != nil {
			return err
		}
		pre := s.svc.Risk.PreTradeCheck(risk.PreTradeInput{
			Portfolio:       portfolio,
			OpenPositions:   len(openPositions),
			TradingStatus:   tradingStatus,
			QuoteReceivedAt: book.ReceivedAt,
			Now:             now.UTC(),
			MarketClose:     s.cfg.EntryWindowEnd.On(now, s.cfg.Location).UTC(),
		})
		if !pre.Allowed {
			if err := s.svc.Repo.InsertRiskEvent(ctx, domain.RiskEvent{
				Severity:      domain.SeverityWarn,
				EventType:     "pre_trade_reject",
				InstrumentUID: sig.InstrumentUID,
				Message:       pre.Reason,
				ContextJSON:   "{}",
			}); err != nil {
				return err
			}
			continue
		}
		placed, err := s.svc.Execution.PlaceEntry(ctx, s.svc.AccountIDHash, instrument, tradeDate, sig.TargetLots, book, s.cfg.PassiveImproveTicks, 1)
		if err != nil && !errors.Is(err, execution.ErrBrokerOrdersDisabled) {
			return err
		}
		_ = s.svc.Notifier.Info(ctx, fmt.Sprintf("entry order %s %s lots=%d status=%s", instrument.Ticker, placed.Side, placed.QuantityLots, placed.Status))
		if placed.FilledLots > 0 {
			if err := s.recordEntryFill(ctx, instrument, placed); err != nil {
				return err
			}
		}
		existing = append(existing, placed)
	}
	return s.transitionTo(ctx, domain.StateMonitorEntryOrders)
}

func (s *Scheduler) monitorEntryOrders(ctx context.Context, now time.Time) error {
	if err := s.transitionTo(ctx, domain.StateMonitorEntryOrders); err != nil {
		return err
	}
	orders, err := s.svc.Repo.ListActiveOrders(ctx, s.svc.AccountIDHash)
	if err != nil {
		return err
	}
	instrumentByUID, err := s.instrumentMap(ctx)
	if err != nil {
		return err
	}
	deadline := s.cfg.NoNewEntryAfter.On(now, s.cfg.Location).UTC()
	if !s.nowUTC().Before(deadline) {
		return s.closeEntryWindow(ctx)
	}
	for _, order := range orders {
		if order.Side != domain.SideBuy || order.BrokerOrderID == "" {
			continue
		}
		instrument, ok := instrumentByUID[order.InstrumentUID]
		if !ok {
			return fmt.Errorf("instrument %s is not in registry", order.InstrumentUID)
		}
		monitored, err := s.svc.Execution.MonitorUntil(ctx, order, execution.MonitorConfig{
			Deadline:     deadline,
			PollInterval: s.cfg.OrderPollInterval,
			MaxAttempts:  s.cfg.MaxEntryOrderAttempts,
			RepostAfter:  repostAfter(now, deadline, s.cfg.MaxEntryOrderAttempts, s.cfg.OrderPollInterval),
			Instrument:   instrument,
			ImproveTicks: s.cfg.PassiveImproveTicks,
			Quote: func(ctx context.Context, instrumentUID string) (domain.OrderBook, error) {
				return s.svc.MarketData.LatestQuote(ctx, instrumentUID, s.cfg.QuoteDepth, s.cfg.MaxQuoteAge)
			},
		})
		if err != nil {
			return err
		}
		if monitored.FilledLots > order.FilledLots || monitored.Commission.GreaterThan(order.Commission) {
			if err := s.recordEntryFill(ctx, instrument, monitored); err != nil {
				return err
			}
		}
	}
	if sinceMidnight(s.nowUTC().In(s.cfg.Location)) >= s.cfg.NoNewEntryAfter.Duration {
		return s.closeEntryWindow(ctx)
	}
	return nil
}

func (s Scheduler) waitExit(ctx context.Context, _ time.Time) error {
	return s.transitionTo(ctx, domain.StateWaitExitWindow)
}

func (s *Scheduler) holdOvernight(ctx context.Context) error {
	if err := s.closeEntryWindow(ctx); err != nil {
		return err
	}
	return s.periodicReconcile(ctx)
}

func (s *Scheduler) placeExitOrders(ctx context.Context, now time.Time) error {
	if err := s.transitionTo(ctx, domain.StatePlaceExitOrders); err != nil {
		return err
	}
	positionsList, err := s.svc.Repo.ListOpenPositions(ctx, s.svc.AccountIDHash)
	if err != nil {
		return err
	}
	existing, err := s.svc.Repo.ListOrders(ctx, s.svc.AccountIDHash, tradingDate(now).AddDate(0, 0, -1), tradingDate(now))
	if err != nil {
		return err
	}
	instrumentByUID, err := s.instrumentMap(ctx)
	if err != nil {
		return err
	}
	for _, pos := range positionsList {
		if pos.Lots <= 0 || hasOrder(existing, pos.InstrumentUID, domain.SideSell) {
			continue
		}
		instrument, ok := instrumentByUID[pos.InstrumentUID]
		if !ok {
			return fmt.Errorf("instrument %s is not in registry", pos.InstrumentUID)
		}
		book, err := s.svc.MarketData.LatestQuote(ctx, pos.InstrumentUID, s.cfg.QuoteDepth, s.cfg.MaxQuoteAge)
		if err != nil {
			return err
		}
		tradingStatus, err := s.svc.Gateway.GetTradingStatus(ctx, pos.InstrumentUID)
		if err != nil {
			tradingStatus = domain.TradingStatusUnknown
		}
		portfolio, err := s.svc.Gateway.GetPortfolio(ctx, s.svc.AccountID)
		if err != nil {
			return err
		}
		pre := s.svc.Risk.PreTradeCheck(risk.PreTradeInput{
			Portfolio:       portfolio,
			OpenPositions:   len(positionsList),
			TradingStatus:   tradingStatus,
			QuoteReceivedAt: book.ReceivedAt,
			Now:             now.UTC(),
			MarketClose:     s.cfg.HardExitDeadline.On(now, s.cfg.Location).UTC(),
		})
		if !pre.Allowed {
			return fmt.Errorf("exit pre-trade rejected: %s", pre.Reason)
		}
		placed, err := s.svc.Execution.PlaceExit(ctx, s.svc.AccountIDHash, instrument, pos.OpenTradeDate, pos.Lots, book, s.cfg.PassiveImproveTicks, 1)
		if err != nil && !errors.Is(err, execution.ErrBrokerOrdersDisabled) {
			return err
		}
		if placed.FilledLots > 0 || placed.Commission.IsPositive() {
			if err := s.recordExitFill(ctx, pos, placed); err != nil {
				return err
			}
			existing = append(existing, placed)
			continue
		}
		pos.Status = domain.PositionExitOrderSent
		if err := s.svc.Repo.UpsertPosition(ctx, pos); err != nil {
			return err
		}
		_ = s.svc.Notifier.Info(ctx, fmt.Sprintf("exit order %s lots=%d status=%s", instrument.Ticker, placed.QuantityLots, placed.Status))
		existing = append(existing, placed)
	}
	return s.transitionTo(ctx, domain.StateMonitorExitOrders)
}

func (s *Scheduler) monitorExitOrders(ctx context.Context, now time.Time) error {
	if err := s.transitionTo(ctx, domain.StateMonitorExitOrders); err != nil {
		return err
	}
	orders, err := s.svc.Repo.ListActiveOrders(ctx, s.svc.AccountIDHash)
	if err != nil {
		return err
	}
	openPositions, err := s.svc.Repo.ListOpenPositions(ctx, s.svc.AccountIDHash)
	if err != nil {
		return err
	}
	positionByInstrument := make(map[string]domain.Position, len(openPositions))
	for _, pos := range openPositions {
		positionByInstrument[pos.InstrumentUID] = pos
	}
	instrumentByUID, err := s.instrumentMap(ctx)
	if err != nil {
		return err
	}
	deadline := s.cfg.HardExitDeadline.On(now, s.cfg.Location).UTC()
	for _, order := range orders {
		if order.Side != domain.SideSell || order.BrokerOrderID == "" {
			continue
		}
		instrument, ok := instrumentByUID[order.InstrumentUID]
		if !ok {
			return fmt.Errorf("instrument %s is not in registry", order.InstrumentUID)
		}
		monitored, err := s.svc.Execution.MonitorUntil(ctx, order, execution.MonitorConfig{
			Deadline:     deadline,
			PollInterval: s.cfg.OrderPollInterval,
			MaxAttempts:  s.cfg.MaxExitOrderAttempts,
			RepostAfter:  repostAfter(now, deadline, s.cfg.MaxExitOrderAttempts, s.cfg.OrderPollInterval),
			Instrument:   instrument,
			ImproveTicks: s.cfg.PassiveImproveTicks,
			Quote: func(ctx context.Context, instrumentUID string) (domain.OrderBook, error) {
				return s.svc.MarketData.LatestQuote(ctx, instrumentUID, s.cfg.QuoteDepth, s.cfg.MaxQuoteAge)
			},
		})
		if err != nil {
			return err
		}
		if monitored.FilledLots > order.FilledLots || monitored.Commission.GreaterThan(order.Commission) {
			fill := exitFillDelta(order, monitored)
			if fill.FilledLots <= 0 && fill.Commission.IsZero() {
				continue
			}
			pos, ok := positionByInstrument[monitored.InstrumentUID]
			if !ok {
				return fmt.Errorf("exit fill for unknown local position %s", monitored.InstrumentUID)
			}
			updated, err := s.recordExitFillWithPosition(ctx, pos, fill)
			if err != nil {
				return err
			}
			positionByInstrument[monitored.InstrumentUID] = updated
		}
	}
	if sinceMidnight(s.nowUTC().In(s.cfg.Location)) >= s.cfg.HardExitDeadline.Duration {
		return s.failOpenPositionsAtHardDeadline(ctx)
	}
	return nil
}

func (s *Scheduler) reconcileAndReport(ctx context.Context, now time.Time) error {
	tradeDate := tradingDate(now)
	sent, err := s.svc.Repo.WasDailyReportSent(ctx, tradeDate, s.svc.AccountIDHash)
	if err != nil {
		return err
	}
	if sent {
		s.logWarn("daily report already sent; skipping duplicate", "date", tradeDate.Format("2006-01-02"))
		return s.transitionTo(ctx, domain.StateSleep)
	}
	if err := s.transitionTo(ctx, domain.StateReconcile); err != nil {
		return err
	}
	if err := s.reconcileCritical(ctx, "reconciliation_critical"); err != nil {
		return err
	}
	return s.sendDailyReport(ctx, now, "ok")
}

func (s *Scheduler) sendDailyReport(ctx context.Context, now time.Time, riskStatus string) error {
	tradeDate := tradingDate(now)
	sent, err := s.svc.Repo.WasDailyReportSent(ctx, tradeDate, s.svc.AccountIDHash)
	if err != nil {
		return err
	}
	if sent {
		s.logWarn("daily report already sent; skipping duplicate", "date", tradeDate.Format("2006-01-02"))
		if !s.hasStateMachine() {
			return nil
		}
		return s.transitionTo(ctx, domain.StateSleep)
	}
	signals, err := s.svc.Repo.ListSignals(ctx, tradeDate)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	positionsList, err := s.svc.Repo.ListPositions(ctx, s.svc.AccountIDHash, tradeDate.AddDate(0, 0, -1), tradeDate)
	if err != nil {
		return err
	}
	orders, err := s.svc.Repo.ListOrders(ctx, s.svc.AccountIDHash, tradeDate.AddDate(0, 0, -1), tradeDate)
	if err != nil {
		return err
	}
	if err := s.applySizeReductionRule(ctx, tradeDate, true); err != nil {
		return err
	}
	if s.hasStateMachine() {
		if err := s.transitionTo(ctx, domain.StateReport); err != nil {
			return err
		}
	}
	msg := report.ComposeDaily(report.DailyInput{
		Date:       tradeDate,
		Mode:       s.cfg.Mode,
		Signals:    signals,
		Positions:  positionsList,
		Orders:     orders,
		RiskStatus: riskStatus,
	})
	if err := s.svc.Notifier.Report(ctx, msg); err != nil {
		return err
	}
	if err := s.svc.Repo.MarkDailyReportSent(ctx, tradeDate, s.svc.AccountIDHash); err != nil {
		return err
	}
	if !s.hasStateMachine() {
		return nil
	}
	return s.transitionTo(ctx, domain.StateSleep)
}

func (s *Scheduler) applySizeReductionRule(ctx context.Context, tradeDate time.Time, emitEvent bool) error {
	averageError, count, ok, err := s.averageExpectedErrorBps(ctx, tradeDate, sizeReductionWindowTrades)
	if err != nil {
		return err
	}
	if !ok || count < sizeReductionWindowTrades || averageError.GreaterThanOrEqual(decimal.NewFromInt(-10)) {
		s.svc.Sizer = s.svc.Sizer.WithSizeFactor(decimal.NewFromInt(1))
		return nil
	}
	factor := decimal.NewFromFloat(sizeReductionFactor)
	s.svc.Sizer = s.svc.Sizer.WithSizeFactor(factor)
	if !emitEvent {
		return nil
	}
	return s.svc.Repo.InsertRiskEvent(ctx, domain.RiskEvent{
		Severity:    domain.SeverityWarn,
		EventType:   "size_reduction_rule_triggered",
		Message:     fmt.Sprintf("average expected_error_bps over %d trades is %s; sizing factor set to %s", count, averageError.StringFixed(2), factor.String()),
		ContextJSON: fmt.Sprintf(`{"average_expected_error_bps":%q,"trades":%d,"size_factor":%q}`, averageError.String(), count, factor.String()),
	})
}

func (s Scheduler) averageExpectedErrorBps(ctx context.Context, tradeDate time.Time, limit int) (decimal.Decimal, int, bool, error) {
	if limit <= 0 {
		return decimal.Zero, 0, false, nil
	}
	positionsList, err := s.svc.Repo.ListPositions(ctx, s.svc.AccountIDHash, tradeDate.AddDate(0, 0, -120), tradeDate)
	if err != nil {
		return decimal.Zero, 0, false, err
	}
	sort.Slice(positionsList, func(i, j int) bool {
		return positionsList[i].UpdatedAt.After(positionsList[j].UpdatedAt)
	})
	signalsByDate := make(map[string][]domain.Signal)
	var errorsBps []decimal.Decimal
	for _, pos := range positionsList {
		if pos.Status != domain.PositionExitFilled {
			continue
		}
		key := tradingDate(pos.OpenTradeDate).Format("2006-01-02")
		signals, ok := signalsByDate[key]
		if !ok {
			signals, err = s.svc.Repo.ListSignals(ctx, tradingDate(pos.OpenTradeDate))
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return decimal.Zero, 0, false, err
			}
			signalsByDate[key] = signals
		}
		for _, sig := range signals {
			if sig.InstrumentUID != pos.InstrumentUID || sig.Decision != domain.DecisionEnter {
				continue
			}
			errorsBps = append(errorsBps, pos.RealizedEdgeBps.Sub(sig.NetEdgeBps))
			break
		}
		if len(errorsBps) == limit {
			break
		}
	}
	if len(errorsBps) == 0 {
		return decimal.Zero, 0, false, nil
	}
	sum := decimal.Zero
	for _, value := range errorsBps {
		sum = sum.Add(value)
	}
	return sum.Div(decimal.NewFromInt(int64(len(errorsBps)))), len(errorsBps), true, nil
}

func (s *Scheduler) checkInfrastructure(ctx context.Context) error {
	if s.cfg.MaxClockDrift <= 0 || s.svc.Gateway == nil {
		return nil
	}
	serverTime, err := s.svc.Gateway.GetServerTime(ctx)
	if err != nil {
		if s.cfg.Mode == domain.ModePaper {
			s.infraFailedSince = time.Time{}
			return nil
		}
		return s.recordInfrastructureFailure(fmt.Errorf("server_time_unavailable: %w", err))
	}
	drift := timeutil.Drift(s.nowUTC(), serverTime)
	if drift > s.cfg.MaxClockDrift {
		return s.recordInfrastructureFailure(fmt.Errorf("server_clock_drift_too_high: %s > %s", drift, s.cfg.MaxClockDrift))
	}
	s.infraFailedSince = time.Time{}
	return nil
}

func (s *Scheduler) recordInfrastructureFailure(err error) error {
	now := s.nowUTC()
	if s.infraFailedSince.IsZero() {
		s.infraFailedSince = now
		s.logWarn("infrastructure check failed; waiting for outage threshold", "err", err, "threshold", s.cfg.APIOutageHalt)
		return nil
	}
	if s.cfg.APIOutageHalt <= 0 || now.Sub(s.infraFailedSince) >= s.cfg.APIOutageHalt {
		return err
	}
	s.logWarn("infrastructure check still failing", "err", err, "elapsed", now.Sub(s.infraFailedSince), "threshold", s.cfg.APIOutageHalt)
	return nil
}

func (s Scheduler) cancelActiveOrders(ctx context.Context, side domain.Side, fallbackStatus domain.OrderStatus, reason string) error {
	orders, err := s.svc.Repo.ListActiveOrders(ctx, s.svc.AccountIDHash)
	if err != nil {
		return err
	}
	cancelled := 0
	for _, order := range orders {
		if order.Side != side {
			continue
		}
		if order.BrokerOrderID != "" && s.cfg.Mode.AllowsBrokerOrders() {
			if err := s.svc.Execution.Cancel(ctx, order); err != nil {
				return fmt.Errorf("cancel %s order %s: %w", side, order.ClientOrderID, err)
			}
			cancelled++
			continue
		}
		if err := s.svc.Repo.UpdateOrderStatus(ctx, order.ClientOrderID, fallbackStatus, order.FilledLots, order.RawStateJSON); err != nil {
			return fmt.Errorf("mark %s order %s %s: %w", side, order.ClientOrderID, fallbackStatus, err)
		}
		cancelled++
	}
	if cancelled == 0 {
		return nil
	}
	if err := s.svc.Repo.InsertRiskEvent(ctx, domain.RiskEvent{
		Severity:    domain.SeverityWarn,
		EventType:   reason,
		Message:     fmt.Sprintf("cancelled %d active %s orders at window boundary", cancelled, side),
		ContextJSON: "{}",
	}); err != nil {
		return err
	}
	return nil
}

func (s Scheduler) closeEntryWindow(ctx context.Context) error {
	if err := s.cancelActiveOrders(ctx, domain.SideBuy, domain.OrderStatusCancelled, "entry_window_closed"); err != nil {
		return err
	}
	return s.transitionTo(ctx, domain.StateHoldOvernight)
}

func (s *Scheduler) recordEntryFill(ctx context.Context, instrument domain.Instrument, order domain.Order) error {
	pos, err := s.svc.Positions.OnEntryFill(ctx, s.svc.AccountIDHash, instrument, order)
	if err != nil {
		return err
	}
	_ = s.svc.Notifier.Info(ctx, fmt.Sprintf("entry fill %s lots=%d status=%s", order.InstrumentUID, order.FilledLots, pos.Status))
	if err := s.handleCommission(ctx, order.InstrumentUID, order.Commission); err != nil {
		return err
	}
	return s.reconcileAfterFill(ctx)
}

func (s *Scheduler) recordExitFill(ctx context.Context, pos domain.Position, order domain.Order) error {
	_, err := s.recordExitFillWithPosition(ctx, pos, order)
	return err
}

func (s *Scheduler) recordExitFillWithPosition(ctx context.Context, pos domain.Position, fill domain.Order) (domain.Position, error) {
	updated, err := s.svc.Positions.OnExitFill(ctx, pos, fill)
	if err != nil {
		return domain.Position{}, err
	}
	_ = s.svc.Notifier.Info(ctx, fmt.Sprintf("exit fill %s lots=%d status=%s pnl=%s", fill.InstrumentUID, fill.FilledLots, updated.Status, updated.NetPnL.StringFixed(2)))
	if err := s.handleCommission(ctx, fill.InstrumentUID, fill.Commission); err != nil {
		return domain.Position{}, err
	}
	if err := s.reconcileAfterFill(ctx); err != nil {
		return domain.Position{}, err
	}
	return updated, nil
}

func (s *Scheduler) handleCommission(ctx context.Context, instrumentUID string, commission decimal.Decimal) error {
	if !risk.CommissionBreached(commission, s.cfg.RequireZeroCommission) {
		return nil
	}
	reason := fmt.Sprintf("actual commission %s > 0", commission.StringFixed(2))
	if s.cfg.QuarantineOnNonZero {
		if err := s.svc.Repo.QuarantineInstrument(ctx, instrumentUID, reason); err != nil {
			return err
		}
	}
	return s.halt(ctx, "actual_commission_nonzero", reason, instrumentUID)
}

func (s *Scheduler) reconcileAfterFill(ctx context.Context) error {
	if !s.cfg.Mode.AllowsBrokerOrders() {
		return nil
	}
	return s.reconcileCritical(ctx, "reconciliation_after_fill_critical")
}

func (s *Scheduler) periodicReconcile(ctx context.Context) error {
	if !s.cfg.Mode.AllowsBrokerOrders() {
		return nil
	}
	now := s.nowUTC()
	if !s.lastReconciledAt.IsZero() && now.Sub(s.lastReconciledAt) < s.cfg.ReconciliationInterval {
		return nil
	}
	return s.reconcileCritical(ctx, "periodic_reconciliation_critical")
}

func (s *Scheduler) reconcileCritical(ctx context.Context, eventType string) error {
	diffs, err := s.svc.Reconcile.Run(ctx)
	if err != nil {
		return err
	}
	s.lastReconciledAt = s.nowUTC()
	for _, diff := range diffs {
		if diff.Kind == "actual_commission_nonzero" && diff.InstrumentUID != "" && s.cfg.QuarantineOnNonZero {
			if err := s.svc.Repo.QuarantineInstrument(ctx, diff.InstrumentUID, diff.Message); err != nil {
				return err
			}
		}
	}
	if reconciliation.HasCritical(diffs) {
		return s.halt(ctx, eventType, "critical reconciliation diff", "")
	}
	return nil
}

func (s *Scheduler) failOpenPositionsAtHardDeadline(ctx context.Context) error {
	if err := s.cancelActiveOrders(ctx, domain.SideSell, domain.OrderStatusExpired, "hard_exit_deadline_cancel"); err != nil {
		return err
	}
	positionsList, err := s.svc.Repo.ListOpenPositions(ctx, s.svc.AccountIDHash)
	if err != nil {
		return err
	}
	var failed []domain.Position
	now := s.nowUTC()
	for _, pos := range positionsList {
		switch pos.Status {
		case domain.PositionHoldingOvernight, domain.PositionExitPartiallyFilled, domain.PositionExitOrderSent:
			pos.Status = domain.PositionExitFailed
			pos.UpdatedAt = now
			if err := s.svc.Repo.UpsertPosition(ctx, pos); err != nil {
				return err
			}
			failed = append(failed, pos)
			_ = s.svc.Notifier.Alert(ctx, fmt.Sprintf("exit_failed: %s lots=%d", pos.InstrumentUID, pos.Lots))
		default:
		}
	}
	if len(failed) == 0 {
		return s.reconcileAndReport(ctx, s.nowUTC().In(s.cfg.Location))
	}
	if err := s.sendDailyReport(ctx, s.nowUTC().In(s.cfg.Location), "hard_exit_deadline_missed"); err != nil {
		s.logWarn("daily report failed after hard deadline", "err", err)
	}
	return s.svc.Risk.Halt(ctx, s.cfg.Mode, "hard_exit_deadline_missed", fmt.Sprintf("%d positions remain open after hard deadline", len(failed)), "")
}

func (s Scheduler) nowUTC() time.Time {
	if s.clock != nil {
		return s.clock.Now().UTC()
	}
	return time.Now().UTC()
}

func repostAfter(now, deadline time.Time, attempts int, poll time.Duration) time.Duration {
	if attempts <= 1 {
		return 0
	}
	if poll <= 0 {
		poll = 500 * time.Millisecond
	}
	remaining := deadline.Sub(now)
	if remaining <= 0 {
		return poll
	}
	after := remaining / time.Duration(attempts)
	if after < poll {
		return poll
	}
	return after
}

func (s Scheduler) maxOrderAttemptsPerTrade() int {
	needed := s.cfg.MaxEntryOrderAttempts + s.cfg.MaxExitOrderAttempts
	if needed <= 0 {
		return 1
	}
	return needed
}

func isSizingSkipReason(reason string) bool {
	return reason == "lots_below_one" || reason == "min_order_notional"
}

func (s Scheduler) hasStateMachine() bool {
	return s.sm != (statemachine.System{})
}

func (s Scheduler) transitionSequence(ctx context.Context, states ...domain.SystemState) error {
	for _, state := range states {
		if err := s.transitionTo(ctx, state); err != nil {
			return err
		}
	}
	return nil
}

func (s Scheduler) transitionTo(ctx context.Context, to domain.SystemState) error {
	from, halted, reason, err := s.svc.Repo.GetSystemState(ctx)
	if err != nil {
		return err
	}
	if halted || from == domain.StateHalted {
		return fmt.Errorf("%w: %s", statemachine.ErrSystemHalted, reason)
	}
	if from == to {
		return s.sm.Heartbeat(ctx, to)
	}
	if err := s.sm.Transition(ctx, from, to); err != nil {
		return err
	}
	return nil
}

func (s Scheduler) halt(ctx context.Context, eventType, reason, instrumentUID string) error {
	_ = s.svc.Notifier.Alert(ctx, fmt.Sprintf("%s: %s", eventType, reason))
	return s.svc.Risk.Halt(ctx, s.cfg.Mode, eventType, reason, instrumentUID)
}

func (s Scheduler) notifySignal(ctx context.Context, _ time.Time, sig domain.Signal) error {
	return s.svc.Notifier.Info(ctx, fmt.Sprintf("signal %s decision=%s edge=%s reason=%s lots=%d", sig.InstrumentUID, sig.Decision, sig.NetEdgeBps.StringFixed(2), sig.RejectReason, sig.TargetLots))
}

func (s Scheduler) instrumentMap(ctx context.Context) (map[string]domain.Instrument, error) {
	instrumentsList, err := s.svc.Repo.ListInstruments(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make(map[string]domain.Instrument, len(instrumentsList))
	for _, instrument := range instrumentsList {
		out[instrument.InstrumentUID] = instrument
	}
	return out, nil
}

func (s Scheduler) logWarn(msg string, args ...any) {
	if s.svc.Log != nil {
		s.svc.Log.Warn(msg, args...)
	}
}

func exitFillDelta(previous, current domain.Order) domain.Order {
	fill := current
	fill.FilledLots = current.FilledLots - previous.FilledLots
	if fill.FilledLots < 0 {
		fill.FilledLots = 0
	}
	fill.Commission = current.Commission.Sub(previous.Commission)
	if fill.Commission.IsNegative() {
		fill.Commission = decimal.Zero
	}
	if fill.FilledLots > 0 {
		currentValue := current.AvgFillPrice.Mul(decimal.NewFromInt(current.FilledLots))
		previousValue := previous.AvgFillPrice.Mul(decimal.NewFromInt(previous.FilledLots))
		fill.AvgFillPrice = currentValue.Sub(previousValue).Div(decimal.NewFromInt(fill.FilledLots))
	}
	return fill
}

func spreadFromBook(book domain.OrderBook, tick decimal.Decimal) (features.SpreadResult, error) {
	bid, ask, err := bestBidAsk(book)
	if err != nil {
		return features.SpreadResult{}, err
	}
	return features.Spread(bid, ask, tick)
}

func bestBidAsk(book domain.OrderBook) (decimal.Decimal, decimal.Decimal, error) {
	bid, ok := book.BestBid()
	if !ok {
		return decimal.Zero, decimal.Zero, execution.ErrEmptyOrderBook
	}
	ask, ok := book.BestAsk()
	if !ok {
		return decimal.Zero, decimal.Zero, execution.ErrEmptyOrderBook
	}
	return bid, ask, nil
}

func hasOrder(orders []domain.Order, instrumentUID string, side domain.Side) bool {
	for _, order := range orders {
		if order.InstrumentUID == instrumentUID && order.Side == side && order.Status != domain.OrderStatusFailed && order.Status != domain.OrderStatusRejected {
			return true
		}
	}
	return false
}

func sinceMidnight(t time.Time) time.Duration {
	h, m, s := t.Clock()
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second
}

func tradingDate(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
