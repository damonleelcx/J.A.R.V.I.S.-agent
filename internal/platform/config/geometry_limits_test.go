package config

import (
	"strings"
	"testing"
)

// What one stored design may be (Phase 3, stage S0 of
// docs/plan-2026-09-13-millions-of-parts.md): defaults decided 2026-09-14, each
// configurable, and a setting that would switch a bound off is refused by name.

func TestGeometryLimitsHaveTheDecidedDefaults(t *testing.T) {
	cfg, _, err := loadWith(t, minimalEnv())
	if err != nil {
		t.Fatalf("minimal config should load: %v", err)
	}
	if g := cfg.Geometry; g.MaxDocumentBytes != 2<<20 || g.MaxDefinitions != 2000 || g.MaxOccurrences != 100000 {
		t.Errorf("geometry limits %+v; want 2 MiB, 2000 definitions and 100,000 occurrences", g)
	}
}

func TestGeometryLimitsAreRead(t *testing.T) {
	env := minimalEnv()
	env["FORGE_GEOMETRY_MAX_DOCUMENT_BYTES"] = "1048576"
	env["FORGE_GEOMETRY_MAX_DEFINITIONS"] = "500"
	env["FORGE_GEOMETRY_MAX_OCCURRENCES"] = "30000"
	cfg, _, err := loadWith(t, env)
	if err != nil {
		t.Fatalf("config with geometry limits should load: %v", err)
	}
	if g := cfg.Geometry; g.MaxDocumentBytes != 1<<20 || g.MaxDefinitions != 500 || g.MaxOccurrences != 30000 {
		t.Errorf("geometry limits %+v were not read from the environment", g)
	}
}

func TestGeometryLimitsMustBePositive(t *testing.T) {
	for _, key := range []string{"FORGE_GEOMETRY_MAX_DOCUMENT_BYTES", "FORGE_GEOMETRY_MAX_DEFINITIONS", "FORGE_GEOMETRY_MAX_OCCURRENCES"} {
		t.Run(key, func(t *testing.T) {
			env := minimalEnv()
			env[key] = "0"
			_, _, err := loadWith(t, env)
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Errorf("%s=0 would switch the bound off and was not refused by name: %v", key, err)
			}
		})
	}
}
