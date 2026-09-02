package agent

import (
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/claim"
)

// The epistemic vocabulary itself now lives in internal/domain/claim, because
// memory and the decision log store claims and are underneath this package in
// the import graph. See that package for why.
//
// These aliases are not a compatibility shim to be cleaned up later. This is the
// package that BUILDS claims — the reply is assembled here — so `agent.Claim` is
// the name at the point of use, and the ledger below is the only thing that
// decides what a reply claims. Moving the definitions did not move that
// decision, and the names should not have to move with them.
type (
	// Epistemic is how FORGE came to hold a statement.
	Epistemic = claim.Epistemic
	// Claim is one statement together with how it is known.
	Claim = claim.Claim
)

const (
	Observed   = claim.Observed
	Retrieved  = claim.Retrieved
	Calculated = claim.Calculated
	Simulated  = claim.Simulated
	Inferred   = claim.Inferred
	Assumed    = claim.Assumed
	Proposed   = claim.Proposed
)

// AllEpistemics returns the seven, weakest last.
func AllEpistemics() []Epistemic { return claim.AllEpistemics() }

// Weakest returns the label a combined statement must carry.
func Weakest(labels ...Epistemic) Epistemic { return claim.Weakest(labels...) }

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
