package config

import (
	"strings"
	"testing"
)

// The transcriber's own endpoint (FORGE_LLM_TRANSCRIBER_BASE_URL and
// FORGE_LLM_TRANSCRIBER_API_KEY).
//
// # Why these exist
//
// The production chat endpoint serves no speech-to-text model (measured
// 2026-09-15), so transcription has to be able to go somewhere else under a key
// of its own. The one thing that must never follow from that is the chat key
// travelling to the other host, and every half-configuration below is refused
// by name because each has a plausible reading that does exactly that.

const (
	chatBase = "https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1"
	sttBase  = "https://dashscope.aliyuncs.com/compatible-mode/v1"
)

// transcriberEnv is minimalEnv with both settings stated, so a developer's own
// environment cannot decide the case.
func transcriberEnv(baseURL, apiKey string) map[string]string {
	env := minimalEnv()
	env["FORGE_LLM_BASE_URL"] = chatBase
	env["FORGE_LLM_API_KEY"] = "sk-chat-key"
	env["FORGE_LLM_TRANSCRIBER_BASE_URL"] = baseURL
	env["FORGE_LLM_TRANSCRIBER_API_KEY"] = apiKey
	return env
}

func TestTranscriberEndpoint_UnsetMeansTheChatEndpointAndItsKeyAsBefore(t *testing.T) {
	cfg, _, err := loadWith(t, transcriberEnv("", ""))
	if err != nil {
		t.Fatalf("a deployment that sets neither must load exactly as it did: %v", err)
	}
	base, key := cfg.LLM.TranscriberEndpoint()
	if base != chatBase || key != "sk-chat-key" {
		t.Errorf("unset transcribes at %q with key %q, want the chat endpoint and key", base, key)
	}
}

func TestTranscriberEndpoint_ASeparateEndpointWithItsOwnKeyIsUsed(t *testing.T) {
	cfg, warnings, err := loadWith(t, transcriberEnv(sttBase+"/", "sk-asr-key"))
	if err != nil {
		t.Fatal(err)
	}
	base, key := cfg.LLM.TranscriberEndpoint()
	if base != sttBase {
		t.Errorf("transcribes at %q, want %q (trailing slash trimmed)", base, sttBase)
	}
	if key != "sk-asr-key" {
		t.Errorf("transcribes with key %q, want the transcriber's own", key)
	}
	// A second host hears recorded speech; the data boundary describes one.
	if !containsSubstr(warnings, "FORGE_LLM_TRANSCRIBER_BASE_URL") {
		t.Errorf("a second endpoint hearing speech was not named at startup: %v", warnings)
	}
}

func TestTranscriberEndpoint_AKeyWithoutAnEndpointIsRefusedByName(t *testing.T) {
	_, _, err := loadWith(t, transcriberEnv("", "sk-asr-key"))
	if err == nil {
		t.Fatal("a transcriber key with no endpoint loaded; it would be sent to the chat host, which did not issue it")
	}
	if !strings.Contains(err.Error(), "FORGE_LLM_TRANSCRIBER_BASE_URL") {
		t.Errorf("the refusal does not name the missing setting: %s", err)
	}
}

func TestTranscriberEndpoint_AnotherHostWithoutAKeyIsRefusedByName(t *testing.T) {
	_, _, err := loadWith(t, transcriberEnv(sttBase, ""))
	if err == nil {
		t.Fatal("a transcriber endpoint on another host loaded with no key of its own; the chat " +
			"key would be the only one left to send it")
	}
	if !strings.Contains(err.Error(), "FORGE_LLM_TRANSCRIBER_API_KEY") {
		t.Errorf("the refusal does not name the missing setting: %s", err)
	}
}

func TestTranscriberEndpoint_TheChatHostItselfMayUseTheChatKey(t *testing.T) {
	sameHost := "https://token-plan.cn-beijing.maas.aliyuncs.com/other-path/v1"
	cfg, _, err := loadWith(t, transcriberEnv(sameHost, ""))
	if err != nil {
		t.Fatalf("a path on the chat endpoint's own origin was refused: %v", err)
	}
	if base, key := cfg.LLM.TranscriberEndpoint(); base != sameHost || key != "sk-chat-key" {
		t.Errorf("got %q with key %q, want the chat key on the chat host", base, key)
	}
}

func TestTranscriberEndpoint_SomethingThatIsNotAURLIsRefused(t *testing.T) {
	for _, bad := range []string{"dashscope.aliyuncs.com/compatible-mode/v1", "ftp://dashscope.aliyuncs.com"} {
		_, _, err := loadWith(t, transcriberEnv(bad, "sk-asr-key"))
		if err == nil || !strings.Contains(err.Error(), "FORGE_LLM_TRANSCRIBER_BASE_URL") {
			t.Errorf("%q: want a refusal naming FORGE_LLM_TRANSCRIBER_BASE_URL, got %v", bad, err)
		}
	}
}

// The resolver on its own, for the client built from a hand-made LLMConfig that
// never passes through Load — the case the refusals above cannot protect.
func TestTranscriberEndpoint_NeverHandsTheChatKeyToAnotherHost(t *testing.T) {
	c := LLMConfig{BaseURL: chatBase, APIKey: "sk-chat-key", TranscriberBaseURL: sttBase}
	base, key := c.TranscriberEndpoint()
	if base != sttBase {
		t.Errorf("transcribes at %q, want %q", base, sttBase)
	}
	if key != "" {
		t.Errorf("another host was given key %q; with no key of its own it must get none", key)
	}
	// Port is part of the origin: the same hostname on another port is another
	// server.
	if SameOrigin("https://h.example:8443/v1", "https://h.example/v1") {
		t.Error("a different port was treated as the same origin")
	}
}

func TestTranscriberEndpoint_TheKeyNeverReachesConfigPrint(t *testing.T) {
	cfg, _, err := loadWith(t, transcriberEnv(sttBase, "sk-ASRLEAK-0123456789"))
	if err != nil {
		t.Fatal(err)
	}
	printed := sprintMap(cfg.Redacted())
	if strings.Contains(strings.ToLower(printed), "sk-asrleak") {
		t.Errorf("Redacted() leaked the transcriber key:\n%s", printed)
	}
	if cfg.Redacted()["llm_transcriber_api_key_set"] != true {
		t.Error("Redacted() does not say whether the transcriber key is set")
	}
	if !strings.Contains(printed, sttBase) {
		t.Errorf("Redacted() does not show which host hears speech:\n%s", printed)
	}
}
