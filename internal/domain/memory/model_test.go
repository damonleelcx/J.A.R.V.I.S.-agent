package memory

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

func ptr(s string) *string { return &s }

// MEM-01 names five layers. A test that enumerates them is what stands between
// "five layers with distinct retention" and someone adding a sixth that quietly
// shares another's lifetime.
func TestLayers_AreExactlyTheFiveThePRDNames(t *testing.T) {
	want := map[Scope]string{
		ScopeTurn:         "turn context",
		ScopeSession:      "session notes",
		ScopeProject:      "project knowledge",
		ScopeUser:         "personal preferences",
		ScopeOrganisation: "org knowledge",
	}
	got := Layers()
	if len(got) != len(want) {
		t.Fatalf("there are %d layers; PRD MEM-01 names %d", len(got), len(want))
	}
	for _, l := range got {
		prd, ok := want[l.Scope]
		if !ok {
			t.Fatalf("layer %q is not one MEM-01 names", l.Scope)
		}
		if l.PRDName != prd {
			t.Fatalf("layer %q says it is the PRD's %q; MEM-01 calls it %q", l.Scope, l.PRDName, prd)
		}
		if strings.TrimSpace(l.Gloss) == "" {
			t.Fatalf("layer %q has no gloss; a layer nobody can explain will be used for the wrong thing", l.Scope)
		}
		delete(want, l.Scope)
	}
	for missing := range want {
		t.Fatalf("MEM-01 names layer %q and it does not exist", missing)
	}
}

// "Distinct retention and sharing" is the whole point of layering. Two layers
// with the same lifetime AND the same audience are one layer with two names.
func TestLayers_RetentionAndSharingActuallyDiffer(t *testing.T) {
	seen := map[string]Scope{}
	for _, l := range Layers() {
		k := l.DefaultTTL.String() + "|" + string(l.Visibility)
		if prior, dup := seen[k]; dup {
			t.Fatalf("layers %q and %q have identical retention (%s) and audience (%s); one of them is not a layer",
				prior, l.Scope, l.DefaultTTL, l.Visibility)
		}
		seen[k] = l.Scope
	}
}

// The short-lived layers are the half MEM-01 added over what 0004 shipped, and
// the reason they exist is that they go away. A turn layer with no TTL is
// project knowledge wearing a different label.
func TestLayers_TheShortLivedOnesExpire(t *testing.T) {
	for _, sc := range []Scope{ScopeTurn, ScopeSession} {
		l, err := LayerOf(sc)
		if err != nil {
			t.Fatal(err)
		}
		if l.DefaultTTL <= 0 {
			t.Fatalf("%s memory has no default lifetime; it would outlive what it describes", sc)
		}
	}
	for _, sc := range []Scope{ScopeProject, ScopeUser, ScopeOrganisation} {
		l, _ := LayerOf(sc)
		if l.DefaultTTL != 0 {
			t.Fatalf("%s memory expires after %s; durable knowledge that evaporates is worse than none", sc, l.DefaultTTL)
		}
	}
}

// Personal preference is the one layer where leaking is the failure people
// actually mind, so it is the one whose audience is asserted by name.
func TestLayers_PersonalMemoryIsNeverShared(t *testing.T) {
	l, err := LayerOf(ScopeUser)
	if err != nil {
		t.Fatal(err)
	}
	if l.Visibility != VisibilityOwnerOnly {
		t.Fatalf("personal memory is visible to %q; MEM-01 says it is personal", l.Visibility)
	}
	if l.Owner != OwnerUser {
		t.Fatalf("personal memory is owned by a %s; it must be owned by the user or nobody can delete their own", l.Owner)
	}
}

// An unknown layer must not fall back to a known one. The safest-looking
// default — durable and widely shared — is the most dangerous one there is.
func TestLayerOf_UnknownScopeIsRefusedNotDefaulted(t *testing.T) {
	if _, err := LayerOf("everything"); err == nil {
		t.Fatal("an unrecognised layer was accepted; it would inherit some other layer's retention and audience")
	}
	if Scope("").Valid() {
		t.Fatal("the empty scope is valid")
	}
}

func TestItem_ExpiredIsNotLive(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Minute)
	i := &Item{Key: "k", ExpiresAt: &past}
	if err := i.Live(now); err == nil {
		t.Fatal("an item an hour past its expiry reported itself live")
	}
}

// Pinning is a user's standing instruction to keep something. It has to beat
// the layer's lifetime or it means nothing.
func TestItem_PinnedOutlivesItsExpiry(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	i := &Item{Key: "k", ExpiresAt: &past, Pinned: true}
	if err := i.Live(now); err != nil {
		t.Fatalf("a pinned item was dropped at its expiry: %v", err)
	}
}

// A forgotten item is dead even if it is pinned and unexpired: the user's
// deletion outranks both.
func TestItem_ForgottenBeatsPinned(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	at := now.Add(-time.Minute)
	i := &Item{Key: "k", Pinned: true, ForgottenAt: &at}
	err := i.Live(now)
	if err == nil {
		t.Fatal("a forgotten item was still live because it happened to be pinned")
	}
	if !errs.Is(err, errs.CodeMemoryForgotten) {
		t.Fatalf("a forgotten item reported %s; the reader needs to know it was deleted, not that it expired", errs.CodeOf(err))
	}
}

// The rule the package will not bend: an item read back next month with no
// epistemic label is indistinguishable from a measurement.
func TestItem_UnlabelledMemoryIsRefused(t *testing.T) {
	i := &Item{Scope: ScopeProject, ProjectID: ptr("prj_1"), Key: "k", Value: []byte(`1`)}
	if err := i.Validate(); err == nil {
		t.Fatal("an item with no epistemic label was accepted")
	}
	i.How = "vibes"
	if err := i.Validate(); err == nil {
		t.Fatal("an item with an invented epistemic label was accepted")
	}
	i.How = claim.Observed
	if err := i.Validate(); err != nil {
		t.Fatalf("a properly labelled item was refused: %v", err)
	}
}

// An item nobody owns is an item nobody can delete, which defeats MEM-02
// before any of its verbs are called.
func TestItem_OwnedLayersRequireTheirOwner(t *testing.T) {
	for _, tc := range []struct {
		scope Scope
		set   func(*Item)
	}{
		{ScopeTurn, func(i *Item) { i.GoalID = ptr("gol_1") }},
		{ScopeSession, func(i *Item) { i.GoalID = ptr("gol_1") }},
		{ScopeProject, func(i *Item) { i.ProjectID = ptr("prj_1") }},
		{ScopeUser, func(i *Item) { i.UserID = ptr("usr_1") }},
	} {
		bare := &Item{Scope: tc.scope, Key: "k", Value: []byte(`1`), How: claim.Observed}
		if err := bare.Validate(); err == nil {
			t.Fatalf("%s memory was accepted with no owner", tc.scope)
		}
		owned := &Item{Scope: tc.scope, Key: "k", Value: []byte(`1`), How: claim.Observed}
		tc.set(owned)
		if err := owned.Validate(); err != nil {
			t.Fatalf("%s memory was refused with its owner set: %v", tc.scope, err)
		}
	}

	// Organisation memory is the exception, and it is one on purpose: there is
	// nothing narrower for it to point at.
	org := &Item{Scope: ScopeOrganisation, Key: "k", Value: []byte(`1`), How: claim.Observed}
	if err := org.Validate(); err != nil {
		t.Fatalf("organisation memory was refused for having no owner: %v", err)
	}
}

// MEM-02's third verb. Every reason must produce a sentence that names what
// matched — an explanation nobody can read is not an explanation.
func TestReason_EveryReasonExplainsItself(t *testing.T) {
	item := &Item{Scope: ScopeProject, Key: "bolt.size"}
	for _, why := range []Reason{ReasonExactKey, ReasonPrefix, ReasonPinned, ReasonLayer} {
		detail := explain(why, item, "bolt")
		if strings.TrimSpace(detail) == "" {
			t.Fatalf("reason %q has no explanation", why)
		}
		if strings.Contains(detail, "defect") {
			t.Fatalf("reason %q fell through to the unrecognised branch", why)
		}
	}
	// And an unrecognised one says so rather than inventing a plausible reason.
	if !strings.Contains(explain("because", item, ""), "defect") {
		t.Fatal("an unrecognised retrieval reason produced a confident-sounding explanation")
	}
}

// An alternative with no reason for its rejection is worse than no alternative:
// it looks like the option was weighed when nothing records that it was.
func TestDecision_RejectedOptionsMustSayWhy(t *testing.T) {
	d := &Decision{
		ProjectID: "prj_1", AuthorID: "usr_1", Title: "t", Decision: "do it",
		DecidedAt:    time.Now().UTC(),
		Alternatives: []Alternative{{Option: "the other way"}},
	}
	if err := d.Validate(); err == nil {
		t.Fatal("an alternative with no why_not was accepted")
	}
	d.Alternatives[0].WhyNot = "needs a solver we do not have"
	if err := d.Validate(); err != nil {
		t.Fatalf("a fully stated alternative was refused: %v", err)
	}
}

// A decision must be attributable. An unattributed decision cannot be
// questioned, which is the only thing the log is for.
func TestDecision_MustNameAProjectAndAnAuthor(t *testing.T) {
	base := func() *Decision {
		return &Decision{ProjectID: "prj_1", AuthorID: "usr_1", Title: "t",
			Decision: "do it", DecidedAt: time.Now().UTC()}
	}
	d := base()
	d.AuthorID = ""
	if err := d.Validate(); err == nil {
		t.Fatal("a decision with no author was accepted")
	}
	d = base()
	d.ProjectID = ""
	if err := d.Validate(); err == nil {
		t.Fatal("a decision with no project was accepted")
	}
	d = base()
	d.Decision = "   "
	if err := d.Validate(); err == nil {
		t.Fatal("a decision that records no decision was accepted")
	}
}

// Evidence goes through the same vocabulary as everything else, which means a
// recalled figure gets named as one rather than passing as a source.
func TestDecision_EvidenceIsLabelledByTheSameRules(t *testing.T) {
	d := &Decision{
		ProjectID: "prj_1", AuthorID: "usr_1", Title: "t", Decision: "do it",
		DecidedAt: time.Now().UTC(),
		Evidence:  []claim.Claim{{Statement: "NEMA 17 is 42.3 mm square", How: claim.Retrieved}},
	}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	got := d.Evidence[0]
	if got.Source == "" {
		t.Fatal("a retrieved claim with no source survived into the decision log unmarked")
	}
	if got.Actionableish() {
		t.Fatal("a figure recalled from model weights was recorded as evidence a reader may act on")
	}
}

func TestExportItem_SerialisesTheEpistemicGloss(t *testing.T) {
	e := ExportItem{Key: "k", Value: json.RawMessage(`1`), How: string(claim.Assumed),
		HowMeans: claim.Assumed.Gloss()}
	blob, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(blob), "how_means") {
		t.Fatal("an export omitted what its epistemic labels mean; six months later nobody can read it")
	}
}
