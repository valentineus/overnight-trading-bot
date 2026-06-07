package features

import (
	"math"
	"testing"
)

func TestRollingStats(t *testing.T) {
	values := []float64{0.01, -0.01, 0.02, 0.03}
	got := Rolling(values, 4, 0.5)
	if !got.Available {
		t.Fatal("expected rolling result")
	}
	if math.Abs(got.Mean-0.0125) > 1e-12 {
		t.Fatalf("mean=%f", got.Mean)
	}
	if math.Abs(got.WinRate-0.75) > 1e-12 {
		t.Fatalf("win=%f", got.WinRate)
	}
	if got.StdDev <= 0 || got.TStat <= 0 {
		t.Fatalf("std/tstat invalid: %+v", got)
	}
}

func TestRollingSigmaZero(t *testing.T) {
	got := Rolling([]float64{0.01, 0.01, 0.01}, 3, 0.08)
	if got.StdDev != 0 || got.TStat != 0 {
		t.Fatalf("expected zero sigma/tstat, got %+v", got)
	}
}
