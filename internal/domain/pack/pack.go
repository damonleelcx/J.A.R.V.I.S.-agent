// Package pack owns the domain packs a project can be worked in, and how far
// this build may take work inside each of them (PRD SEC-07).
package pack

import (
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
)

// Domain packs, the industries they serve, and the ceiling on work here.
//
// # What a pack is
//
// The PRD is explicit that "a pack is NOT a prompt": it bundles schemas,
// terminology, standards access, tool adapters, artifact validators, 3D
// conventions, evaluation suites, safety policies, data-handling rules and
// qualified-review requirements. It is the rule set a project is worked under.
//
// # What it was
//
// A free-text column. `forge_projects.pack` was required to be non-empty and
// then never read by anything, and its two producers hard-code "software" and
// "general". So a typo, a pack this build has never heard of, and a regulated
// domain were all equally acceptable and equally inert — the column recorded a
// claim about which rules applied and no rule ever consulted it.
//
// # Why availability became a CEILING rather than a boolean
//
// The previous shape was `Available bool`, and six of eight packs were false.
// The reasoning was sound and the granularity was wrong: `mechanical` was
// refused because this build cannot gate DRAWING RELEASE, which is PRD tier R3 —
// and that refusal also blocked concept CAD, a render and a revision, which are
// R1 and are the work FORGE exists to do. A mechanical engineer could not open a
// project to sketch a bracket because the build could not certify one.
//
// So a pack now carries the highest tier work may reach inside it HERE. The
// boundary it was protecting is unchanged — nothing in mechanical reaches R2 —
// but it is expressed at the granularity risk actually has. PRD §8.1 already
// tiers every action, engine.Classify already computes the tier a call sits at,
// and tools.Grant already refuses above a ceiling with a reason the model can
// act on. This is that machinery applied one level up, not a new mechanism.
//
// Requires is unchanged in meaning and now reads correctly: it is what a
// deployment would need before work ABOVE the ceiling could happen, and it is
// shown at the moment somebody asks for that work rather than at the door.
//
// # Why some packs still have NO ceiling at all
//
// MaxTier is empty for `medical` and `robotics`, which means no work of any
// tier: a project cannot be created in them. That is not the same judgement as
// the ceiling above, and it is deliberately left where it was.
//
// The PRD's own carve-out says the medical pack is "educational and
// device-concept scope only. Patient-specific use requires a separately
// validated deployment and is not enabled by this codebase". Robotics grants no
// actuation capability at all, so there is nothing here to bound. Neither is an
// industry this product's selector offers, and widening them is not something a
// change about industry coverage should do quietly.
//
// # Why this is not a configuration flag
//
// The obvious shape is FORGE_ENABLED_PACKS, and it is the wrong one. An
// environment variable that switches on patient-specific clinical use would
// make the boundary of a regulated deployment something an operator crosses by
// editing a file — no validation evidence, no qualified authority, no record of
// who decided. A flag would quietly make the PRD's carve-out false.
//
// Reaching regulated use therefore requires changing this table, in a commit,
// with whatever validation evidence that deployment has to stand on. That is a
// higher bar than a config knob on purpose, and it is the only bar this
// repository is in a position to enforce.

// Pack is the rule set a project is worked under.
type Pack string

// The packs this build knows. The set is closed: a project may not declare
// something outside it, because a pack nothing recognises selects no rules while
// looking like it selected some.
const (
	Software      Pack = "software"
	Mechanical    Pack = "mechanical"
	Manufacturing Pack = "manufacturing"
	Automotive    Pack = "automotive"
	Aerospace     Pack = "aerospace"
	Civil         Pack = "civil"
	Electrical    Pack = "electrical"
	Construction  Pack = "construction"
	ProductDesign Pack = "product-design"
	Architecture  Pack = "architecture"
	Robotics      Pack = "robotics"
	Medical       Pack = "medical"
	General       Pack = "general"
)

// Definition is one pack and what this build may do inside it.
type Definition struct {
	Pack Pack
	// Industry is the label the product's industry selector shows for this pack,
	// or empty for a pack the selector does not offer.
	//
	// Held here so the name a person picked and the name the rules are filed
	// under cannot drift into two vocabularies for one thing. Lookup accepts
	// either, so "Civil engineering" from the dropdown and "civil" from a script
	// reach the same rules.
	Industry string
	// Summary is the pack's safety boundary, in one line.
	Summary string
	// MaxTier is the highest PRD §8.1 risk tier work may reach in this pack HERE.
	//
	// Empty means no work at all: a project cannot be created in this pack. See
	// the package comment for why that is a different judgement from a low
	// ceiling rather than the bottom of the same scale.
	MaxTier engine.RiskTier
	// Requires is what a deployment would need before work ABOVE MaxTier could
	// happen: the validation, the qualified authority, the standard. Shown in the
	// refusal, because a limit with no path is a dead end.
	Requires string
	// Conventions is what FORGE has to get right to be useful in this domain:
	// its units, the vocabulary practitioners use, and what a first answer is
	// expected to establish.
	//
	// # Why this is in the pack and not in the prompt
	//
	// The PRD says a pack bundles "terminology, ... 3D conventions" and is "NOT a
	// prompt". This is the terminology half, and it lives here because the
	// alternative is a growing chain of conditions inside converseFraming — one
	// domain's vocabulary reachable only by reading the prompt that mentions it.
	// A pack is the rule set; this is the part of the rule set the model needs
	// stated.
	//
	// Kept SHORT deliberately. It is prepended to every turn in the project, so
	// its cost is paid on every request, and a page of domain lore would crowd
	// out the conversation it is supposed to inform. Units and the two or three
	// things a practitioner would notice were missing — no more.
	//
	// Empty for packs the industry selector does not offer: `general` has no
	// conventions BY DEFINITION (it is the unknown-domain pack), and a project
	// cannot be created in `robotics` or `medical` at all.
	Conventions string
	// Schema is the node kinds a project in this domain is expected to carry
	// (workspace.Kind values). A civil project with no recorded load case is
	// incomplete; a software one is not, and only the pack knows the difference.
	//
	// Plain strings rather than workspace.Kind: workspace imports this package,
	// so importing it back would be a cycle. TestPackSchemaKindsAreRealKinds in
	// internal/domain/workspace holds the two vocabularies together — it can see
	// both, which is exactly why it lives there and not here.
	Schema []string
	// GeometryUnit is the length unit geometry in this domain defaults to, as a
	// geometry.Unit value ("mm", "cm", "m", "in"), or empty where the domain does
	// not imply one.
	GeometryUnit string
	// GeometryAxes is the frame a reader has to assume to make sense of a
	// coordinate here. Stated because it DIFFERS between domains — a vehicle
	// frame is X-forward and a building is Z-up against a site datum — and a
	// position with the wrong frame assumed is wrong without looking wrong.
	GeometryAxes string
	// Adapters are the tool adapters this domain needs, by the names in
	// internal/tools. Every one of them is UNAVAILABLE in this build.
	//
	// # Why declare tools that do not work
	//
	// A refusal that says "no FEA solver is available" is true and generic. One
	// that can say "civil work needs FEA and this deployment has none" tells
	// somebody whether they are missing a dependency or standing outside what
	// this product does. The spike that looked at integrating a real kernel
	// recommended against it (docs/spikes/2026-09-02-zoo-text-to-cad), so what a
	// domain REQUIRES and what this build HAS are separate facts, and the honest
	// thing is to state both rather than to let the gap go unnamed.
	Adapters []string
	// DataRules is how material in this domain must be handled: what is
	// sensitive, and what must not travel into a transcript or a shared room.
	//
	// Short and readable rather than a policy engine. It is shown to the person
	// (forgectl project industry) and to the model (the conversation framing),
	// which is where a handling rule can actually change what happens — this
	// build has no mechanism that could enforce one, and pretending otherwise
	// would be the fabricated-capability failure in a new place.
	DataRules string
}

// Available reports whether a project may be created in this pack at all.
func (d Definition) Available() bool { return d.MaxTier.Valid() }

// Permits reports whether work at tier t may happen in this pack here.
//
// R5 is prohibited everywhere regardless of ceiling (PRD §8.1: refused, not
// gated), so a table row can never authorise it by accident.
func (d Definition) Permits(t engine.RiskTier) bool {
	if !d.Available() || !t.Valid() || t.Prohibited() {
		return false
	}
	return d.MaxTier.AtLeast(t)
}

// definitions is the table. A table rather than a switch because it decides what
// gets refused, so it has to be readable by somebody who will not read the
// function that applies it.
//
// Ordered as the product's industry selector orders them, then the packs the
// selector does not offer. The order is the one users see; keeping it here means
// a reader comparing the two is comparing like with like.
var definitions = []Definition{
	{
		Pack:     Mechanical,
		Industry: "Mechanical engineering",
		Summary:  "Render is not proof; drawing release, tooling, fabrication and certification need qualified review.",
		MaxTier:  engine.RiskR1,
		Requires: "a qualified-review workflow for drawing release, tooling and certification, " +
			"which this build does not implement",
		Conventions: "Units are mm unless the person uses inches; state which. Name materials with\n" +
			"their temper or grade (aluminium 6061-T6, not \"aluminium\"). A dimension that\n" +
			"matters carries a tolerance or says it has none yet. Fasteners are metric by\n" +
			"designation (M3, M4) with a stated clearance or thread fit. Say what a proposed\n" +
			"part is DATUMED from — a hole pattern with no datum cannot be made.",
		Schema:       []string{"requirement", "constraint", "component", "interface", "hazard"},
		GeometryUnit: "mm",
		GeometryAxes: "Y up; the origin is the assembly's centre and parts are centred on their " +
			"own position.",
		Adapters: []string{"cad_parametric_edit", "cad_import", "fea_solve"},
		DataRules: "Models and drawings are commercial-in-confidence. Keep supplier part " +
			"numbers, pricing and unreleased tolerances out of shared transcripts.",
	},
	{
		Pack:     Manufacturing,
		Industry: "Manufacturing",
		Summary:  "Process concepts and tooling studies are drafts; a released process changes what gets built.",
		MaxTier:  engine.RiskR1,
		Requires: "qualified manufacturing review and process validation before a routing, tooling " +
			"or production process may be released, which this build does not implement",
		Conventions: "Units are mm. Talk in process terms: operation sequence, cycle time, fixturing,\n" +
			"datum scheme, tolerance stack. Name the process before the feature (3-axis mill,\n" +
			"injection moulding, sheet metal) — a radius that is free on one is impossible on\n" +
			"another. Draft angles, minimum wall and bend radius are stated, not assumed.\n" +
			"Say what would have to change if the volume were 100x.",
		Schema:       []string{"requirement", "constraint", "component", "test"},
		GeometryUnit: "mm",
		GeometryAxes: "Y up; the origin is the assembly's centre.",
		Adapters:     []string{"cad_import"},
		DataRules: "Process parameters, cycle times and tooling costs are commercially " +
			"sensitive. Supplier terms do not belong in a transcript.",
	},
	{
		Pack:     Automotive,
		Industry: "Automotive",
		Summary:  "Concept and packaging work is reversible; anything touching a vehicle safety function is not.",
		MaxTier:  engine.RiskR1,
		Requires: "a functional-safety process with a qualified safety authority owning hazard " +
			"analysis and release, which this build neither implements nor can represent",
		Conventions: "Units are mm; vehicle axes are X forward, Y right, Z up, and say so when it\n" +
			"matters. Talk in packaging terms: envelope, clearance to adjacent systems,\n" +
			"service access, harness routing. Name the load case for any structural claim.\n" +
			"Anything touching braking, steering, restraint or battery is a SAFETY function —\n" +
			"say so plainly and do not treat it as an ordinary part.",
		Schema:       []string{"requirement", "constraint", "component", "interface", "hazard", "test"},
		GeometryUnit: "mm",
		GeometryAxes: "Vehicle frame: X forward, Y right, Z up. Say so whenever a coordinate is " +
			"given.",
		Adapters: []string{"cad_import", "fea_solve"},
		DataRules: "Programme names and launch dates are confidential before reveal. VINs " +
			"and telematics identify vehicles and their drivers; treat them as " +
			"personal data.",
	},
	{
		Pack:     Aerospace,
		Industry: "Aerospace",
		Summary:  "No unsupervised hazardous procedure, flight command, launch decision or release authority.",
		MaxTier:  engine.RiskR1,
		Requires: "release authority and hazardous-procedure control under the operating certificate " +
			"of the organisation flying the vehicle",
		Conventions: "Units are mm or inches; state which, every time. Mass is a first-class number:\n" +
			"give it or say you have not estimated it. Name the load case and the factor of\n" +
			"safety for any structural claim, or say neither is established. Distinguish\n" +
			"flight hardware from ground support equipment in the first answer. Never imply\n" +
			"airworthiness, qualification or release — none of that happens here.",
		Schema:       []string{"requirement", "constraint", "component", "interface", "hazard", "evidence", "test"},
		GeometryUnit: "mm",
		GeometryAxes: "Body frame: X forward, Y right, Z up. State the frame and the datum " +
			"every time.",
		Adapters: []string{"cad_import", "fea_solve"},
		DataRules: "Design data may be export-controlled. Do not move it between " +
			"jurisdictions, into shared rooms, or into a transcript without checking " +
			"what regime it falls under.",
	},
	{
		Pack:     Civil,
		Industry: "Civil engineering",
		Summary:  "A licensed engineer owns calculations, issued drawings, compliance and field direction.",
		MaxTier:  engine.RiskR1,
		Requires: "a named licensed engineer holding sign-off authority over calculations and issued " +
			"drawings — an authority this build cannot represent, let alone verify",
		Conventions: "Units are mm for details and m for spans and levels; state which. Name the load\n" +
			"case (dead, live, wind, seismic) behind any structural number, and the code\n" +
			"family if one is in play. Distinguish a preliminary sizing from a design: a\n" +
			"member size with no calculation behind it is a starting point, and saying so is\n" +
			"the difference between useful and dangerous. Levels are given relative to a\n" +
			"stated datum.",
		Schema:       []string{"requirement", "constraint", "component", "hazard", "evidence"},
		GeometryUnit: "mm",
		GeometryAxes: "Z up; levels are given against a stated datum, never against 'the " +
			"ground'.",
		Adapters: []string{"fea_solve"},
		DataRules: "Site and ground investigation data is client-confidential, and survey " +
			"files carry precise locations. Treat them as identifying.",
	},
	{
		Pack:     Electrical,
		Industry: "Electrical engineering",
		Summary:  "High-voltage, RF, battery, bench actuation, procurement and compliance are gated.",
		MaxTier:  engine.RiskR1,
		Requires: "gating for the hazardous classes above, which this build does not implement",
		Conventions: "Units are mm for geometry, and give voltage, current and power explicitly.\n" +
			"Distinguish signal from power early. Name the creepage and clearance any spacing\n" +
			"claim rests on, and the voltage class it assumes. Give wire sizes with a\n" +
			"standard (AWG or mm2) and connectors by series. Anything above SELV, plus RF and\n" +
			"battery, is a hazard class — say so and do not design past it.",
		Schema:       []string{"requirement", "constraint", "component", "interface", "hazard", "test"},
		GeometryUnit: "mm",
		GeometryAxes: "Y up; board coordinates are stated from a named origin corner.",
		Adapters:     []string{"spice_simulate", "cad_import"},
		DataRules: "Schematics and firmware are commercial-in-confidence. Credentials, keys " +
			"and calibration constants must never enter a transcript.",
	},
	{
		Pack:     Construction,
		Industry: "Construction",
		Summary:  "Sequencing and concept work is reversible; issued documents and field direction are not.",
		MaxTier:  engine.RiskR1,
		Requires: "a licensed engineer or architect of record holding sign-off over issued " +
			"construction documents and field direction",
		Conventions: "Units are mm for details and m for setting out; state which. Talk in sequence:\n" +
			"what is built before what, what is temporary, what has to be accessible later.\n" +
			"Distinguish permanent works from temporary works — the second kills people and\n" +
			"is usually the part left implicit. Give levels against a stated datum. Say what\n" +
			"trade owns each interface.",
		Schema:       []string{"requirement", "constraint", "hazard", "evidence", "test"},
		GeometryUnit: "mm",
		GeometryAxes: "Z up; setting-out and levels are given against a stated site datum.",
		Adapters:     nil,
		DataRules: "Site records identify individuals. Method statements, incident notes and " +
			"access arrangements are confidential and some are safety-sensitive.",
	},
	{
		Pack:     ProductDesign,
		Industry: "Product design",
		Summary:  "Concepts, form studies and revisions are drafts; releasing one to tooling commits money and time.",
		MaxTier:  engine.RiskR1,
		Requires: "a design-release process with a named owner accountable for what goes to tooling, " +
			"which this build does not implement",
		Conventions: "Units are mm. Talk about the form, the interfaces and the user's hand: grip,\n" +
			"reach, what is touched and how often. Give materials with a finish, and say\n" +
			"which surfaces are cosmetic (an A-surface) and which are not. Wall thickness and\n" +
			"draft are stated for anything moulded. Say what the concept does NOT yet resolve\n" +
			"— a form study is not a mechanism.",
		Schema:       []string{"requirement", "constraint", "component", "criterion"},
		GeometryUnit: "mm",
		GeometryAxes: "Y up; the origin is the product's centre.",
		Adapters:     []string{"cad_import"},
		DataRules: "Unreleased industrial design is the most leak-sensitive material in this " +
			"domain: a render is recognisable long before launch.",
	},
	{
		Pack:     Architecture,
		Industry: "Architecture",
		Summary:  "Massing and layout studies are drafts; issued drawings and permit submissions carry legal weight.",
		MaxTier:  engine.RiskR1,
		Requires: "an architect of record holding sign-off over issued drawings, plus building-code " +
			"compliance checking this build does not implement",
		Conventions: "Units are mm for details and m for planning; state which. Give areas with the\n" +
			"basis (gross, net, usable). Talk in terms a drawing set uses: grid, level,\n" +
			"core, envelope, circulation. Orientation and daylight are stated when they\n" +
			"affect the answer. Distinguish massing and layout studies from anything that\n" +
			"looks like a coordinated drawing — the first is a sketch and must not read as\n" +
			"the second.",
		Schema:       []string{"requirement", "constraint", "criterion", "evidence"},
		GeometryUnit: "mm",
		GeometryAxes: "Z up; levels against a stated datum, plan coordinates against a named " +
			"grid.",
		Adapters: nil,
		DataRules: "Client and occupant information is confidential, and drawings can reveal " +
			"security-relevant detail about a building's access and services.",
	},
	{
		Pack:     General,
		Industry: "Other",
		Summary:  "Unknown domain or missing standards: autonomy is lower and expert review is triggered.",
		MaxTier:  engine.RiskR2,
		Requires: "the domain to be identified, so that the rules which apply to it can be the ones " +
			"applied rather than the general fallback",
		Schema:   []string{"requirement", "constraint"},
		Adapters: nil,
		DataRules: "The domain is unknown, so nothing can be assumed safe to share. Treat " +
			"material as confidential and ask before it leaves this project.",
	},
	// Below here: packs the industry selector does not offer.
	{
		Pack:    Software,
		Summary: "Sandbox by default; review before merge or deploy; secrets and production are stricter.",
		MaxTier: engine.RiskR2,
		Requires: "release and deployment authority over the system being changed, which this build " +
			"does not hold — it grants no deploy, transact or control capability at all",
		Schema:   []string{"requirement", "constraint", "component", "interface", "test"},
		Adapters: nil,
		DataRules: "Secrets never enter a transcript, and production data is not test data. " +
			"A sandbox that reads live records is production.",
	},
	{
		Pack:    Robotics,
		Summary: "Simulation first; physical motion needs bounded mode, interlocks, clearance and human control.",
		// No ceiling: not workable here at all. See the package comment.
		Requires: "bounded-mode motion control with interlocks. This build grants no actuation " +
			"capability at all, so there is nothing here to bound",
	},
	{
		Pack:    Medical,
		Summary: "Regulated intended use only; a clinician approves patient-specific output; no autonomous diagnosis, treatment or instrument actuation.",
		// No ceiling, and this row is the one somebody will argue about. The PRD's
		// carve-out already settled it: "educational and device-concept scope only.
		// Patient-specific use requires a separately validated deployment and is
		// NOT enabled by this codebase."
		Requires: "a separately validated deployment under the intended use its regulator approved, " +
			"with a clinician holding approval authority over every patient-specific output. " +
			"This codebase is educational and device-concept scope only and does not enable " +
			"patient-specific use",
	},
}

// normalise reduces a name to the form the table is keyed by, so that the label
// a person read in a dropdown, the id a script passes, and the value already in
// the database all reach the same row.
//
// Spaces and underscores fold to the hyphen the ids use: "Product design",
// "product_design" and "product-design" are one pack, and a person who typed
// what they saw is not told their industry does not exist.
func normalise(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.ReplaceAll(s, "_", "-")
	s = strings.ReplaceAll(s, " ", "-")
	return s
}

// Lookup returns the definition of a pack, and whether it is one at all.
//
// Accepts the pack id ("civil") or the industry label the selector shows
// ("Civil engineering"), because refusing the second would mean the product's
// own vocabulary was not one this build recognised.
func Lookup(name string) (Definition, bool) {
	want := normalise(name)
	if want == "" {
		return Definition{}, false
	}
	for _, d := range definitions {
		if normalise(string(d.Pack)) == want {
			return d, true
		}
		if d.Industry != "" && normalise(d.Industry) == want {
			return d, true
		}
	}
	return Definition{}, false
}

// All returns every pack this build knows, workable or not.
//
// Including the ones that are not workable is the point: a list of what a
// deployment may do is only readable next to what it may not.
func All() []Definition {
	out := make([]Definition, len(definitions))
	copy(out, definitions)
	return out
}

// Industries returns the packs the product's industry selector offers, in the
// order it offers them.
//
// This is the producer for that list. Building the selector from anywhere else
// would create a second vocabulary for the same thing, which is the failure this
// package exists to prevent one level down.
func Industries() []Definition {
	var out []Definition
	for _, d := range definitions {
		if d.Industry != "" {
			out = append(out, d)
		}
	}
	return out
}

// Names lists the packs a project may be created in, for error text.
func Names() []string {
	var out []string
	for _, d := range definitions {
		if d.Available() {
			out = append(out, string(d.Pack))
		}
	}
	return out
}
