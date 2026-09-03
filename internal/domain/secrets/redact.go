package secrets

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
)

// Redaction is the other half of SEC-03.
//
// # Why this is not optional hardening
//
// Substituting a handle for a value on the way INTO a tool is worthless on its
// own, because the tool's output goes back to the model. `sh -c 'echo $TOKEN'`,
// an HTTP client that logs the request it sent, a library error quoting the
// header it choked on — each of them puts the value in the tool result, and from
// there it reaches the context window, the next tool call, the transcript and
// the ledger.
//
// So every value the broker resolves is scrubbed out of the tool's output before
// anything else sees it.
//
// # What is scrubbed, and why more than the literal value
//
// A value that made a round trip is often not byte-identical to the one that went
// in. A shell may have escaped it, an HTTP client may have percent-encoded it,
// something may have base64'd it into an Authorization header, a JSON encoder may
// have escaped its quotes. Each of those is the same credential and none of them
// matches a naive strings.Replace.
//
// So the redactor matches a small, fixed set of encodings of each value. It is a
// closed list rather than a clever matcher: a heuristic that tried to catch
// "anything that looks like the secret" would also catch things that are not,
// and a redactor with false positives quietly destroys tool output.
//
// # What is honestly claimed
//
// This catches a value that passed through a common encoding. It does not catch
// one a tool deliberately obfuscated — reversed, split across fields, encrypted.
// A tool that WANTS to exfiltrate a credential it was legitimately handed can.
// That is an egress problem (PRD SEC-05) and is not solved here.

// Mask is what a redacted value is replaced with.
//
// It names the handle rather than being a row of asterisks, so a reader of the
// output — model or human — can tell WHICH secret was there. "[redacted]" alone
// turns a debuggable message into a mystery.
func Mask(name string) string { return "[redacted " + HandlePrefix + name + "]" }

// Redactor removes resolved secret values from text.
//
// The zero value is usable and redacts nothing, which is the correct behaviour
// for a tool call that resolved no secrets.
type Redactor struct {
	// replacements maps an encoding of a value to its mask. Built once per tool
	// call; a call resolves at most a handful of secrets.
	replacements []replacement
}

type replacement struct {
	from string
	to   string
}

// NewRedactor builds a redactor for a set of resolved values, keyed by handle
// name.
//
// Values shorter than minRedactableLength are skipped. A one- or two-character
// secret would match constantly in ordinary output and turn every tool result
// into holes; such a value is not a credential worth protecting, and destroying
// legitimate output to pretend otherwise helps nobody. The skip is deliberate
// and is reported by SkippedShort so a caller can warn about it.
func NewRedactor(values map[string]string) *Redactor {
	r := &Redactor{}
	for name, value := range values {
		if len(value) < minRedactableLength {
			continue
		}
		mask := Mask(name)
		for _, encoded := range encodings(value) {
			r.replacements = append(r.replacements, replacement{from: encoded, to: mask})
		}
	}
	// Longest first, so a value that contains another value is masked as itself
	// rather than being half-replaced from the inside out.
	sort.SliceStable(r.replacements, func(i, j int) bool {
		return len(r.replacements[i].from) > len(r.replacements[j].from)
	})
	return r
}

// minRedactableLength is the shortest value worth scrubbing. Four characters is
// short enough to appear by chance in ordinary output.
const minRedactableLength = 5

// SkippedShort returns the handle names whose values were too short to redact
// safely. A caller should refuse or warn rather than proceed quietly: a secret
// that cannot be scrubbed is one the model will eventually see.
func SkippedShort(values map[string]string) []string {
	var out []string
	for name, value := range values {
		if len(value) < minRedactableLength {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// encodings returns the forms of a value that a tool might return.
//
// Deliberately a short, closed list. Every entry is a transformation something in
// the path actually applies:
//
//   - the value itself
//   - percent-encoded, by an HTTP client building a URL or form body
//   - JSON-escaped, by anything that put it in a JSON body and echoed it back
//   - base64, by an Authorization header or an encoded payload
//   - base64 of "user:value", the Basic-auth shape
//
// Not included, on purpose: reversal, hex, split-across-lines. Those are what a
// tool does when it is trying to get a value past a redactor, and a redactor
// that chases them is competing with an attacker inside its own process — which
// it cannot win, and pretending otherwise is worse than saying so.
func encodings(value string) []string {
	out := []string{value}

	if enc := url.QueryEscape(value); enc != value {
		out = append(out, enc)
	}
	if enc, err := json.Marshal(value); err == nil {
		// Trim the surrounding quotes: what appears inside a larger JSON
		// document is the escaped body, not the quoted string.
		if s := string(enc); len(s) >= 2 {
			if inner := s[1 : len(s)-1]; inner != value {
				out = append(out, inner)
			}
		}
	}
	out = append(out, base64.StdEncoding.EncodeToString([]byte(value)))
	// Basic auth: base64("anything:value") varies with the username, so only the
	// most common shape is covered — an empty user, which is what token-as-password
	// clients send.
	out = append(out, base64.StdEncoding.EncodeToString([]byte(":"+value)))
	return out
}

// Redact returns s with every known encoding of every value replaced.
func (r *Redactor) Redact(s string) string {
	if r == nil || len(r.replacements) == 0 || s == "" {
		return s
	}
	for _, rep := range r.replacements {
		s = strings.ReplaceAll(s, rep.from, rep.to)
	}
	return s
}

// RedactJSON redacts inside a JSON document, preserving its shape.
//
// Operating on the raw bytes rather than decoding and re-encoding: a tool's
// output is handed to the model as-is, and round-tripping it through a decoder
// would reorder keys and reformat numbers, which changes what the model reads for
// reasons that have nothing to do with secrets.
func (r *Redactor) RedactJSON(b []byte) []byte {
	if r == nil || len(r.replacements) == 0 || len(b) == 0 {
		return b
	}
	return []byte(r.Redact(string(b)))
}

// Leaks reports which handle names still appear in s in resolved form.
//
// For fences and for a defence-in-depth check after redaction: if this ever
// returns anything on already-redacted text, the redactor missed an encoding and
// the right response is to fail the tool call rather than pass the value on.
func (r *Redactor) Leaks(s string, values map[string]string) []string {
	var out []string
	for name, value := range values {
		if len(value) >= minRedactableLength && strings.Contains(s, value) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
