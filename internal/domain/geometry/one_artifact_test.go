package geometry_test

import (
	"context"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// A save that PINS its artifact (decided 2026-09-15).
//
// A geometry version lands on the artifact its document's NAME resolves to, and
// for a conversational turn it still does — that rule is what makes "make it
// taller" version 2 of the bracket. A build goal is the exception: its steps are
// one piece of work, so it names the artifact outright and a step that renames
// the model appends to the history the build opened rather than starting a
// second one (#119, workspace.Service.artifactFor).
//
// Against real Postgres, because every claim here is about which rows exist:
// which artifact a version hangs off, which path that artifact has, and what one
// project can reach in another.

// otherProject is a second project owned by the same person, for the fence that
// a pinned artifact of another project is refused.
func (h *harness) otherProject(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	p := id.New(id.PrefixProject)
	if _, err := h.pool.Exec(ctx,
		`insert into forge_projects (id, owner_id, name, created_at, updated_at) values ($1,$2,'Q',$3,$3)`,
		p, h.userID, h.clk.Now()); err != nil {
		t.Fatal(err)
	}
	if err := access.NewService(h.pool, h.clk, logx.Discard()).
		EnsureOwner(ctx, h.pool, p, h.userID); err != nil {
		t.Fatal(err)
	}
	return p
}

// A pinned save appends to THAT artifact, and stores the document's new name.
//
// Both halves matter. If the artifact did not win, a build's rename would open a
// second history; if the name were not stored, the rename would be invisible and
// the version would claim the model is still called what it was called before.
func TestSave_APinnedSaveAppendsToThatArtifactAndStillStoresTheNewName(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first, err := h.svc.Save(ctx, h.proposal("desk lamp base", plate("p", 60)))
	if err != nil {
		t.Fatal(err)
	}
	renamed := h.proposal("desk lamp", plate("p", 60))
	renamed.ArtifactID = first.ArtifactID
	second, err := h.svc.Save(ctx, renamed)
	if err != nil {
		t.Fatal(err)
	}

	if second.ArtifactID != first.ArtifactID {
		t.Errorf("the pinned save landed on artifact %s rather than %s, so the two are two histories "+
			"instead of one", second.ArtifactID, first.ArtifactID)
	}
	if second.Version != 2 {
		t.Errorf("the pinned save is v%d; it must append to the artifact it named", second.Version)
	}
	if second.Path != first.Path {
		t.Errorf("the artifact's path became %q; a pinned save appends to a history and does not rename "+
			"it, or the versions before the rename would move", second.Path)
	}
	// The rename, where a reader finds it: on the version.
	if second.Name != "desk lamp" || second.Document.Name != "desk lamp" {
		t.Errorf("the version says the model is called %q/%q, not %q — the rename happened and the record "+
			"denies it", second.Name, second.Document.Name, "desk lamp")
	}
	stored, err := h.svc.Find(ctx, second.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != "desk lamp" || stored.ArtifactID != first.ArtifactID {
		t.Errorf("read back, the version is %q on artifact %s", stored.Name, stored.ArtifactID)
	}
}

// A CONVERSATIONAL save after a rename still starts a second artifact.
//
// Unchanged behaviour, fenced because the pin exists now: a turn has no history
// of its own to belong to, so the name is the only thing that says which
// history it is a revision of. Pinning here would need a variant id threaded
// through the client and trusted on the way back.
func TestSave_AConversationalSaveAfterARenameStillStartsASecondArtifact(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first, err := h.svc.Save(ctx, h.proposal("desk lamp base", plate("p", 60)))
	if err != nil {
		t.Fatal(err)
	}
	// No ArtifactID: exactly what the workbench conversation sends.
	renamed, err := h.svc.Save(ctx, h.proposal("desk lamp", plate("p", 60)))
	if err != nil {
		t.Fatal(err)
	}
	if renamed.ArtifactID == first.ArtifactID {
		t.Fatal("a renamed conversational proposal appended to the old artifact; a turn lands on the " +
			"artifact its name resolves to, and changing that would move a history nobody asked to move")
	}
	if renamed.Version != 1 || renamed.Path == first.Path {
		t.Errorf("the renamed proposal is v%d at %q; it starts a history of its own", renamed.Version, renamed.Path)
	}
}

// A pinned artifact belonging to ANOTHER PROJECT is refused.
//
// ‼️ Authorisation is per project. A version filed into another project's
// artifact would be invisible to everyone in the project it was made in, and
// visible to readers of a project it does not belong to — and there is no second
// door further down to stop at.
func TestSave_APinnedArtifactFromAnotherProjectIsRefused(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	elsewhere := h.otherProject(t)
	away := h.proposal("desk lamp base", plate("p", 60))
	away.ProjectID = elsewhere
	theirs, err := h.svc.Save(ctx, away)
	if err != nil {
		t.Fatal(err)
	}

	mine := h.proposal("desk lamp", plate("p", 60))
	mine.ArtifactID = theirs.ArtifactID
	if _, err := h.svc.Save(ctx, mine); err == nil {
		t.Fatal("a variant was appended to another project's artifact")
	} else if !errs.Is(err, errs.CodeValidationFailed) {
		t.Errorf("appending to another project's artifact failed with %v; it is a refusal of what was "+
			"asked, not an outage", err)
	}
	// Nothing was written there. A refusal that still appends is the disclosure
	// this fence exists to prevent, one version later.
	var versions int
	if err := h.pool.QueryRow(ctx,
		`select count(*) from forge_artifact_versions where artifact_id = $1`, theirs.ArtifactID).
		Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if versions != 1 {
		t.Errorf("the other project's artifact holds %d version(s), want the 1 it made itself", versions)
	}
}

// Adopting appends to the artifact the source is a version OF, even when the
// document's name says otherwise.
//
// It used to reach that artifact by keeping the name, which is the same answer
// only while a name and an artifact are the same fact. On a build's artifact
// they are not, and adopting by name would put the copy in a history the
// "already current" check above it never looked at.
func TestAdopt_AppendsToTheArtifactTheSourceIsOnAfterARename(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	// Three versions on one pinned history, the way a three-step build leaves
	// them: opened under one name, renamed at the second step, renamed again at
	// the third.
	first, err := h.svc.Save(ctx, h.proposal("desk lamp base", plate("p", 60)))
	if err != nil {
		t.Fatal(err)
	}
	renamed := h.proposal("desk lamp", plate("p", 72))
	renamed.ArtifactID = first.ArtifactID
	middle, err := h.svc.Save(ctx, renamed)
	if err != nil {
		t.Fatal(err)
	}
	newest := h.proposal("desk lamp with shade", plate("p", 80))
	newest.ArtifactID = first.ArtifactID
	if _, err := h.svc.Save(ctx, newest); err != nil {
		t.Fatal(err)
	}

	// ‼️ The version adopted is the RENAMED one — v2 — and deliberately not v1.
	//
	// Adopting v1 proves nothing about this: v1's own name still resolves to the
	// artifact v1 is on, so the name rule and the artifact rule AGREE there, and
	// a copy made by either lands in the same place. This fence was written that
	// way first and its drill stayed green for exactly that reason (the same trap
	// as the sweep-twist fence in scripts/drill-fences.sh). v2 is where the two
	// rules disagree: its name says desk-lamp.forge.json and its artifact says
	// desk-lamp-base.forge.json.
	//
	// It is also not the current version, which Adopt refuses — hence the third
	// save above.
	adopted, err := h.svc.Adopt(ctx, middle.VersionID, h.userID, "the lamp without the shade was better")
	if err != nil {
		t.Fatal(err)
	}
	if adopted.ArtifactID != first.ArtifactID {
		t.Errorf("the adopted copy landed on artifact %s (%s) rather than %s (%s), so the design a person "+
			"chose is not the newest version of the thing they were choosing for",
			adopted.ArtifactID, adopted.Path, first.ArtifactID, first.Path)
	}
	if adopted.Version != 4 {
		t.Errorf("the adopted copy is v%d of its artifact; it must be the newest", adopted.Version)
	}
	if adopted.Path != first.Path {
		t.Errorf("the adopted copy is at %q rather than %q; adopting brings a variant forward inside one "+
			"history and does not start another", adopted.Path, first.Path)
	}
}

// Re-specifying appends to the source's own artifact, for the same reason.
func TestRespec_AppendsToTheArtifactTheSourceIsOnAfterARename(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()

	first, err := h.svc.Save(ctx, h.parametricProposal("desk lamp base"))
	if err != nil {
		t.Fatal(err)
	}
	renamed := h.parametricProposal("desk lamp")
	renamed.ArtifactID = first.ArtifactID
	second, err := h.svc.Save(ctx, renamed)
	if err != nil {
		t.Fatal(err)
	}

	next, _, err := h.svc.Respec(ctx, second.VersionID, h.userID, map[string]float64{"plate_size": 80})
	if err != nil {
		t.Fatal(err)
	}
	if next.ArtifactID != first.ArtifactID {
		t.Errorf("the re-specified variant is artifact %s rather than %s, so it cannot be compared with "+
			"the variant it was computed from", next.ArtifactID, first.ArtifactID)
	}
	if next.Version != 3 {
		t.Errorf("the re-specified variant is v%d; it must append", next.Version)
	}
}
