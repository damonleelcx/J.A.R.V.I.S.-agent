package collab_test

import (
	"context"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/collab"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The hub is derived state: it carries liveness, never the record. These tests
// hold it to the three properties the rest of the design leans on — everybody in
// a room hears an event, nobody hears another room's, and a subscriber that
// stops reading is dropped rather than allowed to block the person talking.

func turnEvent(roomID, text string) collab.Event {
	return collab.Event{
		Kind:   collab.EventTurn,
		RoomID: roomID,
		Turn:   &collab.Turn{RoomID: roomID, Text: text},
		At:     time.Unix(0, 0),
	}
}

func TestHubDeliversToEverySubscriberInTheRoom(t *testing.T) {
	t.Parallel()
	hub := collab.NewHub(logx.Discard())

	a := hub.Subscribe("room-1", "usr_1")
	defer a.Close()
	b := hub.Subscribe("room-1", "usr_1")
	defer b.Close()

	if got := hub.Subscribers("room-1"); got != 2 {
		t.Fatalf("Subscribers = %d, want 2", got)
	}

	hub.Publish(context.Background(), turnEvent("room-1", "the tolerance is fine"))

	for name, sub := range map[string]*collab.Subscription{"a": a, "b": b} {
		select {
		case ev := <-sub.Events:
			if ev.Turn.Text != "the tolerance is fine" {
				t.Errorf("%s received %q", name, ev.Turn.Text)
			}
		case <-time.After(time.Second):
			t.Errorf("%s received nothing", name)
		}
	}
}

// A room is a boundary, not a topic filter applied by the client. Somebody in
// one meeting must not receive another meeting's turns — the transcript is
// scoped and the live view has to be scoped the same way.
func TestHubDoesNotLeakBetweenRooms(t *testing.T) {
	t.Parallel()
	hub := collab.NewHub(logx.Discard())

	other := hub.Subscribe("room-2", "usr_1")
	defer other.Close()

	hub.Publish(context.Background(), turnEvent("room-1", "not for room 2"))

	select {
	case ev := <-other.Events:
		t.Fatalf("room-2 subscriber received a room-1 event: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// The property that keeps a stalled browser from slowing down a meeting: a
// subscriber that stops reading is disconnected, and told why, rather than
// blocking Publish or being silently skipped.
//
// Silently skipping is the failure this guards against. A client that misses an
// event without being told renders a transcript with a hole in it and has no way
// to discover that; being disconnected makes it re-read the record, which is the
// truth.
func TestHubDropsASubscriberThatStopsReading(t *testing.T) {
	t.Parallel()
	hub := collab.NewHub(logx.Discard())

	slow := hub.Subscribe("room-1", "usr_1")
	reader := hub.Subscribe("room-1", "usr_1")
	defer reader.Close()

	// The healthy subscriber acknowledges each event before the next is
	// published, so it PROVABLY keeps up rather than merely being expected to
	// win a scheduling race. Without the handshake a tight publish loop outruns
	// any goroutine and drops the healthy reader too, which would make this test
	// flaky and its claim untrue.
	got := make(chan struct{})
	go func() {
		for range reader.Events {
			got <- struct{}{}
		}
		close(got)
	}()

	// Well past the buffer, so the slow subscriber must overflow.
	const sent = 200
	received := 0
	for i := 0; i < sent; i++ {
		hub.Publish(context.Background(), turnEvent("room-1", "burst"))
		select {
		case <-got:
			received++
		case <-time.After(time.Second):
			t.Fatalf("the keeping-up subscriber stalled after %d events", received)
		}
	}
	if received != sent {
		t.Errorf("keeping-up subscriber received %d of %d events", received, sent)
	}

	// The slow subscriber's channel is closed...
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-slow.Events:
			if !ok {
				goto closed
			}
		case <-deadline:
			t.Fatal("the slow subscriber was never dropped; Publish would block on it")
		}
	}
closed:
	// ...and it can tell that it was dropped for lagging rather than for the
	// room ending, which is what makes the client re-read instead of giving up.
	if !slow.Lagged() {
		t.Error("dropped subscriber reports Lagged() = false; the client cannot tell why it was disconnected")
	}
	// The slow one was dropped; the one that kept up was not. A single stalled
	// browser must not take the room down with it.
	if n := hub.Subscribers("room-1"); n != 1 {
		t.Errorf("Subscribers after the drop = %d, want 1 (the subscriber that kept up)", n)
	}
}

// An ordinary disconnect must NOT look like lagging. If it did, every closed tab
// would tell the next client to re-read, and "you fell behind" would stop
// meaning anything.
func TestHubOrdinaryCloseIsNotReportedAsLag(t *testing.T) {
	t.Parallel()
	hub := collab.NewHub(logx.Discard())

	sub := hub.Subscribe("room-1", "usr_1")
	sub.Close()

	if _, ok := <-sub.Events; ok {
		t.Fatal("Events was not closed by Close")
	}
	if sub.Lagged() {
		t.Error("a subscription closed by its own client reports Lagged() = true")
	}
	sub.Close() // idempotent; a double close must not panic
}

// A nil hub is the forgectl case: a writer with nobody watching. It must be a
// no-op rather than a crash, so no caller needs a branch of its own.
func TestHubNilPublishIsANoOp(t *testing.T) {
	t.Parallel()
	var hub *collab.Hub
	hub.Publish(context.Background(), turnEvent("room-1", "nobody is listening"))
}
