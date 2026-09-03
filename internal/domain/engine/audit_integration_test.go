package engine_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
)

// The audit chain, against a real database and the real migration chain.
//
// The unit tests hold the hash function's shape. These hold the property that
// actually matters: that a row edited AFTER it was written stops verifying —
// which cannot be tested without a database to edit it in.

func appendEvents(t *testing.T, h *harness, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		payload, _ := json.Marshal(map[string]any{"i": i, "note": "event payload"})
		ev := &engine.Event{
			GoalID: h.goalID, Kind: engine.EventTaskStarted, Actor: engine.ActorExecutor,
			Summary: "step", Payload: payload,
		}
		if err := h.repo.AppendEvent(ctx, h.pool, ev, h.clk.Now()); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
}

func TestAuditChain_VerifiesOverRealEvents(t *testing.T) {
	h := newHarness(t)
	appendEvents(t, h, 6)

	report, err := h.repo.VerifyChain(context.Background(), h.pool, h.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Intact() {
		t.Fatalf("a freshly written timeline did not verify: %s\n%+v", report.Summary(), report.Findings)
	}
	if report.Events != 6 || report.Chained != 6 {
		t.Fatalf("walked %d events, %d chained; want 6 and 6", report.Events, report.Chained)
	}
	if report.Unchained != 0 {
		t.Fatalf("%d events reported unchained on a fresh schema", report.Unchained)
	}
}

// The point of the whole exercise: an edit made after the fact is visible, and
// the report names the row that changed rather than shrugging.
func TestAuditChain_DetectsAnEditedSummary(t *testing.T) {
	h := newHarness(t)
	appendEvents(t, h, 5)
	ctx := context.Background()

	if _, err := h.pool.Exec(ctx,
		`update forge_events set summary = 'quietly changed' where goal_id = $1 and seq = 3`,
		h.goalID); err != nil {
		t.Fatal(err)
	}

	report, err := h.repo.VerifyChain(ctx, h.pool, h.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Intact() {
		t.Fatal("a summary was edited in place and the chain still verified")
	}
	first := report.Findings[0]
	if first.Seq != 3 {
		t.Errorf("first finding is at seq %d, want the row that was edited (3)", first.Seq)
	}
	if first.Problem != "content-altered" {
		t.Errorf("problem is %q, want content-altered", first.Problem)
	}
	// Exactly one finding, naming exactly the altered row.
	//
	// An earlier version of this test demanded that every later link break too,
	// on the reasoning that "that is what a chain is for". It is not: the later
	// rows point at the RECORDED hash of this one, which the editor did not
	// touch, so they still add up — and they are not the rows that were altered.
	// Cascading here would bury the one true finding under noise. The chain earns
	// its keep against the harder tamper instead, where the editor also fixes up
	// the row's own hash: see TestAuditChain_DetectsAResealedRow.
	if len(report.Findings) != 1 {
		t.Errorf("want exactly one finding naming the altered row, got %d: %+v",
			len(report.Findings), report.Findings)
	}
}

// The tamper a per-row checksum cannot see.
//
// Someone who knows the scheme edits a row AND recomputes that row's own hash,
// so the row is internally consistent again. A checksum-per-row would now pass.
// The chain does not: the NEXT row still points at the hash the altered row used
// to have, and that link is now wrong. This is the test that distinguishes a
// chain from a column of checksums, and it is the reason for the design.
func TestAuditChain_DetectsAResealedRow(t *testing.T) {
	h := newHarness(t)
	appendEvents(t, h, 5)
	ctx := context.Background()

	// Read the row, alter it, and reseal it exactly as AppendEvent would.
	var e engine.Event
	var actor string
	var prevHash string
	if err := h.pool.QueryRow(ctx, `
		select id, goal_id, task_id, seq, kind, actor, actor_id, summary, payload, created_at, prev_hash
		  from forge_events where goal_id = $1 and seq = 3`, h.goalID).
		Scan(&e.ID, &e.GoalID, &e.TaskID, &e.Seq, &e.Kind, &actor, &e.ActorID,
			&e.Summary, &e.Payload, &e.CreatedAt, &prevHash); err != nil {
		t.Fatal(err)
	}
	e.Actor = engine.Actor(actor)
	e.Summary = "rewritten, and resealed"
	digest, err := engine.PayloadDigest(e.Payload)
	if err != nil {
		t.Fatal(err)
	}
	resealed := engine.EventHash(prevHash, &e, digest)
	if _, err := h.pool.Exec(ctx,
		`update forge_events set summary = $2, hash = $3 where goal_id = $1 and seq = 3`,
		h.goalID, e.Summary, resealed); err != nil {
		t.Fatal(err)
	}

	report, err := h.repo.VerifyChain(ctx, h.pool, h.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Intact() {
		t.Fatal("a row was edited and resealed, and the timeline still verified — " +
			"this is a column of checksums, not a chain")
	}
	// The altered row now hashes correctly. The break shows up at the row AFTER
	// it, which still points at the old value.
	if report.Findings[0].Seq != 4 || report.Findings[0].Problem != "broken-link" {
		t.Fatalf("want broken-link at seq 4 (the row pointing at the old hash), got %+v",
			report.Findings[0])
	}
}

// Deleting an event is the tamper that a per-row checksum cannot see. The chain
// can, because the row after it points at something that is no longer there.
func TestAuditChain_DetectsADeletedEvent(t *testing.T) {
	h := newHarness(t)
	appendEvents(t, h, 5)
	ctx := context.Background()

	if _, err := h.pool.Exec(ctx,
		`delete from forge_events where goal_id = $1 and seq = 2`, h.goalID); err != nil {
		t.Fatal(err)
	}
	report, err := h.repo.VerifyChain(ctx, h.pool, h.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Intact() {
		t.Fatal("an event was deleted and the chain still verified")
	}
	if report.Findings[0].Seq != 3 {
		t.Errorf("first finding at seq %d; the break should surface at the event after the deletion (3)",
			report.Findings[0].Seq)
	}
	if report.Findings[0].Problem != "broken-link" {
		t.Errorf("problem is %q, want broken-link", report.Findings[0].Problem)
	}
}

// A payload edited in place is reported as its own kind of finding, so an
// operator can tell "someone changed what happened" from "someone changed the
// record of when it happened".
func TestAuditChain_DetectsAnEditedPayload(t *testing.T) {
	h := newHarness(t)
	appendEvents(t, h, 3)
	ctx := context.Background()

	if _, err := h.pool.Exec(ctx,
		`update forge_events set payload = '{"i": 999, "note": "tampered"}'::jsonb
		  where goal_id = $1 and seq = 2`, h.goalID); err != nil {
		t.Fatal(err)
	}
	report, err := h.repo.VerifyChain(ctx, h.pool, h.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Intact() {
		t.Fatal("a payload was rewritten and the chain still verified")
	}
	var found bool
	for _, f := range report.Findings {
		if f.Seq == 2 && f.Problem == "payload-altered" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no payload-altered finding at seq 2: %+v", report.Findings)
	}
}

// jsonb decides key order and number formatting for us. If the canonical form
// did not survive that round trip, every verification would fail on every event
// and the feature would be worse than useless. This is the fence on that claim.
func TestAuditChain_SurvivesJsonbNormalisation(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Deliberately awkward: keys out of order, nested, a float, a negative, an
	// empty object, unicode, and a large integer.
	payload := json.RawMessage(`{"z":1,"a":{"n":-2,"f":1.5},"m":[3,2,1],"e":{},"u":"café","big":9007199254740991}`)
	ev := &engine.Event{
		GoalID: h.goalID, Kind: engine.EventTaskSucceeded, Actor: engine.ActorVerifier,
		Summary: "awkward payload", Payload: payload,
	}
	if err := h.repo.AppendEvent(ctx, h.pool, ev, h.clk.Now()); err != nil {
		t.Fatal(err)
	}
	report, err := h.repo.VerifyChain(ctx, h.pool, h.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Intact() {
		t.Fatalf("a payload did not survive the jsonb round trip: %s\n%+v",
			report.Summary(), report.Findings)
	}
}

// Rows written before 0005 have no hash. They are reported as unchained and
// never repaired: giving them a chain would attest to an order nobody recorded.
func TestAuditChain_ReportsPreExistingRowsRatherThanInventingHashes(t *testing.T) {
	h := newHarness(t)
	appendEvents(t, h, 2)
	ctx := context.Background()

	// Simulate a row from before the migration.
	if _, err := h.pool.Exec(ctx,
		`update forge_events set prev_hash = null, hash = null, payload_digest = null
		  where goal_id = $1 and seq = 1`, h.goalID); err != nil {
		t.Fatal(err)
	}
	report, err := h.repo.VerifyChain(ctx, h.pool, h.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Unchained != 1 {
		t.Fatalf("unchained count is %d, want 1", report.Unchained)
	}
	// Counted and surfaced in the summary, but NOT a finding: a database that
	// predates the migration must not fail its own audit forever for a reason
	// that is not tampering.
	if !report.Intact() {
		t.Fatalf("a pre-migration row was reported as tamper evidence: %+v", report.Findings)
	}
	if !strings.Contains(report.Summary(), "cannot be attested") {
		t.Fatalf("the summary hides the coverage gap: %q", report.Summary())
	}
}

// Nulling the chain columns must not be a way to leave no trace. A leading run
// of unchained rows is the legitimate pre-migration case; one that appears after
// the chain has started is somebody removing a link.
func TestAuditChain_DetectsAStrippedHash(t *testing.T) {
	h := newHarness(t)
	appendEvents(t, h, 4)
	ctx := context.Background()

	if _, err := h.pool.Exec(ctx,
		`update forge_events set prev_hash = null, hash = null, payload_digest = null
		  where goal_id = $1 and seq = 3`, h.goalID); err != nil {
		t.Fatal(err)
	}
	report, err := h.repo.VerifyChain(ctx, h.pool, h.goalID)
	if err != nil {
		t.Fatal(err)
	}
	if report.Intact() {
		t.Fatal("a hash was stripped mid-chain and the timeline still verified — " +
			"nulling two columns would be a way to erase an event without trace")
	}
	if report.Findings[0].Problem != "missing-hash" || report.Findings[0].Seq != 3 {
		t.Fatalf("want missing-hash at seq 3, got %+v", report.Findings[0])
	}
}
