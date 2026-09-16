package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// Which gate refused a build step, and why.
//
// # The problem this solves
//
// Two live car builds on 2026-09-15 lost their first step — one lost its first
// two — and every one of them said "produced no geometry". Offline, that one
// sentence turned out to be what SEVEN different refusals said: a reply whose JSON
// did not parse (a comment, an unquoted expression, a string where a number goes),
// an edit sent to a model that did not exist yet, a tree with no root, and a reply
// that only asked to be built in passes. A step "left out: it would have broken
// the model" did not say which faults it added. Neither run kept the replies, so
// nothing could be diagnosed and the next run would have been paid for blind.
//
// docs/bugfix/2026-09-15-a-failed-build-step-said-no-geometry-whatever-refused-it.md
//
// # Why the note carries the detail and not a log
//
// The note is the one thing every consumer of a build already reads: the stream a
// person watches, the build goal's step result, and the live measurement. A log
// line would reach none of them.
//
// # Why bounded
//
// A reply that does not parse can be sixteen thousand tokens, and a tree can add
// hundreds of faults at once. The note says which gate, the first thing wrong and
// how many more, and never grows with the model.
const (
	gateUnreadable    = "came back unreadable"
	gateNoGeometry    = "sent no geometry"
	gateEditRefused   = "sent an edit that could not be applied"
	gatePlacesNothing = "produced a model that places nothing"
	gateBroke         = "was left out: it would have broken the model"
)

// maxStepNoteDetail bounds what one refusal says after its gate, in characters.
const maxStepNoteDetail = 700

// maxAddedFaultsNamed is how many added faults a refusal names before counting the rest.
const maxAddedFaultsNamed = 3

// stepGates maps each gate's phrase to the short name a measurement reports.
var stepGates = []struct{ phrase, name string }{
	{gateUnreadable, "unreadable"},
	{gateNoGeometry, "no-geometry"},
	{gateEditRefused, "edit-refused"},
	{gatePlacesNothing, "places-nothing"},
	{gateBroke, "faults-added"},
	{"could not be built", "call-failed"},
	{"was not built", "context-ceiling"},
}

// StepGateOf names the gate a build step's note says refused it, or "" for a note
// that is not a refusal (a step that was built and corrected says what it
// corrected instead). Exported for the live measurement, which reports the gate of
// every step that lost its work.
func StepGateOf(note string) string {
	for _, g := range stepGates {
		if strings.Contains(note, ") "+g.phrase) {
			return g.name
		}
	}
	return ""
}

// stepNote is one refusal: the step, the gate and why, bounded.
func stepNote(n int, name, gate, detail string) string {
	detail = strings.TrimRight(clipRunes(strings.TrimSpace(detail), maxStepNoteDetail), ".")
	if detail == "" {
		return fmt.Sprintf("Step %d (%s) %s.", n, name, gate)
	}
	return fmt.Sprintf("Step %d (%s) %s: %s.", n, name, gate, detail)
}

// unreadableDetail says why a reply's JSON could not be read, or "" when it can be.
//
// Asked the way parseReply reads: strictly first, then with the dimensions read
// out of numeric slots. A reply either of those reads is not unreadable.
func unreadableDetail(resp *llm.Response) string {
	if resp == nil {
		return "there was no reply"
	}
	body := []byte(extractJSON(resp.Content))
	var probe Reply
	err := json.Unmarshal(body, &probe)
	if err == nil {
		return ""
	}
	if repaired, moved := repairDimensions(body); moved && json.Unmarshal(repaired, &probe) == nil {
		return ""
	}
	why := "its JSON does not parse: " + jsonProblem(body, err)
	if resp.Truncated() {
		why = fmt.Sprintf("it was cut off at the reply limit after %d characters, so %s", len(resp.Content), why)
	}
	return why
}

// jsonProblem is the decoder's complaint with the text around where it stopped.
func jsonProblem(body []byte, err error) string {
	var syntax *json.SyntaxError
	var typed *json.UnmarshalTypeError
	switch {
	case errors.As(err, &syntax):
		return fmt.Sprintf("%v, at character %d: %s", err, syntax.Offset, excerptAt(body, syntax.Offset))
	case errors.As(err, &typed):
		return fmt.Sprintf("%q is a JSON %s where a %v was expected, at character %d: %s",
			typed.Field, typed.Value, typed.Type, typed.Offset, excerptAt(body, typed.Offset))
	}
	return clipRunes(err.Error(), 200)
}

// excerptAt is a short, one-line piece of body ending just after offset.
func excerptAt(body []byte, offset int64) string {
	const before, after = 60, 20
	start, end := int(offset)-before, int(offset)+after
	if start < 0 {
		start = 0
	}
	if end > len(body) {
		end = len(body)
	}
	if start > end {
		start = end
	}
	s := strings.ToValidUTF8(string(body[start:end]), "")
	s = strings.Join(strings.Fields(s), " ")
	return "…" + s + "…"
}

// noGeometryDetail says what a reply with neither a prototype nor an edit did send.
func noGeometryDetail(reply Reply) string {
	why := `its reply had neither a "prototype" nor a "prototype_edit"`
	if reply.BuildInPasses {
		why = `it asked to be built in passes ("build_in_passes") instead of building this step`
	}
	if s := strings.TrimSpace(reply.Speech); s != "" {
		why += fmt.Sprintf("; it said %q", clipRunes(s, 160))
	}
	return why
}

// placesNothingDetail says why a document that arrived settled to nothing.
func placesNothingDetail(d *Prototype) string {
	if d == nil {
		return "there was no model"
	}
	if len(d.Definitions) > 0 || len(d.Assemblies) > 0 {
		return fmt.Sprintf(`it wrote %d definition(s) and %d assembly(ies) and no "root", so none of them is placed`,
			len(d.Definitions), len(d.Assemblies))
	}
	return "it has no parts and no tree"
}

// addedFaults names the faults a step's model has that the model before it did
// not, the first few by name and the rest by count.
//
// By sentence, as a multiset: a step that moves a fault from one part to another
// has added one and removed one, and a step that repeats an existing fault on a
// second occurrence has added one.
func addedFaults(before, after []geometry.Problem) string {
	had := map[string]int{}
	for _, f := range before {
		had[f.Name+"\x00"+f.Detail]++
	}
	var added []geometry.Problem
	for _, f := range after {
		key := f.Name + "\x00" + f.Detail
		if had[key] > 0 {
			had[key]--
			continue
		}
		added = append(added, f)
	}
	head := fmt.Sprintf("it had %d fault(s) where the model before it had %d", len(after), len(before))
	if len(added) == 0 {
		return head
	}
	named := make([]string, 0, maxAddedFaultsNamed)
	for i, f := range added {
		if i == maxAddedFaultsNamed {
			break
		}
		named = append(named, clipRunes(strings.TrimSpace(f.Name+" "+f.Detail), 200))
	}
	out := head + ", and these are new: " + strings.Join(named, "; ")
	if more := len(added) - len(named); more > 0 {
		out += fmt.Sprintf("; and %d more", more)
	}
	return out
}
