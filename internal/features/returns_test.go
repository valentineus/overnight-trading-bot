package features

import (
	"math"
	"testing"

	"github.com/shopspring/decimal"
)

func dec(raw string) decimal.Decimal {
	v, err := decimal.NewFromString(raw)
	if err != nil {
		panic(err)
	}
	return v
}

func TestReturnsAndLogIdentity(t *testing.T) {
	rOn, err := OvernightReturn(dec("102"), dec("100"))
	if err != nil {
		t.Fatal(err)
	}
	if !rOn.Equal(dec("0.02")) {
		t.Fatalf("overnight return=%s", rOn)
	}
	rDay, err := IntradayReturn(dec("105"), dec("102"))
	if err != nil {
		t.Fatal(err)
	}
	if !rDay.Round(10).Equal(dec("0.0294117647")) {
		t.Fatalf("intraday return=%s", rDay)
	}
	linear := CumulativeLinear([]decimal.Decimal{dec("0.01"), dec("-0.02"), dec("0.03")})
	logs := []float64{math.Log(1.01), math.Log(0.98), math.Log(1.03)}
	if math.Abs(linear.InexactFloat64()-CumulativeLog(logs)) > 1e-10 {
		t.Fatalf("linear/log cumulative mismatch")
	}
}
