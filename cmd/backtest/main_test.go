package main

import (
	"strings"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/backtest"
	"overnight-trading-bot/internal/domain"
)

func TestValidateMetadataRejectsMissingLotOrTick(t *testing.T) {
	candles := map[string][]domain.Candle{
		"uid": {{InstrumentUID: "uid", TradeDate: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)}},
	}
	err := validateMetadata(candles, map[string]backtest.InstrumentMetadata{
		"uid": {Lot: 10},
	})
	if err == nil || !strings.Contains(err.Error(), "missing lot/min_price_increment metadata") {
		t.Fatalf("err=%v, want missing metadata error", err)
	}
}

func TestValidateMetadataAcceptsCompleteMetadata(t *testing.T) {
	candles := map[string][]domain.Candle{
		"uid": {{InstrumentUID: "uid", TradeDate: time.Date(2026, 6, 8, 0, 0, 0, 0, time.UTC)}},
	}
	err := validateMetadata(candles, map[string]backtest.InstrumentMetadata{
		"uid": {Lot: 10, MinPriceIncrement: decimal.RequireFromString("0.01")},
	})
	if err != nil {
		t.Fatal(err)
	}
}
