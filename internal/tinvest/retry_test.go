package tinvest

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWithRetryRetriesUntilSuccess(t *testing.T) {
	attempts := 0
	err := withRetry(context.Background(), 3, 0, func() error {
		attempts++
		if attempts < 3 {
			return errors.New("temporary")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if attempts != 3 {
		t.Fatalf("attempts=%d, want 3", attempts)
	}
}

func TestWithRetryStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	attempts := 0
	err := withRetry(ctx, 3, time.Millisecond, func() error {
		attempts++
		return errors.New("temporary")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v, want context.Canceled", err)
	}
	if attempts != 0 {
		t.Fatalf("attempts=%d, want 0", attempts)
	}
}
