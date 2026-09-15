package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The build goal, as a person using it sees it (2026-09-15, a live exercise).
// Through the real handlers against Postgres, with a stub model.

// A build's tasks come back from GET /v1/goals/{id} in step order. The console
// renders them in the order they arrive, and listed a live five-step build as
// 1, 2, 5, 3, 4.
// docs/bugfix/2026-09-15-a-goals-tasks-were-listed-out-of-step-order.md
func TestGetGoal_ABuildsTasksAreListedInStepOrder(t *testing.T) {
	var steps []string
	for i := 1; i <= 12; i++ {
		steps = append(steps, fmt.Sprintf(`{"name":"part %d","what":"add part %d"}`, i, i))
	}
	stub := &buildLLM{replies: []string{`{"steps":[` + strings.Join(steps, ",") + `]}`}}
	h, _, user := buildHandlers(t, stub)

	rec := httptest.NewRecorder()
	h.CreateGoal(rec, postAs(user, "/v1/goals", `{"title":"Twelve","statement":"a twelve-part thing","build":true}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	goalID := decode(t, rec)["goal"].(map[string]any)["id"].(string)

	rec = httptest.NewRecorder()
	r := getAs(user, "/v1/goals/"+goalID)
	r.SetPathValue("id", goalID)
	h.GetGoal(rec, r)
	var got struct {
		Tasks []TaskDTO `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	var titles []string
	for _, task := range got.Tasks {
		titles = append(titles, task.Title)
	}
	if len(titles) != 12 {
		t.Fatalf("%d task(s) for twelve steps", len(titles))
	}
	for i, title := range titles {
		if !strings.HasPrefix(title, fmt.Sprintf("Step %d of 12", i+1)) {
			t.Fatalf("the goal lists its steps out of order:\n%s", strings.Join(titles, "\n"))
		}
	}
}

// A goal's token ceiling can be set when it is created, build or not, and is
// read back. Until now there was no field for it: a live exercise set it by SQL.
func TestCreateGoal_ATokenCeilingIsStoredOnTheGoal(t *testing.T) {
	stub := &buildLLM{tokens: 70, replies: []string{buildThreeSteps}}
	h, pool, user := buildHandlers(t, stub)

	rec := httptest.NewRecorder()
	h.CreateGoal(rec, postAs(user, "/v1/goals",
		`{"title":"A car","statement":"a car","build":true,"max_tokens":5000}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Goal GoalDTO `json:"goal"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	var stored *int64
	if err := pool.QueryRow(context.Background(),
		`select max_tokens from forge_goals where id = $1`, body.Goal.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == nil || *stored != 5000 {
		t.Errorf("the goal's max_tokens is %v; the request asked for 5000", stored)
	}
	if body.Goal.MaxTokens == nil || *body.Goal.MaxTokens != 5000 {
		t.Errorf("the goal does not read back its ceiling: %s", rec.Body.String())
	}
}

// A ceiling that is not positive, or is above the engine's own (100,000 in this
// harness), is refused before anything is written or any model is asked.
// BudgetGuard reads a goal's ceiling INSTEAD of the engine's, so a larger one
// would raise a deployment's limit from a request body.
func TestCreateGoal_ATokenCeilingOutOfRangeIsRefusedBeforeAnythingIsWritten(t *testing.T) {
	for _, build := range []bool{false, true} {
		for _, ceiling := range []int64{0, -5, 100_001} {
			t.Run(fmt.Sprintf("build=%v/%d", build, ceiling), func(t *testing.T) {
				stub := &buildLLM{replies: []string{buildThreeSteps}}
				h, pool, user := buildHandlers(t, stub)

				body, _ := json.Marshal(map[string]any{"title": "A car", "statement": "a car",
					"build": build, "max_tokens": ceiling})
				rec := httptest.NewRecorder()
				h.CreateGoal(rec, postAs(user, "/v1/goals", string(body)))

				if rec.Code != http.StatusBadRequest {
					t.Errorf("want 400, got %d: %s", rec.Code, rec.Body.String())
				}
				if !strings.Contains(rec.Body.String(), "ceiling") {
					t.Errorf("the refusal does not say what was wrong: %s", rec.Body.String())
				}
				var n int
				if err := pool.QueryRow(context.Background(),
					`select count(*) from forge_goals where created_by = $1`, user.ID).Scan(&n); err != nil {
					t.Fatal(err)
				}
				if n != 0 {
					t.Errorf("a refused ceiling left %d goal(s) behind", n)
				}
				if asked := stub.calls(); len(asked) != 0 {
					t.Errorf("the model was asked %d time(s) for a refused request", len(asked))
				}
			})
		}
	}
}
