package app

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunRequiresAppMode(t *testing.T) {
	t.Setenv("APP_MODE", "")
	err := Run(context.Background(), Options{RunOnce: true})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "APP_MODE") && !strings.Contains(err.Error(), "MODE") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunBacktestModeWithoutDB(t *testing.T) {
	t.Setenv("APP_MODE", "backtest")
	var stdout bytes.Buffer
	err := Run(context.Background(), Options{Stdout: &stdout, RunOnce: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), "backtest") {
		t.Fatalf("unexpected stdout: %s", stdout.String())
	}
}
