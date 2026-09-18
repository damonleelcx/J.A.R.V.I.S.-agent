package agent_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// TestLiveLookAtKeptCars puts the build's visual check (stage V4: the whole model,
// then each sub-assembly the root places drawn on its own, at most four) to the live
// vision model on cars a live run already built and kept, so the check is paid for
// without paying for another car. FORGE_LIVE_LOOK_DOCS lists the documents
// (comma-separated paths); FORGE_MEASURE_TOKEN_BUDGET caps the spend with the car
// harness's meter. docs/spikes/2026-09-17-live-verification
func TestLiveLookAtKeptCars(t *testing.T) {
	docs := os.Getenv("FORGE_LIVE_LOOK_DOCS")
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" || docs == "" {
		t.Skip("set FORGE_LLM_API_KEY, FORGE_LIVE_LLM_TESTS=1 and FORGE_LIVE_LOOK_DOCS to look at kept cars live")
	}
	budget, err := strconv.ParseInt(os.Getenv("FORGE_MEASURE_TOKEN_BUDGET"), 10, 64)
	if err != nil || budget <= 0 {
		t.Fatalf("FORGE_MEASURE_TOKEN_BUDGET must be set to a positive number of tokens")
	}
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Fatal("FORGE_CAD_PYTHON must be set: the check looks at what the kernel built")
	}
	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "look-kept"})
	kernel := cad.New(python, log).WithScripts(true)
	defer kernel.Close()
	inner := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL:        envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen3.7-plus"),
		Vision:         envOrDefault("FORGE_LLM_VISION_MODEL", "qwen3.8-max"),
		RequestTimeout: 3 * time.Minute,
		MaxRetries:     2,
	}, log, clock.System{})
	meter := &meteredClient{inner: inner, budget: budget}
	conv := agent.NewConversation(meter, persona.DefaultCharacter()).
		WithScripts(liveRunner{kernel}).WithSolids(liveSolids{kernel})

	for _, path := range strings.Split(docs, ",") {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		var doc geometry.Document
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		began := time.Now()
		before, callsBefore, _ := meter.report()
		seen, looked, of, fromKernel, err := agent.LookedForTest(ctx, conv, &doc,
			"a sports car, in as much mechanical detail as you can manage")
		cancel()
		after, callsAfter, refused := meter.report()
		t.Logf("LOOK-KEPT doc=%s occurrences=%d from_kernel=%v looked_closely=%d of=%d problems=%d "+
			"tokens=%d calls=%d refused=%d seconds=%.1f err=%v", path, doc.Occurrences(), fromKernel, looked, of,
			len(seen), after-before, callsAfter-callsBefore, refused, time.Since(began).Seconds(), err)
		if of > looked {
			t.Logf("LOOK-KEPT note would read: FORGE looked closely at %d of %d sub-assemblies", looked, of)
		}
		named := 0
		for _, p := range seen {
			if strings.HasPrefix(p.Detail, "in ") {
				named++
			}
			t.Logf("  problem: %s — %s", p.Name, p.Detail)
		}
		t.Logf("LOOK-KEPT problems_naming_a_sub_assembly=%d of %d", named, len(seen))
	}
	for _, line := range lookLines(looksOf(meter.recorded())) {
		t.Logf("%s", line)
	}
	spent, calls, refused := meter.report()
	t.Logf("LOOK-SPEND tokens=%d of %d calls=%d refused=%d", spent, budget, calls, refused)
}
