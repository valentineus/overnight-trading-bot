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

func TestSyncMetadataSkipsInstrumentWhenRemoteMetadataFails(t *testing.T) {
	ctx := context.Background()
	repo := testutil.NewMemoryRepository()
	gateway := tinvest.NewFakeGateway()
	bad := domain.Instrument{
		InstrumentUID:     "uid",
		Ticker:            "TRUR",
		ClassCode:         "TQTF",
		Lot:               1,
		MinPriceIncrement: decimal.NewFromInt(1),
		Currency:          "RUB",
		Enabled:           true,
	}
	good := domain.Instrument{
		InstrumentUID:     "good-local",
		Ticker:            "TGLD",
		ClassCode:         "TQTF",
		Lot:               1,
		MinPriceIncrement: decimal.NewFromInt(1),
		Currency:          "RUB",
		Enabled:           true,
	}
	remoteGood := good
	remoteGood.InstrumentUID = "good-remote"
	remoteGood.Name = "remote metadata"
	if err := repo.UpsertInstrument(ctx, bad); err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertInstrument(ctx, good); err != nil {
		t.Fatal(err)
	}
	gateway.Instruments["uid"] = bad
	gateway.Instruments["good-remote"] = remoteGood
	gateway.InstrumentErrors["uid"] = errors.New("metadata unavailable")

	err := NewRegistry(repo, gateway).SyncMetadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(repo.RiskEvents) != 1 || repo.RiskEvents[0].EventType != "instrument_metadata_sync_failed" {
		t.Fatalf("risk events=%+v, want one metadata sync failure", repo.RiskEvents)
	}
	instruments, err := repo.ListInstruments(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	foundRemote := false
	for _, instrument := range instruments {
		if instrument.InstrumentUID == "good-remote" && instrument.Name == "remote metadata" {
			foundRemote = true
		}
	}
	if !foundRemote {
		t.Fatalf("instruments=%+v, want successful instruments to keep syncing", instruments)
	}
}
