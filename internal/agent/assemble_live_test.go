package agent_test

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// TestLiveAssemble builds a car in passes and counts what comes out.
//
// # The number this exists to move
//
// Asked for a sports car in ONE reply, this model answers "a sports car is too
// large and complex for a single parametric prototype here" and returns six
// boxes. That is the ceiling Stage 2 is for, and the only honest measure of
// whether it lifted is how many parts survive to the end — surviving being the
// hard part, since a 13-part car lost its spoiler in 2 runs of 5.
//
// It asserts what must ALWAYS hold: more than one reply's worth of parts, all of
// them buildable, and nothing removed without saying so. It does NOT assert the
// car is beautiful; that has no machine test.
func TestLiveAssemble(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1 to run the live assemble test")
	}
	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "assemble-live-test"})
	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL:        envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen3.7-plus"),
		Vision:         envOrDefault("FORGE_LLM_VISION_MODEL", "qwen3.8-max"),
		RequestTimeout: 3 * time.Minute,
		MaxRetries:     2,
	}, log, clock.System{})
	conv := agent.NewConversation(client, persona.DefaultCharacter())

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	start := time.Now()
	doc, notes, err := agent.AssembleForTest(ctx, conv,
		"a sports car, in as much mechanical detail as you can manage", nil,
		func(s agent.BuildStep) error {
			t.Logf("step %d/%d %-22s parts=%-3d %s", s.N, s.Of, s.Name, s.Parts, s.Note)
			return nil
		})
	if err != nil {
		t.Fatalf("the build failed: %v", err)
	}
	t.Logf("built %d parts and %d features in %s", len(doc.Parts), len(doc.Features), time.Since(start).Round(time.Second))
	for _, n := range notes {
		t.Logf("note: %s", n)
	}

	// One reply manages 6-13 and loses things above that. A build that does not
	// beat one reply is latency with extra steps.
	if len(doc.Parts) <= 6 {
		t.Errorf("the finished model has %d parts. A single reply already manages six, so "+
			"building in passes bought nothing", len(doc.Parts))
	}
	// Every part must BUILD. A part count reached by adding things that do not
	// exist in the solid is a worse lie than a small model.
	if faults := doc.Faults(); len(faults) > 0 {
		for _, f := range faults {
			t.Errorf("the finished model does not build: %s — %s", f.Name, f.Detail)
		}
	}
	t.Logf("mesh: %d triangles", len(geometry.Tessellate(*doc, geometry.Millimetre).Triangles()))
}
