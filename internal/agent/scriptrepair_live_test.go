package agent_test

import (
	"context"
	"log/slog"
	"os"
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

// TestLiveScriptRepair asks a REAL model to fix a script a REAL build123d
// refused, and accepts the answer only when the kernel builds it.
//
// # Why this cannot be a stub
//
// Every fence in scriptrepair_test.go hands the model's answer to a runner the
// test controls, which proves the loop, the caps, the acceptance rule and the
// wiring — and nothing at all about the premise. The premise is that a model
// handed build123d's own words can fix the script it just wrote. If it cannot,
// this whole path is four extra model calls and a longer wait for the same
// missing part, and it should not ship.
//
// # Why the failures are the ones that actually happened
//
// The first case is verbatim the defect found on the live deployment: a profile
// swept or extruded as a WIRE, which build123d refuses with "A face or sketch
// must be provided". The second is a script that names a builder that does not
// exist, which is what a model reaches for when it is guessing.
func TestLiveScriptRepair(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1 to run the live script repair test")
	}
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("set FORGE_CAD_PYTHON to a Python with build123d — this test refuses to " +
			"substitute a fake kernel, because a fake kernel cannot refuse a script")
	}
	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "script-live-test"})
	kernel := cad.New(python, log).WithScripts(true)
	defer kernel.Close()

	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL:        envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen3.7-plus"),
		RequestTimeout: 3 * time.Minute,
		MaxRetries:     2,
	}, log, clock.System{})

	conv := agent.NewConversation(client, persona.DefaultCharacter()).
		WithScripts(liveRunner{kernel})

	cases := []struct {
		name   string
		script string
		why    string
	}{
		{
			name: "a profile extruded as a wire",
			// The live defect, reduced. extrude() wants a face; a Polyline is a
			// wire, and build123d says so in the words the repair is handed.
			script: `pts = [(0, 0), (20, 0), (20, 10), (0, 10)]
profile = Polyline(*pts, close=True)
result = extrude(profile, amount=5)`,
			why: "this is the shape of the failure found on the live deployment",
		},
		{
			name:   "a builder that does not exist",
			script: `result = InvoluteGear(module=2, teeth=20, thickness=6)`,
			why:    "what a model reaches for when it is guessing at an API",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
			defer cancel()

			// It really does not build to begin with. Without this the test can
			// pass by never exercising anything.
			if _, err := kernel.RunScript(ctx, tc.script); err == nil {
				t.Fatalf("the fixture builds as written, so this test proves nothing (%s)", tc.why)
			} else {
				t.Logf("build123d refuses it: %v", err)
			}

			doc := &geometry.Document{
				Name: "Live", Units: "mm",
				Parts: []geometry.Part{{ID: "part", Name: "Gear", Shape: "script", Script: tc.script}},
			}
			reply, changed := agent.ScriptRepairForTest(ctx, conv, doc)

			t.Logf("changed=%v\nnotes: %s\nscript now:\n%s", changed, reply.Repaired,
				reply.Prototype.Parts[0].Script)

			if !changed {
				t.Fatalf("the model could not fix a script build123d refused. %s\n"+
					"That is the premise of this whole path: if it does not hold, the loop is "+
					"four model calls and a longer wait for the same missing part.", reply.Repaired)
			}
			// The acceptance rule already ran the kernel. Running it once more
			// here is the independent check that the rule means what it says.
			if _, err := kernel.RunScript(ctx, reply.Prototype.Parts[0].Script); err != nil {
				t.Fatalf("a script was accepted that does not build: %v", err)
			}
		})
	}
}

// liveRunner is the real kernel behind the agent's narrow interface — the same
// adapter httpapi uses, kept here so the live test exercises the real thing
// rather than a copy of the loop.
type liveRunner struct{ k *cad.Kernel }

func (r liveRunner) RunScript(ctx context.Context, source string) error {
	_, err := r.k.RunScript(ctx, source)
	return err
}

// TestLiveGearTurn is the user's actual path: ask a real model for a gear and
// check that what comes back BUILDS.
//
// # Why this exists beside the repair test above
//
// That one hands the model a script known to be broken. This one asks the
// question a person asks and lets the model choose everything — whether to reach
// for a script at all, what to write, and whether it needs fixing. It is the only
// test here that can fail because the CONTRACT is wrong rather than the loop.
//
// "make me a gear" is the exact prompt that produced, on the live deployment:
// shape "gear" (a word that does not exist, drawn as a bounding box) before the
// availability line, and then a script that ran and raised inside build123d.
func TestLiveGearTurn(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1 to run the live gear turn")
	}
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" {
		t.Skip("set FORGE_CAD_PYTHON to a Python with build123d")
	}
	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "gear-live-test"})
	kernel := cad.New(python, log).WithScripts(true)
	defer kernel.Close()

	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL:        envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen3.7-plus"),
		RequestTimeout: 3 * time.Minute,
		MaxRetries:     2,
	}, log, clock.System{})

	conv := agent.NewConversation(client, persona.DefaultCharacter()).
		WithScripts(liveRunner{kernel})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	reply, err := conv.Respond(ctx, "", nil,
		"Make me a 20-tooth involute spur gear, module 2, 6mm thick.", "", nil, nil)
	if err != nil {
		t.Fatalf("the turn failed: %v", err)
	}
	t.Logf("speech: %s", reply.Speech)
	t.Logf("notes:  %s", reply.Repaired)
	if reply.Prototype == nil {
		t.Fatal("the turn produced no geometry at all")
	}

	// Whatever it chose, every scripted part it kept must build. That is the
	// promise: the turn does not hand back a part it has been told cannot exist.
	scripted := 0
	for _, p := range reply.Prototype.Parts {
		t.Logf("part %q shape=%q scripted=%v", p.Label(), p.Shape, p.Script != "")
		if !strings.EqualFold(strings.TrimSpace(p.Shape), "script") || p.Script == "" {
			continue
		}
		scripted++
		if _, err := kernel.RunScript(ctx, p.Script); err != nil {
			t.Errorf("the turn kept a scripted part that does not build.\n"+
				"part: %s\nerror: %v\nnotes: %s\nscript:\n%s",
				p.Label(), err, reply.Repaired, p.Script)
		}
	}
	if scripted == 0 {
		// Not a failure. A gear the vocabulary can express without a script is a
		// better answer than one that needs one, and `repeat` exists for exactly
		// this. Logged so a run that never exercises the path says so, rather
		// than reporting a pass that measured nothing.
		t.Log("the model built this without a script, so this run did not exercise the " +
			"script path. Not a defect — but not evidence for it either.")
	}
}
