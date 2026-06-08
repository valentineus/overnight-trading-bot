package mysql

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
)

func TestMinuteCandleRowPreservesTimestamp(t *testing.T) {
	ts := time.Date(2026, 6, 8, 15, 25, 30, 123000000, time.UTC)
	row := minuteCandleRowFromDomain(domain.Candle{
		InstrumentUID: "uid",
		TradeDate:     ts,
		Open:          decimal.NewFromInt(1),
	})
	if !row.TradeDate.Equal(ts) {
		t.Fatalf("minute timestamp=%s, want %s", row.TradeDate, ts)
	}

	daily := candleRowFromDomain(domain.Candle{InstrumentUID: "uid", TradeDate: ts})
	if daily.TradeDate.Equal(ts) || daily.TradeDate.Hour() != 0 || daily.TradeDate.Minute() != 0 {
		t.Fatalf("daily timestamp was not truncated to date: %s", daily.TradeDate)
	}
}
