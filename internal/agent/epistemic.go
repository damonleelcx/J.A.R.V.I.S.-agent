package agent

import (
	"sort"
	"strings"
)

// How a thing is known (PRD RSN-05).
//
// # Why this is a closed vocabulary and not a free-text note
//
// The PRD names seven ways a statement can come to be true, and the difference
// between them is the difference between a number you can act on and one you
// cannot. "The bracket is 42.3mm" is a different promise depending on whether it
// was measured, looked up, computed, simulated, guessed from context, chosen for
// want of an instruction, or merely suggested. A free-text label would collapse
// under paraphrase — "approximately", "roughly", "I think" — and could not be
// checked by anything.
//
// So: seven constants, an ordering, and a rule about which of them may carry a
// figure that somebody will act on.
//
// # The one that started this
//
// The standards detector (standards.go) found FORGE stating a fabricated NEMA 17
// bolt pattern in its `assumptions` list. That was one edge of this distinction —
// RETRIEVED masquerading as ASSUMED. This file is the whole vocabulary, so the
// next such confusion has a name before it happens rather than after.

// Epistemic is how FORGE came to hold a statement.
type Epistemic string

const (
	// Observed — FORGE saw it directly in this deployment: a file it read, a
	// command's output, a value returned by a tool that actually ran.
	Observed Epistemic = "observed"
	// Retrieved — taken from a source outside FORGE. A published standard, a
	// datasheet, a document. **Retrieved without a reachable source is the
	// dangerous case**: it means recalled from model weights, which is where the
	// fabricated bolt pattern came from.
	Retrieved Epistemic = "retrieved"
	// Calculated — derived arithmetically from values already known, and the
	// derivation can be shown.
	Calculated Epistemic = "calculated"
	// Simulated — produced by a model of the world rather than the world. There
	// is no solver in this deployment, so nothing may currently claim it.
	Simulated Epistemic = "simulated"
	// Inferred — concluded from context by reasoning that is not arithmetic.
	Inferred Epistemic = "inferred"
	// Assumed — chosen by FORGE because nobody said, and stated so.
	Assumed Epistemic = "assumed"
	// Proposed — offered for a decision. Not yet true of anything.
	Proposed Epistemic = "proposed"
)

// epistemics is the closed set, with what each one obliges.
//
// A table rather than a switch: adding a category means adding a row, and the
// obligations are readable side by side instead of scattered across call sites.
var epistemics = []struct {
	Label Epistemic
	// Rank orders them from "checked by this system" to "not checked at all".
	// Used to decide which label wins when a statement has several origins: the
	// weakest one does, because a chain is as strong as its worst link.
	Rank int
	// NeedsSource marks a category that is meaningless without something to
	// point at. A retrieved figure with no source is a recalled figure.
	NeedsSource bool
	// Actionable marks categories a reader may act on without further checking.
	Actionable bool
	// Gloss is shown to a person, in their words rather than ours.
	Gloss string
}{
	{Observed, 0, false, true, "seen directly by FORGE in this deployment"},
	{Calculated, 1, false, true, "derived from values already known"},
	{Retrieved, 2, true, false, "taken from a source outside FORGE"},
	{Simulated, 3, true, false, "produced by a model, not by the world"},
	{Inferred, 4, false, false, "concluded from context, not measured"},
	{Assumed, 5, false, false, "chosen by FORGE because nobody said"},
	{Proposed, 6, false, false, "offered for a decision; not yet true of anything"},
}

// Valid reports whether e is one of the seven.
func (e Epistemic) Valid() bool {
	for _, d := range epistemics {
		if d.Label == e {
			return true
		}
	}
	return false
}

func (e Epistemic) def() (int, bool, bool, string) {
	for _, d := range epistemics {
		if d.Label == e {
			return d.Rank, d.NeedsSource, d.Actionable, d.Gloss
		}
	}
	// Unknown labels rank worst and are actionable by nobody. An unrecognised
	// label must never be safer than a recognised one.
	return len(epistemics), false, false, "unrecognised"
}

// Gloss explains the label in a reader's terms.
func (e Epistemic) Gloss() string { _, _, _, g := e.def(); return g }

// NeedsSource reports whether this category is meaningless without one.
func (e Epistemic) NeedsSource() bool { _, n, _, _ := e.def(); return n }

// Actionable reports whether a reader may act on it without checking first.
func (e Epistemic) Actionable() bool { _, _, a, _ := e.def(); return a }

// Actionableish reports whether this particular claim may be acted on. A
// category can be actionable in principle and this claim still not be — a
// retrieved figure with no reachable source is the case that matters.
func (c Claim) Actionableish() bool {
	if !c.How.Actionable() {
		return false
	}
	return !c.How.NeedsSource() || strings.TrimSpace(c.Source) != ""
}

// AllEpistemics returns the seven, weakest last.
func AllEpistemics() []Epistemic {
	out := make([]Epistemic, 0, len(epistemics))
	for _, d := range epistemics {
		out = append(out, d.Label)
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, _, _, _ := out[i].def()
		rj, _, _, _ := out[j].def()
		return ri < rj
	})
	return out
}

// Weakest returns the label a combined statement must carry.
//
// A value calculated FROM an assumed input is not calculated, it is assumed with
// extra steps — the arithmetic does not launder the guess. So a statement drawing
// on several origins takes the weakest of them.
func Weakest(labels ...Epistemic) Epistemic {
	if len(labels) == 0 {
		return Proposed
	}
	worst := labels[0]
	worstRank, _, _, _ := worst.def()
	for _, l := range labels[1:] {
		if r, _, _, _ := l.def(); r > worstRank {
			worst, worstRank = l, r
		}
	}
	return worst
}

// Claim is one statement together with how it is known.
type Claim struct {
	// Statement is the thing said, in FORGE's own words.
	Statement string `json:"statement"`
	// How is the epistemic category. Never empty: an unlabelled claim is exactly
	// the thing this package exists to prevent.
	How Epistemic `json:"how"`
	// Source is where it came from, for categories that require one. A named
	// standard, a file path, a tool name.
	Source string `json:"source,omitempty"`
	// Subject is what the claim is about, when it is about one thing — a part id,
	// a task id — so a reader can find it.
	Subject string `json:"subject,omitempty"`
}

// Validate enforces the rules the categories imply.
//
// It corrects rather than rejects where correcting is honest: a retrieved figure
// with no source IS a recalled one, and saying so is more useful than refusing
// the whole reply over it. It reports what it changed so the caller can log it.
func (c *Claim) Validate() (changed string) {
	if strings.TrimSpace(c.Statement) == "" {
		c.Statement = "(empty statement)"
		changed = "empty statement"
	}
	if !c.How.Valid() {
		// An unrecognised label is downgraded, never upgraded. If we cannot tell
		// how something is known, it is not known.
		c.How = Inferred
		return join(changed, "unrecognised label downgraded to inferred")
	}
	if c.How.NeedsSource() && strings.TrimSpace(c.Source) == "" {
		switch c.How {
		case Retrieved:
			// The fabricated-standard case, given its true name.
			c.Source = "FORGE's own memory — no reference source exists in this deployment"
			return join(changed, "retrieved claim had no source")
		case Simulated:
			// There is no solver here, so nothing can honestly be simulated.
			c.How = Inferred
			return join(changed, "simulated claim downgraded to inferred: no solver exists in this deployment")
		}
	}
	return changed
}

func join(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

// ClaimLedger derives the epistemic record for one reply.
//
// # Why it is derived rather than asked for
//
// The model is not asked to label its own claims. It would comply, and the
// labels would be as reliable as the fabricated bolt pattern that started this —
// a component cannot be its own guard. So the ledger is built from what the
// reply actually contains: assumptions are assumed by construction, standards
// references are retrieved by construction, and geometry is proposed by
// construction.
//
// # One derivation path
//
// Recalled (standards.go) and Claims describe overlapping facts, and two
// independent derivations of overlapping facts is how they come to disagree. So
// Claims is computed FROM Recalled rather than beside it: the standards detector
// stays the one thing that decides what counts as a standards reference, and this
// decides what that means epistemically.
func (r *Reply) ClaimLedger() []Claim {
	var out []Claim
	seen := map[string]bool{}

	add := func(c Claim) {
		key := string(c.How) + "|" + c.Statement
		if seen[key] || strings.TrimSpace(c.Statement) == "" {
			return
		}
		seen[key] = true
		c.Validate()
		out = append(out, c)
	}

	// Standards references are RETRIEVED, and Validate names the source as
	// FORGE's own memory when there is nothing to point at — which, in this
	// deployment, there never is.
	recalledText := map[string]bool{}
	for _, rc := range r.Recalled {
		recalledText[rc.Text] = true
		add(Claim{
			Statement: rc.Text,
			How:       Retrieved,
			Subject:   strings.Join(rc.Standards, ", "),
		})
	}

	if r.Prototype != nil {
		// Every assumption is ASSUMED by construction — that is what the field
		// means once standards figures have been pulled out of it.
		for _, a := range r.Prototype.Assumptions {
			if recalledText[a] {
				continue // already recorded, more precisely, as retrieved
			}
			add(Claim{Statement: a, How: Assumed})
		}
		// The geometry itself is PROPOSED: it is offered for a decision and is
		// not yet true of anything. VIS-06 says the same thing in the banner;
		// this says it in the ledger.
		add(Claim{
			Statement: r.Prototype.Name + " — geometry as drawn",
			How:       Proposed,
			Subject:   r.Prototype.Name,
		})
	}
	if r.ProposedGoal != nil {
		add(Claim{Statement: r.ProposedGoal.Title, How: Proposed, Subject: "goal"})
	}
	return out
}
