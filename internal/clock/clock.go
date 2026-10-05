package clock

import "time"

// Clock reports the current time. Use cases take one instead of reading the
// wall clock, so tests can control what "now" means.
type Clock interface {
	Now() time.Time
}

// System is the real clock.
type System struct{}

// Now returns the current wall clock time.
func (System) Now() time.Time {
	//nolint:forbidigo // the single place in the codebase allowed to read the wall clock
	return time.Now()
}
