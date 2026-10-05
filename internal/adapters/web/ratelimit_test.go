package web

import (
	"fmt"
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
			t.Fatalf("Allow before the %dth failure = false, want true", i+1)
		}
		limiter.Fail(key, clk.Now())
	}
	if limiter.Allow(key, clk.Now()) {
		t.Fatal("the eleventh Allow = true, want false")
	}
}

func TestLimiterWindowRollsOver(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(limiterTestStart)
	limiter := NewLimiter(10, 15*time.Minute)
	key := "tenant-1|owner@example.com"

	for range 10 {
		limiter.Fail(key, clk.Now())
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

	limiter.Fail(key, clk.Now())
	if limiter.Allow(key, clk.Now()) {
		t.Fatal("Allow after one failure = true, want false")
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

	limiter.Fail("tenant-1|owner@example.com", clk.Now())
	if !limiter.Allow("tenant-1|other@example.com", clk.Now()) {
		t.Fatal("Allow for an untouched key = false, want true")
	}
	if limiter.Allow("tenant-1|owner@example.com", clk.Now()) {
		t.Fatal("Allow for the failed key = true, want false")
	}
}

func TestLimiterBoundsRememberedKeys(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(limiterTestStart)
	limiter := NewLimiter(1, 15*time.Minute)

	// More distinct keys than the limiter may remember, all with a live
	// window: the map is capped, so one of them gets dropped.
	const attempts = maxLimiterEntries + 1
	keys := make([]string, attempts)
	for i := range keys {
		keys[i] = fmt.Sprintf("user-%04d@example.com", i)
		limiter.Fail(keys[i], clk.Now())
	}

	blocked := 0
	for _, key := range keys {
		if !limiter.Allow(key, clk.Now()) {
			blocked++
		}
	}
	if blocked != maxLimiterEntries {
		t.Fatalf("blocked live keys = %d, want %d: the limiter must cap its map", blocked, maxLimiterEntries)
	}

	// A key recorded after the window rolled over still blocks normally.
	clk.Advance(15 * time.Minute)
	fresh := "fresh@example.com"
	limiter.Fail(fresh, clk.Now())
	if limiter.Allow(fresh, clk.Now()) {
		t.Fatal("Allow for a key with a recorded failure = true, want false")
	}

	// Every key from the first burst is allowed again: either its window
	// expired or the limiter evicted it.
	for _, key := range keys {
		if !limiter.Allow(key, clk.Now()) {
			t.Fatalf("Allow(%q) after the window expired = false, want true", key)
		}
	}
}
