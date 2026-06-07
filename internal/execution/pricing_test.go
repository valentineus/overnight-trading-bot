package execution

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
)

func ed(raw string) decimal.Decimal {
	v, err := decimal.NewFromString(raw)
	if err != nil {
		panic(err)
	}
	return v
}

func TestLimitPricesDoNotCross(t *testing.T) {
	buy, err := LimitBuyPrice(ed("100"), ed("100.03"), ed("0.01"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !buy.Equal(ed("100.01")) {
		t.Fatalf("buy=%s", buy)
	}
	sell, err := LimitSellPrice(ed("100"), ed("100.03"), ed("0.01"), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !sell.Equal(ed("100.02")) {
		t.Fatalf("sell=%s", sell)
	}
	tightBuy, _ := LimitBuyPrice(ed("100"), ed("100.01"), ed("0.01"), 1)
	if !tightBuy.Equal(ed("100")) {
		t.Fatalf("tight buy=%s", tightBuy)
	}
}

func TestClientOrderIDDeterministic(t *testing.T) {
	date := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	longUID := "a-realistic-instrument-uid-that-is-much-longer-than-the-order-id-limit"
	a := ClientOrderID(date, longUID, domain.SideBuy, 1)
	b := ClientOrderID(date, longUID, domain.SideBuy, 1)
	c := ClientOrderID(date, longUID, domain.SideBuy, 2)
	if a != b || a == c {
		t.Fatalf("unexpected ids: %s %s %s", a, b, c)
	}
	if len(a) > maxClientOrderIDLen {
		t.Fatalf("client order id len=%d, want <=%d: %s", len(a), maxClientOrderIDLen, a)
	}
}
