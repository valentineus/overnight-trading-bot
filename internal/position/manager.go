package position

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/money"
	"overnight-trading-bot/internal/repository"
)

var ErrExitFillExceedsPositionLots = errors.New("exit fill exceeds local position lots")

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
	fillLots := order.FilledLots
	if fillLots < 0 {
		fillLots = 0
	}
	fillPrice := order.AvgFillPrice
	if !fillPrice.IsPositive() {
		fillPrice = order.LimitPrice
	}
	if existing, ok, err := m.findEntryPosition(ctx, accountIDHash, order); err != nil {
		return domain.Position{}, err
	} else if ok {
		previousLots := existing.Lots
		totalLots := previousLots + fillLots
		if fillLots > 0 && totalLots > 0 {
			previousValue := existing.AvgBuyPrice.Mul(decimal.NewFromInt(previousLots))
			fillValue := fillPrice.Mul(decimal.NewFromInt(fillLots))
			existing.AvgBuyPrice = previousValue.Add(fillValue).Div(decimal.NewFromInt(totalLots))
		}
		existing.Lots = totalLots
		existing.Lot = lot
		existing.CommissionTotal = existing.CommissionTotal.Add(order.Commission)
		if existing.OpenedAt == nil {
			existing.OpenedAt = &now
		}
		if order.FilledLots < order.QuantityLots {
			existing.Status = domain.PositionEntryPartiallyFilled
		} else if existing.Status != domain.PositionHoldingOvernight {
			existing.Status = domain.PositionEntryFilled
		}
		existing.UpdatedAt = now
		if err := m.repo.UpsertPosition(ctx, existing); err != nil {
			return domain.Position{}, err
		}
		return existing, nil
	}
	pos := domain.Position{
		AccountIDHash:   accountIDHash,
		InstrumentUID:   order.InstrumentUID,
		OpenTradeDate:   order.TradeDate,
		Lots:            fillLots,
		Lot:             lot,
		AvgBuyPrice:     fillPrice,
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

func (m Manager) findEntryPosition(ctx context.Context, accountIDHash string, order domain.Order) (domain.Position, bool, error) {
	positions, err := m.repo.ListPositions(ctx, accountIDHash, order.TradeDate, order.TradeDate)
	if err != nil {
		return domain.Position{}, false, err
	}
	for _, pos := range positions {
		if pos.InstrumentUID != order.InstrumentUID {
			continue
		}
		switch pos.Status {
		case domain.PositionEntrySignalled,
			domain.PositionEntryOrderSent,
			domain.PositionEntryPartiallyFilled,
			domain.PositionEntryFilled,
			domain.PositionHoldingOvernight:
			return pos, true, nil
		default:
		}
	}
	return domain.Position{}, false, nil
}

func (m Manager) OnExitFill(ctx context.Context, pos domain.Position, exitOrder domain.Order) (domain.Position, error) {
	now := time.Now().UTC()
	lot := pos.Lot
	if lot <= 0 {
		lot = 1
	}
	if exitOrder.FilledLots > pos.Lots {
		err := fmt.Errorf("%w: filled_lots=%d position_lots=%d instrument_uid=%s", ErrExitFillExceedsPositionLots, exitOrder.FilledLots, pos.Lots, pos.InstrumentUID)
		if m.repo != nil {
			_ = m.repo.InsertRiskEvent(ctx, domain.RiskEvent{
				TS:            now,
				Severity:      domain.SeverityCritical,
				EventType:     "exit_overfill",
				InstrumentUID: pos.InstrumentUID,
				Message:       err.Error(),
				ContextJSON:   fmt.Sprintf(`{"filled_lots":%d,"position_lots":%d}`, exitOrder.FilledLots, pos.Lots),
			})
		}
		return pos, err
	}
	executedLots := exitOrder.FilledLots
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
