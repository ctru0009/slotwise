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
// exhaust memory — but eviction is itself a lever an attacker can pull, so the
// cap sits far above the number of accounts one process serves while keeping
// the eviction sort cheap enough to be irrelevant. Every entry cost an attempt
// that verified a password hash, so filling the map is expensive, and clearing
// one account's window by flooding costs that many attempts again.
const maxLimiterEntries = 4096

// Limiter is a fixed-window rate limiter, safe for concurrent use. Allow is the
// only accounting call: it records the attempt and reports whether the window
// had room for it, so a burst of concurrent attempts cannot pass the check
// before any of them is recorded — which is precisely the gap an attacker wants
// for password guessing, since each admitted attempt costs a hash
// verification. Callers clear a key with Reset when an attempt succeeds.
//
// Fixed windows are cheap but coarse: a burst that straddles a window boundary
// can admit up to twice the limit.
type Limiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	entries map[string]entry
}

// entry is one key's current window: when it started and the attempts it saw.
type entry struct {
	start time.Time
	hits  int
}

// NewLimiter returns a Limiter that allows limit attempts per window.
func NewLimiter(limit int, window time.Duration) *Limiter {
	return &Limiter{
		limit:   limit,
		window:  window,
		entries: make(map[string]entry),
	}
}

// Allow records an attempt on key at now and reports whether the window had
// room for it. It reports false once limit attempts were recorded inside the
// window, and starts a fresh window once the previous one expired.
func (l *Limiter) Allow(key string, now time.Time) bool {
	h := hashKey(key)
	l.mu.Lock()
	defer l.mu.Unlock()

	e, ok := l.entries[h]
	if !ok || now.Sub(e.start) >= l.window {
		l.entries[h] = entry{start: now, hits: 1}
		l.evictLocked(now)
		return true
	}
	if e.hits >= l.limit {
		return false
	}
	e.hits++
	l.entries[h] = e
	return true
}

// Reset forgets key, so its next attempt starts with a clean window.
func (l *Limiter) Reset(key string) {
	h := hashKey(key)
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, h)
}

// size reports how many keys the limiter remembers. It exists so tests can pin
// the memory bound, which is otherwise invisible.
func (l *Limiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.entries)
}

// evictLocked keeps the entry map bounded. Expired windows go first; if the map
// is still full it drops the oldest live windows, because the alternatives are
// worse: refusing new keys while full would turn the memory bound into a way to
// lock every account out, and tracking a new key without room would let it be
// attempted forever. The cap is therefore large enough that filling it costs
// maxLimiterEntries password verifications before an attacker can clear a
// single account's window.
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
