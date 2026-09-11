package agent_test

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sort"
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
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// TestLiveGearMeasure is ONE sample of the rate that decides whether asking for a
// gear gets you the gear you asked for. Run it -count=10 in each arm of an A/B.
//
// # Why this and not TestLiveGearTurn
//
// That test asks whether every SCRIPTED part a turn kept will build, and passes
// when the model used no script at all — right for what it checks, and useless as
// a rate: a gear drawn as a named block and a real gear both pass it. This asks
// the person's question of the whole reply — does the assembly build in the real
// kernel, with nothing left out, at the size that was asked for — so a script
// and the "gear" shape are judged by the same rule.
//
// The outcome goes on one GEAR-VERDICT line so a batch can be tallied, and
// anything other than builds-as-asked FAILS: a measurement that cannot fail is a
// log line, not a measurement.
func TestLiveGearMeasure(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1 to run the live gear measurement")
	}
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("set FORGE_CAD_PYTHON to a Python with build123d")
	}
	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "gear-measure"})
	kernel := cad.New(python, log).WithScripts(true)
	defer kernel.Close()

	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL:        envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen3.7-plus"),
		RequestTimeout: 3 * time.Minute,
		MaxRetries:     2,
	}, log, clock.System{})
	// Scripts ON in both arms, as production runs: the question is what the model
	// reaches for when it has both, not what it does when one is taken away.
	conv := agent.NewConversation(client, persona.DefaultCharacter()).
		WithScripts(liveRunner{kernel})

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	// The prompt every earlier gear measurement used, so the rates compare.
	const prompt = "Make me a 20-tooth involute spur gear, module 2, 6mm thick."
	start := time.Now()
	var shapes []string
	verdict := func(outcome, detail string) {
		t.Logf("GEAR-VERDICT outcome=%s shapes=%s seconds=%.0f %s",
			outcome, strings.Join(shapes, ","), time.Since(start).Seconds(), detail)
		if outcome != "builds-as-asked" {
			t.Errorf("the gear that was asked for was not delivered: %s — %s", outcome, detail)
		}
	}

	reply, err := conv.Respond(ctx, "", nil, prompt, "", nil, nil)
	if err != nil {
		verdict("turn-failed", errs.DetailOf(err))
		return
	}
	if reply.Prototype == nil {
		verdict("no-geometry", reply.Speech)
		return
	}
	for _, p := range reply.Prototype.Parts {
		shapes = append(shapes, strings.ToLower(strings.TrimSpace(p.Shape)))
	}
	unit, ok := geometry.ParseUnit(reply.Prototype.Units)
	if !ok {
		verdict("no-unit", reply.Prototype.Units)
		return
	}
	build, err := kernel.BuildDocument(ctx, *reply.Prototype, unit, "step")
	if err != nil {
		verdict("does-not-build", errs.DetailOf(err))
		return
	}
	for _, n := range build.Inferred {
		if strings.Contains(n, "not in this file") {
			verdict("part-missing", n)
			return
		}
	}
	if len(build.Skipped) > 0 || len(build.FeatureFailures) > 0 {
		verdict("part-missing", fmt.Sprint(build.Skipped, build.FeatureFailures))
		return
	}

	ext := []float64{build.Bounds[3] - build.Bounds[0], build.Bounds[4] - build.Bounds[1],
		build.Bounds[5] - build.Bounds[2]}
	sort.Float64s(ext)
	detail := fmt.Sprintf("extents=%.2f,%.2f,%.2f volume=%.0f", ext[0], ext[1], ext[2], build.Volume)
	// Module 2 × (20 + 2) is 44 across the tips, and 6 thick was asked for.
	if math.Abs(ext[1]-44) > 1 || math.Abs(ext[2]-44) > 1 || math.Abs(ext[0]-6) > 0.5 {
		verdict("wrong-size", detail)
		return
	}
	verdict("builds-as-asked", detail)
}
