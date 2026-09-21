package config

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/buildinfo"
)

// ‼️ forge.config.loaded says which build wrote it (2026-09-20).
//
// forge.config.loaded is the one line every deployment keeps and the first thing
// read in an incident, and until now it described the configuration of a process
// whose identity was nowhere in the log. The build is not configuration, which
// is why it is commented as such where it is added — it is here because this is
// the line somebody already has in front of them.
func TestRedacted_PrintsTheBuildAndSaysUnknownWhenTheImageDidNotStampIt(t *testing.T) {
	restore := buildinfo.Get()
	t.Cleanup(func() { buildinfo.Set(restore.Version, restore.Commit, restore.Date) })

	cfg, _, err := loadWith(t, minimalEnv())
	if err != nil {
		t.Fatal(err)
	}

	// The unstamped case, which is what a plain `docker build` produces.
	buildinfo.Set("", "", "")
	printed, ok := cfg.Redacted()["build"].(string)
	if !ok {
		t.Fatalf("forge.config.loaded prints no build at all: %v", cfg.Redacted()["build"])
	}
	if printed != "unknown (commit unknown, built unknown)" {
		t.Errorf("an unstamped build prints build=%q", printed)
	}

	// The stamped case: the same sentence `forgectl version` prints, so two
	// copies of it can be compared by eye.
	buildinfo.Set("v0.9.1", "ab12cd3", "2026-09-20T09:41:07Z")
	printed, _ = cfg.Redacted()["build"].(string)
	if printed != "v0.9.1 (commit ab12cd3, built 2026-09-20T09:41:07Z)" {
		t.Errorf("forge.config.loaded prints build=%q, want the stamp it was built with", printed)
	}
	if printed != buildinfo.Get().String() {
		t.Errorf("the log line %q and buildinfo %q have drifted apart", printed, buildinfo.Get())
	}

	// And it stayed redacted: adding a field to this map is how a secret gets
	// into a log, so the existing promise is re-checked here rather than
	// assumed.
	if s := sprintMap(cfg.Redacted()); strings.Contains(s, "sk-test") {
		t.Errorf("Redacted leaked the API key:\n%s", s)
	}
}
