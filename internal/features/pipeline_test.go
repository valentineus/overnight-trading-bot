package features

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/testutil"
	"overnight-trading-bot/internal/timeutil"
)

func TestComputeExpectedCostIncludesCommissionAndSlippage(t *testing.T) {
	var candles []domain.Candle
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 6; i++ {
		price := decimal.NewFromInt(int64(100 + i))
		candles = append(candles, domain.Candle{
			InstrumentUID: "uid",
			TradeDate:     start.AddDate(0, 0, i),
			Open:          price,
			Close:         price,
			VolumeLots:    decimal.NewFromInt(1000),
		})
	}
	got, err := Compute(domain.Instrument{
		InstrumentUID:                "uid",
		Lot:                          1,
		ExpectedCommissionBpsPerSide: decimal.NewFromInt(1),
	}, candles, start.AddDate(0, 0, 5), SpreadResult{SpreadBps: decimal.NewFromInt(10)}, PipelineConfig{
		RollingShort:           2,
		RollingLong:            2,
		EWMALambda:             0.08,
		RiskBufferBps:          decimal.NewFromInt(5),
		EntrySlippageBps:       decimal.NewFromInt(2),
		ExitSlippageBps:        decimal.NewFromInt(3),
		CommissionRoundtripBps: decimal.NewFromInt(4),
	}, decimal.NewFromInt(10000), decimal.NewFromInt(9000))
	if err != nil {
		t.Fatal(err)
	}
	if !got.ExpectedCostBps.Equal(decimal.NewFromInt(26)) {
		t.Fatalf("expected cost=%s, want 26", got.ExpectedCostBps)
	}
	if !got.EntryIntervalVolume.Equal(decimal.NewFromInt(10000)) || !got.ExitIntervalVolume.Equal(decimal.NewFromInt(9000)) {
		t.Fatalf("interval volumes were not preserved: %+v", got)
	}
}

func TestIntervalVolume(t *testing.T) {
	got := IntervalVolume([]domain.Candle{
		{Close: decimal.NewFromInt(100), VolumeLots: decimal.NewFromInt(10)},
		{Close: decimal.NewFromInt(101), VolumeLots: decimal.NewFromInt(20)},
	}, 2)
	if !got.Equal(decimal.NewFromInt(6040)) {
		t.Fatalf("interval volume=%s, want 6040", got)
	}
}

func TestAverageIntervalVolumeUsesExecutionWindowsAcrossDays(t *testing.T) {
	loc := time.FixedZone("MSK", 3*60*60)
	window := timeutil.Window{
		Start: mustTOD("18:20:00"),
		End:   mustTOD("18:40:00"),
	}
	candles := []domain.Candle{
		{TradeDate: time.Date(2026, 6, 1, 15, 20, 0, 0, time.UTC), Close: decimal.NewFromInt(100), VolumeLots: decimal.NewFromInt(10)},
		{TradeDate: time.Date(2026, 6, 1, 15, 50, 0, 0, time.UTC), Close: decimal.NewFromInt(999), VolumeLots: decimal.NewFromInt(999)},
		{TradeDate: time.Date(2026, 6, 2, 15, 25, 0, 0, time.UTC), Close: decimal.NewFromInt(200), VolumeLots: decimal.NewFromInt(10)},
	}
	got := AverageIntervalVolume(candles, 1, window, loc)
	if !got.Equal(decimal.NewFromInt(1500)) {
		t.Fatalf("average interval volume=%s, want 1500", got)
	}
}

func TestRecomputeExcludesTradeDateDailyCandle(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	start := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	var candles []domain.Candle
	for i := 0; i < 6; i++ {
		closePrice := decimal.NewFromInt(100)
		if i == 5 {
			closePrice = decimal.NewFromInt(100000)
		}
		candles = append(candles, domain.Candle{
			InstrumentUID: "uid",
			TradeDate:     start.AddDate(0, 0, i),
			Open:          decimal.NewFromInt(100),
			Close:         closePrice,
			VolumeLots:    decimal.NewFromInt(1),
		})
	}
	if err := repo.UpsertDailyCandles(ctx, candles); err != nil {
		t.Fatal(err)
	}
	pipeline := NewPipeline(repo, PipelineConfig{
		RollingShort: 2,
		RollingLong:  2,
		EWMALambda:   0.08,
	})
	got, err := pipeline.Recompute(ctx, domain.Instrument{InstrumentUID: "uid", Lot: 1}, start.AddDate(0, 0, 5), SpreadResult{})
	if err != nil {
		t.Fatal(err)
	}
	if !got.ADV20.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("ADV20=%s, want tradeDate candle excluded", got.ADV20)
	}
}

func mustTOD(raw string) timeutil.TimeOfDay {
	tod, err := timeutil.ParseTimeOfDay(raw)
	if err != nil {
		panic(err)
	}
	return tod
}
