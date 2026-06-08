package features

import (
	"context"
	"encoding/json"
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
	if !got.ExpectedCostBps.Equal(decimal.NewFromInt(22)) {
		t.Fatalf("expected cost=%s, want 22", got.ExpectedCostBps)
	}
	var breakdown map[string]string
	if err := json.Unmarshal([]byte(got.CostBreakdownJSON), &breakdown); err != nil {
		t.Fatalf("cost breakdown is not valid JSON: %v", err)
	}
	wantBreakdown := map[string]string{
		"expected_spread_entry_bps":   "5",
		"expected_spread_exit_bps":    "5",
		"expected_slippage_entry_bps": "2",
		"expected_slippage_exit_bps":  "3",
		"commission_roundtrip_bps":    "2",
		"risk_buffer_bps":             "5",
		"expected_cost_bps":           "22",
	}
	for key, want := range wantBreakdown {
		if breakdown[key] != want {
			t.Fatalf("breakdown[%s]=%q, want %q in %s", key, breakdown[key], want, got.CostBreakdownJSON)
		}
	}
	if !got.EntryIntervalVolume.Equal(decimal.NewFromInt(10000)) || !got.ExitIntervalVolume.Equal(decimal.NewFromInt(9000)) {
		t.Fatalf("interval volumes were not preserved: %+v", got)
	}
}

func TestComputeExpectedCostFallsBackToConfigCommission(t *testing.T) {
	candles := flatCandles(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 6)
	got, err := Compute(domain.Instrument{
		InstrumentUID: "uid",
		Lot:           1,
	}, candles, candles[5].TradeDate, SpreadResult{SpreadBps: decimal.NewFromInt(10)}, PipelineConfig{
		RollingShort:           2,
		RollingLong:            2,
		EWMALambda:             0.08,
		RiskBufferBps:          decimal.NewFromInt(5),
		EntrySlippageBps:       decimal.NewFromInt(2),
		ExitSlippageBps:        decimal.NewFromInt(3),
		CommissionRoundtripBps: decimal.NewFromInt(4),
	}, decimal.Zero, decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ExpectedCostBps.Equal(decimal.NewFromInt(24)) {
		t.Fatalf("expected cost=%s, want 24", got.ExpectedCostBps)
	}
}

func TestComputeStoresHistoricalQ05Abs(t *testing.T) {
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	returns := []string{"-0.10", "0.01", "0.02", "0.03", "0.04"}
	candles := []domain.Candle{{
		InstrumentUID: "uid",
		TradeDate:     start,
		Open:          decimal.NewFromInt(100),
		Close:         decimal.NewFromInt(100),
		VolumeLots:    decimal.NewFromInt(1),
	}}
	for i, raw := range returns {
		r, err := decimal.NewFromString(raw)
		if err != nil {
			t.Fatal(err)
		}
		open := decimal.NewFromInt(100).Mul(decimal.NewFromInt(1).Add(r))
		candles = append(candles, domain.Candle{
			InstrumentUID: "uid",
			TradeDate:     addBusinessDays(start, i+1),
			Open:          open,
			Close:         decimal.NewFromInt(100),
			VolumeLots:    decimal.NewFromInt(1),
		})
	}
	got, err := Compute(domain.Instrument{InstrumentUID: "uid", Lot: 1}, candles, addBusinessDays(start, 6), SpreadResult{}, PipelineConfig{
		RollingShort: 5,
		RollingLong:  5,
		EWMALambda:   0.08,
	}, decimal.Zero, decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	want := decimal.NewFromFloat(0.078)
	diff := got.Q05On60Abs.Sub(want)
	if diff.IsNegative() {
		diff = diff.Neg()
	}
	if diff.GreaterThan(decimal.NewFromFloat(0.000001)) {
		t.Fatalf("Q05On60Abs=%s, want about %s", got.Q05On60Abs, want)
	}
}

func TestComputeSkipsOvernightReturnAcrossMissingWeekday(t *testing.T) {
	start := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC) // Monday.
	candles := []domain.Candle{
		{InstrumentUID: "uid", TradeDate: start, Open: decimal.NewFromInt(100), Close: decimal.NewFromInt(100), VolumeLots: decimal.NewFromInt(1)},
		{InstrumentUID: "uid", TradeDate: start.AddDate(0, 0, 1), Open: decimal.NewFromInt(101), Close: decimal.NewFromInt(100), VolumeLots: decimal.NewFromInt(1)},
		{InstrumentUID: "uid", TradeDate: start.AddDate(0, 0, 3), Open: decimal.NewFromInt(50), Close: decimal.NewFromInt(100), VolumeLots: decimal.NewFromInt(1)},
	}
	got, err := Compute(domain.Instrument{InstrumentUID: "uid", Lot: 1}, candles, start.AddDate(0, 0, 4), SpreadResult{}, PipelineConfig{
		RollingShort: 1,
		RollingLong:  1,
		EWMALambda:   0.08,
	}, decimal.Zero, decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	want := decimal.RequireFromString("0.01")
	if !got.ROn.Equal(want) {
		t.Fatalf("ROn=%s, want %s from last consecutive pair", got.ROn, want)
	}
}

func TestComputeAllowsWeekendGap(t *testing.T) {
	friday := time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC)
	monday := friday.AddDate(0, 0, 3)
	candles := []domain.Candle{
		{InstrumentUID: "uid", TradeDate: friday, Open: decimal.NewFromInt(100), Close: decimal.NewFromInt(100), VolumeLots: decimal.NewFromInt(1)},
		{InstrumentUID: "uid", TradeDate: monday, Open: decimal.NewFromInt(101), Close: decimal.NewFromInt(100), VolumeLots: decimal.NewFromInt(1)},
	}
	got, err := Compute(domain.Instrument{InstrumentUID: "uid", Lot: 1}, candles, monday.AddDate(0, 0, 1), SpreadResult{}, PipelineConfig{
		RollingShort: 1,
		RollingLong:  1,
		EWMALambda:   0.08,
	}, decimal.Zero, decimal.Zero)
	if err != nil {
		t.Fatal(err)
	}
	want := decimal.RequireFromString("0.01")
	if !got.ROn.Equal(want) {
		t.Fatalf("ROn=%s, want %s across weekend", got.ROn, want)
	}
}

func flatCandles(start time.Time, count int) []domain.Candle {
	candles := make([]domain.Candle, 0, count)
	for i := 0; i < count; i++ {
		price := decimal.NewFromInt(int64(100 + i))
		candles = append(candles, domain.Candle{
			InstrumentUID: "uid",
			TradeDate:     start.AddDate(0, 0, i),
			Open:          price,
			Close:         price,
			VolumeLots:    decimal.NewFromInt(1000),
		})
	}
	return candles
}

func addBusinessDays(start time.Time, days int) time.Time {
	out := start
	for added := 0; added < days; {
		out = out.AddDate(0, 0, 1)
		if out.Weekday() == time.Saturday || out.Weekday() == time.Sunday {
			continue
		}
		added++
	}
	return out
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
