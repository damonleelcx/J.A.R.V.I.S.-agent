package clock

import (
	"sync"
	"testing"
	"time"
)

// TestSystemClockIsUTC matters because every lease, expiry, and backoff decision
// in this system is a timestamp comparison. A local-time value reaching the
// database shifts all of them by the offset, silently.
func TestSystemClockIsUTC(t *testing.T) {
	now := System{}.Now()
	if now.Location() != time.UTC {
		t.Errorf("System.Now() returned location %v, want UTC", now.Location())
	}
	if d := time.Since(now).Abs(); d > time.Minute {
		t.Errorf("System.Now() is %v away from time.Now(); it is not tracking the wall clock", d)
	}
}

// TestFakeIsControllable is the property the durable engine's tests depend on:
// "the lease expired and another worker took over" must be assertable instantly,
// not by sleeping for the lease duration. Time paths that can only be tested by
// waiting do not get tested, and that is where a durable system rots.
func TestFakeIsControllable(t *testing.T) {
	start := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	f := NewFake(start)

	if !f.Now().Equal(start) {
		t.Fatalf("Now() = %v, want %v", f.Now(), start)
	}
	f.Advance(90 * time.Second)
	if got, want := f.Now(), start.Add(90*time.Second); !got.Equal(want) {
		t.Errorf("after Advance: %v, want %v", got, want)
	}
	f.Set(start)
	if !f.Now().Equal(start) {
		t.Errorf("Set did not reposition the clock")
	}
}

func TestFakeNormalisesToUTC(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	f := NewFake(time.Date(2026, 9, 2, 8, 0, 0, 0, loc))
	if f.Now().Location() != time.UTC {
		t.Errorf("Fake stored a non-UTC location %v; it must normalise like System does", f.Now().Location())
	}
}

// TestFakeIsRaceFree — the engine's tests advance the clock from one goroutine
// while workers read it from others.
func TestFakeIsRaceFree(t *testing.T) {
	f := NewFake(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC))
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); f.Advance(time.Second) }()
		go func() { defer wg.Done(); _ = f.Now() }()
	}
	wg.Wait()
	if got := f.Now(); !got.Equal(time.Date(2026, 9, 2, 12, 0, 16, 0, time.UTC)) {
		t.Errorf("after 16 concurrent advances: %v", got)
	}
}

// Compile-time proof that both implementations satisfy the interface, so a
// signature change cannot silently leave production on the fake or vice versa.
var (
	_ Clock = System{}
	_ Clock = (*Fake)(nil)
)
