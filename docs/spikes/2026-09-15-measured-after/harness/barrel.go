//go:build ignore

// Writes the airframe barrel of docs/spikes/2026-09-15-one-million-occurrences (#89) as a
// geometry document, for the kernel build ceiling measurement in README.md beside this file.
//
// # Why a copy of #89's generator
//
// airframeBarrel lives in internal/domain/geometry/barrel_scale_test.go on
// scale/one-million, which is not in this branch. This is the same document — four
// definitions (rivet, skin panel, stringer, frame segment), the rivet placed by three
// grid patterns in a bay, the bay turned round by a polar pattern and repeated along x —
// with the sector count as a second knob, so a single bay can be 4,096 and 8,192
// occurrences. Occurrences are sectors × (126 × bays + 2).
//
// The barrel is here because its stringers are long boxes: V1's broad phase tests a box
// longer than four grid cells against every box, which #89 measured as quadratic and which
// is fixed on a branch this one does not include. The car's longest parts are panels.
//
//	go run docs/spikes/2026-09-15-kernel-build-ceiling/barrel.go -out DIR
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

const (
	bayLength = 500.0
	chord     = 150.0 // stringer pitch at the skin's inner face
)

func barrel(bays, sectors int) geometry.Document {
	r := chord / 2 / math.Sin(math.Pi/float64(sectors))
	rivet := geometry.Part{ID: "rivet", Name: "Rivet", Shape: "cylinder",
		Size: map[string]float64{"radius": 2.4, "height": 14}}
	skin := geometry.Part{ID: "skin", Name: "Skin panel", Shape: "box",
		Size: map[string]float64{"width": bayLength - 2, "height": 2, "depth": chord - 4}}
	stringer := geometry.Part{ID: "stringer", Name: "Stringer", Shape: "box",
		Size: map[string]float64{"width": bayLength * float64(bays), "height": 20, "depth": 20}}
	segment := geometry.Part{ID: "frame-segment", Name: "Frame segment", Shape: "box",
		Size: map[string]float64{"width": 6, "height": 95,
			"depth": 2*(r-120)*math.Sin(math.Pi/float64(sectors)) - 4}}
	grid := func(rows, columns int, row, column []float64) *geometry.Pattern {
		return &geometry.Pattern{Kind: "grid", Rows: rows, Columns: columns, RowOffset: row, ColumnOffset: column}
	}
	polar := func() *geometry.Pattern { return &geometry.Pattern{Kind: "polar", Count: sectors, About: "x"} }
	bay := geometry.Assembly{ID: "bay", Name: "Bay", Children: []geometry.Child{
		{ID: "skin", Ref: "skin", Position: []float64{bayLength / 2, r + 1, 0}},
		{ID: "stringer-rivets", Ref: "rivet", Position: []float64{15, r - 5, -5},
			Pattern: grid(2, 48, []float64{0, 0, 10}, []float64{10, 0, 0})},
		{ID: "frame-rivets-port", Ref: "rivet", Position: []float64{8, r - 5, -69},
			Pattern: grid(2, 7, []float64{bayLength - 16, 0, 0}, []float64{0, 0, 8})},
		{ID: "frame-rivets-starboard", Ref: "rivet", Position: []float64{8, r - 5, 21},
			Pattern: grid(2, 7, []float64{bayLength - 16, 0, 0}, []float64{0, 0, 8})},
	}}
	ring := geometry.Assembly{ID: "ring", Name: "Ring of bays", Children: []geometry.Child{
		{ID: "sector", Ref: "bay", Pattern: polar()},
	}}
	frame := geometry.Assembly{ID: "frame", Name: "Frame", Children: []geometry.Child{
		{ID: "segment", Ref: "frame-segment", Position: []float64{0, r - 72.5, 0}, Pattern: polar()},
	}}
	bayChild := geometry.Child{ID: "bay", Ref: "ring"}
	if bays > 1 {
		bayChild.Pattern = &geometry.Pattern{Kind: "linear", Count: bays, Offset: []float64{bayLength, 0, 0}}
	}
	root := geometry.Assembly{ID: "barrel", Name: "Barrel section", Children: []geometry.Child{
		bayChild,
		{ID: "frame", Ref: "frame",
			Pattern: &geometry.Pattern{Kind: "linear", Count: bays + 1, Offset: []float64{bayLength, 0, 0}}},
		{ID: "stringer", Ref: "stringer", Position: []float64{bayLength * float64(bays) / 2, r - 10.5, 0},
			Pattern: polar()},
	}}
	return geometry.Document{Name: "Airframe barrel section", Units: "mm", Root: "barrel",
		NotVerified: []string{"a synthetic scale-up fixture, not an airframe design"},
		Definitions: []geometry.Part{rivet, skin, stringer, segment},
		Assemblies:  []geometry.Assembly{bay, ring, frame, root}}
}

func main() {
	out := flag.String("out", ".", "directory to write barrel-<occurrences>.json into")
	flag.Parse()
	// bays, sectors: 4,096; 8,192; 16,256; 30,400; 60,640 occurrences.
	for _, size := range [][2]int{{1, 32}, {1, 64}, {2, 64}, {3, 80}, {6, 80}} {
		d := barrel(size[0], size[1])
		n := len(d.Expanded().Parts)
		if want := size[1] * (126*size[0] + 2); n != want {
			fmt.Fprintf(os.Stderr, "%d bays × %d sectors expanded to %d parts, want %d\n", size[0], size[1], n, want)
			os.Exit(1)
		}
		body, err := json.Marshal(d)
		if err != nil {
			panic(err)
		}
		path := filepath.Join(*out, fmt.Sprintf("barrel-%d.json", n))
		if err := os.WriteFile(path, body, 0o600); err != nil {
			panic(err)
		}
		fmt.Printf("%s: %d bays × %d sectors, %d occurrences, %d bytes\n", path, size[0], size[1], n, len(body))
	}
}
