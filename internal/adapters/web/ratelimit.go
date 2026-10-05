package web

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sync"
	"time"
)

// maxLimiterEntries caps how many distinct keys a Limiter remembers. Keys are
// user-controlled (login emails), so an unbounded map would be a cheap way to
// exhaust memory.
const maxLimiterEntries = 1024

// Limiter is a fixed-window rate limiter, safe for concurrent use. Each key
// gets one window of window length; Allow permits an attempt while fewer than
// limit failures were recorded in the current window, and Fail records one.
//
// Fixed windows are cheap but coarse: a burst that straddles a window
// boundary can admit up to twice the limit.
type Limiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	entries map[string]entry
}

// entry is one key's current window: when it started and the failures it saw.
type entry struct {
	start time.Time
	hits  int
}

// NewLimiter returns a Limiter that allows limit failures per window.
func NewLimiter(limit int, window time.Duration) *Limiter {
	return &Limiter{
		limit:   limit,
		window:  window,
		entries: make(map[string]entry),
	}
}

// Allow reports whether key may be attempted at now. It records nothing.
func (l *Limiter) Allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[hashKey(key)]
	if !ok || now.Sub(e.start) >= l.window {
		return true
	}
	return e.hits < l.limit
}

// Fail records one failed attempt for key, starting a new window when the
// previous one expired.
func (l *Limiter) Fail(key string, now time.Time) {
	h := hashKey(key)
	l.mu.Lock()
	defer l.mu.Unlock()
	if e, ok := l.entries[h]; ok && now.Sub(e.start) < l.window {
		e.hits++
		l.entries[h] = e
		return
	}
	l.entries[h] = entry{start: now, hits: 1}
	l.evictLocked(now)
}

// Reset forgets key, so its next attempt starts with a clean window.
func (l *Limiter) Reset(key string) {
	h := hashKey(key)
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, h)
}

// evictLocked keeps the entry map bounded, dropping expired keys first and
// the oldest live keys only when that was not enough.
func (l *Limiter) evictLocked(now time.Time) {
	if len(l.entries) <= maxLimiterEntries {
		return
	}
	for h, e := range l.entries {
		if now.Sub(e.start) >= l.window {
			delete(l.entries, h)
		}
	}
	if len(l.entries) <= maxLimiterEntries {
		return
	}
	hashes := make([]string, 0, len(l.entries))
	for h := range l.entries {
		hashes = append(hashes, h)
	}
	slices.SortFunc(hashes, func(a, b string) int {
		return l.entries[a].start.Compare(l.entries[b].start)
	})
	for _, h := range hashes[:len(hashes)-maxLimiterEntries] {
		delete(l.entries, h)
	}
}

// hashKey hashes a limiter key before it is stored, so the map never holds
// user-controlled strings (email addresses) in plaintext.
func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}
