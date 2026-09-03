package main

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// The first test in this binary to exercise its own argument parsing.
//
// # Why it exists
//
// Go's flag package stops at the first non-flag argument, so a command shaped
// `<id> --flag value` must take the id before parsing. Two commands in this
// binary shipped without doing that and refused for want of flags that were on
// the command line (docs/bugfix/2026-09-02-forgectl-memory-forget-ignored-its-flags.md).
// It was found by running the binary, because the suite called handlers and
// services and never forgectl itself.
//
// `geometry export` is the third command with that shape. This is the fence.
func TestParseExportArgs_FlagsAfterTheIDAreStillRead(t *testing.T) {
	got, err := parseExportArgs([]string{"ver_abc", "--format", "stl", "--out", "/tmp/x.stl", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if got.VersionID != "ver_abc" {
		t.Errorf("version id read as %q", got.VersionID)
	}
	if got.Format != "stl" {
		t.Errorf("--format after the id was ignored; format is %q", got.Format)
	}
	if got.Out != "/tmp/x.stl" {
		t.Errorf("--out after the id was ignored; out is %q", got.Out)
	}
	if !got.DryRun {
		t.Error("--dry-run after the id was ignored")
	}
}

// The default is the format most likely to carry the label, not the one most
// likely to be silently misread.
func TestParseExportArgs_DefaultsToOBJ(t *testing.T) {
	got, err := parseExportArgs([]string{"ver_abc"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Format != "obj" {
		t.Errorf("default format is %q", got.Format)
	}
	if got.DryRun {
		t.Error("--dry-run defaulted to true, which would make export write nothing")
	}
}

// A missing id, or flags where the id should be, must produce the usage line
// rather than an error about something else.
func TestParseExportArgs_RefusesWithoutAnID(t *testing.T) {
	for _, args := range [][]string{nil, {}, {"--format", "obj"}} {
		_, err := parseExportArgs(args)
		if errs.CodeOf(err) != errs.CodeValidationFailed {
			t.Fatalf("%v produced %v", args, err)
		}
		if !strings.Contains(err.Error(), "version id comes first") {
			t.Errorf("%v: the error does not say what to type: %v", args, err)
		}
	}
}

// Two ids is a mistake worth naming: somebody meaning to compare them would
// otherwise get one file and no indication the second was dropped.
func TestParseExportArgs_RefusesASecondID(t *testing.T) {
	_, err := parseExportArgs([]string{"ver_a", "ver_b"})
	if err == nil {
		t.Fatal("a second version id was silently discarded")
	}
	if !strings.Contains(err.Error(), "compare") {
		t.Errorf("the error does not point at the command they probably wanted: %v", err)
	}
}
