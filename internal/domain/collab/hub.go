package collab

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The live half of a room (PRD COL-01).
//
// # What this is, and what it deliberately is not
//
// room.go builds the durable RECORD: who was present, who said what, which
// approvals were made. That record is the truth and it lives in Postgres. This
// file adds the other half a shared session needs — telling the people currently
// in the room that something just happened, without them polling for it.
//
// The hub is therefore **derived state, and never authoritative**. Every write
// goes to the database first and is published second. If this process dies, or
// a subscriber misses an event, nothing is lost that matters: the transcript is
// still complete, and a client that reconnects and re-reads the room sees
// everything. Liveness is the only thing in memory.
//
// That ordering is the whole design. The tempting alternative — publish first
// so the room feels faster, write after — produces a room where somebody saw a
// turn that is not in the transcript, which is precisely the failure COL-01
// exists to prevent.
//
// # Why a slow subscriber is disconnected rather than waited for
//
// A browser that has stopped reading (a laptop lid closed mid-meeting, a stalled
// network) must never slow down the person who is talking. So sends are
// non-blocking, and a subscriber whose buffer is full is **closed with a lag
// signal** rather than silently skipped.
//
// Dropping the connection is the honest option and the self-healing one: a
// client that is told it fell behind reconnects and re-reads the room from the
// database, which is the truth anyway. A client that is silently skipped keeps
// rendering a transcript with a hole in it and has no way to find out.

// EventKind is what happened in a room.
type EventKind string

const (
	// EventTurn — somebody said something. Carries the persisted turn.
	EventTurn EventKind = "turn"
	// EventPresence — somebody joined or left.
	EventPresence EventKind = "presence"
	// EventClosed — the session ended. Subscribers stop after this.
	EventClosed EventKind = "closed"
	// EventLagged — this subscriber fell too far behind to be caught up.
	//
	// Not a room event at all, and never carried on the channel: it names what
	// the HTTP layer emits after seeing Subscription.Lagged on a closed stream,
	// telling that one client to reconnect and re-read. It exists because the
	// alternative to saying so is a client that quietly renders an incomplete
	// transcript.
	EventLagged EventKind = "lagged"
	// EventHello — the first frame on a stream, naming the session.
	//
	// A connection needs a name before it can be addressed. Media signalling is
	// per-connection, not per-person: one person with two tabs is two peers, and
	// a renegotiation offer meant for one of them is wrong for the other.
	EventHello EventKind = "hello"
	// EventMediaOffer — a renegotiation offer for ONE session.
	//
	// An SFU has to re-offer whenever the set of tracks a peer receives changes,
	// which happens every time somebody joins or leaves. That offer is addressed
	// to a single connection, which is what Event.ToStream is for.
	EventMediaOffer EventKind = "media-offer"
	// EventSpeaking — somebody started or stopped talking.
	//
	// Published, never recorded. Who is talking right now is a fact about this
	// instant; the transcript already holds what was said, and a row per breath
	// would bury it. Present carries the state.
	EventSpeaking EventKind = "speaking"
	// EventTranscribing — the room started or stopped being written down.
	//
	// Published the moment it changes, because a privacy state that changed
	// silently is worse than no control at all: somebody would go on speaking
	// believing they were off the record. Present carries the new state.
	EventTranscribing EventKind = "transcribing"
	// EventRedacted — spoken turns in this room had their content deleted.
	//
	// Published because a deletion that reaches only the person who asked for it
	// is not one. Everybody else's open transcript would go on displaying the
	// words indefinitely, and the person who deleted them would have no way to
	// know. Carries no content, only the fact: clients re-read the record.
	EventRedacted EventKind = "redacted"
)

// Event is one thing a subscriber is told.
//
// Turn is the persisted row, not the request — a subscriber sees the same
// sequence number and timestamp the transcript will show, so the live view and
// the record cannot disagree about ordering.
type Event struct {
	Kind   EventKind
	RoomID string
	Turn   *Turn
	// UserID and Present describe a presence change. Present is false on leave.
	UserID  string
	Present bool
	At      time.Time
	// ToStream addresses one connection instead of the whole room. Empty means
	// everybody, which is the ordinary case.
	ToStream string
	// FromStream names the connection an event came from, for events that are
	// about one participant rather than about the room.
	FromStream string
	// SDP carries a media signalling payload for EventMediaOffer.
	//
	// Opaque here, and deliberately so: this package is the shared-session
	// RECORD, and it does not model WebRTC. Keeping the payload a string is what
	// stops a media library becoming a dependency of the transcript — the same
	// reason room.go describes itself as transport-agnostic.
	SDP string
}

// subscriberBuffer is how many events a subscriber may fall behind before it is
// disconnected.
//
// Sized for a burst, not for an absent reader: a room where several people talk
// at once produces a handful of events in quick succession, and a browser that
// cannot drain 32 of them is not reading at all.
const subscriberBuffer = 32

// Subscription is one connection's view of a room.
type Subscription struct {
	// ID names this connection. Media signalling is addressed to it.
	ID string
	// UserID is who opened it. Held so that a caller naming a stream id can be
	// checked against the person who actually owns that stream.
	UserID string

	// Events is closed when the subscription ends, for any reason. Read Lagged
	// once it closes to find out which reason.
	Events <-chan Event

	hub    *Hub
	roomID string
	ch     chan Event
	once   sync.Once
	// lagged records WHY the channel closed.
	//
	// A flag rather than a final event on the channel, and that is a correctness
	// requirement rather than a style choice: the sender would have to write to
	// ch after releasing the hub lock, and a subscriber that closed itself in
	// that window would turn a disconnect into "send on closed channel" — a
	// panic that takes the process down. Nothing is ever sent to a subscription
	// outside the read lock.
	lagged atomic.Bool
}

// Lagged reports whether this subscription was dropped for falling behind.
//
// Meaningful once Events is closed. False means an ordinary end: the client
// disconnected, or the room closed.
func (s *Subscription) Lagged() bool { return s.lagged.Load() }

// Close ends the subscription. Safe to call more than once, from any goroutine.
//
// The order matters: remove takes the hub's write lock, which cannot be held
// while any Publish is sending, so by the time the channel is closed no send can
// be in flight and no later one can start.
func (s *Subscription) Close() {
	s.once.Do(func() {
		s.hub.remove(s.roomID, s)
		close(s.ch)
	})
}

// Hub fans room events out to whoever is currently connected.
//
// The zero value is not usable; call NewHub.
type Hub struct {
	mu   sync.RWMutex
	subs map[string]map[*Subscription]struct{}
	log  *logx.Logger
}

// NewHub builds an empty hub.
func NewHub(log *logx.Logger) *Hub {
	return &Hub{subs: make(map[string]map[*Subscription]struct{}), log: log}
}

// Subscribe registers a connection for one room's events.
//
// The caller must Close the subscription; a handler does so on request
// cancellation, which covers a closed tab as well as a clean disconnect.
func (h *Hub) Subscribe(roomID, userID string) *Subscription {
	ch := make(chan Event, subscriberBuffer)
	sub := &Subscription{ID: id.New(id.PrefixStream), UserID: userID,
		Events: ch, hub: h, roomID: roomID, ch: ch}

	h.mu.Lock()
	defer h.mu.Unlock()
	if h.subs[roomID] == nil {
		h.subs[roomID] = make(map[*Subscription]struct{})
	}
	h.subs[roomID][sub] = struct{}{}
	return sub
}

// Subscribers reports how many connections are watching a room.
//
// Exists for the capacity ceiling (PRD NFR-04) and for tests; it is a count of
// live connections, not of participants — the record answers who is present.
func (h *Hub) Subscribers(roomID string) int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs[roomID])
}

// StreamBelongsTo reports whether a stream is an open stream of this person's,
// in this room.
//
// All three have to match. A caller who could name somebody else's stream id
// could renegotiate their audio out from under them — the media equivalent of
// putting words in another person's mouth, which the transcript already refuses.
// Checking the room as well means a stream id learned in one room grants nothing
// in another.
func (h *Hub) StreamBelongsTo(roomID, streamID, userID string) bool {
	if streamID == "" || userID == "" {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	for sub := range h.subs[roomID] {
		if sub.ID == streamID {
			return sub.UserID == userID
		}
	}
	return false
}

// Publish delivers an event to everyone watching the room.
//
// Never blocks. Callers hold no lock of their own across this, and a stalled
// subscriber cannot delay the write path that produced the event.
//
// A nil hub is a no-op, so a caller with no subscribers — forgectl — needs no
// branch of its own and cannot forget one.
func (h *Hub) Publish(ctx context.Context, ev Event) {
	if h == nil {
		return
	}
	h.mu.RLock()
	lagged := make([]*Subscription, 0)
	for sub := range h.subs[ev.RoomID] {
		// An addressed event reaches one connection. Filtered here rather than in
		// a second Publish method, so there is one delivery path with one set of
		// lag semantics — two would eventually disagree about what happens to a
		// slow subscriber.
		if ev.ToStream != "" && sub.ID != ev.ToStream {
			continue
		}
		select {
		case sub.ch <- ev:
		default:
			// Full buffer. Collected here and disconnected below, because
			// closing a subscription takes the write lock this loop holds for
			// reading.
			lagged = append(lagged, sub)
		}
	}
	h.mu.RUnlock()

	for _, sub := range lagged {
		// Marked before closing, so a reader that observes the closed channel
		// always sees the reason. Nothing is written to the channel here — see
		// the note on Subscription.lagged.
		sub.lagged.Store(true)
		// A dropped subscriber is a real degradation of somebody's session, not
		// a routine event: it is logged at WARN so it is visible without being
		// hunted for.
		h.log.Warn(ctx, logx.EventRoomStreamLagged,
			"room_id", ev.RoomID, "buffer", subscriberBuffer)
		sub.Close()
	}
}

// remove unregisters a subscription. Called only from Subscription.Close.
func (h *Hub) remove(roomID string, sub *Subscription) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if set := h.subs[roomID]; set != nil {
		delete(set, sub)
		if len(set) == 0 {
			// Rooms come and go; an empty map per room that was ever busy would
			// be a slow leak in a long-running process.
			delete(h.subs, roomID)
		}
	}
}
