package deploy

import (
	"strings"
	"testing"
)

// ‼️ The image stamps the commit it was built from (2026-09-20).
//
// Until now deploy/Dockerfile built with `-ldflags="-s -w"` and nothing else, so
// every container FORGE has ever run reported version "dev", commit "unknown",
// and the first question of any incident — which build is answering? — could
// only be answered from outside the system, by trusting that the tag on the
// image is the tag that was built. The three binaries already read
// `main.version` / `main.commit` / `main.date`; nothing passed them.
//
// This holds the plumbing, which is the half a unit test cannot reach: an image
// is not built in `go test`, and a build argument that is dropped — a typo in an
// ARG name, a stage that never declared it, a RUN that drifts back to the plain
// ldflags — leaves a green build and an unstamped image. `make image` reads the
// stamp back out of the image afterwards; this refuses the change that would
// make that read say "unknown".
func TestDockerfile_StampsTheBuildIntoAllThreeBinaries(t *testing.T) {
	df := read(t, "Dockerfile")

	// 1. Declared, and defaulting to "unknown". A default of "dev", or of a
	//    version literal, is how an unstamped image starts claiming to be
	//    something. The builder stage and the runtime stage each need their own
	//    declaration: ARGs do not cross a FROM.
	for _, arg := range []string{"FORGE_VERSION", "FORGE_COMMIT", "FORGE_BUILD_DATE"} {
		decl := "ARG " + arg + "=unknown"
		if n := strings.Count(df, decl); n != 2 {
			t.Errorf("Dockerfile declares %q %d time(s), want 2 (the builder stage and the runtime stage)", decl, n)
		}
	}

	// 2. Every binary is linked with all three. Checked per binary, because the
	//    three go builds are three separate commands and one of them losing the
	//    flags is exactly the failure that would go unnoticed: forged would
	//    report a version and forgectl, the thing an operator runs to ask,
	//    would not.
	for _, bin := range []string{"forged", "forge-worker", "forgectl"} {
		var line string
		for _, l := range strings.Split(df, "\n") {
			if strings.Contains(l, "-o /out/"+bin+" ") || strings.Contains(l, "-o /out/"+bin+"\t") ||
				strings.Contains(l, "-o /out/"+bin+"  ") {
				line = l
			}
		}
		if line == "" {
			t.Errorf("no build line for %s", bin)
			continue
		}
		if !strings.Contains(line, `-ldflags="$LD"`) {
			t.Errorf("%s is built with %q, which does not carry the version flags", bin, strings.TrimSpace(line))
		}
	}
	for _, flag := range []string{
		"-X main.version=${FORGE_VERSION}",
		"-X main.commit=${FORGE_COMMIT}",
		"-X main.date=${FORGE_BUILD_DATE}",
	} {
		if !strings.Contains(df, flag) {
			t.Errorf("the Dockerfile never passes %q, so that field stays unknown in the image", flag)
		}
	}

	// 3. The old unstamped form is gone rather than merely joined. A second RUN
	//    that still links with only `-s -w` would overwrite a stamped binary
	//    with an unstamped one and nothing would say so.
	if strings.Contains(df, `-ldflags="-s -w"`) {
		t.Error(`the Dockerfile still has a -ldflags="-s -w" build, which produces an unstamped binary`)
	}

	// 4. The labels, so the image answers without being run.
	for _, label := range []string{
		`org.opencontainers.image.version="${FORGE_VERSION}"`,
		`org.opencontainers.image.revision="${FORGE_COMMIT}"`,
		`org.opencontainers.image.created="${FORGE_BUILD_DATE}"`,
	} {
		if !strings.Contains(df, label) {
			t.Errorf("the image carries no %s label", label)
		}
	}
}

// And the caller: `make image` is what puts real values in those arguments, from
// the same git-derived variables `make build` stamps into the local binaries. A
// target that builds the image without them is how production goes back to
// reporting "unknown" while every test here stays green.
func TestMakeImage_PassesTheVersionArgumentsFromGit(t *testing.T) {
	mk := read(t, "../Makefile")

	start := strings.Index(mk, "\nimage: ")
	if start < 0 {
		t.Fatal("the Makefile has no `image` target, so the image is built by hand and the stamp is optional")
	}
	rest := mk[start+1:]
	if end := strings.Index(rest, "\n.PHONY:"); end > 0 {
		rest = rest[:end]
	}

	for _, want := range []string{
		"--build-arg FORGE_VERSION=$(VERSION)",
		"--build-arg FORGE_COMMIT=$(COMMIT)",
		"--build-arg FORGE_BUILD_DATE=$(BUILD_DATE)",
		"-f deploy/Dockerfile",
	} {
		if !strings.Contains(rest, want) {
			t.Errorf("`make image` does not pass %q:\n%s", want, rest)
		}
	}
	// The read-back. Without it a dropped argument is invisible until an
	// operator asks production what it is running.
	if !strings.Contains(rest, "forgectl $(IMAGE) version") {
		t.Errorf("`make image` never reads the stamp back out of the image it built:\n%s", rest)
	}
	// The three variables are derived from git, not written down.
	for _, want := range []string{
		"VERSION     ?= $(shell git describe",
		"COMMIT      ?= $(shell git rev-parse",
	} {
		if !strings.Contains(mk, want) {
			t.Errorf("the Makefile no longer derives the stamp from git: missing %q", want)
		}
	}
}
