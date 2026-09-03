// Package workspace owns the reasoning structure, the project graph and the
// artifact lifecycle (PRD RSN-01, WRK-03, WRK-04).
//
// # Why these three are one package
//
// RSN-01 asks for goals, requirements, constraints, assumptions, decisions,
// risks and success criteria. WRK-03 asks for requirements, components,
// interfaces, files, tests, hazards, decisions, owners and evidence. Both lists
// contain requirements and decisions, so two packages would hold two copies of
// each from the first day and they would disagree as soon as anybody edited one.
//
// WRK-04 joins them because an artifact is a graph node with a history: WRK-03's
// "files" and WRK-04's "artifacts" are the same thing seen from two angles.
//
// # The three properties this package is built to hold
//
//  1. A node's KIND never changes. An assumption that turns out to be true does
//     not become a requirement — a requirement is created deriving from it. The
//     whole value of the label is that somebody can later ask what was built on
//     top of a guess, and mutating the kind erases the question.
//
//  2. Edges are typed, and which kinds may connect which is a table. An untyped
//     graph answers "is there a line between these" and nothing else.
//
//  3. Verification state and human disposition are different columns, forever.
//     "The tests passed" and "a person accepted it" are different facts, and one
//     column for both eventually reports a machine's opinion as a person's.
package workspace

import (
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Kind is what a node is. The values are stored in forge_nodes.kind and
// constrained by forge_nodes_kind_check.
type Kind string

const (
	// --- RSN-01: the reasoning structure ---

	// KindRequirement is something the work must do.
	KindRequirement Kind = "requirement"
	// KindConstraint is a bound the work must stay inside.
	KindConstraint Kind = "constraint"
	// KindAssumption is something taken as true because nobody said otherwise.
	KindAssumption Kind = "assumption"
	// KindRisk is something that might go wrong, and what it would cost.
	KindRisk Kind = "risk"
	// KindCriterion is how anyone would know the work succeeded.
	KindCriterion Kind = "criterion"

	// --- WRK-03: the structural half ---

	// KindComponent is a part of the thing being built.
	KindComponent Kind = "component"
	// KindInterface is a boundary two components agree on.
	KindInterface Kind = "interface"
	// KindTest is a check that can be run.
	KindTest Kind = "test"
	// KindHazard is a source of harm. Distinct from a risk on purpose: a hazard
	// is the sharp edge, a risk is the chance somebody touches it. The PRD names
	// both (RSN-01 risks, WRK-03 hazards) and collapsing them would lose the
	// distinction safety work is built on.
	KindHazard Kind = "hazard"
	// KindEvidence is something that supports a claim: a run, a measurement, a
	// document.
	KindEvidence Kind = "evidence"

	// --- anchors: content lives in its own table ---

	// KindGoal anchors forge_goals.
	KindGoal Kind = "goal"
	// KindDecision anchors forge_decisions (PRD MEM-03).
	KindDecision Kind = "decision"
	// KindOwner anchors forge_users. WRK-03's "owners".
	KindOwner Kind = "owner"
	// KindArtifact anchors forge_artifacts. WRK-03 calls these "files"; the term
	// used throughout is artifact, because a file on disk is only one shape.
	KindArtifact Kind = "artifact"
)

// Anchor says which table a kind's content lives in, when it is not this one.
type Anchor string

const (
	// AnchorNone — the graph owns this node. Title and body are in the row.
	AnchorNone     Anchor = ""
	AnchorGoal     Anchor = "goal_id"
	AnchorDecision Anchor = "decision_id"
	AnchorOwner    Anchor = "owner_id"
	AnchorArtifact Anchor = "artifact_id"
)

// KindDef is one row of the node vocabulary.
//
// A table rather than a switch, for the third time in this codebase and for the
// same reason: adding a kind means adding a row, and what each kind obliges is
// readable side by side instead of scattered across the call sites that happen
// to care.
type KindDef struct {
	Kind Kind
	// PRDName is the word the PRD uses, when it differs from ours.
	PRDName string
	// Anchor is empty when the graph owns this node's content.
	Anchor Anchor
	// Allowed is the epistemic labels this kind may carry (PRD RSN-05). Empty
	// means any of the seven.
	//
	// The one that matters is KindAssumption: exactly {assumed}. An assumption
	// filed as "observed" is not an assumption, and permitting it would let the
	// most important distinction in the graph be erased by a typo.
	Allowed []claim.Epistemic
	// Default is the label used when a writer names none.
	Default claim.Epistemic
	Gloss   string
}

var kinds = []KindDef{
	{KindRequirement, "requirement", AnchorNone,
		[]claim.Epistemic{claim.Retrieved, claim.Observed, claim.Proposed}, claim.Retrieved,
		"something the work must do"},
	{KindConstraint, "constraint", AnchorNone,
		[]claim.Epistemic{claim.Retrieved, claim.Observed, claim.Calculated, claim.Proposed}, claim.Retrieved,
		"a bound the work must stay inside"},
	{KindAssumption, "assumption", AnchorNone,
		[]claim.Epistemic{claim.Assumed}, claim.Assumed,
		"taken as true because nobody said otherwise"},
	{KindRisk, "risk", AnchorNone,
		nil, claim.Inferred,
		"something that might go wrong, and what it would cost"},
	{KindCriterion, "success criterion", AnchorNone,
		[]claim.Epistemic{claim.Retrieved, claim.Proposed, claim.Observed}, claim.Proposed,
		"how anyone would know the work succeeded"},
	{KindComponent, "component", AnchorNone,
		nil, claim.Proposed, "a part of the thing being built"},
	{KindInterface, "interface", AnchorNone,
		nil, claim.Proposed, "a boundary two components agree on"},
	{KindTest, "test", AnchorNone,
		nil, claim.Proposed, "a check that can be run"},
	{KindHazard, "hazard", AnchorNone,
		nil, claim.Inferred, "a source of harm; the sharp edge, not the chance of touching it"},
	{KindEvidence, "evidence", AnchorNone,
		[]claim.Epistemic{claim.Observed, claim.Calculated, claim.Simulated, claim.Retrieved}, claim.Observed,
		"something that supports a claim: a run, a measurement, a document"},

	{KindGoal, "goal", AnchorGoal, nil, claim.Proposed, "an objective; lives in forge_goals"},
	{KindDecision, "decision", AnchorDecision, nil, claim.Proposed, "a recorded choice; lives in forge_decisions"},
	{KindOwner, "owner", AnchorOwner, nil, claim.Observed, "an accountable person; lives in forge_users"},
	{KindArtifact, "file / artifact", AnchorArtifact, nil, claim.Observed, "a thing produced; lives in forge_artifacts"},
}

// Kinds returns the vocabulary, reasoning kinds first.
func Kinds() []KindDef { return append([]KindDef(nil), kinds...) }

// KindOf returns the definition for a kind.
//
// Unknown kinds are an error, never a default. A node that fell back to some
// other kind's rules would carry that kind's epistemic permissions, and the
// permissive-looking default is the dangerous one.
func KindOf(k Kind) (KindDef, error) {
	for _, d := range kinds {
		if d.Kind == k {
			return d, nil
		}
	}
	return KindDef{}, errs.New("workspace.KindOf", errs.CodeValidationFailed).
		WithDetail("%q is not a node kind; the kinds are %s", k, strings.Join(kindNames(), ", "))
}

// Valid reports whether k is one of the fourteen.
func (k Kind) Valid() bool { _, err := KindOf(k); return err == nil }

// Anchored reports whether this kind's content lives in another table.
func (k Kind) Anchored() bool {
	d, err := KindOf(k)
	return err == nil && d.Anchor != AnchorNone
}

func kindNames() []string {
	out := make([]string, 0, len(kinds))
	for _, d := range kinds {
		out = append(out, string(d.Kind))
	}
	return out
}

// Status is where a node stands. Deliberately not "done": a requirement is not
// finished, it is agreed to or it is not.
type Status string

const (
	StatusOpen     Status = "open"
	StatusAccepted Status = "accepted"
	StatusRejected Status = "rejected"
	// StatusRetired is no longer in force but kept, because "we used to require
	// this" is the question a graph is asked six months later.
	StatusRetired Status = "retired"
)

// Valid reports whether s is a recognised status.
func (s Status) Valid() bool {
	return s == StatusOpen || s == StatusAccepted || s == StatusRejected || s == StatusRetired
}

// Node is one thing in the graph.
type Node struct {
	ID        string
	ProjectID string
	Kind      Kind
	Title     string
	Body      string
	How       claim.Epistemic
	Source    string
	Status    Status

	// Exactly one is set on an anchor kind, none on an owned kind.
	GoalID     *string
	DecisionID *string
	OwnerID    *string
	ArtifactID *string

	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// AnchorRef returns the id this node anchors, and true when it anchors one.
func (n *Node) AnchorRef() (string, bool) {
	for _, p := range []*string{n.GoalID, n.DecisionID, n.OwnerID, n.ArtifactID} {
		if p != nil && *p != "" {
			return *p, true
		}
	}
	return "", false
}

// Validate checks a node against its kind before it is written.
func (n *Node) Validate() error {
	const op = "workspace.Node.Validate"

	def, err := KindOf(n.Kind)
	if err != nil {
		return err
	}
	if strings.TrimSpace(n.ProjectID) == "" || strings.TrimSpace(n.CreatedBy) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a node must name its project and who created it; an unattributed node cannot be questioned")
	}
	if !n.Status.Valid() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("node status %q is not one of open, accepted, rejected, retired", n.Status)
	}

	if def.Anchor == AnchorNone {
		if strings.TrimSpace(n.Title) == "" {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("a %s needs a title; an untitled node shows up in every traversal and tells nobody anything", n.Kind)
		}
		if _, anchored := n.AnchorRef(); anchored {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("a %s owns its own content and must not anchor another row", n.Kind)
		}
	} else {
		ref, anchored := n.AnchorRef()
		if !anchored {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("a %s anchors a row in another table and must name it", n.Kind)
		}
		if err := n.anchorMatchesKind(def, ref); err != nil {
			return err
		}
	}

	if n.How == "" {
		n.How = def.Default
	}
	if !n.How.Valid() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("node %q carries epistemic label %q, which is not one of the seven", n.Title, n.How)
	}
	if len(def.Allowed) > 0 && !allows(def.Allowed, n.How) {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("%s %s cannot be %s. %s. Permitted: %s. "+
				"If it is known some other way, it is a different kind of node — create one and derive it from this.",
				articleFor(string(n.Kind)), n.Kind, n.How, def.Gloss, labelList(def.Allowed))
	}
	return nil
}

// anchorMatchesKind checks the right column is the one that is set.
func (n *Node) anchorMatchesKind(def KindDef, ref string) error {
	const op = "workspace.Node.Validate"

	set := map[Anchor]*string{
		AnchorGoal: n.GoalID, AnchorDecision: n.DecisionID,
		AnchorOwner: n.OwnerID, AnchorArtifact: n.ArtifactID,
	}
	if p := set[def.Anchor]; p == nil || *p == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a %s anchor must set %s; this one set a different column, so the anchor points at the wrong kind of thing",
				n.Kind, def.Anchor)
	}
	for a, p := range set {
		if a != def.Anchor && p != nil && *p != "" {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("a %s anchor set both %s and %s; an anchor names exactly one row", n.Kind, def.Anchor, a)
		}
	}
	_ = ref
	return nil
}

// articleFor returns "a" or "an". Error messages are read by people under
// pressure, and "a assumption" makes a reader trust the rest of the sentence
// less than they should.
func articleFor(word string) string {
	if word == "" {
		return "a"
	}
	switch word[0] {
	case 'a', 'e', 'i', 'o', 'u':
		return "an"
	}
	return "a"
}

func allows(set []claim.Epistemic, e claim.Epistemic) bool {
	for _, a := range set {
		if a == e {
			return true
		}
	}
	return false
}

func labelList(set []claim.Epistemic) string {
	out := make([]string, 0, len(set))
	for _, e := range set {
		out = append(out, string(e))
	}
	return strings.Join(out, ", ")
}

// ---------------------------------------------------------------------------
// Edges
// ---------------------------------------------------------------------------

// EdgeKind is a typed relation.
type EdgeKind string

const (
	// EdgeDerivesFrom — this was produced from that. The provenance edge, and
	// the one that makes an immutable Kind workable: promoting an assumption to
	// a requirement is a new node deriving from the old one.
	EdgeDerivesFrom EdgeKind = "derives_from"
	// EdgeSatisfies — this meets that.
	EdgeSatisfies EdgeKind = "satisfies"
	// EdgeVerifies — this CHECKS that. Distinct from satisfies, because "it is
	// built" and "it was checked" are different facts (PRD AGT-08).
	EdgeVerifies EdgeKind = "verifies"
	// EdgeDependsOn — this needs that first. The only edge where a cycle is a
	// defect rather than a shape, so it is the one the cycle check walks.
	EdgeDependsOn EdgeKind = "depends_on"
	// EdgeConstrains — that is bounded by this.
	EdgeConstrains EdgeKind = "constrains"
	// EdgeMitigates — this reduces that risk or hazard.
	EdgeMitigates EdgeKind = "mitigates"
	// EdgeOwns — this person is accountable for that.
	EdgeOwns EdgeKind = "owns"
	// EdgeEvidences — this supports that.
	EdgeEvidences EdgeKind = "evidences"
)

// EdgeDef declares one relation and which kinds it may connect.
//
// # Why the pairings are a table and not a check constraint
//
// The rule is two-dimensional — eight edge kinds against fourteen node kinds —
// and a SQL constraint spelling it out would be forty lines nobody could read or
// change. The database enforces the vocabulary (an unknown edge kind is
// refused); this enforces the pairings, and a fence asserts the two agree.
//
// The point of the pairings is that "test verifies requirement" and "requirement
// verifies test" are not both sentences. A graph that accepts either is a graph
// where the direction of every edge is a coin toss, and every query over it is
// wrong half the time.
type EdgeDef struct {
	Kind EdgeKind
	// From and To are the permitted kinds. Empty means any.
	From []Kind
	To   []Kind
	// Reads is the edge as a sentence, for error messages and for the console.
	Reads string
	Gloss string
}

var (
	reasoningKinds = []Kind{KindRequirement, KindConstraint, KindAssumption,
		KindRisk, KindHazard, KindCriterion}
	buildKinds = []Kind{KindComponent, KindInterface, KindTest, KindArtifact}
	checkable  = []Kind{KindRequirement, KindConstraint, KindCriterion,
		KindComponent, KindInterface, KindArtifact}
)

var edges = []EdgeDef{
	{EdgeDerivesFrom, nil, nil, "%s derives from %s",
		"provenance: this was produced from that"},
	{EdgeSatisfies,
		append(append([]Kind{}, buildKinds...), KindDecision),
		[]Kind{KindRequirement, KindConstraint, KindCriterion},
		"%s satisfies %s", "this meets that"},
	{EdgeVerifies,
		[]Kind{KindTest, KindEvidence},
		checkable,
		"%s verifies %s", "this checks that; not the same as meeting it"},
	{EdgeDependsOn,
		append(append([]Kind{}, buildKinds...), KindGoal),
		append(append([]Kind{}, buildKinds...), KindGoal),
		"%s depends on %s", "this needs that first; cycles here are defects"},
	{EdgeConstrains,
		[]Kind{KindConstraint},
		nil,
		"%s constrains %s", "that is bounded by this"},
	{EdgeMitigates,
		append(append([]Kind{}, buildKinds...), KindRequirement, KindDecision),
		[]Kind{KindRisk, KindHazard},
		"%s mitigates %s", "this reduces that"},
	{EdgeOwns,
		[]Kind{KindOwner},
		nil,
		"%s owns %s", "this person is accountable for that"},
	{EdgeEvidences,
		[]Kind{KindEvidence, KindArtifact},
		nil,
		"%s evidences %s", "this supports that"},
}

// EdgeKinds returns the eight.
func EdgeKinds() []EdgeDef { return append([]EdgeDef(nil), edges...) }

// EdgeKindOf returns the definition for an edge kind.
func EdgeKindOf(k EdgeKind) (EdgeDef, error) {
	for _, d := range edges {
		if d.Kind == k {
			return d, nil
		}
	}
	return EdgeDef{}, errs.New("workspace.EdgeKindOf", errs.CodeValidationFailed).
		WithDetail("%q is not an edge kind; the kinds are %s", k, strings.Join(edgeNames(), ", "))
}

func edgeNames() []string {
	out := make([]string, 0, len(edges))
	for _, d := range edges {
		out = append(out, string(d.Kind))
	}
	return out
}

// Valid reports whether k is one of the eight.
func (k EdgeKind) Valid() bool { _, err := EdgeKindOf(k); return err == nil }

// Permits reports whether this edge kind may run from one kind to another, and
// says what the sentence would have been when it may not.
func (d EdgeDef) Permits(from, to Kind) error {
	const op = "workspace.EdgeDef.Permits"

	if len(d.From) > 0 && !containsKind(d.From, from) {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("nothing of kind %s can %s anything. %s. Permitted on the left: %s",
				from, d.Kind, d.Gloss, joinKinds(d.From))
	}
	if len(d.To) > 0 && !containsKind(d.To, to) {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a %s cannot %s a %s. %s. Permitted on the right: %s",
				from, d.Kind, to, d.Gloss, joinKinds(d.To))
	}
	return nil
}

func containsKind(set []Kind, k Kind) bool {
	for _, s := range set {
		if s == k {
			return true
		}
	}
	return false
}

func joinKinds(set []Kind) string {
	out := make([]string, 0, len(set))
	for _, k := range set {
		out = append(out, string(k))
	}
	return strings.Join(out, ", ")
}

// Edge is one typed relation between two nodes.
type Edge struct {
	ID        string
	ProjectID string
	Kind      EdgeKind
	FromID    string
	ToID      string
	Note      string
	CreatedBy string
	CreatedAt time.Time
}
