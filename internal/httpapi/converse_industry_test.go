package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// The industry a person chose reaches the project the turn creates.
//
// # Why this exists on top of the geometry fences
//
// internal/domain/geometry proves that Save creates the project in the industry
// it is GIVEN. It passes whether or not anything ever gives it one — and the
// field is set in exactly one place, in this handler, from the request. That is
// the same shape as the defect this file's neighbour records: "requirementsFor
// may be correct and simply not called".
//
// The conversation path creates most FIRST projects, because a project is born
// the first time geometry is kept. If the handler stops passing the field, every
// project born in the browser silently returns to `general` and the selector on
// screen decides nothing — which is exactly the state this whole area removed.

func TestConverse_TheChosenIndustryReachesTheProject(t *testing.T) {
	w := workspaceHarness(t)
	ctx := context.Background()

	deps := w.h.deps
	deps.LLM = &prototypeLLM{}
	h := NewConverseHandlers(deps)

	// No project_id: this turn is the one that creates the project, which is the
	// whole point. A turn against an existing project cannot observe this.
	body := `{"message":"Sketch a studio massing.","project_id":"","industry":"Architecture"}`
	r := httptest.NewRequest("POST", "/v1/converse", strings.NewReader(body))
	r = r.WithContext(context.WithValue(ctx, ctxKeyUser, w.owner))
	rec := httptest.NewRecorder()
	h.Converse(rec, r)

	if !strings.Contains(rec.Body.String(), `"kind":"variant"`) {
		t.Fatalf("the turn saved no geometry, so no project was created: %d %s",
			rec.Code, rec.Body.String())
	}
	// The id arrives on the `variant` event, which is the event that exists
	// BECAUSE a project was created to hold the geometry.
	var created string
	for _, line := range strings.Split(rec.Body.String(), "\n") {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var payload struct {
			Kind    string `json:"kind"`
			Variant struct {
				ProjectID string `json:"project_id"`
			} `json:"variant"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &payload); err == nil &&
			payload.Kind == "variant" && payload.Variant.ProjectID != "" {
			created = payload.Variant.ProjectID
		}
	}
	if created == "" {
		t.Fatalf("the response never named the project it created:\n%s", rec.Body.String())
	}

	var pack string
	if err := w.pool.QueryRow(ctx,
		`select pack from forge_projects where id = $1`, created).Scan(&pack); err != nil {
		t.Fatal(err)
	}
	if pack != "architecture" {
		t.Errorf("a turn that chose Architecture created a project in pack %q.\n"+
			"The handler is the only place the choice becomes a value; if it stops passing "+
			"it, every project born in the browser is filed as Other and the selector on "+
			"screen decides nothing", pack)
	}
}

// The request can carry an industry at all.
//
// A structural fence, and it earns its place the way its neighbour does: the
// field is optional and unused on most turns, so nothing else would notice it
// being dropped in a tidy-up — and the browser is then unable to state a domain
// again, silently.
func TestConverse_TheRequestCanCarryAnIndustry(t *testing.T) {
	if !hasField(converseRequest{}, "Industry") {
		t.Fatal("converseRequest cannot carry an industry, so the workbench selector has " +
			"nowhere to send what a person picked")
	}
}
