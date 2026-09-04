package pack

import (
	"strings"
	"testing"
)

// Domain packs and validated intended use (PRD SEC-07).

// Every pack the PRD names is in the table, and each one says what it is.
//
// A pack missing from the table is not a gap in documentation: Lookup would
// report it as unknown, and a project legitimately working in that domain would
// be refused with "this build has never heard of it" rather than with the reason
// it is actually unavailable.
func TestEveryPackTheRequirementsNameIsInTheTable(t *testing.T) {
	for _, name := range []string{"software", "mechanical", "electrical", "civil",
		"robotics", "aerospace", "medical", "general"} {
		d, ok := Lookup(name)
		if !ok {
			t.Errorf("%s is a pack PRD §Domain packs names and this build does not know it", name)
			continue
		}
		if strings.TrimSpace(d.Summary) == "" {
			t.Errorf("%s carries no safety boundary; the pack is a rule set, not a label", name)
		}
		if !d.Available && strings.TrimSpace(d.Requires) == "" {
			t.Errorf("%s is unavailable and says nothing about what would make it available.\n"+
				"A refusal with no path is a dead end, and the person reading it is trying to "+
				"find out whether they are blocked by policy or by a bug", name)
		}
	}
}

// The regulated and qualified-review packs are NOT available in this build.
//
// This is the requirement, not an implementation detail. PRD SEC-07 asks that
// regulated deployments stay inside validated intended use, and the carve-out
// says the medical pack is "educational and device-concept scope only. Patient-
// specific use requires a separately validated deployment and is not enabled by
// this codebase". A build that made medicine available would be making that
// sentence false.
func TestNoRegulatedPackIsAvailableInThisBuild(t *testing.T) {
	for _, name := range []string{"medical", "civil", "aerospace", "robotics",
		"electrical", "mechanical"} {
		d, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s is not in the table", name)
		}
		if d.Available {
			t.Errorf("the %s pack is available in this build.\n"+
				"Nothing here implements the qualified review, licensed authority or validated "+
				"intended use that pack requires, so a project created in it would record that "+
				"those rules applied while nothing applied them", name)
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

// What IS available is the work this build actually implements.
func TestTheAvailablePacksAreTheOnesThisBuildImplements(t *testing.T) {
	got := AvailableNames()
	want := map[string]bool{"software": true, "general": true}
	if len(got) != len(want) {
		t.Fatalf("available packs = %v; expected exactly software and general", got)
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("%q is available and this build implements no rules for it", name)
		}
	}
}

// Lookup is case- and whitespace-insensitive, and still closed.
//
// "Medical " with a trailing space must not slip past a boundary that "medical"
// is refused by; a closed set that a stray keystroke opens is not closed.
func TestLookupIsClosedAndForgivingAboutForm(t *testing.T) {
	if d, ok := Lookup("  MEDICAL "); !ok || d.Pack != Medical {
		t.Error("a pack name differing only in case and spacing was not recognised, so the " +
			"boundary it is refused by could be walked around with a space")
	}
	for _, unknown := range []string{"", "sofware", "biotech", "software-v2"} {
		if _, ok := Lookup(unknown); ok {
			t.Errorf("%q was accepted as a pack; a pack nothing recognises selects no rules "+
				"while looking like it selected some", unknown)
		}
	}
}
