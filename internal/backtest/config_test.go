package backtest

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestRequireZeroCommissionDefaultDoesNotOverrideExplicitFalse(t *testing.T) {
	defaultEngine := New(Config{})
	if !defaultEngine.requireZeroCommission() {
		t.Fatal("default require_zero_commission should be true")
	}
	requireZero := false
	explicitEngine := New(Config{RequireZeroCommission: &requireZero})
	if explicitEngine.requireZeroCommission() {
		t.Fatal("explicit require_zero_commission=false was overridden")
	}
}

func TestAssumedSpreadUsesFundTypeSpecificDefaults(t *testing.T) {
	engine := New(Config{
		AssumedSpreadBps: decimal.NewFromInt(20),
		InstrumentFundTypes: map[string]string{
			"mm": "money_market",
			"eq": "equity",
		},
	})
	if got := engine.assumedSpreadBps("mm"); !got.Equal(decimal.NewFromInt(5)) {
		t.Fatalf("money market spread=%s, want 5", got)
	}
	if got := engine.assumedSpreadBps("eq"); !got.Equal(decimal.NewFromInt(25)) {
		t.Fatalf("equity spread=%s, want 25", got)
	}
	if got := engine.assumedSpreadBps("unknown"); !got.Equal(decimal.NewFromInt(20)) {
		t.Fatalf("default spread=%s, want 20", got)
	}
}
