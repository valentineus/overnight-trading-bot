package risk

import (
	"testing"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
)

func TestPreTradeClosingPositionBypassesOpenPositionLimit(t *testing.T) {
	manager := NewManager(nil, ManagerConfig{MaxOpenPositions: 1})
	input := PreTradeInput{
		Portfolio:       domain.Portfolio{Equity: decimal.NewFromInt(1000)},
		OpenPositions:   1,
		TradingStatus:   domain.TradingStatusNormal,
		ClosingPosition: true,
	}
	result := manager.PreTradeCheck(input)
	if !result.Allowed {
		t.Fatalf("closing position rejected: %s", result.Reason)
	}
	input.ClosingPosition = false
	result = manager.PreTradeCheck(input)
	if result.Allowed || result.Reason != "max_open_positions" {
		t.Fatalf("entry result=%+v, want max_open_positions reject", result)
	}
}
