package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// The tamper-evident timeline (PRD SAF-06).
//
// # The property
//
// Every event carries the hash of the event before it, per goal. Change a
// summary, delete an event, reorder two of them, and every link after the change
// stops matching — and VerifyChain reports the FIRST break, which is the row
// that was altered rather than a vague "something is wrong".
//
// # Why the hashing lives in Go rather than in SQL
//
// It is the part that has to be checkable. A digest computed by a database
// function is verified by asking the same database to compute it again, which
// answers a different question than "does this row still say what it said". In
// Go it is a pure function of the event, testable without a database, and the
// verifier can be pointed at a dump.
//
// # What is honestly claimed
//
// Tamper-EVIDENT, not tamper-proof. Anyone able to write these rows can rewrite
// the chain over them. It catches silent edits — a bug, a migration, a careless
// UPDATE, someone who did not know the chain was there — which is the realistic
// threat to an audit log living inside the system it audits. Detecting an
// attacker who owns the database needs digests shipped off-box; that is not
// implemented and is not claimed.

// chainGenesis is the previous-hash value of a goal's first event. A fixed,
// named constant rather than the empty string, so a row whose prev_hash was
// blanked by accident is distinguishable from a legitimate first event.
const chainGenesis = "forge.timeline.genesis.v1"

// EventHash is the deterministic digest of one event's contents.
//
// Field order and the separator are part of the format. The separator is a byte
// that cannot occur in the text fields being joined, so two different events
// cannot produce the same input string by moving content across a boundary —
// ("a", "bc") and ("ab", "c") must not hash alike.
func EventHash(prevHash string, e *Event, payloadDigest string) string {
	actorID := ""
	if e.ActorID != nil {
		actorID = *e.ActorID
	}
	taskID := ""
	if e.TaskID != nil {
		taskID = *e.TaskID
	}
	h := sha256.New()
	for _, field := range []string{
		"forge.event.v1",
		prevHash,
		e.ID,
		e.GoalID,
		taskID,
		fmt.Sprintf("%d", e.Seq),
		e.Kind,
		string(e.Actor),
		actorID,
		e.Summary,
		payloadDigest,
		// UTC and nanosecond-explicit: a timestamp rendered in a different zone
		// or truncated differently would break verification on a machine
		// configured differently from the one that wrote it.
		e.CreatedAt.UTC().Format(time.RFC3339Nano),
	} {
		h.Write([]byte(field))
		h.Write([]byte{0x1e}) // ASCII record separator
	}
	return hex.EncodeToString(h.Sum(nil))
}

// PayloadDigest hashes an event payload through a canonical form.
//
// jsonb normalises what it stores — key order and number formatting are the
// database's choice — so hashing the bytes we happened to send would fail
// verification against the bytes the database happens to return. Both sides go
// through the same canonicalisation instead: decode, re-encode with Go's
// map-key ordering, hash that.
//
// An empty or absent payload digests as the canonical empty object, so "no
// payload" and "{}" are the same fact and cannot be swapped for one another.
func PayloadDigest(payload json.RawMessage) (string, error) {
	raw := strings.TrimSpace(string(payload))
	if raw == "" || raw == "null" {
		raw = "{}"
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return "", errs.Wrap("engine.PayloadDigest", errs.CodeSerializationFail, err).
			WithDetail("event payload is not valid JSON, so it cannot be entered into the audit chain")
	}
	canonical, err := json.Marshal(v)
	if err != nil {
		return "", errs.Wrap("engine.PayloadDigest", errs.CodeSerializationFail, err)
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), nil
}

// ChainFinding is one thing wrong with a goal's timeline.
type ChainFinding struct {
	Seq  int64
	ID   string
	Kind string
	// Problem is one of: "broken-link", "payload-altered", "unchained",
	// "missing-hash". Named rather than free text so an operator can grep and a
	// test can assert.
	Problem string
	Detail  string
}

// ChainReport is the result of verifying one goal's timeline.
type ChainReport struct {
	GoalID string
	// Events is how many rows were walked; Chained is how many carried a hash.
	Events  int
	Chained int
	// Unchained counts rows written before the chain existed. They are counted
	// and reported, but they are NOT findings: a database that predates this
	// migration would otherwise fail its own audit forever, for a reason that is
	// not tampering, and a check that always fails is a check nobody reads.
	//
	// Only a LEADING run of them is benign. An unchained row that appears after
	// a chained one means somebody removed a chain, and that is a finding —
	// otherwise nulling two columns would be a way to leave no trace.
	Unchained int
	// Findings is tamper evidence and nothing else.
	Findings []ChainFinding
}

// Intact reports whether there is any evidence of tampering.
//
// It is deliberately not "is everything attested". Unattested rows are a
// coverage fact, reported separately by Summary; conflating the two would mean
// an old database can never be clean and a new one is never distinguishable
// from it.
func (r ChainReport) Intact() bool { return len(r.Findings) == 0 }

// Summary is one line for a human or a log.
func (r ChainReport) Summary() string {
	if r.Intact() {
		if r.Unchained > 0 {
			return fmt.Sprintf("%d event(s): chain intact over %d; %d predate the chain and cannot be attested",
				r.Events, r.Chained, r.Unchained)
		}
		return fmt.Sprintf("%d event(s): chain intact", r.Events)
	}
	return fmt.Sprintf("%d event(s): %d PROBLEM(S), first at seq %d",
		r.Events, len(r.Findings), r.Findings[0].Seq)
}

// VerifyChain walks a goal's timeline and reports the first place it stops
// adding up, and every place after it.
//
// It reads only what is stored. Nothing is repaired and nothing is written: a
// verifier that fixes what it finds destroys the evidence it was run to gather.
func (r *Repository) VerifyChain(ctx context.Context, ex db.Querier, goalID string) (*ChainReport, error) {
	const op = "engine.Repository.VerifyChain"

	rows, err := ex.Query(ctx, `
		select id, goal_id, task_id, seq, kind, actor, actor_id, summary,
		       payload, created_at, prev_hash, hash, payload_digest
		  from forge_events
		 where goal_id = $1
		 order by seq asc`, goalID)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer rows.Close()

	report := &ChainReport{GoalID: goalID}
	var expectedPrev string
	seenChained := false

	for rows.Next() {
		var e Event
		var actor string
		var prevHash, hash, storedDigest *string
		if err := rows.Scan(&e.ID, &e.GoalID, &e.TaskID, &e.Seq, &e.Kind, &actor,
			&e.ActorID, &e.Summary, &e.Payload, &e.CreatedAt,
			&prevHash, &hash, &storedDigest); err != nil {
			return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
		e.Actor = Actor(actor)
		report.Events++

		if hash == nil || *hash == "" {
			report.Unchained++
			if seenChained {
				// A gap after the chain started. Rows do not spontaneously lose
				// their hash, so this is somebody removing one.
				report.Findings = append(report.Findings, ChainFinding{
					Seq: e.Seq, ID: e.ID, Kind: e.Kind, Problem: "missing-hash",
					Detail: "this event has no hash but follows events that do — a chain was removed here",
				})
			}
			continue
		}

		if !seenChained {
			// The chain starts (or resumes after pre-migration rows) here. What
			// came before it cannot be attested, so its recorded prev_hash is
			// taken as given rather than checked against a genesis it may
			// legitimately not have.
			seenChained = true
			expectedPrev = deref(prevHash)
		}
		report.Chained++

		if deref(prevHash) != expectedPrev {
			report.Findings = append(report.Findings, ChainFinding{
				Seq: e.Seq, ID: e.ID, Kind: e.Kind, Problem: "broken-link",
				Detail: fmt.Sprintf("prev_hash is %s, expected %s — an event before this one was altered, removed, or reordered",
					short(deref(prevHash)), short(expectedPrev)),
			})
		}

		digest, derr := PayloadDigest(e.Payload)
		if derr != nil {
			report.Findings = append(report.Findings, ChainFinding{
				Seq: e.Seq, ID: e.ID, Kind: e.Kind, Problem: "payload-altered",
				Detail: "payload is not valid JSON: " + derr.Error(),
			})
			digest = deref(storedDigest)
		} else if storedDigest == nil || *storedDigest != digest {
			report.Findings = append(report.Findings, ChainFinding{
				Seq: e.Seq, ID: e.ID, Kind: e.Kind, Problem: "payload-altered",
				Detail: "the payload no longer matches the digest recorded when it was written",
			})
			digest = deref(storedDigest) // keep walking with what was recorded
		}

		if want := EventHash(deref(prevHash), &e, digest); want != *hash {
			report.Findings = append(report.Findings, ChainFinding{
				Seq: e.Seq, ID: e.ID, Kind: e.Kind, Problem: "content-altered",
				Detail: "this event's own contents no longer hash to its recorded hash",
			})
		}
		expectedPrev = *hash
	}
	if err := rows.Err(); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return report, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func short(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:12] + "…"
}
