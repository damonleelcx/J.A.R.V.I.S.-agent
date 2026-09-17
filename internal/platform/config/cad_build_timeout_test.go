package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// The kernel's build limit was a constant, cad.buildTimeout. It is a setting now,
// with the constant's value as its default, so a deployment on a slower node can
// give a build more time without a new binary (plan row "Kernel timeout is not a
// crash", PR #100's "Not in this fix").
func TestCADBuildTimeoutIsThirtySecondsByDefaultAndPrinted(t *testing.T) {
	cfg, _, err := loadWith(t, minimalEnv())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CAD.BuildTimeout != 30*time.Second {
		t.Errorf("FORGE_CAD_BUILD_TIMEOUT defaults to %s, want 30s", cfg.CAD.BuildTimeout)
	}
	if !cfg.CAD.Prestart {
		t.Error("FORGE_CAD_PRESTART defaults to false, want true")
	}

	env := minimalEnv()
	env["FORGE_CAD_BUILD_TIMEOUT"] = "45s"
	env["FORGE_CAD_POOL"] = "2"
	env["FORGE_CAD_PRESTART"] = "false"
	cfg, _, err = loadWith(t, env)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CAD.BuildTimeout != 45*time.Second || cfg.CAD.Prestart {
		t.Errorf("FORGE_CAD_BUILD_TIMEOUT=45s FORGE_CAD_PRESTART=false loaded %s, %v", cfg.CAD.BuildTimeout, cfg.CAD.Prestart)
	}
	// forge.config.loaded is how an operator reads a CAD_KERNEL_TIMEOUT against
	// the limit that produced it.
	printed := cfg.Redacted()
	for key, want := range map[string]string{"cad_build_timeout": "45s", "cad_pool": "2", "cad_prestart": "false"} {
		if got := fmt.Sprint(printed[key]); got != want {
			t.Errorf("forge.config.loaded prints %s=%q, want %q", key, got, want)
		}
	}
}

func TestCADBuildTimeoutMustBePositiveAndFitTheWriteTimeout(t *testing.T) {
	for _, c := range []struct{ value, writeTimeout, says string }{
		{"0s", "", "positive"},
		{"-5s", "", "positive"},
		{"thirty", "", "Go duration"},
		{"2m", "1m", "FORGE_HTTP_WRITE_TIMEOUT"},
	} {
		t.Run(c.value, func(t *testing.T) {
			env := minimalEnv()
			env["FORGE_CAD_BUILD_TIMEOUT"] = c.value
			if c.writeTimeout != "" {
				env["FORGE_HTTP_WRITE_TIMEOUT"] = c.writeTimeout
			}
			_, _, err := loadWith(t, env)
			if err == nil || !strings.Contains(err.Error(), "FORGE_CAD_BUILD_TIMEOUT") || !strings.Contains(err.Error(), c.says) {
				t.Errorf("FORGE_CAD_BUILD_TIMEOUT=%s was not refused by name saying %q: %v", c.value, c.says, err)
			}
		})
	}
	env := minimalEnv()
	env["FORGE_CAD_BUILD_TIMEOUT"] = "1m"
	env["FORGE_HTTP_WRITE_TIMEOUT"] = "1m"
	if _, _, err := loadWith(t, env); err != nil {
		t.Errorf("a build limit equal to the write timeout was refused: %v", err)
	}
}
