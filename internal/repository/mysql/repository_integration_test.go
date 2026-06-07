//go:build integration

package mysql

import (
	"context"
	"testing"
	"time"

	"github.com/jmoiron/sqlx"
	"github.com/shopspring/decimal"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/mariadb"

	"overnight-trading-bot/internal/domain"
)

func TestRepositoryMariaDBMigrationsAndRoundTrip(t *testing.T) {
	ctx := context.Background()
	container, err := mariadb.Run(ctx,
		"mariadb:11.4",
		mariadb.WithDatabase("overnight_bot"),
		mariadb.WithUsername("bot"),
		mariadb.WithPassword("bot"),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := testcontainers.TerminateContainer(container); err != nil {
			t.Logf("terminate mariadb: %v", err)
		}
	})
	dsn, err := container.ConnectionString(ctx, "parseTime=true", "loc=UTC", "multiStatements=true")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sqlx.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(ctx, db.DB); err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(db)
	instrument := domain.Instrument{
		InstrumentUID:     "uid-trur",
		Ticker:            "TRUR",
		ClassCode:         "TQTF",
		Name:              "TRUR",
		Lot:               1,
		MinPriceIncrement: decimal.NewFromFloat(0.0001),
		Currency:          "RUB",
		Enabled:           true,
	}
	if err := repo.ReplaceInstrument(ctx, "PENDING:TRUR", instrument); err != nil {
		t.Fatal(err)
	}
	tradeDate := time.Date(2026, 6, 7, 0, 0, 0, 0, time.UTC)
	position := domain.Position{
		AccountIDHash: "hash",
		InstrumentUID: "uid-trur",
		OpenTradeDate: tradeDate,
		Lots:          10,
		AvgBuyPrice:   decimal.NewFromInt(100),
		Status:        domain.PositionHoldingOvernight,
	}
	if err := repo.UpsertPosition(ctx, position); err != nil {
		t.Fatal(err)
	}
	position.Lots = 8
	position.ExitFilledLots = 2
	if err := repo.UpsertPosition(ctx, position); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.GetContext(ctx, &count, `
SELECT COUNT(*) FROM positions WHERE account_id_hash='hash' AND instrument_uid='uid-trur' AND open_trade_date=?`, tradeDate); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("positions count=%d, want 1", count)
	}
	if err := repo.MarkDailyReportSent(ctx, tradeDate, "hash"); err != nil {
		t.Fatal(err)
	}
	sent, err := repo.WasDailyReportSent(ctx, tradeDate, "hash")
	if err != nil {
		t.Fatal(err)
	}
	if !sent {
		t.Fatalf("daily report marker was not persisted")
	}
	if err := repo.UpsertOrder(ctx, domain.Order{
		ClientOrderID: "bad",
		AccountIDHash: "hash",
		InstrumentUID: "missing",
		TradeDate:     tradeDate,
		Side:          domain.SideBuy,
		OrderType:     domain.OrderTypeLimit,
		LimitPrice:    decimal.NewFromInt(100),
		QuantityLots:  1,
		Status:        domain.OrderStatusSent,
		RawStateJSON:  "{}",
	}); err == nil {
		t.Fatalf("expected FK failure for missing instrument")
	}
}
