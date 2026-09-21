// Package buildinfo carries the version a running binary was built from.
//
// # Why this exists
//
// Production runs an image, and the one question asked of a deployment in every
// incident is "which build is this?". Until now the answer existed only in the
// three `main` packages' link-time variables, which the image never set: the
// Dockerfile built with `-ldflags="-s -w"` and nothing else, so every container
// ever deployed reported version "dev", commit "unknown". "dev" is worse than
// nothing, because it reads like an answer.
//
// So the value lives in one place, every surface that reports a version reads it
// from here, and a build that did not stamp itself says UNKNOWN — the same word
// everywhere, and never a plausible-looking wrong answer. The deployment side of
// this is deploy/Dockerfile's FORGE_VERSION / FORGE_COMMIT / FORGE_BUILD_DATE
// build arguments, which `make image` fills in from git.
package buildinfo

import (
	"fmt"
	"strings"
)

// Unknown is what a field reads when the build did not stamp it.
//
// One word, used by every field and every surface, so "is this stamped?" is a
// string comparison anybody can make and not a set of per-field placeholders.
const Unknown = "unknown"

// Info is what a binary knows about where it came from.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"built"`
}

var current = Info{Version: Unknown, Commit: Unknown, Date: Unknown}

// Set records what the linker wrote into a main package's variables.
//
// Called once, from main, before anything logs or serves. It is not safe to call
// concurrently with Get and is not meant to be: a binary's version does not
// change while it runs.
func Set(version, commit, date string) {
	current = Info{Version: stated(version), Commit: stated(commit), Date: stated(date)}
}

// Get is what this binary was built from.
func Get() Info { return current }

// stated keeps a value that says something and turns everything else into Unknown.
//
// The placeholders are the words an unstamped Go build leaves behind: the empty
// string when -X names a variable that no longer exists, "dev" and "unknown"
// from the defaults in the three mains, and "(devel)" from runtime/debug. None of
// them identify a build, and each of them has at some point been read as if it
// did.
func stated(s string) string {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "", Unknown, "dev", "devel", "(devel)", "none", "null":
		return Unknown
	}
	return s
}

// Stamped reports whether the build said anything about itself at all.
//
// True when the version OR the commit is stated: a build stamped with a commit
// and no tag is still answerable, and that is the usual case for an image built
// from a branch.
func (i Info) Stamped() bool { return i.Version != Unknown || i.Commit != Unknown }

// String is the one line every surface prints, so an operator reading a log, a
// CLI and an HTTP response compares three copies of the same sentence.
func (i Info) String() string {
	return fmt.Sprintf("%s (commit %s, built %s)", i.Version, i.Commit, i.Date)
}
