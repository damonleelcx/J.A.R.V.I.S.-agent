package cad_test

import (
	"testing"
)

// What testdata/release_memory.py reports.
type releaseMemory struct {
	Platform  string `json:"platform"`
	Glibc     bool   `json:"glibc"`
	Replies   int    `json:"replies"`
	RepliesOK []bool `json:"replies_ok"`
	Calls     []struct {
		RepliesWritten     int  `json:"replies_written"`
		LastReplyStillHeld bool `json:"last_reply_still_held"`
		Ran                bool `json:"ran"`
	} `json:"calls"`
	StepsWritten          int  `json:"steps_written"`
	DocumentsBefore       int  `json:"documents_before"`
	DocumentsAfter        int  `json:"documents_after"`
	TrimFound             bool `json:"trim_found"`
	ReleaseRan            bool `json:"release_ran"`
	ReleaseRanSwitchedOff bool `json:"release_ran_switched_off"`
}

// After every reply — a build, a refusal of an unreadable line, two STEP exports — the
// kernel's loop drops the request and reply and asks glibc to hand its free pages
// back (malloc_trim(0)), after the reply is written, never before. On Linux with glibc
// that call is really made; anywhere else (this laptop's Windows, musl, macOS) there
// is no malloc_trim and the release does nothing and raises nothing. Measured on the
// worker's image, it takes the kernel from ~1.2 GiB to ~0.5 GiB after a 90,880-occurrence
// STEP export (docs/spikes/2026-09-17-kernel-last-walls).
func TestKernel_TheKernelHandsFreeMemoryBackAfterEachReply(t *testing.T) {
	var got releaseMemory
	testdataJSON(t, "release_memory.py", &got)
	t.Logf("%s (glibc %v): %d replies %v, release calls %+v; malloc_trim found %v, ran %v",
		got.Platform, got.Glibc, got.Replies, got.RepliesOK, got.Calls, got.TrimFound, got.ReleaseRan)
	if got.Replies != 4 || len(got.RepliesOK) != 4 || !got.RepliesOK[0] || got.RepliesOK[1] || !got.RepliesOK[2] ||
		!got.RepliesOK[3] || got.StepsWritten != 2 {
		t.Fatalf("replies %v with %d STEP file(s); want a build, a refusal and two exports", got.RepliesOK, got.StepsWritten)
	}
	if len(got.Calls) != 4 {
		t.Fatalf("memory released %d time(s) for 4 replies; once after each", len(got.Calls))
	}
	for i, c := range got.Calls {
		if c.RepliesWritten != i+1 {
			t.Errorf("release %d ran with %d replies written; after reply %d is written, not before", i+1, c.RepliesWritten, i+1)
		}
		if c.LastReplyStillHeld {
			t.Errorf("release %d ran while the loop still held its reply: that memory cannot be handed back", i+1)
		}
		if c.Ran != got.Glibc {
			t.Errorf("release %d ran = %v on %s (glibc %v); malloc_trim runs exactly where glibc has it", i+1, c.Ran, got.Platform, got.Glibc)
		}
	}
	if got.TrimFound != got.Glibc || got.ReleaseRan != got.Glibc {
		t.Errorf("malloc_trim found %v, release ran %v, on %s with glibc %v", got.TrimFound, got.ReleaseRan, got.Platform, got.Glibc)
	}
	// Found 2026-09-17: every export left an empty document open in the XDE
	// application for the life of the process (see _step_document).
	if got.DocumentsAfter != got.DocumentsBefore {
		t.Errorf("the XDE application held %d document(s) before two exports and %d after: an export leaves its document open",
			got.DocumentsBefore, got.DocumentsAfter)
	}
	if got.ReleaseRanSwitchedOff {
		t.Error("_RELEASE_AFTER_REPLY = False still released memory")
	}
}
