package agent

import (
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Whether the solid the kernel built could be MADE, and what a section of it is
// worth — both on their way to the turn.
//
// # Where this sits, and why it never calls a model
//
// addresses issue 6. The kernel measures five numbers per part while it holds the
// solids the picture was drawn from (cad/sidecar.py, _manufacturability); Go
// judges them against one table of process limits (geometry/manufacturability.go)
// and says what it found. Like the interference check, and unlike every other
// check in a turn, this costs no model call at all — which is why it can run on
// every kernel render rather than when somebody remembers to ask.
//
// # Why nothing is repaired, ever
//
// A thin wall may be a deliberate membrane; a square inside corner may be a part
// that is cast rather than cut; a 60 degree overhang may be printed on supports
// somebody has already planned. FORGE cannot tell any of those from a mistake, and
// this repository has already had to delete one checker that fired on correct
// models. So the findings reach the reader beside the coverage notes and the
// document is left exactly as the model wrote it — which is also what makes them
// safe to report in full.
//
// # Why they arrive with the interference notes
//
// repairIfPartsOverlap is the one place every turn path reads the kernel's FINAL
// sheet — after a repair, if one was kept, so the findings describe the document
// the reader gets rather than the one it replaced. builtFeaturesNote is there for
// the same reason.

// noteManufacturability puts the check's findings, its coverage and any named
// section's properties into the turn.
//
// ‼️ Silent when the picture is not the kernel's, exactly like the interference
// notes. A deployment with no kernel measures nothing, and a model that was never
// checked must not read as one that passed — that is the fifth promise, and it is
// the whole reason this returns without a word instead of with a reassuring one.
func noteManufacturability(reply *Reply, sheet *builtSheet) {
	if reply == nil || reply.Prototype == nil || sheet == nil || !sheet.FromKernel {
		return
	}
	report := geometry.Manufacturability(*reply.Prototype, sheet.Manufacturability,
		sheet.ManufacturabilityTruncated, sheet.ManufacturabilityParts)
	if note := geometry.ManufacturabilityNote(report); note != "" {
		reply.noteRepair(note)
	}
	if note := geometry.SectionNote(sheet.Sections); note != "" {
		reply.noteRepair(note)
	}
}
