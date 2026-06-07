package money

import (
	"testing"

	"github.com/shopspring/decimal"
)

func d(raw string) decimal.Decimal {
	v, err := decimal.NewFromString(raw)
	if err != nil {
		panic(err)
	}
	return v
}

func TestRoundToTick(t *testing.T) {
	tests := []struct {
		price string
		tick  string
		mode  RoundMode
		want  string
	}{
		{"10.12346", "0.0001", RoundNearest, "10.1235"},
		{"10.126", "0.01", RoundFloor, "10.12"},
		{"10.126", "0.01", RoundCeil, "10.13"},
		{"10.24", "0.5", RoundNearest, "10"},
		{"10.26", "0.5", RoundNearest, "10.5"},
	}
	for _, tt := range tests {
		got, err := RoundToTick(d(tt.price), d(tt.tick), tt.mode)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Equal(d(tt.want)) {
			t.Fatalf("RoundToTick(%s,%s)=%s want %s", tt.price, tt.tick, got, tt.want)
		}
	}
}
