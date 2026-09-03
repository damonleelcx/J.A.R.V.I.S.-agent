package engine

import "sort"

// Dynamic risk classification (PRD SAF-01).
//
// # What was static, and why that was wrong
//
// A tool declared one tier and that tier was the answer, everywhere, forever.
// But the same call is not equally consequential in every setting: writing a
// file you can delete is not writing one you cannot, and running a deploy
// against a developer's laptop is not running it against production. A tier
// fixed at registration describes the tool. SAF-01 asks for a tier that
// describes the ACTION — "tier rises with permissions, irreversibility,
// deployment context" — which is a property of the call and its surroundings.
//
// # The shape: floors and a raise, never a lowering
//
// Each rule can only push the tier UP. There is no rule that makes something
// safer than it declared itself, and there is deliberately no way to write one:
// Classify takes the maximum of the declared tier and every floor that applies,
// then applies raises. A classifier that could lower a tier would be a
// permission system with a bypass in it, and the bypass would be reached by
// exactly the calls that most needed the gate.
//
// # Why a table
//
// Because it will be argued with. These rules decide what gets refused, so they
// have to be readable by somebody who is not going to read the function that
// applies them — and adding a rule must not mean adding a branch to a growing
// conditional that nobody can reason about as a whole.

// Classification is what a risk decision is made from.
//
// Every field is a fact about THIS call, not about the tool in general. The
// caller assembles it; this package decides what it means.
type Classification struct {
	// Declared is the tier the tool's contract states. The floor below which
	// nothing here can take it.
	Declared RiskTier
	// Irreversible is an effect that cannot be taken back at all.
	Irreversible bool
	// ManualUndo is an effect a person could undo, but no machine will.
	ManualUndo bool
	// Mutating is whether the call changes anything.
	Mutating bool
	// Deploys, Transacts and Actuates are the permissions whose exercise is
	// consequential in itself: making a change visible outside the workspace,
	// spending money, moving something physical.
	Deploys   bool
	Transacts bool
	Actuates  bool
	// Production is the deployment context. False means a developer's machine or
	// an integration environment, where the same action is a smaller event.
	Production bool
}

// riskRule is one reason a tier may be higher than declared.
//
// A rule either sets a FLOOR (this kind of action is never below tier X) or
// RAISES by a step (this circumstance makes whatever it already was worse).
// Floors are absolute and order-independent; the raise is applied after every
// floor, so "production makes it worse" compounds with "it is irreversible"
// rather than competing with it.
type riskRule struct {
	// why is shown to the model and written to the ledger. Phrased as the reason
	// rather than the rule name, because it is read by somebody asking "why was
	// I refused" and "rule 3 matched" does not answer that.
	why   string
	floor RiskTier
	raise bool
	when  func(Classification) bool
}

// riskRules is the policy, one entry per axis PRD SAF-01 names.
//
// Deliberately short. Each rule earns its place by naming a way an action is
// more consequential than the tool that performs it, and a rule that fires on
// everything would raise every tier and gate nothing.
var riskRules = []riskRule{
	{
		// Irreversibility. The registration check already refuses to register an
		// irreversible tool below R2; this is the same rule at CALL time, where a
		// tool that raised its own tier for this invocation is also covered.
		why:   "the effect cannot be undone",
		floor: RiskR2,
		when:  func(c Classification) bool { return c.Irreversible },
	},
	{
		why:   "undoing this needs a person, not a command",
		floor: RiskR1,
		when:  func(c Classification) bool { return c.ManualUndo },
	},
	{
		// Permissions. Registration forces these tools to at least R2; exercising
		// one is operational work rather than merely consequential digital work,
		// which is what R3 is for.
		why:   "it makes a change visible outside the workspace",
		floor: RiskR3,
		when:  func(c Classification) bool { return c.Deploys },
	},
	{
		why:   "it spends money or incurs an obligation",
		floor: RiskR3,
		when:  func(c Classification) bool { return c.Transacts },
	},
	{
		// Physical actuation is never granted by this codebase. The rule exists so
		// that a connector claiming the capability is classified rather than
		// quietly accepted at whatever tier it declared.
		why:   "it actuates something physical",
		floor: RiskR4,
		when:  func(c Classification) bool { return c.Actuates },
	},
	{
		// Deployment context. The only raise, and it applies to change rather than
		// to reading: a query against production is not a bigger event than a
		// query against a laptop, and treating it as one would raise every tier
		// until the ceiling stopped meaning anything.
		why:   "it changes production rather than a development environment",
		raise: true,
		when:  func(c Classification) bool { return c.Production && c.Mutating },
	},
}

// Classify returns the tier this call actually sits at, and why it is not the
// declared one.
//
// The reasons are returned even when the tier did not move, so a caller that
// wants to explain a refusal has the material without re-deriving it. They are
// sorted so the same call classifies identically every time — an explanation
// that reorders between runs reads as though the policy changed.
func Classify(c Classification) (RiskTier, []string) {
	tier := c.Declared
	if !tier.Valid() {
		// An unclassifiable call is not a safe one. R5 is refused rather than
		// gated, which is the correct answer to "we do not know what this is".
		return RiskR5, []string{"the declared tier " + string(c.Declared) + " is not a known tier"}
	}

	var reasons []string
	raise := false
	for _, r := range riskRules {
		if !r.when(c) {
			continue
		}
		if r.raise {
			raise = true
			reasons = append(reasons, r.why)
			continue
		}
		if !tier.AtLeast(r.floor) {
			tier = r.floor
			reasons = append(reasons, r.why)
		}
	}
	if raise {
		tier = tier.next()
	}
	sort.Strings(reasons)
	return tier, reasons
}

// next returns the tier one step up, stopping at R5.
//
// R5 is prohibited rather than gated, so a raise that reaches it turns the call
// into a refusal. That is the intended end of the ladder: something that was
// already safety-critical and is now being done to production is not a thing to
// ask a person to approve in a hurry.
func (t RiskTier) next() RiskTier {
	order := []RiskTier{RiskR0, RiskR1, RiskR2, RiskR3, RiskR4, RiskR5}
	for i, r := range order {
		if r == t && i+1 < len(order) {
			return order[i+1]
		}
	}
	return RiskR5
}
