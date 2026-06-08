package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/execution"
	"overnight-trading-bot/internal/marketdata"
	"overnight-trading-bot/internal/position"
	"overnight-trading-bot/internal/reconciliation"
	"overnight-trading-bot/internal/risk"
	signalengine "overnight-trading-bot/internal/signal"
	"overnight-trading-bot/internal/statemachine"
	"overnight-trading-bot/internal/testutil"
	"overnight-trading-bot/internal/timeutil"
	"overnight-trading-bot/internal/tinvest"
)

func TestPhaseUsesMoscowWindows(t *testing.T) {
	loc := time.FixedZone("MSK", 3*60*60)
	s := Scheduler{cfg: Config{
		Location:         loc,
		EntrySignalTime:  mustTOD("18:10:00"),
		EntryWindowStart: mustTOD("18:20:00"),
		NoNewEntryAfter:  mustTOD("18:38:30"),
		ExitWatchStart:   mustTOD("09:50:00"),
		ExitWindowStart:  mustTOD("10:05:00"),
		ExitWindowEnd:    mustTOD("10:25:00"),
		HardExitDeadline: mustTOD("10:45:00"),
	}}
	tests := []struct {
		at   string
		want domain.SystemState
	}{
		{"2026-06-06T09:55:00+03:00", domain.StateWaitExitWindow},
		{"2026-06-06T10:10:00+03:00", domain.StatePlaceExitOrders},
		{"2026-06-06T10:30:00+03:00", domain.StateMonitorExitOrders},
		{"2026-06-06T11:00:00+03:00", domain.StateReconcile},
		{"2026-06-06T18:15:00+03:00", domain.StateGenerateSignals},
		{"2026-06-06T18:25:00+03:00", domain.StatePlaceEntryOrders},
		{"2026-06-06T19:00:00+03:00", domain.StateHoldOvernight},
	}
	for _, tt := range tests {
		t.Run(tt.at, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339, tt.at)
			if err != nil {
				t.Fatal(err)
			}
			if got := s.phase(at.In(loc)); got != tt.want {
				t.Fatalf("phase=%s, want %s", got, tt.want)
			}
		})
	}
}

func TestPhaseHonorsExitNotBeforeWhenWindowStartsEarlier(t *testing.T) {
	loc := time.FixedZone("MSK", 3*60*60)
	s := Scheduler{cfg: Config{
		Location:         loc,
		EntrySignalTime:  mustTOD("18:10:00"),
		ExitWatchStart:   mustTOD("09:50:00"),
		ExitNotBefore:    mustTOD("10:03:00"),
		ExitWindowStart:  mustTOD("10:00:00"),
		ExitWindowEnd:    mustTOD("10:25:00"),
		HardExitDeadline: mustTOD("10:45:00"),
	}}
	at, err := time.Parse(time.RFC3339, "2026-06-06T10:01:00+03:00")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.phase(at.In(loc)); got != domain.StateWaitExitWindow {
		t.Fatalf("phase before ExitNotBefore=%s, want WAIT_EXIT_WINDOW", got)
	}
	at, err = time.Parse(time.RFC3339, "2026-06-06T10:04:00+03:00")
	if err != nil {
		t.Fatal(err)
	}
	if got := s.phase(at.In(loc)); got != domain.StatePlaceExitOrders {
		t.Fatalf("phase after ExitNotBefore=%s, want PLACE_EXIT_ORDERS", got)
	}
}

func TestInfrastructureOutageRequiresThreshold(t *testing.T) {
	gateway := tinvest.NewFakeGateway()
	gateway.ServerTime = time.Now().UTC().Add(-10 * time.Second)
	s := &Scheduler{
		cfg: Config{
			Mode:          domain.ModeSandbox,
			MaxClockDrift: 2 * time.Second,
			APIOutageHalt: 180 * time.Second,
		},
		svc: Services{Gateway: gateway},
	}
	if err := s.checkInfrastructure(context.Background()); err != nil {
		t.Fatalf("first infrastructure failure should be tolerated: %v", err)
	}
	s.infraFailedSince = time.Now().UTC().Add(-181 * time.Second)
	if err := s.checkInfrastructure(context.Background()); err == nil {
		t.Fatalf("expected outage after threshold")
	}
}

func TestReconcileAndReportIsIdempotentPerDate(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	gateway := tinvest.NewFakeGateway()
	notifier := &countNotifier{}
	recon := reconciliation.New(repo, gateway, "account", "hash")
	if err := repo.SaveSystemState(ctx, domain.StateMonitorExitOrders, domain.ModePaper, false, "", "{}"); err != nil {
		t.Fatal(err)
	}
	s := Scheduler{
		cfg: Config{Mode: domain.ModePaper, Location: time.UTC},
		sm:  statemachine.New(repo, domain.ModePaper),
		svc: Services{
			Repo:          repo,
			Gateway:       gateway,
			Reconcile:     recon,
			Notifier:      notifier,
			Risk:          risk.NewManager(repo, risk.ManagerConfig{}),
			AccountID:     "account",
			AccountIDHash: "hash",
		},
	}
	now := time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)
	if err := s.reconcileAndReport(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := s.reconcileAndReport(ctx, now); err != nil {
		t.Fatal(err)
	}
	if notifier.reports != 1 {
		t.Fatalf("reports sent=%d, want 1", notifier.reports)
	}
}

func TestExitFillDeltaUsesOnlyNewlyExecutedLots(t *testing.T) {
	previous := domain.Order{
		FilledLots:   2,
		AvgFillPrice: decimal.NewFromInt(100),
		Commission:   decimal.NewFromFloat(0.50),
	}
	current := domain.Order{
		FilledLots:   4,
		AvgFillPrice: decimal.NewFromInt(110),
		Commission:   decimal.NewFromFloat(1.25),
	}
	fill := exitFillDelta(previous, current)
	if fill.FilledLots != 2 {
		t.Fatalf("delta filled lots=%d, want 2", fill.FilledLots)
	}
	if !fill.AvgFillPrice.Equal(decimal.NewFromInt(120)) {
		t.Fatalf("delta avg fill price=%s, want 120", fill.AvgFillPrice)
	}
	if !fill.Commission.Equal(decimal.NewFromFloat(0.75)) {
		t.Fatalf("delta commission=%s, want 0.75", fill.Commission)
	}
}

func TestHardDeadlineMarksOpenPositionFailedAndHalts(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	openDate := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	if err := repo.UpsertPosition(ctx, domain.Position{
		AccountIDHash: "hash",
		InstrumentUID: "uid",
		OpenTradeDate: openDate,
		Lots:          1,
		Lot:           1,
		Status:        domain.PositionHoldingOvernight,
	}); err != nil {
		t.Fatal(err)
	}
	notifier := &countNotifier{}
	s := Scheduler{
		cfg: Config{Mode: domain.ModePaper, Location: time.UTC},
		svc: Services{
			Repo:          repo,
			Risk:          risk.NewManager(repo, risk.ManagerConfig{}),
			Notifier:      notifier,
			AccountIDHash: "hash",
		},
	}
	if err := s.failOpenPositionsAtHardDeadline(ctx); err != nil {
		t.Fatal(err)
	}
	if !repo.Halted || repo.State != domain.StateHalted {
		t.Fatalf("system not halted: state=%s halted=%v", repo.State, repo.Halted)
	}
	positions, err := repo.ListOpenPositions(ctx, "hash")
	if err != nil {
		t.Fatal(err)
	}
	if len(positions) != 1 || positions[0].Status != domain.PositionExitFailed {
		t.Fatalf("positions=%+v, want EXIT_FAILED", positions)
	}
	if notifier.alerts != 1 {
		t.Fatalf("alerts=%d, want 1", notifier.alerts)
	}
	if notifier.reports != 1 {
		t.Fatalf("reports=%d, want daily report before HALT", notifier.reports)
	}
}

func TestHoldOvernightCancelsActiveBuyOrders(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	tradeDate := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	if err := repo.UpsertOrder(ctx, domain.Order{
		ClientOrderID: "buy",
		AccountIDHash: "hash",
		InstrumentUID: "uid",
		TradeDate:     tradeDate,
		Side:          domain.SideBuy,
		OrderType:     domain.OrderTypeLimit,
		QuantityLots:  1,
		Status:        domain.OrderStatusNew,
	}); err != nil {
		t.Fatal(err)
	}
	s := Scheduler{
		cfg: Config{Mode: domain.ModePaper, Location: time.UTC},
		sm:  statemachine.New(repo, domain.ModePaper),
		svc: Services{
			Repo:          repo,
			Execution:     &execution.Engine{},
			AccountIDHash: "hash",
		},
	}
	if err := repo.SaveSystemState(ctx, domain.StateMonitorEntryOrders, domain.ModePaper, false, "", "{}"); err != nil {
		t.Fatal(err)
	}
	if err := s.holdOvernight(ctx); err != nil {
		t.Fatal(err)
	}
	orders, err := repo.ListOrders(ctx, "hash", tradeDate, tradeDate)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || orders[0].Status != domain.OrderStatusCancelled {
		t.Fatalf("orders=%+v, want CANCELLED", orders)
	}
}

func TestNonZeroCommissionQuarantinesInstrumentAndHalts(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	if err := repo.UpsertInstrument(ctx, domain.Instrument{
		InstrumentUID: "uid",
		Ticker:        "TRUR",
		Enabled:       true,
	}); err != nil {
		t.Fatal(err)
	}
	notifier := &countNotifier{}
	s := Scheduler{
		cfg: Config{
			Mode:                  domain.ModePaper,
			RequireZeroCommission: true,
			QuarantineOnNonZero:   true,
		},
		svc: Services{
			Repo:     repo,
			Risk:     risk.NewManager(repo, risk.ManagerConfig{}),
			Notifier: notifier,
		},
	}
	if err := s.handleCommission(ctx, "uid", decimal.NewFromFloat(0.01)); err != nil {
		t.Fatal(err)
	}
	if !repo.Halted || repo.State != domain.StateHalted {
		t.Fatalf("system not halted: state=%s halted=%v", repo.State, repo.Halted)
	}
	instruments, err := repo.ListInstruments(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(instruments) != 1 || !instruments[0].Quarantine {
		t.Fatalf("instrument not quarantined: %+v", instruments)
	}
	if notifier.alerts != 1 {
		t.Fatalf("alerts=%d, want 1", notifier.alerts)
	}
}

func TestEntryInstrumentPreTradeRejectsQuarantineAndCommission(t *testing.T) {
	s := Scheduler{cfg: Config{RequireZeroCommission: true}}
	err := s.checkEntryInstrumentBeforeOrder(domain.Instrument{
		InstrumentUID:     "uid",
		Ticker:            "TRUR",
		Enabled:           true,
		Quarantine:        true,
		Lot:               1,
		MinPriceIncrement: decimal.NewFromInt(1),
		Currency:          "RUB",
	}, domain.TradingStatusNormal)
	if err == nil {
		t.Fatal("expected quarantine rejection")
	}
	err = s.checkEntryInstrumentBeforeOrder(domain.Instrument{
		InstrumentUID:                "uid",
		Ticker:                       "TRUR",
		Enabled:                      true,
		Lot:                          1,
		MinPriceIncrement:            decimal.NewFromInt(1),
		Currency:                     "RUB",
		ExpectedCommissionBpsPerSide: decimal.NewFromInt(1),
	}, domain.TradingStatusNormal)
	if err == nil || err.Error() != signalengine.ReasonCommission {
		t.Fatalf("err=%v, want commission rejection", err)
	}
}

func TestPreTradeDailyLossBreachHalts(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	now := time.Date(2026, 6, 8, 18, 20, 0, 0, time.UTC)
	closedAt := now.Add(-time.Hour)
	if err := repo.UpsertPosition(ctx, domain.Position{
		AccountIDHash: "hash",
		InstrumentUID: "uid",
		OpenTradeDate: tradingDate(now),
		Status:        domain.PositionExitFilled,
		NetPnL:        decimal.NewFromInt(-200),
		ClosedAt:      &closedAt,
	}); err != nil {
		t.Fatal(err)
	}
	notifier := &countNotifier{}
	s := Scheduler{
		cfg: Config{Mode: domain.ModePaper, Location: time.UTC},
		svc: Services{
			Repo:          repo,
			Risk:          risk.NewManager(repo, risk.ManagerConfig{MaxDailyLossPct: decimal.RequireFromString("0.01")}),
			Notifier:      notifier,
			AccountIDHash: "hash",
		},
	}
	_, err := s.preTradeCheck(ctx, now, "uid", domain.Portfolio{
		Equity: decimal.NewFromInt(10000),
		Cash:   decimal.NewFromInt(10000),
	}, 0, domain.TradingStatusNormal, now)
	if !errors.Is(err, statemachine.ErrSystemHalted) {
		t.Fatalf("err=%v, want ErrSystemHalted", err)
	}
	if !repo.Halted || repo.HaltReason != "pre-trade hard limit breached: max_daily_loss" {
		t.Fatalf("halted=%v reason=%q", repo.Halted, repo.HaltReason)
	}
	if notifier.alerts != 1 {
		t.Fatalf("alerts=%d, want 1", notifier.alerts)
	}
}

func TestStepSendsMissedDailyReportAfterEntrySignalTime(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	notifier := &countNotifier{}
	now := time.Date(2026, 6, 8, 18, 15, 0, 0, time.UTC)
	s := Scheduler{
		clock: fixedClock{now: now},
		cfg: Config{
			Mode:            domain.ModePaper,
			Location:        time.UTC,
			EntrySignalTime: mustTOD("18:10:00"),
		},
		sm: statemachine.New(repo, domain.ModePaper),
		svc: Services{
			Repo:          repo,
			Notifier:      notifier,
			AccountIDHash: "hash",
		},
	}
	if err := s.Step(ctx); err != nil {
		t.Fatal(err)
	}
	if notifier.reports != 1 {
		t.Fatalf("reports=%d, want catch-up report", notifier.reports)
	}
	sent, err := repo.WasDailyReportSent(ctx, now, "hash")
	if err != nil {
		t.Fatal(err)
	}
	if !sent {
		t.Fatal("daily report was not marked as sent")
	}
}

func TestSizeReductionRuleCutsSizerAfterBadExpectedErrors(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	tradeDate := time.Date(2026, 6, 30, 0, 0, 0, 0, time.UTC)
	for i := 0; i < sizeReductionWindowTrades; i++ {
		date := tradeDate.AddDate(0, 0, -i)
		if err := repo.UpsertSignal(ctx, domain.Signal{
			TradeDate:     date,
			InstrumentUID: "uid",
			Decision:      domain.DecisionEnter,
			NetEdgeBps:    decimal.NewFromInt(20),
		}); err != nil {
			t.Fatal(err)
		}
		if err := repo.UpsertPosition(ctx, domain.Position{
			AccountIDHash:   "hash",
			InstrumentUID:   "uid",
			OpenTradeDate:   date,
			Lot:             1,
			Status:          domain.PositionExitFilled,
			RealizedEdgeBps: decimal.Zero,
			UpdatedAt:       date.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
	}
	s := Scheduler{
		svc: Services{
			Repo:          repo,
			AccountIDHash: "hash",
			Sizer: risk.NewSizer(risk.SizingConfig{
				MaxPositionPct:             decimal.NewFromInt(1),
				MaxTotalExposurePct:        decimal.NewFromInt(1),
				MaxParticipationRate:       decimal.NewFromInt(1),
				CashUsageBuffer:            decimal.NewFromInt(1),
				RiskBudgetPerInstrumentPct: decimal.NewFromInt(1),
				MinOrderNotionalRUB:        decimal.NewFromInt(1),
			}),
		},
	}
	if err := s.applySizeReductionRule(ctx, tradeDate, true); err != nil {
		t.Fatal(err)
	}
	sized := s.svc.Sizer.Size(risk.SizingInput{
		Portfolio:           domain.Portfolio{Equity: decimal.NewFromInt(10_000), Cash: decimal.NewFromInt(10_000)},
		SelectedInstruments: 1,
		LimitPrice:          decimal.NewFromInt(100),
		Lot:                 1,
		EntryIntervalVolume: decimal.NewFromInt(10_000),
		ExitIntervalVolume:  decimal.NewFromInt(10_000),
		Q05OvernightAbs:     decimal.NewFromInt(1),
	})
	if sized.Lots != 50 {
		t.Fatalf("lots=%d, want reduced 50", sized.Lots)
	}
	if len(repo.RiskEvents) != 1 || repo.RiskEvents[0].EventType != "size_reduction_rule_triggered" {
		t.Fatalf("risk events=%+v", repo.RiskEvents)
	}
}

func TestBatchSignalLimitsCapSlotsAndExposure(t *testing.T) {
	s := Scheduler{
		cfg: Config{MaxOpenPositions: 5},
		svc: Services{Sizer: risk.NewSizer(risk.SizingConfig{
			MaxPositionPct:             decimal.NewFromInt(1),
			MaxTotalExposurePct:        decimal.RequireFromString("0.50"),
			MaxParticipationRate:       decimal.NewFromInt(1),
			CashUsageBuffer:            decimal.NewFromInt(1),
			RiskBudgetPerInstrumentPct: decimal.NewFromInt(1),
			MinOrderNotionalRUB:        decimal.NewFromInt(1),
		})},
	}
	book := domain.OrderBook{
		Bids: []domain.OrderBookLevel{{Price: decimal.NewFromInt(99), QuantityLots: 10}},
		Asks: []domain.OrderBookLevel{{Price: decimal.NewFromInt(101), QuantityLots: 10}},
	}
	generated := make([]signalCandidate, 0, 9)
	for i := 0; i < 9; i++ {
		uid := string(rune('a' + i))
		generated = append(generated, signalCandidate{
			Signal: domain.Signal{
				InstrumentUID: uid,
				Decision:      domain.DecisionEnter,
				Score:         decimal.NewFromInt(int64(100 - i)),
			},
			Instrument: domain.Instrument{InstrumentUID: uid, Lot: 1, MinPriceIncrement: decimal.NewFromInt(1)},
			Feature: domain.FeatureSet{
				EntryIntervalVolume: decimal.NewFromInt(1_000_000),
				ExitIntervalVolume:  decimal.NewFromInt(1_000_000),
				SigmaOn60:           decimal.NewFromInt(1),
			},
			Book: book,
		})
	}
	s.applyBatchSignalLimits(domain.Portfolio{Equity: decimal.NewFromInt(100_000), Cash: decimal.NewFromInt(100_000)}, decimal.Zero, 0, generated)
	enters := 0
	total := decimal.Zero
	for _, candidate := range generated {
		if candidate.Signal.Decision == domain.DecisionEnter {
			enters++
			total = total.Add(candidate.Signal.TargetNotional)
		}
	}
	if enters != 5 {
		t.Fatalf("enter signals=%d, want 5", enters)
	}
	if total.GreaterThan(decimal.NewFromInt(50_000)) {
		t.Fatalf("total target notional=%s exceeds 50%% exposure", total)
	}
	if generated[5].Signal.RejectReason != signalengine.ReasonMaxPositions {
		t.Fatalf("sixth signal reason=%q, want max positions", generated[5].Signal.RejectReason)
	}
}

func TestPlaceEntryRejectsWideSpreadBeforeOrder(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	tradeDate := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	instrument := domain.Instrument{
		InstrumentUID:        "uid",
		Ticker:               "TRUR",
		ClassCode:            "TQTF",
		Enabled:              true,
		Lot:                  1,
		MinPriceIncrement:    decimal.RequireFromString("0.01"),
		Currency:             "RUB",
		FreeOrderLimitPerDay: -1,
	}
	if err := repo.UpsertInstrument(ctx, instrument); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertSignal(ctx, domain.Signal{
		TradeDate:     tradeDate,
		InstrumentUID: "uid",
		Decision:      domain.DecisionEnter,
		Score:         decimal.NewFromInt(10),
		TargetLots:    1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertFeature(ctx, domain.FeatureSet{
		InstrumentUID:       "uid",
		TradeDate:           tradeDate,
		EntryIntervalVolume: decimal.NewFromInt(1_000_000),
		ExitIntervalVolume:  decimal.NewFromInt(1_000_000),
		SigmaOn60:           decimal.NewFromInt(1),
	}); err != nil {
		t.Fatal(err)
	}
	gateway := tinvest.NewFakeGateway()
	gateway.OrderBooks["uid"] = domain.OrderBook{
		InstrumentUID: "uid",
		Bids:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(90), QuantityLots: 10}},
		Asks:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(110), QuantityLots: 10}},
		ReceivedAt:    time.Now().UTC(),
	}
	execEngine := execution.NewEngine(domain.ModePaper, "account", gateway, repo)
	now := tradeDate.Add(18 * time.Hour)
	s := Scheduler{
		clock: fixedClock{now: now},
		cfg: Config{
			Mode:             domain.ModePaper,
			Location:         time.UTC,
			NoNewEntryAfter:  mustTOD("23:00:00"),
			MaxQuoteAge:      time.Minute,
			MarketClose:      mustTOD("23:30:00"),
			MaxOpenPositions: 5,
		},
		sm: statemachine.New(repo, domain.ModePaper),
		svc: Services{
			Repo:          repo,
			Gateway:       gateway,
			MarketData:    marketdata.NewLoader(repo, gateway),
			Signals:       signalengine.New(signalengine.Config{MaxSpreadBpsDefault: decimal.NewFromInt(20)}),
			Sizer:         risk.NewSizer(testSizingConfig()),
			FreeOrders:    risk.NewFreeOrderBudget(repo),
			Risk:          risk.NewManager(repo, risk.ManagerConfig{MaxOpenPositions: 5}),
			Execution:     &execEngine,
			Positions:     position.NewManager(repo),
			Notifier:      &countNotifier{},
			AccountID:     "account",
			AccountIDHash: "hash",
		},
	}
	if err := repo.SaveSystemState(ctx, domain.StateWaitEntryWindow, domain.ModePaper, false, "", "{}"); err != nil {
		t.Fatal(err)
	}
	if err := s.placeEntryOrders(ctx, now); err != nil {
		t.Fatal(err)
	}
	orders, err := repo.ListOrders(ctx, "hash", tradeDate, tradeDate)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 0 {
		t.Fatalf("orders=%+v, want no order on wide spread", orders)
	}
	if len(repo.RiskEvents) != 1 || repo.RiskEvents[0].EventType != "pre_trade_reject" {
		t.Fatalf("risk events=%+v", repo.RiskEvents)
	}
}

func TestPlaceExitUsesCurrentTradeDateForOrderAndFreeCounter(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	openDate := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	exitDate := openDate.AddDate(0, 0, 1)
	instrument := domain.Instrument{
		InstrumentUID:        "uid",
		Ticker:               "TRUR",
		ClassCode:            "TQTF",
		Enabled:              true,
		Lot:                  1,
		MinPriceIncrement:    decimal.RequireFromString("0.01"),
		Currency:             "RUB",
		FreeOrderLimitPerDay: 10,
	}
	if err := repo.UpsertInstrument(ctx, instrument); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertPosition(ctx, domain.Position{
		AccountIDHash: "hash",
		InstrumentUID: "uid",
		OpenTradeDate: openDate,
		Lots:          2,
		Lot:           1,
		AvgBuyPrice:   decimal.NewFromInt(100),
		Status:        domain.PositionHoldingOvernight,
	}); err != nil {
		t.Fatal(err)
	}
	gateway := tinvest.NewFakeGateway()
	gateway.OrderBooks["uid"] = domain.OrderBook{
		InstrumentUID: "uid",
		Bids:          []domain.OrderBookLevel{{Price: decimal.NewFromInt(100), QuantityLots: 10}},
		Asks:          []domain.OrderBookLevel{{Price: decimal.RequireFromString("100.10"), QuantityLots: 10}},
		ReceivedAt:    time.Now().UTC(),
	}
	execEngine := execution.NewEngine(domain.ModePaper, "account", gateway, repo)
	s := Scheduler{
		cfg: Config{
			Mode:             domain.ModePaper,
			Location:         time.UTC,
			HardExitDeadline: mustTOD("23:00:00"),
			MaxQuoteAge:      time.Minute,
			MarketClose:      mustTOD("23:30:00"),
		},
		sm: statemachine.New(repo, domain.ModePaper),
		svc: Services{
			Repo:          repo,
			Gateway:       gateway,
			MarketData:    marketdata.NewLoader(repo, gateway),
			Signals:       signalengine.New(signalengine.Config{MaxSpreadBpsDefault: decimal.NewFromInt(20)}),
			FreeOrders:    risk.NewFreeOrderBudget(repo),
			Risk:          risk.NewManager(repo, risk.ManagerConfig{}),
			Execution:     &execEngine,
			Positions:     position.NewManager(repo),
			Reconcile:     reconciliation.New(repo, gateway, "account", "hash"),
			Notifier:      &countNotifier{},
			AccountID:     "account",
			AccountIDHash: "hash",
		},
	}
	if err := repo.SaveSystemState(ctx, domain.StateWaitExitWindow, domain.ModePaper, false, "", "{}"); err != nil {
		t.Fatal(err)
	}
	if err := s.placeExitOrders(ctx, exitDate.Add(10*time.Hour)); err != nil {
		t.Fatal(err)
	}
	orders, err := repo.ListOrders(ctx, "hash", exitDate, exitDate)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || !sameTradingDate(orders[0].TradeDate, exitDate) {
		t.Fatalf("orders=%+v, want one exit order on current date", orders)
	}
	sentToday, err := repo.GetFreeOrdersSent(ctx, exitDate, "uid")
	if err != nil {
		t.Fatal(err)
	}
	sentOpenDate, err := repo.GetFreeOrdersSent(ctx, openDate, "uid")
	if err != nil {
		t.Fatal(err)
	}
	if sentToday != 1 || sentOpenDate != 0 {
		t.Fatalf("free counters today=%d openDate=%d, want 1/0", sentToday, sentOpenDate)
	}
}

func TestGracefulShutdownCancelsActiveOrders(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	gateway := tinvest.NewFakeGateway()
	tradeDate := time.Date(2026, 6, 6, 0, 0, 0, 0, time.UTC)
	order := domain.Order{
		ClientOrderID: "shutdown-order",
		BrokerOrderID: "broker-shutdown-order",
		AccountIDHash: "hash",
		InstrumentUID: "uid",
		TradeDate:     tradeDate,
		Side:          domain.SideBuy,
		OrderType:     domain.OrderTypeLimit,
		LimitPrice:    decimal.NewFromInt(100),
		QuantityLots:  1,
		Status:        domain.OrderStatusSent,
		RawStateJSON:  "{}",
	}
	if err := repo.UpsertOrder(ctx, order); err != nil {
		t.Fatal(err)
	}
	gateway.Orders[order.BrokerOrderID] = order
	execEngine := execution.NewEngine(domain.ModeSandbox, "account", gateway, repo)
	s := Scheduler{
		cfg: Config{Mode: domain.ModeSandbox},
		svc: Services{
			Repo:          repo,
			Execution:     &execEngine,
			AccountIDHash: "hash",
		},
	}
	if err := s.GracefulShutdown(ctx); err != nil {
		t.Fatal(err)
	}
	orders, err := repo.ListOrders(ctx, "hash", tradeDate, tradeDate)
	if err != nil {
		t.Fatal(err)
	}
	if len(orders) != 1 || orders[0].Status != domain.OrderStatusCancelled {
		t.Fatalf("orders=%+v, want cancelled", orders)
	}
}

func mustTOD(raw string) timeutil.TimeOfDay {
	tod, err := timeutil.ParseTimeOfDay(raw)
	if err != nil {
		panic(err)
	}
	return tod
}

func testSizingConfig() risk.SizingConfig {
	return risk.SizingConfig{
		MaxPositionPct:             decimal.NewFromInt(1),
		MaxTotalExposurePct:        decimal.NewFromInt(1),
		MaxParticipationRate:       decimal.NewFromInt(1),
		CashUsageBuffer:            decimal.NewFromInt(1),
		RiskBudgetPerInstrumentPct: decimal.NewFromInt(1),
		MinOrderNotionalRUB:        decimal.NewFromInt(1),
	}
}

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time {
	return c.now
}

func (fixedClock) Sleep(<-chan struct{}, time.Duration) bool {
	return true
}

type countNotifier struct {
	reports int
	alerts  int
}

func (n *countNotifier) Info(context.Context, string) error   { return nil }
func (n *countNotifier) Warn(context.Context, string) error   { return nil }
func (n *countNotifier) Alert(context.Context, string) error  { n.alerts++; return nil }
func (n *countNotifier) Report(context.Context, string) error { n.reports++; return nil }
func (n *countNotifier) Close() error                         { return nil }
