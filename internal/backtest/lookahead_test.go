package backtest

import (
	"strings"
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
		{TradeDate: time.Date(2024, 1, 2, 12, 0, 0, 0, time.UTC), Low: decimal.NewFromInt(1), High: decimal.NewFromInt(200), VolumeLots: decimal.NewFromInt(1_000_000)},
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

func TestEvaluateCandidateUsesInstrumentLotAndTick(t *testing.T) {
	engine := New(Config{
		RollingShort:                   2,
		RollingLong:                    2,
		MinTStat60:                     decimal.NewFromInt(-1),
		MinWinRate60:                   decimal.NewFromFloat(0.1),
		MinNetEdgeBps:                  decimal.NewFromInt(-1000),
		MinADVRUB:                      decimal.NewFromInt(1),
		Lot:                            1,
		LotsByInstrument:               map[string]int64{"uid": 10},
		MinPriceIncrementsByInstrument: map[string]decimal.Decimal{"uid": decimal.NewFromFloat(0.05)},
		EntrySlippageBps:               decimal.NewFromInt(13),
		ExitSlippageBps:                decimal.NewFromInt(13),
	})
	candles := candidateCandles("uid")
	got, ok, err := engine.evaluateCandidate("uid", candles, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected candidate")
	}
	if got.lot != 10 {
		t.Fatalf("lot=%d, want 10", got.lot)
	}
	if !got.adv.Equal(decimal.NewFromInt(10_000)) {
		t.Fatalf("adv=%s, want 10000", got.adv)
	}
	if !got.buy.Equal(decimal.NewFromFloat(100.15)) {
		t.Fatalf("buy=%s, want rounded 100.15", got.buy)
	}
	if !got.sell.Equal(decimal.NewFromFloat(104.85)) {
		t.Fatalf("sell=%s, want rounded 104.85", got.sell)
	}
}

func TestWindowCapacityUsesMinuteEntryAndExitWindows(t *testing.T) {
	engine := New(Config{
		Lot:                  10,
		MaxParticipationRate: decimal.NewFromFloat(0.10),
	})
	entryDate := time.Date(2024, 1, 2, 18, 25, 0, 0, time.UTC)
	exitDate := time.Date(2024, 1, 3, 10, 5, 0, 0, time.UTC)
	got := engine.windowCapacity(candidate{
		instrumentUID: "uid",
		entry:         domain.Candle{TradeDate: entryDate},
		exit:          domain.Candle{TradeDate: exitDate},
	}, []domain.Candle{
		{TradeDate: entryDate, Close: decimal.NewFromInt(100), VolumeLots: decimal.NewFromInt(20)},
		{TradeDate: exitDate, Close: decimal.NewFromInt(200), VolumeLots: decimal.NewFromInt(5)},
		{TradeDate: time.Date(2024, 1, 3, 12, 0, 0, 0, time.UTC), Close: decimal.NewFromInt(999), VolumeLots: decimal.NewFromInt(999)},
	})
	if !got.Equal(decimal.NewFromInt(1000)) {
		t.Fatalf("capacity=%s, want min(entry=20000, exit=10000)*0.10 = 1000", got)
	}
}

func TestBacktestWithoutMinuteDataDoesNotReportADVAsCapacity(t *testing.T) {
	engine := New(Config{
		RollingShort:  2,
		RollingLong:   2,
		MinTStat60:    decimal.NewFromInt(-1),
		MinWinRate60:  decimal.NewFromFloat(0.1),
		MinNetEdgeBps: decimal.NewFromInt(-1000),
		MinADVRUB:     decimal.NewFromInt(1),
	})
	result, err := engine.Run(map[string][]domain.Candle{"uid": candidateCandles("uid")})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Trades) == 0 {
		t.Fatal("expected daily-only minimal backtest trade")
	}
	if !result.Trades[0].CapacityRUB.IsZero() {
		t.Fatalf("capacity=%s, want zero when minute windows are unavailable", result.Trades[0].CapacityRUB)
	}
}

func TestLoadCandlesCSVWithMetadata(t *testing.T) {
	raw := strings.NewReader(`instrument_uid,trade_date,open,high,low,close,volume_lots,lot,min_price_increment
uid,2024-01-02,100,101,99,100,10,10,0.05
`)
	candles, metadata, err := LoadCandlesCSVWithMetadata(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(candles["uid"]) != 1 {
		t.Fatalf("candles=%+v", candles)
	}
	if metadata["uid"].Lot != 10 || !metadata["uid"].MinPriceIncrement.Equal(decimal.NewFromFloat(0.05)) {
		t.Fatalf("metadata=%+v", metadata["uid"])
	}
}

func candidateCandles(uid string) []domain.Candle {
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	return []domain.Candle{
		{InstrumentUID: uid, TradeDate: start, Open: decimal.NewFromInt(100), Close: decimal.NewFromInt(100), VolumeLots: decimal.NewFromInt(10)},
		{InstrumentUID: uid, TradeDate: start.AddDate(0, 0, 1), Open: decimal.NewFromInt(101), Close: decimal.NewFromInt(100), VolumeLots: decimal.NewFromInt(10)},
		{InstrumentUID: uid, TradeDate: start.AddDate(0, 0, 2), Open: decimal.NewFromInt(102), Close: decimal.NewFromInt(100), VolumeLots: decimal.NewFromInt(10)},
		{InstrumentUID: uid, TradeDate: start.AddDate(0, 0, 3), Open: decimal.NewFromInt(105), Close: decimal.NewFromInt(100), VolumeLots: decimal.NewFromInt(10)},
	}
}
