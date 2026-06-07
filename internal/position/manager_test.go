package position

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/testutil"
)

func TestOnEntryFillKeepsBuyCommission(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(testutil.NewMemoryRepository())
	pos, err := manager.OnEntryFill(ctx, "hash", domain.Instrument{Lot: 1}, domain.Order{
		InstrumentUID: "uid",
		TradeDate:     time.Now().UTC(),
		QuantityLots:  10,
		FilledLots:    10,
		AvgFillPrice:  decimal.NewFromInt(100),
		Commission:    decimal.NewFromInt(3),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !pos.CommissionTotal.Equal(decimal.NewFromInt(3)) {
		t.Fatalf("commission=%s, want 3", pos.CommissionTotal)
	}
}

func TestOnExitFillPartialUsesExecutedLots(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(testutil.NewMemoryRepository())
	openAt := time.Now().UTC()
	pos := domain.Position{
		AccountIDHash:   "hash",
		InstrumentUID:   "uid",
		OpenTradeDate:   openAt,
		Lots:            10,
		Lot:             1,
		AvgBuyPrice:     decimal.NewFromInt(100),
		Status:          domain.PositionHoldingOvernight,
		CommissionTotal: decimal.NewFromInt(2),
		OpenedAt:        &openAt,
	}
	updated, err := manager.OnExitFill(ctx, pos, domain.Order{
		InstrumentUID: "uid",
		FilledLots:    4,
		AvgFillPrice:  decimal.NewFromInt(110),
		Commission:    decimal.NewFromInt(1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domain.PositionExitPartiallyFilled || updated.ClosedAt != nil {
		t.Fatalf("unexpected partial status/closed_at: %+v", updated)
	}
	if updated.Lots != 6 {
		t.Fatalf("remaining lots=%d, want 6", updated.Lots)
	}
	if !updated.GrossPnL.Equal(decimal.NewFromInt(40)) {
		t.Fatalf("gross pnl=%s, want 40", updated.GrossPnL)
	}
	if updated.ExitFilledLots != 4 || !updated.AvgSellPrice.Equal(decimal.NewFromInt(110)) {
		t.Fatalf("exit aggregation lots=%d avg=%s", updated.ExitFilledLots, updated.AvgSellPrice)
	}
	second, err := manager.OnExitFill(ctx, updated, domain.Order{
		InstrumentUID: "uid",
		FilledLots:    3,
		AvgFillPrice:  decimal.NewFromInt(120),
	})
	if err != nil {
		t.Fatal(err)
	}
	wantAvg := decimal.NewFromInt(800).Div(decimal.NewFromInt(7))
	if second.ExitFilledLots != 7 || !second.AvgSellPrice.Equal(wantAvg) {
		t.Fatalf("weighted avg sell=%s lots=%d, want %s/7", second.AvgSellPrice, second.ExitFilledLots, wantAvg)
	}
}

func TestOnExitFillUsesInstrumentLotForAbsolutePnL(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(testutil.NewMemoryRepository())
	openAt := time.Now().UTC()
	pos := domain.Position{
		AccountIDHash:   "hash",
		InstrumentUID:   "uid",
		OpenTradeDate:   openAt,
		Lots:            4,
		Lot:             10,
		AvgBuyPrice:     decimal.NewFromInt(100),
		Status:          domain.PositionHoldingOvernight,
		CommissionTotal: decimal.NewFromInt(2),
		OpenedAt:        &openAt,
	}
	updated, err := manager.OnExitFill(ctx, pos, domain.Order{
		InstrumentUID: "uid",
		FilledLots:    4,
		AvgFillPrice:  decimal.NewFromInt(105),
		Commission:    decimal.NewFromInt(3),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.GrossPnL.Equal(decimal.NewFromInt(200)) {
		t.Fatalf("gross pnl=%s, want 200", updated.GrossPnL)
	}
	if !updated.NetPnL.Equal(decimal.NewFromInt(195)) {
		t.Fatalf("net pnl=%s, want 195", updated.NetPnL)
	}
}

func TestOnExitFillUsesLotInRealizedEdgeCommissionBase(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(testutil.NewMemoryRepository())
	openAt := time.Now().UTC()
	pos := domain.Position{
		AccountIDHash: "hash",
		InstrumentUID: "uid",
		OpenTradeDate: openAt,
		Lots:          1,
		Lot:           100,
		AvgBuyPrice:   decimal.NewFromInt(100),
		Status:        domain.PositionHoldingOvernight,
		OpenedAt:      &openAt,
	}
	updated, err := manager.OnExitFill(ctx, pos, domain.Order{
		InstrumentUID: "uid",
		FilledLots:    1,
		AvgFillPrice:  decimal.NewFromInt(100),
		Commission:    decimal.NewFromInt(10),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.RealizedEdgeBps.Equal(decimal.NewFromInt(-10)) {
		t.Fatalf("realized edge=%s, want -10 bps", updated.RealizedEdgeBps)
	}
}
