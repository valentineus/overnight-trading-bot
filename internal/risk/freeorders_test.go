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
