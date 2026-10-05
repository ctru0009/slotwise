package web

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ctru0009/slotwise/internal/clock"
)

// limiterTestStart is the fixed instant the limiter tests build their fake
// clock from, so no window depends on the wall clock.
var limiterTestStart = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

func TestLimiterBlocksAfterLimit(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(limiterTestStart)
	limiter := NewLimiter(10, 15*time.Minute)
	key := "tenant-1|owner@example.com"

	for i := range 10 {
		if !limiter.Allow(key, clk.Now()) {
			t.Fatalf("Allow for attempt %d = false, want true", i+1)
		}
	}
	if limiter.Allow(key, clk.Now()) {
		t.Fatal("the eleventh Allow = true, want false")
	}
}

// TestLimiterAllowsExactlyTheLimitConcurrently is the regression test for the
// check-then-act gap: when accounting happens in a separate call after the
// check, a burst of simultaneous attempts all pass the check before any of them
// is recorded, and every one of them reaches the password verification it was
// supposed to be limited to.
func TestLimiterAllowsExactlyTheLimitConcurrently(t *testing.T) {
	t.Parallel()
	const (
		limit   = 10
		callers = 200
	)
	limiter := NewLimiter(limit, 15*time.Minute)
	now := limiterTestStart

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		admitted int
	)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if limiter.Allow("tenant-1|owner@example.com", now) {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if admitted != limit {
		t.Errorf("%d of %d simultaneous attempts were admitted, want exactly %d", admitted, callers, limit)
	}
}

func TestLimiterWindowRollsOver(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(limiterTestStart)
	limiter := NewLimiter(10, 15*time.Minute)
	key := "tenant-1|owner@example.com"

	for range 10 {
		limiter.Allow(key, clk.Now())
	}
	if limiter.Allow(key, clk.Now()) {
		t.Fatal("Allow inside the window = true, want false")
	}

	clk.Advance(15 * time.Minute)
	if !limiter.Allow(key, clk.Now()) {
		t.Fatal("Allow after the window expired = false, want true")
	}
}

func TestLimiterResetClearsBlockedKey(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(limiterTestStart)
	limiter := NewLimiter(1, 15*time.Minute)
	key := "tenant-1|owner@example.com"

	limiter.Allow(key, clk.Now())
	if limiter.Allow(key, clk.Now()) {
		t.Fatal("Allow after one attempt = true, want false")
	}

	limiter.Reset(key)
	if !limiter.Allow(key, clk.Now()) {
		t.Fatal("Allow after Reset = false, want true")
	}
}

func TestLimiterKeysAreIndependent(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(limiterTestStart)
	limiter := NewLimiter(1, 15*time.Minute)

	limiter.Allow("tenant-1|owner@example.com", clk.Now())
	if !limiter.Allow("tenant-1|other@example.com", clk.Now()) {
		t.Fatal("Allow for an untouched key = false, want true")
	}
	if limiter.Allow("tenant-1|owner@example.com", clk.Now()) {
		t.Fatal("Allow for the spent key = true, want false")
	}
}

func TestLimiterBoundsRememberedKeys(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(limiterTestStart)
	limiter := NewLimiter(1, 15*time.Minute)

	// Twice as many live keys as the limiter may remember: the map has to stop
	// growing, which is what keeps user-controlled keys from exhausting memory.
	for i := range 2 * maxLimiterEntries {
		key := fmt.Sprintf("user-%04d@example.com", i)
		if !limiter.Allow(key, clk.Now()) {
			t.Fatalf("Allow(%q) = false on its first attempt, want true", key)
		}
	}
	if got := limiter.size(); got > maxLimiterEntries {
		t.Errorf("the limiter remembers %d keys, want at most %d", got, maxLimiterEntries)
	}

	// A key that keeps failing is throttled for the length of its window and
	// admitted again after it rolls over, whether or not it was remembered.
	key := "tenant-1|owner@example.com"
	limiter.Allow(key, clk.Now())
	if limiter.Allow(key, clk.Now()) {
		t.Fatal("Allow after one attempt = true, want false")
	}
	clk.Advance(15 * time.Minute)
	if !limiter.Allow(key, clk.Now()) {
		t.Fatal("Allow after the window expired = false, want true")
	}
}
