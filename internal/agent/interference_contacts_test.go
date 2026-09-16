package agent

import (
	"fmt"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// A repair may not add contacts (decided 2026-09-15).
//
// #115 made a repair's acceptance turn on the kernel's BURIED total, because the listed
// count is capped at 10,000 (#113) and so could never fall on a large model. It ignored
// the FOUND total deliberately. That leaves a repair accepted whenever fewer parts are
// buried than before — including one that pulls a part out of the solid it was inside
// and leaves it touching ten new neighbours, which is not a fix.
//
// Both totals are judged now: the buried total must fall AND the found total must not
// rise. A refusal names which of the two tests failed and states both numbers.

// pinnedGrazes is n pairs sharing material with NONE of them buried: the contacts a
// repair adds when it slides a part out of a solid and into its neighbours.
func pinnedGrazes(n int) []geometry.Interference {
	out := make([]geometry.Interference, n)
	for i := range out {
		out[i] = geometry.Interference{A: fmt.Sprintf("rivet-%d", i), B: fmt.Sprintf("skin-%d", i%64),
			ALabel: "Rivet", BLabel: "Skin", Volume: 2, Fraction: 0.02}
	}
	return out
}

// barrelPairs is the 1M airframe barrel's candidate pairs, so a sheet below reads as the
// model this decision was taken about (docs/spikes/2026-09-15-next-scale-walls).
const barrelPairs = 2191348

// barrelBuried is that barrel as the kernel reports it: 1,760,000 pairs sharing
// material, 768,000 of them buried, and the worst 10,000 listed.
func barrelBuried() builtSheet {
	return builtSheet{Interferences: pinnedBuriedClashes(10000), Checked: barrelPairs, Pairs: barrelPairs,
		Found: 1760000, Buried: 768000, BuriedCounted: true}
}

// ‼️ The case this decision exists for. Half the rivets came out of their stringers and
// half a million more pairs of parts now share material: judged on the buried total
// alone this was kept, and the document that got installed touched more of the model
// than the one it replaced.
func TestInterference_ARepairThatBuriesFewerPartsButTouchesMoreIsRefused(t *testing.T) {
	after := Built{Interferences: pinnedBuriedClashes(10000), Checked: barrelPairs, Pairs: barrelPairs,
		Found: 2300000, Buried: 400000, BuriedCounted: true}

	reply, kept := repairTurn(t, barrelBuried(), after)

	if kept {
		t.Errorf("a repair that buried 368,000 fewer parts and put 540,000 more pairs of them in "+
			"contact was kept: %q", reply.Repaired)
	}
	// Both totals, both ways round: the note has to let a reader see the trade it refused.
	for _, want := range []string{"could not correct", "768000", "400000", "1760000", "2300000",
		"pairs sharing material rose"} {
		if !strings.Contains(reply.Repaired, want) {
			t.Errorf("the note does not say %q: %q", want, reply.Repaired)
		}
	}
	// ‼️ The refusal must name the test that failed. "no fewer" and "more, not fewer" are
	// both lies here: fewer parts really are buried, and that is why the found total had
	// to be looked at.
	for _, wrong := range []string{"no fewer", "more, not fewer"} {
		if strings.Contains(reply.Repaired, wrong) {
			t.Errorf("a repair refused for adding contacts was refused as though it buried more (%q): %q",
				wrong, reply.Repaired)
		}
	}
}

// A repair that buries fewer and adds no contacts is kept, which is what #115 shipped
// and what this decision must not break.
func TestInterference_ARepairIsKeptWhenFewerAreBuriedAndNoMorePairsShareMaterial(t *testing.T) {
	for _, tc := range []struct {
		name       string
		afterFound int
	}{
		{"fewer pairs share material", 1392000},
		// ‼️ Equal is not a rise. A clash that goes from 100% buried to a 2% graze is the
		// same one pair sharing material before and after — the repair working, not a
		// contact added. A rule that refused this would refuse the ordinary fix.
		{"the same pairs share material", 1760000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			after := Built{Interferences: pinnedBuriedClashes(10000), Checked: barrelPairs,
				Pairs: barrelPairs, Found: tc.afterFound, Buried: 400000, BuriedCounted: true}

			reply, kept := repairTurn(t, barrelBuried(), after)

			if !kept {
				t.Errorf("a repair that buried 368,000 fewer parts and added no pairs sharing "+
					"material was refused: %q", reply.Repaired)
			}
			if !strings.Contains(reply.Repaired, "kept a repair") {
				t.Errorf("a kept repair did not say it was kept: %q", reply.Repaired)
			}
			for _, wrong := range []string{"rose from", "could not be compared"} {
				if strings.Contains(reply.Repaired, wrong) {
					t.Errorf("a repair that added no contacts was described as though it had (%q): %q",
						wrong, reply.Repaired)
				}
			}
		})
	}
}

// A repair that buries MORE is refused for burying more, however few pairs share
// material afterwards. The buried test is the first of the two and the note says so.
func TestInterference_ARepairThatBuriesMoreIsRefusedForBuryingMore(t *testing.T) {
	// Far fewer pairs share material than before — and more of them are buried. Fewer
	// contacts must not buy a deeper burial any more than fewer burials buys contacts.
	after := Built{Interferences: pinnedBuriedClashes(10000), Checked: barrelPairs, Pairs: barrelPairs,
		Found: 900000, Buried: 800000, BuriedCounted: true}

	reply, kept := repairTurn(t, barrelBuried(), after)

	if kept {
		t.Errorf("a repair that buried 32,000 more parts was kept because fewer pairs touched: %q",
			reply.Repaired)
	}
	for _, want := range []string{"could not correct", "more, not fewer", "768000", "800000"} {
		if !strings.Contains(reply.Repaired, want) {
			t.Errorf("the note does not say %q: %q", want, reply.Repaired)
		}
	}
}

// ‼️ A found total the kernel did not take is never compared, and never called unchanged.
//
// A reply can carry the buried count and not the found one — buildOf then leaves found at
// the list's length, which is a FLOOR, and provably so: a check cannot find fewer pairs
// sharing material than it says are buried. Compared as a total it is 10,000 against
// 1,392,000 and every repair of the model is refused for contacts it never added; read as
// "unchanged" the new test passes on a number nobody took. So the repair is judged on the
// buried total alone and the note says the found totals were not comparable.
// docs/bugfix/2026-09-15-a-found-floor-was-printed-as-a-total.md
func TestInterference_AFoundTotalTheKernelDidNotTakeIsNeitherComparedNorCalledUnchanged(t *testing.T) {
	// Buried counted, found missing: found reads as the 10,000 listed, below the 768,000
	// the same check says are buried.
	floorBefore := builtSheet{Interferences: pinnedBuriedClashes(10000), Checked: barrelPairs,
		Pairs: barrelPairs, Buried: 768000, BuriedCounted: true}
	for _, tc := range []struct {
		name   string
		before builtSheet
		after  Built
	}{
		{"the found total before the repair is a floor", floorBefore,
			Built{Interferences: pinnedBuriedClashes(10000), Checked: barrelPairs, Pairs: barrelPairs,
				Found: 1392000, Buried: 400000, BuriedCounted: true}},
		{"the found total after the repair is a floor", barrelBuried(),
			Built{Interferences: pinnedBuriedClashes(10000), Checked: barrelPairs, Pairs: barrelPairs,
				Buried: 400000, BuriedCounted: true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reply, kept := repairTurn(t, tc.before, tc.after)

			if !kept {
				t.Errorf("a repair that buried 368,000 fewer parts was refused over a found total "+
					"the kernel never took: %q", reply.Repaired)
			}
			for _, want := range []string{"could not be compared", "did not say how many pairs share material"} {
				if !strings.Contains(reply.Repaired, want) {
					t.Errorf("the note does not say %q: %q", want, reply.Repaired)
				}
			}
			// ‼️ The floor must never be printed as a total: "768000 buried clash(es) among
			// 10000 pair(s) sharing material" is arithmetic nonsense and a claim the kernel
			// did not make.
			if strings.Contains(reply.Repaired, "among 10000 pair(s)") {
				t.Errorf("a floor of 10,000 pairs was stated as the total sharing material: %q", reply.Repaired)
			}
		})
	}
}
