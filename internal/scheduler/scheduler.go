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

type Config struct {
	Mode                       domain.Mode
	Location                   *time.Location
	RollingLong                int
	IntervalVolumeLookbackDays int
	TickInterval               time.Duration
	EntrySignalTime            timeutil.TimeOfDay
	EntryWindowStart           timeutil.TimeOfDay
	EntryWindowEnd             timeutil.TimeOfDay
	NoNewEntryAfter            timeutil.TimeOfDay
	ExitWatchStart             timeutil.TimeOfDay
	ExitNotBefore              timeutil.TimeOfDay
	ExitWindowStart            timeutil.TimeOfDay
	ExitWindowEnd              timeutil.TimeOfDay
	HardExitDeadline           timeutil.TimeOfDay
	MarketClose                timeutil.TimeOfDay
	QuoteDepth                 int32
	MaxQuoteAge                time.Duration
	OrderPollInterval          time.Duration
	PassiveImproveTicks        int
	MaxEntryOrderAttempts      int
	MaxExitOrderAttempts       int
	MinTimeToClose             time.Duration
	MaxClockDrift              time.Duration
	APIOutageHalt              time.Duration
	RequireZeroCommission      bool
	QuarantineOnNonZero        bool
	FreeOrderCountPolicy       string
	ReconciliationInterval     time.Duration
	MaxOpenPositions           int
	SizeReductionWindowTrades  int
	SizeReductionFactor        decimal.Decimal
	SizeReductionTriggerBps    decimal.Decimal
	TradingCalendarExchange    string
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

type signalCandidate struct {
	Signal     domain.Signal
	Instrument domain.Instrument
	Feature    domain.FeatureSet
	Book       domain.OrderBook
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
	if cfg.IntervalVolumeLookbackDays <= 0 {
		cfg.IntervalVolumeLookbackDays = 20
	}
	if cfg.SizeReductionWindowTrades <= 0 {
		cfg.SizeReductionWindowTrades = 20
	}
	if !cfg.SizeReductionFactor.IsPositive() {
		cfg.SizeReductionFactor = decimal.RequireFromString("0.5")
	}
	if cfg.SizeReductionTriggerBps.IsZero() {
		cfg.SizeReductionTriggerBps = decimal.NewFromInt(-10)
	}
	if cfg.TradingCalendarExchange == "" {
		cfg.TradingCalendarExchange = "MOEX"
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

func (s Scheduler) GracefulShutdown(ctx context.Context) error {
	if s.svc.Repo == nil || s.svc.Execution == nil {
		return nil
	}
	return s.cancelActiveOrders(ctx, domain.SideBuy, domain.OrderStatusCancelled, "shutdown_cancel_active_orders")
}

func (s *Scheduler) Step(ctx context.Context) error {
	if err := s.checkInfrastructure(ctx); err != nil {
		return err
	}
	now := s.clock.Now().In(s.cfg.Location)
	reported, err := s.sendMissedDailyReport(ctx, now)
	if err != nil {
		return err
	}
	if reported {
		return nil
	}
	phase := s.phase(now)
	current, halted, reason, err := s.svc.Repo.GetSystemState(ctx)
	if err != nil {
		return err
	}
	if halted || current == domain.StateHalted {
		return fmt.Errorf("%w: %s", statemachine.ErrSystemHalted, reason)
	}
	switch phase {
	case domain.StateWaitExitWindow:
		return s.waitExit(ctx, now)
	case domain.StatePlaceExitOrders:
		if current == domain.StateMonitorExitOrders {
			return s.monitorExitOrders(ctx, now)
		}
		return s.placeExitOrders(ctx, now)
	case domain.StateMonitorExitOrders:
		return s.monitorExitOrders(ctx, now)
	case domain.StateReconcile:
		return s.failOpenPositionsAtHardDeadline(ctx)
	case domain.StateGenerateSignals:
		if signalPhaseAlreadyPrepared(current) {
			return s.sm.Heartbeat(ctx, current)
		}
		return s.prepareSignals(ctx, now)
	case domain.StatePlaceEntryOrders:
		if current == domain.StateMonitorEntryOrders {
			return s.monitorEntryOrders(ctx, now)
		}
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
	// WAIT_ENTRY_WINDOW is a persisted checkpoint after signal generation; wall-clock
	// phases move directly from GENERATE_SIGNALS to PLACE_ENTRY_ORDERS.
	tod := sinceMidnight(now)
	exitWindowStart := s.cfg.ExitWindowStart.Duration
	if s.cfg.ExitNotBefore.Duration > exitWindowStart {
		exitWindowStart = s.cfg.ExitNotBefore.Duration
	}
	switch {
	case tod >= s.cfg.ExitWatchStart.Duration && tod < exitWindowStart:
		return domain.StateWaitExitWindow
	case tod >= exitWindowStart && tod < s.cfg.ExitWindowEnd.Duration:
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
	dailyFrom := tradeDate.AddDate(0, 0, -s.cfg.RollingLong-10)
	if err := s.svc.MarketData.BackfillDaily(ctx, instrumentsList, dailyFrom, tradeDate); err != nil {
		return err
	}
	tradingDays, err := s.tradingDaysFromDailyCandles(ctx, instrumentsList, dailyFrom, tradeDate.AddDate(0, 0, -1))
	if err != nil {
		return err
	}
	s.svc.Features = s.svc.Features.WithTradingDays(tradingDays)
	minuteFrom := s.cfg.EntryWindowStart.On(tradeDate.AddDate(0, 0, -s.cfg.IntervalVolumeLookbackDays), s.cfg.Location)
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
	instrumentByUID := make(map[string]domain.Instrument, len(instrumentsList))
	for _, instrument := range instrumentsList {
		instrumentByUID[instrument.InstrumentUID] = instrument
	}
	existingExposure := positionsExposure(openPositions, instrumentByUID, portfolio)
	generated := make([]signalCandidate, 0, len(instrumentsList))
	for _, instrument := range instrumentsList {
		candidate, err := s.generateInstrumentSignal(ctx, tradeDate, len(openPositions), instrument)
		if err != nil {
			return err
		}
		generated = append(generated, candidate)
	}
	s.applyBatchSignalLimits(portfolio, existingExposure, len(openPositions), generated)
	for _, candidate := range generated {
		if err := s.svc.Repo.UpsertSignal(ctx, candidate.Signal); err != nil {
			return err
		}
		if err := s.notifySignal(ctx, now, candidate.Signal); err != nil {
			return err
		}
	}
	return s.transitionTo(ctx, domain.StateWaitEntryWindow)
}

func (s Scheduler) tradingDaysFromDailyCandles(ctx context.Context, instrumentsList []domain.Instrument, from, to time.Time) ([]time.Time, error) {
	if to.Before(from) {
		return nil, nil
	}
	seen := make(map[time.Time]struct{})
	for _, instrument := range instrumentsList {
		if !instrument.Enabled || instrument.Quarantine {
			continue
		}
		candles, err := s.svc.Repo.ListDailyCandles(ctx, instrument.InstrumentUID, from, to)
		if err != nil {
			return nil, fmt.Errorf("load daily candles for trading calendar %s: %w", instrument.Ticker, err)
		}
		for _, candle := range candles {
			seen[tradingDate(candle.TradeDate)] = struct{}{}
		}
	}
	days := make([]time.Time, 0, len(seen))
	for day := range seen {
		days = append(days, day)
	}
	sort.Slice(days, func(i, j int) bool {
		return days[i].Before(days[j])
	})
	return days, nil
}

func (s Scheduler) generateInstrumentSignal(ctx context.Context, tradeDate time.Time, openPositionCount int, instrument domain.Instrument) (signalCandidate, error) {
	book, err := s.svc.MarketData.LatestQuote(ctx, instrument.InstrumentUID, s.cfg.QuoteDepth, s.cfg.MaxQuoteAge)
	if err != nil {
		return s.rejectedSignal(tradeDate, instrument, "quote_unavailable", err), nil
	}
	spread, err := spreadFromBook(book, instrument.MinPriceIncrement)
	if err != nil {
		return s.rejectedSignal(tradeDate, instrument, "spread_unavailable", err), nil
	}
	tradingStatus, err := s.svc.Gateway.GetTradingStatus(ctx, instrument.InstrumentUID)
	if err != nil {
		tradingStatus = domain.TradingStatusUnknown
	}
	feature, err := s.svc.Features.Recompute(ctx, instrument, tradeDate, spread)
	if err != nil {
		return s.rejectedSignal(tradeDate, instrument, "features_unavailable", err), nil
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
	return signalCandidate{Signal: sig, Instrument: instrument, Feature: feature, Book: book}, nil
}

func (s Scheduler) rejectedSignal(tradeDate time.Time, instrument domain.Instrument, reason string, cause error) signalCandidate {
	sig := domain.Signal{
		TradeDate:     tradeDate,
		InstrumentUID: instrument.InstrumentUID,
		Decision:      domain.DecisionReject,
		RejectReason:  reason,
		ContextJSON:   fmt.Sprintf(`{"error":%q}`, cause.Error()),
		CreatedAt:     s.nowUTC(),
	}
	return signalCandidate{Signal: sig, Instrument: instrument}
}

func (s Scheduler) applyBatchSignalLimits(portfolio domain.Portfolio, existingExposure decimal.Decimal, openPositionCount int, generated []signalCandidate) {
	enterIndexes := make([]int, 0, len(generated))
	for i := range generated {
		if generated[i].Signal.Decision == domain.DecisionEnter {
			enterIndexes = append(enterIndexes, i)
		}
	}
	sort.SliceStable(enterIndexes, func(i, j int) bool {
		left := generated[enterIndexes[i]].Signal
		right := generated[enterIndexes[j]].Signal
		if left.Score.Equal(right.Score) {
			return left.InstrumentUID < right.InstrumentUID
		}
		return left.Score.GreaterThan(right.Score)
	})
	remainingSlots := len(enterIndexes)
	if s.cfg.MaxOpenPositions > 0 {
		remainingSlots = s.cfg.MaxOpenPositions - openPositionCount
		if remainingSlots < 0 {
			remainingSlots = 0
		}
		if remainingSlots > len(enterIndexes) {
			remainingSlots = len(enterIndexes)
		}
	}
	selectedCount := remainingSlots
	reservedCash := decimal.Zero
	for rank, index := range enterIndexes {
		candidate := &generated[index]
		if rank >= remainingSlots {
			candidate.Signal.Decision = domain.DecisionSkip
			candidate.Signal.TargetLots = 0
			candidate.Signal.TargetNotional = decimal.Zero
			candidate.Signal.RejectReason = signal.ReasonMaxPositions
			continue
		}
		sized, sizingErr := s.sizeSignal(portfolio, candidate.Instrument, candidate.Feature, candidate.Book, selectedCount, existingExposure, reservedCash)
		switch {
		case sizingErr != nil:
			candidate.Signal.Decision = domain.DecisionReject
			candidate.Signal.RejectReason = sizingErr.Error()
		case sized.Lots <= 0:
			candidate.Signal.Decision = domain.DecisionReject
			if isSizingSkipReason(sized.Reason) {
				candidate.Signal.Decision = domain.DecisionSkip
			}
			candidate.Signal.RejectReason = sized.Reason
		default:
			candidate.Signal.TargetLots = sized.Lots
			candidate.Signal.TargetNotional = sized.TargetNotional
			reservedCash = reservedCash.Add(sized.TargetNotional)
		}
	}
}

func (s Scheduler) sizeSignal(portfolio domain.Portfolio, instrument domain.Instrument, feature domain.FeatureSet, book domain.OrderBook, selected int, existingExposure, reservedCash decimal.Decimal) (risk.SizingResult, error) {
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
		ExistingExposure:    existingExposure,
		ReservedCash:        reservedCash,
		LimitPrice:          price,
		Lot:                 instrument.Lot,
		EntryIntervalVolume: feature.EntryIntervalVolume,
		ExitIntervalVolume:  feature.ExitIntervalVolume,
		Q05OvernightAbs:     feature.Q05On60Abs,
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
	sortSignalsForEntry(signals)
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
	portfolio, err := s.svc.Gateway.GetPortfolio(ctx, s.svc.AccountID)
	if err != nil {
		return err
	}
	baseExposure := positionsExposure(openPositions, instrumentByUID, portfolio)
	pendingExposure := ordersExposure(existing, instrumentByUID, domain.SideBuy, true)
	reservedCash := pendingExposure
	projectedOpenPositions := len(openPositions) + countActiveOrders(existing, domain.SideBuy, tradeDate)
	entryCandidates := entryOrderCandidates(signals, existing)
	for _, sig := range signals {
		if sig.Decision != domain.DecisionEnter || sig.TargetLots <= 0 || hasOrder(existing, sig.InstrumentUID, domain.SideBuy) {
			continue
		}
		remainingSelections := remainingSignalCount(entryCandidates, sig.InstrumentUID)
		if s.cfg.MaxOpenPositions > 0 && projectedOpenPositions >= s.cfg.MaxOpenPositions {
			if err := s.recordPreTradeReject(ctx, sig.InstrumentUID, signal.ReasonMaxPositions, `{"reason":"max_positions_reached"}`); err != nil {
				return err
			}
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
		if err := s.checkSpreadBeforeOrder(ctx, instrument, book); err != nil {
			if insertErr := s.recordPreTradeReject(ctx, sig.InstrumentUID, err.Error(), `{"reason":"spread_limit"}`); insertErr != nil {
				return insertErr
			}
			continue
		}
		tradingStatus, err := s.svc.Gateway.GetTradingStatus(ctx, sig.InstrumentUID)
		if err != nil {
			tradingStatus = domain.TradingStatusUnknown
		}
		if err := s.checkEntryInstrumentBeforeOrder(instrument, tradingStatus); err != nil {
			if insertErr := s.recordPreTradeReject(ctx, sig.InstrumentUID, err.Error(), `{"reason":"instrument_pre_trade"}`); insertErr != nil {
				return insertErr
			}
			continue
		}
		portfolio, err = s.svc.Gateway.GetPortfolio(ctx, s.svc.AccountID)
		if err != nil {
			return err
		}
		feature, err := s.svc.Repo.GetFeature(ctx, sig.InstrumentUID, tradeDate)
		if err != nil {
			return err
		}
		sized, err := s.sizeSignal(portfolio, instrument, feature, book, remainingSelections, baseExposure.Add(pendingExposure), reservedCash)
		if err != nil {
			return err
		}
		lots := min(sig.TargetLots, sized.Lots)
		if lots <= 0 {
			reason := sized.Reason
			if reason == "" {
				reason = risk.ErrNoSizingCapacity.Error()
			}
			if err := s.recordPreTradeReject(ctx, sig.InstrumentUID, reason, `{"reason":"sizing"}`); err != nil {
				return err
			}
			continue
		}
		pre, err := s.preTradeCheck(ctx, now, sig.InstrumentUID, portfolio, projectedOpenPositions, false, tradingStatus, quoteTimestamp(book))
		if err != nil {
			return err
		}
		if !pre.Allowed {
			if err := s.recordPreTradeReject(ctx, sig.InstrumentUID, pre.Reason, "{}"); err != nil {
				return err
			}
			continue
		}
		placed, err := s.svc.Execution.PlaceEntry(ctx, s.svc.AccountIDHash, instrument, tradeDate, lots, book, s.cfg.PassiveImproveTicks, 1)
		if err != nil && !errors.Is(err, execution.ErrBrokerOrdersDisabled) {
			return err
		}
		if errors.Is(err, execution.ErrBrokerOrdersDisabled) {
			continue
		}
		_ = s.svc.Notifier.Info(ctx, fmt.Sprintf("entry order %s %s lots=%d status=%s", instrument.Ticker, placed.Side, placed.QuantityLots, placed.Status))
		if placed.FilledLots > 0 {
			if err := s.recordEntryFill(ctx, instrument, placed); err != nil {
				return err
			}
		}
		existing = append(existing, placed)
		notional := orderNotional(placed, instrument)
		pendingExposure = pendingExposure.Add(notional)
		reservedCash = reservedCash.Add(notional)
		projectedOpenPositions++
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
	tradeDate := tradingDate(now)
	for _, order := range orders {
		if order.Side != domain.SideBuy || order.BrokerOrderID == "" || !sameTradingDate(order.TradeDate, tradeDate) {
			continue
		}
		instrument, ok := instrumentByUID[order.InstrumentUID]
		if !ok {
			return fmt.Errorf("instrument %s is not in registry", order.InstrumentUID)
		}
		monitored, err := s.svc.Execution.MonitorOnce(ctx, order, execution.MonitorConfig{
			Deadline:     deadline,
			PollInterval: s.cfg.OrderPollInterval,
			MaxAttempts:  s.cfg.MaxEntryOrderAttempts,
			RepostAfter:  repostAfter(now, deadline, s.cfg.MaxEntryOrderAttempts, s.cfg.OrderPollInterval),
			Instrument:   instrument,
			ImproveTicks: s.cfg.PassiveImproveTicks,
			Quote: func(ctx context.Context, instrumentUID string) (domain.OrderBook, error) {
				return s.svc.MarketData.LatestQuote(ctx, instrumentUID, s.cfg.QuoteDepth, s.cfg.MaxQuoteAge)
			},
			RepostCheck: func(ctx context.Context, order domain.Order, instrument domain.Instrument, book domain.OrderBook) error {
				return s.repostPreTradeCheck(ctx, s.nowUTC().In(s.cfg.Location), order, instrument, book)
			},
		})
		if err != nil {
			return err
		}
		previousFill := execution.AggregatedOrderFill(order)
		if monitored.FilledLots > previousFill.FilledLots || monitored.Commission.GreaterThan(previousFill.Commission) {
			fill := entryFillDelta(order, monitored)
			if fill.FilledLots <= 0 && fill.Commission.IsZero() {
				continue
			}
			if err := s.recordEntryFill(ctx, instrument, fill); err != nil {
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
	if err := s.promoteEntryFilledPositions(ctx); err != nil {
		return err
	}
	return s.periodicReconcile(ctx)
}

func (s Scheduler) promoteEntryFilledPositions(ctx context.Context) error {
	positionsList, err := s.svc.Repo.ListOpenPositions(ctx, s.svc.AccountIDHash)
	if err != nil {
		return err
	}
	now := s.nowUTC()
	for _, pos := range positionsList {
		if pos.Status != domain.PositionEntryFilled {
			continue
		}
		pos.Status = domain.PositionHoldingOvernight
		pos.UpdatedAt = now
		if err := s.svc.Repo.UpsertPosition(ctx, pos); err != nil {
			return err
		}
	}
	return nil
}

func (s *Scheduler) placeExitOrders(ctx context.Context, now time.Time) error {
	if err := s.transitionTo(ctx, domain.StatePlaceExitOrders); err != nil {
		return err
	}
	exitTradeDate := tradingDate(now)
	positionsList, err := s.svc.Repo.ListOpenPositions(ctx, s.svc.AccountIDHash)
	if err != nil {
		return err
	}
	existing, err := s.svc.Repo.ListOrders(ctx, s.svc.AccountIDHash, exitTradeDate.AddDate(0, 0, -1), exitTradeDate)
	if err != nil {
		return err
	}
	instrumentByUID, err := s.instrumentMap(ctx)
	if err != nil {
		return err
	}
	for _, pos := range positionsList {
		if pos.Lots <= 0 || hasActiveBrokerOrder(existing, pos.InstrumentUID, domain.SideSell) {
			continue
		}
		instrument, ok := instrumentByUID[pos.InstrumentUID]
		if !ok {
			return fmt.Errorf("instrument %s is not in registry", pos.InstrumentUID)
		}
		if _, err := s.svc.FreeOrders.Check(ctx, exitTradeDate, instrument, s.orderBudgetNeededForAttempts(s.cfg.MaxExitOrderAttempts)); err != nil {
			if insertErr := s.recordPreTradeReject(ctx, pos.InstrumentUID, err.Error(), `{"reason":"free_order_budget_insufficient"}`); insertErr != nil {
				return insertErr
			}
			continue
		}
		book, err := s.svc.MarketData.LatestQuote(ctx, pos.InstrumentUID, s.cfg.QuoteDepth, s.cfg.MaxQuoteAge)
		if err != nil {
			return err
		}
		if err := s.checkSpreadBeforeOrder(ctx, instrument, book); err != nil {
			if insertErr := s.recordPreTradeReject(ctx, pos.InstrumentUID, err.Error(), `{"reason":"spread_limit"}`); insertErr != nil {
				return insertErr
			}
			continue
		}
		tradingStatus, err := s.svc.Gateway.GetTradingStatus(ctx, pos.InstrumentUID)
		if err != nil {
			tradingStatus = domain.TradingStatusUnknown
		}
		portfolio, err := s.svc.Gateway.GetPortfolio(ctx, s.svc.AccountID)
		if err != nil {
			return err
		}
		pre, err := s.preTradeCheck(ctx, now, pos.InstrumentUID, portfolio, len(positionsList), true, tradingStatus, quoteTimestamp(book))
		if err != nil {
			return err
		}
		if !pre.Allowed {
			return fmt.Errorf("exit pre-trade rejected: %s", pre.Reason)
		}
		placed, err := s.svc.Execution.PlaceExit(ctx, s.svc.AccountIDHash, instrument, exitTradeDate, pos.Lots, book, s.cfg.PassiveImproveTicks, 1)
		if err != nil && !errors.Is(err, execution.ErrBrokerOrdersDisabled) {
			return err
		}
		if errors.Is(err, execution.ErrBrokerOrdersDisabled) {
			continue
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
	exitTradeDate := tradingDate(now)
	activeExitOrders := make([]domain.Order, 0, len(orders))
	for _, order := range orders {
		if order.Side != domain.SideSell || order.BrokerOrderID == "" || !sameTradingDate(order.TradeDate, exitTradeDate) {
			continue
		}
		instrument, ok := instrumentByUID[order.InstrumentUID]
		if !ok {
			return fmt.Errorf("instrument %s is not in registry", order.InstrumentUID)
		}
		monitored, err := s.svc.Execution.MonitorOnce(ctx, order, execution.MonitorConfig{
			Deadline:     deadline,
			PollInterval: s.cfg.OrderPollInterval,
			MaxAttempts:  s.cfg.MaxExitOrderAttempts,
			RepostAfter:  repostAfter(now, deadline, s.cfg.MaxExitOrderAttempts, s.cfg.OrderPollInterval),
			Instrument:   instrument,
			ImproveTicks: s.cfg.PassiveImproveTicks,
			Quote: func(ctx context.Context, instrumentUID string) (domain.OrderBook, error) {
				return s.svc.MarketData.LatestQuote(ctx, instrumentUID, s.cfg.QuoteDepth, s.cfg.MaxQuoteAge)
			},
			RepostCheck: func(ctx context.Context, order domain.Order, instrument domain.Instrument, book domain.OrderBook) error {
				return s.repostPreTradeCheck(ctx, s.nowUTC().In(s.cfg.Location), order, instrument, book)
			},
		})
		if err != nil {
			return err
		}
		if isActiveOrder(monitored.Status) && monitored.BrokerOrderID != "" {
			activeExitOrders = append(activeExitOrders, monitored)
		}
		previousFill := execution.AggregatedOrderFill(order)
		if monitored.FilledLots > previousFill.FilledLots || monitored.Commission.GreaterThan(previousFill.Commission) {
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
	if sinceMidnight(now.In(s.cfg.Location)) >= s.cfg.HardExitDeadline.Duration {
		return s.failOpenPositionsAtHardDeadline(ctx)
	}
	openPositions, err = s.svc.Repo.ListOpenPositions(ctx, s.svc.AccountIDHash)
	if err != nil {
		return err
	}
	if hasOpenPositionWithoutActiveExitOrder(openPositions, activeExitOrders, exitTradeDate) {
		return s.placeExitOrders(ctx, now)
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
	if s.cfg.Mode.AllowsBrokerOrders() {
		if err := s.reconcileCritical(ctx, "reconciliation_critical"); err != nil {
			return err
		}
	}
	return s.sendDailyReport(ctx, now, "ok")
}

func (s *Scheduler) sendMissedDailyReport(ctx context.Context, now time.Time) (bool, error) {
	if s.svc.Repo == nil || !s.hasStateMachine() {
		return false, nil
	}
	tod := sinceMidnight(now)
	if tod < s.cfg.EntrySignalTime.Duration {
		return false, nil
	}
	phase := s.phase(now)
	if phase == domain.StateReconcile || phase == domain.StateReport {
		return false, nil
	}
	state, halted, _, err := s.svc.Repo.GetSystemState(ctx)
	if err != nil {
		return false, err
	}
	if halted || state == domain.StateHalted {
		return false, nil
	}
	if state != domain.StateInit && state != domain.StateSleep {
		return false, nil
	}
	tradeDate := tradingDate(now)
	sent, err := s.svc.Repo.WasDailyReportSent(ctx, tradeDate, s.svc.AccountIDHash)
	if err != nil {
		return false, err
	}
	if sent {
		return false, nil
	}
	return true, s.reconcileAndReport(ctx, now)
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
		Date:           tradeDate,
		Mode:           s.cfg.Mode,
		Signals:        signals,
		Positions:      positionsList,
		Orders:         orders,
		AverageSlipBps: report.AverageAdverseSlippageBps(orders, 0),
		RiskStatus:     riskStatus,
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
	window := s.sizeReductionWindowTrades()
	trigger := s.sizeReductionTriggerBps()
	averageError, count, ok, err := s.averageExpectedErrorBps(ctx, tradeDate, window)
	if err != nil {
		return err
	}
	if !ok || count < window || averageError.GreaterThanOrEqual(trigger) {
		s.svc.Sizer = s.svc.Sizer.WithSizeFactor(decimal.NewFromInt(1))
		return nil
	}
	factor := s.sizeReductionFactor()
	s.svc.Sizer = s.svc.Sizer.WithSizeFactor(factor)
	if emitEvent {
		if err := s.svc.Repo.InsertRiskEvent(ctx, domain.RiskEvent{
			Severity:    domain.SeverityWarn,
			EventType:   "size_reduction_rule_triggered",
			Message:     fmt.Sprintf("average expected_error_bps over %d trades is %s; sizing factor set to %s", count, averageError.StringFixed(2), factor.String()),
			ContextJSON: fmt.Sprintf(`{"average_expected_error_bps":%q,"trades":%d,"size_factor":%q}`, averageError.String(), count, factor.String()),
		}); err != nil {
			return err
		}
	}
	return s.handleLiveReadonlyAfterSizeReduction(ctx, tradeDate, averageError, count, factor, emitEvent)
}

func (s Scheduler) averageExpectedErrorBps(ctx context.Context, tradeDate time.Time, limit int) (decimal.Decimal, int, bool, error) {
	return s.averageExpectedErrorBpsWindow(ctx, tradeDate, 0, limit)
}

func (s Scheduler) averageExpectedErrorBpsWindow(ctx context.Context, tradeDate time.Time, offset, limit int) (decimal.Decimal, int, bool, error) {
	if limit <= 0 {
		return decimal.Zero, 0, false, nil
	}
	if offset < 0 {
		offset = 0
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
			if offset > 0 {
				offset--
				break
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
		return s.recordInfrastructureFailure(ctx, fmt.Errorf("server_time_unavailable: %w", err))
	}
	drift := timeutil.Drift(s.nowUTC(), serverTime)
	if drift > s.cfg.MaxClockDrift {
		reason := fmt.Sprintf("server_clock_drift_too_high: %s > %s", drift, s.cfg.MaxClockDrift)
		if err := s.halt(ctx, "server_clock_drift_too_high", reason, ""); err != nil {
			return err
		}
		return fmt.Errorf("%w: %s", statemachine.ErrSystemHalted, reason)
	}
	s.infraFailedSince = time.Time{}
	return nil
}

func (s *Scheduler) recordInfrastructureFailure(ctx context.Context, err error) error {
	now := s.nowUTC()
	if s.infraFailedSince.IsZero() {
		s.infraFailedSince = now
		s.logWarn("infrastructure check failed; pausing scheduler step", "err", err, "threshold", s.cfg.APIOutageHalt)
		if s.svc.Repo != nil {
			if insertErr := s.svc.Repo.InsertRiskEvent(ctx, domain.RiskEvent{
				TS:          now,
				Severity:    domain.SeverityWarn,
				EventType:   "infrastructure_outage_started",
				Message:     err.Error(),
				ContextJSON: fmt.Sprintf(`{"threshold_sec":%d}`, int(s.cfg.APIOutageHalt.Seconds())),
			}); insertErr != nil {
				return insertErr
			}
		}
		return nil
	}
	elapsed := now.Sub(s.infraFailedSince)
	if s.cfg.APIOutageHalt > 0 && elapsed >= s.cfg.APIOutageHalt {
		s.logWarn("infrastructure check still failing after outage threshold; continuing soft pause", "err", err, "elapsed", elapsed, "threshold", s.cfg.APIOutageHalt)
		return nil
	}
	s.logWarn("infrastructure check still failing; pausing scheduler step", "err", err, "elapsed", elapsed, "threshold", s.cfg.APIOutageHalt)
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
		case domain.PositionEntryFilled, domain.PositionHoldingOvernight, domain.PositionExitPartiallyFilled, domain.PositionExitOrderSent:
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

func (s Scheduler) checkSpreadBeforeOrder(_ context.Context, instrument domain.Instrument, book domain.OrderBook) error {
	spread, err := spreadFromBook(book, instrument.MinPriceIncrement)
	if err != nil {
		return err
	}
	limit := s.svc.Signals.SpreadLimit(instrument)
	if limit.IsPositive() && spread.SpreadBps.GreaterThan(limit) {
		return fmt.Errorf("%s: spread_bps=%s max_spread_bps=%s", signal.ReasonSpread, spread.SpreadBps.String(), limit.String())
	}
	return nil
}

func (s Scheduler) repostPreTradeCheck(ctx context.Context, now time.Time, order domain.Order, instrument domain.Instrument, book domain.OrderBook) error {
	if err := s.checkSpreadBeforeOrder(ctx, instrument, book); err != nil {
		_ = s.recordPreTradeReject(ctx, order.InstrumentUID, err.Error(), `{"reason":"spread_limit","stage":"repost"}`)
		return err
	}
	tradingStatus, err := s.svc.Gateway.GetTradingStatus(ctx, order.InstrumentUID)
	if err != nil {
		tradingStatus = domain.TradingStatusUnknown
	}
	if order.Side == domain.SideBuy {
		if err := s.checkEntryInstrumentBeforeOrder(instrument, tradingStatus); err != nil {
			_ = s.recordPreTradeReject(ctx, order.InstrumentUID, err.Error(), `{"reason":"instrument_pre_trade","stage":"repost"}`)
			return err
		}
	}
	portfolio, err := s.svc.Gateway.GetPortfolio(ctx, s.svc.AccountID)
	if err != nil {
		return err
	}
	openPositions, err := s.svc.Repo.ListOpenPositions(ctx, s.svc.AccountIDHash)
	if err != nil {
		return err
	}
	pre, err := s.preTradeCheck(ctx, now, order.InstrumentUID, portfolio, len(openPositions), order.Side == domain.SideSell, tradingStatus, quoteTimestamp(book))
	if err != nil {
		return err
	}
	if !pre.Allowed {
		_ = s.recordPreTradeReject(ctx, order.InstrumentUID, pre.Reason, `{"stage":"repost"}`)
		return errors.New(pre.Reason)
	}
	return nil
}

func (s Scheduler) checkEntryInstrumentBeforeOrder(instrument domain.Instrument, tradingStatus domain.TradingStatus) error {
	if err := instruments.CheckInstrument(instrument, tradingStatus); err != nil {
		return err
	}
	if s.cfg.RequireZeroCommission && instrument.ExpectedCommissionBpsPerSide.IsPositive() {
		return errors.New(signal.ReasonCommission)
	}
	return nil
}

func (s Scheduler) preTradeCheck(ctx context.Context, now time.Time, instrumentUID string, portfolio domain.Portfolio, openPositions int, closingPosition bool, tradingStatus domain.TradingStatus, quoteReceivedAt time.Time) (risk.PreTradeResult, error) {
	serverClockDrift, serverTimeUnavailable, err := s.preTradeClockDrift(ctx, now)
	if err != nil {
		return risk.PreTradeResult{}, err
	}
	metrics, err := s.riskMetrics(ctx, now, portfolio)
	if err != nil {
		if haltErr := s.halt(ctx, "database_unavailable", fmt.Sprintf("pre-trade risk metrics unavailable: %s", err), instrumentUID); haltErr != nil {
			return risk.PreTradeResult{}, fmt.Errorf("database_unavailable: %w; halt failed: %v", err, haltErr)
		}
		return risk.PreTradeResult{Allowed: false, Reason: "database_unavailable"}, fmt.Errorf("%w: database_unavailable", statemachine.ErrSystemHalted)
	}
	unknownOrder, unknownHolding, err := s.unknownBrokerState(ctx, portfolio)
	if err != nil {
		return risk.PreTradeResult{}, err
	}
	result := s.svc.Risk.PreTradeCheck(risk.PreTradeInput{
		Portfolio:             portfolio,
		OpenPositions:         openPositions,
		ClosingPosition:       closingPosition,
		DailyPnL:              metrics.dailyPnL,
		WeeklyPnL:             metrics.weeklyPnL,
		MonthlyDrawdownPct:    metrics.monthlyDrawdownPct,
		AvgSlippageBps10:      metrics.avgSlippageBps10,
		TradingStatus:         tradingStatus,
		QuoteReceivedAt:       quoteReceivedAt,
		Now:                   now.UTC(),
		MarketClose:           s.preTradeDeadlineOn(now, closingPosition),
		ServerTimeUnavailable: serverTimeUnavailable,
		ServerClockDrift:      serverClockDrift,
		MaxClockDrift:         s.cfg.MaxClockDrift,
		UnknownBrokerOrder:    unknownOrder,
		UnknownBrokerHolding:  unknownHolding,
	})
	if !result.Allowed && isHardHaltPreTradeReason(result.Reason) {
		if err := s.halt(ctx, result.Reason, fmt.Sprintf("pre-trade hard limit breached: %s", result.Reason), instrumentUID); err != nil {
			return result, err
		}
		return result, fmt.Errorf("%w: %s", statemachine.ErrSystemHalted, result.Reason)
	}
	return result, nil
}

func (s Scheduler) preTradeClockDrift(ctx context.Context, now time.Time) (time.Duration, bool, error) {
	if s.cfg.MaxClockDrift <= 0 || s.svc.Gateway == nil {
		return 0, false, nil
	}
	serverTime, err := s.svc.Gateway.GetServerTime(ctx)
	if err != nil {
		if s.cfg.Mode == domain.ModePaper {
			return 0, false, nil
		}
		return 0, true, nil
	}
	return timeutil.Drift(now.UTC(), serverTime), false, nil
}

func (s Scheduler) unknownBrokerState(ctx context.Context, portfolio domain.Portfolio) (bool, bool, error) {
	if !s.cfg.Mode.AllowsBrokerOrders() {
		return false, false, nil
	}
	localOrders, err := s.svc.Repo.ListActiveOrders(ctx, s.svc.AccountIDHash)
	if err != nil {
		return false, false, err
	}
	localByBroker := make(map[string]struct{}, len(localOrders))
	for _, order := range localOrders {
		if order.BrokerOrderID != "" {
			localByBroker[order.BrokerOrderID] = struct{}{}
		}
	}
	brokerOrders, err := s.svc.Gateway.GetActiveOrders(ctx, s.svc.AccountID)
	if err != nil {
		return false, false, err
	}
	for _, brokerOrder := range brokerOrders {
		if brokerOrder.BrokerOrderID == "" {
			continue
		}
		if _, ok := localByBroker[brokerOrder.BrokerOrderID]; !ok {
			return true, false, nil
		}
	}
	localPositions, err := s.svc.Repo.ListOpenPositions(ctx, s.svc.AccountIDHash)
	if err != nil {
		return false, false, err
	}
	localLots := make(map[string]int64, len(localPositions))
	for _, pos := range localPositions {
		localLots[pos.InstrumentUID] += pos.Lots
	}
	for _, holding := range portfolio.Holdings {
		if holding.QuantityLots > 0 && localLots[holding.InstrumentUID] == 0 {
			return false, true, nil
		}
	}
	return false, false, nil
}

func isHardHaltPreTradeReason(reason string) bool {
	switch reason {
	case "database_unavailable",
		"server_clock_drift_too_high",
		"unknown_broker_order",
		"unknown_broker_position",
		"trading_status_unknown_before_order",
		"max_daily_loss",
		"max_weekly_loss",
		"max_monthly_drawdown":
		return true
	default:
		return false
	}
}

type preTradeMetrics struct {
	dailyPnL           decimal.Decimal
	weeklyPnL          decimal.Decimal
	monthlyDrawdownPct decimal.Decimal
	avgSlippageBps10   decimal.Decimal
}

func (s Scheduler) riskMetrics(ctx context.Context, now time.Time, portfolio domain.Portfolio) (preTradeMetrics, error) {
	today := tradingDate(now)
	monthStart := today.AddDate(0, -1, 0)
	positionsList, err := s.svc.Repo.ListPositions(ctx, s.svc.AccountIDHash, monthStart.AddDate(0, 0, -7), today)
	if err != nil {
		return preTradeMetrics{}, err
	}
	weekStart := today.AddDate(0, 0, -6)
	var metrics preTradeMetrics
	monthlyPnL := decimal.Zero
	for _, pos := range positionsList {
		if pos.Status != domain.PositionExitFilled {
			continue
		}
		closedAt := positionCloseTime(pos)
		if closedAt.IsZero() {
			continue
		}
		closeDate := tradingDate(closedAt)
		if closeDate.Equal(today) {
			metrics.dailyPnL = metrics.dailyPnL.Add(pos.NetPnL)
		}
		if !closeDate.Before(weekStart) {
			metrics.weeklyPnL = metrics.weeklyPnL.Add(pos.NetPnL)
		}
		if !closeDate.Before(monthStart) {
			monthlyPnL = monthlyPnL.Add(pos.NetPnL)
		}
	}
	if monthlyPnL.IsNegative() && portfolio.Equity.IsPositive() {
		metrics.monthlyDrawdownPct = monthlyPnL.Neg().Div(portfolio.Equity)
	}
	orders, err := s.svc.Repo.ListOrders(ctx, s.svc.AccountIDHash, monthStart.AddDate(0, 0, -7), today)
	if err != nil {
		return preTradeMetrics{}, err
	}
	metrics.avgSlippageBps10 = report.AverageAdverseSlippageBps(orders, 10)
	return metrics, nil
}

func positionCloseTime(pos domain.Position) time.Time {
	if pos.ClosedAt != nil {
		return pos.ClosedAt.UTC()
	}
	return pos.UpdatedAt.UTC()
}

func (s Scheduler) marketCloseOn(now time.Time) time.Time {
	if s.cfg.MarketClose.Duration <= 0 {
		return time.Time{}
	}
	return s.cfg.MarketClose.On(now, s.cfg.Location).UTC()
}

func (s Scheduler) preTradeDeadlineOn(now time.Time, closingPosition bool) time.Time {
	if closingPosition && s.cfg.HardExitDeadline.Duration > 0 {
		return s.cfg.HardExitDeadline.On(now, s.cfg.Location).UTC()
	}
	if !closingPosition && s.cfg.NoNewEntryAfter.Duration > 0 {
		return s.cfg.NoNewEntryAfter.On(now, s.cfg.Location).UTC()
	}
	return s.marketCloseOn(now)
}

func (s *Scheduler) handleLiveReadonlyAfterSizeReduction(ctx context.Context, tradeDate time.Time, averageError decimal.Decimal, count int, factor decimal.Decimal, emitRecommendation bool) error {
	if s.cfg.Mode != domain.ModeLiveTrade {
		return nil
	}
	window := s.sizeReductionWindowTrades()
	trigger := s.sizeReductionTriggerBps()
	previousAverage, previousCount, previousOK, err := s.averageExpectedErrorBpsWindow(ctx, tradeDate, window, window)
	if err != nil {
		return err
	}
	if previousOK && previousCount == window && previousAverage.LessThan(trigger) {
		return s.activateLiveReadonly(ctx, averageError, count, previousAverage, previousCount, factor)
	}
	if !emitRecommendation {
		return nil
	}
	return s.recommendLiveReadonlyAfterSizeReduction(ctx, averageError, count, factor)
}

func (s *Scheduler) activateLiveReadonly(ctx context.Context, averageError decimal.Decimal, count int, previousAverage decimal.Decimal, previousCount int, factor decimal.Decimal) error {
	if s.cfg.Mode != domain.ModeLiveTrade {
		return nil
	}
	if s.svc.Repo == nil {
		return nil
	}
	state, halted, reason, err := s.svc.Repo.GetSystemState(ctx)
	if err != nil {
		return err
	}
	if halted || state == domain.StateHalted {
		return nil
	}
	message := fmt.Sprintf(
		"average expected_error_bps stayed below %s for two consecutive %d-trade windows; switching to live_readonly",
		s.sizeReductionTriggerBps().String(),
		s.sizeReductionWindowTrades(),
	)
	s.cfg.Mode = domain.ModeLiveReadonly
	if s.svc.Execution != nil {
		s.svc.Execution.SetMode(domain.ModeLiveReadonly)
	}
	s.sm = statemachine.New(s.svc.Repo, domain.ModeLiveReadonly)
	contextJSON := fmt.Sprintf(
		`{"average_expected_error_bps":%q,"trades":%d,"previous_average_expected_error_bps":%q,"previous_trades":%d,"size_factor":%q,"mode":%q}`,
		averageError.String(),
		count,
		previousAverage.String(),
		previousCount,
		factor.String(),
		domain.ModeLiveReadonly,
	)
	if err := s.svc.Repo.SaveSystemState(ctx, state, domain.ModeLiveReadonly, false, reason, contextJSON); err != nil {
		return err
	}
	if s.svc.Notifier != nil {
		_ = s.svc.Notifier.Alert(ctx, message)
	}
	return s.svc.Repo.InsertRiskEvent(ctx, domain.RiskEvent{
		Severity:    domain.SeverityAlert,
		EventType:   "live_readonly_activated",
		Message:     message,
		ContextJSON: contextJSON,
	})
}

func (s Scheduler) recommendLiveReadonlyAfterSizeReduction(ctx context.Context, averageError decimal.Decimal, count int, factor decimal.Decimal) error {
	if s.cfg.Mode != domain.ModeLiveTrade {
		return nil
	}
	message := fmt.Sprintf("size reduction remains active after %d trades; consider switching to live_readonly until expected error recovers", count)
	if s.svc.Notifier != nil {
		_ = s.svc.Notifier.Alert(ctx, message)
	}
	return s.svc.Repo.InsertRiskEvent(ctx, domain.RiskEvent{
		Severity:  domain.SeverityAlert,
		EventType: "live_readonly_recommended",
		Message:   message,
		ContextJSON: fmt.Sprintf(
			`{"average_expected_error_bps":%q,"trades":%d,"size_factor":%q,"recommended_mode":%q}`,
			averageError.String(),
			count,
			factor.String(),
			domain.ModeLiveReadonly,
		),
	})
}

func (s Scheduler) recordPreTradeReject(ctx context.Context, instrumentUID, message, contextJSON string) error {
	return s.svc.Repo.InsertRiskEvent(ctx, domain.RiskEvent{
		Severity:      domain.SeverityWarn,
		EventType:     "pre_trade_reject",
		InstrumentUID: instrumentUID,
		Message:       message,
		ContextJSON:   contextJSON,
	})
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
	needed := s.orderBudgetNeededForAttempts(s.cfg.MaxEntryOrderAttempts) + s.orderBudgetNeededForAttempts(s.cfg.MaxExitOrderAttempts)
	if needed <= 0 {
		return 1
	}
	return needed
}

func (s Scheduler) sizeReductionWindowTrades() int {
	if s.cfg.SizeReductionWindowTrades <= 0 {
		return 20
	}
	return s.cfg.SizeReductionWindowTrades
}

func (s Scheduler) sizeReductionFactor() decimal.Decimal {
	if !s.cfg.SizeReductionFactor.IsPositive() {
		return decimal.RequireFromString("0.5")
	}
	return s.cfg.SizeReductionFactor
}

func (s Scheduler) sizeReductionTriggerBps() decimal.Decimal {
	if s.cfg.SizeReductionTriggerBps.IsZero() {
		return decimal.NewFromInt(-10)
	}
	return s.cfg.SizeReductionTriggerBps
}

func (s Scheduler) orderBudgetNeededForAttempts(attempts int) int {
	if attempts <= 0 {
		attempts = 1
	}
	needed := attempts
	if s.cfg.FreeOrderCountPolicy == execution.FreeOrderPolicyCancelCounts {
		needed += attempts - 1
	}
	return needed
}

func isSizingSkipReason(reason string) bool {
	return reason == "lots_below_one" || reason == "min_order_notional"
}

func signalPhaseAlreadyPrepared(state domain.SystemState) bool {
	switch state {
	case domain.StateWaitEntryWindow,
		domain.StatePlaceEntryOrders,
		domain.StateMonitorEntryOrders,
		domain.StateHoldOvernight:
		return true
	default:
		return false
	}
}

func quoteTimestamp(book domain.OrderBook) time.Time {
	if !book.Time.IsZero() {
		return book.Time.UTC()
	}
	return book.ReceivedAt.UTC()
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
	if s.svc.Notifier != nil {
		_ = s.svc.Notifier.Alert(ctx, fmt.Sprintf("%s: %s", eventType, reason))
	}
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

func entryFillDelta(previous, current domain.Order) domain.Order {
	previous = execution.AggregatedOrderFill(previous)
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
	fill.QuantityLots = current.QuantityLots - previous.FilledLots
	if fill.QuantityLots < 0 {
		fill.QuantityLots = 0
	}
	return fill
}

func exitFillDelta(previous, current domain.Order) domain.Order {
	previous = execution.AggregatedOrderFill(previous)
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

func hasActiveBrokerOrder(orders []domain.Order, instrumentUID string, side domain.Side) bool {
	for _, order := range orders {
		if order.InstrumentUID == instrumentUID && order.Side == side && order.BrokerOrderID != "" && isActiveOrder(order.Status) {
			return true
		}
	}
	return false
}

func hasOpenPositionWithoutActiveExitOrder(positions []domain.Position, orders []domain.Order, tradeDate time.Time) bool {
	for _, pos := range positions {
		if pos.Lots <= 0 {
			continue
		}
		hasActiveSell := false
		for _, order := range orders {
			if order.InstrumentUID == pos.InstrumentUID &&
				order.Side == domain.SideSell &&
				order.BrokerOrderID != "" &&
				sameTradingDate(order.TradeDate, tradeDate) &&
				isActiveOrder(order.Status) {
				hasActiveSell = true
				break
			}
		}
		if !hasActiveSell {
			return true
		}
	}
	return false
}

func sortSignalsForEntry(signals []domain.Signal) {
	sort.SliceStable(signals, func(i, j int) bool {
		if signals[i].Decision != signals[j].Decision {
			return signals[i].Decision == domain.DecisionEnter
		}
		if signals[i].Score.Equal(signals[j].Score) {
			return signals[i].InstrumentUID < signals[j].InstrumentUID
		}
		return signals[i].Score.GreaterThan(signals[j].Score)
	})
}

func entryOrderCandidates(signals []domain.Signal, existing []domain.Order) []string {
	out := make([]string, 0, len(signals))
	for _, sig := range signals {
		if sig.Decision == domain.DecisionEnter && sig.TargetLots > 0 && !hasOrder(existing, sig.InstrumentUID, domain.SideBuy) {
			out = append(out, sig.InstrumentUID)
		}
	}
	return out
}

func remainingSignalCount(candidates []string, instrumentUID string) int {
	for i, candidate := range candidates {
		if candidate == instrumentUID {
			return len(candidates) - i
		}
	}
	return 1
}

func countActiveOrders(orders []domain.Order, side domain.Side, tradeDate time.Time) int {
	count := 0
	for _, order := range orders {
		if order.Side == side && sameTradingDate(order.TradeDate, tradeDate) && isActiveOrder(order.Status) {
			count++
		}
	}
	return count
}

func ordersExposure(orders []domain.Order, instruments map[string]domain.Instrument, side domain.Side, activeOnly bool) decimal.Decimal {
	total := decimal.Zero
	for _, order := range orders {
		if order.Side != side {
			continue
		}
		if activeOnly && !isActiveOrder(order.Status) {
			continue
		}
		instrument := instruments[order.InstrumentUID]
		total = total.Add(orderRemainingNotional(order, instrument))
	}
	return total
}

func positionsExposure(positions []domain.Position, instruments map[string]domain.Instrument, portfolio domain.Portfolio) decimal.Decimal {
	local := decimal.Zero
	for _, pos := range positions {
		instrument := instruments[pos.InstrumentUID]
		lot := pos.Lot
		if lot <= 0 {
			lot = instrument.Lot
		}
		if lot <= 0 || !pos.AvgBuyPrice.IsPositive() || pos.Lots <= 0 {
			continue
		}
		local = local.Add(pos.AvgBuyPrice.Mul(decimal.NewFromInt(pos.Lots)).Mul(decimal.NewFromInt(lot)))
	}
	return money.Max(local, portfolioExposure(portfolio))
}

func portfolioExposure(portfolio domain.Portfolio) decimal.Decimal {
	total := decimal.Zero
	for _, holding := range portfolio.Holdings {
		if holding.MarketValue.IsPositive() {
			total = total.Add(holding.MarketValue)
		}
	}
	return total
}

func orderNotional(order domain.Order, instrument domain.Instrument) decimal.Decimal {
	lot := instrument.Lot
	if lot <= 0 {
		lot = 1
	}
	lots := order.QuantityLots
	if lots <= 0 {
		lots = order.FilledLots
	}
	return order.LimitPrice.Mul(decimal.NewFromInt(lots)).Mul(decimal.NewFromInt(lot))
}

func orderRemainingNotional(order domain.Order, instrument domain.Instrument) decimal.Decimal {
	remaining := order.QuantityLots - order.FilledLots
	if remaining <= 0 {
		return decimal.Zero
	}
	lot := instrument.Lot
	if lot <= 0 {
		lot = 1
	}
	return order.LimitPrice.Mul(decimal.NewFromInt(remaining)).Mul(decimal.NewFromInt(lot))
}

func isActiveOrder(status domain.OrderStatus) bool {
	return status == domain.OrderStatusNew || status == domain.OrderStatusSent || status == domain.OrderStatusPartiallyFilled
}

func sameTradingDate(a, b time.Time) bool {
	return tradingDate(a).Equal(tradingDate(b))
}

func sinceMidnight(t time.Time) time.Duration {
	h, m, s := t.Clock()
	return time.Duration(h)*time.Hour + time.Duration(m)*time.Minute + time.Duration(s)*time.Second
}

func tradingDate(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}
