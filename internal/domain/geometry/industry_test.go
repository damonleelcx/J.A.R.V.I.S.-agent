package geometry_test

import (
	"context"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
)

// A project created by keeping a variant records the domain that was chosen.
//
// # The hole this closes
//
// Save passed the CONSTANT "general" to EnsureProject, with a comment saying
// nothing in a workbench conversation had declared a domain. That was true when
// nothing could. Once the workbench gained an industry selector it stopped being
// true — and this is the path that creates most first projects, because a
// project is born the first time geometry is kept, which happens long before any
// work is proposed.
//
// So half the product could state an industry and half could not, and the half
// that could not was the half people use first.

func keptWithIndustry(t *testing.T, h *harness, industry string) string {
	t.Helper()
	n := h.proposal("bracket", plate("base", 60))
	// No project: this is the call that creates one, which is the whole point.
	n.ProjectID = ""
	n.Industry = industry
	v, err := h.svc.Save(context.Background(), n)
	if err != nil {
		t.Fatalf("keeping a variant with industry %q: %v", industry, err)
	}
	return v.ProjectID
}

func packOfProject(t *testing.T, h *harness, projectID string) string {
	t.Helper()
	var got string
	if err := h.pool.QueryRow(context.Background(),
		`select pack from forge_projects where id = $1`, projectID).Scan(&got); err != nil {
		t.Fatalf("reading the pack: %v", err)
	}
	return got
}

func TestSave_CreatesTheProjectInTheIndustryChosen(t *testing.T) {
	h := newHarness(t)

	for _, tc := range []struct{ given, want string }{
		{"Civil engineering", "civil"},
		{"Architecture", "architecture"},
		{"product-design", "product-design"},
	} {
		id := keptWithIndustry(t, h, tc.given)
		if got := packOfProject(t, h, id); got != tc.want {
			t.Errorf("a variant kept under %q created a project in pack %q, expected %q.\n"+
				"This is the path that creates most first projects; if it cannot carry the "+
				"domain, the selector on screen decides nothing", tc.given, got, tc.want)
		}
	}
}

// An unstated industry is `general` — the pack that MEANS unknown — as before.
//
// The half that keeps this safe to ship: a client that sends nothing behaves
// exactly as it did, and `general` is not a fallback invented to fill a hole. It
// lowers autonomy and triggers expert review, which is the right answer when
// nobody said.
func TestSave_UnstatedIndustryIsStillGeneral(t *testing.T) {
	h := newHarness(t)
	if got := packOfProject(t, h, keptWithIndustry(t, h, "")); got != "general" {
		t.Errorf("a variant kept with no industry created a project in %q", got)
	}
}

// An industry this build does not know is refused, not silently downgraded.
//
// Falling back to `general` here would be the original defect: work proceeding
// under rules nobody chose, in a project that looks exactly like one whose
// domain was decided.
func TestSave_RefusesAnIndustryThisBuildDoesNotKnow(t *testing.T) {
	h := newHarness(t)
	n := h.proposal("bracket", plate("base", 60))
	n.ProjectID = ""
	n.Industry = "cryptocurrency"

	if _, err := h.svc.Save(context.Background(), n); err == nil {
		t.Fatal("a project was created in an industry this build has never heard of")
	}
}

var _ = workspace.AgentConverse
