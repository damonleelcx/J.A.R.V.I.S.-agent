package tools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Unavailable is a declared connector with no backend in this deployment.
//
// # Why these exist at all, rather than simply being absent
//
// FORGE's domain packs describe capabilities this codebase does not implement:
// a parametric CAD kernel, a SPICE simulator, an FEA solver, a DICOM reader.
// There are two ways to handle that, and only one of them is safe.
//
// Leaving them out entirely means the model is asked to do mechanical or
// electrical work with no tool that fits, and the most likely thing it does is
// produce a plausible answer from its own weights — a tolerance, a current, a
// natural frequency — presented exactly like a computed one. That is the
// single most dangerous output this system can produce (PRD RSN-06), and the
// absence of a tool is what invites it.
//
// Declaring them and failing loudly means the model reaches for the right tool,
// is told CONNECTOR_UNAVAILABLE with a specific reason, and has to say so. The
// refusal becomes visible in the timeline and in the answer, where a human can
// see it.
//
// So: never simulated, never approximated, never silently missing.
type Unavailable struct {
	contract Contract
}

// NewUnavailable declares a connector that cannot run here.
func NewUnavailable(name, description, reason string, caps []Capability, tier engine.RiskTier, schema json.RawMessage) *Unavailable {
	return &Unavailable{contract: Contract{
		Name:              name,
		Description:       description,
		InputSchema:       schema,
		Capabilities:      caps,
		RiskTier:          tier,
		Reversibility:     ReversibleNone,
		Timeout:           5 * time.Second,
		Idempotent:        true,
		Available:         false,
		UnavailableReason: reason,
	}}
}

// Contract implements Tool.
func (u *Unavailable) Contract() Contract { return u.contract }

// Run always fails, and says exactly why.
//
// The error names what is missing and what a human could do instead, because
// "unavailable" on its own leaves the user with a dead end and the model with an
// incentive to fill the gap itself.
func (u *Unavailable) Run(ctx context.Context, inv Invocation) (*Result, error) {
	return nil, errs.New("tools.Unavailable.Run", errs.CodeConnectorUnavailable).
		WithDetail("%s is declared but has no backend in this deployment. %s "+
			"Do not estimate or infer what it would have returned — say that the check could not be run.",
			u.contract.Name, u.contract.UnavailableReason).
		WithField("tool", u.contract.Name)
}

// StandardUnavailableConnectors are the domain-pack capabilities this build
// declares but cannot perform. Registering them is what turns "FORGE quietly
// guessed a stress figure" into "FORGE said it could not compute one".
func StandardUnavailableConnectors() []Tool {
	obj := func(props string) json.RawMessage {
		return json.RawMessage(`{"type":"object","properties":{` + props + `},"additionalProperties":false}`)
	}
	return []Tool{
		NewUnavailable(
			"cad_parametric_edit",
			"Modify a parametric CAD model: dimensions, features, constraints, assembly structure.",
			"No CAD kernel is linked into this build, so no geometry can be created, modified, or measured. "+
				"A qualified engineer must make this change in the native CAD tool.",
			[]Capability{CapRead, CapWrite}, engine.RiskR2,
			obj(`"model":{"type":"string"},"change":{"type":"string"}`),
		),
		NewUnavailable(
			"cad_import",
			"Read a CAD file — STEP, IGES, native part or assembly — and turn it into geometry "+
				"this workbench can show and revise.",
			"No CAD kernel is linked into this build, so a CAD file cannot be read. This is the one "+
				"input PRD VIS-01 names that nothing here can accept: text, voice, a sketch and a "+
				"photograph all reach FORGE, and a STEP file does not. Export a view as an image and "+
				"attach that, or state the dimensions — both produce a model FORGE drew rather than "+
				"one it read, which is the honest difference and is labelled as such.",
			[]Capability{CapRead}, engine.RiskR1,
			obj(`"path":{"type":"string"},"units":{"type":"string"}`),
		),
		NewUnavailable(
			"fea_solve",
			"Run a finite-element analysis and return stresses, displacements and factors of safety.",
			"No FEA solver is available here. Structural adequacy cannot be established by this deployment, "+
				"and a value produced without a solver is not an analysis.",
			[]Capability{CapSimulate}, engine.RiskR3,
			obj(`"model":{"type":"string"},"load_case":{"type":"string"}`),
		),
		NewUnavailable(
			"spice_simulate",
			"Run a SPICE simulation on a schematic and return node voltages, currents and timing.",
			"No SPICE engine is available here. Circuit behaviour must be verified on the bench or in the "+
				"native EDA tool.",
			[]Capability{CapSimulate}, engine.RiskR2,
			obj(`"netlist":{"type":"string"},"analysis":{"type":"string"}`),
		),
		NewUnavailable(
			"dicom_read",
			"Read patient imaging in DICOM format.",
			"Patient imaging is not accessible from this deployment and this build has no approved intended "+
				"use for patient-specific data. This is a deliberate boundary, not a missing dependency.",
			[]Capability{CapRead}, engine.RiskR4,
			obj(`"study":{"type":"string"}`),
		),
	}
}
