package cad

import (
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// A list shorter than the count the kernel reported is a summary whatever the flag
// says, and a count below the list is never believed: a reader that sees N findings
// and a count of N must be able to trust nothing was left out (next scale walls).
func TestBuildOf_AListShorterThanItsCountIsASummary(t *testing.T) {
	three := []geometry.Interference{{A: "a"}, {A: "b"}, {A: "c"}}
	count := func(n int) *int { return &n }
	for _, tc := range []struct {
		name       string
		res        reply
		found      int
		summarized bool
	}{
		{"no count", reply{Interferences: three}, 3, false},
		{"the count is the list", reply{Interferences: three, InterferencesFound: count(3)}, 3, false},
		{"more found than listed", reply{Interferences: three, InterferencesFound: count(11175), InterferencesSummarized: true}, 11175, true},
		{"more found, flag missing", reply{Interferences: three, InterferencesFound: count(10)}, 10, true},
		{"a count below the list", reply{Interferences: three, InterferencesFound: count(1)}, 3, false},
		{"nothing found", reply{InterferencesFound: count(0)}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.res
			b, err := buildOf(&res, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			if b.InterferencesFound != tc.found || b.InterferencesSummarized != tc.summarized {
				t.Errorf("found %d, summarized=%v; want %d, %v", b.InterferencesFound, b.InterferencesSummarized,
					tc.found, tc.summarized)
			}
		})
	}
}
