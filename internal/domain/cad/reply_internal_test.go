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

// The buried count a repair is judged by is never below the buried clashes listed,
// and says whether it is all of them (repair judged by the kernel total). A reply
// that does not carry it is the whole count only when its list is the whole list.
func TestBuildOf_ABuriedCountIsNeverBelowTheListAndSaysWhetherItIsAll(t *testing.T) {
	two := []geometry.Interference{{A: "a", Fraction: 0.9}, {A: "b", Fraction: 0.5}, {A: "c", Fraction: 0.49}}
	count := func(n int) *int { return &n }
	for _, tc := range []struct {
		name    string
		res     reply
		buried  int
		counted bool
	}{
		{"no counts, the whole list", reply{Interferences: two}, 2, true},
		{"no buried count, the whole list", reply{Interferences: two, InterferencesFound: count(3)}, 2, true},
		{"no buried count, a cut list", reply{Interferences: two, InterferencesFound: count(1760000), InterferencesSummarized: true}, 2, false},
		{"a buried count past the list", reply{Interferences: two, InterferencesFound: count(1760000), InterferencesBuried: count(768000)}, 768000, true},
		{"a buried count below the list", reply{Interferences: two, InterferencesBuried: count(1)}, 2, true},
		{"nothing found", reply{InterferencesFound: count(0), InterferencesBuried: count(0)}, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := tc.res
			b, err := buildOf(&res, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			if b.InterferencesBuried != tc.buried || b.InterferencesBuriedCounted != tc.counted {
				t.Errorf("buried %d, counted=%v; want %d, %v", b.InterferencesBuried, b.InterferencesBuriedCounted,
					tc.buried, tc.counted)
			}
		})
	}
}
