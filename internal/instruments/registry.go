package instruments

import (
	"context"
	"fmt"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/repository"
	"overnight-trading-bot/internal/tinvest"
)

type Registry struct {
	repo    repository.Repository
	gateway tinvest.Gateway
}

func NewRegistry(repo repository.Repository, gateway tinvest.Gateway) Registry {
	return Registry{repo: repo, gateway: gateway}
}

func (r Registry) SyncMetadata(ctx context.Context) error {
	instruments, err := r.repo.ListInstruments(ctx, true)
	if err != nil {
		return err
	}
	for _, instrument := range instruments {
		if !instrument.Enabled || instrument.Quarantine {
			continue
		}
		remote, err := r.gateway.GetInstrument(ctx, instrument.Ticker, instrument.ClassCode)
		if err != nil {
			return fmt.Errorf("sync instrument metadata %s: %w", instrument.Ticker, err)
		}
		remote.Enabled = instrument.Enabled && remote.Enabled
		remote.FundType = instrument.FundType
		remote.ExpectedCommissionBpsPerSide = instrument.ExpectedCommissionBpsPerSide
		remote.FreeOrderLimitPerDay = instrument.FreeOrderLimitPerDay
		remote.Quarantine = instrument.Quarantine
		remote.QuarantineReason = instrument.QuarantineReason
		remote.ExcludeReason = instrument.ExcludeReason
		if err := r.repo.ReplaceInstrument(ctx, instrument.InstrumentUID, remote); err != nil {
			return fmt.Errorf("replace synced instrument %s: %w", instrument.Ticker, err)
		}
	}
	return nil
}

func CheckInstrument(instrument domain.Instrument, status domain.TradingStatus) error {
	switch {
	case !instrument.Enabled:
		return fmt.Errorf("%s disabled", instrument.Ticker)
	case instrument.Quarantine:
		return fmt.Errorf("%s quarantined: %s", instrument.Ticker, instrument.QuarantineReason)
	case !instrument.MetadataValid():
		return fmt.Errorf("%s invalid metadata", instrument.Ticker)
	case status != domain.TradingStatusNormal:
		return fmt.Errorf("%s trading status %s", instrument.Ticker, status)
	default:
		return nil
	}
}
