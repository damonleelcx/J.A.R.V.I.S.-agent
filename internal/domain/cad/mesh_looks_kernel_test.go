package cad_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Looks designed, stages A5 (kernel half) and B6.

// A5: a mesh over the triangle budget is really coarsened. build123d's mesh() keeps
// any triangulation finer than asked, so until 2026-09-18 every coarser try got
// the first try's triangles back, and the angular limit — the one that binds on a
// curved face — was never coarsened at all.
func TestKernel_AMeshOverTheBudgetIsReallyCoarsened(t *testing.T) {
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("FORGE_CAD_PYTHON is unset; skipping the CAD kernel tests")
	}
	out, err := exec.Command(python, filepath.Join("testdata", "mesh_budget.py"), "sidecar.py").Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("mesh_budget.py: %v\n%s", err, exit.Stderr)
		}
		t.Fatal(err)
	}
	var got struct {
		Full, Tight struct {
			Triangles  int     `json:"mesh_triangles"`
			Angular    float64 `json:"mesh_angular"`
			Simplified bool    `json:"mesh_simplified"`
			Error      *string `json:"mesh_error"`
		}
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	t.Logf("%s", out)
	if got.Full.Triangles != 5000 || got.Full.Angular != 0.1 || got.Full.Simplified {
		t.Errorf("at the shipped budget: %+v; want 5,000 triangles at the 0.1 rad limit, not simplified", got.Full)
	}
	if got.Tight.Error != nil {
		t.Fatalf("a budget of 1,500 triangles could not be met: %s", *got.Tight.Error)
	}
	if got.Tight.Triangles > 1500 || !got.Tight.Simplified || got.Tight.Angular <= 0.1 {
		t.Errorf("under a 1,500 budget: %+v; want at most 1,500 triangles, simplified, a coarser angle", got.Tight)
	}
}

// A5: every vertex carries a unit normal from the surface. On a cylinder's side it
// is radial — smooth, not a facet's — and at the rims the side and the caps keep
// their own, so the hard edge stays hard. A turned copy's normals turn with it.
func TestKernel_AMeshCarriesSmoothNormalsAndKeepsHardEdges(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	const r, h = 20.0, 40.0
	cyl := func(id string, x float64, rot []float64) geometry.Part {
		return geometry.Part{ID: id, Name: id, Shape: "cylinder", Size: map[string]float64{"radius": r, "height": h},
			Position: []float64{x, 0, 0}, Rotation: rot}
	}
	doc := geometry.Document{Name: "pins", Units: "mm", Parts: []geometry.Part{
		cyl("upright", 0, []float64{0, 0, 0}), cyl("lying", 100, []float64{0, 0, 90})}}
	built, err := k.BuildMesh(ctx, doc, geometry.Millimetre)
	if err != nil {
		t.Fatal(err)
	}
	if built.Angular != 0.1 {
		t.Errorf("the mesh reports an angular limit of %g, want 0.1", built.Angular)
	}
	if len(built.MeshDefinitions) != 1 || len(built.MeshDefinitions[0].Normals) != len(built.MeshDefinitions[0].Vertices) {
		t.Fatalf("want one definition with one normal per vertex; got %d definitions", len(built.MeshDefinitions))
	}
	for _, m := range built.WorldMeshes() {
		// The axis this copy stands on, and its centre.
		axis, cx := [3]float64{0, 1, 0}, 0.0
		if m.ID == "lying" {
			axis, cx = [3]float64{-1, 0, 0}, 100
		}
		side, capped, rimSide, rimCap := 0, 0, 0, 0
		for i := 0; i+2 < len(m.Vertices); i += 3 {
			p := [3]float64{m.Vertices[i] - cx, m.Vertices[i+1], m.Vertices[i+2]}
			n := [3]float64{m.Normals[i], m.Normals[i+1], m.Normals[i+2]}
			if l := math.Sqrt(n[0]*n[0] + n[1]*n[1] + n[2]*n[2]); math.Abs(l-1) > 1e-3 {
				t.Fatalf("%s: vertex %d has a normal of length %g", m.ID, i/3, l)
			}
			along := p[0]*axis[0] + p[1]*axis[1] + p[2]*axis[2]
			radial := [3]float64{p[0] - along*axis[0], p[1] - along*axis[1], p[2] - along*axis[2]}
			rl := math.Sqrt(radial[0]*radial[0] + radial[1]*radial[1] + radial[2]*radial[2])
			onRim := math.Abs(math.Abs(along)-h/2) < 1e-6 && math.Abs(rl-r) < 1e-6
			dotAxis := n[0]*axis[0] + n[1]*axis[1] + n[2]*axis[2]
			switch {
			case rl > 1e-9 && math.Abs(n[0]-radial[0]/rl) < 1e-3 && math.Abs(n[1]-radial[1]/rl) < 1e-3 &&
				math.Abs(n[2]-radial[2]/rl) < 1e-3:
				side++
				if onRim {
					rimSide++
				}
			case math.Abs(math.Abs(dotAxis)-1) < 1e-3 && dotAxis*along > 0:
				capped++
				if onRim {
					rimCap++
				}
			default:
				t.Fatalf("%s: vertex %v has normal %v, neither radial nor along the axis outward", m.ID, p, n)
			}
		}
		if side == 0 || capped == 0 || rimSide == 0 || rimCap == 0 {
			t.Errorf("%s: %d side and %d cap normals, %d and %d on the rims; want both kinds, and both at the rims",
				m.ID, side, capped, rimSide, rimCap)
		}
	}
}

// B6: a perforation is cut as ONE boolean. 600 holes one at a time is minutes of
// OCCT (1,000 took 288 s); as one boolean it is seconds, and the volume is exact.
func TestKernel_APerforationIsCutAsOneBoolean(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	const n, W, D, R, H = 600, 1000.0, 600.0, 3.0, 5.0
	doc := geometry.Document{Name: "panel", Units: "mm", Parts: []geometry.Part{box("panel", W, H, D, 0, 0, 0)}}
	var with []string
	cols := 30
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("hole-%d", i)
		with = append(with, id)
		x := -W/2 + (float64(i%cols)+0.5)*W/float64(cols)
		z := -D/2 + (float64(i/cols)+0.5)*D/float64(n/cols)
		doc.Parts = append(doc.Parts, geometry.Part{ID: id, Name: id, Shape: "cylinder",
			Size: map[string]float64{"radius": R, "height": 20}, Position: []float64{x, 0, z}, Rotation: []float64{0, 0, 0}})
	}
	doc.Features = []geometry.Feature{{ID: "perforate", Op: "cut", Of: "panel", With: with}}
	start := time.Now()
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatalf("a 600-hole perforation did not build: %v", err)
	}
	t.Logf("600 holes: features phase %s, whole build %s", got.Phases.Features, time.Since(start))
	if len(got.FeatureFailures) != 0 || got.Parts != 1 {
		t.Fatalf("parts=%d failures=%v", got.Parts, got.FeatureFailures)
	}
	want := W*H*D - n*math.Pi*R*R*H
	if math.Abs(got.Volume-want) > 1e-6*want {
		t.Errorf("volume %.3f mm³, want %.3f", got.Volume, want)
	}
	// Generous: one boolean took ~1-3 s here under load; one at a time is past the
	// kernel's 30 s limit.
	if got.Phases.Features > 20*time.Second {
		t.Errorf("the cut took %s; one boolean with every tool takes seconds", got.Phases.Features)
	}
}
