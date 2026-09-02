// Package memory owns what FORGE remembers between turns, sessions and goals,
// and the log of what it decided (PRD MEM-01, MEM-02, MEM-03).
//
// # The shape of the problem
//
// An agent that remembers everything forever is as useless as one that
// remembers nothing: last week's passing detail comes back as though it were
// still true, and the user cannot tell which of the two it is. So memory is
// LAYERED — five layers with different lifetimes and different audiences — and
// every item says how it came to be known.
//
// # The three properties this package is built to hold
//
//  1. Retention is enforced at READ time, not only by a sweep. A layer whose
//     expiry depends on a cron job is a layer with no expiry on the day the
//     cron job is down, and nothing about the returned rows would say so.
//
//  2. Retrieval explains itself. Every recalled item carries the reason THIS
//     query returned it, derived from the predicate that matched rather than
//     narrated by the caller (PRD MEM-02).
//
//  3. Forgetting holds. FORGE writes memory on its own initiative, so a plain
//     delete would be undone the next time it observed the same thing. A
//     forgotten key stays forgotten until somebody deliberately purges it.
package memory

import (
	"fmt"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Scope is which layer an item belongs to. The values are the ones stored in
// forge_memory.scope and constrained by forge_memory_scope_check.
type Scope string

const (
	// ScopeTurn is the context of one exchange. Minutes.
	ScopeTurn Scope = "turn"
	// ScopeSession is what a working session accumulated. Days.
	ScopeSession Scope = "session"
	// ScopeProject is project knowledge. Durable.
	ScopeProject Scope = "project"
	// ScopeUser is personal preference. Durable, and never shared.
	ScopeUser Scope = "user"
	// ScopeOrganisation is org-wide knowledge. Durable.
	ScopeOrganisation Scope = "organisation"
)

// Owner is what a layer hangs off, and therefore which column must be set.
type Owner string

const (
	OwnerGoal    Owner = "goal"
	OwnerProject Owner = "project"
	OwnerUser    Owner = "user"
	// OwnerNone is org-wide: there is nothing narrower to point at.
	OwnerNone Owner = "none"
)

// Visibility is who may read a layer.
//
// # What is and is not enforced today
//
// Enforced: an item in ScopeUser is returned only to the user it belongs to,
// and an item in ScopeProject only within that project. Those are the two that
// carry the real risk — personal preference leaking into shared context is the
// failure people actually mind.
//
// NOT enforced: VisibilityOrganisation is currently everybody with an account,
// because there is no organisation membership model yet (it arrives with
// COL-01/SEC-02 in wave 6). It is declared here so that when membership lands
// there is one place to enforce it, and so nobody reads "organisation" today
// and assumes a boundary that does not exist.
type Visibility string

const (
	VisibilityGoal         Visibility = "goal participants"
	VisibilityProject      Visibility = "project members"
	VisibilityOwnerOnly    Visibility = "the owning user only"
	VisibilityOrganisation Visibility = "everyone in the organisation"
)

// Layer is one row of MEM-01: a scope together with what it obliges.
//
// A table rather than a switch, for the same reason the epistemic vocabulary is
// one: adding a layer means adding a row, and the retention and sharing rules
// are readable side by side instead of scattered across the call sites that
// happen to need them.
type Layer struct {
	Scope Scope
	// PRDName is MEM-01's own word for this layer. Two of the five stored values
	// predate the PRD's wording — `user` is its "personal preferences",
	// `organisation` its "org knowledge" — and renaming a shipped column value
	// for a synonym would be a migration that buys nothing. This carries the
	// PRD's name so a reader can match the two without a decoder ring.
	PRDName string
	Owner   Owner
	// DefaultTTL is how long an item lives when the writer names no expiry.
	// Zero means it does not expire on its own.
	//
	// These are defaults, not ceilings: a caller may set a shorter expiry, and a
	// user may pin an item so it outlives its layer. Pinning is deliberately the
	// ONLY override — a second "survives compaction" flag beside it would be a
	// knob that can disagree with the first one.
	DefaultTTL time.Duration
	Visibility Visibility
	Gloss      string
}

// layers is the closed set. Order is shortest-lived first, which is also the
// order they are listed in PRD MEM-01.
var layers = []Layer{
	{ScopeTurn, "turn context", OwnerGoal, 15 * time.Minute, VisibilityGoal,
		"what this exchange needs and the next one will not"},
	{ScopeSession, "session notes", OwnerGoal, 7 * 24 * time.Hour, VisibilityGoal,
		"what this stretch of work established"},
	{ScopeProject, "project knowledge", OwnerProject, 0, VisibilityProject,
		"what is true of this project until somebody changes it"},
	{ScopeUser, "personal preferences", OwnerUser, 0, VisibilityOwnerOnly,
		"how this person likes to work; never shared"},
	{ScopeOrganisation, "org knowledge", OwnerNone, 0, VisibilityOrganisation,
		"what holds across every project here"},
}

// Layers returns the five, shortest-lived first.
func Layers() []Layer { return append([]Layer(nil), layers...) }

// LayerOf returns the definition for a scope.
//
// An unrecognised scope is an error rather than a default. Defaulting would
// give an unknown layer the retention and audience of a known one, and the
// safest-looking default (durable, widely shared) is the most dangerous.
func LayerOf(s Scope) (Layer, error) {
	for _, l := range layers {
		if l.Scope == s {
			return l, nil
		}
	}
	return Layer{}, errs.New("memory.LayerOf", errs.CodeValidationFailed).
		WithDetail("%q is not a memory layer; the layers are %s", s, strings.Join(scopeNames(), ", "))
}

// Valid reports whether s is one of the five.
func (s Scope) Valid() bool { _, err := LayerOf(s); return err == nil }

func scopeNames() []string {
	out := make([]string, 0, len(layers))
	for _, l := range layers {
		out = append(out, string(l.Scope))
	}
	return out
}

// Item is one thing FORGE remembers.
type Item struct {
	ID    string
	Scope Scope
	// GoalID, ProjectID and UserID: exactly the one the layer's Owner names is
	// set. The database enforces this too (forge_memory_scope_has_owner), so an
	// item cannot exist that the person it belongs to cannot reach.
	GoalID    *string
	ProjectID *string
	UserID    *string

	Key string
	// Value is the content, as stored. Cleared to JSON null when forgotten.
	Value []byte
	// How is the epistemic status (PRD RSN-05). Never empty on a written item.
	How claim.Epistemic
	// Source is provenance: where the content came from. Distinct from How,
	// which is what kind of knowing it is, and from a retrieval Reason, which is
	// why one particular query surfaced it.
	Source string
	Pinned bool
	// ExpiresAt is when this item stops being returned. Nil means never.
	ExpiresAt *time.Time

	// ForgottenAt, ForgottenBy and ForgottenReason record a user's deletion. A
	// forgotten item keeps its row so the key stays occupied and the agent
	// cannot re-learn it; see Forget in the service.
	ForgottenAt     *time.Time
	ForgottenBy     *string
	ForgottenReason string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Forgotten reports whether a user has deleted this item.
func (i *Item) Forgotten() bool { return i.ForgottenAt != nil }

// Live reports whether the item should be returned to a reader at instant now,
// and says why not when it should not.
//
// This is the read-time half of retention. The sweep is an optimisation; this
// is the guarantee. If the two ever disagree, this one is what a reader sees.
func (i *Item) Live(now time.Time) error {
	const op = "memory.Item.Live"

	if i.Forgotten() {
		return errs.New(op, errs.CodeMemoryForgotten).
			WithDetail("%q was forgotten at %s", i.Key, i.ForgottenAt.UTC().Format(time.RFC3339))
	}
	// Pinning is a user's explicit instruction to keep something past its
	// layer's lifetime, so it is checked before expiry rather than after.
	if i.Pinned {
		return nil
	}
	if i.ExpiresAt != nil && !now.Before(*i.ExpiresAt) {
		return errs.New(op, errs.CodeNotFound).
			WithDetail("%q expired at %s", i.Key, i.ExpiresAt.UTC().Format(time.RFC3339))
	}
	return nil
}

// Validate checks an item against its layer before it is written.
func (i *Item) Validate() error {
	const op = "memory.Item.Validate"

	layer, err := LayerOf(i.Scope)
	if err != nil {
		return err
	}
	if strings.TrimSpace(i.Key) == "" {
		return errs.New(op, errs.CodeValidationFailed).WithDetail("a memory item needs a key")
	}
	if !i.How.Valid() {
		// The one rule this package will not bend. An item read back next month
		// with no epistemic label is indistinguishable from a measurement, and
		// that is the confusion the whole claim vocabulary exists to prevent.
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("memory item %q has epistemic label %q; it must be one of %s",
				i.Key, i.How, strings.Join(epistemicNames(), ", "))
	}
	if len(i.Value) == 0 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("memory item %q has no value; write JSON null explicitly if that is the intent", i.Key)
	}

	set := func(p *string) bool { return p != nil && strings.TrimSpace(*p) != "" }
	var have bool
	var want string
	switch layer.Owner {
	case OwnerGoal:
		have, want = set(i.GoalID), "goal_id"
	case OwnerProject:
		have, want = set(i.ProjectID), "project_id"
	case OwnerUser:
		have, want = set(i.UserID), "user_id"
	case OwnerNone:
		have, want = true, ""
	}
	if !have {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("%s memory is owned by a %s, so %s must be set — an item nobody owns is an item nobody can delete",
				layer.Scope, layer.Owner, want)
	}
	return nil
}

func epistemicNames() []string {
	all := claim.AllEpistemics()
	out := make([]string, 0, len(all))
	for _, e := range all {
		out = append(out, string(e))
	}
	return out
}

// Reason is why one particular recall returned one particular item (PRD MEM-02).
//
// # Why this is a closed set and not a sentence
//
// "Show why an item was retrieved" is only worth anything if the answer is the
// truth about the query rather than a plausible story about it. A free-text
// reason written by the caller is a story: it is composed by the same code that
// wanted the item, and nothing checks it. These are produced by the retriever
// from the predicate that actually matched, so they cannot describe a match
// that did not happen.
type Reason string

const (
	// ReasonExactKey — the caller asked for this key by name.
	ReasonExactKey Reason = "exact-key"
	// ReasonPrefix — the caller asked for a key prefix and this key has it.
	ReasonPrefix Reason = "key-prefix"
	// ReasonPinned — pinned in a layer being read, so it is returned whether or
	// not it matched anything else. Pinning is a standing instruction to surface
	// something, which is a different reason from matching.
	ReasonPinned Reason = "pinned"
	// ReasonLayer — the caller asked for a whole layer and this is live in it.
	ReasonLayer Reason = "layer"
)

// Recalled is an item together with why this query returned it.
type Recalled struct {
	Item Item
	Why  Reason
	// Detail is the reason in a sentence, naming the thing that matched.
	Detail string
}

// String renders the explanation MEM-02 owes the reader.
func (r Recalled) String() string {
	return fmt.Sprintf("%s [%s] — %s", r.Item.Key, r.Item.How, r.Detail)
}

// explain builds the Detail for a reason. Kept beside the constants so a new
// reason cannot be added without a sentence to go with it.
func explain(why Reason, item *Item, query string) string {
	switch why {
	case ReasonExactKey:
		return fmt.Sprintf("you asked for the key %q", query)
	case ReasonPrefix:
		return fmt.Sprintf("its key starts with %q", query)
	case ReasonPinned:
		return fmt.Sprintf("it is pinned in %s memory", item.Scope)
	case ReasonLayer:
		return fmt.Sprintf("it is live in %s memory", item.Scope)
	default:
		// Unreachable while Reason stays closed; if it is ever reached, saying so
		// is better than an empty explanation that looks like a real one.
		return fmt.Sprintf("returned for an unrecognised reason (%q) — this is a defect", why)
	}
}
