package geometry

import (
	"math"
	"sort"
	"strings"
)

// Mass, centre of gravity and envelope, rolled up through the tree.
//
// Phase 5, stage V3 of docs/plan-2026-09-13-millions-of-parts.md. The kernel
// measures where each built part's volume is (cad.Kernel.BuildProperties); this
// weighs it by the part's material and adds it up for the whole model and for
// every assembly a part is placed under.
//
// # Why mass is sometimes not claimed
//
// Mass needs a density for every part. When any part has none, the roll-up is
// weighted by VOLUME, says so in Basis, and names the parts without one. The
// alternative — a default density, or leaving those parts out — produces a centre
// of gravity that looks exactly like a measured one and is not (decided
// 2026-09-15).

// SolidMeasure is one built part as the kernel measured it, in millimetres.
type SolidMeasure struct {
	ID string
	// Volume is in cubic millimetres.
	Volume float64
	// Centroid is the centre of volume, and Bounds minX, minY, minZ, maxX, maxY,
	// maxZ.
	Centroid [3]float64
	Bounds   [6]float64
	// Measured is false when the kernel could not read the part's centre or box.
	// Such a part is named in the report, never weighed.
	Measured bool
}

// The weights a roll-up used.
const (
	// MassByDensity: every part had a density; Mass is kilograms and Centre is
	// the centre of gravity.
	MassByDensity = "mass"
	// MassByVolume: some part had none; Mass is zero and Centre is the centre of
	// VOLUME, which is the centre of gravity only if the model is one material.
	MassByVolume = "volume"
)

// MassGroup is the whole model (Path "") or everything placed under one assembly
// occurrence (Path "left-wheel", "left-wheel/hub").
type MassGroup struct {
	Path   string     `json:"path"`
	Parts  int        `json:"parts"`
	Volume float64    `json:"volume_mm3"`
	Mass   float64    `json:"mass_kg"`
	Centre [3]float64 `json:"centre_mm"`
	Bounds [6]float64 `json:"bounds_mm"`
}

// MassReport is a roll-up and what it rests on.
type MassReport struct {
	Basis  string      `json:"basis"`
	Groups []MassGroup `json:"groups"`
	// WithoutDensity names the parts that made Basis MassByVolume.
	WithoutDensity []string `json:"without_density,omitempty"`
	// Unmeasured names parts the kernel built but could not measure, or that have
	// no volume (a surface): they are in no group.
	Unmeasured []string `json:"unmeasured,omitempty"`
}

// MassProperties rolls the kernel's measurements up through the document's tree.
//
// A part's material is looked up by its placed id in the expanded document, the
// ids the kernel was sent. A group per strict path prefix: "left-wheel/hub/bolt-3"
// adds to "", "left-wheel" and "left-wheel/hub".
func MassProperties(doc Document, measures []SolidMeasure) MassReport {
	density := map[string]float64{}
	for _, p := range doc.Expanded().Parts {
		if p.Material != nil && p.Material.Density > 0 {
			density[p.ID] = p.Material.Density
		}
	}

	report := MassReport{Basis: MassByDensity}
	usable := make([]SolidMeasure, 0, len(measures))
	for _, m := range measures {
		if !m.Measured || m.Volume <= 0 {
			report.Unmeasured = append(report.Unmeasured, m.ID)
			continue
		}
		usable = append(usable, m)
		if density[m.ID] <= 0 {
			report.WithoutDensity = append(report.WithoutDensity, m.ID)
		}
	}
	if len(report.WithoutDensity) > 0 {
		report.Basis = MassByVolume
	}

	type total struct {
		parts                int
		volume, mass, weight float64
		moment               [3]float64
		bounds               [6]float64
	}
	groups := map[string]*total{}
	add := func(path string, m SolidMeasure, mass, weight float64) {
		g := groups[path]
		if g == nil {
			g = &total{bounds: [6]float64{math.Inf(1), math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1), math.Inf(-1)}}
			groups[path] = g
		}
		g.parts++
		g.volume += m.Volume
		g.mass += mass
		g.weight += weight
		for a := 0; a < 3; a++ {
			g.moment[a] += weight * m.Centroid[a]
			g.bounds[a] = math.Min(g.bounds[a], m.Bounds[a])
			g.bounds[a+3] = math.Max(g.bounds[a+3], m.Bounds[a+3])
		}
	}
	for _, m := range usable {
		// Cubic millimetres to cubic metres, times kilograms per cubic metre.
		mass := m.Volume * 1e-9 * density[m.ID]
		weight := m.Volume
		if report.Basis == MassByDensity {
			weight = mass
		}
		add("", m, mass, weight)
		segments := strings.Split(m.ID, PathSeparator)
		for n := 1; n < len(segments); n++ {
			add(strings.Join(segments[:n], PathSeparator), m, mass, weight)
		}
	}

	for path, g := range groups {
		out := MassGroup{Path: path, Parts: g.parts, Volume: g.volume, Bounds: g.bounds}
		if report.Basis == MassByDensity {
			out.Mass = g.mass
		}
		if g.weight > 0 {
			for a := 0; a < 3; a++ {
				out.Centre[a] = g.moment[a] / g.weight
			}
		}
		report.Groups = append(report.Groups, out)
	}
	sort.Slice(report.Groups, func(i, j int) bool { return report.Groups[i].Path < report.Groups[j].Path })
	return report
}
