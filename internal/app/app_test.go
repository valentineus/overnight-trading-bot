package app

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func TestRunRequiresConfigPath(t *testing.T) {
	err := Run(context.Background(), Options{})
	if err == nil {
		t.Fatal("expected error")
	}

	if !strings.Contains(err.Error(), "config path is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunReportsMissingConfig(t *testing.T) {
	err := Run(context.Background(), Options{ConfigPath: "missing.yaml"})
	if err == nil {
		t.Fatal("expected error")
	}

	if !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunUsesExistingConfig(t *testing.T) {
	touch := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(touch, []byte("instruments: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	err := Run(context.Background(), Options{
		ConfigPath: touch,
		Stdout:     &stdout,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(stdout.String(), "initialized") {
		t.Fatalf("unexpected stdout: %q", stdout.String())
	}
}
