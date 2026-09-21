package buildinfo

import "testing"

// ‼️ A build that was not stamped says so; a build that was reports exactly what
// it was stamped with (2026-09-20).
//
// The half that matters is the first one. An image built without the version
// arguments used to report version "dev", which reads like an answer and is not
// one: it is indistinguishable from a developer's laptop build, and an operator
// comparing it against a release tag has nothing to compare. Every unstamped
// field is now the same word, so "is this stamped?" is answerable by looking.
func TestBuildInfo_SaysUnknownRatherThanSomethingThatLooksLikeAVersion(t *testing.T) {
	restore := Get()
	t.Cleanup(func() { Set(restore.Version, restore.Commit, restore.Date) })

	// Nothing set at all.
	Set("", "", "")
	got := Get()
	if got.Version != Unknown || got.Commit != Unknown || got.Date != Unknown {
		t.Errorf("an unstamped build reads %+v, want every field %q", got, Unknown)
	}
	if got.Stamped() {
		t.Error("an unstamped build claims to be stamped")
	}
	if s := got.String(); s != "unknown (commit unknown, built unknown)" {
		t.Errorf("an unstamped build prints %q", s)
	}

	// The placeholders a Go build leaves behind are not versions either. "dev"
	// is the one this repository shipped in every image it ever built.
	for _, placeholder := range []string{"dev", "DEV", " dev ", "(devel)", "unknown", "none"} {
		Set(placeholder, placeholder, placeholder)
		if g := Get(); g.Version != Unknown || g.Stamped() {
			t.Errorf("%q is reported as a version: %+v", placeholder, g)
		}
	}

	// A real stamp is passed through untouched — no normalising, no
	// prettifying, because it has to compare equal to what the image was
	// labelled with.
	Set("v0.9.1-12-gab12cd3", "ab12cd3", "2026-09-20T09:41:07Z")
	got = Get()
	if got.Version != "v0.9.1-12-gab12cd3" || got.Commit != "ab12cd3" || got.Date != "2026-09-20T09:41:07Z" {
		t.Errorf("a stamped build reads %+v, want the three values it was given", got)
	}
	if !got.Stamped() {
		t.Error("a stamped build does not say it is stamped")
	}
	if s := got.String(); s != "v0.9.1-12-gab12cd3 (commit ab12cd3, built 2026-09-20T09:41:07Z)" {
		t.Errorf("a stamped build prints %q", s)
	}

	// A commit with no tag is the ordinary case for an image built off a
	// branch, and it is still an answer.
	Set("", "ab12cd3", "")
	if g := Get(); !g.Stamped() || g.Version != Unknown || g.Commit != "ab12cd3" {
		t.Errorf("a commit-only build reads %+v and stamped=%v", g, g.Stamped())
	}
}
