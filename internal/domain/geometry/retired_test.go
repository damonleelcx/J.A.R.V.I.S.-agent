package geometry

import (
	"strings"
	"testing"
)

// The fences over a retired shape word.
//
// # What has to stay true
//
// `tube` was retired in wave 27 because it never modelled a bore and had no
// field in which to state one. Retiring it from the CONTRACT is not the risky
// half — every variant already stored with a tube in it must still open, still
// render and still export, because the ledger is append-only and a part that
// stops drawing is a revision history that lies about what was proposed.
//
// So three things are held here: a stored tube still builds, it builds as what
// it always was, and the reader is TOLD — through every path that reads a
// document, because a note in one of them and silence in the others is a screen
// where two answers about one part disagree.

func tubeDocument() Document {
	return Document{
		Name: "legacy", Units: "mm",
		Parts: []Part{{ID: "sleeve", Name: "Sleeve", Shape: "tube",
			Size:     map[string]float64{"radius": 10, "height": 40},
			Position: []float64{0, 0, 0}, Rotation: []float64{0, 0, 0}}},
	}
}

func TestARetiredShapeStillDraws(t *testing.T) {
	got := Tessellate(tubeDocument(), Millimetre)
	tris := got.Triangles()
	if len(tris) == 0 {
		t.Fatal("a stored document with a retired shape word drew nothing. " +
			"Retiring a word removes it from the contract; it cannot remove it from " +
			"variants already in the ledger, and a part that stops drawing is a " +
			"revision history that lies about what was proposed.")
	}
	// It is a cylinder of ⌀20 × 40 and not a bounding box, which is what the
	// unknown-shape fallback would have drawn.
	var maxX, maxY float64
	for _, tr := range tris {
		for _, v := range [][3]float64{tr.A, tr.B, tr.C} {
			if v[0] > maxX {
				maxX = v[0]
			}
			if v[1] > maxY {
				maxY = v[1]
			}
		}
	}
	if maxX > 10.0001 || maxX < 9.9 {
		t.Errorf("the solid reaches %.4f in x; a ⌀20 cylinder reaches 10 and a bounding "+
			"box would reach further", maxX)
	}
	if maxY < 19.999 || maxY > 20.001 {
		t.Errorf("the solid reaches %.4f in y; a 40 tall part centred on the origin reaches 20", maxY)
	}
}

func TestARetiredShapeSaysWhatItWasReadAs(t *testing.T) {
	// Every reader of a document, because a note on one screen and silence on
	// the next is worse than no note: the reader who saw the silent one now
	// believes the bore is there.
	t.Run("the mesh", func(t *testing.T) {
		notes := Tessellate(tubeDocument(), Millimetre).Inferences
		assertMentionsRetirement(t, notes)
	})
	t.Run("the exporter", func(t *testing.T) {
		_, notes := Solids(tubeDocument(), Millimetre)
		assertMentionsRetirement(t, notes)
	})
}

func assertMentionsRetirement(t *testing.T, notes []string) {
	t.Helper()
	for _, n := range notes {
		if strings.Contains(n, "solid cylinder") && strings.Contains(n, "holes") {
			return
		}
	}
	t.Errorf("nothing said that this part was read as a solid cylinder, or how to say a "+
		"bore properly. A retired word that resolves silently is the same claim the word "+
		"itself made — that the part is hollow — with nobody left to contradict it.\nNotes: %v",
		notes)
}

// The exported solid carries the RESOLVED word, because that is what the kernel
// is asked to build and the kernel has no case for the retired one. If this
// stopped being true the sidecar would refuse the part by name, which is the
// failure that gets fixed rather than the one that ships.
func TestTheKernelIsSentTheResolvedWord(t *testing.T) {
	solids, _ := Solids(tubeDocument(), Millimetre)
	if len(solids) != 1 {
		t.Fatalf("want one solid, got %d", len(solids))
	}
	if solids[0].Shape != "cylinder" {
		t.Errorf("the kernel would be sent shape %q; sidecar.py has no case for it and would "+
			"refuse the part", solids[0].Shape)
	}
}

// The summary line names the same shape the file holds. Two different answers
// about one part on one screen is the defect; this is where it would appear.
func TestTheSummaryLineDescribesTheResolvedShape(t *testing.T) {
	p := tubeDocument().Parts[0]
	got := Dimensions(p, Millimetre)
	if !strings.Contains(got, "⌀20") {
		t.Errorf("the summary of a retired shape is %q; it should read as the cylinder it is, "+
			"with a diameter", got)
	}
}
