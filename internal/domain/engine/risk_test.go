package engine

import (
	"strings"
	"testing"
)

// Dynamic risk classification (PRD SAF-01).
//
// The table is the policy, so these cases are the policy's acceptance criteria:
// each one names a way an action is more consequential than the tool performing
// it, which is the distinction the requirement is asking for.

func TestClassifyRaisesWithTheThingsSAF01Names(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   Classification
		want RiskTier
		why  string
	}{
		{
			name: "a plain reversible draft stays where it was declared",
			in:   Classification{Declared: RiskR1, Mutating: true},
			want: RiskR1,
		},
		{
			// Irreversibility.
			name: "an irreversible effect is never below R2",
			in:   Classification{Declared: RiskR1, Mutating: true, Irreversible: true},
			want: RiskR2,
			why:  "undone",
		},
		{
			name: "an effect only a person can undo is never below R1",
			in:   Classification{Declared: RiskR0, Mutating: true, ManualUndo: true},
			want: RiskR1,
			why:  "person",
		},
		{
			// Permissions.
			name: "deploying is operational work, not merely consequential",
			in:   Classification{Declared: RiskR2, Mutating: true, Deploys: true},
			want: RiskR3,
			why:  "outside the workspace",
		},
		{
			name: "spending money is too",
			in:   Classification{Declared: RiskR2, Mutating: true, Transacts: true},
			want: RiskR3,
			why:  "money",
		},
		{
			name: "actuating something physical is R4",
			in:   Classification{Declared: RiskR2, Mutating: true, Actuates: true},
			want: RiskR4,
			why:  "physical",
		},
		{
			// Deployment context.
			name: "the same change is one tier higher against production",
			in:   Classification{Declared: RiskR1, Mutating: true, Production: true},
			want: RiskR2,
			why:  "production",
		},
		{
			// The reason reading is deliberately NOT raised in production: a read
			// against production is not a bigger event, and raising it would push
			// every tier up until the ceiling stopped discriminating.
			name: "reading production is not raised",
			in:   Classification{Declared: RiskR1, Mutating: false, Production: true},
			want: RiskR1,
		},
		{
			// The axes compound rather than compete. This is the case a single
			// max-of-floors design would get wrong: it would answer R2.
			name: "an irreversible change to production is worse than either alone",
			in:   Classification{Declared: RiskR1, Mutating: true, Irreversible: true, Production: true},
			want: RiskR3,
		},
		{
			name: "a deploy to production is R4",
			in:   Classification{Declared: RiskR2, Mutating: true, Deploys: true, Production: true},
			want: RiskR4,
		},
		{
			// Nothing lowers. A tool that declares itself high stays high even
			// when every rule would have placed it lower.
			name: "a high declaration is never talked down",
			in:   Classification{Declared: RiskR4, Mutating: false},
			want: RiskR4,
		},
		{
			name: "an unknown tier is prohibited rather than assumed safe",
			in:   Classification{Declared: RiskTier("banana")},
			want: RiskR5,
			why:  "not a known tier",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, reasons := Classify(tc.in)
			if got != tc.want {
				t.Errorf("classified %s, want %s (reasons: %v)", got, tc.want, reasons)
			}
			if tc.why == "" {
				return
			}
			joined := strings.Join(reasons, " | ")
			if !strings.Contains(joined, tc.why) {
				t.Errorf("the tier moved but the explanation does not mention %q: %s\n"+
					"A refusal a person cannot act on is a refusal they will work around.",
					tc.why, joined)
			}
		})
	}
}

// A rule may only ever push upward.
//
// Asserted over the whole rule table rather than case by case, so a rule added
// later is covered by this without anybody remembering to extend a list. The
// property is what makes the classifier safe to put in front of a permission
// check: there is no input for which it grants more than the declaration.
func TestNoRuleEverLowersATier(t *testing.T) {
	tiers := []RiskTier{RiskR0, RiskR1, RiskR2, RiskR3, RiskR4}
	bools := []bool{false, true}
	for _, declared := range tiers {
		for _, irr := range bools {
			for _, manual := range bools {
				for _, mut := range bools {
					for _, dep := range bools {
						for _, tx := range bools {
							for _, act := range bools {
								for _, prod := range bools {
									in := Classification{
										Declared: declared, Irreversible: irr, ManualUndo: manual,
										Mutating: mut, Deploys: dep, Transacts: tx,
										Actuates: act, Production: prod,
									}
									got, _ := Classify(in)
									if !got.AtLeast(declared) {
										t.Fatalf("Classify(%+v) returned %s, below the declared %s",
											in, got, declared)
									}
								}
							}
						}
					}
				}
			}
		}
	}
}

// Every rule that fires says why.
//
// The reason strings are shown to the model and written into refusals. A rule
// that raised a tier silently would produce "this tool is R3" against a contract
// that says R2, with nothing to explain the gap.
func TestEveryRuleExplainsItself(t *testing.T) {
	for i, r := range riskRules {
		if strings.TrimSpace(r.why) == "" {
			t.Errorf("rule %d fires with no explanation", i)
		}
		if r.floor == "" && !r.raise {
			t.Errorf("rule %d (%q) neither sets a floor nor raises; it cannot do anything", i, r.why)
		}
		if r.floor != "" && r.raise {
			t.Errorf("rule %d (%q) both sets a floor and raises; one rule, one effect", i, r.why)
		}
	}
}
