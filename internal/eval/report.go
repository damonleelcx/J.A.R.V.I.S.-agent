package eval

import (
	"fmt"
	"strings"
)

// Rendering the report.
//
// One implementation, on the report itself, for the same reason the workspace
// renders an edge's sentence server-side: two surfaces composing their own
// summary of the same numbers will eventually disagree about what the numbers
// mean, and the one a person reads at 3am will be the wrong one.

// Render writes the report as a person needs to read it.
//
// The rates come first and the raw observations after, because the question
// being asked is "did anything move", and the evidence is what you go to once
// something has. Every run's detail is printed — including the ones that held —
// so the scoring can be re-judged rather than taken on trust.
func (r *Report) Render(verbose bool) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Model: %s   Repeats: %d   Requests: %d   Tokens: %d   Elapsed: %s\n\n",
		r.Model, r.Repeats, r.Requests, r.Tokens, r.Elapsed.Round(100_000_000))

	b.WriteString("Every scorer below is deterministic Go over the reply. Nothing here grades its own\n")
	b.WriteString("homework, and a rate is a measurement rather than a guarantee: 4 of 4 is four runs.\n\n")

	// The two kinds are printed apart, and the regressions first.
	//
	// # Why the separation is in the OUTPUT and not only in the data
	//
	// They answer different questions and a reader who conflates them draws the
	// wrong conclusion in both directions. A regression below its floor means a
	// promise this build already broke once has broken again — act now. A
	// coverage case scoring badly means an industry is served worse than claimed,
	// which is a product gap, not a regression, and no floor was ever set for it.
	//
	// Interleaved, the second reads as the first, and the usual response to a
	// wall of red is to lower a number. So the report says which is which before
	// the numbers, not after.
	renderSection(&b, r, KindRegression, verbose,
		"REGRESSIONS — each traces to a defect this build actually produced.",
		"A rate below the floor means a promise that was fixed once has broken again.")
	renderSection(&b, r, KindCoverage, verbose,
		"COVERAGE — each is an industry the product offers in its selector.",
		"These have no floors: nothing has measured them before, and a floor invented "+
			"from one run is a target dressed as an observation. Read them as a "+
			"description of how well each domain is currently served.")

	if r.Met() {
		b.WriteString("Every scorer reached its floor. That is a measurement of this run, not a guarantee\n")
		b.WriteString("about the next one — the same prompt has produced a correct figure and a fabricated\n")
		b.WriteString("one four runs apart.\n")
	} else {
		b.WriteString("At least one scorer fell below its floor. The floors are observations rather than\n")
		b.WriteString("targets, so the question is what changed — the model, its version, or the prompt —\n")
		b.WriteString("and not which number to lower.\n")
	}
	return b.String()
}

// renderSection writes the cases of one kind, or nothing when there are none.
//
// A case with no kind is rendered as a regression, which is what every case was
// before coverage existed — an unlabelled case is not dropped from the report.
func renderSection(b *strings.Builder, r *Report, kind Kind, verbose bool, heading, note string) {
	var idx []int
	for i := range r.Cases {
		k := r.Cases[i].Case.Kind
		if k == "" {
			k = KindRegression
		}
		if k == kind {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return
	}
	fmt.Fprintf(b, "%s\n%s\n\n", heading, wrapText(note, 88, ""))

	for _, i := range idx {
		c := &r.Cases[i]
		if c.Case.Industry != "" {
			fmt.Fprintf(b, "%s   [%s]\n", c.Case.ID, c.Case.Industry)
		} else {
			fmt.Fprintf(b, "%s\n", c.Case.ID)
		}
		fmt.Fprintf(b, "  %s\n\n", wrapText(c.Case.Why, 74, "  "))

		if c.Errors > 0 {
			// Kept apart from the scores, always. A model that timed out is a
			// different finding from a model that answered badly, and merging
			// them lets an outage read as a regression.
			fmt.Fprintf(b, "  %d run(s) did not complete and were NOT scored:\n", c.Errors)
			for _, d := range c.ErrorDetail {
				fmt.Fprintf(b, "    %s\n", d)
			}
			b.WriteString("\n")
		}

		for j := range c.Scores {
			s := &c.Scores[j]
			status, bound := "  ok ", fmt.Sprintf("floor %.0f%%", s.Scorer.Floor*100)
			if s.Scorer.Tracked {
				// Printed as what it is. A tracked property with "floor 0%"
				// beside it reads as a requirement somebody broke, and the next
				// person deletes it.
				status, bound = "track", "tracked, not required"
			} else if !s.Met() {
				status = "BELOW"
			}
			fmt.Fprintf(b, "  %s  %-52s %d/%d  (%s)\n",
				status, s.Scorer.Name, s.Held, s.Runs, bound)
			fmt.Fprintf(b, "         %s\n", wrapText(s.Scorer.Asserts, 68, "         "))
			if !s.Met() || s.Scorer.Tracked || verbose {
				fmt.Fprintf(b, "         %s\n", wrapText(s.Scorer.FloorWhy, 68, "         "))
				for _, d := range s.Detail {
					fmt.Fprintf(b, "         %s\n", wrapText(d, 68, "           "))
				}
			}
			b.WriteString("\n")
		}
	}
}

// wrapText breaks a paragraph so a terminal does not have to.
func wrapText(s string, width int, indent string) string {
	var lines []string
	var line string
	for _, word := range strings.Fields(s) {
		if line == "" {
			line = word
			continue
		}
		if len(line)+1+len(word) > width {
			lines = append(lines, line)
			line = word
			continue
		}
		line += " " + word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"+indent)
}
