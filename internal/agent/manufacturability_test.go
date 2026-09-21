package agent

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// How a manufacturability finding reaches the turn. addresses issue 6.

func thickness(v float64) *float64 { return &v }

// madeBy is twoBoxes with a process named on the small part.
func madeBy(process string) *Prototype {
	doc := twoBoxes()
	doc.Parts[1].Process = process
	return doc
}

// ‼️ A described render measures nothing, and must not be told its parts can be
// made. The same rule the interference notes follow, for the same reason: a check
// that did not run must never read as one that passed.
func TestManufacturability_ADescribedRenderSaysNothingAboutMakingAnything(t *testing.T) {
	c := &Conversation{client: &repairStub{}}
	reply := &Reply{Prototype: madeBy(string(geometry.ProcessPrintingFDM))}
	sheet := builtSheet{Image: "data:,", FromKernel: false,
		Manufacturability:      []geometry.PartMeasure{{ID: "cylinder-body", MinWall: thickness(0.1)}},
		ManufacturabilityParts: 2}

	// ‼️ Both ways in, because they are two separate refusals.
	//
	// repairIfPartsOverlap returns before its deferred block when the picture is
	// not the kernel's, so going through it alone never reaches this function at
	// all — and a fence that only did that stayed green with the guard below
	// deleted. noteManufacturability owns the epistemics of the notes it writes and
	// must refuse on its own, whatever the one caller it has today happens to do
	// first.
	noteManufacturability(reply, &sheet)
	if reply.Repaired != "" {
		t.Errorf("called directly, a described render produced a manufacturability note: %q",
			reply.Repaired)
	}

	c.repairIfPartsOverlap(context.Background(), reply, &sheet)

	if reply.Repaired != "" {
		t.Errorf("a described render produced a manufacturability note: %q", reply.Repaired)
	}
}

// A finding reaches the turn with the part, the rule, the measured value, the
// limit and the fact that the limit is not validated — and nothing is changed.
func TestManufacturability_AFindingReachesTheTurnWithItsNumbersAndChangesNothing(t *testing.T) {
	stub := &repairStub{}
	c := &Conversation{client: stub}
	doc := madeBy(string(geometry.ProcessPrintingFDM))
	doc.Parts[0].Process = string(geometry.ProcessPrintingFDM)
	reply := &Reply{Prototype: doc}
	before := doc.Parts[1].Size["width"]
	sheet := builtSheet{Image: "data:,", FromKernel: true, Checked: 1, Pairs: 1,
		Manufacturability: []geometry.PartMeasure{
			{ID: "block", Faces: 6, MinWall: thickness(50)},
			{ID: "cylinder-body", Faces: 6, MinWall: thickness(0.25)},
		},
		ManufacturabilityParts: 2}

	c.repairIfPartsOverlap(context.Background(), reply, &sheet)

	for _, want := range []string{"Master Cylinder", "minimum wall thickness", "0.25 mm",
		"0.8 mm", "FDM printing", "UNVALIDATED", "Nothing was changed"} {
		if !strings.Contains(reply.Repaired, want) {
			t.Errorf("the turn does not say %q: %q", want, reply.Repaired)
		}
	}
	// ‼️ A finding never drives a repair. A thin wall may be a deliberate
	// membrane, and this repository has already had to delete one checker that
	// fired on correct models.
	if stub.calls != 0 {
		t.Errorf("a manufacturability finding drove %d repair call(s)", stub.calls)
	}
	if doc.Parts[1].Size["width"] != before {
		t.Errorf("the check changed the document: %g -> %g", before, doc.Parts[1].Size["width"])
	}
	// ‼️ Exactly one finding, about the one part that has one. A part that was
	// checked and is fine is not named: a note that lists every part it looked at
	// is a note whose findings nobody can find.
	if n := strings.Count(reply.Repaired, "minimum wall thickness"); n != 1 {
		t.Errorf("the turn names the wall rule %d times; only one part breaks it: %q", n, reply.Repaired)
	}
	if strings.Contains(reply.Repaired, "Engine Block") {
		t.Errorf("a part that was checked and is fine was named anyway: %q", reply.Repaired)
	}
}

// A clean, complete check adds nothing: a note on every clean turn is a note
// nobody reads. The same rule the interference coverage note settled on.
func TestManufacturability_ACompleteCleanCheckAddsNoNote(t *testing.T) {
	c := &Conversation{client: &repairStub{}}
	doc := madeBy(string(geometry.ProcessMilling3Axis))
	doc.Parts[0].Process = string(geometry.ProcessMilling3Axis)
	reply := &Reply{Prototype: doc}
	sheet := builtSheet{Image: "data:,", FromKernel: true, Checked: 1, Pairs: 1,
		Manufacturability: []geometry.PartMeasure{
			{ID: "block", Faces: 6, MinWall: thickness(50), MinFeature: thickness(50)},
			{ID: "cylinder-body", Faces: 6, MinWall: thickness(10), MinFeature: thickness(10)},
		},
		ManufacturabilityParts: 2}

	c.repairIfPartsOverlap(context.Background(), reply, &sheet)

	if reply.Repaired != "" {
		t.Errorf("a complete clean manufacturability check produced a note: %q", reply.Repaired)
	}
}

// ‼️ A check the budget stopped says so in the turn, and a part it never reached
// is not covered by silence.
func TestManufacturability_ATruncatedCheckSaysSoInTheTurn(t *testing.T) {
	c := &Conversation{client: &repairStub{}}
	doc := madeBy(string(geometry.ProcessMilling3Axis))
	doc.Parts[0].Process = string(geometry.ProcessMilling3Axis)
	reply := &Reply{Prototype: doc}
	sheet := builtSheet{Image: "data:,", FromKernel: true, Checked: 1, Pairs: 1,
		Manufacturability: []geometry.PartMeasure{
			{ID: "block", Faces: 6, MinWall: thickness(50), MinFeature: thickness(50)},
		},
		ManufacturabilityTruncated: true, ManufacturabilityParts: 900}

	c.repairIfPartsOverlap(context.Background(), reply, &sheet)

	for _, want := range []string{"measured 1 of 900", "not known to be makeable"} {
		if !strings.Contains(reply.Repaired, want) {
			t.Errorf("the turn does not say %q: %q", want, reply.Repaired)
		}
	}
}

// Section properties reach the turn, always beside the sentence saying they are
// not a stress check.
func TestManufacturability_ASectionReachesTheTurnWithItsCaveat(t *testing.T) {
	c := &Conversation{client: &repairStub{}}
	reply := &Reply{Prototype: twoBoxes()}
	sheet := builtSheet{Image: "data:,", FromKernel: true, Checked: 1, Pairs: 1,
		Sections: []geometry.SectionProperties{{
			ID: "mid-span", Part: "block", Axis: "x", At: 0, Area: 10000,
			Axes: [2]string{"y", "z"}, SecondMoments: [2]float64{8333333, 8333333},
			Fibres: [2]float64{50, 50}, Moduli: [2]float64{166666, 166666},
		}},
		ManufacturabilityParts: 0}

	c.repairIfPartsOverlap(context.Background(), reply, &sheet)

	for _, want := range []string{"mid-span", "second moment of area", "GEOMETRY, not a stress"} {
		if !strings.Contains(reply.Repaired, want) {
			t.Errorf("the turn does not say %q: %q", want, reply.Repaired)
		}
	}
}

// A process FORGE does not know is DROPPED before anything is checked, and said —
// never quietly checked against a neighbouring process.
func TestManufacturability_AProcessFORGEDoesNotKnowIsDroppedAndSaid(t *testing.T) {
	doc := twoBoxes()
	doc.Parts[1].Process = "sand-casting"
	doc = settleDocument(doc)

	if doc.Parts[1].Process != "" {
		t.Errorf("an unknown process survived settling: %q", doc.Parts[1].Process)
	}
	said := strings.Join(doc.NotVerified, " ")
	if !strings.Contains(said, "sand-casting") || !strings.Contains(said, "Master Cylinder") {
		t.Errorf("the dropped process is not reported: %v", doc.NotVerified)
	}
	// And a process it DOES know survives untouched.
	keeps := twoBoxes()
	keeps.Parts[1].Process = string(geometry.ProcessInjectionMoulding)
	keeps = settleDocument(keeps)
	if keeps.Parts[1].Process != string(geometry.ProcessInjectionMoulding) {
		t.Errorf("a valid process was dropped: %q", keeps.Parts[1].Process)
	}
}

// ‼️ The contract offers exactly the processes the validator accepts, and exactly
// the axes a section may be cut on.
//
// A contract naming a process the validator drops is a model writing the field
// faithfully and losing it — the defect FinishGuide and the density ceiling were
// each held to the code to avoid.
func TestTheContractTeachesProcessAsTheValidatorReadsIt(t *testing.T) {
	for _, contract := range []struct {
		name string
		text string
	}{{"conversation", converseFraming}, {"build step", buildContract}} {
		t.Run(contract.name, func(t *testing.T) {
			if !strings.Contains(contract.text, `"process"`) {
				t.Fatalf("the %s contract never mentions \"process\"", contract.name)
			}
			for _, name := range geometry.ProcessNames() {
				if !strings.Contains(contract.text, string(name)) {
					t.Errorf("the %s contract does not offer %q", contract.name, name)
				}
			}
			// Every process name the contract offers in the guide's own shape must
			// be one the validator accepts.
			offered := regexp.MustCompile(`\b([a-z0-9]+(?:-[a-z0-9]+)+)\b \(`).FindAllStringSubmatch(
				geometry.ProcessGuide(), -1)
			if len(offered) == 0 {
				t.Fatal("the process guide offers no names at all")
			}
			for _, m := range offered {
				if !geometry.ValidProcess(m[1]) {
					t.Errorf("the guide offers %q, which the validator refuses", m[1])
				}
			}
			// And sections are taught with the axes ValidateSections keeps.
			if !strings.Contains(contract.text, `"sections"`) {
				t.Errorf("the %s contract never mentions \"sections\"", contract.name)
			}
			if !strings.Contains(contract.text, "x, y or z") {
				t.Errorf("the %s contract does not teach the three section axes", contract.name)
			}
			// ‼️ And it must never present a section as a strength check.
			if !strings.Contains(contract.text, "not strength") {
				t.Errorf("the %s contract does not say a section is not strength", contract.name)
			}
		})
	}
}
