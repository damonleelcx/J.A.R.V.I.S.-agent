package collab

// This file is in package collab rather than collab_test because the drill below
// has to fill a subscriber's buffer EXACTLY to capacity, which means knowing
// subscriberBuffer. An external test can only guess, and a guess that overshoots
// drops the subscriber during the fill — which is how the first version of this
// fence came out green against a genuinely broken hub.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Regression fence: Publish must never write to a subscription's channel.
//
// # The bug this exists for
//
// The first version of Publish notified a lagging subscriber by sending a final
// event to its channel, after releasing the hub's read lock. A client that
// closed itself in that window closed the channel first, and the send then
// panicked with "send on closed channel" — taking the entire server down,
// triggered by nothing worse than a browser tab closing at an unlucky moment.
//
// The fix records the reason in Subscription.lagged and sends nothing. See the
// note on that field.
//
// # Why the drill has to be shaped like this
//
// A subscriber reaches the lagging path ONLY when its buffer is already full, so
// the race window opens on the single publish that overflows it. The fill is
// therefore exactly subscriberBuffer events — one more and the subscriber is
// dropped during setup, the buggy line never runs, and the fence passes against
// broken code.
//
// Verified by mutation: restoring the channel send makes this test panic.
func TestPublishNeverSendsOutsideTheLock(t *testing.T) {
	t.Parallel()
	hub := NewHub(logx.Discard())
	ctx := context.Background()

	for round := 0; round < 200; round++ {
		subs := make([]*Subscription, 4)
		for i := range subs {
			subs[i] = hub.Subscribe("room-1", "usr_1")
		}

		// Fill every buffer to exactly capacity. Nobody reads, and no overflow
		// has happened yet, so all four are still subscribed and all four are
		// one event away from the lagging path.
		for i := 0; i < subscriberBuffer; i++ {
			hub.Publish(ctx, Event{Kind: EventTurn, RoomID: "room-1",
				Turn: &Turn{RoomID: "room-1", Text: "fill"}, At: time.Unix(0, 0)})
		}
		if n := hub.Subscribers("room-1"); n != len(subs) {
			t.Fatalf("fill dropped subscribers early: %d of %d left — subscriberBuffer changed?",
				n, len(subs))
		}

		// Now the overflow and the close, at the same time. This is the window.
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			hub.Publish(ctx, Event{Kind: EventTurn, RoomID: "room-1",
				Turn: &Turn{RoomID: "room-1", Text: "overflow"}, At: time.Unix(0, 0)})
		}()
		go func() {
			defer wg.Done()
			for _, s := range subs {
				s.Close()
			}
		}()
		wg.Wait()

		for _, s := range subs {
			s.Close()
		}
	}
}
