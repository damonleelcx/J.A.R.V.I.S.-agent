package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	domainpack "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Service is the use-case layer over the workspace model.
type Service struct {
	pool   *db.Pool
	repo   *Repository
	engine *engine.Repository
	clock  clock.Clock
	log    *logx.Logger
}

// NewService wires the service.
func NewService(pool *db.Pool, clk clock.Clock, log *logx.Logger) *Service {
	return &Service{pool: pool, repo: NewRepository(), engine: engine.NewRepository(), clock: clk, log: log}
}

// Repo exposes the persistence port for callers already inside a transaction.
func (s *Service) Repo() *Repository { return s.repo }

// NewNode is a request to add something to the graph.
type NewNode struct {
	ProjectID string
	Kind      Kind
	Title     string
	Body      string
	// How is the epistemic label. Empty takes the kind's default — unlike memory,
	// where a default would be a lie told by omission, here the kind itself
	// carries the answer for most kinds (an assumption is assumed, full stop) and
	// the permitted set refuses anything dishonest.
	How       claim.Epistemic
	Source    string
	Status    Status
	CreatedBy string
}

// Add creates an owned node.
func (s *Service) Add(ctx context.Context, n NewNode) (*Node, error) {
	const op = "workspace.Service.Add"

	def, err := KindOf(n.Kind)
	if err != nil {
		return nil, err
	}
	if def.Anchor != AnchorNone {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("a %s lives in its own table; use Anchor to bring one into the graph rather than creating a copy of it here", n.Kind)
	}
	now := s.clock.Now()
	status := n.Status
	if status == "" {
		status = StatusOpen
	}
	node := &Node{
		ID: id.New(id.PrefixNode), ProjectID: n.ProjectID, Kind: n.Kind,
		Title: strings.TrimSpace(n.Title), Body: n.Body, How: n.How, Source: n.Source,
		Status: status, CreatedBy: n.CreatedBy, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repo.CreateNode(ctx, s.pool, node); err != nil {
		return nil, err
	}
	s.log.Info(ctx, logx.EventNodeAdded, "node_id", node.ID, "kind", string(node.Kind),
		"project_id", node.ProjectID, "how", string(node.How))
	return node, nil
}

// Anchor brings an existing row — a goal, decision, owner or artifact — into the
// graph, returning the anchor node.
//
// Find-or-create, and idempotent by the unique index rather than by a read: two
// callers anchoring the same goal at once must not produce two anchors, because
// every traversal would then return each of its edges twice and neither anchor
// would be the wrong one to delete.
func (s *Service) Anchor(ctx context.Context, projectID string, kind Kind, refID, createdBy string) (*Node, error) {
	return s.AnchorIn(ctx, s.pool, projectID, kind, refID, createdBy, "")
}

// AnchorIn is Anchor inside a transaction the caller owns, and commits nothing.
//
// title is optional and only meaningful for kinds whose anchored row has a name
// worth reading in a traversal — an artifact's path, for instance. Empty leaves
// the node titleless, which Graph.Title renders as "kind ref".
func (s *Service) AnchorIn(ctx context.Context, q db.Querier, projectID string, kind Kind,
	refID, createdBy, title string) (*Node, error) {
	const op = "workspace.Service.Anchor"

	def, err := KindOf(kind)
	if err != nil {
		return nil, err
	}
	if def.Anchor == AnchorNone {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("%s nodes own their content; there is nothing to anchor", kind)
	}
	now := s.clock.Now()
	node := &Node{
		ID: id.New(id.PrefixNode), ProjectID: projectID, Kind: kind, Title: title,
		How: def.Default, Status: StatusAccepted,
		CreatedBy: createdBy, CreatedAt: now, UpdatedAt: now,
	}
	ref := refID
	switch def.Anchor {
	case AnchorGoal:
		node.GoalID = &ref
	case AnchorDecision:
		node.DecisionID = &ref
	case AnchorOwner:
		node.OwnerID = &ref
	case AnchorArtifact:
		node.ArtifactID = &ref
	}
	// Find-or-create in one statement. The read-then-insert this used to do had
	// a window between the two, and closing it with a recovered unique violation
	// is only safe outside a transaction — see Repository.EnsureAnchor.
	return s.repo.EnsureAnchor(ctx, q, node)
}

// Relate draws a typed edge.
func (s *Service) Relate(ctx context.Context, kind EdgeKind, fromID, toID, note, createdBy string) (*Edge, error) {
	e := &Edge{
		ID: id.New(id.PrefixEdge), Kind: kind, FromID: fromID, ToID: toID,
		Note: note, CreatedBy: createdBy, CreatedAt: s.clock.Now(),
	}
	if err := s.repo.CreateEdge(ctx, s.pool, e); err != nil {
		return nil, err
	}
	return e, nil
}

// Promote creates a new node deriving from an existing one.
//
// # Why promotion is a new node and not an edit
//
// An assumption that turns out to be true does not BECOME a requirement. If it
// did, the graph would lose the only record that this requirement started life
// as a guess — and "what have we built on top of assumptions?" is the question
// the assumption kind exists to answer.
//
// So promotion adds a requirement, draws derives_from back to the assumption,
// and leaves the assumption in place with whatever status the caller gives it
// (usually retired: no longer in force, still readable). Both are true at once,
// which is what actually happened.
func (s *Service) Promote(ctx context.Context, fromID string, to NewNode, retireSource bool) (*Node, *Edge, error) {
	const op = "workspace.Service.Promote"

	source, err := s.repo.FindNode(ctx, s.pool, fromID)
	if err != nil {
		return nil, nil, err
	}
	if source.Kind.Anchored() {
		return nil, nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("a %s anchor is a pointer at another table, not a claim that can be promoted", source.Kind)
	}
	if to.ProjectID == "" {
		to.ProjectID = source.ProjectID
	}
	if to.ProjectID != source.ProjectID {
		return nil, nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("promotion stays inside one project")
	}
	if to.Title == "" {
		to.Title = source.Title
	}
	if to.Source == "" {
		to.Source = fmt.Sprintf("promoted from %s %s", source.Kind, source.ID)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	now := s.clock.Now()
	status := to.Status
	if status == "" {
		status = StatusOpen
	}
	node := &Node{
		ID: id.New(id.PrefixNode), ProjectID: to.ProjectID, Kind: to.Kind,
		Title: strings.TrimSpace(to.Title), Body: to.Body, How: to.How, Source: to.Source,
		Status: status, CreatedBy: to.CreatedBy, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repo.CreateNode(ctx, tx, node); err != nil {
		return nil, nil, err
	}
	edge := &Edge{
		ID: id.New(id.PrefixEdge), Kind: EdgeDerivesFrom,
		FromID: node.ID, ToID: source.ID,
		Note:      fmt.Sprintf("promoted from a %s", source.Kind),
		CreatedBy: to.CreatedBy, CreatedAt: now,
	}
	if err := s.repo.CreateEdge(ctx, tx, edge); err != nil {
		return nil, nil, err
	}
	if retireSource {
		source.Status = StatusRetired
		source.UpdatedAt = now
		if err := s.repo.UpdateNode(ctx, tx, source); err != nil {
			return nil, nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	s.log.Info(ctx, logx.EventNodePromoted, "from", source.ID, "from_kind", string(source.Kind),
		"to", node.ID, "to_kind", string(node.Kind), "source_retired", retireSource)
	return node, edge, nil
}

// Edit rewrites a node's content. Its kind is not a parameter, on purpose.
func (s *Service) Edit(ctx context.Context, nodeID, title, body string, how claim.Epistemic, status Status) (*Node, error) {
	node, err := s.repo.FindNode(ctx, s.pool, nodeID)
	if err != nil {
		return nil, err
	}
	if title != "" {
		node.Title = title
	}
	node.Body = body
	if how != "" {
		node.How = how
	}
	if status != "" {
		node.Status = status
	}
	node.UpdatedAt = s.clock.Now()
	if err := s.repo.UpdateNode(ctx, s.pool, node); err != nil {
		return nil, err
	}
	return node, nil
}

// Graph is a project's nodes and edges together.
type Graph struct {
	ProjectID string
	Nodes     []Node
	Edges     []Edge
}

// Load reads a project's whole graph.
func (s *Service) Load(ctx context.Context, projectID string) (*Graph, error) {
	nodes, err := s.repo.ListNodes(ctx, s.pool, NodeFilter{ProjectID: projectID})
	if err != nil {
		return nil, err
	}
	edges, err := s.repo.ListEdges(ctx, s.pool, projectID)
	if err != nil {
		return nil, err
	}
	return &Graph{ProjectID: projectID, Nodes: nodes, Edges: edges}, nil
}

// Title renders a node for a human, resolving anchors to something readable.
func (g *Graph) Title(nodeID string) string {
	for i := range g.Nodes {
		n := &g.Nodes[i]
		if n.ID != nodeID {
			continue
		}
		if n.Title != "" {
			return n.Title
		}
		if ref, ok := n.AnchorRef(); ok {
			return string(n.Kind) + " " + ref
		}
		return n.ID
	}
	return nodeID
}

// ---------------------------------------------------------------------------
// Review
// ---------------------------------------------------------------------------

// Finding is one thing the review noticed.
type Finding struct {
	// Problem is a named category so an operator can grep and a test can assert.
	Problem string
	NodeIDs []string
	Detail  string
}

// Review is what a project's graph says about itself.
//
// # Why defects and gaps are separate lists
//
// A dependency cycle is WRONG: the graph asserts something impossible and no
// amount of further work makes it true. A requirement with nothing verifying it
// is INCOMPLETE: the graph is telling the truth about a project that is not
// finished, which is the normal state of every project that has ever existed.
//
// Mixing them produces a report that is always red, and a check that is always
// red is a check nobody reads — the same reasoning that keeps pre-chain events
// out of the audit verifier's findings.
type Review struct {
	ProjectID string
	Nodes     int
	Edges     int
	// Defects are contradictions. A graph with any is lying about something.
	Defects []Finding
	// Gaps are absences. Expected, worth showing, never a failure.
	Gaps []Finding
}

// Sound reports whether the graph contradicts itself.
func (r Review) Sound() bool { return len(r.Defects) == 0 }

// Summary is one line for a human or a log.
func (r Review) Summary() string {
	if r.Sound() {
		return fmt.Sprintf("%d node(s), %d edge(s): consistent; %d gap(s) to close",
			r.Nodes, r.Edges, len(r.Gaps))
	}
	return fmt.Sprintf("%d node(s), %d edge(s): %d DEFECT(S), first: %s",
		r.Nodes, r.Edges, len(r.Defects), r.Defects[0].Detail)
}

// Review walks a project's graph and reports what it contradicts and what it
// is missing.
//
// It reads only what is stored and writes nothing. A review that repaired what
// it found would destroy the evidence it was run to gather — the same rule the
// audit verifier follows.
func (s *Service) Review(ctx context.Context, projectID string) (*Review, error) {
	g, err := s.Load(ctx, projectID)
	if err != nil {
		return nil, err
	}
	rev := &Review{ProjectID: projectID, Nodes: len(g.Nodes), Edges: len(g.Edges)}

	byID := map[string]*Node{}
	for i := range g.Nodes {
		byID[g.Nodes[i].ID] = &g.Nodes[i]
	}
	out := map[string][]Edge{}
	in := map[string][]Edge{}
	for _, e := range g.Edges {
		out[e.FromID] = append(out[e.FromID], e)
		in[e.ToID] = append(in[e.ToID], e)
	}

	// --- defect: a dependency cycle ---
	//
	// depends_on is the one edge where a cycle is impossible rather than merely
	// unusual: A cannot need B first if B needs A first. Detected with an
	// iterative walk rather than recursion, because a graph with a cycle is
	// exactly the input that would blow a recursive stack.
	if cycle := findCycle(g.Edges, EdgeDependsOn); len(cycle) > 0 {
		names := make([]string, 0, len(cycle))
		for _, nid := range cycle {
			names = append(names, g.Title(nid))
		}
		rev.Defects = append(rev.Defects, Finding{
			Problem: "dependency-cycle", NodeIDs: cycle,
			Detail: "these depend on each other in a loop, so none of them can be done first: " +
				strings.Join(names, " → "),
		})
	}

	for i := range g.Nodes {
		n := &g.Nodes[i]

		// --- defect: something accepted that rests only on a guess ---
		//
		// The epistemic vocabulary earning its keep on the graph. A requirement
		// somebody has AGREED TO, whose every input is assumed, is a commitment
		// made on the strength of nobody having said otherwise. That is not a
		// gap in the work; it is a false impression of solidity, which is why it
		// is a defect.
		if n.Status == StatusAccepted && !n.Kind.Anchored() {
			if guessed, sources := restsOnlyOnGuesses(n, out[n.ID], byID); guessed {
				rev.Defects = append(rev.Defects, Finding{
					Problem: "accepted-on-assumption", NodeIDs: append([]string{n.ID}, sources...),
					Detail: fmt.Sprintf("%q is accepted but everything it derives from is assumed; "+
						"it reads as settled and rests on nobody having said otherwise", n.Title),
				})
			}
		}

		// --- gap: nothing verifies this ---
		if n.Kind == KindRequirement || n.Kind == KindCriterion {
			if !hasIncoming(in[n.ID], EdgeVerifies) {
				rev.Gaps = append(rev.Gaps, Finding{
					Problem: "unverified", NodeIDs: []string{n.ID},
					Detail: fmt.Sprintf("nothing verifies %q — no test or evidence points at it", n.Title),
				})
			}
		}
		// --- gap: nothing mitigates this ---
		if n.Kind == KindRisk || n.Kind == KindHazard {
			if !hasIncoming(in[n.ID], EdgeMitigates) && n.Status != StatusRetired {
				rev.Gaps = append(rev.Gaps, Finding{
					Problem: "unmitigated", NodeIDs: []string{n.ID},
					Detail: fmt.Sprintf("nothing mitigates the %s %q", n.Kind, n.Title),
				})
			}
		}
		// --- gap: nobody owns this ---
		if n.Kind == KindRequirement || n.Kind == KindComponent || n.Kind == KindHazard {
			if !hasIncoming(in[n.ID], EdgeOwns) {
				rev.Gaps = append(rev.Gaps, Finding{
					Problem: "unowned", NodeIDs: []string{n.ID},
					Detail: fmt.Sprintf("no owner is accountable for %q", n.Title),
				})
			}
		}
	}

	sortFindings(rev.Defects)
	sortFindings(rev.Gaps)
	return rev, nil
}

// restsOnlyOnGuesses reports whether every derives_from input of a node is an
// assumption, and names them.
//
// "Only" matters: a node deriving from one assumption and one measurement is
// not resting on a guess, it is resting on a measurement with a guess beside it.
// A node with no inputs at all is not reported either — it rests on nothing
// recorded, which is a different observation and not this one.
func restsOnlyOnGuesses(n *Node, outgoing []Edge, byID map[string]*Node) (bool, []string) {
	var sources []string
	for _, e := range outgoing {
		if e.Kind != EdgeDerivesFrom {
			continue
		}
		target, ok := byID[e.ToID]
		if !ok {
			continue
		}
		if target.Kind != KindAssumption && target.How != claim.Assumed {
			return false, nil
		}
		sources = append(sources, target.ID)
	}
	return len(sources) > 0, sources
}

func hasIncoming(edges []Edge, kind EdgeKind) bool {
	for _, e := range edges {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// findCycle returns one cycle over a single edge kind, or nil.
//
// Iterative depth-first with an explicit stack: the input that produces a cycle
// is exactly the input that would recurse forever, so the algorithm that detects
// it must not be the one that falls over on it.
func findCycle(edges []Edge, kind EdgeKind) []string {
	adj := map[string][]string{}
	for _, e := range edges {
		if e.Kind == kind {
			adj[e.FromID] = append(adj[e.FromID], e.ToID)
		}
	}
	const (
		white = 0 // unvisited
		grey  = 1 // on the current path
		black = 2 // finished
	)
	colour := map[string]int{}
	var path []string

	roots := make([]string, 0, len(adj))
	for from := range adj {
		roots = append(roots, from)
	}
	sort.Strings(roots) // deterministic: the same graph reports the same cycle

	type frame struct {
		node string
		next int
	}
	for _, root := range roots {
		if colour[root] != white {
			continue
		}
		stack := []frame{{root, 0}}
		colour[root] = grey
		path = []string{root}

		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			if top.next < len(adj[top.node]) {
				child := adj[top.node][top.next]
				top.next++
				switch colour[child] {
				case grey:
					// Found it. The cycle is the tail of the path from child on.
					for i, n := range path {
						if n == child {
							return append(append([]string{}, path[i:]...), child)
						}
					}
					return []string{child}
				case white:
					colour[child] = grey
					stack = append(stack, frame{child, 0})
					path = append(path, child)
				}
				continue
			}
			colour[top.node] = black
			stack = stack[:len(stack)-1]
			if len(path) > 0 {
				path = path[:len(path)-1]
			}
		}
	}
	return nil
}

func sortFindings(f []Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].Problem != f[j].Problem {
			return f[i].Problem < f[j].Problem
		}
		return f[i].Detail < f[j].Detail
	})
}

// ---------------------------------------------------------------------------
// Projects
// ---------------------------------------------------------------------------

// EnsureProject returns projectID, creating a project when it is empty.
//
// # Why this exists rather than a second INSERT
//
// Two things now need "a project to put this in, making one if there is not
// one": drafting a goal (internal/agent/intake.go) and keeping a geometry
// variant from the workbench (PRD VIS-04). Each writing its own INSERT means two
// producers of the same row, and the two would drift on the parts that are easy
// to forget — the membership row in particular, without which the person who
// just created a project cannot see it.
//
// The caller supplies the pack, because the pack is not a label: it selects
// which validators, safety policy and approval rules apply to everything inside
// the project (PRD §7), and defaulting it here would be this function choosing a
// rule set on somebody else's behalf.
func (s *Service) EnsureProject(ctx context.Context, q db.Querier, projectID, ownerID, name, pack string) (string, error) {
	const op = "workspace.Service.EnsureProject"

	if strings.TrimSpace(projectID) != "" {
		return projectID, nil
	}
	if strings.TrimSpace(ownerID) == "" {
		return "", errs.New(op, errs.CodeValidationFailed).
			WithDetail("a project must name its owner")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errs.New(op, errs.CodeValidationFailed).
			WithDetail("a project must be named; the schema refuses a blank one")
	}
	if strings.TrimSpace(pack) == "" {
		return "", errs.New(op, errs.CodeValidationFailed).
			WithDetail("a project must declare its domain pack, which selects the rules that apply inside it")
	}
	// PRD SEC-07. The pack was free text until now: a typo, a pack this build has
	// never heard of and a regulated domain were all equally acceptable and
	// equally inert, so the column recorded a claim about which rules applied
	// while no rule ever consulted it.
	//
	// The closed set is checked here rather than by a database constraint because
	// the refusal has to explain itself — "violates check constraint" tells
	// somebody nothing about why medicine is not on the list.
	def, known := domainpack.Lookup(pack)
	if !known {
		return "", errs.New(op, errs.CodeValidationFailed).
			WithDetail("%q is not a domain pack this build knows. A pack nothing recognises selects no "+
				"rules while looking like it selected some. Available here: %s",
				pack, strings.Join(domainpack.AvailableNames(), ", "))
	}
	if !def.Available {
		// Refused rather than gated, and deliberately not switchable by
		// configuration — see internal/domain/pack. An environment variable that
		// turned on patient-specific clinical use would make a regulated boundary
		// something an operator crosses by editing a file.
		return "", errs.New(op, errs.CodeForbidden).
			WithDetail("this build is not validated for the %q pack, so a project cannot be created in "+
				"it.\n\n%s\n\nIt would require %s.\n\nAvailable here: %s",
				def.Pack, def.Summary, def.Requires, strings.Join(domainpack.AvailableNames(), ", "))
	}
	now := s.clock.Now()
	newID := id.New(id.PrefixProject)
	if _, err := q.Exec(ctx,
		`insert into forge_projects (id, owner_id, name, pack, created_at, updated_at)
		 values ($1,$2,$3,$4,$5,$5)`, newID, ownerID, name, pack, now); err != nil {
		return "", errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	// The creator becomes the project's owner in the MEMBERSHIP table, which is
	// what authorisation reads (PRD SEC-02). owner_id above records who made it;
	// without this row they could not see what they had just created.
	if err := access.NewService(nil, s.clock, s.log).EnsureOwner(ctx, q, newID, ownerID); err != nil {
		return "", err
	}
	return newID, nil
}

// ---------------------------------------------------------------------------
// Artifacts (PRD WRK-04)
// ---------------------------------------------------------------------------

// Change is one recorded modification to an artifact, carrying WRK-04's seven.
type Change struct {
	ProjectID string
	Path      string
	Kind      ArtifactKind

	InitiatorID string
	Agent       Agent
	ToolCallID  *string
	Inputs      any
	Diff        string

	// GoalID, when set, links this change to the timeline — and through it to
	// the audit chain. This is the thing the chain was built for.
	GoalID string
	// TaskID narrows the event further when there is one.
	TaskID *string
	// Summary is the event's one line.
	Summary string

	// DerivedFrom names graph nodes this change was PRODUCED FROM — the
	// requirements and constraints whose recorded words went into the work
	// (PRD VIS-01, WRK-03).
	//
	// Ids are treated as untrusted: only those that turn out to be nodes of THIS
	// project are linked. See deriveFromIn.
	DerivedFrom []string
}

// RecordChange appends a version and, when the change belongs to a goal, writes
// the timeline event it points at.
//
// # Why the event and the version are written together
//
// The version's whole claim is that it can be traced. If the event were written
// separately, a crash between the two would leave a version pointing at nothing
// or an event describing a change with no record — and both look exactly like a
// normal row afterwards. One transaction makes "the change happened" and "the
// change was recorded" the same fact.
func (s *Service) RecordChange(ctx context.Context, c Change) (*Artifact, *Version, error) {
	const op = "workspace.Service.RecordChange"

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	artifact, version, err := s.RecordChangeIn(ctx, tx, c)
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	s.LogVersioned(ctx, artifact, version)
	return artifact, version, nil
}

// RecordChangeIn appends a version inside a transaction the CALLER owns, and
// commits nothing.
//
// # Why this exists
//
// Some artifacts have content that lives in a second table — geometry is the
// first (PRD VIS-04, migration 0011). Writing the version in one transaction and
// its content in another would leave, after a crash between them, a version
// claiming to be a variant with no geometry behind it. Afterwards it looks
// exactly like a normal row, which is the failure mode this whole path is
// arranged to avoid.
//
// The caller commits, and the caller logs: this function deliberately does not
// announce a change that may still be rolled back.
func (s *Service) RecordChangeIn(ctx context.Context, tx db.Querier, c Change) (*Artifact, *Version, error) {
	const op = "workspace.Service.RecordChangeIn"

	inputs, err := json.Marshal(orEmptyObject(c.Inputs))
	if err != nil {
		return nil, nil, errs.Wrap(op, errs.CodeSerializationFail, err).
			WithDetail("the inputs recorded for a change to %q cannot be encoded as JSON", c.Path)
	}
	kind := c.Kind
	if kind == "" {
		kind = ArtifactFile
	}
	now := s.clock.Now()

	artifact, err := s.repo.FindOrCreateArtifact(ctx, tx, &Artifact{
		ID: id.New(id.PrefixArtifact), ProjectID: c.ProjectID, Path: c.Path,
		Kind: kind, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return nil, nil, err
	}

	version := &Version{
		ID: id.New(id.PrefixVersion), ArtifactID: artifact.ID,
		InitiatorID: c.InitiatorID, Agent: c.Agent, ToolCallID: c.ToolCallID,
		Inputs: inputs, Diff: c.Diff,
		Verification: Unverified, Disposition: Pending,
		CreatedAt: now,
	}

	if c.GoalID != "" {
		summary := c.Summary
		if summary == "" {
			summary = "changed " + c.Path
		}
		payload, _ := json.Marshal(map[string]any{
			"artifact_id": artifact.ID, "path": artifact.Path, "agent": string(c.Agent),
		})
		ev := &engine.Event{
			GoalID: c.GoalID, TaskID: c.TaskID, Kind: engine.EventArtifactChanged,
			Actor: engine.Actor(c.Agent), ActorID: &c.InitiatorID,
			Summary: summary, Payload: payload,
		}
		if err := s.engine.AppendEvent(ctx, tx, ev, now); err != nil {
			return nil, nil, err
		}
		version.EventID = &ev.ID
	}

	if err := s.repo.AppendVersion(ctx, tx, version); err != nil {
		return nil, nil, err
	}

	// PRD WRK-03: the project graph spans files as well as requirements and
	// tests. KindArtifact has existed to hold them since the workspace model was
	// written, and nothing produced one — so every artifact this system recorded
	// belonged to no graph, and the only way to reach one was an id somebody
	// already had. A file that cannot be found from its project is a file the
	// canvas cannot show (PRD WRK-01).
	//
	// # Why this is inside the transaction rather than best effort
	//
	// Because the alternative is the failure this repository keeps finding. An
	// anchor written after the commit, or written and allowed to fail quietly,
	// makes the graph SILENTLY PARTIAL: most artifacts appear, some do not, and
	// nothing distinguishes "this project has no files" from "the anchor write
	// lost a race". A listing that is usually complete is worse than one that is
	// either complete or absent, because nobody checks the former.
	//
	// The write is built so it cannot fail for reasons of its own: the node's
	// shape is fixed here rather than supplied, and the insert is ON CONFLICT DO
	// NOTHING, so the only way it fails is the database being unreachable — in
	// which case the version write beside it has failed too.
	//
	// The title is the path, and it stays true: an artifact is IDENTIFIED by its
	// path (FindOrCreateArtifact looks it up that way), so a rename produces a
	// different artifact rather than a stale title on this one.
	anchor, err := s.AnchorIn(ctx, tx, artifact.ProjectID, KindArtifact,
		artifact.ID, c.InitiatorID, artifact.Path)
	if err != nil {
		return nil, nil, err
	}
	if err := s.deriveFromIn(ctx, tx, anchor, c.DerivedFrom, c.InitiatorID); err != nil {
		return nil, nil, err
	}
	return artifact, version, nil
}

// deriveFromIn records what this change was produced from, in the caller's
// transaction.
//
// # Why derives_from and not satisfies
//
// The workbench's "build from" flow reads a requirement's own words into the
// turn and the model proposes geometry. What is true afterwards is that the
// geometry WAS PRODUCED FROM that requirement. What is NOT true is that it meets
// it: nothing checked that, and satisfies reads "this meets that".
//
// Recording satisfies would be the system asserting an unverified claim on its
// own behalf — the exact thing the split between Verification and Disposition
// exists to prevent, and the exact thing PRD RSN-06 forbids. It would also be
// read that way by anything that traverses the graph later: a requirement would
// look met because somebody once described it out loud.
//
// derives_from is the provenance edge and says only what happened. If the shape
// does turn out to meet the requirement, a person or a test says so, with the
// edge that means it.
//
// # Why the ids are filtered rather than trusted
//
// They arrive from a client. A node that is not in this project cannot be linked
// truthfully at ALL, and attempting it would fail a foreign key or the
// cross-project check — inside a transaction that is carrying the artifact
// version, which would then be lost to a stray id. So the nodes are resolved
// first, in one read, and only real ones are linked.
//
// # Why the write is all-or-nothing for the ones that DO resolve
//
// Same argument as the anchor above. Provenance that is usually recorded is
// worse than provenance that is either recorded or absent, because nobody
// checks the former. EnsureEdge cannot fail for a reason of its own — the
// endpoints have just been read, and the insert is ON CONFLICT DO NOTHING, so a
// second turn built from the same requirement is a no-op rather than a
// transaction-aborting unique violation.
//
// What was dropped is logged. A named id that resolved to nothing is a client
// and a server disagreeing about what exists, and it must not pass in silence.
func (s *Service) deriveFromIn(ctx context.Context, q db.Querier, anchor *Node,
	from []string, createdBy string) error {

	if len(from) == 0 || anchor == nil {
		return nil
	}
	nodes, err := s.repo.ListNodes(ctx, q, NodeFilter{ProjectID: anchor.ProjectID, IDs: from})
	if err != nil {
		return err
	}
	found := map[string]bool{}
	for i := range nodes {
		found[nodes[i].ID] = true
	}

	var unresolved []string
	for _, id := range from {
		// The artifact's own anchor is skipped rather than refused: the schema
		// forbids a self-edge, and a caller naming it has said something
		// meaningless rather than something wrong.
		if !found[id] && id != anchor.ID {
			unresolved = append(unresolved, id)
		}
	}
	if len(unresolved) > 0 {
		s.log.Warn(ctx, logx.EventProvenanceUnresolved,
			"project_id", anchor.ProjectID, "artifact_node_id", anchor.ID,
			"unresolved", strings.Join(unresolved, ","),
			"detail", "this change named nodes to derive from that are not in this project; "+
				"they are not linked and the provenance recorded is therefore incomplete")
	}

	now := s.clock.Now()
	for i := range nodes {
		if nodes[i].ID == anchor.ID {
			continue
		}
		e := &Edge{
			ID: id.New(id.PrefixEdge), ProjectID: anchor.ProjectID, Kind: EdgeDerivesFrom,
			FromID: anchor.ID, ToID: nodes[i].ID,
			Note:      "generated from this at the workbench",
			CreatedBy: createdBy, CreatedAt: now,
		}
		if err := s.repo.EnsureEdge(ctx, q, e); err != nil {
			return err
		}
	}
	return nil
}

// LogVersioned announces a committed version. Exported because RecordChangeIn
// hands the transaction to its caller, and the caller is therefore the only one
// that knows the change survived — a rolled-back change must never appear in
// the log as one that happened.
func (s *Service) LogVersioned(ctx context.Context, artifact *Artifact, version *Version) {
	s.log.Info(ctx, logx.EventArtifactVersioned, "artifact_id", artifact.ID,
		"path", artifact.Path, "version", version.Version, "agent", string(version.Agent),
		"event_id", derefOr(version.EventID, "none"))
}

// Verify records what a machine found about a version. It does not touch the
// disposition; see the type comments on Verification and Disposition.
func (s *Service) Verify(ctx context.Context, versionID string, state Verification, note string) error {
	if err := s.repo.SetVerification(ctx, s.pool, versionID, state, note); err != nil {
		return err
	}
	s.log.Info(ctx, logx.EventArtifactVerified, "version_id", versionID, "state", string(state))
	return nil
}

// Dispose records what a person decided about a version.
func (s *Service) Dispose(ctx context.Context, versionID string, d Disposition, byUserID, reason string) error {
	if err := s.repo.SetDisposition(ctx, s.pool, versionID, d, byUserID, reason, s.clock.Now()); err != nil {
		return err
	}
	s.log.Info(ctx, logx.EventArtifactDispositioned,
		"version_id", versionID, "disposition", string(d), "by", byUserID)
	return nil
}

// History is an artifact with its versions, newest first.
type History struct {
	Artifact Artifact
	Versions []Version
}

// History returns an artifact's whole lifecycle.
func (s *Service) History(ctx context.Context, artifactID string) (*History, error) {
	a, err := s.repo.FindArtifact(ctx, s.pool, artifactID)
	if err != nil {
		return nil, err
	}
	versions, err := s.repo.ListVersions(ctx, s.pool, artifactID)
	if err != nil {
		return nil, err
	}
	return &History{Artifact: *a, Versions: versions}, nil
}

func orEmptyObject(v any) any {
	if v == nil {
		return map[string]any{}
	}
	return v
}

func derefOr(p *string, fallback string) string {
	if p == nil || *p == "" {
		return fallback
	}
	return *p
}
