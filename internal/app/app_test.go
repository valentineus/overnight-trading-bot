package app

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/testutil"
	"overnight-trading-bot/internal/tinvest"
)

func TestRunRequiresAppMode(t *testing.T) {
	t.Setenv("APP_MODE", "")
	err := Run(context.Background(), Options{RunOnce: true})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "APP_MODE") && !strings.Contains(err.Error(), "MODE") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunBacktestModeWithoutDB(t *testing.T) {
	t.Setenv("APP_MODE", "backtest")
	var stdout bytes.Buffer
	err := Run(context.Background(), Options{Stdout: &stdout, RunOnce: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "backtest") {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
}

func TestHealthURLDefaultsToReadyEndpoint(t *testing.T) {
	if got := HealthURL(":3300"); got != "http://127.0.0.1:3300/ready" {
		t.Fatalf("HealthURL(:3300)=%s", got)
	}
	if got := HealthURL("127.0.0.1:3301"); got != "http://127.0.0.1:3301/ready" {
		t.Fatalf("HealthURL(host)=%s", got)
	}
}

func TestSeedPaperGatewayMakesSeedInstrumentsDiscoverable(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	if err := repo.UpsertInstrument(ctx, domain.Instrument{
		InstrumentUID:     "PENDING:TRUR",
		Ticker:            "TRUR",
		ClassCode:         "TQTF",
		Name:              "TRUR",
		Lot:               1,
		MinPriceIncrement: decimal.RequireFromString("0.0001"),
		Currency:          "RUB",
		Enabled:           true,
	}); err != nil {
		t.Fatal(err)
	}
	gateway := tinvest.NewFakeGateway()
	if err := seedPaperGateway(ctx, repo, gateway); err != nil {
		t.Fatal(err)
	}
	instrument, err := gateway.GetInstrument(ctx, "TRUR", "TQTF")
	if err != nil {
		t.Fatal(err)
	}
	if !instrument.MetadataValid() || strings.HasPrefix(instrument.InstrumentUID, "PENDING:") {
		t.Fatalf("instrument was not made runnable for paper: %+v", instrument)
	}
}
