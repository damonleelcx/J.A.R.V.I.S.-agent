package agent

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// What an overlap repair asks a model, bounded (repair bound and check profile).
//
// The 1M airframe barrel buries 768,000 rivets; #113 bounded the kernel's list to the
// worst 10,000, and a repair still sent a line for each: 1.41 MB. These fences hold
// the summary that replaced it, offline, with the repair's model a stub.

// barrelLike is the airframe barrel's tree (geometry/barrel_scale_test.go) without
// its dimensions: a bay of skin and rivet rows, turned into a ring of sectors,
// repeated along the barrel, and a stringer per sector the barrel's length.
func barrelLike() *Prototype {
	grid := &geometry.Pattern{Kind: "grid", Rows: 2, Columns: 48, RowOffset: []float64{0, 0, 10}, ColumnOffset: []float64{10, 0, 0}}
	polar := &geometry.Pattern{Kind: "polar", Count: 80, About: "x"}
	return &geometry.Document{Name: "barrel", Units: "mm", Root: "barrel",
		Definitions: []geometry.Part{
			{ID: "rivet", Name: "Rivet", Shape: "cylinder", Size: map[string]float64{"radius": 2.4, "height": 14}},
			{ID: "skin", Name: "Skin panel", Shape: "box", Size: map[string]float64{"width": 498, "height": 2, "depth": 146}},
			{ID: "stringer", Name: "Stringer", Shape: "box", Size: map[string]float64{"width": 50000, "height": 20, "depth": 20}},
		},
		Assemblies: []geometry.Assembly{
			{ID: "bay", Children: []geometry.Child{
				{ID: "skin", Ref: "skin"},
				{ID: "stringer-rivets", Ref: "rivet", Pattern: grid},
				{ID: "frame-rivets-port", Ref: "rivet", Pattern: grid},
			}},
			{ID: "ring", Children: []geometry.Child{{ID: "sector", Ref: "bay", Pattern: polar}}},
			{ID: "barrel", Children: []geometry.Child{
				{ID: "bay", Ref: "ring", Pattern: &geometry.Pattern{Kind: "linear", Count: 100, Offset: []float64{500, 0, 0}}},
				{ID: "stringer", Ref: "stringer", Pattern: polar},
			}},
		}}
}

// barrelClashes is n clashes the way the kernel lists the barrel's: worst first.
// Stringer rivets are 82% inside their stringer, frame rivets 60% inside a skin
// panel, a few rivets 14% inside the skin (reported, not buried), and deeper ones
// are sprinkled through so the fractions are not all one number.
func barrelClashes(n int) []geometry.Interference {
	out := make([]geometry.Interference, 0, n)
	for k := 0; len(out) < n; k++ {
		bay, sector, rivet := k/(80*96)+1, k/96%80+1, k%96+1
		switch {
		case k%10 == 9:
			out = append(out, geometry.Interference{
				A: fmt.Sprintf("bay-%d/sector-%d/frame-rivets-port-%d", bay, sector, rivet), B: fmt.Sprintf("bay-%d/sector-%d/skin", bay, sector),
				ALabel: "Rivet", BLabel: "Skin panel", Volume: 150, Fraction: 0.60})
		case k%10 == 8:
			out = append(out, geometry.Interference{
				A: fmt.Sprintf("bay-%d/sector-%d/stringer-rivets-%d", bay, sector, rivet), B: fmt.Sprintf("bay-%d/sector-%d/skin", bay, sector),
				ALabel: "Rivet", BLabel: "Skin panel", Volume: 36, Fraction: 0.14})
		default:
			out = append(out, geometry.Interference{
				A: fmt.Sprintf("bay-%d/sector-%d/stringer-rivets-%d", bay, sector, rivet), B: fmt.Sprintf("stringer-%d", sector),
				ALabel: "Rivet", BLabel: "Stringer", Volume: 208, Fraction: 0.82 + float64(k%7)*0.001})
		}
	}
	// The kernel's order: worst first, ties as found.
	sortWorstFirst(out)
	return out
}

func sortWorstFirst(found []geometry.Interference) {
	for i := 1; i < len(found); i++ {
		for j := i; j > 0 && found[j].Fraction > found[j-1].Fraction; j-- {
			found[j], found[j-1] = found[j-1], found[j]
		}
	}
}

// asked is the problem section of what the stub model was sent, and its size as
// repairGeometry counts it.
func asked(t *testing.T, prompt string) (string, int) {
	t.Helper()
	_, rest, ok := strings.Cut(prompt, "cannot be built:\n")
	section, _, ok2 := strings.Cut(rest, "\n\nCorrect ONLY")
	if !ok || !ok2 {
		t.Fatalf("the repair prompt has no problem section:\n%.400s", prompt)
	}
	return section, len(section) + 1
}

// asksAsLines is what a repair is shown for a list small enough to be asked as it
// is, and its size: every buried clash, told which child placed its parts
// (placedByNotes), written the way repairGeometry writes a problem — named.
//
// ‼️ Both halves of that are the merge of #113's stack with this branch: main gave
// every buried finding its placement sentence and gave every problem line its part's
// name, and both are inside what maxRepairProblemBytes bounds. "As it was asked
// before" is measured here so it cannot drift from georepair.go silently.
func asksAsLines(doc *Prototype, found []geometry.Interference) ([]string, int) {
	problems := placedByNotes(doc, found, geometry.InterferenceProblems(found))
	lines := make([]string, len(problems))
	for i, p := range problems {
		lines[i] = "- " + strings.TrimSpace(p.Name+" "+p.Detail)
	}
	return lines, problemBytes(problems)
}

func buriedIn(found []geometry.Interference) int {
	n := 0
	for _, f := range found {
		if f.Buried() {
			n++
		}
	}
	return n
}

// ‼️ The case this exists for: 10,000 listed clashes, 1,760,000 found, and the
// model is asked within the budget, told every count exactly, and named the worst.
func TestInterference_ARepairOfTenThousandClashesIsAskedWithinItsBudget(t *testing.T) {
	found := barrelClashes(10000)
	stub := &repairStub{}
	c := &Conversation{client: stub}
	reply := &Reply{Prototype: barrelLike()}
	sheet := builtSheet{Image: "data:,", FromKernel: true, Checked: 2191348, Pairs: 2191348, Found: 1760000,
		Interferences: found}

	c.repairIfPartsOverlap(context.Background(), reply, &sheet)

	if stub.calls != 1 {
		t.Fatalf("10,000 buried clashes drove %d repair call(s), want 1", stub.calls)
	}
	section, size := asked(t, stub.asked)
	if old := problemBytes(geometry.InterferenceProblems(found)); old <= maxRepairProblemBytes {
		t.Fatalf("the fixture is too small to need a summary: %d bytes as lines", old)
	}
	if size > maxRepairProblemBytes {
		t.Errorf("the repair was asked %d bytes of problems, over the %d budget", size, maxRepairProblemBytes)
	}
	buried := buriedIn(found)
	for _, want := range []string{
		"FORGE found 1760000 pairs of parts sharing material",
		"the kernel listed the 10000 that share the most",
		fmt.Sprintf("and %d of those are buried", buried),
		fmt.Sprintf("The worst, %s in %s: %s", found[0].A, found[0].B, found[0].Describe()),
		`Rivet (definition "rivet", placed by bay/sector/stringer-rivets) is 82% to 83% inside Stringer (definition "stringer", placed by stringer)`,
		`placed by bay/sector/frame-rivets-port) is 60% inside Skin panel (definition "skin", placed by bay/sector/skin)`,
	} {
		if !strings.Contains(section, want) {
			t.Errorf("the repair was not told %q:\n%s", want, section)
		}
	}
	checkSummaryCounts(t, section, buried)
}

// checkSummaryCounts holds a summary's numbers to each other: the groups described
// and the groups counted in the last line are every group, and their clashes are
// every buried clash.
func checkSummaryCounts(t *testing.T, section string, buried int) {
	t.Helper()
	head := regexp.MustCompile(`(\d+) group\(s\); the (\d+) described below cover (\d+) of the (\d+) buried`).FindStringSubmatch(section)
	if head == nil {
		t.Fatalf("the summary does not say how many it covers:\n%s", section)
	}
	groups, described, covered, of := atoi(head[1]), atoi(head[2]), atoi(head[3]), atoi(head[4])
	if of != buried {
		t.Errorf("the summary says %d buried, the list has %d", of, buried)
	}
	lines := regexp.MustCompile(`(?m)^- Group (\d+) of (\d+), (\d+) buried clash`).FindAllStringSubmatch(section, -1)
	sum := 0
	for n, l := range lines {
		if atoi(l[1]) != n+1 || atoi(l[2]) != groups {
			t.Errorf("group line %d reads %q", n+1, l[0])
		}
		sum += atoi(l[3])
	}
	if len(lines) != described || sum != covered {
		t.Errorf("the header says %d groups cover %d clashes; the lines are %d groups covering %d", described, covered, len(lines), sum)
	}
	tail := regexp.MustCompile(`(\d+) more group\(s\), (\d+) buried clash\(es\) between them`).FindStringSubmatch(section)
	leftGroups, leftClashes := 0, 0
	if tail != nil {
		leftGroups, leftClashes = atoi(tail[1]), atoi(tail[2])
	}
	if described+leftGroups != groups || covered+leftClashes != buried {
		t.Errorf("%d+%d groups of %d and %d+%d clashes of %d: a summary lost count", described, leftGroups, groups,
			covered, leftClashes, buried)
	}
}

func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		panic(err)
	}
	return n
}

// The same list is the same prompt, byte for byte, however many groups tie. Groups
// live in a map while they are counted; the order they are told in may not.
func TestInterference_ARepairSummaryIsTheSamePromptEveryTime(t *testing.T) {
	// 600 unrelated parts, each buried once in a housing: 600 groups with the same
	// depth and the same count, more than the budget can describe.
	var found []geometry.Interference
	for k := 0; k < 600; k++ {
		found = append(found, geometry.Interference{A: fmt.Sprintf("pin-%c%c", 'a'+k/26, 'a'+k%26), B: "housing",
			ALabel: "Pin", BLabel: "Housing", Volume: 90, Fraction: 0.9})
	}
	first := repairAsks(twoBoxes(), found, len(found))
	for run := 0; run < 20; run++ {
		again := repairAsks(twoBoxes(), found, len(found))
		if fmt.Sprint(again) != fmt.Sprint(first) {
			t.Fatalf("run %d asked something different:\n%v\nagainst\n%v", run, again, first)
		}
	}
	text := make([]string, len(first))
	for i, p := range first {
		text[i] = "- " + p.Detail
	}
	section := strings.Join(text, "\n")
	if !strings.Contains(section, "Group 1 of 600, 1 buried clash(es): Pin (part pin-aa)") {
		t.Errorf("tied groups are not told in the order they were listed:\n%.600s", section)
	}
	checkSummaryCounts(t, section, 600)
}

// ‼️ The worst clash is named whatever else is in the list: last in it, in a group
// of one, behind thousands of shallower clashes, with a label long enough to spend
// the budget by itself.
func TestInterference_TheWorstClashIsNamedWhateverTheBudget(t *testing.T) {
	// Two names one byte apart, so one of them puts the line's cut inside a
	// two-byte character whatever the rest of the line adds up to.
	for _, name := range []string{"Main bearing ", "Main bearing x"} {
		found := barrelClashes(5000)
		long := strings.Repeat("é", 3000)
		worst := geometry.Interference{A: "bearing", B: "bay-1/sector-1/skin", ALabel: name + long,
			BLabel: "Skin panel", Volume: 4000, Fraction: 0.99}
		found = append(found, worst)

		problems := repairAsks(barrelLike(), found, 20000)

		if size := problemBytes(problems); size > maxRepairProblemBytes {
			t.Errorf("%d bytes of problems, over the %d budget", size, maxRepairProblemBytes)
		}
		if len(problems) < 2 || !strings.HasPrefix(problems[1].Detail, "The worst, bearing in bay-1/sector-1/skin: "+name+"ééé") ||
			!strings.Contains(problems[0].Detail, "found 20000 pairs") {
			t.Fatalf("the worst clash, last in the list, is not named second after the counts:\n%v", problems)
		}
		for _, p := range problems {
			if !utf8.ValidString(p.Detail) {
				t.Errorf("a line was cut inside a character: %q", p.Detail[len(p.Detail)-8:])
			}
		}
	}
}

// ‼️ The budget holds at every length of line, not only at the lengths the other
// fences happen to produce. The header's count and the last line are written after
// the groups are chosen; a budget that kept no room for them would pass itself by a
// few bytes whenever the groups filled it closely, which fixed sizes may never do.
func TestInterference_ARepairSummaryNeverPassesItsBudget(t *testing.T) {
	closest := 0
	for pad := 0; pad < 400; pad++ {
		var found []geometry.Interference
		for k := 0; k < 300; k++ {
			found = append(found, geometry.Interference{A: fmt.Sprintf("pin-%03d", k), B: "housing",
				ALabel: "Pin " + strings.Repeat("x", pad), BLabel: "Housing", Volume: 90, Fraction: 0.9})
		}
		size := problemBytes(repairAsks(twoBoxes(), found, 1000000))
		if size > maxRepairProblemBytes {
			t.Fatalf("labels %d bytes longer: %d bytes of problems, over the %d budget", pad, size, maxRepairProblemBytes)
		}
		if size > closest {
			closest = size
		}
	}
	if closest < maxRepairProblemBytes-128 {
		t.Errorf("the sweep came no closer than %d bytes to the %d budget, so it could not see an overrun",
			maxRepairProblemBytes-closest, maxRepairProblemBytes)
	}
}

// A model whose buried clashes fit the budget as lines is asked exactly what it was
// asked before — the live car's four, and the longest list that still fits.
func TestInterference_AFewBuriedClashesAreAskedAboutAsBefore(t *testing.T) {
	car := []geometry.Interference{
		{A: "master-cylinder", B: "engine-block", ALabel: "Master cylinder", BLabel: "Engine block", Volume: 9000, Fraction: 1},
		{A: "pinion-gear", B: "central-tunnel", ALabel: "Pinion", BLabel: "Central tunnel", Volume: 700, Fraction: 0.88},
		{A: "left-upright", B: "left-front-rotor", Volume: 300, Fraction: 0.7},
		{A: "right-upright", B: "right-front-rotor", Volume: 300, Fraction: 0.7},
		{A: "tyre", B: "rim", Volume: 20, Fraction: 0.02},
	}
	fits := barrelClashes(1)
	for {
		next := barrelClashes(len(fits) + 1)
		if _, size := asksAsLines(barrelLike(), next); size > maxRepairProblemBytes {
			break
		}
		fits = next
	}
	for name, found := range map[string][]geometry.Interference{"the live car": car, "the longest list that fits": fits} {
		t.Run(name, func(t *testing.T) {
			stub := &repairStub{}
			c := &Conversation{client: stub}
			reply := &Reply{Prototype: barrelLike()}
			sheet := builtSheet{Image: "data:,", FromKernel: true, Found: len(found), Interferences: found}

			c.repairIfPartsOverlap(context.Background(), reply, &sheet)

			section, size := asked(t, stub.asked)
			want, wantSize := asksAsLines(barrelLike(), found)
			if section != strings.Join(want, "\n") {
				t.Errorf("a list that fits was not asked as it was:\n%.600s", section)
			}
			// ‼️ And problemBytes counted those same bytes. The budget is checked against
			// it, so a counter that skipped the part each line names would let the bound
			// be passed by a path per line without any fence noticing.
			if size != wantSize {
				t.Errorf("the prompt is %d bytes of problems, problemBytes counted %d", size, wantSize)
			}
			if size > maxRepairProblemBytes {
				t.Errorf("a list said to fit was asked %d bytes, over the %d budget", size, maxRepairProblemBytes)
			}
		})
	}
	over := barrelClashes(len(fits) + 1)
	if got := repairAsks(barrelLike(), over, len(over)); !strings.HasPrefix(got[0].Detail, "FORGE found") {
		t.Errorf("one clash past the budget was still asked as lines: %.200s", got[0].Detail)
	}
}

// A count, not a time: from 100 to 100,000 listed clashes the prompt stays within
// its budget, and every count in it is exact.
func TestInterference_ARepairSummaryDoesNotGrowWithTheClashes(t *testing.T) {
	for _, n := range []int{300, 10000, 100000} {
		found := barrelClashes(n)
		problems := repairAsks(barrelLike(), found, 3*n)
		text := make([]string, len(problems))
		for i, p := range problems {
			text[i] = "- " + p.Detail
		}
		section := strings.Join(text, "\n")
		if size := problemBytes(problems); size > maxRepairProblemBytes {
			t.Errorf("%d clashes asked %d bytes, over the %d budget", n, size, maxRepairProblemBytes)
		}
		if !strings.Contains(section, fmt.Sprintf("found %d pairs", 3*n)) || !strings.Contains(section, fmt.Sprintf("The worst, %s in %s: %s", found[0].A, found[0].B, found[0].Describe())) {
			t.Errorf("%d clashes: the total or the worst is missing:\n%.500s", n, section)
		}
		checkSummaryCounts(t, section, buriedIn(found))
	}
}

// A clash is traced to the placement that made it, in the document's own ids.
func TestInterference_AClashIsTracedToThePlacementThatMadeIt(t *testing.T) {
	doc := barrelLike()
	doc.Assemblies[0].Children = append(doc.Assemblies[0].Children,
		geometry.Child{ID: "stringer-rivets-2", Ref: "rivet"})
	doc.Parts = []geometry.Part{{ID: "bolt", Shape: "cylinder"}}
	placed := placements(doc)
	for id, want := range map[string]placement{
		"bay-3/sector-7/stringer-rivets-12":   {path: "bay/sector/stringer-rivets", definition: "rivet"},
		"bay-3/sector-7/stringer-rivets-12-2": {path: "bay/sector/stringer-rivets", definition: "rivet"},
		"bay-3/sector-7/stringer-rivets-2":    {path: "bay/sector/stringer-rivets-2", definition: "rivet"},
		"bay-3/sector-7/stringer-rivets-2-5":  {path: "bay/sector/stringer-rivets-2", definition: "rivet"},
		"bay-1/sector-80/skin":                {path: "bay/sector/skin", definition: "skin"},
		"stringer-79":                         {path: "stringer", definition: "stringer"},
		"bolt-3":                              {path: "bolt"},
		"bay-1/sector-x/skin":                 {path: "bay-1/sector-x/skin"},
		"bay-1/sector-1":                      {path: "bay-1/sector-1"},
		"ghost":                               {path: "ghost"},
	} {
		if got := placed(id); got != want {
			t.Errorf("%s traced to %+v, want %+v", id, got, want)
		}
	}
}
