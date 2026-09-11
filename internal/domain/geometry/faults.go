package geometry

// The reasons a part or a feature will not be in the model.
//
// # Why this exists separately from the notes
//
// Solids() already reports everything it did — defaults it chose, words it read
// charitably, parts it could not build — as one flat list of sentences for a
// person to read. That list is right for a person and useless for a decision:
// "no height was given, so 1 mm was used" and "an outline needs at least 3
// points, so it is not in this file" are the same kind of string and completely
// different kinds of event. The first is a note about a model that exists; the
// second means a part is MISSING.
//
// Severity already distinguishes them at the point they are produced, and was
// being thrown away at the boundary. This keeps it.
//
// # What it is for
//
// A turn that produces geometry with faults has produced a model with a hole in
// it, and the person is about to be told their change was made. Observed twice
// on real turns, 2026-09-09: a spoiler whose wing was a two-point outline, and a
// wheel arch whose loop repeated a point — in both cases FORGE said it had done
// the thing, and the part was not there. This is what lets the turn notice.
//
// Warnings are deliberately not included. A defaulted dimension and a word read
// charitably are both reported to the reader and neither means anything is
// absent, so repairing them would be asking the model to restate a document
// that is already correct.
func (d *Document) Faults() []Problem {
	if d == nil {
		return nil
	}
	var out []Problem
	// Patterns are written out first, or a feature naming a repeated part reads
	// as naming something that does not exist and this reports a fault in a
	// document that builds perfectly well.
	//
	// Gears before that, in the same order Solids does it: a gear whose numbers
	// do not describe one is a part that is NOT in the model, and this is the
	// check that hands it to the repair loop.
	expanded, gearProblems := expandGears(*d)
	expanded, repeatProblems := expandRepeats(expanded)
	d = &expanded

	// Both producers, because a part can be missing for either reason: an
	// outline that cannot be read, or a feature naming something that is not
	// there. A caller that consulted one of them would be blind to half of it.
	_, _, profileProblems := d.resolvedProfiles()
	_, featureProblems := d.Operations()
	profileProblems = append(profileProblems, repeatProblems...)
	profileProblems = append(profileProblems, gearProblems...)
	for _, p := range append(profileProblems, featureProblems...) {
		if p.Severity == Error {
			out = append(out, p)
		}
	}
	return out
}
