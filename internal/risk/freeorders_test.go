package risk

import (
	"context"
	"errors"
	"testing"
	"time"

	"overnight-trading-bot/internal/domain"
)

func TestFreeOrderBudgetSubmittedPolicy(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryFreeOrderStore()
	budget := NewFreeOrderBudget(store)
	instr := domain.Instrument{InstrumentUID: "uid", FreeOrderLimitPerDay: 2}
	date := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	if _, err := budget.Check(ctx, date, instr, 2); err != nil {
		t.Fatal(err)
	}
	if err := budget.Submitted(ctx, date, instr.InstrumentUID); err != nil {
		t.Fatal(err)
	}
	if _, err := budget.Check(ctx, date, instr, 2); !errors.Is(err, ErrFreeOrderBudget) {
		t.Fatalf("expected ErrFreeOrderBudget, got %v", err)
	}
}

func TestFreeOrderBudgetRequiresExplicitPolicy(t *testing.T) {
	ctx := context.Background()
	budget := NewFreeOrderBudget(NewMemoryFreeOrderStore())
	date := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	if _, err := budget.Check(ctx, date, domain.Instrument{InstrumentUID: "uid"}, 1); !errors.Is(err, ErrFreeOrderPolicyUnspecified) {
		t.Fatalf("expected ErrFreeOrderPolicyUnspecified, got %v", err)
	}
	if _, err := budget.Check(ctx, date, domain.Instrument{InstrumentUID: "uid", FreeOrderLimitPerDay: -1}, 1); err != nil {
		t.Fatalf("explicit no-cap policy should pass, got %v", err)
	}
}
