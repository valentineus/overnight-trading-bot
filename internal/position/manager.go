package position

import (
	"context"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/money"
	"overnight-trading-bot/internal/repository"
)

type Manager struct {
	repo repository.Repository
}

func NewManager(repo repository.Repository) Manager {
	return Manager{repo: repo}
}

func (m Manager) OnEntryFill(ctx context.Context, accountIDHash string, instrument domain.Instrument, order domain.Order) (domain.Position, error) {
	now := time.Now().UTC()
	lot := instrument.Lot
	if lot <= 0 {
		lot = 1
	}
	pos := domain.Position{
		AccountIDHash:   accountIDHash,
		InstrumentUID:   order.InstrumentUID,
		OpenTradeDate:   order.TradeDate,
		Lots:            order.FilledLots,
		Lot:             lot,
		AvgBuyPrice:     order.AvgFillPrice,
		CommissionTotal: order.Commission,
		Status:          domain.PositionEntryFilled,
		OpenedAt:        &now,
		UpdatedAt:       now,
	}
	if pos.Lots < order.QuantityLots {
		pos.Status = domain.PositionEntryPartiallyFilled
	}
	if err := m.repo.UpsertPosition(ctx, pos); err != nil {
		return domain.Position{}, err
	}
	return pos, nil
}

func (m Manager) OnExitFill(ctx context.Context, pos domain.Position, exitOrder domain.Order) (domain.Position, error) {
	now := time.Now().UTC()
	lot := pos.Lot
	if lot <= 0 {
		lot = 1
	}
	executedLots := min(exitOrder.FilledLots, pos.Lots)
	if executedLots < 0 {
		executedLots = 0
	}
	previousExitLots := pos.ExitFilledLots
	pos.ExitFilledLots += executedLots
	if executedLots > 0 {
		previousValue := pos.AvgSellPrice.Mul(decimal.NewFromInt(previousExitLots))
		newValue := exitOrder.AvgFillPrice.Mul(decimal.NewFromInt(executedLots))
		pos.AvgSellPrice = previousValue.Add(newValue).Div(decimal.NewFromInt(pos.ExitFilledLots))
	}
	pos.CommissionTotal = pos.CommissionTotal.Add(exitOrder.Commission)
	executedUnits := decimal.NewFromInt(executedLots).Mul(decimal.NewFromInt(lot))
	pos.GrossPnL = pos.GrossPnL.Add(exitOrder.AvgFillPrice.Sub(pos.AvgBuyPrice).Mul(executedUnits))
	pos.NetPnL = pos.GrossPnL.Sub(pos.CommissionTotal)
	if pos.AvgBuyPrice.IsPositive() {
		baseLots := pos.ExitFilledLots
		if baseLots <= 0 {
			baseLots = pos.Lots
		}
		base := pos.AvgBuyPrice.Mul(decimal.NewFromInt(baseLots)).Mul(decimal.NewFromInt(lot))
		edge, _ := money.Bps(pos.NetPnL, base)
		pos.RealizedEdgeBps = edge
	}
	pos.Status = domain.PositionExitFilled
	if executedLots < pos.Lots {
		pos.Lots -= executedLots
		pos.Status = domain.PositionExitPartiallyFilled
		pos.ClosedAt = nil
	} else {
		pos.Lots = 0
		pos.ClosedAt = &now
	}
	pos.UpdatedAt = now
	if err := m.repo.UpsertPosition(ctx, pos); err != nil {
		return domain.Position{}, err
	}
	return pos, nil
}
