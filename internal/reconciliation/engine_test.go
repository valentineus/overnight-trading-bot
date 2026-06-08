package reconciliation

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/testutil"
	"overnight-trading-bot/internal/tinvest"
)

func TestReconciliationFindsCriticalDiffs(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	gateway := tinvest.NewFakeGateway()
	now := time.Now().UTC()
	if err := repo.UpsertOrder(ctx, domain.Order{
		ClientOrderID: "local",
		BrokerOrderID: "broker-missing",
		AccountIDHash: "hash",
		InstrumentUID: "uid-local",
		TradeDate:     now,
		Side:          domain.SideBuy,
		OrderType:     domain.OrderTypeLimit,
		QuantityLots:  1,
		Status:        domain.OrderStatusSent,
	}); err != nil {
		t.Fatal(err)
	}
	gateway.Orders["broker-unknown"] = domain.Order{
		ClientOrderID: "unknown",
		BrokerOrderID: "broker-unknown",
		AccountIDHash: "hash",
		InstrumentUID: "uid-broker",
		QuantityLots:  1,
		Status:        domain.OrderStatusSent,
	}
	if err := repo.UpsertPosition(ctx, domain.Position{
		AccountIDHash: "hash",
		InstrumentUID: "uid-local",
		OpenTradeDate: now,
		Lots:          2,
		Status:        domain.PositionHoldingOvernight,
	}); err != nil {
		t.Fatal(err)
	}
	gateway.Portfolio = domain.Portfolio{
		Equity: decimal.NewFromInt(100000),
		Cash:   decimal.NewFromInt(90000),
		Holdings: []domain.Holding{
			{InstrumentUID: "uid-local", QuantityLots: 1},
			{InstrumentUID: "uid-broker-only", QuantityLots: 3},
		},
	}
	diffs, err := New(repo, gateway, "account", "hash").Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := map[string]bool{
		"unknown_active_order":    false,
		"missing_local_order":     false,
		"position_lots_mismatch":  false,
		"unknown_broker_position": false,
	}
	for _, diff := range diffs {
		if _, ok := wantKinds[diff.Kind]; ok {
			wantKinds[diff.Kind] = true
		}
	}
	for kind, seen := range wantKinds {
		if !seen {
			t.Fatalf("missing diff kind %s in %+v", kind, diffs)
		}
	}
	if !HasCritical(diffs) {
		t.Fatalf("expected critical diffs")
	}
}

func TestCompareOperationsCommissionPerInstrument(t *testing.T) {
	orders := []domain.Order{
		{InstrumentUID: "TRUR", Status: domain.OrderStatusFilled, Commission: decimal.NewFromInt(2)},
		{InstrumentUID: "TGLD", Status: domain.OrderStatusFilled, Commission: decimal.NewFromInt(1)},
	}
	operations := []domain.Operation{
		{InstrumentUID: "TRUR", Type: "OPERATION_TYPE_BUY", Commission: decimal.NewFromInt(1)},
		{InstrumentUID: "TGLD", Type: "OPERATION_TYPE_BUY", Commission: decimal.NewFromInt(2)},
	}
	diffs := compareOperations(orders, operations)
	seen := map[string]bool{}
	for _, diff := range diffs {
		if diff.Kind == "commission_mismatch" {
			seen[diff.InstrumentUID] = true
		}
	}
	if !seen["TRUR"] || !seen["TGLD"] {
		t.Fatalf("expected per-instrument commission diffs, got %+v", diffs)
	}
}

func TestReconciliationQuarantinesOnNonZeroBrokerCommission(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	gateway := tinvest.NewFakeGateway()
	if err := repo.UpsertInstrument(ctx, domain.Instrument{
		InstrumentUID: "uid",
		Ticker:        "TRUR",
		Enabled:       true,
	}); err != nil {
		t.Fatal(err)
	}
	gateway.Operations = []domain.Operation{{
		InstrumentUID: "uid",
		Type:          "OPERATION_TYPE_BROKER_FEE",
		Commission:    decimal.NewFromFloat(0.01),
		ExecutedAt:    time.Now().UTC(),
	}}
	diffs, err := New(repo, gateway, "account", "hash").
		WithCommissionPolicy(true, true, decimal.NewFromFloat(0.01)).
		Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, diff := range diffs {
		if diff.Kind == "actual_commission_nonzero" && diff.Critical {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected actual_commission_nonzero diff, got %+v", diffs)
	}
	instruments, err := repo.ListInstruments(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(instruments) != 1 || !instruments[0].Quarantine {
		t.Fatalf("instrument not quarantined: %+v", instruments)
	}
}

func TestReconciliationSkipsFreshInFlightLocalOrders(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	gateway := tinvest.NewFakeGateway()
	now := time.Now().UTC()
	if err := repo.UpsertOrder(ctx, domain.Order{
		ClientOrderID: "fresh",
		AccountIDHash: "hash",
		InstrumentUID: "uid",
		TradeDate:     now,
		Side:          domain.SideBuy,
		OrderType:     domain.OrderTypeLimit,
		QuantityLots:  1,
		Status:        domain.OrderStatusSent,
		CreatedAt:     now,
	}); err != nil {
		t.Fatal(err)
	}
	diffs, err := New(repo, gateway, "account", "hash").WithInFlightGrace(10 * time.Second).Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, diff := range diffs {
		if diff.Kind == "local_order_without_broker_id" || diff.Kind == "missing_local_order" {
			t.Fatalf("fresh in-flight order produced diff: %+v", diffs)
		}
	}
}

func TestReconciliationFindsCashMismatch(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	gateway := tinvest.NewFakeGateway()
	if err := repo.UpsertPosition(ctx, domain.Position{
		AccountIDHash: "hash",
		InstrumentUID: "uid",
		OpenTradeDate: time.Now().UTC(),
		Lots:          2,
		Status:        domain.PositionHoldingOvernight,
	}); err != nil {
		t.Fatal(err)
	}
	gateway.Portfolio = domain.Portfolio{
		Equity: decimal.NewFromInt(1000),
		Cash:   decimal.NewFromInt(700),
		Holdings: []domain.Holding{{
			InstrumentUID: "uid",
			QuantityLots:  2,
			MarketValue:   decimal.NewFromInt(200),
		}},
	}
	diffs, err := New(repo, gateway, "account", "hash").Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, diff := range diffs {
		if diff.Kind == "cash_mismatch" && diff.Critical {
			return
		}
	}
	t.Fatalf("missing cash_mismatch in %+v", diffs)
}
