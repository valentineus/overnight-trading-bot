package features

import "testing"

func TestSpread(t *testing.T) {
	got, err := Spread(dec("99"), dec("101"), dec("0.1"))
	if err != nil {
		t.Fatal(err)
	}
	if !got.Mid.Equal(dec("100")) || !got.SpreadBps.Equal(dec("200")) || !got.HalfSpreadBps.Equal(dec("100")) || !got.TickBps.Equal(dec("10")) {
		t.Fatalf("unexpected spread: %+v", got)
	}
}
