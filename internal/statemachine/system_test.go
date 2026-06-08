package statemachine

import (
	"context"
	"errors"
	"testing"
	"time"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/reconciliation"
	"overnight-trading-bot/internal/testutil"
	"overnight-trading-bot/internal/tinvest"
)

func TestHeartbeatDoesNotClearHalt(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	system := New(repo, domain.ModeLiveTrade)
	if err := system.Halt(ctx, "manual kill switch"); err != nil {
		t.Fatal(err)
	}
	if err := system.Heartbeat(ctx, domain.StateSleep); err != nil {
		t.Fatal(err)
	}
	state, halted, reason, err := repo.GetSystemState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if state != domain.StateHalted || !halted || reason != "manual kill switch" {
		t.Fatalf("halt was not sticky: state=%s halted=%v reason=%q", state, halted, reason)
	}
}

func TestTransitionBlockedWhileHalted(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	system := New(repo, domain.ModePaper)
	if err := system.Halt(ctx, "risk"); err != nil {
		t.Fatal(err)
	}
	err := system.Transition(ctx, domain.StateHalted, domain.StateInit)
	if !errors.Is(err, ErrSystemHalted) {
		t.Fatalf("expected ErrSystemHalted, got %v", err)
	}
}

func TestUnhaltPreservesMode(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	if err := repo.SaveSystemState(ctx, domain.StateHalted, domain.ModeLiveTrade, true, "risk", "{}"); err != nil {
		t.Fatal(err)
	}
	if err := repo.Unhalt(ctx, "checked"); err != nil {
		t.Fatal(err)
	}
	_, halted, _, err := repo.GetSystemState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if halted || repo.Mode != domain.ModeLiveTrade {
		t.Fatalf("unhalt did not preserve mode: halted=%v mode=%s", halted, repo.Mode)
	}
}

func TestCalendarRecoveryAllowsRestartInsideExitWindow(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	system := New(repo, domain.ModePaper)
	if err := system.Transition(ctx, domain.StateInit, domain.StatePlaceExitOrders); err != nil {
		t.Fatalf("INIT -> PLACE_EXIT_ORDERS should be legal on restart: %v", err)
	}
	if err := repo.SaveSystemState(ctx, domain.StateHoldOvernight, domain.ModePaper, false, "", "{}"); err != nil {
		t.Fatal(err)
	}
	if err := system.Transition(ctx, domain.StateHoldOvernight, domain.StatePlaceExitOrders); err != nil {
		t.Fatalf("HOLD_OVERNIGHT -> PLACE_EXIT_ORDERS should be legal on restart: %v", err)
	}
}

func TestRecoverFromMonitorEntryHaltsOnCriticalReconciliationDiff(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	if err := repo.SaveSystemState(ctx, domain.StateMonitorEntryOrders, domain.ModePaper, false, "", "{}"); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertOrder(ctx, domain.Order{
		ClientOrderID: "local",
		BrokerOrderID: "broker-missing",
		AccountIDHash: "hash",
		InstrumentUID: "uid",
		TradeDate:     time.Now().UTC(),
		Side:          domain.SideBuy,
		OrderType:     domain.OrderTypeLimit,
		QuantityLots:  1,
		Status:        domain.OrderStatusSent,
		CreatedAt:     time.Now().UTC().Add(-time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	system := New(repo, domain.ModePaper)
	state, err := system.Recover(ctx, reconciliation.New(repo, tinvest.NewFakeGateway(), "account", "hash"))
	if err == nil {
		t.Fatal("expected critical reconciliation error")
	}
	if state != domain.StateHalted || !repo.Halted {
		t.Fatalf("state=%s halted=%v, want HALTED", state, repo.Halted)
	}
}
