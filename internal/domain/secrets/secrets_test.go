package secrets

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Handles are found wherever a tool takes a string, because that is where a
// credential goes: inside a header, inside a URL, inside a command line.
func TestFindHandles_ScansAnywhereInAnArgument(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []string
	}{
		{`{"token":"secret://github_token"}`, []string{"github_token"}},
		{`{"header":"Authorization: Bearer secret://gh_pat"}`, []string{"gh_pat"}},
		{`{"url":"https://x/?k=secret://api_key&z=1"}`, []string{"api_key"}},
		{`{"cmd":"curl -H 'X: secret://a' -H 'Y: secret://b'"}`, []string{"a", "b"}},
		{`{"a":"secret://dup","b":"secret://dup"}`, []string{"dup"}},
		{`{"nothing":"here"}`, nil},
		// A handle followed by a path separator: the name ends at the slash,
		// which is what somebody composing a URL from a handle would mean.
		{`{"u":"secret://tok/extra"}`, []string{"tok"}},
		// A bare prefix with no name is not a handle.
		{`{"u":"secret://"}`, nil},
	} {
		got := FindHandles(tc.in)
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Fatalf("FindHandles(%q) = %v; want %v", tc.in, got, tc.want)
		}
	}
}

// A name that differs only by case would be two handles the model cannot tell
// apart and one secret somebody thinks they revoked.
func TestValidateName_IsNarrowOnPurpose(t *testing.T) {
	for _, bad := range []string{"", "GitHub", "has space", "has-dash", "_leading", "has.dot",
		strings.Repeat("x", 64)} {
		if err := ValidateName(bad); err == nil {
			t.Fatalf("%q was accepted as a secret name", bad)
		}
	}
	for _, good := range []string{"a", "github_token", "key1", "9lives"} {
		if err := ValidateName(good); err != nil {
			t.Fatalf("%q was refused: %v", good, err)
		}
	}
}

// Deny by default. A secret somebody declared and has not thought about yet is
// readable by nothing.
func TestGrantedTo_DeniesByDefaultAndAfterRevocation(t *testing.T) {
	s := &Secret{Name: "tok"}
	if s.GrantedTo("http_get") {
		t.Fatal("a secret with no grants was readable by a tool")
	}
	s.Tools = []string{"http_get"}
	if !s.GrantedTo("http_get") {
		t.Fatal("a granted tool was refused")
	}
	if s.GrantedTo("shell") {
		t.Fatal("a grant to one tool leaked to another")
	}
	// Exact match, not a prefix: "http_get" must not imply "http_get_admin".
	if s.GrantedTo("http_get_admin") {
		t.Fatal("a grant matched by prefix; a permission broad enough to cover a class covers the wrong member of it")
	}

	now := mustTime()
	s.RevokedAt = &now
	if s.GrantedTo("http_get") {
		t.Fatal("a revoked secret was still readable by a tool it had been granted to")
	}
}

// ---------------------------------------------------------------------------
// Redaction — the half the mechanism is worthless without
// ---------------------------------------------------------------------------

// The failure this exists to prevent: the tool has the value, and its output is
// about to become the model's context.
func TestRedactor_RemovesTheValueFromToolOutput(t *testing.T) {
	// Not a real provider's token prefix — see the note in
	// internal/agent/secrets_test.go. The redactor replaces the literal it was
	// given; the shape bought nothing and kept a secret scanner permanently red.
	const value = "FIXTURE-not-a-real-credential-3b90ae12"
	r := NewRedactor(map[string]string{"gh": value})

	out := r.Redact(`{"stdout":"Authorization: Bearer ` + value + `\n"}`)
	if strings.Contains(out, value) {
		t.Fatalf("the value survived redaction: %s", out)
	}
	if !strings.Contains(out, "secret://gh") {
		t.Fatalf("the mask does not name the handle, so a reader cannot tell what was there: %s", out)
	}
}

// A value that made a round trip is often not byte-identical to the one that
// went in. Each of these is something in the path actually applies.
func TestRedactor_CatchesTheEncodingsAValueComesBackIn(t *testing.T) {
	const value = "s3cret-Value_With/Slashes+And=Signs"
	r := NewRedactor(map[string]string{"tok": value})

	jsonEscaped, _ := json.Marshal(value)
	for name, encoded := range map[string]string{
		"literal":         value,
		"percent-encoded": url.QueryEscape(value),
		"json-escaped":    string(jsonEscaped[1 : len(jsonEscaped)-1]),
		"base64":          base64.StdEncoding.EncodeToString([]byte(value)),
		"basic auth":      base64.StdEncoding.EncodeToString([]byte(":" + value)),
	} {
		out := r.Redact("prefix " + encoded + " suffix")
		if strings.Contains(out, encoded) {
			t.Fatalf("a %s value survived redaction: %s", name, out)
		}
	}
}

// A value that contains another value must be masked as itself rather than
// half-replaced from the inside out.
func TestRedactor_LongestMatchWins(t *testing.T) {
	r := NewRedactor(map[string]string{
		"short": "abcdef",
		"long":  "abcdefghijkl",
	})
	out := r.Redact("value=abcdefghijkl")
	if strings.Contains(out, "abcdef") {
		t.Fatalf("part of a value survived: %s", out)
	}
	if !strings.Contains(out, "secret://long") {
		t.Fatalf("the longer value was not recognised as itself: %s", out)
	}
}

// A one- or two-character value would match constantly and turn every tool
// result into holes. Skipping is right; skipping silently is not.
func TestRedactor_ShortValuesAreSkippedAndReported(t *testing.T) {
	values := map[string]string{"tiny": "ab", "real": "long-enough-value"}
	r := NewRedactor(values)

	out := r.Redact("the cab was ab and the token is long-enough-value")
	if strings.Contains(out, "long-enough-value") {
		t.Fatal("a redactable value survived")
	}
	if !strings.Contains(out, "cab") {
		t.Fatalf("a two-character value was redacted and destroyed ordinary text: %s", out)
	}
	skipped := SkippedShort(values)
	if len(skipped) != 1 || skipped[0] != "tiny" {
		t.Fatalf("the unredactable handle was not reported: %v", skipped)
	}
}

// The defence-in-depth check. If this ever fires on already-redacted text, an
// encoding was missed and the caller must lose the result rather than pass the
// value on.
func TestRedactor_LeaksReportsWhatSurvived(t *testing.T) {
	values := map[string]string{"tok": "a-real-value-here"}
	r := NewRedactor(values)

	if leaks := r.Leaks(r.Redact("x a-real-value-here y"), values); len(leaks) != 0 {
		t.Fatalf("Leaks reported %v on redacted text", leaks)
	}
	// An encoding nothing covers, standing in for one the redactor missed.
	reversed := "ereh-eulav-laer-a"
	if leaks := r.Leaks(reversed, values); len(leaks) != 0 {
		t.Fatalf("Leaks matched a reversed value; it must report only what it can actually see: %v", leaks)
	}
	if leaks := r.Leaks("raw a-real-value-here", values); len(leaks) != 1 {
		t.Fatalf("Leaks missed a surviving literal value: %v", leaks)
	}
}

// The zero value redacts nothing, which is the correct behaviour for a call
// that resolved no secrets — and it must not panic.
func TestRedactor_ZeroValueIsUsable(t *testing.T) {
	var r *Redactor
	if got := r.Redact("untouched"); got != "untouched" {
		t.Fatalf("a nil redactor changed its input: %q", got)
	}
	empty := NewRedactor(nil)
	if got := empty.Redact("untouched"); got != "untouched" {
		t.Fatalf("an empty redactor changed its input: %q", got)
	}
}

// The struct that reaches a prompt has no field that could hold a value. This
// is asserted rather than left to inspection, because the failure mode is
// somebody adding one later for a good-sounding reason.
func TestAvailable_HasNowhereToPutAValue(t *testing.T) {
	blob, err := json.Marshal(Available{
		Handle: "secret://tok", Description: "for the API", Tools: []string{"http_get"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(blob, &fields); err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"value", "secret", "token", "env_var", "password"} {
		if _, present := fields[banned]; present {
			t.Fatalf("Available serialises a %q field, and this struct goes into a prompt", banned)
		}
	}
	if len(fields) != 3 {
		t.Fatalf("Available has %d serialised fields; it should carry a handle, a purpose and the tools that may use it: %v",
			len(fields), fields)
	}
}

// Substitution is the way in; it must replace every occurrence, not the first.
func TestResolution_SubstitutesEveryOccurrence(t *testing.T) {
	r := &Resolution{Values: map[string]string{"tok": "VALUE"}}
	got := r.Substitute(`{"a":"secret://tok","b":"x secret://tok y"}`)
	if strings.Contains(got, HandlePrefix) {
		t.Fatalf("a handle survived substitution: %s", got)
	}
	if strings.Count(got, "VALUE") != 2 {
		t.Fatalf("not every occurrence was substituted: %s", got)
	}
}

func mustTime() time.Time { return time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC) }
