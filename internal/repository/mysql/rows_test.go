package mysql

import (
	"errors"
	"fmt"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
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

func TestIsMissingTableError(t *testing.T) {
	if !isMissingTableError(&mysql.MySQLError{Number: 1146}) {
		t.Fatal("expected MySQL 1146 to be treated as missing table")
	}
	if !isMissingTableError(fmt.Errorf("wrap: %w", &mysql.MySQLError{Number: 1146})) {
		t.Fatal("expected wrapped MySQL 1146 to be treated as missing table")
	}
	if isMissingTableError(errors.New("not mysql")) {
		t.Fatal("plain errors must not be treated as missing table")
	}
}
