package risk

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"overnight-trading-bot/internal/domain"
)

func TestHaltPersistsStateBeforeRiskEvent(t *testing.T) {
	sink := &recordingHaltSink{}
	manager := NewManager(sink, ManagerConfig{})
	if err := manager.Halt(context.Background(), domain.ModeLiveTrade, "risk", "stop", "uid"); err != nil {
		t.Fatal(err)
	}
	if len(sink.calls) != 2 || sink.calls[0] != "state" || sink.calls[1] != "event" {
		t.Fatalf("calls=%v, want state before event", sink.calls)
	}
	if sink.state != domain.StateHalted || !sink.halted || sink.reason != "stop" {
		t.Fatalf("state=%s halted=%v reason=%q", sink.state, sink.halted, sink.reason)
	}
}

func TestHaltFailStopsWhenStatePersistFails(t *testing.T) {
	oldExit := exitProcess
	defer func() { exitProcess = oldExit }()
	exitCode := -1
	exitProcess = func(code int) {
		exitCode = code
		panic("exit")
	}
	sink := &recordingHaltSink{saveErr: errors.New("db down")}
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected fail-stop panic from exit hook")
		}
		if exitCode != 1 {
			t.Fatalf("exit code=%d, want 1", exitCode)
		}
		if sink.eventInserted {
			t.Fatal("risk event inserted before failed halt state persist")
		}
	}()
	_ = NewManager(sink, ManagerConfig{}).Halt(context.Background(), domain.ModeLiveTrade, "risk", "stop", "")
}

type recordingHaltSink struct {
	calls         []string
	saveErr       error
	eventInserted bool
	state         domain.SystemState
	halted        bool
	reason        string
}

func (s *recordingHaltSink) InsertRiskEvent(context.Context, domain.RiskEvent) error {
	s.calls = append(s.calls, "event")
	s.eventInserted = true
	return nil
}

func (s *recordingHaltSink) SaveSystemState(_ context.Context, state domain.SystemState, _ domain.Mode, halted bool, reason string, _ string) error {
	s.calls = append(s.calls, "state")
	if s.saveErr != nil {
		return s.saveErr
	}
	s.state = state
	s.halted = halted
	s.reason = reason
	return nil
}

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

func TestPreTradeRejectsServerClockDrift(t *testing.T) {
	manager := NewManager(nil, ManagerConfig{})
	input := PreTradeInput{
		Portfolio:        domain.Portfolio{Equity: decimal.NewFromInt(1000)},
		TradingStatus:    domain.TradingStatusNormal,
		ServerClockDrift: 3 * time.Second,
		MaxClockDrift:    2 * time.Second,
	}
	result := manager.PreTradeCheck(input)
	if result.Allowed || result.Reason != "server_clock_drift_too_high" {
		t.Fatalf("result=%+v, want server_clock_drift_too_high reject", result)
	}
}

func TestPreTradeRejectsUnavailableServerTime(t *testing.T) {
	manager := NewManager(nil, ManagerConfig{})
	input := PreTradeInput{
		Portfolio:             domain.Portfolio{Equity: decimal.NewFromInt(1000)},
		TradingStatus:         domain.TradingStatusNormal,
		ServerTimeUnavailable: true,
	}
	result := manager.PreTradeCheck(input)
	if result.Allowed || result.Reason != "server_time_unavailable" {
		t.Fatalf("result=%+v, want server_time_unavailable reject", result)
	}
}
