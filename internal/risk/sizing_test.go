package risk

import (
	"testing"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
)

func rd(raw string) decimal.Decimal {
	v, err := decimal.NewFromString(raw)
	if err != nil {
		panic(err)
	}
	return v
}

func TestSizerTakesMinimumOfLimits(t *testing.T) {
	sizer := NewSizer(SizingConfig{
		MaxPositionPct:             rd("0.10"),
		MaxTotalExposurePct:        rd("0.50"),
		MaxParticipationRate:       rd("0.01"),
		CashUsageBuffer:            rd("0.95"),
		RiskBudgetPerInstrumentPct: rd("0.005"),
		MinOrderNotionalRUB:        rd("1000"),
	})
	got := sizer.Size(SizingInput{
		Portfolio:           domain.Portfolio{Equity: rd("100000"), Cash: rd("90000")},
		SelectedInstruments: 5,
		LimitPrice:          rd("100"),
		Lot:                 1,
		EntryIntervalVolume: rd("1000000"),
		ExitIntervalVolume:  rd("1000000"),
		Q05OvernightAbs:     rd("0.05"),
	})
	if got.Lots != 100 || !got.TargetNotional.Equal(rd("10000")) {
		t.Fatalf("unexpected sizing: %+v", got)
	}
}

func TestSizerMinOrderGate(t *testing.T) {
	sizer := NewSizer(SizingConfig{
		MaxPositionPct:             rd("0.10"),
		MaxTotalExposurePct:        rd("0.50"),
		MaxParticipationRate:       rd("0.01"),
		CashUsageBuffer:            rd("0.95"),
		RiskBudgetPerInstrumentPct: rd("0.005"),
		MinOrderNotionalRUB:        rd("1000"),
	})
	got := sizer.Size(SizingInput{
		Portfolio:           domain.Portfolio{Equity: rd("10000"), Cash: rd("10000")},
		SelectedInstruments: 1,
		LimitPrice:          rd("999"),
		Lot:                 1,
		EntryIntervalVolume: rd("1000000"),
		ExitIntervalVolume:  rd("1000000"),
		Q05OvernightAbs:     rd("0.05"),
	})
	if got.Lots != 0 || got.Reason != "min_order_notional" {
		t.Fatalf("unexpected min order gate: %+v", got)
	}
}

func TestSizerBindingLimits(t *testing.T) {
	sizer := NewSizer(SizingConfig{
		MaxPositionPct:             rd("0.10"),
		MaxTotalExposurePct:        rd("0.50"),
		MaxParticipationRate:       rd("0.01"),
		CashUsageBuffer:            rd("0.95"),
		RiskBudgetPerInstrumentPct: rd("0.005"),
		MinOrderNotionalRUB:        rd("1"),
	})
	tests := []struct {
		name  string
		input SizingInput
		want  decimal.Decimal
	}{
		{
			name: "cap",
			input: SizingInput{
				Portfolio:           domain.Portfolio{Equity: rd("100000"), Cash: rd("100000")},
				SelectedInstruments: 1,
				LimitPrice:          rd("100"),
				Lot:                 1,
				EntryIntervalVolume: rd("5000000"),
				ExitIntervalVolume:  rd("5000000"),
			},
			want: rd("10000"),
		},
		{
			name: "exposure",
			input: SizingInput{
				Portfolio:           domain.Portfolio{Equity: rd("100000"), Cash: rd("100000")},
				SelectedInstruments: 10,
				LimitPrice:          rd("100"),
				Lot:                 1,
				EntryIntervalVolume: rd("5000000"),
				ExitIntervalVolume:  rd("5000000"),
			},
			want: rd("5000"),
		},
		{
			name: "liquidity",
			input: SizingInput{
				Portfolio:           domain.Portfolio{Equity: rd("100000"), Cash: rd("100000")},
				SelectedInstruments: 1,
				LimitPrice:          rd("100"),
				Lot:                 1,
				EntryIntervalVolume: rd("300000"),
				ExitIntervalVolume:  rd("500000"),
			},
			want: rd("3000"),
		},
		{
			name: "risk",
			input: SizingInput{
				Portfolio:           domain.Portfolio{Equity: rd("100000"), Cash: rd("100000")},
				SelectedInstruments: 1,
				LimitPrice:          rd("100"),
				Lot:                 1,
				EntryIntervalVolume: rd("5000000"),
				ExitIntervalVolume:  rd("5000000"),
				Q05OvernightAbs:     rd("0.10"),
			},
			want: rd("5000"),
		},
		{
			name: "cash",
			input: SizingInput{
				Portfolio:           domain.Portfolio{Equity: rd("100000"), Cash: rd("2000")},
				SelectedInstruments: 1,
				LimitPrice:          rd("100"),
				Lot:                 1,
				EntryIntervalVolume: rd("5000000"),
				ExitIntervalVolume:  rd("5000000"),
			},
			want: rd("1900"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sizer.Size(tt.input)
			if !got.TargetNotional.Equal(tt.want) {
				t.Fatalf("target=%s, want %s limits=%v", got.TargetNotional, tt.want, got.Limits)
			}
		})
	}
}

func TestSizerAppliesSizeReductionFactor(t *testing.T) {
	sizer := NewSizer(SizingConfig{
		MaxPositionPct:             rd("1"),
		MaxTotalExposurePct:        rd("1"),
		MaxParticipationRate:       rd("1"),
		CashUsageBuffer:            rd("1"),
		RiskBudgetPerInstrumentPct: rd("1"),
		MinOrderNotionalRUB:        rd("1"),
	}).WithSizeFactor(rd("0.5"))
	got := sizer.Size(SizingInput{
		Portfolio:           domain.Portfolio{Equity: rd("10000"), Cash: rd("10000")},
		SelectedInstruments: 1,
		LimitPrice:          rd("100"),
		Lot:                 1,
		EntryIntervalVolume: rd("10000"),
		ExitIntervalVolume:  rd("10000"),
		Q05OvernightAbs:     rd("1"),
	})
	if got.Lots != 50 || !got.TargetNotional.Equal(rd("5000")) {
		t.Fatalf("unexpected reduced sizing: %+v", got)
	}
}

func TestSizerSubtractsExistingExposureAndReservedCash(t *testing.T) {
	sizer := NewSizer(SizingConfig{
		MaxPositionPct:             rd("1"),
		MaxTotalExposurePct:        rd("0.50"),
		MaxParticipationRate:       rd("1"),
		CashUsageBuffer:            rd("1"),
		RiskBudgetPerInstrumentPct: rd("1"),
		MinOrderNotionalRUB:        rd("1"),
	})
	got := sizer.Size(SizingInput{
		Portfolio:           domain.Portfolio{Equity: rd("100000"), Cash: rd("50000")},
		SelectedInstruments: 2,
		ExistingExposure:    rd("30000"),
		ReservedCash:        rd("10000"),
		LimitPrice:          rd("100"),
		Lot:                 1,
		EntryIntervalVolume: rd("1000000"),
		ExitIntervalVolume:  rd("1000000"),
		Q05OvernightAbs:     rd("1"),
	})
	if got.Lots != 100 || !got.TargetNotional.Equal(rd("10000")) {
		t.Fatalf("unexpected sizing with reserved exposure: %+v", got)
	}
}
