package cad_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The interference list's bound (next scale walls): a reply lists at most
// _INTERFERENCE_LIST_LIMIT clashes, the worst, and counts every one. The
// 1,008,160-occurrence barrel finds 1,760,000; docs/spikes/2026-09-15-next-scale-walls.

type listLimitRun struct {
	Limit         int                     `json:"limit"`
	Interferences []geometry.Interference `json:"interferences"`
	Found         *int                    `json:"found"`
	Summarized    bool                    `json:"summarized"`
	Truncated     bool                    `json:"truncated"`
	Pairs         int                     `json:"pairs"`
	ReplyBytes    int                     `json:"reply_bytes"`
}

type listLimit struct {
	Error        string       `json:"error"`
	ShippedLimit int          `json:"shipped_limit"`
	Whole        listLimitRun `json:"whole"`
	At           listLimitRun `json:"at"`
	Below        listLimitRun `json:"below"`
	Seven        listLimitRun `json:"seven"`
}

// A list cut to its bound is the head of the whole list, in the same order, and
// the reply says how many there were and that it is a summary. A list exactly at
// the bound is whole and says so.
func TestKernel_AListCutToItsBoundIsTheWorstAndSaysHowManyThereWere(t *testing.T) {
	var got listLimit
	testdataJSON(t, "interference_list_limit.py", &got)
	if got.Error != "" {
		t.Fatal(got.Error)
	}
	whole := got.Whole
	n := len(whole.Interferences)
	t.Logf("%d clash(es) from %d pair(s); shipped bound %d; reply %d bytes whole, %d at 7",
		n, whole.Pairs, got.ShippedLimit, whole.ReplyBytes, got.Seven.ReplyBytes)
	if n < 30 || whole.Truncated {
		t.Fatalf("the fixture found %d clash(es), truncated=%v; it needs enough to cut", n, whole.Truncated)
	}
	if got.ShippedLimit < 1000 {
		t.Errorf("the shipped bound is %d; the turn's own fences count thousands of clashes", got.ShippedLimit)
	}
	for _, run := range []struct {
		name       string
		r          listLimitRun
		listed     int
		summarized bool
	}{
		{"far above the count", whole, n, false},
		{"exactly at the count", got.At, n, false},
		{"one below the count", got.Below, n - 1, true},
		{"at 7", got.Seven, 7, true},
	} {
		r := run.r
		if r.Found == nil {
			t.Errorf("%s: the reply does not say how many clashes there were", run.name)
			continue
		}
		if *r.Found != n || r.Pairs != whole.Pairs || r.Truncated {
			t.Errorf("%s: found %d of %d pairs, truncated=%v; the whole check found %d of %d",
				run.name, *r.Found, r.Pairs, r.Truncated, n, whole.Pairs)
		}
		if len(r.Interferences) != run.listed || r.Summarized != run.summarized {
			t.Errorf("%s: listed %d, summarized=%v; want %d, %v", run.name, len(r.Interferences), r.Summarized,
				run.listed, run.summarized)
		}
		for i, have := range r.Interferences {
			if i >= n {
				break
			}
			want := whole.Interferences[i]
			if have != want {
				t.Errorf("%s: clash %d is %+v; the whole list's is %+v", run.name, i, have, want)
				break
			}
		}
	}
	// Ties sort in the order the pairs were found, so a cut through a tie keeps the
	// same ones every time: the fixture must have one there.
	tie := false
	for i := 1; i < n; i++ {
		if whole.Interferences[i].Fraction == whole.Interferences[i-1].Fraction {
			tie = true
		}
		if whole.Interferences[i].Fraction > whole.Interferences[i-1].Fraction {
			t.Errorf("the whole list is not worst first at %d", i)
		}
	}
	if !tie {
		t.Error("the fixture has no tied fractions, so it cannot show a cut through a tie is deterministic")
	}
}

// Through Go at the shipped bound: 150 blocks each 0.02 mm along from the last
// share material in all 11,175 pairs. The reply lists 10,000 — every pair up to
// 101 steps apart and one of the 102-step pairs — counts 11,175, and is not a
// truncated check.
func TestKernel_AReplyWithMoreClashesThanItListsCountsThemAll(t *testing.T) {
	k := kernel(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	block := geometry.Part{ID: "block", Name: "Block", Shape: "box", Size: map[string]float64{"width": 10, "height": 10, "depth": 10}}
	doc := geometry.Document{Name: "stack", Units: "mm", Root: "stack",
		Definitions: []geometry.Part{block},
		Assemblies: []geometry.Assembly{{ID: "stack", Children: []geometry.Child{
			{ID: "blocks", Ref: "block", Pattern: &geometry.Pattern{Kind: "linear", Count: 150, Offset: []float64{0.02, 0, 0}}},
		}}}}
	got, err := k.BuildDocument(ctx, doc, geometry.Millimetre, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%d parts: %d pair(s), %d boolean(s), %d reused, %d found, %d listed, summarized=%v, truncated=%v",
		got.Parts, got.InterferencePairs, got.InterferenceBooleans, got.InterferenceReused,
		got.InterferencesFound, len(got.Interferences), got.InterferencesSummarized, got.InterferencesTruncated)
	if got.Parts != 150 || got.InterferencePairs != 11175 || got.InterferencesTruncated {
		t.Fatalf("%d parts, %d pairs, truncated=%v; want 150 blocks, all 11,175 pairs checked",
			got.Parts, got.InterferencePairs, got.InterferencesTruncated)
	}
	if got.InterferencesFound != 11175 {
		t.Errorf("the build says %d clashes; every pair of blocks shares material, 11,175", got.InterferencesFound)
	}
	if len(got.Interferences) != 10000 || !got.InterferencesSummarized {
		t.Errorf("listed %d, summarized=%v; want the worst 10,000 and a summary", len(got.Interferences), got.InterferencesSummarized)
	}
	if len(got.Interferences) == 0 {
		return
	}
	first, last := got.Interferences[0], got.Interferences[len(got.Interferences)-1]
	// Neighbours share 9.98 of 10 mm; the 10,000th pair is 102 steps apart, 7.96.
	if math.Abs(first.Fraction-0.998) > 1e-6 || math.Abs(last.Fraction-0.796) > 1e-6 {
		t.Errorf("the list runs from %.6f to %.6f; the worst 10,000 run from 0.998 to 0.796", first.Fraction, last.Fraction)
	}
}
