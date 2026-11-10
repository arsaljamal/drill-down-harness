package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

var errTransient = errors.New("transient")
var errPermanent = errors.New("permanent")

func testPolicy(max int, waits *[]time.Duration) Policy {
	return Policy{
		MaxAttempts: max,
		BaseDelay:   100 * time.Millisecond,
		MaxDelay:    300 * time.Millisecond,
		Retryable:   func(err error) bool { return errors.Is(err, errTransient) },
		rand:        func() float64 { return 1 }, // always the ceiling, so delays are predictable
		sleep: func(_ context.Context, d time.Duration) error {
			*waits = append(*waits, d)
			return nil
		},
	}
}

func TestRetriesTransientUntilSuccess(t *testing.T) {
	var waits []time.Duration
	calls := 0
	err := Do(context.Background(), testPolicy(5, &waits), func(context.Context) error {
		calls++
		if calls < 3 {
			return errTransient
		}
		return nil
	})
	if err != nil {
		t.Fatalf("want success, got %v", err)
	}
	if calls != 3 {
		t.Fatalf("want 3 calls, got %d", calls)
	}
	want := []time.Duration{100 * time.Millisecond, 200 * time.Millisecond}
	if len(waits) != len(want) || waits[0] != want[0] || waits[1] != want[1] {
		t.Fatalf("waits = %v, want %v", waits, want)
	}
}

func TestBackoffIsCappedAtMaxDelay(t *testing.T) {
	var waits []time.Duration
	_ = Do(context.Background(), testPolicy(5, &waits), func(context.Context) error { return errTransient })
	if got := waits[len(waits)-1]; got != 300*time.Millisecond {
		t.Fatalf("last wait = %v, want cap 300ms", got)
	}
}

func TestStopsOnPermanentError(t *testing.T) {
	var waits []time.Duration
	calls := 0
	err := Do(context.Background(), testPolicy(5, &waits), func(context.Context) error {
		calls++
		return errPermanent
	})
	if !errors.Is(err, errPermanent) || calls != 1 {
		t.Fatalf("want 1 call and permanent error, got %d calls, %v", calls, err)
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	var waits []time.Duration
	calls := 0
	err := Do(context.Background(), testPolicy(3, &waits), func(context.Context) error {
		calls++
		return errTransient
	})
	if !errors.Is(err, errTransient) || calls != 3 {
		t.Fatalf("want 3 calls and transient error, got %d calls, %v", calls, err)
	}
}

func TestStopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	err := Do(ctx, Policy{MaxAttempts: 10, BaseDelay: time.Hour, MaxDelay: time.Hour}, func(context.Context) error {
		calls++
		cancel()
		return errTransient
	})
	if !errors.Is(err, errTransient) || calls != 1 {
		t.Fatalf("want 1 call after cancel, got %d calls, %v", calls, err)
	}
}
