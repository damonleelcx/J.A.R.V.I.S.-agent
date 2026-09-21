package workspace_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The claim a raised risk ceiling rests on runs out (PRD AGT-03; issue 20).
//
// # Why this is the sharp end of the time-bound clause
//
// 0021 is careful that a raised ceiling is ATTRIBUTABLE: the four columns move
// together, so a holder always has an author, and a claim with nobody attesting
// to it raises nothing. Attribution is not currency. The named engineer who
// accepted responsibility for r2 work can leave the company, and before this the
// project's ceiling stayed raised on the strength of a record nobody had
// revisited — the failure 0021 guards against, one step removed. It guards
// against authority with no author; this guards against authority that has
// expired.

// A recorded authority stops raising the ceiling when it lapses.
//
// pack.Definition.CeilingWith takes one bool and nothing else, and that bool is
// ReviewAuthority.Recorded(). So expiry is answered there or it is answered
// nowhere: a caller reading Holder and deciding for itself would be the second
// authorisation path this codebase spends its comments refusing to have.
func TestReviewAuthority_ARecordedAuthorityExpires(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := civilProject(t, h)

	var buf bytes.Buffer
	svc := workspace.NewService(h.pool, h.clk, logx.New(logx.Options{Output: &buf}))

	until := h.clk.Now().Add(48 * time.Hour)
	if err := svc.RecordReviewAuthority(ctx, h.pool, id,
		"R. Okonkwo", "CEng MICE 481920", h.userID, until); err != nil {
		t.Fatal(err)
	}
	def, err := svc.PackFor(ctx, h.pool, id)
	if err != nil {
		t.Fatal(err)
	}
	if def.ReviewCeiling == def.MaxTier || !def.ReviewCeiling.Valid() {
		t.Fatalf("this fence needs a domain whose ceiling actually rises; %s raises %s to %s",
			def.Pack, def.MaxTier, def.ReviewCeiling)
	}

	a, err := svc.ReviewAuthorityFor(ctx, h.pool, id)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Recorded() || a.Expired() {
		t.Fatalf("a claim recorded until %s is not in force now: %+v",
			until.UTC().Format(time.RFC3339), a)
	}
	if !a.ExpiresAt.Equal(until) {
		t.Errorf("the claim ends at %v, recorded until %v", a.ExpiresAt, until)
	}
	if got := def.CeilingWith(a.Recorded()); got != def.ReviewCeiling {
		t.Fatalf("the ceiling is %s while the authority is in force, want %s", got, def.ReviewCeiling)
	}

	h.clk.Advance(49 * time.Hour)

	a, err = svc.ReviewAuthorityFor(ctx, h.pool, id)
	if err != nil {
		t.Fatal(err)
	}
	if a.Recorded() {
		t.Fatal("a lapsed authority still counts as recorded.\n" +
			"Recorded() is the ONE gate the raised ceiling hangs on, so a claim that stays " +
			"true past its end goes on permitting r2 work indefinitely — which is the whole " +
			"of what PRD AGT-03's time-bound clause is about here")
	}
	if got := def.CeilingWith(a.Recorded()); got != def.MaxTier {
		t.Errorf("the ceiling is %s after the claim lapsed, want the pack's ordinary %s",
			got, def.MaxTier)
	}
	// Lapsed, not vanished. A ceiling that fell back with nothing saying why is
	// the same silence this feature exists to end.
	if !a.Expired() {
		t.Error("the lapsed claim does not report itself as expired, so no surface can say " +
			"why the ceiling moved")
	}
	if a.Holder != "R. Okonkwo" || a.RecordedBy != h.userID {
		t.Errorf("the lapsed claim lost who it was about: %+v", a)
	}
	if !strings.Contains(buf.String(), string(logx.EventReviewAuthorityExpired)) {
		t.Errorf("no %s audit event when the ceiling fell back.\nlog: %s",
			logx.EventReviewAuthorityExpired, buf.String())
	}
}

// An authority recorded with no end gets the default, never "forever".
//
// The same argument as a membership grant: if an unstated expiry meant permanent
// then every claim recorded from a form with no date field would be permanent,
// and the clause would hold only for the callers who remembered it.
func TestReviewAuthority_AnUndatedClaimGetsTheDefaultLifetime(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := civilProject(t, h)

	if err := h.svc.RecordReviewAuthority(ctx, h.pool, id,
		"R. Okonkwo", "", h.userID, time.Time{}); err != nil {
		t.Fatal(err)
	}
	a, err := h.svc.ReviewAuthorityFor(ctx, h.pool, id)
	if err != nil {
		t.Fatal(err)
	}
	if a.ExpiresAt.IsZero() {
		t.Fatal("an authority recorded with no stated end has none at all, so the ceiling it " +
			"raises never comes back down on its own")
	}
	want := h.clk.Now().Add(access.DefaultGrantLifetime)
	if d := a.ExpiresAt.Sub(want); d > time.Second || d < -time.Second {
		t.Errorf("the default claim ends at %s, want %s",
			a.ExpiresAt.UTC().Format(time.RFC3339), want.UTC().Format(time.RFC3339))
	}
}

// A claim that is already over is refused rather than stored.
//
// The same reasoning that already refuses a claim on a domain with no raised
// ceiling: storing it would leave the project carrying something that looks
// exactly like a raised ceiling and changes nothing.
func TestReviewAuthority_RefusesAClaimThatHasAlreadyEnded(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	id := civilProject(t, h)

	err := h.svc.RecordReviewAuthority(ctx, h.pool, id, "R. Okonkwo", "", h.userID,
		h.clk.Now().Add(-time.Hour))
	if err == nil {
		t.Fatal("an authority was recorded that had already expired")
	}
	if errs.CodeOf(err) != errs.CodeValidationFailed {
		t.Errorf("refused with %s", errs.CodeOf(err))
	}
	if a, _ := h.svc.ReviewAuthorityFor(ctx, h.pool, id); a.Recorded() || a.Expired() {
		t.Error("the refused claim was stored anyway")
	}
}

// Nothing in the pack table can raise a ceiling without an authority in force.
//
// Table-driven over every pack, so a domain added later with a ReviewCeiling and
// no thought about expiry fails here by name rather than shipping a ceiling that
// cannot come down.
func TestReviewAuthority_NoPackRaisesWithoutAnAuthorityInForce(t *testing.T) {
	for _, name := range pack.Names() {
		def, ok := pack.Lookup(name)
		if !ok {
			t.Fatalf("pack.Names lists %q and pack.Lookup does not know it", name)
		}
		var lapsed workspace.ReviewAuthority
		if got := def.CeilingWith(lapsed.Recorded()); got != def.MaxTier {
			t.Errorf("%s reaches %s with no authority in force, want its ordinary %s",
				def.Pack, got, def.MaxTier)
		}
	}
}
