package geometry

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A synthetic airframe barrel, for the scale-up milestone of
// docs/plan-2026-09-13-millions-of-parts.md (1,000,000 occurrences).
//
// # What it is
//
// A barrel section along x, made of four definitions and four assemblies:
//
//   - a skin panel, a stringer, a frame segment and ONE rivet definition;
//   - "bay": one stringer pitch of one frame bay — its skin panel, two rows of
//     rivets along the stringer and two rows along each frame, every row a grid
//     pattern of the one rivet;
//   - "ring": the bay turned round the barrel by a polar pattern;
//   - "frame": a ring of frame segments, by a polar pattern;
//   - "barrel": the ring repeated along x by a linear pattern, a frame at every
//     station, and a stringer per sector the length of the barrel.
//
// Occurrences are sectors × (126 × bays + 2): 10,080 a bay at 80 sectors, plus
// 160. Nothing is listed: the rivets are three patterned children, so what is
// stored grows with the description and not with the count.
//
// # Its interference is known before anything is built
//
// In the bay's own frame x runs along the barrel, y points out through the skin
// and z runs round it. The skin is y ∈ [R, R+2]; the stringer is y ∈ [R−20.5,
// R−0.5], z ∈ [−10, 10]; the frame is y ∈ [R−120, R−25]; a rivet (radius 2.4,
// 14 long along y, centred at R−5) is y ∈ [R−12, R+2]. So:
//
//   - each of the 96 stringer rivets shares 2 mm of itself with the skin and
//     11.5 mm with the stringer: two clashes;
//   - each of the 28 frame rivets shares 2 mm with the skin only: one clash;
//   - nothing else touches. Panels and frame segments are 4 mm narrower than their
//     pitch, the stringer clears the skin by 0.5 mm and the frame by 4.5 mm,
//     rivets clear each other by at least 3.2 mm.
//
// Expected clashes: 220 per bay per sector, 17,600 × bays at 80 sectors.
const (
	barrelSectors     = 80
	barrelBayLength   = 500.0
	barrelChord       = 150.0 // stringer pitch measured at the skin's inner face
	barrelRivetsFound = 220   // clashes per bay per sector
)

// barrelRadius is the skin's inner radius that gives the chord its pitch.
func barrelRadius(sectors int) float64 {
	return barrelChord / 2 / math.Sin(math.Pi/float64(sectors))
}

// airframeBarrel is the barrel with `bays` frame bays and `sectors` stringers.
func airframeBarrel(bays, sectors int) Document {
	r := barrelRadius(sectors)
	rivet := Part{ID: "rivet", Name: "Rivet", Shape: "cylinder",
		Size: map[string]float64{"radius": 2.4, "height": 14}}
	skin := Part{ID: "skin", Name: "Skin panel", Shape: "box",
		Size: map[string]float64{"width": barrelBayLength - 2, "height": 2, "depth": barrelChord - 4}}
	stringer := Part{ID: "stringer", Name: "Stringer", Shape: "box",
		Size: map[string]float64{"width": barrelBayLength * float64(bays), "height": 20, "depth": 20}}
	segment := Part{ID: "frame-segment", Name: "Frame segment", Shape: "box",
		Size: map[string]float64{"width": 6, "height": 95,
			"depth": 2*(r-120)*math.Sin(math.Pi/float64(sectors)) - 4}}

	grid := func(rows, columns int, row, column []float64) *Pattern {
		return &Pattern{Kind: "grid", Rows: rows, Columns: columns, RowOffset: row, ColumnOffset: column}
	}
	polar := func() *Pattern { return &Pattern{Kind: "polar", Count: sectors, About: "x"} }

	bay := Assembly{ID: "bay", Name: "Bay", Children: []Child{
		{ID: "skin", Ref: "skin", Position: []float64{barrelBayLength / 2, r + 1, 0}},
		{ID: "stringer-rivets", Ref: "rivet", Position: []float64{15, r - 5, -5},
			Pattern: grid(2, 48, []float64{0, 0, 10}, []float64{10, 0, 0})},
		{ID: "frame-rivets-port", Ref: "rivet", Position: []float64{8, r - 5, -69},
			Pattern: grid(2, 7, []float64{barrelBayLength - 16, 0, 0}, []float64{0, 0, 8})},
		{ID: "frame-rivets-starboard", Ref: "rivet", Position: []float64{8, r - 5, 21},
			Pattern: grid(2, 7, []float64{barrelBayLength - 16, 0, 0}, []float64{0, 0, 8})},
	}}
	ring := Assembly{ID: "ring", Name: "Ring of bays", Children: []Child{
		{ID: "sector", Ref: "bay", Pattern: polar()},
	}}
	frame := Assembly{ID: "frame", Name: "Frame", Children: []Child{
		{ID: "segment", Ref: "frame-segment", Position: []float64{0, r - 72.5, 0}, Pattern: polar()},
	}}
	bayChild := Child{ID: "bay", Ref: "ring"}
	if bays > 1 {
		bayChild.Pattern = &Pattern{Kind: "linear", Count: bays, Offset: []float64{barrelBayLength, 0, 0}}
	}
	barrel := Assembly{ID: "barrel", Name: "Barrel section", Children: []Child{
		bayChild,
		{ID: "frame", Ref: "frame",
			Pattern: &Pattern{Kind: "linear", Count: bays + 1, Offset: []float64{barrelBayLength, 0, 0}}},
		{ID: "stringer", Ref: "stringer", Position: []float64{barrelBayLength * float64(bays) / 2, r - 10.5, 0},
			Pattern: polar()},
	}}
	return Document{Name: "Airframe barrel section", Units: "mm", Root: "barrel",
		NotVerified: []string{"a synthetic scale-up fixture, not an airframe design"},
		Definitions: []Part{rivet, skin, stringer, segment},
		Assemblies:  []Assembly{bay, ring, frame, barrel}}
}

func barrelOccurrences(bays, sectors int) int { return sectors * (126*bays + 2) }

// barrelSolids is the kernel request for already-expanded parts, for a design over
// the drawing ceiling that SolidsAndOperations refuses. It applies solid.go's
// rules for the two shapes the barrel uses; TestBarrel_TheMeasuredRequestIsTheKernelsRequest
// holds it to SolidsAndOperations where that answers.
func barrelSolids(parts []Part) []Solid {
	out := make([]Solid, 0, len(parts))
	for _, p := range parts {
		dims := map[string]float64{}
		switch p.Shape {
		case "box":
			dims["width"], dims["height"], dims["depth"] = p.Size["width"], p.Size["height"], p.Size["depth"]
		case "cylinder":
			dims["radius"], dims["height"] = p.Size["radius"], p.Size["height"]
			dims["radius_top"] = dims["radius"]
		}
		var pos [3]float64
		copy(pos[:], padTo3(p.Position))
		out = append(out, Solid{ID: p.ID, Label: p.Label(), Shape: p.Shape, Dims: dims,
			Matrix: RotationMatrix(p.RotationRadians()), Position: pos})
	}
	return out
}

// The generator describes the rivet once and places every one by a pattern.
func TestBarrel_PlacesItsRivetsByPatternsNotByListing(t *testing.T) {
	d := airframeBarrel(2, barrelSectors)
	if len(d.Definitions) != 4 || len(d.Assemblies) != 4 {
		t.Fatalf("%d definitions and %d assemblies, want 4 and 4", len(d.Definitions), len(d.Assemblies))
	}
	for _, p := range d.TreeProblems() {
		t.Errorf("tree problem: %s %s", p.Name, p.Detail)
	}
	if reps := d.EnumeratedRepetition(); len(reps) != 0 {
		t.Errorf("A3 found %d runs that could be patterns, first: %s", len(reps), reps[0].Warning())
	}
	rivetChildren := 0
	for _, a := range d.Assemblies {
		for _, c := range a.Children {
			if c.Ref == "rivet" {
				rivetChildren++
				if c.Pattern == nil {
					t.Errorf("%s/%s places the rivet without a pattern", a.ID, c.ID)
				}
			}
		}
	}
	if rivetChildren != 3 {
		t.Errorf("%d children place the rivet, want 3 patterned rows", rivetChildren)
	}

	want := barrelOccurrences(2, barrelSectors)
	if want != 20_320 {
		t.Fatalf("the formula says %d occurrences for 2 bays, want 20,320", want)
	}
	if n := occurrences(d, 1<<30); n != want {
		t.Errorf("counted %d occurrences, want %d", n, want)
	}
	parts := d.Expanded().Parts
	rivets := 0
	for _, p := range parts {
		if p.Shape == "cylinder" {
			rivets++
		}
	}
	if len(parts) != want || rivets != 2*barrelSectors*124 {
		t.Errorf("expanded to %d parts and %d rivets, want %d and %d", len(parts), rivets, want, 2*barrelSectors*124)
	}
	// What is stored is the description: a few kilobytes whatever the count.
	body, _ := json.Marshal(airframeBarrel(100, barrelSectors))
	if len(body) > 8<<10 {
		t.Errorf("a 1M-occurrence barrel is %d bytes of JSON; it should be its description, not its parts", len(body))
	}
	if n := occurrences(airframeBarrel(100, barrelSectors), 1<<30); n != 1_008_160 {
		t.Errorf("100 bays count %d occurrences, want 1,008,160", n)
	}
}

// The request the measurement sends past the drawing ceiling is the request the
// Go path sends below it.
func TestBarrel_TheMeasuredRequestIsTheKernelsRequest(t *testing.T) {
	d := airframeBarrel(1, 8) // 1,024 occurrences: drawable
	want, ops, _, inferred := SolidsAndOperations(d, Millimetre)
	if len(want) != barrelOccurrences(1, 8) || len(ops) != 0 || len(inferred) != 0 {
		t.Fatalf("SolidsAndOperations: %d solids, %d operations, notes %q", len(want), len(ops), inferred)
	}
	if got := barrelSolids(d.Expanded().Parts); !reflect.DeepEqual(got, want) {
		for i := range want {
			if !reflect.DeepEqual(got[i], want[i]) {
				t.Fatalf("solid %d differs:\n got %+v\nwant %+v", i, got[i], want[i])
			}
		}
		t.Fatalf("the requests differ in length: %d, want %d", len(got), len(want))
	}
}

// TestScaleUp_MeasureAirframeBarrel is the Go half of
// docs/spikes/2026-09-15-one-million-occurrences. It runs only when
// FORGE_SCALE_MEASURE_OUT names a directory: it writes each size's kernel request
// there for measure.py, and prints what Go spends. When measure.py has written a
// mesh reply for a size (mesh-<bays>.json), it also re-encodes that as the mesh
// endpoint's payload.
func TestScaleUp_MeasureAirframeBarrel(t *testing.T) {
	out := os.Getenv("FORGE_SCALE_MEASURE_OUT")
	if out == "" {
		t.Skip("set FORGE_SCALE_MEASURE_OUT to a directory to measure the barrel")
	}
	sizes := []int{1, 9, 10, 30, 100}
	if s := os.Getenv("FORGE_SCALE_MEASURE_BAYS"); s != "" {
		sizes = nil
		for _, f := range strings.Split(s, ",") {
			n, err := strconv.Atoi(strings.TrimSpace(f))
			if err != nil {
				t.Fatal(err)
			}
			sizes = append(sizes, n)
		}
	}
	writeRequests := os.Getenv("FORGE_SCALE_MEASURE_NO_REQUESTS") == ""
	mib := func(b uint64) float64 { return float64(b) / (1 << 20) }

	for _, bays := range sizes {
		d := airframeBarrel(bays, barrelSectors)
		rec := map[string]any{"bays": bays, "sectors": barrelSectors, "cpus": runtime.NumCPU()}
		body, _ := json.Marshal(d)
		rec["document_bytes"] = len(body)
		// Added 2026-09-15 (repair bound and check profile): the document itself, which
		// internal/agent's TestScaleUp_MeasureTheRepairPrompt traces the kernel's clashes
		// through. A few kilobytes at every size.
		if err := os.WriteFile(filepath.Join(out, fmt.Sprintf("barrel-doc-%d.json", bays)), body, 0o644); err != nil {
			t.Fatal(err)
		}
		rec["definitions"] = len(d.Definitions)
		rec["assemblies"] = len(d.Assemblies)

		// The storage door at the DEFAULT limits.
		SetLimits(Limits{})
		start := time.Now()
		counted := occurrences(d, 1<<30)
		rec["count_ms"] = float64(time.Since(start).Microseconds()) / 1000
		rec["occurrences"] = counted
		rec["door_bytes_ok"] = int64(len(body)) <= DefaultMaxDocumentBytes
		rec["door_definitions_ok"] = len(d.Definitions) <= DefaultMaxDefinitions
		verdict := "accepted"
		if err := (&NewVariant{InitiatorID: "u", Agent: "converse", Generator: "g", Inputs: map[string]any{},
			Document: d}).Validate(); err != nil {
			verdict = err.Error()
		}
		rec["door_default"] = verdict
		rec["draw_refusal"] = d.DrawRefusal() != ""

		// Everything after the door needs a bound above the size.
		SetLimits(Limits{MaxOccurrences: 2_000_000})
		var expanded []Part
		for run := 1; run <= 2; run++ {
			expanded = nil
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			start = time.Now()
			e := d.Expanded()
			elapsed := time.Since(start)
			runtime.ReadMemStats(&after)
			expanded = e.Parts
			rec[fmt.Sprintf("expand_ms_%d", run)] = float64(elapsed.Microseconds()) / 1000
			rec[fmt.Sprintf("expand_alloc_mib_%d", run)] = mib(after.TotalAlloc - before.TotalAlloc)
			rec[fmt.Sprintf("expand_heap_mib_%d", run)] = mib(after.HeapAlloc)
		}
		rec["expanded_parts"] = len(expanded)
		start = time.Now()
		rec["enumerated_repetition"] = len(d.EnumeratedRepetition())
		rec["repetition_ms"] = float64(time.Since(start).Microseconds()) / 1000

		// The kernel request, as roundTrip would marshal it.
		start = time.Now()
		solids := barrelSolids(expanded)
		rec["solids_ms"] = float64(time.Since(start).Microseconds()) / 1000
		if writeRequests {
			path := filepath.Join(out, fmt.Sprintf("barrel-%d.json", bays))
			start = time.Now()
			n, err := writeRequest(path, solids)
			if err != nil {
				t.Fatal(err)
			}
			rec["request_encode_ms"] = float64(time.Since(start).Microseconds()) / 1000
			rec["request_bytes"] = n
		}

		// V3's roll-up, fed synthetic measurements: the Go half of mass properties.
		measures := syntheticMeasures(expanded)
		for run := 1; run <= 2; run++ {
			runtime.GC()
			start = time.Now()
			report := MassProperties(d, measures)
			rec[fmt.Sprintf("mass_ms_%d", run)] = float64(time.Since(start).Microseconds()) / 1000
			rec["mass_groups"] = len(report.Groups)
		}
		solids, measures, expanded = nil, nil, nil

		// The mesh endpoint's payload, when measure.py has written the kernel's reply.
		if meshPath := filepath.Join(out, fmt.Sprintf("mesh-%d.json", bays)); fileExists(meshPath) {
			measureMeshPayload(t, meshPath, rec)
		}
		SetLimits(Limits{})

		line, _ := json.Marshal(rec)
		fmt.Printf("GO %s\n", line)
		if err := os.WriteFile(filepath.Join(out, fmt.Sprintf("go-%d.json", bays)), line, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// writeRequest streams {"solids": [...], "operations": [], "format": ""}.
func writeRequest(path string, solids []Solid) (int64, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<20)
	if _, err := w.WriteString(`{"operations":[],"format":"","solids":[`); err != nil {
		return 0, err
	}
	for i, s := range solids {
		if i > 0 {
			_ = w.WriteByte(',')
		}
		b, err := json.Marshal(s)
		if err != nil {
			return 0, err
		}
		_, _ = w.Write(b)
	}
	_, _ = w.WriteString("]}\n")
	if err := w.Flush(); err != nil {
		return 0, err
	}
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}

// syntheticMeasures stands in for the kernel's part properties: the shape's exact
// volume, its centre at its position, a box its size either side. Enough to time
// the roll-up; not a measurement of the barrel's mass.
func syntheticMeasures(parts []Part) []SolidMeasure {
	out := make([]SolidMeasure, 0, len(parts))
	for _, p := range parts {
		var volume, half float64
		switch p.Shape {
		case "box":
			volume = p.Size["width"] * p.Size["height"] * p.Size["depth"]
			half = math.Max(p.Size["width"], math.Max(p.Size["height"], p.Size["depth"])) / 2
		case "cylinder":
			volume = math.Pi * p.Size["radius"] * p.Size["radius"] * p.Size["height"]
			half = math.Max(p.Size["radius"], p.Size["height"]/2)
		}
		var c [3]float64
		copy(c[:], padTo3(p.Position))
		out = append(out, SolidMeasure{ID: p.ID, Volume: volume, Centroid: c, Measured: true,
			Bounds: [6]float64{c[0] - half, c[1] - half, c[2] - half, c[0] + half, c[1] + half, c[2] + half}})
	}
	return out
}

// measureMeshPayload decodes the kernel's mesh fields as cad does and encodes them
// as the mesh endpoint's "definitions" and "instances" (httpapi/geometry.go).
func measureMeshPayload(t *testing.T, path string, rec map[string]any) {
	t.Helper()
	type definition struct {
		Vertices  []float64 `json:"vertices"`
		Triangles []int32   `json:"triangles"`
	}
	type instance struct {
		ID         string      `json:"id"`
		Label      string      `json:"label"`
		Definition int         `json:"definition"`
		Matrix     [16]float64 `json:"matrix"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var reply struct {
		Definitions []definition `json:"mesh_definitions"`
		Instances   []instance   `json:"mesh_instances"`
	}
	start := time.Now()
	if err := json.Unmarshal(raw, &reply); err != nil {
		t.Fatal(err)
	}
	rec["mesh_decode_ms"] = float64(time.Since(start).Microseconds()) / 1000
	raw = nil
	start = time.Now()
	body, err := json.Marshal(map[string]any{"definitions": reply.Definitions, "instances": reply.Instances})
	if err != nil {
		t.Fatal(err)
	}
	rec["mesh_encode_ms"] = float64(time.Since(start).Microseconds()) / 1000
	rec["mesh_endpoint_bytes"] = len(body)
	rec["mesh_endpoint_instances"] = len(reply.Instances)
	rec["mesh_endpoint_definitions"] = len(reply.Definitions)
}
