package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// An overlap repair is judged by how many buried clashes the kernel FOUND, not by
// how many it listed (repair judged by the kernel total).
//
// The kernel lists at most the worst 10,000 clashes (#113) and counts all of them.
// A model with more buried clashes than that lists 10,000 buried clashes before a
// repair and 10,000 after it, whatever the repair did: judged by the list, no repair
// of it could be kept, and a repair that made it worse read exactly like one that
// changed nothing.
// docs/bugfix/2026-09-15-an-overlap-repair-was-judged-by-a-list-that-could-not-shrink.md

// pinnedBuriedClashes is n buried clashes, as the 1M barrel's cut list holds them:
// every one a rivet 83% inside its stringer.
func pinnedBuriedClashes(n int) []geometry.Interference {
	out := make([]geometry.Interference, n)
	for i := range out {
		out[i] = geometry.Interference{A: fmt.Sprintf("rivet-%d", i), B: fmt.Sprintf("stringer-%d", i%64),
			ALabel: "Rivet", BLabel: "Stringer", Volume: 80, Fraction: 0.83}
	}
	return out
}

// repairTurn runs one overlap repair: the model moves the master cylinder to x=500,
// and the kernel's re-check answers after. It returns the reply and whether the
// repair was installed.
func repairTurn(t *testing.T, before builtSheet, after Built) (*Reply, bool) {
	t.Helper()
	moved := twoBoxes()
	moved.Parts[1].Position = []float64{500, 0, 0}
	stub := &repairStub{reply: mustJSON(t, moved)}
	if after.Parts == nil {
		after.Parts = []geometry.RenderPart{cube("block")}
	}
	c := (&Conversation{client: stub}).WithSolids(coverageSolids{built: after})
	reply := &Reply{Prototype: twoBoxes()}
	before.Image, before.FromKernel = "data:,", true

	c.repairIfPartsOverlap(context.Background(), reply, &before)

	if stub.calls != 1 {
		t.Fatalf("a buried clash drove %d repair call(s), want 1", stub.calls)
	}
	return reply, reply.Prototype.Parts[1].Position[0] == 500
}

// ‼️ The case this fix exists for: the list is cut at 10,000 before and after, and
// only the kernel's count says what the repair did.
func TestInterference_ARepairPastTheListBoundIsJudgedByTheKernelsTotal(t *testing.T) {
	const pairs, found, buried = 2191348, 1760000, 768000
	before := builtSheet{Interferences: pinnedBuriedClashes(10000), Checked: pairs, Pairs: pairs,
		Found: found, Buried: buried, BuriedCounted: true}
	for _, tc := range []struct {
		name        string
		afterBuried int
		afterFound  int
		kept        bool
		says        []string
	}{
		{"fewer buried", 400000, 1392000, true, []string{"768000", "400000", "1760000", "1392000"}},
		{"more buried", 900000, 1892000, false, []string{"could not correct", "768000", "900000", "more, not fewer"}},
		{"as many buried", buried, found, false, []string{"could not correct", "768000 buried", "no fewer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after := Built{Interferences: pinnedBuriedClashes(10000), Checked: pairs, Pairs: pairs,
				Found: tc.afterFound, Buried: tc.afterBuried, BuriedCounted: true}

			reply, kept := repairTurn(t, before, after)

			if kept != tc.kept {
				t.Errorf("%d buried before and %d after (10,000 listed both times): kept=%v, want %v",
					buried, tc.afterBuried, kept, tc.kept)
			}
			for _, want := range tc.says {
				if !strings.Contains(reply.Repaired, want) {
					t.Errorf("the note does not say %q: %q", want, reply.Repaired)
				}
			}
			if strings.Contains(reply.Repaired, "not known to be clear") {
				t.Errorf("a whole check whose list was cut read as a truncated one: %q", reply.Repaired)
			}
		})
	}
}

// ‼️ A re-check whose buried clashes were not counted cannot show a repair helped.
//
// A reply from a kernel that does not count them (a sidecar from before it did)
// holds only the listed buried clashes, and when its list is cut that is a floor, not
// a total: 10,000 is what 768,000 buried and 10,000 buried both list. Taken as a total
// it would keep a repair that did nothing.
func TestInterference_ARepairIsNotKeptOnACountTheKernelDidNotTake(t *testing.T) {
	before := builtSheet{Interferences: pinnedBuriedClashes(10000), Checked: 9, Pairs: 9,
		Found: 1760000, Buried: 768000, BuriedCounted: true}
	after := Built{Interferences: pinnedBuriedClashes(10000), Checked: 9, Pairs: 9, Found: 1760000}

	reply, kept := repairTurn(t, before, after)

	if kept {
		t.Errorf("a repair was kept on a re-check that counted only the 10,000 buried it listed: %q", reply.Repaired)
	}
	for _, want := range []string{"could not correct", "did not count"} {
		if !strings.Contains(reply.Repaired, want) {
			t.Errorf("the note does not say %q: %q", want, reply.Repaired)
		}
	}
}

// ‼️ A re-check the pair budget stopped is still never read as clean (Phase 5, V2).
//
// Its count covers only the pairs it checked. Whatever is decided about the repair,
// the note must not say the parts were moved apart as though the model were clear.
func TestInterference_ARepairReCheckedOnlyInPartNeverReadsAsClean(t *testing.T) {
	before := builtSheet{Interferences: clash(1.0), Checked: 1, Pairs: 1, Found: 1}
	after := Built{Truncated: true, Checked: 2000, Pairs: 5000}

	reply, _ := repairTurn(t, before, after)

	for _, want := range []string{"checked 2000 of 5000", "not known to be clear"} {
		if !strings.Contains(reply.Repaired, want) {
			t.Errorf("a re-check stopped after 2000 of 5000 pairs did not say %q: %q", want, reply.Repaired)
		}
	}
	if strings.Contains(reply.Repaired, "moved them apart") {
		t.Errorf("a re-check stopped part-way read as a clean one: %q", reply.Repaired)
	}
}

// A small model whose whole list is the whole count is judged exactly as before,
// from a builder that counts and from one that does not.
func TestInterference_ASmallRepairIsJudgedAsItAlwaysWas(t *testing.T) {
	three := func(fraction float64) []geometry.Interference {
		out := make([]geometry.Interference, 3)
		for i := range out {
			out[i] = clash(fraction)[0]
			out[i].A = fmt.Sprintf("cylinder-body-%d", i)
		}
		return out
	}
	for _, tc := range []struct {
		name          string
		before, after []geometry.Interference
		kept          bool
		says          string
	}{
		{"one buried, none after", clash(1.0), nil, true, "moved them apart"},
		{"one buried, still one", clash(1.0), clash(1.0), false, "could not correct"},
		{"one buried, a graze after", clash(1.0), clash(0.02), true, "moved them apart"},
		{"three buried, two after", three(0.9), three(0.9)[:2], true, "could not correct"},
		{"one buried, three after", clash(1.0), three(0.9), false, "could not correct"},
	} {
		for _, counts := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/counted=%v", tc.name, counts), func(t *testing.T) {
				after := Built{Interferences: tc.after}
				if counts {
					after.Found = len(tc.after)
					after.Buried = len(geometry.InterferenceProblems(tc.after))
					after.BuriedCounted = true
				}

				reply, kept := repairTurn(t, builtSheet{Interferences: tc.before}, after)

				if kept != tc.kept {
					t.Errorf("kept=%v, want %v: %q", kept, tc.kept, reply.Repaired)
				}
				if !strings.Contains(reply.Repaired, tc.says) {
					t.Errorf("the note does not say %q: %q", tc.says, reply.Repaired)
				}
				if strings.Contains(reply.Repaired, "lists the") || strings.Contains(reply.Repaired, "did not count") {
					t.Errorf("a whole list was described as a cut one: %q", reply.Repaired)
				}
			})
		}
	}
}
