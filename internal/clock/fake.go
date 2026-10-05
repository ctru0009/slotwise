package clock

import (
	"sync"
	"time"
)

// Fake is a clock tests drive by hand; it never advances on its own.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake returns a Fake that reads t until it is changed.
func NewFake(t time.Time) *Fake {
	return &Fake{now: t}
}

// Now returns the fake's current time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Set moves the fake to t.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t
}

// Advance moves the fake forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}
