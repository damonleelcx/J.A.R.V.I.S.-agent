package geometry

import (
	"strings"
	"testing"
)

// The table, the judging and the note. addresses issue 6.

func measure(v float64) *float64 { return &v }

func partWith(id, process string) Part {
	return Part{ID: id, Name: id, Shape: "box", Process: process,
		Size: map[string]float64{"width": 10, "height": 10, "depth": 10}}
}

// ‼️ Every limit lives in ONE table, and every row that applies cites where its
// number came from and says it is not validated. A limit with no citation is a
// number somebody typed, and a finding built on one is worse than no finding.
func TestManufacturability_EveryLimitIsCitedAndLabelledUnvalidated(t *testing.T) {
	if len(Profiles) == 0 {
		t.Fatal("there are no process profiles at all")
	}
	for name, p := range Profiles {
		if p.Name != name {
			t.Errorf("the row keyed %q calls itself %q", name, p.Name)
		}
		if p.Label == "" {
			t.Errorf("%s has no label, so a finding cannot name the process it judged", name)
		}
		for rule, limit := range map[string]Limit{
			RuleMinWall: p.MinWall, RuleMinFeature: p.MinFeature,
			RuleInternalRadius: p.InternalRadius, RuleDraft: p.Draft, RuleOverhang: p.Overhang,
		} {
			if !limit.Applies() {
				// A rule this process has no opinion about carries no number
				// either: a limit of zero that "does not apply" is one edit away
				// from being compared against.
				if limit.Value != 0 || limit.Source != "" {
					t.Errorf("%s's %s does not apply but carries %g / %q",
						name, rule, limit.Value, limit.Source)
				}
				continue
			}
			if limit.Value <= 0 {
				t.Errorf("%s's %s applies with a limit of %g", name, rule, limit.Value)
			}
			if strings.TrimSpace(limit.Source) == "" {
				t.Errorf("%s's %s is %g with no source", name, rule, limit.Value)
			}
			if limit.Validated {
				t.Errorf("%s's %s claims to be validated; every number in this table is a "+
					"published rule of thumb, and the turn says UNVALIDATED because of it", name, rule)
			}
		}
	}
	// Draft belongs to moulding and overhang to printing, and neither belongs to
	// milling: a milled part is held in stock that supports itself.
	if Profiles[ProcessMilling3Axis].Overhang.Applies() || Profiles[ProcessMilling3Axis].Draft.Applies() {
		t.Error("3-axis milling was given a draft or overhang rule")
	}
	if !Profiles[ProcessInjectionMoulding].Draft.Applies() {
		t.Error("injection moulding has no draft rule, which is the rule moulding exists to have")
	}
	if !Profiles[ProcessPrintingFDM].Overhang.Applies() {
		t.Error("FDM printing has no overhang rule")
	}
}

// A finding carries the measured value AND the limit, and says which part and
// which rule. "Too thin" is an opinion; a number beside a number is a claim a
// reader can argue with.
func TestManufacturability_AFindingNamesThePartTheRuleTheValueAndTheLimit(t *testing.T) {
	doc := Document{Parts: []Part{partWith("bracket", string(ProcessPrintingFDM))}}
	// FDM's wall floor is 0.8 mm; 0.35 is under it.
	report := Manufacturability(doc, []PartMeasure{{ID: "bracket", Faces: 6, MinWall: measure(0.35)}}, false, 1)

	if len(report.Findings) != 1 {
		t.Fatalf("a 0.35 mm wall on an FDM part produced %d finding(s): %+v",
			len(report.Findings), report.Findings)
	}
	f := report.Findings[0]
	if f.Part != "bracket" || f.Rule != RuleMinWall {
		t.Errorf("the finding is about %q / %q", f.Part, f.Rule)
	}
	if f.Measured != 0.35 || f.Limit != Profiles[ProcessPrintingFDM].MinWall.Value {
		t.Errorf("measured %g against limit %g", f.Measured, f.Limit)
	}
	said := f.Describe()
	for _, want := range []string{"bracket", RuleMinWall, "0.35 mm", "0.8 mm", "FDM printing", "UNVALIDATED"} {
		if !strings.Contains(said, want) {
			t.Errorf("the finding does not say %q: %q", want, said)
		}
	}
}

// ‼️ A part that names no process is checked against NOTHING and named as
// unchecked. It is never counted as clean, and no process is guessed for it: the
// same 0.9 mm wall is fine milled, marginal moulded and impossible in metal powder.
func TestManufacturability_APartWithNoProcessIsNamedAsUncheckedAndNeverGuessedAt(t *testing.T) {
	doc := Document{Parts: []Part{partWith("bracket", ""), partWith("housing", string(ProcessMilling3Axis))}}
	// A 0.35 mm wall on both. Only the one with a process is judged.
	report := Manufacturability(doc, []PartMeasure{
		{ID: "bracket", Faces: 6, MinWall: measure(0.35)},
		{ID: "housing", Faces: 6, MinWall: measure(0.35)},
	}, false, 2)

	if len(report.Findings) != 1 || report.Findings[0].Part != "housing" {
		t.Fatalf("a part with no process was judged anyway: %+v", report.Findings)
	}
	if len(report.WithoutProcess) != 1 || report.WithoutProcess[0] != "bracket" {
		t.Errorf("the part with no process is not named as unchecked: %+v", report.WithoutProcess)
	}
	if report.Clean() {
		t.Error("a model with an unchecked part reported itself clean")
	}
	note := ManufacturabilityNote(report)
	if !strings.Contains(note, "bracket") || !strings.Contains(note, "name no process") {
		t.Errorf("the turn does not say the part was not checked: %q", note)
	}
}

// A rule a process has no opinion about produces no finding, however bad the
// number: a milled part's overhang is not a defect, because the stock holds it up.
func TestManufacturability_ARuleTheProcessDoesNotHaveNeverFires(t *testing.T) {
	doc := Document{Parts: []Part{partWith("block", string(ProcessMilling3Axis))}}
	report := Manufacturability(doc, []PartMeasure{
		{ID: "block", Faces: 6, MaxOverhang: measure(89), MinDraft: measure(0)},
	}, false, 1)

	if len(report.Findings) != 0 {
		t.Fatalf("a milled part was judged on draft or overhang: %+v", report.Findings)
	}
	if !report.Clean() {
		t.Errorf("a milled block with an 89 degree overhang is not a finding and should read clean: %+v", report)
	}
	if note := ManufacturabilityNote(report); note != "" {
		t.Errorf("a complete clean check produced a note: %q", note)
	}
}

// A measurement the kernel did not take is nil, and nil is not zero.
//
// ‼️ The whole reason the measurements are pointers. A part whose wall could not be
// measured would, as a plain float, arrive as 0.0 — the worst finding it is possible
// to have — and the model would be told to thicken a wall nobody looked at.
func TestManufacturability_AnUnmeasuredValueIsNotJudgedAsZero(t *testing.T) {
	doc := Document{Parts: []Part{partWith("rib", string(ProcessPrintingSLM))}}
	report := Manufacturability(doc, []PartMeasure{{ID: "rib", Faces: 6}}, false, 1)

	if len(report.Findings) != 0 {
		t.Fatalf("a part with no measurements at all produced findings: %+v", report.Findings)
	}
	if report.Checked != 1 {
		t.Errorf("the part was not counted as checked: %+v", report)
	}
	// And a part the kernel refused outright is named, not silently absent.
	refused := Manufacturability(doc, []PartMeasure{{ID: "rib", Unchecked: "BRep_API: command not done"}}, false, 1)
	if len(refused.Unmeasured) != 1 || !strings.Contains(refused.Unmeasured[0], "rib") {
		t.Errorf("a part the kernel could not measure is not named: %+v", refused.Unmeasured)
	}
	if note := ManufacturabilityNote(refused); !strings.Contains(note, "could not be measured") {
		t.Errorf("the turn does not say the part was not measured: %q", note)
	}
}

// A square inside corner is zero, and zero is a FINDING for a milled part —
// distinguishing "0 mm radius" from "no internal corner at all", which is nil.
func TestManufacturability_ASharpCornerIsAFindingAndNoCornerIsNot(t *testing.T) {
	doc := Document{Parts: []Part{partWith("pocket", string(ProcessMilling3Axis)),
		partWith("block", string(ProcessMilling3Axis))}}
	report := Manufacturability(doc, []PartMeasure{
		{ID: "pocket", Faces: 8, InternalRadius: measure(0)},
		{ID: "block", Faces: 6},
	}, false, 2)

	if len(report.Findings) != 1 || report.Findings[0].Part != "pocket" ||
		report.Findings[0].Rule != RuleInternalRadius {
		t.Fatalf("a square inside corner and a box with none produced %+v", report.Findings)
	}
	if m := report.Findings[0].Measured; m != 0 {
		t.Errorf("the sharp corner was reported as %g mm, not 0", m)
	}
}

// ‼️ A truncated check says so, and its empty finding list is never read as a
// makeable model. The same rule the interference pair budget follows (Phase 5, V2).
func TestManufacturability_ATruncatedCheckSaysSoAndIsNeverClean(t *testing.T) {
	doc := Document{Parts: []Part{partWith("one", string(ProcessMilling3Axis))}}
	report := Manufacturability(doc, []PartMeasure{{ID: "one", Faces: 6, MinWall: measure(50)}}, true, 400)

	if report.Clean() {
		t.Fatal("a check the budget stopped reported itself clean")
	}
	note := ManufacturabilityNote(report)
	if !strings.Contains(note, "measured 1 of 400") || !strings.Contains(note, "not known to be makeable") {
		t.Errorf("a check that stopped after 1 of 400 parts said: %q", note)
	}
}

// A model where nobody named a process says ONE sentence, because there is no
// clean result to be silent about — and it says how to get one.
func TestManufacturability_AModelThatNamesNoProcessAnywhereSaysSoOnce(t *testing.T) {
	doc := Document{Parts: []Part{partWith("a", ""), partWith("b", ""), partWith("c", "")}}
	report := Manufacturability(doc, []PartMeasure{
		{ID: "a", Faces: 6, MinWall: measure(0.1)}, {ID: "b", Faces: 6}, {ID: "c", Faces: 6},
	}, false, 3)

	note := ManufacturabilityNote(report)
	if !strings.Contains(note, "No part says how it is made") || !strings.Contains(note, "none of the 3") {
		t.Errorf("a model with no process anywhere said: %q", note)
	}
	// And it offers the names the validator actually accepts.
	for _, name := range ProcessNames() {
		if !strings.Contains(note, string(name)) {
			t.Errorf("the note does not offer %q: %q", name, note)
		}
	}
}

// The worst finding is named first, by how far past its limit it is as a SHARE of
// it: a list cut to three must not lead with the mildest.
func TestManufacturability_TheWorstFindingIsNamedFirst(t *testing.T) {
	doc := Document{Parts: []Part{
		partWith("graze", string(ProcessPrintingFDM)),
		partWith("ruined", string(ProcessPrintingFDM)),
	}}
	report := Manufacturability(doc, []PartMeasure{
		// 0.79 of a 0.8 limit, against 0.08 of the same limit.
		{ID: "graze", Faces: 6, MinWall: measure(0.79)},
		{ID: "ruined", Faces: 6, MinWall: measure(0.08)},
	}, false, 2)

	if len(report.Findings) != 2 {
		t.Fatalf("want two findings, got %+v", report.Findings)
	}
	if report.Findings[0].Part != "ruined" {
		t.Errorf("the mildest finding was named first: %+v", report.Findings)
	}
}

// Nothing is repaired, ever: the report is a report.
func TestManufacturability_AFindingChangesNothingAboutTheDocument(t *testing.T) {
	doc := Document{Parts: []Part{partWith("bracket", string(ProcessPrintingFDM))}}
	before := doc.Parts[0]
	report := Manufacturability(doc, []PartMeasure{{ID: "bracket", Faces: 6, MinWall: measure(0.1)}}, false, 1)

	if len(report.Findings) == 0 {
		t.Fatal("the fence needs a finding to have something to not repair")
	}
	if doc.Parts[0].Size["height"] != before.Size["height"] || doc.Parts[0].Process != before.Process {
		t.Errorf("the check changed the document: %+v", doc.Parts[0])
	}
	if note := ManufacturabilityNote(report); !strings.Contains(note, "Nothing was changed") {
		t.Errorf("the turn does not say nothing was changed: %q", note)
	}
}

// The contract's process names and the validator's are one list.
func TestManufacturability_TheGuideOffersExactlyWhatIsValid(t *testing.T) {
	guide := ProcessGuide()
	for _, name := range ProcessNames() {
		if !ValidProcess(string(name)) {
			t.Errorf("%q is in the table and not valid", name)
		}
		if !strings.Contains(guide, string(name)) {
			t.Errorf("the guide does not offer %q: %q", name, guide)
		}
	}
	if ValidProcess("sand-casting") {
		t.Error("a process with no row in the table was accepted")
	}
	if ValidProcess("") {
		t.Error("an empty process was accepted as a name rather than as silence")
	}
}
