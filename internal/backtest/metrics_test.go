package backtest

import "testing"

func TestMetrics(t *testing.T) {
	got := ComputeMetrics([]Point{
		point("START", "100", "0"),
		point("2024-01-02", "110", "0.10"),
		point("2024-01-03", "99", "-0.10"),
	}, []Trade{{Return: point("", "0", "0.10").Return}, {Return: point("", "0", "-0.10").Return}})
	if got.NumberOfTrades != 2 || got.WinRate != 0.5 || got.MaxDrawdown >= 0 || got.VaR95 >= 0 {
		t.Fatalf("unexpected metrics: %+v", got)
	}
}
