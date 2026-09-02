package config

import (
	"fmt"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// minimalEnv is the smallest set that must load successfully. Tests mutate a
// copy of it so each case isolates one defect.
func minimalEnv() map[string]string {
	return map[string]string{
		"FORGE_ENV":            "development",
		"FORGE_DATABASE_URL":   "postgres://forge:pw@localhost:5432/forge?sslmode=disable",
		"FORGE_SESSION_SECRET": strings.Repeat("s", 48),
		"FORGE_LLM_API_KEY":    "sk-test",
	}
}

func loadWith(t *testing.T, env map[string]string) (*Config, []string, error) {
	t.Helper()
	for k, v := range env {
		t.Setenv(k, v)
	}
	return Load()
}

func TestMinimalConfigLoads(t *testing.T) {
	cfg, _, err := loadWith(t, minimalEnv())
	if err != nil {
		t.Fatalf("minimal config should load: %v", err)
	}
	if cfg.Env != EnvDevelopment {
		t.Errorf("Env = %q", cfg.Env)
	}
	if cfg.LLM.Planner == "" || cfg.LLM.Verifier == "" {
		t.Error("model roles must have defaults; an unset role would fail at first use, deep inside a running goal")
	}
}

func TestMissingRequiredValuesAreAllReportedAtOnce(t *testing.T) {
	// A deployment must not be fixed one variable per restart.
	env := minimalEnv()
	delete(env, "FORGE_DATABASE_URL")
	delete(env, "FORGE_SESSION_SECRET")
	delete(env, "FORGE_LLM_API_KEY")
	t.Setenv("FORGE_DATABASE_URL", "")
	t.Setenv("FORGE_SESSION_SECRET", "")
	t.Setenv("FORGE_LLM_API_KEY", "")

	_, _, err := loadWith(t, env)
	if err == nil {
		t.Fatal("expected failure with three required values missing")
	}
	msg := err.Error()
	for _, want := range []string{"FORGE_DATABASE_URL", "FORGE_SESSION_SECRET", "FORGE_LLM_API_KEY"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error should name %s; got: %s", want, msg)
		}
	}
	if errs.CodeOf(err) != errs.CodeConfigInvalid {
		t.Errorf("code = %v, want CONFIG_INVALID", errs.CodeOf(err))
	}
}

// TestProductionRefusesTheFileMailTransport is a safety gate, not a preference.
// The file transport writes mail to disk instead of delivering it; shipping it
// to production means every verification and password-reset email silently
// vanishes, and the failure is invisible until a user complains.
func TestProductionRefusesTheFileMailTransport(t *testing.T) {
	env := minimalEnv()
	env["FORGE_ENV"] = "production"
	env["FORGE_PUBLIC_URL"] = "https://forge.example.com"
	env["FORGE_MAIL_TRANSPORT"] = "file"

	_, _, err := loadWith(t, env)
	if err == nil {
		t.Fatal("production must refuse the file mail transport")
	}
	if !strings.Contains(err.Error(), "FORGE_MAIL_TRANSPORT") {
		t.Errorf("error should name the offending variable: %s", err)
	}
}

func TestProductionRefusesPlaintextPublicURL(t *testing.T) {
	env := minimalEnv()
	env["FORGE_ENV"] = "production"
	env["FORGE_PUBLIC_URL"] = "http://forge.example.com"
	env["FORGE_MAIL_TRANSPORT"] = "smtp"
	env["FORGE_SMTP_HOST"] = "smtp.example.com"

	_, _, err := loadWith(t, env)
	if err == nil {
		t.Fatal("production must refuse a plaintext public URL: reset links carry live credentials")
	}
}

func TestProductionRefusesInsecureCookies(t *testing.T) {
	env := minimalEnv()
	env["FORGE_ENV"] = "production"
	env["FORGE_PUBLIC_URL"] = "https://forge.example.com"
	env["FORGE_MAIL_TRANSPORT"] = "smtp"
	env["FORGE_SMTP_HOST"] = "smtp.example.com"
	env["FORGE_COOKIE_SECURE"] = "false"

	_, _, err := loadWith(t, env)
	if err == nil {
		t.Fatal("production must refuse FORGE_COOKIE_SECURE=false")
	}
}

func TestShortSessionSecretIsRefused(t *testing.T) {
	env := minimalEnv()
	env["FORGE_SESSION_SECRET"] = "tooshort"
	_, _, err := loadWith(t, env)
	if err == nil {
		t.Fatal("a short session secret is a forgeable session and must be refused")
	}
	if !strings.Contains(err.Error(), "openssl rand") {
		t.Error("the remedy should show how to generate one")
	}
}

// TestHeartbeatMustBeShorterThanLease guards a configuration that looks fine
// and then makes every long-running task lose its lease mid-flight — the exact
// class of defect that only appears under load, hours in.
func TestHeartbeatMustBeShorterThanLease(t *testing.T) {
	env := minimalEnv()
	env["FORGE_LEASE_DURATION"] = "30s"
	env["FORGE_LEASE_HEARTBEAT"] = "30s"
	_, _, err := loadWith(t, env)
	if err == nil {
		t.Fatal("a heartbeat equal to the lease duration must be refused")
	}
}

func TestNarrowHeartbeatMarginWarns(t *testing.T) {
	env := minimalEnv()
	env["FORGE_LEASE_DURATION"] = "30s"
	env["FORGE_LEASE_HEARTBEAT"] = "20s"
	_, warnings, err := loadWith(t, env)
	if err != nil {
		t.Fatalf("this is legal, just risky: %v", err)
	}
	if !containsSubstr(warnings, "missed heartbeat") {
		t.Errorf("expected a warning that one missed heartbeat loses the lease; got %v", warnings)
	}
}

// TestVerifierIndependenceWarning is a safety control expressed as config.
// PRD SAF-03 requires high-risk conclusions be checked by a method independent
// of the generative path. One model grading its own output is not independent.
func TestVerifierIndependenceWarning(t *testing.T) {
	env := minimalEnv()
	env["FORGE_LLM_EXECUTOR_MODEL"] = "qwen3.8-max"
	env["FORGE_LLM_VERIFIER_MODEL"] = "qwen-plus"
	_, warnings, err := loadWith(t, env)
	if err != nil {
		t.Fatalf("same-family models are legal, just weaker: %v", err)
	}
	if !containsSubstr(warnings, "SAF-03") {
		t.Errorf("expected a verifier-independence warning citing SAF-03; got %v", warnings)
	}

	env["FORGE_LLM_VERIFIER_MODEL"] = "deepseek-v4-pro"
	_, warnings, err = loadWith(t, env)
	if err != nil {
		t.Fatal(err)
	}
	if containsSubstr(warnings, "SAF-03") {
		t.Errorf("cross-family models should not warn; got %v", warnings)
	}
}

func TestModelFamilyDetection(t *testing.T) {
	cases := map[string]string{
		"qwen3.8-max":      "qwen",
		"qwen-plus":        "qwen",
		"deepseek-v4-pro":  "deepseek",
		"ZHIPU/GLM-5.3":    "zhipu",
		"kimi/kimi-k3":     "kimi",
		"claude-opus-5":    "claude",
		"unknown-model-xy": "unknown-model-xy",
	}
	for model, want := range cases {
		if got := modelFamily(model); got != want {
			t.Errorf("modelFamily(%q) = %q, want %q", model, got, want)
		}
	}
}

// TestRedactedNeverLeaksSecrets is the fence behind "startup may print its
// configuration". If this regresses, credentials land in the log aggregator.
func TestRedactedNeverLeaksSecrets(t *testing.T) {
	env := minimalEnv()
	env["FORGE_SESSION_SECRET"] = "SUPERSECRETSESSIONVALUE_0123456789abcdef"
	env["FORGE_LLM_API_KEY"] = "sk-LEAKME-0123456789"
	env["FORGE_DATABASE_URL"] = "postgres://forge:PGPASSWORDLEAK@db.example.com:5432/forge"
	cfg, _, err := loadWith(t, env)
	if err != nil {
		t.Fatal(err)
	}

	rendered := strings.ToLower(sprintMap(cfg.Redacted()))
	for _, secret := range []string{"supersecretsessionvalue", "sk-leakme", "pgpasswordleak"} {
		if strings.Contains(rendered, secret) {
			t.Errorf("Redacted() leaked %q:\n%s", secret, rendered)
		}
	}
	// It must still be useful: host and database name are what an operator reads.
	if !strings.Contains(rendered, "db.example.com") {
		t.Error("Redacted() should keep the database host, which is what an operator needs to see")
	}
	if !strings.Contains(rendered, "true") {
		t.Error("Redacted() should report presence of secrets as booleans")
	}
}

func TestUnknownEnvIsRefused(t *testing.T) {
	env := minimalEnv()
	env["FORGE_ENV"] = "staging"
	_, _, err := loadWith(t, env)
	if err == nil {
		t.Fatal("an unrecognised FORGE_ENV must be refused, not silently treated as development")
	}
}

func TestBadDurationNamesTheVariable(t *testing.T) {
	env := minimalEnv()
	env["FORGE_LEASE_DURATION"] = "2 minutes"
	_, _, err := loadWith(t, env)
	if err == nil {
		t.Fatal("expected a parse failure")
	}
	if !strings.Contains(err.Error(), "FORGE_LEASE_DURATION") {
		t.Errorf("error must name the variable so it can be fixed: %s", err)
	}
}

func containsSubstr(hay []string, needle string) bool {
	for _, h := range hay {
		if strings.Contains(h, needle) {
			return true
		}
	}
	return false
}

func sprintMap(m map[string]any) string {
	var b strings.Builder
	for k, v := range m {
		fmt.Fprintf(&b, "%s=%v ", k, v)
	}
	return b.String()
}
