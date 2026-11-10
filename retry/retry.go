// Package retry runs an operation again on transient failures, with
// exponential backoff and full jitter.
package retry

import (
	"context"
	"math/rand/v2"
	"time"
)

// Policy controls how an operation is retried.
type Policy struct {
	MaxAttempts int           // total attempts, including the first
	BaseDelay   time.Duration // delay before the second attempt, before jitter
	MaxDelay    time.Duration // upper bound for any single delay

	// Retryable reports whether an error is worth another attempt.
	// When nil, every error is retried.
	Retryable func(error) bool

	// WaitHint, if set, can return how long the server asked us to wait
	// (for example from a Retry-After header). It replaces the backoff for
	// that attempt, capped at MaxHint; a longer hint ends the retries.
	WaitHint func(error) (time.Duration, bool)
	MaxHint  time.Duration

	// OnRetry is called before each wait, for logging and metrics.
	OnRetry func(attempt int, err error, wait time.Duration)

	// Hooks for tests.
	rand  func() float64
	sleep func(context.Context, time.Duration) error
}

// Default is a sensible policy for model API calls.
func Default(retryable func(error) bool) Policy {
	return Policy{MaxAttempts: 4, BaseDelay: 500 * time.Millisecond, MaxDelay: 8 * time.Second, Retryable: retryable}
}

// Do calls fn until it succeeds, returns a non-retryable error, runs out of
// attempts, or ctx is done. It returns the last error.
func Do(ctx context.Context, p Policy, fn func(context.Context) error) error {
	if p.MaxAttempts < 1 {
		p.MaxAttempts = 1
	}
	rnd := p.rand
	if rnd == nil {
		rnd = rand.Float64
	}
	sleep := p.sleep
	if sleep == nil {
		sleep = sleepCtx
	}

	var err error
	for attempt := 1; ; attempt++ {
		if err = fn(ctx); err == nil {
			return nil
		}
		if attempt >= p.MaxAttempts || ctx.Err() != nil {
			return err
		}
		if p.Retryable != nil && !p.Retryable(err) {
			return err
		}
		wait := p.backoff(attempt, rnd)
		if p.WaitHint != nil {
			if hint, ok := p.WaitHint(err); ok {
				if p.MaxHint > 0 && hint > p.MaxHint {
					return err // e.g. a daily quota: retrying now is pointless
				}
				wait = hint
			}
		}
		if p.OnRetry != nil {
			p.OnRetry(attempt, err, wait)
		}
		if serr := sleep(ctx, wait); serr != nil {
			return err
		}
	}
}

// backoff returns a random delay in [0, min(MaxDelay, BaseDelay*2^(attempt-1))].
func (p Policy) backoff(attempt int, rnd func() float64) time.Duration {
	ceiling := p.BaseDelay << (attempt - 1)
	if ceiling <= 0 || (p.MaxDelay > 0 && ceiling > p.MaxDelay) {
		ceiling = p.MaxDelay
	}
	return time.Duration(rnd() * float64(ceiling))
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
