package main

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// printStructure prints how the variants' trees differ (Phase 7, stage E2), and
// nothing at all for flat documents, whose output is unchanged.
//
// One line per definition with how many times each variant places it, and never
// a line per placed part: a wheel placed four times that changed is one line.
// Assemblies list only the children, interfaces and features that differ — an
// assembly of two hundred unchanged children is otherwise two hundred lines
// saying so.
func printStructure(out io.Writer, s *geometry.Structure) error {
	if s == nil {
		return nil
	}
	mark := func(differs bool) string {
		if differs {
			return "≠ "
		}
		return "  "
	}
	fmt.Fprintln(out, "\nSTRUCTURE")
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	line := mark(s.Root.Differs) + "root"
	for _, v := range s.Root.Values {
		line += "\t" + v
	}
	fmt.Fprintln(w, line+"\t")
	for _, d := range s.Definitions {
		line := mark(d.Differs()) + "definition " + d.Label
		for i, n := range d.Occurrences {
			if contains(d.MissingFrom, i+1) {
				line += "\tABSENT"
				continue
			}
			line += fmt.Sprintf("\tplaced %d×", n)
		}
		if len(d.Changed) > 0 {
			line += "\tchanged: " + strings.Join(d.Changed, ", ")
		}
		fmt.Fprintln(w, line+"\t")
	}
	if err := w.Flush(); err != nil {
		return err
	}

	for _, a := range s.Assemblies {
		fmt.Fprintf(out, "%sassembly %s%s\n", mark(a.Differs()), a.Label, describeMember(a.MissingFrom, a.Changed))
		for _, kind := range []struct {
			name string
			rows []geometry.MemberRow
		}{{"child", a.Children}, {"interface", a.Interfaces}, {"feature", a.Features}} {
			for _, m := range kind.rows {
				if m.Differs() {
					fmt.Fprintf(out, "    %s %s%s\n", kind.name, m.ID, describeMember(m.MissingFrom, m.Changed))
				}
			}
		}
	}
	return nil
}

// describeMember is ": not in column 2; changed: position", or "" when neither.
func describeMember(missing []int, changed []string) string {
	var bits []string
	if len(missing) > 0 {
		cols := make([]string, 0, len(missing))
		for _, c := range missing {
			cols = append(cols, fmt.Sprint(c))
		}
		bits = append(bits, "not in column "+strings.Join(cols, ", "))
	}
	if len(changed) > 0 {
		bits = append(bits, "changed: "+strings.Join(changed, ", "))
	}
	if len(bits) == 0 {
		return ""
	}
	return ": " + strings.Join(bits, "; ")
}

func contains(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
