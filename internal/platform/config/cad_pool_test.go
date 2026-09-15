package config

import (
	"strings"
	"testing"
)

// Phase 4, stage K3. One process unless a deployment asks for more, because each
// process is a build123d in memory under the pod's limit.
func TestCADPoolIsOneProcessByDefault(t *testing.T) {
	cfg, _, err := loadWith(t, minimalEnv())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CAD.Pool != 1 {
		t.Errorf("FORGE_CAD_POOL defaults to %d, want 1", cfg.CAD.Pool)
	}
}

func TestCADPoolMustBePositive(t *testing.T) {
	env := minimalEnv()
	env["FORGE_CAD_POOL"] = "0"
	_, _, err := loadWith(t, env)
	if err == nil || !strings.Contains(err.Error(), "FORGE_CAD_POOL") {
		t.Errorf("FORGE_CAD_POOL=0 would leave no process to build with and was not refused by name: %v", err)
	}
}
