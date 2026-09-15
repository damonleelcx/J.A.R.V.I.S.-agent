package geometry

import (
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
)

// What one stored design may be, and how much of one FORGE draws or builds at once.
//
// Phase 3, stage S0 of docs/plan-2026-09-13-millions-of-parts.md. Decided
// 2026-09-14:
//
//   - STORAGE is bounded by the size of what a design describes: 2 MiB of JSON,
//     2000 definitions and 100,000 occurrences by default, each configurable
//     (FORGE_GEOMETRY_MAX_DOCUMENT_BYTES, _MAX_DEFINITIONS, _MAX_OCCURRENCES).
//   - The 4096-part ceiling applies to DRAWING and BUILDING only. A 30k-occurrence
//     car can be saved and versioned; the viewport, the mesh and the kernel refuse
//     it, loudly and whole, until instancing lands.
//
// Measured before deciding. A car of 150 definitions and 10 assemblies is ~96 KiB
// stored as a tree, and would be ~10 MiB written flat at 30k: a design grows with
// what it describes once, not with how often it is placed. Expanding occurrences
// costs 7 ms / 17 MiB at 30k, 20 ms / 52 MiB at 100k and 185 ms / 573 MiB at 1M,
// which is why the occurrence bound is 100k and not the final milestone.
//
// # Why occurrences are counted, not expanded
//
// Patterns nest 16 deep at up to 512 copies each, so a few hundred bytes can
// describe more occurrences than any machine can hold, and every reader that
// expands a tree — Faults, the mesh, measurement, the storage door — would try.
// occurrences() multiplies the counts instead, saturating past the bound, so a
// runaway design costs microseconds to refuse.
type Limits struct {
	MaxDocumentBytes int64
	MaxDefinitions   int
	MaxOccurrences   int
}

const (
	DefaultMaxDocumentBytes int64 = 2 << 20
	DefaultMaxDefinitions         = 2000
	DefaultMaxOccurrences         = 100_000
)

// maxDrawnParts is how many parts FORGE BUILDS at once for everything but a view: a
// STEP export, mass properties, the Go mesh and every Go mesh export. The name is from
// before Phase 6, when it bounded the viewport too; maxViewportParts is the viewport's
// own, and maxBuiltParts the kernel's for a view.
//
// ‼️ This is a PRE-INSTANCING ceiling, not a statement of what a design can
// describe (it bounded the tree itself until S0). K1 (one build per definition) has
// landed, but a build past it has only been measured through the sidecar directly
// (26 s at 30k in docs/spikes/2026-09-15-mesh-per-definition), never through
// BuildDocument, so it stays until it is.
const maxDrawnParts = 4096

// maxBuiltParts is how many parts the CAD kernel builds at once for a VIEW: the whole
// design's mesh, a subtree's, and a script check (cad.Kernel.BuildMesh). Nothing else.
//
// ‼️ Raised from 4096 on a measured number, for this path only: forged in a Linux
// container limited like its pod (1 CPU, 1 GiB, deploy/k8s/30-forged.yaml), through GET
// /v1/geometry/{id}/mesh with the shipped 30 s kernel timeout, built an 8,315-part car in
// 12.1-13.0 s and an 8,192-part barrel in 5.7-8.8 s, three runs each, the container
// peaking at 372 MiB (docs/spikes/2026-09-15-ceiling-on-linux). Not 16,384: the car took
// 17.3-21.5 s there, less than twice under the timeout. amd64, not the arm64 node. STEP
// export and mass properties were not measured past 4096 and keep maxDrawnParts.
// forge3d.js holds the same number as LAZY_OCCURRENCES, and
// TestRendererLoadsLazilyExactlyPastTheKernelsViewCeiling holds the two together.
const maxBuiltParts = 8192

// maxViewportParts is how many parts the browser draws at once.
//
// Raised from 4096 by Phase 6, stage W1 (instanced drawing): every copy of a
// definition is one instance of one uploaded mesh, drawn with one call, so what
// grows with a design's size is 21 floats a copy and a sphere test, not a buffer and
// a draw call. Set to the default storage bound, so a design FORGE accepts with its
// default settings can be looked at.
//
// ‼️ Raised on a measured number, and only that one: a 99,971-occurrence car drawn in
// Chrome 152 over WebGL2 on an Intel UHD iGPU (ANGLE, D3D11) loaded in 341 ms and
// drew in 23.4 ms a frame median with its level of detail, 41.8 ms without, CPU and
// GPU together; the same car one call per copy took 148 ms. One laptop, one run, a
// shared machine: docs/spikes/2026-09-15-instanced-viewport. It is not a claim of 60
// frames a second at the ceiling. forge3d.js holds the same number as
// MAX_VIEWPORT_PARTS, and TestRendererFlattensATreeLikeTheExporter the words.
const maxViewportParts = DefaultMaxOccurrences

// current is process-wide and set once at start (ConfigureLimits).
//
// ‼️ Why not a parameter: the expansions that need the occurrence bound run inside
// Faults, the mesh, measurement and the kernel request, which carry no
// configuration and describe geometry rather than policy. Threading a Limits
// through every one of them would put an operator's setting into every signature
// that reads a document. Tests that change it restore it.
var current atomic.Pointer[Limits]

// SetLimits replaces the process's limits. A field that is not positive keeps its
// default, so a partial setting cannot switch a bound off.
func SetLimits(l Limits) {
	if l.MaxDocumentBytes <= 0 {
		l.MaxDocumentBytes = DefaultMaxDocumentBytes
	}
	if l.MaxDefinitions <= 0 {
		l.MaxDefinitions = DefaultMaxDefinitions
	}
	if l.MaxOccurrences <= 0 {
		l.MaxOccurrences = DefaultMaxOccurrences
	}
	current.Store(&l)
}

// ConfigureLimits sets the process's limits from its configuration. Called once,
// at start, by every process that reads geometry.
func ConfigureLimits(c config.GeometryConfig) {
	SetLimits(Limits{MaxDocumentBytes: c.MaxDocumentBytes, MaxDefinitions: c.MaxDefinitions,
		MaxOccurrences: c.MaxOccurrences})
}

// CurrentLimits are the limits in force.
func CurrentLimits() Limits {
	if l := current.Load(); l != nil {
		return *l
	}
	return Limits{MaxDocumentBytes: DefaultMaxDocumentBytes, MaxDefinitions: DefaultMaxDefinitions,
		MaxOccurrences: DefaultMaxOccurrences}
}

// DrawRefusal is why this design is not BUILT — sent to the kernel, meshed in Go or
// exported — or "" when it can be. The viewport's own answer is ViewportRefusal.
//
// A design over the ceiling is refused WHOLE: the first 4096 parts of a car would
// pass for the car.
func (d Document) DrawRefusal() string {
	if occurrences(d, maxDrawnParts) <= maxDrawnParts {
		return ""
	}
	return fmt.Sprintf("This design places more than %d parts, which is the most FORGE builds or exports "+
		"at once until building at that size has been measured. It is stored as it is, and the viewport "+
		"can still show it if it places no more than %d; nothing was built.", maxDrawnParts, maxViewportParts)
}

// BuildRefusal is why the CAD kernel does not build this design for a view
// (cad.Kernel.BuildMesh), or "" when it does. Every other build and export answers
// DrawRefusal, which is tighter: past maxDrawnParts only a view is built.
func (d Document) BuildRefusal() string {
	if occurrences(d, maxBuiltParts) <= maxBuiltParts {
		return ""
	}
	return fmt.Sprintf("This design places more than %d parts, which is the most the FORGE CAD kernel builds "+
		"at once for a view. It is stored as it is, and the viewport can still show it if it places no more "+
		"than %d; nothing was built.", maxBuiltParts, maxViewportParts)
}

// ViewportRefusal is why the browser does not draw this design, or "" when it does.
//
// Refused whole for the same reason as DrawRefusal: the first hundred thousand
// parts of a design would pass for the design. The wording is shared with
// forge3d.js (drawRefusal), which TestRendererFlattensATreeLikeTheExporter holds
// to it.
func (d Document) ViewportRefusal() string {
	if occurrences(d, maxViewportParts) <= maxViewportParts {
		return ""
	}
	return fmt.Sprintf("This design places more than %d parts, which is the most the FORGE viewport draws "+
		"at once. It is stored as it is; nothing was drawn.", maxViewportParts)
}

// occurrenceProblem refuses a design that describes more occurrences than FORGE
// stores, before anything expands it. nil when it is within the bound.
func occurrenceProblem(d Document) *Problem {
	limit := CurrentLimits().MaxOccurrences
	if occurrences(d, limit) <= limit {
		return nil
	}
	name := strings.TrimSpace(d.Name)
	if name == "" {
		name = "this design"
	}
	return &Problem{Severity: Error, Name: name,
		Detail: fmt.Sprintf("describes more than %d occurrences, the most FORGE stores in one design "+
			"(FORGE_GEOMETRY_MAX_OCCURRENCES), so it was not expanded; split it into separate designs, or "+
			"raise the setting on a node that can hold what it expands to", limit)}
}

// occurrences counts the parts a document places — top-level parts and their
// repeat copies, and everything its tree places — without placing any of them.
// It saturates at limit+1, so a design that asks for more costs nothing extra.
//
// Rules are the expansions' own: a refused pattern or repeat places nothing, a
// pattern or repeat of one places one. Depth is not bounded here and a cycle
// counts nothing, so the count is never below what the walk would place.
func occurrences(d Document, limit int) int {
	sat := func(n int) int {
		if n > limit {
			return limit + 1
		}
		return n
	}
	mul := func(a, b int) int {
		if a == 0 || b == 0 {
			return 0
		}
		if a > (limit+1)/b {
			return limit + 1
		}
		return sat(a * b)
	}

	total := 0
	for _, p := range d.Parts {
		total = sat(total + repeatCount(p))
	}
	if d.Root == "" {
		return total
	}
	defs := map[string]Part{}
	for _, p := range d.Definitions {
		if strings.TrimSpace(p.ID) != "" && defs[p.ID].ID == "" {
			defs[p.ID] = p
		}
	}
	asms := map[string]Assembly{}
	for _, a := range d.Assemblies {
		_, isDef := defs[a.ID]
		if strings.TrimSpace(a.ID) != "" && asms[a.ID].ID == "" && !isDef {
			asms[a.ID] = a
		}
	}
	memo := map[string]int{}
	onPath := map[string]bool{}
	var count func(id string) int
	count = func(id string) int {
		if def, ok := defs[id]; ok {
			return repeatCount(def)
		}
		a, ok := asms[id]
		if !ok || onPath[id] {
			return 0
		}
		if n, ok := memo[id]; ok {
			return n
		}
		onPath[id] = true
		n := 0
		for _, c := range a.Children {
			slots, _ := c.Pattern.copies()
			n = sat(n + mul(len(slots), count(c.Ref)))
		}
		delete(onPath, id)
		memo[id] = n
		return n
	}
	return sat(total + count(d.Root))
}

// repeatCount is how many parts one part places after its own repeat: the rule
// expandRepeats follows.
func repeatCount(p Part) int {
	switch r := p.Repeat; {
	case r == nil || r.Count < 2:
		return 1
	case r.Count > maxRepeat:
		return 0
	default:
		return r.Count
	}
}
