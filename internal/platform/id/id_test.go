package id

import (
	"strings"
	"testing"
	"time"
)

func TestFormat(t *testing.T) {
	s := New(PrefixTask)
	if !strings.HasPrefix(s, "tsk_") {
		t.Errorf("id %q lacks the tsk_ prefix", s)
	}
	if len(s) != len("tsk_")+26 {
		t.Errorf("id %q has length %d, want %d", s, len(s), len("tsk_")+26)
	}
	if !Valid(s, PrefixTask) {
		t.Errorf("Valid rejected a freshly generated id %q", s)
	}
}

// TestPrefixIsTypeSafety is the point of prefixing: passing a user id where a
// task id belongs must be rejectable before it reaches the database, so that
// "task not found" cannot silently mean "you passed the wrong kind of id".
func TestPrefixIsTypeSafety(t *testing.T) {
	user := New(PrefixUser)
	if Valid(user, PrefixTask) {
		t.Error("a user id was accepted as a task id")
	}
	if !Valid(user, PrefixUser) {
		t.Error("a user id was rejected as a user id")
	}
}

func TestRejectsMalformed(t *testing.T) {
	// Built from a known-good 26-character body so each case isolates exactly
	// one defect. An earlier version of this test hand-typed the bodies and got
	// the lengths wrong, so the "illegal letter" cases were being rejected for
	// being short and never exercised the alphabet check at all.
	const good = "0123456789ABCDEFGHJKMNPQRS" // 26 chars, all in the alphabet
	if len(good) != 26 {
		t.Fatalf("fixture body is %d chars, want 26; the cases below would test the wrong thing", len(good))
	}
	if !Valid("tsk_"+good, PrefixTask) {
		t.Fatal("fixture body is not itself valid; every case below would pass vacuously")
	}

	cases := map[string]string{
		"empty":             "",
		"prefix only":       "tsk_",
		"no underscore":     "tsk" + good,
		"wrong prefix":      "usr_" + good,
		"too short by one":  "tsk_" + good[:25],
		"too long by one":   "tsk_" + good + "T",
		"illegal letter I":  "tsk_I" + good[1:],
		"illegal letter L":  "tsk_L" + good[1:],
		"illegal letter O":  "tsk_O" + good[1:],
		"illegal letter U":  "tsk_U" + good[1:],
		"lowercase body":    "tsk_" + strings.ToLower(good),
		"separator in body": "tsk_" + good[:25] + "_",
	}
	for name, s := range cases {
		if Valid(s, PrefixTask) {
			t.Errorf("%s: Valid accepted %q (len %d)", name, s, len(s))
		}
	}
}

// TestTimeOrdering is why this is not a bare UUIDv4: the hottest query in the
// system is "most recent rows for this project", and a random primary key
// destroys index locality as the table grows.
func TestTimeOrdering(t *testing.T) {
	base := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	var prev string
	for i := 0; i < 200; i++ {
		cur := NewAt(PrefixTask, base.Add(time.Duration(i)*time.Millisecond))
		if prev != "" && cur <= prev {
			t.Fatalf("ids are not lexicographically increasing with time: %q followed %q", cur, prev)
		}
		prev = cur
	}
}

func TestUniquenessUnderContention(t *testing.T) {
	const n = 20000
	seen := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		s := New(PrefixEvent)
		if seen[s] {
			t.Fatalf("collision after %d ids: %q", i, s)
		}
		seen[s] = true
	}
}

// TestSameMillisecondStillUnique covers the case ordering tests miss: many ids
// minted inside one millisecond rely entirely on the 80 random bits.
func TestSameMillisecondStillUnique(t *testing.T) {
	at := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	seen := map[string]bool{}
	for i := 0; i < 5000; i++ {
		s := NewAt(PrefixJob, at)
		if seen[s] {
			t.Fatalf("collision within one millisecond after %d ids", i)
		}
		seen[s] = true
	}
}

func TestCrockfordAlphabetExcludesConfusables(t *testing.T) {
	// I/L/O/U are excluded so an id read aloud or retyped from a screenshot
	// cannot be corrupted by the usual confusions.
	for _, r := range "ILOU" {
		if strings.ContainsRune(crockford, r) {
			t.Errorf("alphabet contains confusable %q", r)
		}
	}
	if len(crockford) != 32 {
		t.Errorf("alphabet has %d symbols, want 32", len(crockford))
	}
}

func TestAllPrefixesDistinct(t *testing.T) {
	all := []Prefix{
		PrefixUser, PrefixSession, PrefixToken, PrefixProject, PrefixGoal,
		PrefixPlan, PrefixTask, PrefixRun, PrefixCheckpoint, PrefixJob,
		PrefixApproval, PrefixArtifact, PrefixEvidence, PrefixEvent,
		PrefixNode, PrefixEdge, PrefixVersion,
		PrefixMemory, PrefixDecision, PrefixToolCall, PrefixRequest,
		PrefixTrace, PrefixSpan,
	}
	seen := map[Prefix]bool{}
	for _, p := range all {
		if seen[p] {
			t.Errorf("prefix %q is used twice; two entity kinds would be indistinguishable in logs", p)
		}
		seen[p] = true
		if len(p) != 3 {
			t.Errorf("prefix %q should be 3 characters for column alignment in logs", p)
		}
	}
}
