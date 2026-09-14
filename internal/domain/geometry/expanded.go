package geometry

// Expanded returns the document as every reader should see it: gears written out
// as the extrusions they are, and every repeated part written out as its copies
// ("spoke-1" … "spoke-60"), with features retargeted to those copies.
//
// # Why this exists
//
// Until 2026-09-13 only the kernel, the mesh and Faults expanded a document.
// Every other reader read the AUTHORED parts and then joined them against ids the
// expanded readers produced: Measure left every copy out of the overall size,
// the contact sheet drew copies in a default grey because their ids were not in
// the authored list, assembly states could not name a copy, the resize check
// labelled copies with an empty name, and the browser — which had no expansion
// at all — drew a sixty-spoke wheel with one spoke.
// docs/bugfix/2026-09-13-repeat-copies-were-invisible-to-most-readers.md
//
// It is the same two expansions, in the same order, that Solids, Tessellate and
// Faults each call — deliberately left as their own calls there, because each of
// those readers is held by a drill that proves IT expands, and one shared call
// would turn three independent guarantees into one. Readers outside this package
// have no other way in.
func (d Document) Expanded() Document {
	e, _ := expandGears(d)
	e, _ = expandRepeats(e)
	return e
}
