package main

import (
	"strings"
	"testing"
)

// The fences the carried defect asked for.
//
// `memory forget` and `memory purge` both shipped ignoring the flags they then
// demanded, because Go's flag package stops at the first non-flag argument
// (docs/bugfix/2026-09-02-forgectl-memory-forget-ignored-its-flags.md). Both
// were fixed by hand and nothing checked that they stayed fixed — the shape was
// held by convention. It is held by these now.

func TestParseForgetArgs_FlagsAfterTheIDAreStillRead(t *testing.T) {
	got, err := parseForgetArgs([]string{"mem_abc", "--as", "usr_1", "--reason", "measured wrong"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ItemID != "mem_abc" {
		t.Errorf("item id read as %q", got.ItemID)
	}
	if got.As != "usr_1" {
		t.Errorf("--as after the id was ignored; this is the exact bug that shipped: %q", got.As)
	}
	if got.Reason != "measured wrong" {
		t.Errorf("--reason after the id was ignored: %q", got.Reason)
	}
}

// A deletion has to name who asked, and the refusal must name the flag — the
// shipped bug's worst property was an error naming the option that HAD been
// given, which sends the reader to inspect their own typing.
func TestParseForgetArgs_RefusesWithoutAnAsker(t *testing.T) {
	if _, err := parseForgetArgs([]string{"mem_abc"}); err == nil {
		t.Fatal("an unattributed deletion was accepted")
	} else if !strings.Contains(err.Error(), "--as") {
		t.Errorf("the error does not name the missing flag: %v", err)
	}
}

func TestParseForgetArgs_RefusesWithoutAnID(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"--as", "usr_1"}} {
		_, err := parseForgetArgs(args)
		if err == nil {
			t.Fatalf("%v was accepted with no item id", args)
		}
		if !strings.Contains(err.Error(), "item id comes first") {
			t.Errorf("%v: the error does not say what to type: %v", args, err)
		}
	}
}

// `memory purge` is the one that undoes a user's deletion record, so its
// --dry-run is the flag somebody reaches for before doing something they cannot
// take back. It shipped being ignored.
func TestParsePurgeArgs_DryRunAfterTheIDIsStillRead(t *testing.T) {
	got, err := parsePurgeArgs([]string{"mem_abc", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if got.ItemID != "mem_abc" {
		t.Errorf("item id read as %q", got.ItemID)
	}
	if !got.DryRun {
		t.Fatal("--dry-run after the id was ignored, so a command asked to change nothing would purge")
	}
}

// And it must default to acting, not to pretending. A --dry-run that defaulted
// on would make the command silently do nothing.
func TestParsePurgeArgs_DefaultsToActing(t *testing.T) {
	got, err := parsePurgeArgs([]string{"mem_abc"})
	if err != nil {
		t.Fatal(err)
	}
	if got.DryRun {
		t.Fatal("purge defaulted to a dry run, so the command would report success and change nothing")
	}
}

func TestParsePurgeArgs_RefusesWithoutAnID(t *testing.T) {
	if _, err := parsePurgeArgs([]string{"--dry-run"}); err == nil {
		t.Fatal("a purge with no item id was accepted")
	}
}
