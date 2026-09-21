package agent

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// Parts occupying the same material.
//
// # The gap this closes, and the measurement that found it
//
// A live car build on 2026-09-12 came back with 28 parts, document faults ZERO,
// a clean kernel build and a passing visual check — and the master cylinder
// entirely inside the engine block, the uprights inside the brake rotors. No
// check in FORGE could see any of it. geometry/assembly.go had said so in plain
// words since it was written: no interference test, no clearance, no kinematics.
// docs/spikes/2026-09-12-car-ceiling/README.md
//
// This is the first check here that is GEOMETRIC rather than visual, and it is
// the only kind that scales. A contact sheet at four hundred parts is a grey
// smudge, and material shared inside a solid was never visible in a picture at
// any part count — the vision model passed this car.
//
// # Why it costs no model call
//
// The numbers arrive from the kernel build that the render already runs (see
// render.go, Built). So the check itself is free, and only a repair costs a
// call. That is the opposite of every other check in this turn and is the reason
// it sits so early in what should be built next.
//
// # Why only the buried ones drive a repair
//
// A concept assembly has legitimate shared material: a tyre modelled a
// millimetre into its rim, two rails meeting at a weld. Reporting those is
// right; rewriting the model because of them is not. geometry.BuriedFraction is
// the line, and the reasoning for it is in interference.go beside the constant.
// Everything under it is reported to the reader and changes nothing.

// repairIfPartsOverlap asks for a fix when parts share material, and says so
// either way.
//
// ‼️ It reads the sheet's findings, which are meaningful ONLY when the picture
// came from the kernel. A deployment with no kernel finds nothing here and must
// not be told its model is clear — that is the silent downgrade the fifth
// promise refuses, so this returns without a word rather than with a reassuring
// one.
func (c *Conversation) repairIfPartsOverlap(ctx context.Context, reply *Reply, sheet *builtSheet) {
	if reply == nil || reply.Prototype == nil || sheet == nil || !sheet.FromKernel {
		return
	}
	// ‼️ How much was checked is said whatever was found — including nothing.
	//
	// This used to return without a word when the list was empty, and a check the
	// pair budget stopped part-way returns an empty list too: a truncated check
	// read exactly like a clean one. Deferred, so it describes the sheet this
	// function ends with, which after a repair is the re-check's (Phase 5, V2).
	defer func() {
		if note := coverageNote(sheet); note != "" {
			reply.noteRepair(note)
		}
		// And what the kernel did not build as described, from the same build
		// (looks designed, stage B1). Here because this is the one place every
		// turn path reads the kernel's final sheet.
		if note := builtFeaturesNote(sheet); note != "" {
			reply.noteRepair(note)
		}
		// And whether what it DID build could be made, plus any named section's
		// properties, from the same build and for the same reason (issue 6;
		// manufacturability.go).
		noteManufacturability(reply, sheet)
	}()
	found := sheet.Interferences
	if len(found) == 0 {
		return
	}
	problems := geometry.InterferenceProblems(found)
	if len(problems) == 0 {
		// Real overlaps, none of them buried. Said once, plainly, and nothing is
		// rewritten: these are the cases a concept model is allowed to have.
		reply.noteRepair("Some parts share material: " + list(found, sheet.Found) +
			" That can be deliberate at this stage, so nothing was moved.")
		return
	}

	// ‼️ What the model is ASKED is bounded; what the repair is JUDGED by is not.
	// Only the prompt is summarized (repairAsks; repair bound and check profile). The
	// repair is judged by every buried clash the kernel FOUND, which past the list's
	// bound is its count, not the list (repairVerdict).
	//
	// ‼️ A buried part in a tree is named by where it IS ("wheel/lug-nut-3"), and that
	// path is not something a repair can edit: the copy comes from a child, its
	// pattern and a definition. The 2026-09-15 live car buried every lug nut in its
	// hub, rim and tyre and the repair was shown only the paths. So each finding says
	// which child places it and, for a pattern copy, which pattern about which axis.
	// That is placedByNotes, and it is inside repairAsks — the notes are part of what
	// is sent, so they are counted against the bound rather than added past it.
	// Fence: TestInterference_ARepairIsToldWhichChildPlacesABuriedCopy.
	refused := ""
	if fixed := c.repairGeometry(ctx, reply.Prototype, repairAsks(reply.Prototype, found, sheet.Found)); fixed != nil {
		// Re-built, not re-read: the question "is it still inside" can only be
		// answered by the kernel, and a repair that claimed success against the
		// OLD numbers would be a check that cannot fail. Same reason look.go
		// re-renders after a repair it accepts.
		after := c.render(ctx, fixed)
		keep, why := repairVerdict(*sheet, after, len(fixed.Faults()) <= len(reply.Prototype.Faults()))
		if !keep {
			refused = why
		} else {
			reply.Prototype = fixed
			*sheet = after
			switch {
			case tallyOf(after).buried > 0:
				reply.noteRepair("FORGE kept a repair and re-checked it with the kernel: " + why + ".")
				found = after.Interferences
			case after.Truncated:
				// ‼️ Never "moved them apart" from a check that stopped part-way: none
				// buried is true only of the pairs it checked (Phase 5, V2), and the
				// deferred coverage note says how many those were.
				reply.noteRepair("FORGE moved parts that were inside each other and re-checked with the kernel, " +
					"which found none buried in the pairs it checked: " + why + ".")
				return
			default:
				reply.noteRepair("Parts were sitting inside each other. FORGE moved them apart " +
					"and re-checked with the kernel: " + why + ".")
				return
			}
		}
	}

	// Still wrong: say it, with the parts named. A reader looking at a model that
	// builds, reports no faults and passes the visual check has no other way to
	// learn that two of its parts are in the same place.
	note := "Parts are inside each other and FORGE could not correct it: " + list(found, sheet.Found)
	if refused != "" {
		note = strings.TrimRight(note, ".") + ". A repair was tried and not kept: " + refused + "."
	}
	reply.noteRepair(note)
}

// # How a repair is judged (repair judged by the kernel total)
//
// A repair is kept when the kernel re-builds it, it adds no document faults, and the
// kernel finds FEWER buried clashes than before. Until 2026-09-15 "finds" was
// counted from the list. The kernel lists at most the worst 10,000 clashes (#113), so a
// model with more buried clashes than that listed 10,000 before a repair and 10,000
// after it: on the 1M barrel's 768,000 buried rivets no repair could ever be kept, and
// a repair that buried more read exactly like one that changed nothing.
// docs/bugfix/2026-09-15-an-overlap-repair-was-judged-by-a-list-that-could-not-shrink.md
//
// So the count is the kernel's own (Built.Buried). A builder that does not count
// leaves the list, which is the whole count only when the list is the whole list —
// how coverageNote tells a cut list from a whole one, by Found against its length.
//
// ‼️ The count AFTER must be a total. The count before may be a floor: fewer than a
// floor is fewer than what it floors. The other way round, 10,000 listed after a
// repair is what 768,000 and 10,000 buried both list, and a repair would be kept on
// nothing.
//
// A check the pair budget stopped (V2) is judged as it always was, by what it
// checked; that it stopped is said by coverageNote, and never read as clean.
//
// # Why the found total is judged too (a repair may not add contacts)
//
// Decided 2026-09-15. Fewer buried is not by itself a fix. A repair is free to drag a
// part out of the solid it was inside and leave it grazing ten new neighbours, and
// judged on the buried total alone that reads as a success: the count fell, the note
// said so, and the document that gets installed touches MORE of the model than the one
// it replaced. The found total is the number that says so — every pair sharing
// material, buried or not — and #115 ignored it deliberately.
//
// So both totals are judged. The buried total must FALL and the found total must not
// RISE, and a refusal says which of the two tests failed, with both numbers. A rise is
// a rise: there is no allowance, because a part moved out of one solid and into contact
// with another has not been fixed, it has been moved. Equal is not a rise — a clash that
// goes from 100% buried to a 2% graze is one pair sharing material before and after, and
// that is the repair working.
//
// ‼️ The found total gets the same epistemics as the buried one, and for the same
// reason: a floor must never be compared as a total. A check cannot find fewer pairs
// sharing material than it says are buried, so found BELOW buried proves found is a
// floor — a reply that carried interferences_buried and not interferences_found, which
// buildOf leaves at the list's length. The found test is then not applied at all, the
// repair is judged on the buried total alone, and the note SAYS the found totals were
// not comparable. Never "unchanged": that would pass a test on a number nobody took.
// docs/bugfix/2026-09-15-a-found-floor-was-printed-as-a-total.md

// buriedTally is what one kernel check says about buried clashes.
type buriedTally struct {
	buried, listed, found int
	// counted says buried is every buried clash found, not a floor from a cut list.
	counted bool
	// foundCounted says found is every pair sharing material, not a floor. A check
	// cannot find fewer pairs than it says are buried, so found below buried is a reply
	// that counted the buried ones and not the found ones (repair may not add contacts).
	foundCounted bool
}

func tallyOf(s builtSheet) buriedTally {
	t := buriedTally{listed: len(s.Interferences), found: max(s.Found, len(s.Interferences))}
	t.buried = len(geometry.InterferenceProblems(s.Interferences))
	t.counted = t.found <= t.listed
	if s.BuriedCounted {
		t.buried, t.counted = max(s.Buried, t.buried), true
	}
	// ‼️ found is NOT raised to the buried count to make the pair consistent. A floor is
	// reported as the floor it is and flagged, so the rule can decline to compare it
	// (repairVerdict); inventing "at least this many pairs share material" here would
	// hand the rule, and the reader, a number the kernel never took.
	t.foundCounted = t.found >= t.buried
	return t
}

func (t buriedTally) String() string {
	switch {
	case !t.counted:
		return fmt.Sprintf("%d buried clash(es) in the %d it listed of %d pair(s) sharing material, "+
			"and it did not count how many of the rest are buried", t.buried, t.listed, t.found)
	case !t.foundCounted:
		// ‼️ Never the sentence below when found is a floor: with 768,000 counted buried
		// behind a list of 10,000 it reads "768000 buried clash(es) among 10000 pair(s)
		// sharing material", which is a floor stated as a total and arithmetic nonsense.
		return fmt.Sprintf("%d buried clash(es), and it did not say how many pairs share material "+
			"in all (it listed %d)", t.buried, t.listed)
	default:
		return fmt.Sprintf("%d buried clash(es) among %d pair(s) sharing material", t.buried, t.found)
	}
}

// repairVerdict says whether a repair re-built as after is kept over before, and why,
// with the kernel's totals.
//
// Two tests, and the note names the one that failed: the buried total must fall, and
// the found total must not rise (a repair may not add contacts).
func repairVerdict(before, after builtSheet, noNewFaults bool) (keep bool, why string) {
	switch {
	case !after.FromKernel:
		return false, "the kernel could not re-check it"
	case !noNewFaults:
		return false, "it added faults to the document"
	}
	b, a := tallyOf(before), tallyOf(after)
	// Each half says what it counted, so the sentence reads as two answers and not as
	// one number that changed: an uncounted half is a clause of its own.
	totals := fmt.Sprintf("before the repair the kernel found %s; after it, %s", b, a)
	switch {
	case !a.counted:
		return false, totals + ", so fewer could not be shown"
	case a.buried < b.buried:
		// Fewer are buried. Whether the parts were moved OUT of the model or merely into
		// contact with more of it is the found total's answer, when both are totals.
		switch {
		case !b.foundCounted || !a.foundCounted:
			return true, totals + ", so fewer are buried; whether more pairs share material " +
				"could not be compared"
		case a.found > b.found:
			return false, totals + fmt.Sprintf(": fewer buried, but the pairs sharing material "+
				"rose from %d to %d, so parts were moved into contact with more of the model "+
				"rather than out of it", b.found, a.found)
		default:
			return true, totals
		}
	case a.buried == b.buried:
		return false, totals + ": no fewer"
	default:
		return false, totals + ": more, not fewer"
	}
}

// coverageNote says how much of the model the kernel's interference check looked
// at, or "" when it looked at all of it.
//
// Two ways a check covers less than the model, and both have to be said, because
// "no parts share material" is only true of the parts that were checked:
//   - the pair budget stopped it (Truncated): "checked X of Y";
//   - a part could not be built (Skipped): it was never in the check at all.
//
// Phase 5, stage V2 of docs/plan-2026-09-13-millions-of-parts.md: every check
// reports "checked X of Y", and a truncated check can never read as clean.
func coverageNote(sheet *builtSheet) string {
	var notes []string
	if sheet.Truncated {
		if sheet.Pairs > 0 {
			notes = append(notes, fmt.Sprintf("FORGE checked %d of %d pairs of parts that could touch "+
				"for shared material and stopped there, so the model is not known to be clear.",
				sheet.Checked, sheet.Pairs))
		} else {
			notes = append(notes, "FORGE stopped checking parts for shared material before it finished, "+
				"so the model is not known to be clear.")
		}
	}
	// ‼️ A list the kernel summarized says so. It is the worst of what was found,
	// not all of it, and "and N more" alone would count only the list (next scale
	// walls; cad.Build.InterferencesFound).
	if listed := len(sheet.Interferences); sheet.Found > listed {
		notes = append(notes, fmt.Sprintf("FORGE found %d pairs of parts sharing material and lists the %d "+
			"that share the most; the rest were counted, not listed.", sheet.Found, listed))
	}
	if n := len(sheet.Skipped); n > 0 {
		const most = 3
		named := sheet.Skipped
		more := ""
		if n > most {
			named, more = named[:most], fmt.Sprintf("; and %d more", n-most)
		}
		notes = append(notes, fmt.Sprintf("%d part(s) could not be built, so they were not checked for "+
			"shared material: %s%s.", n, strings.Join(named, "; "), more))
	}
	// A mesh-only part is never checked (geometry/lattice.go): said, so "no parts
	// share material" never covers a lattice nobody looked at.
	if note := geometry.MeshOnlyNote(sheet.MeshOnly, "the check for shared material"); note != "" {
		notes = append(notes, note)
	}
	return strings.Join(notes, " ")
}

// builtFeaturesNote says which features the kernel did not build as the design says.
//
// # Why the turn says it (looks designed, stage B1)
//
// Since 2026-09-18 a fillet OCCT refuses is retried at half and a quarter of its
// size, one edge group at a time, instead of being dropped whole. That keeps a
// rounded part rounded — and makes the part differ from its document in a way
// nobody can see on screen: R1.25 and R5 look alike at a glance. So every
// reduced round, and every failed feature, is said in the turn, with where. A
// round built smaller than asked is not an error to repair — the model asked
// for more than the geometry takes — so this only reports.
func builtFeaturesNote(sheet *builtSheet) string {
	if sheet == nil {
		return ""
	}
	const most = 3
	named := func(all []string) string {
		if len(all) <= most {
			return strings.Join(all, "; ")
		}
		return strings.Join(all[:most], "; ") + fmt.Sprintf("; and %d more", len(all)-most)
	}
	var notes []string
	if n := len(sheet.FeatureReductions); n > 0 {
		notes = append(notes, fmt.Sprintf("%d fillet(s) or chamfer(s) did not fit as written, so the kernel "+
			"built them smaller or on fewer edges: %s.", n, named(sheet.FeatureReductions)))
	}
	if n := len(sheet.FeatureFailures); n > 0 {
		notes = append(notes, fmt.Sprintf("%d feature(s) could not be applied at all: %s.", n,
			named(sheet.FeatureFailures)))
	}
	return strings.Join(notes, " ")
}

// # What a repair is asked, bounded (repair bound and check profile)
//
// repairGeometry sends one prompt line per problem. Until 2026-09-15 an overlap
// repair sent one per buried clash. The 1M airframe barrel buries 768,000 rivets
// in their stringers: 108 MB of prompt lines before #113 bounded the kernel's list,
// and 1.41 MB (10,000 lines at 141 bytes) after it
// (docs/spikes/2026-09-15-next-scale-walls). Neither is a prompt. And 10,000 lines
// saying "Rivet is 82% inside Stringer" are ONE mistake: one child placed in one
// assembly, copied by patterns.
//
// So a repair is asked about PLACEMENTS. A clash's parts are resolved to the
// document's own words — the chain of child ids that placed each one, pattern
// copy numbers dropped ("bay-3/sector-7/stringer-rivets-12" is
// "bay/sector/stringer-rivets"), and the definition it places. Clashes with the same
// pair of placements are one group, told as a count, the range of how deep they
// go, and a few by name. That is what the model can change: a child's position in
// an assembly, not rivet 12 of sector 7.
//
// ‼️ Three rules, each fenced:
//   - A model whose buried clashes fit the budget as they are is asked exactly what
//     it was asked before, line for line. Summarizing four clashes would be less
//     detail for nothing.
//   - Every summary says how many pairs were found, how many were listed, how many
//     of those are buried, and how many clashes the groups it describes cover. Every
//     count is exact about what it counts: the kernel lists at most 10,000
//     (_INTERFERENCE_LIST_LIMIT), and how many of the rest are buried is not known
//     here, so it is never claimed.
//   - The worst clash is always named, first, whatever the budget.
//
// A summary is not truncation. coverageNote's "checked X of Y" (V2) is about pairs
// never checked; the kernel's cut list (#113) is about clashes found and not
// listed; this is about clashes listed and not spelled out to a model. Each says
// its own number.

// maxRepairProblemBytes bounds the problem lines an overlap repair sends, counted
// as repairGeometry writes them ("- " + line + "\n").
//
// # Why 16 KiB
//
// About 4,000 tokens at the ~4 bytes a token English prose runs in these prompts.
// The largest context this turn already sends a model beside a document is
// maxStepContextBytes, 64 KiB, in assemble.go; the problems are what is WRONG with a
// document, and should not outweigh the document they come with, so a quarter of
// that. It holds ~110 of the 141-byte lines #113 measured, and the live car that
// found this defect class buried at most four (docs/spikes/2026-09-12-car-ceiling):
// every model FORGE has built live is asked as before. A chosen bound, not a
// measured optimum — no model was measured repairing at either size.
const maxRepairProblemBytes = 16 << 10

// maxRepairLineBytes bounds one line of a summary, so a label nobody bounded cannot
// spend the budget on itself and push out the worst clash.
const maxRepairLineBytes = 1 << 10

// maxRepairExamples is how many clashes a group names beside its count.
const maxRepairExamples = 3

// repairAsks is what an overlap repair asks a model about: every buried clash as
// before when they fit maxRepairProblemBytes, otherwise a summary by placement.
// total is how many pairs the kernel found, which is more than found when it
// summarized its list.
func repairAsks(doc *Prototype, found []geometry.Interference, total int) []geometry.Problem {
	return repairAsksWithin(doc, found, total, maxRepairProblemBytes)
}

// repairAsksWithin is repairAsks against a stated budget, so a measurement can price
// the bound rather than assume it (docs/spikes/2026-09-15-last-hot-spots). The shipped
// path is repairAsks and only it chooses the budget; nothing else passes one but
// TestScaleUp_MeasureTheRepairPrompt, which sweeps candidates to choose the constant.
//
// ‼️ The sweep prices the lines that are actually SENT, notes and names included: the
// placedByNotes call and problemBytes both sit inside here, below the budget, not
// above it in repairAsks. A sweep that priced bare findings would choose a constant
// for a prompt nobody sends.
func repairAsksWithin(doc *Prototype, found []geometry.Interference, total, limit int) []geometry.Problem {
	// Each line first says which child places its parts (placedByNotes), because a
	// list that fits is asked line for line and a path alone is not editable. The
	// budget is then measured on those lines, notes included: they are sent, so they
	// are spent. A summary needs no notes — it is already told BY placement, and
	// repeating the sentence per clash is what it exists to replace.
	buried := placedByNotes(doc, found, geometry.InterferenceProblems(found))
	if problemBytes(buried) <= limit {
		return buried
	}
	if total < len(found) {
		total = len(found)
	}

	type group struct {
		a, b     placement
		clashes  []geometry.Interference
		first    int
		min, max float64
	}
	var groups []*group
	index := map[[2]placement]*group{}
	placed := placements(doc)
	worst, count := -1, 0
	for i, f := range found {
		if !f.Buried() {
			continue
		}
		count++
		if worst < 0 || f.Fraction > found[worst].Fraction {
			worst = i
		}
		// Keyed by where the parts came from, never by label: a named, patterned
		// child labels its copies "Rivet 1", "Rivet 2", and they are one placement.
		key := [2]placement{placed(f.A), placed(f.B)}
		g := index[key]
		if g == nil {
			g = &group{a: key[0], b: key[1], first: i, min: f.Fraction, max: f.Fraction}
			g.a.label, g.b.label = f.ALabel, f.BLabel
			index[key] = g
			groups = append(groups, g)
		}
		g.clashes = append(g.clashes, f)
		g.min, g.max = math.Min(g.min, f.Fraction), math.Max(g.max, f.Fraction)
	}
	// Deepest first, then the most clashes, then the one listed first. Never a map's
	// order: the same list is always the same prompt.
	sort.SliceStable(groups, func(i, j int) bool {
		gi, gj := groups[i], groups[j]
		if gi.max != gj.max {
			return gi.max > gj.max
		}
		if len(gi.clashes) != len(gj.clashes) {
			return len(gi.clashes) > len(gj.clashes)
		}
		return gi.first < gj.first
	})

	listed := fmt.Sprintf("the kernel listed all %d", len(found))
	if total > len(found) {
		listed = fmt.Sprintf("the kernel listed the %d that share the most", len(found))
	}
	// ‼️ Unnamed, all four of them. repairGeometry prefixes a problem with the part it
	// is about, because a fault's sentence does not say. A summary's sentences do, and
	// none of them is about one part: the header is about the whole model, a group is
	// about a pair of PLACEMENTS, the last line is about the groups left out. Naming
	// them would put "bay-1/sector-1/stringer-rivets-7" in front of "FORGE found
	// 1760000 pairs…", which is the opposite of saying what the line is about.
	out := []geometry.Problem{
		{Severity: geometry.Error, Detail: clip(fmt.Sprintf(
			"FORGE found %d pairs of parts sharing material; %s, and %d of those are buried. "+
				"That is too many to list one by one, so they are grouped by the placements in this "+
				"document that put the parts there: fix the placement and every copy of it moves.",
			total, listed, count))},
		// By id as well as label: a label is shared by every copy of a definition, and
		// "Rivet is 83% inside Stringer" is true of 768,000 pairs on the barrel.
		{Severity: geometry.Error, Detail: clip(fmt.Sprintf("The worst, %s in %s: %s",
			found[worst].A, found[worst].B, found[worst].Describe()))},
	}
	// What the header will add once the groups are counted, and the last line: both
	// reserved at their longest (no count below exceeds its total), so they always fit.
	coverage := func(described, covered int) string {
		return fmt.Sprintf(" %d group(s); the %d described below cover %d of the %d buried.",
			len(groups), described, covered, count)
	}
	tail := func(groupsLeft, clashesLeft int) string {
		return fmt.Sprintf("%d more group(s), %d buried clash(es) between them, are not described here.",
			groupsLeft, clashesLeft)
	}
	budget := limit - problemBytes(out) - len(coverage(len(groups), count)) -
		len("- \n") - len(tail(len(groups), count))
	described, covered := 0, 0
	for n, g := range groups {
		line := clip(describeGroup(n+1, len(groups), g.a, g.b, g.clashes, g.min, g.max))
		if len("- \n")+len(line) > budget {
			break
		}
		budget -= len("- \n") + len(line)
		out = append(out, geometry.Problem{Severity: geometry.Error, Detail: line})
		described++
		covered += len(g.clashes)
	}
	out[0].Detail += coverage(described, covered)
	if described < len(groups) {
		out = append(out, geometry.Problem{Severity: geometry.Error,
			Detail: tail(len(groups)-described, count-covered)})
	}
	return out
}

// placement is where a part came from in the document: the chain of child ids that
// placed it, pattern copy numbers dropped, and the definition it places. A part the
// tree cannot account for is its own placement, by id.
type placement struct {
	path, definition, label string
}

func (p placement) describe() string {
	name := p.label
	if name == "" {
		name = p.path
	}
	if p.definition == "" {
		return fmt.Sprintf("%s (part %s)", name, p.path)
	}
	return fmt.Sprintf("%s (definition %q, placed by %s)", name, p.definition, p.path)
}

// placements resolves built part ids through the document's tree, each id once.
func placements(doc *Prototype) func(id string) placement {
	asms, defs := map[string]*geometry.Assembly{}, map[string]bool{}
	if doc != nil {
		for i := range doc.Assemblies {
			asms[doc.Assemblies[i].ID] = &doc.Assemblies[i]
		}
		for _, d := range doc.Definitions {
			defs[d.ID] = true
		}
	}
	seen := map[string]placement{}
	resolve := func(id string) placement {
		segs := strings.Split(id, geometry.PathSeparator)
		if at := asms[doc.Root]; at != nil {
			var path []string
			for n, seg := range segs {
				c := childOf(at, seg)
				if c == nil {
					break
				}
				path = append(path, c.ID)
				last := n == len(segs)-1
				if sub := asms[c.Ref]; sub != nil && !last {
					at = sub
					continue
				}
				if defs[c.Ref] && last {
					return placement{path: strings.Join(path, geometry.PathSeparator), definition: c.Ref}
				}
				break
			}
		}
		// A top-level part, or a copy its own repeat wrote ("bolt-3").
		if len(segs) == 1 {
			for _, p := range doc.Parts {
				if id == p.ID || copyOf(id, p.ID) {
					return placement{path: p.ID}
				}
			}
		}
		return placement{path: id}
	}
	return func(id string) placement {
		if doc == nil {
			return placement{path: id}
		}
		p, ok := seen[id]
		if !ok {
			p = resolve(id)
			seen[id] = p
		}
		return p
	}
}

// childOf is the child of a that placed the id segment seg: its own id, or a copy
// numbered by its pattern, by the definition's repeat, or both ("rivets-12-2"). The
// longest matching id wins, so "rivets-2" placed as a child is not read as a copy of
// "rivets".
func childOf(a *geometry.Assembly, seg string) *geometry.Child {
	var best *geometry.Child
	for i := range a.Children {
		c := &a.Children[i]
		if c.ID == seg {
			return c
		}
		if copyOf(seg, c.ID) && (best == nil || len(c.ID) > len(best.ID)) {
			best = c
		}
	}
	return best
}

// copyOf reports whether seg is base with one or two copy numbers after it.
func copyOf(seg, base string) bool {
	rest, ok := strings.CutPrefix(seg, base+"-")
	if !ok {
		return false
	}
	numbers := strings.Split(rest, "-")
	if len(numbers) > 2 {
		return false
	}
	for _, n := range numbers {
		if !allDigits(n) {
			return false
		}
	}
	return true
}

func describeGroup(n, of int, a, b placement, clashes []geometry.Interference, lo, hi float64) string {
	depth := fmt.Sprintf("%.0f%%", hi*100)
	if fmt.Sprintf("%.0f", lo*100) != fmt.Sprintf("%.0f", hi*100) {
		depth = fmt.Sprintf("%.0f%% to %.0f%%", lo*100, hi*100)
	}
	named := make([]string, 0, maxRepairExamples)
	for i, f := range clashes {
		if i == maxRepairExamples {
			break
		}
		named = append(named, fmt.Sprintf("%s in %s (%.0f%%, %.0f mm³)", f.A, f.B, f.Fraction*100, f.Volume))
	}
	return fmt.Sprintf("Group %d of %d, %d buried clash(es): %s is %s inside %s. For example %s.",
		n, of, len(clashes), a.describe(), depth, b.describe(), strings.Join(named, "; "))
}

// problemBytes is how many bytes problems take as repairGeometry writes them.
//
// ‼️ Named, because repairGeometry is: it writes "- " + Name + " " + Detail, trimmed
// (georepair.go, TestRepair_EveryFaultTheRepairIsShownNamesThePartItIsAbout). A
// counter that measured Detail alone would under-count every per-clash line by its
// part's path — 33 bytes each on the barrel — and the bound would be spent past
// without anything noticing.
func problemBytes(problems []geometry.Problem) int {
	n := 0
	for _, p := range problems {
		n += len("- \n") + len(strings.TrimSpace(p.Name+" "+p.Detail))
	}
	return n
}

// clip cuts s to maxRepairLineBytes on a character boundary.
func clip(s string) string {
	if len(s) <= maxRepairLineBytes {
		return s
	}
	const more = "…"
	cut := maxRepairLineBytes - len(more)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + more
}

// list is the reader's sentence for a set of findings.
//
// Capped, because a broken assembly can produce dozens and a note nobody
// finishes reading is a note nobody reads. The worst are first — the kernel
// sorts by fraction — so a cap never hides the biggest one.
//
// total is how many were found, which is more than the list when the kernel
// summarized it; "and N more" counts from it, so a summarized list is never read
// as a short one.
func list(found []geometry.Interference, total int) string {
	const most = 3
	if total < len(found) {
		total = len(found)
	}
	parts := make([]string, 0, most+1)
	for i, f := range found {
		if i == most {
			break
		}
		parts = append(parts, f.Describe())
	}
	if more := total - len(parts); more > 0 {
		parts = append(parts, fmt.Sprintf("and %d more", more))
	}
	return strings.Join(parts, " ")
}

// placedByNotes adds to each buried finding in a tree which child places the part,
// so the repair edits the design that put it there. A finding about top-level parts
// is returned unchanged.
func placedByNotes(doc *Prototype, found []geometry.Interference, problems []geometry.Problem) []geometry.Problem {
	if doc == nil || doc.Root == "" {
		return problems
	}
	other := map[string]string{}
	for _, f := range found {
		other[f.A+"\x00"+f.Describe()] = f.B
	}
	out := make([]geometry.Problem, len(problems))
	for i, p := range problems {
		out[i] = p
		var where []string
		for _, id := range []string{p.Name, other[p.Name+"\x00"+p.Detail]} {
			if s := placedBySentence(*doc, id); s != "" {
				where = append(where, s)
			}
		}
		if len(where) > 0 {
			out[i].Detail = p.Detail + " (" + strings.Join(where, "; ") + ")"
		}
	}
	return out
}

// placedBySentence says what places one flattened part, or "".
func placedBySentence(doc geometry.Document, id string) string {
	at, ok := doc.PlacedBy(id)
	if !ok {
		return ""
	}
	if at.Copy > 0 && at.Pattern != nil {
		kind := strings.ToLower(strings.TrimSpace(at.Pattern.Kind))
		how := fmt.Sprintf("a %s pattern", kind)
		if kind == "polar" {
			how = fmt.Sprintf("a polar pattern about %q, which turns every copy about that axis through the "+
				"origin of the frame the child is measured in", at.Pattern.About)
		}
		return fmt.Sprintf("%s is copy %d of child %q of assembly %q, placed by %s: move that child or change "+
			"its pattern or the definition %q, not the copy", id, at.Copy, at.Child, at.Assembly, how, at.Ref)
	}
	return fmt.Sprintf("%s is placed by child %q of assembly %q: move that child or change %q, not the placed path",
		id, at.Child, at.Assembly, at.Ref)
}
