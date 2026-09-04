package pack

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
)

// Domain packs, industry coverage, and validated intended use (PRD SEC-07).
//
// # Why these fences changed shape (2026-09-04)
//
// TestNoRegulatedPackIsAvailableInThisBuild used to assert that mechanical,
// civil, electrical, aerospace, robotics and medical were ALL unavailable, and
// it was protecting something real: this build implements no qualified review,
// holds no licensed authority, and must not record that those rules applied
// while nothing applied them.
//
// It was protecting it at the wrong granularity. `Available bool` refused the
// whole of mechanical because drawing release (R3) could not be gated — which
// also refused concept CAD, a render and a revision (R1), the work this product
// exists to do. The boundary was correct; the unit was a domain when risk is
// per-action.
//
// So the assertion is RESTATED, not removed: no pack may permit consequential
// work (R2+), every pack must name the authority that would unblock it, and the
// two packs with no ceiling at all stay uncreatable. That is the same protection
// expressed where risk actually lives. Anything that makes a pack permit R2 here
// turns these red, which is the property the old fence had and the reason it is
// worth keeping.
//
// See docs/bugfix/2026-09-04-the-pack-was-written-and-never-read.md.

// everyPack is written out rather than taken from All(), because a fence that
// enumerates the thing it is checking cannot fail: deleting a row would delete
// the assertion about it in the same motion.
var everyPack = []string{
	"software", "mechanical", "manufacturing", "automotive", "aerospace",
	"civil", "electrical", "construction", "product-design", "architecture",
	"robotics", "medical", "general",
}

// Every pack this build claims is in the table, and each one says what it is.
//
// A pack missing from the table is not a gap in documentation: Lookup would
// report it as unknown, and a project legitimately working in that domain would
// be refused with "this build has never heard of it" rather than with the reason
// it is actually limited.
func TestEveryPackTheRequirementsNameIsInTheTable(t *testing.T) {
	for _, name := range everyPack {
		d, ok := Lookup(name)
		if !ok {
			t.Errorf("%s is a pack this build names and the table does not know it", name)
			continue
		}
		if strings.TrimSpace(d.Summary) == "" {
			t.Errorf("%s carries no safety boundary; the pack is a rule set, not a label", name)
		}
		// Requires is mandatory for EVERY pack now, not only unavailable ones:
		// it is what a deployment would need to work above the ceiling, and every
		// pack has a ceiling. A pack whose limit has no stated path is a dead end
		// for the person trying to find out whether they are blocked by policy or
		// by a bug.
		if strings.TrimSpace(d.Requires) == "" {
			t.Errorf("%s states no path above its ceiling.\n"+
				"A limit with no path is a dead end, and the person reading it is trying to "+
				"find out whether they are blocked by policy or by a bug", name)
		}
	}
}

// theIndustriesTheProductOffers is the selector's list, verbatim.
//
// Hardcoded for everyPack's reason, and it is the fence that holds the actual
// requirement: these are the industries a person can pick, so every one of them
// must reach a pack. If somebody deletes a row, this goes red rather than
// quietly shrinking the product.
var theIndustriesTheProductOffers = []string{
	"Mechanical engineering", "Manufacturing", "Automotive", "Aerospace",
	"Civil engineering", "Electrical engineering", "Construction",
	"Product design", "Architecture", "Other",
}

// Every industry the selector offers resolves to a pack that can be worked in.
//
// Both halves matter. Resolving proves the vocabularies agree; being workable
// proves the industry is served rather than listed — a dropdown entry that
// refuses every project is worse than no entry, because the person picked it
// believing it meant something.
func TestEveryIndustryTheSelectorOffersIsWorkable(t *testing.T) {
	for _, label := range theIndustriesTheProductOffers {
		d, ok := Lookup(label)
		if !ok {
			t.Errorf("the industry selector offers %q and no pack answers to it, so a person "+
				"picking it is refused for a name the product itself showed them", label)
			continue
		}
		if !d.Available() {
			t.Errorf("%q resolves to the %s pack, which no project may be created in. "+
				"The selector offers work this build refuses", label, d.Pack)
		}
		if !d.Permits(engine.RiskR1) {
			t.Errorf("%q (pack %s) does not permit R1 work. R1 is a reversible draft — concept "+
				"geometry, a render, a revision — which is the work this product does. An "+
				"industry that cannot reach it is listed, not covered", label, d.Pack)
		}
	}
	if got, want := len(Industries()), len(theIndustriesTheProductOffers); got != want {
		t.Errorf("the table offers %d industries and the selector offers %d; "+
			"one of the two has been changed without the other", got, want)
	}
}

// No pack permits consequential work, and every one says what would unblock it.
//
// This is the restatement of TestNoRegulatedPackIsAvailableInThisBuild. R2 is
// "consequential digital action" — merge, baseline change, release preparation
// upward from there — and nothing in this build implements the qualified review,
// licensed authority or validated intended use those need in an engineering
// domain. Software and general are excluded: R2 there is code review and an
// approval gate, which this build DOES implement.
func TestNoEngineeringPackPermitsConsequentialWork(t *testing.T) {
	for _, name := range []string{"mechanical", "manufacturing", "automotive", "aerospace",
		"civil", "electrical", "construction", "product-design", "architecture"} {
		d, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s is not in the table", name)
		}
		if d.Permits(engine.RiskR2) {
			t.Errorf("the %s pack permits R2 work.\n"+
				"Nothing here implements the qualified review, licensed authority or validated "+
				"intended use that domain requires, so work done under it would record that "+
				"those rules applied while nothing applied them", name)
		}
		if strings.TrimSpace(d.Requires) == "" {
			t.Errorf("%s is ceilinged and names no authority that would raise it", name)
		}
	}
}

// The two packs with no ceiling at all stay uncreatable.
//
// Kept separate from the ceiling fence above because it is a different
// judgement, and the one somebody will argue about. The PRD carve-out settled
// medical: "educational and device-concept scope only. Patient-specific use
// requires a separately validated deployment and is not enabled by this
// codebase." Robotics grants no actuation capability, so there is nothing to
// bound. Neither is an industry the selector offers.
func TestMedicalAndRoboticsAreNotWorkableAtAll(t *testing.T) {
	for _, name := range []string{"medical", "robotics"} {
		d, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s is not in the table", name)
		}
		if d.Available() {
			t.Errorf("a project may be created in the %s pack. This build implements none of "+
				"what that domain requires, and the selector does not offer it", name)
		}
		for _, tier := range engine.AllRiskTiers() {
			if d.Permits(tier) {
				t.Errorf("the %s pack permits %s work", name, tier)
			}
		}
		if d.Industry != "" {
			t.Errorf("the %s pack is offered to users as %q while no project may be created "+
				"in it", name, d.Industry)
		}
	}
	// Medical says the specific thing, because it is the one somebody will argue
	// about and the PRD already settled it.
	med, _ := Lookup("medical")
	for _, want := range []string{"validated deployment", "clinician", "patient-specific"} {
		if !strings.Contains(med.Requires, want) {
			t.Errorf("the medical refusal does not mention %q: %q", want, med.Requires)
		}
	}
}

// R5 is refused everywhere, whatever a row says.
//
// PRD §8.1: R5 is refused and not gated, so no table edit may authorise it. A
// ceiling is a maximum for tiers that are permissible at all, not a licence.
func TestNoPackPermitsProhibitedWork(t *testing.T) {
	for _, d := range All() {
		if d.Permits(engine.RiskR5) {
			t.Errorf("the %s pack permits R5 work, which is prohibited with or without "+
				"approval", d.Pack)
		}
	}
}

// Names lists what a project may be created in, and nothing else.
func TestNamesAreExactlyTheWorkablePacks(t *testing.T) {
	got := map[string]bool{}
	for _, n := range Names() {
		got[n] = true
	}
	for _, n := range everyPack {
		d, _ := Lookup(n)
		if d.Available() != got[n] {
			t.Errorf("%s: Available()=%v but listed by Names()=%v; the list somebody is shown "+
				"disagrees with the gate they will hit", n, d.Available(), got[n])
		}
	}
	if got["medical"] || got["robotics"] {
		t.Error("Names() offers a pack no project may be created in")
	}
}

// Lookup is case-, space- and separator-insensitive, and still closed.
//
// "Medical " with a trailing space must not slip past a boundary that "medical"
// is refused by; a closed set that a stray keystroke opens is not closed. And
// "Product design" — what the dropdown actually shows — must reach the same pack
// as "product-design", or the product's own vocabulary is one this build rejects.
func TestLookupIsClosedAndForgivingAboutForm(t *testing.T) {
	if d, ok := Lookup("  MEDICAL "); !ok || d.Pack != Medical {
		t.Error("a pack name differing only in case and spacing was not recognised, so the " +
			"boundary it is refused by could be walked around with a space")
	}
	for _, form := range []string{"Product design", "product-design", "PRODUCT_DESIGN", " product design "} {
		if d, ok := Lookup(form); !ok || d.Pack != ProductDesign {
			t.Errorf("%q did not reach the product-design pack", form)
		}
	}
	if d, ok := Lookup("Civil engineering"); !ok || d.Pack != Civil {
		t.Error("the industry label the selector shows did not reach its pack")
	}
	for _, unknown := range []string{"", "sofware", "biotech", "software-v2", "engineering"} {
		if _, ok := Lookup(unknown); ok {
			t.Errorf("%q was accepted as a pack; a pack nothing recognises selects no rules "+
				"while looking like it selected some", unknown)
		}
	}
}
