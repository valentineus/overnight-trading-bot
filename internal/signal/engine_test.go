package signal

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
)

func sd(raw string) decimal.Decimal {
	v, err := decimal.NewFromString(raw)
	if err != nil {
		panic(err)
	}
	return v
}

func baseCandidate() Candidate {
	return Candidate{
		TradeDate: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
		Instrument: domain.Instrument{
			InstrumentUID:     "uid",
			Ticker:            "TRUR",
			ClassCode:         "TQTF",
			Lot:               1,
			MinPriceIncrement: sd("0.01"),
			Currency:          "RUB",
			Enabled:           true,
		},
		Features: domain.FeatureSet{
			MuOn60:     sd("0.002"),
			MuOn252:    sd("0.001"),
			SigmaOn60:  sd("0.01"),
			TStatOn60:  sd("2"),
			WinOn60:    sd("0.60"),
			NetEdgeBps: sd("20"),
			SpreadBps:  sd("5"),
			TickBps:    sd("1"),
			ADV20:      sd("10000000"),
		},
		TradingStatus: domain.TradingStatusNormal,
		FreeOrderOK:   true,
	}
}

func TestEngineEnter(t *testing.T) {
	engine := New(Config{
		MinTStat60:              sd("1.25"),
		MinWinRate60:            sd("0.55"),
		MinNetEdgeBps:           sd("10"),
		MinADVRUB:               sd("5000000"),
		MaxSpreadBpsDefault:     sd("20"),
		MaxSpreadBpsMoneyMarket: sd("5"),
		MaxSpreadBpsBondFunds:   sd("10"),
		MaxSpreadBpsEquityFunds: sd("25"),
		MaxTickBps:              sd("10"),
		RequireZeroCommission:   true,
		MaxPositions:            5,
	})
	sig := engine.Evaluate(baseCandidate())
	if sig.Decision != domain.DecisionEnter || sig.RejectReason != "" {
		t.Fatalf("unexpected signal: %+v", sig)
	}
}

func TestEngineFirstRejectReason(t *testing.T) {
	engine := New(Config{MinTStat60: sd("1.25"), MinWinRate60: sd("0.55"), MinNetEdgeBps: sd("10"), MinADVRUB: sd("5000000"), MaxSpreadBpsDefault: sd("20"), MaxTickBps: sd("10"), RequireZeroCommission: true})
	c := baseCandidate()
	c.Features.MuOn60 = decimal.Zero
	c.Features.NetEdgeBps = decimal.Zero
	sig := engine.Evaluate(c)
	if sig.RejectReason != ReasonMuShort {
		t.Fatalf("reason=%s", sig.RejectReason)
	}
}

func TestEngineUsesSkipForCapacityReasons(t *testing.T) {
	engine := New(Config{MinTStat60: sd("1.25"), MinWinRate60: sd("0.55"), MinNetEdgeBps: sd("10"), MinADVRUB: sd("5000000"), MaxSpreadBpsDefault: sd("20"), MaxTickBps: sd("10"), RequireZeroCommission: true, MaxPositions: 1})
	c := baseCandidate()
	c.OpenPositions = 1
	sig := engine.Evaluate(c)
	if sig.Decision != domain.DecisionSkip || sig.RejectReason != ReasonMaxPositions {
		t.Fatalf("unexpected skip signal: %+v", sig)
	}
}
