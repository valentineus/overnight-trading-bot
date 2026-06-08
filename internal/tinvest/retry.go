package tinvest

import (
	"context"
	"time"

	backofflib "github.com/cenkalti/backoff/v4"
)

func withRetry(ctx context.Context, attempts int, interval time.Duration, fn func() error) error {
	if attempts <= 0 {
		attempts = 1
	}
	if interval < 0 {
		interval = 0
	}
	policy := backofflib.NewExponentialBackOff()
	policy.InitialInterval = interval
	policy.MaxInterval = interval * 8
	policy.Multiplier = 2
	policy.MaxElapsedTime = 0
	policy.Reset()
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(); err != nil {
			lastErr = err
		} else {
			return nil
		}
		if attempt == attempts-1 || interval <= 0 {
			continue
		}
		timer := time.NewTimer(policy.NextBackOff())
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		}
	}
	return lastErr
}

func retryValue[T any](ctx context.Context, attempts int, interval time.Duration, fn func() (T, error)) (T, error) {
	var out T
	err := withRetry(ctx, attempts, interval, func() error {
		var err error
		out, err = fn()
		return err
	})
	if err != nil {
		var zero T
		return zero, err
	}
	return out, nil
}

func requestWithTimeout[T any](ctx context.Context, timeout time.Duration, fn func(context.Context) (T, error)) (T, error) {
	if timeout <= 0 {
		return fn(ctx)
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return fn(callCtx)
}
