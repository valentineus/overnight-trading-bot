package instruments

import (
	"context"
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/testutil"
	"overnight-trading-bot/internal/tinvest"
)

func TestSyncMetadataFailsWhenEnabledInstrumentCannotBeLoaded(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	gateway := tinvest.NewFakeGateway()
	instrument := domain.Instrument{
		InstrumentUID:     "uid",
		Ticker:            "TRUR",
		ClassCode:         "TQTF",
		Lot:               1,
		MinPriceIncrement: decimal.NewFromInt(1),
		Currency:          "RUB",
		Enabled:           true,
	}
	if err := repo.UpsertInstrument(ctx, instrument); err != nil {
		t.Fatal(err)
	}
	gateway.Instruments["uid"] = instrument
	gateway.InstrumentErrors["uid"] = errors.New("metadata unavailable")

	err := NewRegistry(repo, gateway).SyncMetadata(ctx)
	if err == nil {
		t.Fatal("expected sync metadata error")
	}
}
