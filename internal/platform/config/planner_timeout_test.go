package config

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// FORGE_PLANNER_REQUEST_TIMEOUT (issue 13).
//
// The planner is the one role measured to need more than the general call
// timeout: 128 s wall clock against a 180 s budget on 2026-09-07, with three
// consecutive timeouts in one evaluation run. .env.example's standing answer —
// do not raise FORGE_LLM_REQUEST_TIMEOUT, because raising it hides the shape of
// the problem — is right, and these tests hold the shape of the fix that
// respects it: the planner gets its own number, every other role keeps the tight
// one, and an upgrade that sets nothing changes nothing.

// ‼️ An unset planner timeout IS the general request timeout.
//
// This is the upgrade-safety property, and it is the reason the variable can be
// added at all. Every deployment that has never heard of it must behave exactly
// as it did: same bound, same failure mode. A default of "no bound" would be
// worse than the bug — the planner would hang instead of timing out — and a new
// constant would change every existing deployment on upgrade without anyone
// asking for it.
func TestConfig_AnUnsetPlannerTimeoutIsTheGeneralRequestTimeout(t *testing.T) {
	cfg, _, err := loadWith(t, minimalEnv())
	if err != nil {
		t.Fatalf("a configuration that sets no planner timeout must load: %v", err)
	}
	if cfg.LLM.PlannerRequestTimeout != cfg.LLM.RequestTimeout {
		t.Errorf("an unset FORGE_PLANNER_REQUEST_TIMEOUT resolved to %s while "+
			"FORGE_LLM_REQUEST_TIMEOUT is %s; upgrading must not change any deployment's behaviour",
			cfg.LLM.PlannerRequestTimeout, cfg.LLM.RequestTimeout)
	}
	if cfg.LLM.PlannerRequestTimeout != 3*time.Minute {
		t.Errorf("the planner timeout is %s, want the general default of 3m",
			cfg.LLM.PlannerRequestTimeout)
	}

	// It follows the general VALUE, not a constant that happens to match its
	// default — a deployment that raised FORGE_LLM_REQUEST_TIMEOUT still gets
	// what it asked for, in the role that made it raise the number.
	env := minimalEnv()
	env["FORGE_LLM_REQUEST_TIMEOUT"] = "8m"
	env["FORGE_TURN_BUDGET"] = "30m"
	cfg, _, err = loadWith(t, env)
	if err != nil {
		t.Fatalf("loading with a raised general timeout: %v", err)
	}
	if cfg.LLM.PlannerRequestTimeout != 8*time.Minute {
		t.Errorf("with FORGE_LLM_REQUEST_TIMEOUT=8m the planner timeout is %s, want 8m",
			cfg.LLM.PlannerRequestTimeout)
	}
}

// ‼️ A planner timeout set smaller than the general one is still what the
// planner gets.
//
// The point is that the value is actually DISTINCT. The planner applies its
// number as a context deadline around its own model call, which can only make
// that call STRICTER than the model client's HTTP timeout — so "smaller than the
// general one" is the case that is end-to-end true today, and the case a
// regression breaks by quietly folding the two variables back into one.
//
// The second half is the half .env.example argues about: setting the planner's
// number must NOT move the general one. If it did, this variable would be the
// blanket raise the repository already decided against, wearing a different name.
func TestConfig_APlannerTimeoutSetSmallerThanTheGeneralOneIsStillWhatThePlannerGets(t *testing.T) {
	env := minimalEnv()
	env["FORGE_LLM_REQUEST_TIMEOUT"] = "3m"
	env["FORGE_PLANNER_REQUEST_TIMEOUT"] = "45s"

	cfg, _, err := loadWith(t, env)
	if err != nil {
		t.Fatalf("loading with a smaller planner timeout: %v", err)
	}
	if cfg.LLM.PlannerRequestTimeout != 45*time.Second {
		t.Errorf("FORGE_PLANNER_REQUEST_TIMEOUT=45s loaded as %s; a value that is read and then "+
			"overwritten by the general one is a variable that does nothing",
			cfg.LLM.PlannerRequestTimeout)
	}
	if cfg.LLM.RequestTimeout != 3*time.Minute {
		t.Errorf("the general FORGE_LLM_REQUEST_TIMEOUT became %s; the planner's own number must "+
			"not reach back and change the bound every other role runs under — that is exactly the "+
			"blanket raise .env.example refuses, under a different name", cfg.LLM.RequestTimeout)
	}

	// Both are printed at start-up, because the interesting fact about either is
	// the gap between them: a plan that died at three minutes is read against
	// these two numbers and nothing else.
	printed := cfg.Redacted()
	for key, want := range map[string]string{
		"llm_request_timeout":     "3m0s",
		"planner_request_timeout": "45s",
	} {
		if got := fmt.Sprint(printed[key]); got != want {
			t.Errorf("forge.config.loaded prints %s=%q, want %q", key, got, want)
		}
	}

	// A value LARGER than the general one loads too. Whether it takes effect
	// end to end is a separate question, answered honestly in
	// docs/spikes/2026-09-20-planner-latency/README.md: the model client's own
	// http.Client.Timeout still caps the call until that is wired.
	env = minimalEnv()
	env["FORGE_LLM_REQUEST_TIMEOUT"] = "3m"
	env["FORGE_PLANNER_REQUEST_TIMEOUT"] = "6m"
	env["FORGE_TURN_BUDGET"] = "30m"
	cfg, _, err = loadWith(t, env)
	if err != nil {
		t.Fatalf("loading with a larger planner timeout: %v", err)
	}
	if cfg.LLM.PlannerRequestTimeout != 6*time.Minute {
		t.Errorf("FORGE_PLANNER_REQUEST_TIMEOUT=6m loaded as %s", cfg.LLM.PlannerRequestTimeout)
	}
}

// ‼️ A planner timeout that is not positive is refused, and says so by name.
//
// Zero and negative both parse as Go durations, so neither is caught by the
// duration parser. Both would cancel the planner's call before it was sent, and
// the error a person would then see names the model rather than the setting —
// the same confusion FORGE_TURN_BUDGET's validation exists to prevent.
//
// Refused rather than silently treated as "unset". Somebody who typed a number
// was trying to say something, and quietly ignoring it leaves them debugging a
// planner that does not obey a variable they can see in their own environment.
func TestConfig_APlannerTimeoutThatIsNotPositiveIsRefusedByName(t *testing.T) {
	for _, nonsense := range []string{"0s", "-5m", "soon"} {
		env := minimalEnv()
		env["FORGE_LLM_REQUEST_TIMEOUT"] = "3m"
		env["FORGE_PLANNER_REQUEST_TIMEOUT"] = nonsense

		_, _, err := loadWith(t, env)
		if err == nil {
			t.Errorf("FORGE_PLANNER_REQUEST_TIMEOUT=%s was accepted; it would cancel the planner's "+
				"call before it was sent and the failure would be reported as the model's", nonsense)
			continue
		}
		if !strings.Contains(err.Error(), "FORGE_PLANNER_REQUEST_TIMEOUT") {
			t.Errorf("FORGE_PLANNER_REQUEST_TIMEOUT=%s was refused without naming the variable, so "+
				"the person who set it cannot tell which one is wrong: %v", nonsense, err)
		}
	}
}
