package config

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/timeutil"
)

func TestValidateRequiresAccountIDForBrokerModes(t *testing.T) {
	cfg := minimalBrokerConfig(domain.ModeSandbox)
	cfg.TInvest.AccountID = ""
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "TINVEST_ACCOUNT_ID") {
		t.Fatalf("Validate err=%v, want TINVEST_ACCOUNT_ID requirement", err)
	}
}

func TestValidateAllowsCancelCountsFreeOrderPolicy(t *testing.T) {
	cfg := minimalBrokerConfig(domain.ModeSandbox)
	cfg.Commission.FreeOrderCountPolicy = "cancel_counts"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate cancel_counts: %v", err)
	}
}

func minimalBrokerConfig(mode domain.Mode) Config {
	return Config{
		App: AppConfig{
			Mode:               mode,
			Timezone:           "Europe/Moscow",
			ShutdownTimeoutSec: 30,
		},
		TInvest: TInvestConfig{
			Token:             "token",
			AccountID:         "account",
			RequestTimeoutSec: 10,
		},
		DB: DBConfig{DSN: "user:pass@tcp(localhost:3306)/bot"},
		Execution: ExecutionConfig{
			EntrySignalTime:     mustTOD("18:10:00"),
			EntryWindowStart:    mustTOD("18:20:00"),
			EntryWindowEnd:      mustTOD("18:38:30"),
			NoNewEntryAfter:     mustTOD("18:38:30"),
			ExitWatchStart:      mustTOD("09:50:00"),
			ExitNotBefore:       mustTOD("10:03:00"),
			ExitWindowStart:     mustTOD("10:05:00"),
			ExitWindowEnd:       mustTOD("10:25:00"),
			HardExitDeadline:    mustTOD("10:45:00"),
			QuoteDepth:          20,
			OrderPollIntervalMS: 500,
		},
		Strategy: StrategyConfig{AllocationMethod: "equal_weight"},
		Risk: RiskConfig{
			APIOutageHaltSec:          180,
			ReconciliationWindowHours: 72,
			ReconciliationSkewSec:     10,
			CommissionToleranceRUB:    decimal.NewFromFloat(0.01),
		},
		Commission: CommissionConfig{FreeOrderCountPolicy: "submitted"},
	}
}

func mustTOD(raw string) timeutil.TimeOfDay {
	tod, err := timeutil.ParseTimeOfDay(raw)
	if err != nil {
		panic(err)
	}
	return tod
}
