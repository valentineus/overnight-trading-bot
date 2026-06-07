package statemachine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"overnight-trading-bot/internal/domain"
	"overnight-trading-bot/internal/reconciliation"
	"overnight-trading-bot/internal/repository"
)

var (
	ErrIllegalTransition = errors.New("illegal system transition")
	ErrSystemHalted      = errors.New("system is halted")
)

type System struct {
	repo repository.Repository
	mode domain.Mode
}

func New(repo repository.Repository, mode domain.Mode) System {
	return System{repo: repo, mode: mode}
}

func (s System) Recover(ctx context.Context, reconcile reconciliation.Engine) (domain.SystemState, error) {
	state, halted, reason, err := s.repo.GetSystemState(ctx)
	if err != nil {
		return "", err
	}
	if halted || state == domain.StateHalted {
		return domain.StateHalted, fmt.Errorf("system halted: %s", reason)
	}
	switch state {
	case domain.StatePlaceEntryOrders, domain.StateMonitorEntryOrders,
		domain.StatePlaceExitOrders, domain.StateMonitorExitOrders,
		domain.StateHoldOvernight:
		diffs, err := reconcile.Run(ctx)
		if err != nil {
			return "", err
		}
		if reconciliation.HasCritical(diffs) {
			if err := s.Halt(ctx, "critical reconciliation diff during recovery"); err != nil {
				return "", err
			}
			return domain.StateHalted, errors.New("critical reconciliation diff during recovery")
		}
		return state, nil
	case domain.StateInit, domain.StateSyncInstruments, domain.StateSyncMarketData, domain.StateGenerateSignals:
		return domain.StateInit, s.persist(ctx, domain.StateInit, false, "")
	default:
		return state, nil
	}
}

func (s System) Transition(ctx context.Context, from, to domain.SystemState) error {
	current, halted, reason, err := s.repo.GetSystemState(ctx)
	if err != nil {
		return err
	}
	if (halted || current == domain.StateHalted) && to != domain.StateHalted {
		return fmt.Errorf("%w: %s", ErrSystemHalted, reason)
	}
	if !legalTransition(from, to) {
		return fmt.Errorf("%w: %s -> %s", ErrIllegalTransition, from, to)
	}
	return s.persist(ctx, to, false, "")
}

func (s System) Halt(ctx context.Context, reason string) error {
	return s.persist(ctx, domain.StateHalted, true, reason)
}

func (s System) Heartbeat(ctx context.Context, state domain.SystemState) error {
	current, halted, reason, err := s.repo.GetSystemState(ctx)
	if err != nil {
		return err
	}
	if halted || current == domain.StateHalted {
		return s.repo.SaveSystemState(ctx, domain.StateHalted, s.mode, true, reason, fmt.Sprintf(`{"heartbeat":"%s"}`, time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return s.repo.SaveSystemState(ctx, state, s.mode, false, "", fmt.Sprintf(`{"heartbeat":"%s"}`, time.Now().UTC().Format(time.RFC3339Nano)))
}

func (s System) persist(ctx context.Context, state domain.SystemState, halted bool, reason string) error {
	return s.repo.SaveSystemState(ctx, state, s.mode, halted, reason, "{}")
}

func legalTransition(from, to domain.SystemState) bool {
	if from == to {
		return true
	}
	if to == domain.StateHalted {
		return true
	}
	allowed := map[domain.SystemState][]domain.SystemState{
		domain.StateInit:               {domain.StateSyncInstruments, domain.StateWaitExitWindow},
		domain.StateSyncInstruments:    {domain.StateSyncMarketData},
		domain.StateSyncMarketData:     {domain.StateGenerateSignals},
		domain.StateGenerateSignals:    {domain.StateWaitEntryWindow},
		domain.StateWaitEntryWindow:    {domain.StatePlaceEntryOrders, domain.StateSleep},
		domain.StatePlaceEntryOrders:   {domain.StateMonitorEntryOrders, domain.StateReconcile},
		domain.StateMonitorEntryOrders: {domain.StateHoldOvernight, domain.StateReconcile},
		domain.StateHoldOvernight:      {domain.StateWaitExitWindow},
		domain.StateWaitExitWindow:     {domain.StatePlaceExitOrders},
		domain.StatePlaceExitOrders:    {domain.StateMonitorExitOrders, domain.StateReconcile},
		domain.StateMonitorExitOrders:  {domain.StateReconcile},
		domain.StateReconcile:          {domain.StateReport, domain.StateHalted},
		domain.StateReport:             {domain.StateSleep},
		domain.StateSleep:              {domain.StateInit, domain.StateWaitExitWindow, domain.StateGenerateSignals},
	}
	for _, candidate := range allowed[from] {
		if candidate == to {
			return true
		}
	}
	return false
}
