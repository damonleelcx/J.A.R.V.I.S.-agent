package httpapi

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Who may do what on every goal, approval and export route, read from the route table
// itself and asked through the real router with real sessions.
//
// # Why this exists
//
// Each of these routes had its own permission fence, written when the route was:
// CreateGoal's (#91), Replan's, the export routes' (#99, #123). Nothing checked the set.
// A route added beside them — a second way to start a goal, a task list, an export
// format — would ship with whatever check its author remembered, and every existing
// fence would stay green. #91 was exactly that: POST /v1/goals had no check at all while
// its neighbours did.
//
// So the table below is keyed by the pattern strings in router.go, and the first test
// PARSES router.go: a goal, approval or export route with no row here fails by name, and
// so does a row whose route is gone. The second test runs every row as four people —
// nobody, a stranger to the project, a viewer of it, its owner — through NewRouter with
// bearer sessions, and checks the answer AND that a refused write changed nothing.
//
// # What is decided, and where it was decided
//
//   - Anonymous: 401 everywhere (RequireAuth).
//   - A stranger: 404 on anything naming a goal, approval, design or export, never 403,
//     which would confirm it exists (requirePermission). The two lists answer 200 with
//     nothing of the project in them.
//   - A viewer reads goals, timelines and approvals, and may export (#123: exporting is
//     reading), but may not create, replan or start a goal or decide an approval: 403,
//     and nothing written.
//   - The owner may do all of it, which is what makes a 404 above mean "refused" rather
//     than "the fixture is broken".

// accessRow is one route's decided answers. viewerMay is whether a viewer's request is
// allowed; a refused one must be 403 and leave the project as it was.
type accessRow struct {
	pattern   string
	viewerMay bool
	// list routes answer a stranger 200 with nothing in them, rather than 404.
	list bool
}

var goalExportAccess = []accessRow{
	{pattern: "GET /v1/goals", viewerMay: true, list: true},
	{pattern: "POST /v1/goals", viewerMay: false},
	{pattern: "POST /v1/goals/{id}/plan", viewerMay: false},
	{pattern: "POST /v1/goals/{id}/start", viewerMay: false},
	{pattern: "GET /v1/goals/{id}", viewerMay: true},
	{pattern: "GET /v1/goals/{id}/timeline", viewerMay: true},
	{pattern: "GET /v1/approvals", viewerMay: true, list: true},
	{pattern: "POST /v1/approvals/{id}", viewerMay: false},
	{pattern: "GET /v1/geometry/{id}/export", viewerMay: true},
	{pattern: "GET /v1/geometry/{id}/export/label", viewerMay: true},
	{pattern: "POST /v1/geometry/{id}/exports", viewerMay: true},
	{pattern: "GET /v1/geometry/{id}/{rest}", viewerMay: true},
	{pattern: "GET /v1/geometry/exports/{exportID}/file", viewerMay: true},
}

// isGoalOrExportRoute is which registered routes this fence owns.
func isGoalOrExportRoute(pattern string) bool {
	_, path, _ := strings.Cut(pattern, " ")
	switch {
	case strings.HasPrefix(path, "/v1/goals"), strings.HasPrefix(path, "/v1/approvals"):
		return true
	case strings.HasPrefix(path, "/v1/geometry"):
		// Every export route, and the {id}/{rest} route the export status is served
		// under because net/http will not register it by its own name (ExportRoute).
		return strings.Contains(path, "export") || strings.Contains(path, "{rest}")
	}
	return false
}

// registeredRoutes parses router.go for every mux.Handle / mux.HandleFunc pattern,
// and whether its handler is wrapped in authed(...).
func registeredRoutes(t *testing.T) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "router.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 2 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || (sel.Sel.Name != "Handle" && sel.Sel.Name != "HandleFunc") {
			return true
		}
		if x, ok := sel.X.(*ast.Ident); !ok || x.Name != "mux" {
			return true
		}
		lit, ok := call.Args[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		pattern, err := strconv.Unquote(lit.Value)
		if err != nil {
			t.Fatalf("router.go:%d: %v", fset.Position(lit.Pos()).Line, err)
		}
		wrapped := false
		if inner, ok := call.Args[1].(*ast.CallExpr); ok {
			if fn, ok := inner.Fun.(*ast.Ident); ok && fn.Name == "authed" {
				wrapped = true
			}
		}
		out[pattern] = wrapped
		return true
	})
	if len(out) < 20 {
		t.Fatalf("parsed only %d routes from router.go; the parse no longer sees the route table, "+
			"and this fence would pass on nothing", len(out))
	}
	return out
}

// A goal, approval or export route that is not in the table fails here by name, and so
// does a row whose route no longer exists. This is the half that catches a new route.
func TestAccessFence_EveryGoalApprovalAndExportRouteIsInTheAccessTable(t *testing.T) {
	routes := registeredRoutes(t)
	rows := map[string]bool{}
	for _, r := range goalExportAccess {
		rows[r.pattern] = true
	}
	var missing, stale, unauthed []string
	for pattern, wrapped := range routes {
		if !isGoalOrExportRoute(pattern) {
			continue
		}
		if !rows[pattern] {
			missing = append(missing, pattern)
		}
		if !wrapped {
			unauthed = append(unauthed, pattern)
		}
	}
	for pattern := range rows {
		if _, ok := routes[pattern]; !ok {
			stale = append(stale, pattern)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	sort.Strings(unauthed)
	if len(missing) > 0 {
		t.Errorf("router.go registers %d goal/approval/export route(s) this fence has no row for:\n  %s\n\n"+
			"Add each to goalExportAccess with the answer a viewer should get. A route with no row is a "+
			"route whose permission nobody has decided — POST /v1/goals shipped that way (#91).",
			len(missing), strings.Join(missing, "\n  "))
	}
	if len(stale) > 0 {
		t.Errorf("goalExportAccess has row(s) for route(s) router.go no longer registers:\n  %s",
			strings.Join(stale, "\n  "))
	}
	if len(unauthed) > 0 {
		t.Errorf("goal/approval/export route(s) registered without authed(...):\n  %s",
			strings.Join(unauthed, "\n  "))
	}
}

type accessFixture struct {
	router                           http.Handler
	pool                             *db.Pool
	owner, viewer, stranger          *identity.User
	tokens                           map[string]string
	project, goal, approval, version string
	export                           string
}

func newAccessFixture(t *testing.T) *accessFixture {
	t.Helper()
	url := os.Getenv("FORGE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("FORGE_TEST_DATABASE_URL is unset; skipping live-database tests. " +
			"Run `make db-up` then `make test-integration`.")
	}
	ctx := context.Background()
	schema := db.UniqueSchema("forge_http_access_fence", "")
	cfg := func(u string) config.DBConfig {
		return config.DBConfig{URL: u, MaxConns: 8, MinConns: 1,
			MaxConnLifetime: time.Hour, MaxConnIdleTime: time.Minute, ConnectTimeout: 10 * time.Second}
	}
	admin, err := db.Connect(ctx, cfg(url), logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	for _, q := range []string{"drop schema if exists " + schema + " cascade", "create schema " + schema} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	sep := "?"
	if strings.Contains(url, "?") {
		sep = "&"
	}
	pool, err := db.Connect(ctx, cfg(url+sep+"search_path="+schema), logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.MigrateFS(ctx, pool, db.Files, db.MigrationsDir, logx.Discard()); err != nil {
		t.Fatal(err)
	}
	// The schema name now carries this run id (db.UniqueSchema), so nothing
	// reuses it and leaving it behind leaks one schema per run.
	t.Cleanup(func() {
		pool.Close()
		db.DropTestSchema(url, schema)
	})

	d := testDeps()
	d.Pool = pool
	d.Clock = clock.System{}
	d.Access = access.NewService(pool, d.Clock, logx.Discard())
	d.Identity = identity.NewService(pool, identity.NewRepository(), nil, d.Config.Auth,
		d.Config.HTTP.PublicURL, d.Clock, logx.Discard())
	d.LLM = &permLLM{}
	d.Blobs = newExportTestStore()
	d.Config.Engine = config.EngineConfig{MaxTasksPerGoal: 50, MaxTaskDepth: 3,
		MaxTokensPerGoal: 100000, MaxCostCentsPerGoal: 500, MaxWallClockPerGoal: time.Hour}

	x := &accessFixture{router: NewRouter(d), pool: pool, tokens: map[string]string{}}
	x.owner = insertUser(t, pool, "access-owner@example.com")
	x.viewer = insertUser(t, pool, "access-viewer@example.com")
	x.stranger = insertUser(t, pool, "access-stranger@example.com")
	repo := identity.NewRepository()
	for _, u := range []*identity.User{x.owner, x.viewer, x.stranger} {
		tok, err := auth.NewToken()
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC().Add(time.Second) // after insertUser's password_changed_at
		if err := repo.CreateSession(ctx, pool, &identity.Session{ID: id.New(id.PrefixSession), UserID: u.ID,
			TokenHash: tok.Hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		x.tokens[u.ID] = tok.Plaintext
	}

	x.goal = seedGoal(t, pool, x.owner.ID, engine.GoalDraft, 1)
	x.project = permProjectOf(t, pool, x.goal)
	if err := d.Access.SetRole(ctx, access.Grant{ProjectID: x.project, UserID: x.viewer.ID,
		Role: access.RoleViewer, By: x.owner.ID}); err != nil {
		t.Fatal(err)
	}
	var taskID string
	if err := pool.QueryRow(ctx, `select id from forge_tasks where goal_id = $1`, x.goal).Scan(&taskID); err != nil {
		t.Fatal(err)
	}
	x.approval = id.New(id.PrefixApproval)
	if _, err := pool.Exec(ctx, `update forge_tasks set status = 'awaiting_approval', risk_tier = 'r2',
		requires_approval = true where id = $1`, taskID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into forge_approvals (id, goal_id, task_id, risk_tier, summary, preview, requested_at)
		values ($1,$2,$3,'r2','a gated task','{}'::jsonb,$4)`, x.approval, x.goal, taskID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	v, err := geometry.NewService(pool, d.Clock, logx.Discard()).Save(ctx, geometry.NewVariant{
		ProjectID: x.project, InitiatorID: x.owner.ID, Agent: workspace.AgentConverse, Generator: "test",
		Inputs: map[string]any{"message": "plate"}, Document: exportPlate()})
	if err != nil {
		t.Fatal(err)
	}
	x.version = v.VersionID

	// The export is requested by the owner through the router, so the status and file
	// routes have a real row to refuse; no worker runs, so it stays queued.
	rec := x.do("POST", "/v1/geometry/"+x.version+"/exports", x.owner, "")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("the owner's export request: %d %s", rec.Code, rec.Body.String())
	}
	x.export = exportIn(t, rec).ID
	return x
}

func (x *accessFixture) do(method, target string, who *identity.User, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	if who != nil {
		r.Header.Set("Authorization", "Bearer "+x.tokens[who.ID])
	}
	rec := httptest.NewRecorder()
	x.router.ServeHTTP(rec, r)
	return rec
}

// request is the concrete request a row's pattern is asked with.
func (x *accessFixture) request(pattern string) (method, target, body string) {
	method, path, _ := strings.Cut(pattern, " ")
	switch pattern {
	case "POST /v1/goals":
		body = `{"title":"access probe","statement":"probe the permission","project_id":"` + x.project + `"}`
	case "POST /v1/goals/{id}/plan":
		body = `{}`
	case "POST /v1/approvals/{id}":
		body = `{"decision":"approve","reason":"access probe"}`
	case "GET /v1/geometry/{id}/export", "GET /v1/geometry/{id}/export/label":
		path += "?format=step"
	}
	switch {
	case pattern == "GET /v1/geometry/{id}/{rest}":
		// The export status route: {id} is the literal "exports" and {rest} the export.
		path = "/v1/geometry/exports/" + x.export
	case strings.Contains(path, "{exportID}"):
		path = strings.Replace(path, "{exportID}", x.export, 1)
	case strings.HasPrefix(path, "/v1/goals/{id}"):
		path = strings.Replace(path, "{id}", x.goal, 1)
	case strings.HasPrefix(path, "/v1/approvals/{id}"):
		path = strings.Replace(path, "{id}", x.approval, 1)
	case strings.HasPrefix(path, "/v1/geometry/{id}"):
		path = strings.Replace(path, "{id}", x.version, 1)
	}
	return method, path, body
}

// state is what a refused write must leave as it was.
func (x *accessFixture) state(t *testing.T) string {
	t.Helper()
	var goals, plans int
	var status, decision string
	ctx := context.Background()
	if err := x.pool.QueryRow(ctx, `select (select count(*) from forge_goals where project_id = $1),
		(select count(*) from forge_plans p join forge_goals g on g.id = p.goal_id where g.project_id = $1),
		(select status from forge_goals where id = $2),
		(select decision from forge_approvals where id = $3)`,
		x.project, x.goal, x.approval).Scan(&goals, &plans, &status, &decision); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("goals=%d plans=%d goal=%s approval=%s", goals, plans, status, decision)
}

func TestAccessFence_GoalApprovalAndExportRoutesAnswerNobodyStrangerViewerAndOwnerAsDecided(t *testing.T) {
	x := newAccessFixture(t)

	for _, row := range goalExportAccess {
		t.Run(row.pattern, func(t *testing.T) {
			method, target, body := x.request(row.pattern)
			if strings.Contains(target, "{") {
				t.Fatalf("no concrete request for %s (got %s); add one to accessFixture.request", row.pattern, target)
			}

			if rec := x.do(method, target, nil, body); rec.Code != http.StatusUnauthorized {
				t.Errorf("anonymous %s %s: %d, want 401: %s", method, target, rec.Code, rec.Body.String())
			}

			before := x.state(t)
			rec := x.do(method, target, x.stranger, body)
			switch {
			case row.list:
				if rec.Code != http.StatusOK {
					t.Errorf("a stranger listing %s: %d, want 200 with nothing in it", target, rec.Code)
				}
				for _, private := range []string{x.goal, x.approval} {
					if strings.Contains(rec.Body.String(), private) {
						t.Errorf("a stranger's %s lists %s from a project they are not in", target, private)
					}
				}
			case rec.Code != http.StatusNotFound:
				t.Errorf("a stranger's %s %s: %d, want 404 (never 403, which confirms it exists): %s",
					method, target, rec.Code, rec.Body.String())
			}
			if after := x.state(t); after != before {
				t.Errorf("a stranger's refused %s %s changed the project: %s -> %s", method, target, before, after)
			}

			before = x.state(t)
			rec = x.do(method, target, x.viewer, body)
			if row.viewerMay {
				if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden || rec.Code == http.StatusNotFound {
					t.Errorf("a viewer's %s %s: %d; a viewer may do this (reads, and exports per #123): %s",
						method, target, rec.Code, rec.Body.String())
				}
				want := x.approval
				if target == "/v1/goals" {
					want = x.goal
				}
				if row.list && !strings.Contains(rec.Body.String(), want) {
					t.Errorf("a viewer's %s does not list %s, which is in their project: %s",
						target, want, rec.Body.String())
				}
			} else {
				if rec.Code != http.StatusForbidden {
					t.Errorf("a viewer's %s %s: %d, want 403: %s", method, target, rec.Code, rec.Body.String())
				}
				if after := x.state(t); after != before {
					t.Errorf("a viewer's refused %s %s changed the project: %s -> %s", method, target, before, after)
				}
			}

			rec = x.do(method, target, x.owner, body)
			if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden || rec.Code == http.StatusNotFound {
				t.Errorf("the owner's %s %s: %d; the owner may do everything here, so the refusals above "+
					"prove nothing: %s", method, target, rec.Code, rec.Body.String())
			}
		})
	}
}
