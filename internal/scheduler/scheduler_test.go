package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/execution"
	"overnight-trading-bot/internal/reconciliation"
	"overnight-trading-bot/internal/risk"
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

func mustTOD(raw string) timeutil.TimeOfDay {
	tod, err := timeutil.ParseTimeOfDay(raw)
	if err != nil {
		panic(err)
	}
	return tod
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
