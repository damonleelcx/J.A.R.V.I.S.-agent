package geometry

import (
	"fmt"
	"sort"
	"strings"
)

// Whether the solid that was built could be MADE.
//
// # The gap this closes
//
// Issue 6: "the kernel builds geometry but never evaluates it". Interference
// closed the first half of it — two parts in the same place are now found and
// said (interference.go, Phase 5 stages V1 and V2). This is the second half, and
// it is a different question: not "do these parts collide" but "can this one part
// come off a machine at all".
//
// A wall a thousandth of a millimetre thick, a pocket with a perfectly sharp
// inside corner, a moulded boss with vertical sides, a printed roof with nothing
// under it — every one of those exports as a clean, well-formed, valid STEP file.
// OCCT is content; the mesh is watertight; the picture looks right. Nothing in
// FORGE before this looked.
//
// # The rule this file obeys: the kernel MEASURES, Go JUDGES
//
// cad/sidecar.py returns five numbers per built part and nothing else. It holds no
// limit, knows no process, and has no opinion. Every limit is HERE, in one table
// (Profiles), each with where the number came from, and each marked UNVALIDATED
// because every one of them is a published rule of thumb rather than something
// FORGE has measured. A second copy of a limit anywhere else is the defect this
// arrangement exists to prevent.
//
// # Why nothing is repaired
//
// Interference repairs only the BURIED case, and the reasoning there applies here
// with more force: a thin wall may be a deliberate membrane, a sharp internal
// corner may be a part that is cast rather than cut, a 60° overhang may be printed
// on supports somebody has already planned. None of those is a defect FORGE can
// see the difference from. So a finding is reported to the reader, in the turn,
// beside the coverage notes — and the document is left exactly as the model wrote
// it.
//
// # Why a part with no process named is not checked
//
// How a part is made is a manufacturing DECISION, like a tolerance, and this
// repository already refuses to invent one of those: "a manufacturing decision
// about process and fit, and the same cylinder is ±0.5 or ±0.005 depending on what
// it does". A default process would make the same mistake with more numbers — the
// same 0.9 mm wall is fine milled, marginal moulded and impossible in metal
// powder. So a part that says nothing is checked against nothing, and is NAMED as
// unchecked rather than counted as clean.

// Process is how a part is made. The model names one per part; the set is closed,
// because every name here has to have a row in Profiles.
type Process string

const (
	// ProcessMilling3Axis — cut from solid stock by a rotating tool that reaches
	// from one direction.
	ProcessMilling3Axis Process = "milling-3-axis"
	// ProcessInjectionMoulding — plastic forced into a steel tool and pulled out
	// of it, which is what makes draft a rule rather than a preference.
	ProcessInjectionMoulding Process = "injection-moulding"
	// ProcessPrintingFDM — fused filament, laid down in layers on air or on
	// support.
	ProcessPrintingFDM Process = "printing-fdm"
	// ProcessPrintingSLM — metal powder fused by a laser, layer by layer.
	ProcessPrintingSLM Process = "printing-slm"
)

// The rules, by the name a finding prints.
const (
	RuleMinWall        = "minimum wall thickness"
	RuleMinFeature     = "minimum feature size"
	RuleInternalRadius = "internal corner radius"
	RuleDraft          = "draft angle"
	RuleOverhang       = "unsupported overhang"
)

// Limit is one number in the table, with where it came from.
//
// ‼️ Validated is false on every row in this file, and the turn says UNVALIDATED
// beside every finding because of it. These are published design-guide rules of
// thumb: real, widely used, and not the same thing as a number FORGE has checked
// against a part somebody actually made. The field exists so that the day one of
// them IS checked, the turn stops saying it — rather than the label quietly
// covering numbers of two different kinds.
type Limit struct {
	// Value is in MILLIMETRES for a length and DEGREES for an angle, like every
	// other number that crosses the kernel boundary.
	Value float64
	// Source is the citation. Never empty on a row that applies.
	Source string
	// Validated says FORGE has checked this number against something of its own.
	Validated bool
	// applies is false for a rule this process has no opinion about: a printed
	// part has no draft, and a milled part has no unsupported overhang because
	// the stock holds itself up.
	applies bool
}

// Applies reports whether the process this limit came from has this rule at all.
func (l Limit) Applies() bool { return l.applies }

// cited builds a limit that applies.
func cited(value float64, source string) Limit {
	return Limit{Value: value, Source: source, applies: true}
}

// ProcessProfile is one process's whole row.
type ProcessProfile struct {
	Name Process
	// Label is the words a finding prints.
	Label string
	// MinWall and MinFeature are floors: below them is a finding.
	MinWall    Limit
	MinFeature Limit
	// InternalRadius is a floor too. Zero measured means a perfectly sharp inside
	// corner, which no rotating tool can cut.
	InternalRadius Limit
	// Draft is a floor: a moulded wall flatter than this against the pull drags
	// on the tool.
	Draft Limit
	// Overhang is a CEILING: above it the layer is being laid on air.
	Overhang Limit
}

// Profiles is the one table. Every limit FORGE checks is in it, and nothing
// outside this file holds a copy — the contract is rendered from it
// (ProcessGuide), the findings are judged from it, and the fences read it.
//
// ‼️ Every number below is a published design-guide rule of thumb. None has been
// validated against a part FORGE has seen made, and each finding says so.
var Profiles = map[Process]ProcessProfile{
	ProcessMilling3Axis: {
		Name: ProcessMilling3Axis, Label: "3-axis milling",
		// Machining design guides put the thin-wall floor at 0.5 mm in metal and
		// 1.5 mm in plastic; 0.8 mm is the middle of that and the figure the
		// widest-used guide prints for aluminium.
		MinWall: cited(0.8, "CNC machining design guides (Protolabs, Xometry, 2024): "+
			"0.5 mm metal to 1.5 mm plastic thin-wall floor"),
		// The smallest end mill a shop stocks as a matter of course is about 1 mm;
		// below that is micro-machining, quoted separately.
		MinFeature: cited(1.0, "CNC machining design guides: 1 mm smallest routinely stocked cutter"),
		// An inside corner is the cutter's own radius. A 1.6 mm cutter leaves
		// 0.8 mm, and a perfectly square inside corner cannot be milled at all.
		InternalRadius: cited(0.8, "geometric: an internal corner cannot be sharper than "+
			"the cutter, and 1.6 mm is the smallest routinely used"),
		// A 3-axis part is held in stock that supports itself, and it is not
		// pulled out of a tool.
		Draft:    Limit{},
		Overhang: Limit{},
	},
	ProcessInjectionMoulding: {
		Name: ProcessInjectionMoulding, Label: "injection moulding",
		// Most thermoplastics mould between 1 and 4 mm; below 1 mm the cavity does
		// not fill reliably.
		MinWall: cited(1.0, "injection moulding design guides (Protolabs, Xometry, 2024): "+
			"1.0-4.0 mm typical wall for thermoplastics"),
		MinFeature: cited(0.5, "injection moulding design guides: 0.5 mm smallest reliably moulded detail"),
		// A sharp inside corner in a moulding is a stress riser and a flow
		// obstruction; the guides ask for a radius of at least half the wall.
		InternalRadius: cited(0.5, "injection moulding design guides: internal radius at least "+
			"0.5x wall, never square"),
		// The number everybody prints: 1 degree per side, more for texture.
		Draft: cited(1.0, "injection moulding design guides: 1 degree per side minimum, "+
			"1.5-2 degrees for a textured face"),
		Overhang: Limit{},
	},
	ProcessPrintingFDM: {
		Name: ProcessPrintingFDM, Label: "FDM printing",
		// Two passes of a 0.4 mm nozzle.
		MinWall:    cited(0.8, "FDM design guides: two perimeters of a 0.4 mm nozzle"),
		MinFeature: cited(0.8, "FDM design guides: 0.4 mm nozzle, smallest reproducible detail ~2 line widths"),
		// Nothing cuts an FDM corner, so there is no corner rule.
		InternalRadius: Limit{},
		Draft:          Limit{},
		// The classic figure, and the default in every slicer.
		Overhang: cited(45, "FDM design guides and slicer defaults: 45 degrees from vertical "+
			"before support is needed"),
	},
	ProcessPrintingSLM: {
		Name: ProcessPrintingSLM, Label: "SLM metal printing",
		MinWall:        cited(0.4, "metal powder-bed design guides (EOS, Renishaw, 2024): 0.4 mm minimum wall"),
		MinFeature:     cited(0.3, "metal powder-bed design guides: 0.3 mm smallest reproducible detail"),
		InternalRadius: Limit{},
		Draft:          Limit{},
		// Metal powder beds are quoted between 30 and 45 degrees; 45 is the
		// permissive end, so a finding here is one every guide would agree with.
		Overhang: cited(45, "metal powder-bed design guides: 30-45 degrees unsupported, "+
			"45 taken as the permissive end"),
	},
}

// ProcessNames lists the processes in a stable order, for the contract and for
// the message a refused process prints.
func ProcessNames() []Process {
	out := make([]Process, 0, len(Profiles))
	for name := range Profiles {
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ValidProcess reports whether a name has a row in Profiles.
func ValidProcess(name string) bool {
	_, ok := Profiles[Process(name)]
	return ok
}

// ProcessGuide is the sentence the contract teaches, rendered FROM the table.
//
// Written out rather than hand-copied for the reason FinishGuide is: a contract
// naming a process the validator refuses is a model writing the field faithfully
// and having it dropped.
func ProcessGuide() string {
	parts := make([]string, 0, len(Profiles))
	for _, name := range ProcessNames() {
		p := Profiles[name]
		parts = append(parts, string(name)+" ("+p.Label+")")
	}
	return strings.Join(parts, ", ")
}

// PartMeasure is one built part as the kernel measured it for this check. Every
// length is in MILLIMETRES and every angle in DEGREES.
//
// The pointers are the difference between "measured zero" and "not measured", and
// that difference is the whole point: a nil MinWall is a part whose wall nobody
// knows, and reporting it as 0 would turn an unchecked part into the worst finding
// in the model.
type PartMeasure struct {
	ID string `json:"id"`
	// Faces is how many faces the part has — the unit the budget counts in.
	Faces int `json:"faces"`
	// MinWall is the thinnest place a ray cast inward from a face centre found.
	MinWall *float64 `json:"min_wall,omitempty"`
	// MinFeature is the smallest edge length, or a circular edge's DIAMETER.
	MinFeature *float64 `json:"min_feature,omitempty"`
	// InternalRadius is 0 when the part has a sharp concave edge, the smallest
	// concave cylindrical radius when it has been filleted, and nil when the part
	// has no internal corner at all.
	InternalRadius *float64 `json:"internal_radius,omitempty"`
	// MinDraft is the flattest wall against the pull direction, and MaxOverhang
	// the steepest downward-facing surface above the part's own lowest point.
	// Both against the assembly's +Y — see Unchecked and the turn's note.
	MinDraft    *float64 `json:"min_draft,omitempty"`
	MaxOverhang *float64 `json:"max_overhang,omitempty"`
	// Unchecked is why the kernel could not measure this part, and empty when it
	// could.
	Unchecked string `json:"unchecked,omitempty"`
}

// Finding is one rule, on one part, with the number that broke it.
//
// ‼️ It carries the MEASURED value and the LIMIT, both, always. "This wall is too
// thin" is an opinion; "this wall is 0.6 mm and FDM printing needs 0.8 mm" is
// something a reader can disagree with, which is the only kind of finding worth
// printing.
type Finding struct {
	Part    string  `json:"part"`
	Label   string  `json:"label,omitempty"`
	Process Process `json:"process"`
	Rule    string  `json:"rule"`
	// Measured and Limit are in the same unit, named by Unit ("mm" or "degrees").
	Measured float64 `json:"measured"`
	Limit    float64 `json:"limit"`
	Unit     string  `json:"unit"`
	// Source is the limit's citation, and Validated its status. See Limit.
	Source    string `json:"source"`
	Validated bool   `json:"validated"`
	// Over is true for a rule with a CEILING (overhang) and false for a floor.
	Over bool `json:"over,omitempty"`
}

// Describe is the sentence a reader sees.
func (f Finding) Describe() string {
	name := f.Label
	if name == "" {
		name = f.Part
	}
	how := "below"
	if f.Over {
		how = "above"
	}
	note := " (UNVALIDATED rule of thumb: " + f.Source + ")"
	if f.Validated {
		note = " (" + f.Source + ")"
	}
	return fmt.Sprintf("%s: %s is %s, %s the %s %s for %s%s",
		name, f.Rule, amount(f.Measured, f.Unit), how, amount(f.Limit, f.Unit),
		"limit", Profiles[f.Process].Label, note)
}

func amount(v float64, unit string) string {
	if unit == degrees {
		return fmt.Sprintf("%.1f degrees", v)
	}
	return fmt.Sprintf("%.3g mm", v)
}

const (
	millimetres = "mm"
	degrees     = "degrees"
)

// Report is one manufacturability pass over a built model.
type Report struct {
	Findings []Finding `json:"findings,omitempty"`
	// Checked is how many parts were judged against a process, Measured how many
	// the kernel measured, and Parts how many it built. Checked below Measured is
	// parts with no process named; Measured below Parts is the budget.
	Checked  int `json:"checked"`
	Measured int `json:"measured"`
	Parts    int `json:"parts"`
	// Truncated says the kernel's face budget stopped the measurement before the
	// end, so an empty finding list is not evidence of a makeable model.
	Truncated bool `json:"truncated,omitempty"`
	// WithoutProcess names the parts that say nothing about how they are made.
	// They are checked against nothing, on purpose — see the note at the top.
	WithoutProcess []string `json:"without_process,omitempty"`
	// Unmeasured names the parts the kernel could not measure, with why.
	Unmeasured []string `json:"unmeasured,omitempty"`
	// MeshOnly names the mesh-only parts (lattice.go), which have no solid to
	// measure and are never checked.
	MeshOnly []string `json:"mesh_only,omitempty"`
}

// Clean reports whether this pass found nothing AND covered everything: the only
// state in which silence is honest.
func (r Report) Clean() bool {
	return len(r.Findings) == 0 && !r.Truncated && len(r.WithoutProcess) == 0 &&
		len(r.Unmeasured) == 0 && r.Checked > 0
}

// Manufacturability judges the kernel's measurements against the table.
//
// doc supplies each part's process and label; measures are what the kernel
// returned, one per part it measured; truncated says its budget bound.
func Manufacturability(doc Document, measures []PartMeasure, truncated bool, parts int) Report {
	process := map[string]Process{}
	label := map[string]string{}
	meshOnly := map[string]bool{}
	report := Report{Truncated: truncated, Measured: len(measures), Parts: parts}
	if report.Parts < len(measures) {
		report.Parts = len(measures)
	}
	for _, p := range doc.Expanded().Parts {
		label[p.ID] = p.Label()
		if p.IsMeshOnly() {
			meshOnly[p.ID] = true
			report.MeshOnly = append(report.MeshOnly, p.Label())
			continue
		}
		if p.Process != "" {
			process[p.ID] = Process(p.Process)
		}
	}
	named := func(id string) string {
		if l := label[id]; l != "" {
			return l
		}
		return id
	}
	for _, m := range measures {
		if meshOnly[m.ID] {
			continue
		}
		if m.Unchecked != "" {
			report.Unmeasured = append(report.Unmeasured, named(m.ID)+": "+m.Unchecked)
			continue
		}
		p, ok := Profiles[process[m.ID]]
		if !ok {
			report.WithoutProcess = append(report.WithoutProcess, named(m.ID))
			continue
		}
		report.Checked++
		report.Findings = append(report.Findings, findingsFor(m, named(m.ID), p)...)
	}
	sort.SliceStable(report.Findings, func(i, j int) bool {
		a, b := report.Findings[i], report.Findings[j]
		return severity(a) > severity(b)
	})
	return report
}

// severity orders the findings worst first, by how far past the limit they are as
// a SHARE of it — 0.2 mm of a 0.8 mm wall is a worse part than 46 degrees against
// a 45 degree limit, and a list cut to three has to name the first one.
func severity(f Finding) float64 {
	if f.Limit == 0 {
		return 0
	}
	if f.Over {
		return (f.Measured - f.Limit) / f.Limit
	}
	return (f.Limit - f.Measured) / f.Limit
}

func findingsFor(m PartMeasure, label string, p ProcessProfile) []Finding {
	var out []Finding
	floor := func(rule string, measured *float64, limit Limit, unit string) {
		if measured == nil || !limit.applies || *measured >= limit.Value {
			return
		}
		out = append(out, Finding{Part: m.ID, Label: label, Process: p.Name, Rule: rule,
			Measured: *measured, Limit: limit.Value, Unit: unit,
			Source: limit.Source, Validated: limit.Validated})
	}
	floor(RuleMinWall, m.MinWall, p.MinWall, millimetres)
	floor(RuleMinFeature, m.MinFeature, p.MinFeature, millimetres)
	floor(RuleInternalRadius, m.InternalRadius, p.InternalRadius, millimetres)
	floor(RuleDraft, m.MinDraft, p.Draft, degrees)
	if m.MaxOverhang != nil && p.Overhang.applies && *m.MaxOverhang > p.Overhang.Value {
		out = append(out, Finding{Part: m.ID, Label: label, Process: p.Name, Rule: RuleOverhang,
			Measured: *m.MaxOverhang, Limit: p.Overhang.Value, Unit: degrees,
			Source: p.Overhang.Source, Validated: p.Overhang.Validated, Over: true})
	}
	return out
}

// ManufacturabilityNote is what reaches the turn, in the shape the interference
// coverage note settled on (Phase 5, stage V2): nothing at all when the check was
// complete and clean, and otherwise exactly what was checked, what was not, and
// what was found — with every number beside its limit.
//
// ‼️ A check that covered NOTHING still says one sentence. A complete clean check
// is silent because silence means "checked, clear"; a check that never ran has no
// clear result to be silent about, and this product's fifth promise is that a
// missing check never reads as a passed one.
func ManufacturabilityNote(r Report) string {
	if r.Clean() || (r.Parts == 0 && r.Measured == 0) {
		return ""
	}
	var notes []string
	if r.Truncated {
		notes = append(notes, fmt.Sprintf("FORGE measured %d of %d part(s) for manufacturability and "+
			"stopped there, so the rest are not known to be makeable.", r.Measured, r.Parts))
	}
	if len(r.Findings) > 0 {
		const most = 3
		shown := r.Findings
		more := ""
		if len(shown) > most {
			shown, more = shown[:most], fmt.Sprintf("; and %d more", len(r.Findings)-most)
		}
		lines := make([]string, 0, len(shown))
		for _, f := range shown {
			lines = append(lines, f.Describe())
		}
		notes = append(notes, fmt.Sprintf("%d manufacturability finding(s) on the solid the kernel built: %s%s. "+
			"Nothing was changed — how a part is made is a decision FORGE does not take.",
			len(r.Findings), strings.Join(lines, "; "), more))
	}
	if n := len(r.WithoutProcess); n > 0 {
		if r.Checked == 0 && !r.Truncated {
			notes = append(notes, fmt.Sprintf("No part says how it is made, so none of the %d was checked "+
				"for manufacturability. Name a process on a part — %s — and FORGE checks its walls, "+
				"features, internal corners, draft and overhang against that process.",
				n, ProcessGuide()))
		} else {
			notes = append(notes, fmt.Sprintf("%d part(s) name no process, so they were not checked for "+
				"manufacturability: %s.", n, namedList(r.WithoutProcess)))
		}
	}
	if n := len(r.Unmeasured); n > 0 {
		notes = append(notes, fmt.Sprintf("%d part(s) could not be measured, so they were not checked for "+
			"manufacturability: %s.", n, namedList(r.Unmeasured)))
	}
	if note := MeshOnlyNote(r.MeshOnly, "the manufacturability check"); note != "" {
		notes = append(notes, note)
	}
	return strings.Join(notes, " ")
}

// namedList names at most three and counts the rest, like every other coverage
// note here.
func namedList(all []string) string {
	const most = 3
	if len(all) <= most {
		return strings.Join(all, "; ")
	}
	return strings.Join(all[:most], "; ") + fmt.Sprintf("; and %d more", len(all)-most)
}
