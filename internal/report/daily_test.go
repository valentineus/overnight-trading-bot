package report

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
)

func TestAverageAdverseSlippageBpsUsesLocalQuoteMid(t *testing.T) {
	orders := []domain.Order{{
		InstrumentUID: "uid",
		Side:          domain.SideBuy,
		LimitPrice:    decimal.NewFromInt(100),
		FilledLots:    2,
		AvgFillPrice:  decimal.NewFromFloat(100.5),
		RawStateJSON:  `{"local":{"local_quote":{"mid":"100"}}}`,
		UpdatedAt:     time.Now().UTC(),
	}}
	got := AverageAdverseSlippageBps(orders, 0)
	if !got.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("slippage=%s, want 50", got)
	}
}

func TestAverageAdverseSlippageBpsPrefersFillQuoteMid(t *testing.T) {
	orders := []domain.Order{{
		InstrumentUID: "uid",
		Side:          domain.SideBuy,
		LimitPrice:    decimal.NewFromInt(100),
		FilledLots:    1,
		AvgFillPrice:  decimal.NewFromFloat(102),
		RawStateJSON:  `{"local":{"local_quote":{"mid":"100"},"fill_quotes":[{"mid":"101"}]}}`,
		UpdatedAt:     time.Now().UTC(),
	}}
	got := AverageAdverseSlippageBps(orders, 0)
	want := decimal.NewFromInt(10_000).Div(decimal.NewFromInt(101))
	if got.Sub(want).Abs().GreaterThan(decimal.NewFromFloat(0.000001)) {
		t.Fatalf("slippage=%s, want fill-mid based slippage", got)
	}
}

func TestAverageAdverseSlippageBpsFallsBackToLimit(t *testing.T) {
	orders := []domain.Order{{
		InstrumentUID: "uid",
		Side:          domain.SideSell,
		LimitPrice:    decimal.NewFromInt(100),
		FilledLots:    1,
		AvgFillPrice:  decimal.NewFromFloat(99.5),
		RawStateJSON:  `{}`,
		UpdatedAt:     time.Now().UTC(),
	}}
	got := AverageAdverseSlippageBps(orders, 0)
	if !got.Equal(decimal.NewFromInt(50)) {
		t.Fatalf("slippage=%s, want 50", got)
	}
}

func TestComposeDailyComputesSlippageWhenInputIsZero(t *testing.T) {
	msg := ComposeDaily(DailyInput{
		Date: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC),
		Mode: domain.ModePaper,
		Orders: []domain.Order{{
			Side:         domain.SideBuy,
			LimitPrice:   decimal.NewFromInt(100),
			FilledLots:   1,
			AvgFillPrice: decimal.NewFromFloat(100.5),
			RawStateJSON: `{"local":{"local_quote":{"mid":"100"}}}`,
			UpdatedAt:    time.Now().UTC(),
		}},
		RiskStatus: "ok",
	})
	if !strings.Contains(msg, "Среднее проскальзывание: 50.00 bps") {
		t.Fatalf("report did not include computed slippage:\n%s", msg)
	}
}
