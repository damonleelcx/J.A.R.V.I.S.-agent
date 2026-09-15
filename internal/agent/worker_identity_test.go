package agent

import (
	"testing"
)

// A worker's identity is what its lease rows name, and every lease guard —
// Heartbeat, Release, recording an export — compares against it. Two workers that
// share one cannot be told apart by any of them: a worker whose lease lapsed and
// was reclaimed by its sibling could still extend it, hand it back, or finish it.
//
// forge-worker starts FORGE_WORKER_CONCURRENCY workers in one loop, in the same
// millisecond, so "started together" is the production case, not a corner.
// docs/bugfix/2026-09-15-workers-started-together-shared-one-lease-identity.md
func TestNewWorker_WorkersStartedTogetherHaveDistinctIdentities(t *testing.T) {
	seen := map[string]int{}
	for i := 0; i < 64; i++ {
		w := NewWorker(WorkerDeps{})
		if prev, dup := seen[w.ID]; dup {
			t.Fatalf("workers %d and %d, started together, are both %q: their leases cannot be told apart",
				prev, i, w.ID)
		}
		seen[w.ID] = i
	}
}
