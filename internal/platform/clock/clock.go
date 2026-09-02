// Package clock abstracts time so that durable-agent behaviour — leases,
// backoff, token expiry, wake-up scheduling — is testable without sleeping.
//
// Why this exists: a long-running agent is mostly *time* logic. Tests that
// assert "the lease expired and another worker picked the task up" must be able
// to move time forward instantly; otherwise those paths go untested, which is
// exactly where a durable system rots.
package clock

import (
	"sync"
	"time"
)

// Clock is the time source. Production uses System; tests use Fake.
type Clock interface {
	// Now returns the current instant in UTC.
	Now() time.Time
}

// System is the real wall clock.
type System struct{}

// Now returns time.Now() in UTC. UTC is enforced here rather than at call sites
// so a local-time value can never reach the database.
func (System) Now() time.Time { return time.Now().UTC() }

// Fake is a controllable clock for tests.
type Fake struct {
	mu  sync.Mutex
	now time.Time
}

// NewFake returns a Fake positioned at t.
func NewFake(t time.Time) *Fake { return &Fake{now: t.UTC()} }

// Now returns the fake's current instant.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// Advance moves the fake clock forward by d.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

// Set positions the fake clock at t.
func (f *Fake) Set(t time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = t.UTC()
}
