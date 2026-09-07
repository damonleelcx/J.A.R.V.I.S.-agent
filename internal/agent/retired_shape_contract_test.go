package agent

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The contract must not offer a word the vocabulary has retired.
//
// # Why this is a fence and not a code review note
//
// The shape list in converseFraming is prose. Nothing compiles it, nothing
// imports it, and a word removed from geometry stays in it forever unless
// something reads both. `tube` was retired in wave 27 precisely because the
// document could not keep the promise the word made — and a model still being
// offered it would go on producing parts that render as solid bar while their
// note says "20×12 with a 2 mm wall".
//
// The list of retired words comes FROM the geometry package rather than being
// repeated here, so a word retired tomorrow is covered the day it is retired.
func TestTheContractOffersNoRetiredShapeWord(t *testing.T) {
	// The shape enum only — not the whole prompt. The prose deliberately still
	// uses the ENGLISH word "tube" in several places ("the bore of a tube that
	// BENDS"), because a tube is still a thing a person asks for; what it must
	// not do is offer it as a value of "shape".
	const marker = `"shape": `
	i := strings.Index(converseFraming, marker)
	if i < 0 {
		t.Fatal(`converseFraming no longer declares a "shape" field, so this fence is ` +
			`reading nothing. Find where the shape words are offered and point it there.`)
	}
	enum := converseFraming[i:]
	if end := strings.Index(enum, "\n        \"shape_note\""); end > 0 {
		enum = enum[:end]
	}
	for _, word := range geometry.RetiredShapeWords() {
		if strings.Contains(enum, `"`+word+`"`) {
			t.Errorf("the contract still offers the retired shape %q:\n%s\n\n"+
				"A model given the word will use it, and the document cannot keep what the "+
				"word promises — which is why it was retired.", word, enum)
		}
	}
	// And the retirement has to be SAID, not merely omitted. A model that
	// reaches for a word and finds it missing invents something; one that is
	// told there is no tube shape, and what to write instead, does that.
	if !strings.Contains(converseFraming, `There is NO "tube" shape`) {
		t.Error(`the contract does not tell the model what to write instead of a tube. ` +
			`Silently removing a word leaves the model to guess, and the guess it made ` +
			`before this vocabulary existed was three extrusions butted end to end.`)
	}
}
