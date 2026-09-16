package cad_test

import (
	"encoding/json"
	"testing"
)

// A clash's key without a build123d Location per step (repair bound and check
// profile; sidecar.py, _pair_keys and _PAIR_KEY_DIRECT).
//
// Profiled on the 1M airframe barrel, the interference check was 121 s of a 174 s
// build, and most of it was keying 2,191,348 candidate pairs through four build123d
// Locations each. The direct path makes the same OCCT calls without the Python
// objects around them. docs/spikes/2026-09-15-check-profile.

type pairKeyRuns struct {
	Error    string `json:"error"`
	Fixtures []struct {
		Name           string `json:"name"`
		Parts          int    `json:"parts"`
		Compared       int    `json:"compared"`
		Differ         int    `json:"differ"`
		First          string `json:"first"`
		Slid           int    `json:"slid"`
		Carried        int    `json:"carried"`
		UnslidCompared int    `json:"unslid_compared"`
		UnslidDiffer   int    `json:"unslid_differ"`
		PlainCompared  int    `json:"plain_compared"`
		PlainDiffer    int    `json:"plain_differ"`
		Memoized       int    `json:"memoized"`
		// Added 2026-09-15 (last hot spots): the same keys with containment taken
		// per pair instead of once per group of (two definitions, two rotations),
		// and how many such groups the fixture reached.
		PlanlessCompared int `json:"planless_compared"`
		PlanlessDiffer   int `json:"planless_differ"`
		Plans            int `json:"plans"`
		Variants         []struct {
			Name     string `json:"name"`
			Compared int    `json:"compared"`
			Differ   int    `json:"differ"`
			Memoized int    `json:"memoized"`
			First    string `json:"first"`
		} `json:"variants"`
		BoxesCompared int             `json:"boxes_compared"`
		BoxesDiffer   int             `json:"boxes_differ"`
		VolumesDiffer int             `json:"volumes_differ"`
		Reference     json.RawMessage `json:"reference"`
		Direct        json.RawMessage `json:"direct"`
	} `json:"fixtures"`
	Scaling []struct {
		Bays               int `json:"bays"`
		Parts              int `json:"parts"`
		Pairs              int `json:"pairs"`
		Found              int `json:"found"`
		Booleans           int `json:"booleans"`
		DirectLocations    int `json:"direct_locations"`
		ReferenceLocations int `json:"reference_locations"`
	} `json:"scaling"`
}

func pairKeyRunsOf(t *testing.T) pairKeyRuns {
	t.Helper()
	var got pairKeyRuns
	testdataJSON(t, "interference_pair_keys.py", &got)
	if got.Error != "" {
		t.Fatal(got.Error)
	}
	return got
}

// ‼️ Every pair's key is the key build123d's Locations gave, to the bit, and the
// check built on it is the same check: the same list (the worst 10,000 of 11,175 on
// the blocks), the same flag, box tests, pairs, booleans, reuses and count.
//
// Every pair of solids, not only the candidates, on the randomized prism fixtures
// (shafts, collars, girders, cones, leaning pins), turned rails and crossbars, tied
// pins, 150 blocks and a turned barrel of rivets; with the slide on and off. A key
// that differed in one bit could split one clash into two booleans or join two into
// one; comparing by repr also tells -0.0 from 0.0.
func TestKernel_APairKeyWithoutLocationsIsTheKeyBuild123dGave(t *testing.T) {
	got := pairKeyRunsOf(t)
	if len(got.Fixtures) != 8 {
		t.Fatalf("%d fixture(s), want 8", len(got.Fixtures))
	}
	slid, carried, memoized, variants, plans := 0, 0, 0, 0, 0
	summarized := false
	for _, f := range got.Fixtures {
		every := f.Parts * (f.Parts - 1) / 2
		t.Logf("%s: %d parts, %d pairs keyed, %d slid, %d carried, %d pairs of rotations memoized",
			f.Name, f.Parts, f.Compared, f.Slid, f.Carried, f.Memoized)
		if f.Compared != every || f.UnslidCompared != every || f.PlainCompared != every || f.PlanlessCompared != every {
			t.Errorf("%s: %d, %d, %d and %d keys compared, want every pair of %d parts (%d)", f.Name, f.Compared,
				f.UnslidCompared, f.PlainCompared, f.PlanlessCompared, f.Parts, every)
		}
		if f.Differ != 0 || f.UnslidDiffer != 0 || f.PlainDiffer != 0 || f.PlanlessDiffer != 0 {
			t.Errorf("%s: %d key(s) differ with the memo and the slide, %d without the slide, %d without the memo, "+
				"%d with containment per pair; first: %s",
				f.Name, f.Differ, f.UnslidDiffer, f.PlainDiffer, f.PlanlessDiffer, f.First)
		}
		for _, v := range f.Variants {
			t.Logf("%s, %s: %d pairs keyed, %d pairs of rotations memoized", f.Name, v.Name, v.Compared, v.Memoized)
			if v.Compared != every || v.Differ != 0 {
				t.Errorf("%s, %s: %d of %d keys differ; first: %s", f.Name, v.Name, v.Differ, v.Compared, v.First)
			}
			variants++
		}
		memoized += f.Memoized
		plans += f.Plans
		if f.BoxesCompared == 0 || f.BoxesDiffer != 0 || f.VolumesDiffer != 0 {
			t.Errorf("%s: of %d placed solids, %d moved box(es) and %d volume(s) differ from the loop's (_MOVED_BOX_DIRECT)",
				f.Name, f.BoxesCompared, f.BoxesDiffer, f.VolumesDiffer)
		}
		if string(f.Reference) != string(f.Direct) {
			t.Errorf("%s: the check is not the same check:\nreference %.600s\ndirect    %.600s", f.Name, f.Reference, f.Direct)
		}
		var ref struct {
			Interferences []json.RawMessage `json:"interferences"`
			Stats         struct {
				Found      int  `json:"found"`
				Summarized bool `json:"summarized"`
			} `json:"stats"`
		}
		if err := json.Unmarshal(f.Reference, &ref); err != nil {
			t.Fatal(err)
		}
		if ref.Stats.Summarized && ref.Stats.Found > len(ref.Interferences) {
			summarized = true
		}
		slid += f.Slid
		carried += f.Carried
	}
	// The fixtures have to reach what could go wrong, or the equality above is cheap.
	// ‼️ A fixture that reached no grouped containment would compare the grouped path
	// against itself and pass saying nothing, so the groups are counted too.
	if slid < 1000 || carried < 100 || !summarized || memoized < 100 || variants != 2 || plans < 100 {
		t.Errorf("the fixtures reach %d slid keys, %d carried, a summarized list=%v, %d memoized pairs of rotations, "+
			"%d location variants and %d containment groups; the fence needs all of them",
			slid, carried, summarized, memoized, variants, plans)
	}
}

// A count, not a time: the check constructs no build123d Location per pair. At 1
// and 8 bays of a turned barrel the direct path builds the same number (none), where
// build123d's grows with the pairs, four a pair.
func TestKernel_KeyingMorePairsBuildsNoMoreLocations(t *testing.T) {
	got := pairKeyRunsOf(t)
	if len(got.Scaling) != 2 {
		t.Fatalf("%d scaling run(s), want 2", len(got.Scaling))
	}
	one, eight := got.Scaling[0], got.Scaling[1]
	t.Logf("1 bay: %d parts, %d pairs, %d found, %d booleans; Locations %d direct, %d reference",
		one.Parts, one.Pairs, one.Found, one.Booleans, one.DirectLocations, one.ReferenceLocations)
	t.Logf("8 bays: %d parts, %d pairs, %d found, %d booleans; Locations %d direct, %d reference",
		eight.Parts, eight.Pairs, eight.Found, eight.Booleans, eight.DirectLocations, eight.ReferenceLocations)
	if eight.Pairs < 7*one.Pairs {
		t.Fatalf("8 bays have %d pairs against %d; the fixture does not scale", eight.Pairs, one.Pairs)
	}
	if eight.DirectLocations != one.DirectLocations {
		t.Errorf("keying %d pairs built %d Locations and %d pairs built %d; the direct path grows with the pairs",
			eight.Pairs, eight.DirectLocations, one.Pairs, one.DirectLocations)
	}
	if eight.ReferenceLocations < 4*eight.Pairs {
		t.Errorf("build123d's path built %d Locations for %d pairs; the reference no longer measures what it replaced",
			eight.ReferenceLocations, eight.Pairs)
	}
}
