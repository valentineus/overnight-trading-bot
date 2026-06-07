package backtest

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
)

func TestBacktestNoLookAheadWithFutureOnlyEdge(t *testing.T) {
	var candles []domain.Candle
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 80; i++ {
		open := decimal.NewFromInt(100)
		if i == 79 {
			open = decimal.NewFromInt(110)
		}
		candles = append(candles, domain.Candle{
			InstrumentUID: "uid",
			TradeDate:     start.AddDate(0, 0, i),
			Open:          open,
			High:          open,
			Low:           open,
			Close:         decimal.NewFromInt(100),
		})
	}
	result, err := New(Config{}).Run(map[string][]domain.Candle{"uid": candles})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Trades) != 0 {
		t.Fatalf("future-only edge leaked into signals: %d trades", len(result.Trades))
	}
}

func TestMinuteExecutionRequiresReachableLimitAndParticipation(t *testing.T) {
	engine := New(Config{
		Lot:                  10,
		MaxParticipationRate: decimal.NewFromFloat(0.10),
	})
	entryDate := time.Date(2024, 1, 2, 18, 25, 0, 0, time.UTC)
	exitDate := time.Date(2024, 1, 3, 10, 5, 0, 0, time.UTC)
	c := candidate{
		instrumentUID: "uid",
		entry:         domain.Candle{TradeDate: entryDate},
		exit:          domain.Candle{TradeDate: exitDate},
		buy:           decimal.NewFromInt(100),
		sell:          decimal.NewFromInt(105),
	}
	minutes := []domain.Candle{
		{TradeDate: entryDate, Low: decimal.NewFromInt(99), High: decimal.NewFromInt(101), VolumeLots: decimal.NewFromInt(20)},
		{TradeDate: exitDate, Low: decimal.NewFromInt(104), High: decimal.NewFromInt(106), VolumeLots: decimal.NewFromInt(20)},
	}
	lots, capacity, ok := engine.minuteExecution(c, minutes, 5)
	if !ok {
		t.Fatal("expected minute execution")
	}
	if lots != 2 {
		t.Fatalf("lots=%d, want 2", lots)
	}
	if !capacity.Equal(decimal.NewFromInt(2000)) {
		t.Fatalf("capacity=%s, want 2000", capacity)
	}
	c.sell = decimal.NewFromInt(110)
	if _, _, ok := engine.minuteExecution(c, minutes, 5); ok {
		t.Fatal("sell limit should be unreachable")
	}
}
