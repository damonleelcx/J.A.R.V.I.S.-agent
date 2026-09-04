package agent

import (
	"strings"
	"testing"

	domainpack "github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/pack"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
)

// The project's industry reaches the conversation (PRD §"Domain packs").
//
// # What this holds
//
// A pack is meant to bundle "terminology, ... 3D conventions". Until 2026-09-04
// `forge_projects.pack` was read by nothing, so it bundled none of that: an
// architect and a firmware engineer got byte-identical instructions, and the
// only domain vocabulary in the system was whatever the model happened to bring.
//
// These assert the conventions are actually in the request — and, just as
// importantly, that a project with no stated domain is unaffected, because that
// is what makes reading the pack safe to add to an existing deployment.

func defOf(t *testing.T, name string) domainpack.Definition {
	t.Helper()
	d, ok := domainpack.Lookup(name)
	if !ok {
		t.Fatalf("%s is not a pack", name)
	}
	return d
}

// Every industry the product offers contributes conventions to the prompt.
func TestFramingFor_EveryIndustryContributesItsConventions(t *testing.T) {
	// The nine engineering industries. "Other" is excluded deliberately and
	// asserted separately below: it is the unknown-domain pack and has nothing
	// to contribute BY DEFINITION.
	for _, label := range []string{
		"Mechanical engineering", "Manufacturing", "Automotive", "Aerospace",
		"Civil engineering", "Electrical engineering", "Construction",
		"Product design", "Architecture",
	} {
		d := defOf(t, label)
		if strings.TrimSpace(d.Conventions) == "" {
			t.Errorf("%s carries no conventions, so FORGE answers in it with no units and no "+
				"vocabulary — which is the state this whole area exists to end", label)
			continue
		}
		framing := framingFor(d)
		if !strings.Contains(framing, d.Conventions) {
			t.Errorf("%s: the conventions are not in the framing the model receives", label)
		}
		if !strings.Contains(framing, d.Industry) {
			t.Errorf("%s: the framing does not name the industry, so the model is given rules "+
				"with no statement of what they are the rules FOR", label)
		}
		// The invariant half must survive: a domain block that replaced the
		// framing would take the honesty rules with it.
		if !strings.Contains(framing, "SPEECH is short") {
			t.Errorf("%s: the domain block displaced the conversation framing", label)
		}
	}
}

// A project with no stated domain gets exactly what it got before packs were read.
//
// The property that makes this safe to ship: `general` is the unknown-domain
// pack, it carries no conventions, and nothing is asserted about a domain nobody
// established. An existing deployment sees no change at all.
func TestFramingFor_AnUnstatedDomainChangesNothing(t *testing.T) {
	general := defOf(t, "general")
	if strings.TrimSpace(general.Conventions) != "" {
		t.Fatal("the `general` pack carries conventions.\n" +
			"It is the pack that MEANS unknown domain — inventing a vocabulary for it " +
			"would be asserting a domain nobody chose")
	}
	if got := framingFor(general); got != converseFraming {
		t.Error("an unstated domain changed the conversation framing")
	}
	if got := framingFor(domainpack.Definition{}); got != converseFraming {
		t.Error("an absent domain changed the conversation framing")
	}
}

// The framing reaches the request both paths build.
//
// buildMessages is shared, and that sharing is itself a fence elsewhere
// (TestBothPathsBuildOneRequest). This asserts the domain rides on it, so the
// streaming path the workbench actually uses cannot lose the domain while the
// buffered path keeps it.
func TestBuildMessages_CarriesTheDomainConventions(t *testing.T) {
	conv := NewConversation(&visionStub{reply: sawIt}, persona.DefaultCharacter())
	civil := defOf(t, "civil")

	built := conv.buildMessages(persona.DefaultCharacter(), civil, nil, "size this beam", "", nil)
	if len(built) == 0 {
		t.Fatal("no messages were built")
	}
	system := built[0].Content
	if !strings.Contains(system, "Civil engineering") {
		t.Error("the system message does not name the project's industry")
	}
	// Matched on a phrase that does not straddle a line break in the conventions
	// text — the block is hard-wrapped, so "load case" spans a newline.
	if !strings.Contains(system, "(dead, live, wind, seismic)") {
		t.Error("the system message does not carry the civil conventions, so FORGE would " +
			"answer a structural question with no statement of what a number rests on")
	}
	if !strings.Contains(system, civil.Conventions) {
		t.Error("the system message carries only part of the domain conventions")
	}
}

// A nil DomainStore answers `general` rather than panicking or asserting a domain.
//
// Nil is legal everywhere it is held — the evaluation harness has no database —
// and a store that could not be nil would make the conversation surface require
// one.
func TestDomainStore_NilAnswersGeneral(t *testing.T) {
	var s *DomainStore
	got := s.For(t.Context(), "prj_whatever")
	if got.Pack != domainpack.General {
		t.Errorf("a nil store answered %q; expected `general`", got.Pack)
	}
	if got.Conventions != "" {
		t.Error("a nil store asserted domain conventions")
	}
}

// The frame and the handling rules reach the model too, not only the prose.
//
// # Why these need their own fence
//
// TestFramingFor_EveryIndustryContributesItsConventions asserts the Conventions
// paragraph arrives. The frame and the data rules are separate fields added
// later, and "the conventions arrived" would stay green if either of them
// silently stopped being spliced in.
//
// The frame is the one that matters most: it DIFFERS between domains — a vehicle
// is X-forward, a building is Z-up against a site datum — and a coordinate read
// in the wrong frame is wrong without looking wrong. A model never told which
// frame it is working in will pick one.
func TestFramingFor_CarriesTheFrameAndTheHandlingRules(t *testing.T) {
	for _, label := range []string{
		"Mechanical engineering", "Manufacturing", "Automotive", "Aerospace",
		"Civil engineering", "Electrical engineering", "Construction",
		"Product design", "Architecture",
	} {
		d := defOf(t, label)
		framing := framingFor(d)

		if strings.TrimSpace(d.GeometryAxes) == "" {
			t.Errorf("%s states no coordinate frame", label)
		} else if !strings.Contains(framing, d.GeometryAxes) {
			t.Errorf("%s: the coordinate frame never reaches the model, so a position it "+
				"proposes means whatever the model assumed", label)
		}
		if d.GeometryUnit != "" && !strings.Contains(framing, d.GeometryUnit) {
			t.Errorf("%s: the default unit does not reach the model", label)
		}
		if strings.TrimSpace(d.DataRules) == "" {
			t.Errorf("%s states no handling rules", label)
		} else if !strings.Contains(framing, d.DataRules) {
			t.Errorf("%s: the handling rules never reach the model — and the model is the "+
				"thing that decides what goes into a reply, so a rule it never sees is a "+
				"rule addressed to nobody", label)
		}
	}
}

// `general` still contributes nothing, frame and handling included.
//
// The property that makes all of this safe on an existing deployment: a project
// that never stated a domain gets byte-identical framing to what it got before
// packs were read at all.
func TestFramingFor_AnUnstatedDomainAddsNoFrameOrRules(t *testing.T) {
	general := defOf(t, "general")
	if general.GeometryAxes != "" || general.GeometryUnit != "" {
		t.Error("the `general` pack asserts a coordinate frame for a domain nobody stated")
	}
	if got := framingFor(general); got != converseFraming {
		t.Error("an unstated domain changed the framing")
	}
}
