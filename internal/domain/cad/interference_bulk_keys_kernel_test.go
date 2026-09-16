package cad_test

import (
	"os"
	"testing"
)

// The interference check's narrow phase one ARRAY at a time (interference approach;
// sidecar.py, _bulk_keys, _bulk_interferences and _BULK_NARROW_PHASE).
//
// #121 took the last micro-optimisation out of the per-pair loop and moved the
// check 0.94-0.98x. This keys a whole GROUP of pairs at once instead: a pair's
// relative translation and its containment test are array arithmetic, and the key
// itself is built by the shipped scalar tail once per distinct row rather than once
// per pair. docs/spikes/2026-09-16-interference-approach.

type bulkKeyVariant struct {
	Name         string `json:"name"`
	SameAnswer   bool   `json:"same_answer"`
	LoopBytes    int    `json:"loop_bytes"`
	ArrayBytes   int    `json:"array_bytes"`
	LoopSHA1     string `json:"loop_sha1"`
	ArraySHA1    string `json:"array_sha1"`
	At           int    `json:"at"`
	LoopAround   string `json:"loop_around"`
	ArrayAround  string `json:"array_around"`
	Compared     int    `json:"compared"`
	Unequal      int    `json:"unequal"`
	ReprDiffer   int    `json:"repr_differ"`
	Slid         int    `json:"slid"`
	Carried      int    `json:"carried"`
	Groups       int    `json:"groups"`
	Rows         int    `json:"rows"`
	DistinctKeys int    `json:"distinct_keys"`
	ScalarPairs  int    `json:"scalar_pairs"`
	First        string `json:"first"`
	Truncated    bool   `json:"truncated"`
	Booleans     int    `json:"booleans"`
	// ‼️ Whether the LOOP's first run over these solids differed from its second.
	// A pair's first OCCT boolean in a process is not always its later ones, so
	// every comparison here is made on warmed solids; see the script.
	FirstRunDiffers bool `json:"first_run_differs"`
}

type bulkKeyRuns struct {
	Error    string `json:"error"`
	Fixtures []struct {
		bulkKeyVariant
		Parts    int              `json:"parts"`
		Variants []bulkKeyVariant `json:"variants"`
	} `json:"fixtures"`
}

// ‼️ Every pair's key is the key build123d's Locations gave, by == — which is what
// the check's cache compares keys by, and therefore the only thing the answer
// depends on — and the whole check built on those keys is the same check: the same
// list, the same flag, the same box tests, pairs, booleans, reuses, found and
// buried counts, byte for byte.
//
// Every pair of solids, not only the candidates, on the four randomized prism
// fixtures, turned rails and crossbars, tied pins, 150 blocks whose clashes are
// more than the list's bound, and a turned barrel of rivets; and each of those
// again with a feature-changed solid in it, with two solids sharing one Location
// object, with chained placements, and with the boolean budget squeezed so the
// truncation path is compared too.
//
// ‼️ ReprDiffer is reported, not required to be zero: -0.0 and 0.0 are one dict key
// and two reprs, and the array path hands out one tuple per key. Unequal is what
// must be zero.
//
// # Why it is gated (FORGE_EXHAUSTIVE_KERNEL_TESTS)
//
// It is the slowest test FORGE has: 1,059 s on the ubuntu-24.04-arm runner (CI run
// 35097920395), when the WHOLE rest of this package takes 686 s there. Run on every
// pull request it put the kernel job past its 30-minute timeout with every test
// passing — honest work, not a hang — and charged every PR on the repository an
// extra eighteen minutes for a property the three-fixture fence below proves on
// every run in 28 s.
//
// ‼️ Gated is not unrun. The nightly `kernel-exhaustive` job in
// .github/workflows/ci.yml sets the gate and runs this, so the eight fixtures and
// four placement variants are still checked on arm64 every day. Unset, it SKIPS and
// says how to run it; it never passes without having run.
func TestKernel_TheArrayNarrowPhaseGivesTheLoopsAnswer(t *testing.T) {
	if os.Getenv("FORGE_EXHAUSTIVE_KERNEL_TESTS") == "" {
		t.Skip("the exhaustive array fence takes ~18 min on CI and runs nightly; set " +
			"FORGE_EXHAUSTIVE_KERNEL_TESTS=1 to run it here, or `make test-cad-exhaustive`. " +
			"TestKernel_TheArrayNarrowPhaseGivesTheLoopsAnswerOnThreeFixtures ran instead.")
	}
	arrayNarrowPhase(t, 8)
}

// The same fence over three fixtures instead of eight — tied pins, turned rails and
// crossbars, and a turned barrel of rivets, which between them reach every branch
// the array path has.
//
// ‼️ This is the fence EVERY CI run holds the array narrow phase to, bit for bit,
// since the eight-fixture one above is gated to the nightly job. It is also what
// every array-path drill in scripts/drill-fences.sh reddens.
//
// It first existed so the drills have something to break. The thorough fence above
// takes eleven minutes on a laptop, and a dozen mutations against it is over two
// hours, which is long enough that the drills would not be run — and a drill that is
// not run is a claim, which is the thing scripts/drill-fences.sh exists to distrust.
func TestKernel_TheArrayNarrowPhaseGivesTheLoopsAnswerOnThreeFixtures(t *testing.T) {
	arrayNarrowPhase(t, 3, "--fast")
}

func arrayNarrowPhase(t *testing.T, want int, args ...string) {
	t.Helper()
	var got bulkKeyRuns
	testdataJSON(t, "interference_bulk_keys.py", &got, args...)
	if got.Error != "" {
		t.Fatal(got.Error)
	}
	if len(got.Fixtures) != want {
		t.Fatalf("%d fixture(s), want %d", len(got.Fixtures), want)
	}
	slid, carried, groups, rows, reprDiffer, handedBack, truncations, unplacedKeys := 0, 0, 0, 0, 0, 0, 0, 0
	unrepeatable := 0
	for _, f := range got.Fixtures {
		every := f.Parts * (f.Parts - 1) / 2
		all := append([]bulkKeyVariant{f.bulkKeyVariant}, f.Variants...)
		for _, v := range all {
			label := f.Name + ", " + v.Name
			if v.Name == f.Name {
				label = f.Name
			}
			t.Logf("%s: %d keys compared, %d unequal, %d equal with the other zero's sign, "+
				"%d groups, %d rows, %d distinct keys, %d pairs handed back; answer %d B %s",
				label, v.Compared, v.Unequal, v.ReprDiffer, v.Groups, v.Rows, v.DistinctKeys,
				v.ScalarPairs, v.ArrayBytes, v.ArraySHA1)
			if !v.SameAnswer {
				t.Errorf("%s: the array narrow phase is not the loop's answer (%d B %s against %d B %s), "+
					"first difference at byte %d:\nloop  %s\narray %s",
					label, v.LoopBytes, v.LoopSHA1, v.ArrayBytes, v.ArraySHA1, v.At, v.LoopAround, v.ArrayAround)
			}
			if v.Unequal != 0 {
				t.Errorf("%s: %d of %d key(s) are not the key build123d's Locations gave; first: %s",
					label, v.Unequal, v.Compared, v.First)
			}
			reprDiffer += v.ReprDiffer
			handedBack += v.ScalarPairs
			if v.Truncated {
				truncations++
			}
			if v.FirstRunDiffers {
				unrepeatable++
				t.Logf("‼️ %s: the LOOP's first run over these solids differed from its second; "+
					"a pair's first OCCT boolean in a process is not always its later ones, "+
					"so this comparison is made on warmed solids", label)
			}
		}
		// The squeezed-budget variant is last and must have stopped the search, or
		// the truncation path was compared against nothing.
		if f.Compared != every {
			t.Errorf("%s: %d keys compared, want every pair of %d parts (%d)", f.Name, f.Compared, f.Parts, every)
		}
		if f.Groups == 0 || f.Rows == 0 {
			t.Errorf("%s: the array path took %d group(s) and built %d row(s); it did not run",
				f.Name, f.Groups, f.Rows)
		}
		slid += f.Slid
		carried += f.Carried
		groups += f.Groups
		rows += f.Rows
		for _, v := range f.Variants {
			if v.Name == "a feature-changed solid" && v.DistinctKeys > f.DistinctKeys {
				unplacedKeys++
			}
		}
	}
	// The fixtures have to reach what could go wrong, or the equality above is cheap.
	// ‼️ One fewer truncated search than there are fixtures: the two-bay barrel's
	// candidate pairs are fewer distinct keys than the squeezed budget of 5, so its
	// search has nothing to stop.
	if slid < 1000 || carried < 20 || groups < 100 || rows < 100 || reprDiffer < 1 ||
		handedBack < 1 || truncations < want-1 || unplacedKeys < want {
		t.Errorf("the fixtures reach %d slid keys, %d carried, %d groups, %d rows, %d keys equal with the "+
			"other zero's sign, %d pairs handed back to the loop, %d truncated searches and %d fixtures whose "+
			"feature-changed variant adds keys of its own; the fence needs all of them",
			slid, carried, groups, rows, reprDiffer, handedBack, truncations, unplacedKeys)
	}
	t.Logf("%d of %d comparisons sat on solids whose first boolean pass differed from the second",
		unrepeatable, 5*len(got.Fixtures))
}
