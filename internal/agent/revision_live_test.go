package agent_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
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

// TestLiveRevisionKeepsTheRestOfTheModel reshapes one part of a finished car
// and measures what happens to the twelve parts nobody asked about.
//
// # The failure this measures
//
// "Make the body less boxy" names one part out of thirteen. The model answers
// with a WHOLE prototype, so every part it does not retype is deleted — the
// document is the model, and omission is removal. That is not a bug in the
// wire format; it is what a rewrite means. The question is whether the model
// understands it.
//
// Measured 2026-09-09 against qwen3.7-plus, three runs from this exact
// document, BEFORE the contract said anything about it:
//
//	run 1 — the body came back under a different id, so the old body survived
//	        alongside the new one and the car had two bodies
//	run 2 — the spoiler (wing + both supports) was gone, unmentioned
//	run 3 — the spoiler was gone AND the new outline crossed itself, so the
//	        body did not build at all
//
// 0/3. Not one of those three was reported to the person, who had asked about
// the body and would have been looking at the body.
//
// # What it asserts, and what it does not
//
// The two failures are not the same KIND of thing, and are asserted
// differently:
//
//   - A silent disappearance is OUR bug. vanished.go is supposed to catch
//     every one of them, so a loss that reaches the reader unannounced means
//     that safety net has a hole. Any occurrence fails.
//   - Geometry that will not build is the MODEL having a bad day, and
//     georepair.go exists because it always will. Curing every slip is not
//     achievable, so this is counted and reported as a rate, and only a run
//     where NOTHING built is treated as a failure — that would mean the
//     contract is not working rather than a model missing once.
//
// It does NOT assert the body is prettier: "less boxy" has no machine test,
// and a test that guessed at one would fail on taste rather than on a
// regression here.
//
// Runs once by default. FORGE_LIVE_RUNS=n repeats it, because the build number
// above is a rate and a single green run is not evidence that a rate moved.
func TestLiveRevisionKeepsTheRestOfTheModel(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1 to run the live revision test")
	}
	base := loadPrototype(t, "testdata/finished-car.json")
	if len(base.Parts) < 10 {
		t.Fatalf("the fixture has %d parts; this test is about what a revision does to the "+
			"parts nobody mentioned, and needs a model with plenty of them", len(base.Parts))
	}

	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "revision-live-test"})
	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL: envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:  os.Getenv("FORGE_LLM_API_KEY"),
		// The same default the shipping config uses (config.go), so this
		// measures the model production actually talks to. A stale default here
		// measures a model nobody runs — or, as on 2026-09-09, one the provider
		// has retired, and every run fails for a reason that has nothing to do
		// with the behaviour under test.
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen3.7-plus"),
		RequestTimeout: 3 * time.Minute,
		MaxRetries:     2,
	}, log, clock.System{})
	conv := agent.NewConversation(client, persona.DefaultCharacter())

	runs := 1
	if n, err := strconv.Atoi(os.Getenv("FORGE_LIVE_RUNS")); err == nil && n > 0 {
		runs = n
	}
	var built int
	for i := 1; i <= runs; i++ {
		if revisionRun(t, conv, base, i) {
			built++
		}
	}
	t.Logf("RESULT: %d/%d revisions produced geometry that builds", built, runs)
	if built == 0 {
		t.Errorf("not one of %d revisions built. One model slip is expected and is what the "+
			"repair pass is for; none of them building means the contract is not working", runs)
	}
}

// revisionRun performs one revision. It FAILS the test outright on a silent
// loss, and returns whether the result builds so the caller can score that as
// a rate.
func revisionRun(t *testing.T, conv *agent.Conversation, base *geometry.Document, n int) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	reply, err := conv.Respond(ctx, "", nil, "make the body less boxy", "", base, nil)
	if err != nil {
		t.Errorf("run %d: the live turn failed: %v", n, err)
		return false
	}
	if reply.Prototype == nil && reply.PrototypeEdit == nil {
		t.Errorf("run %d: a revision request produced no geometry at all", n)
		return false
	}
	after := reply.Prototype
	if after == nil {
		t.Errorf("run %d: an edit reached the caller unresolved", n)
		return false
	}

	gone := missingParts(base, after)
	told := reply.Repaired

	if len(gone) > 0 {
		// Only unannounced losses count. A removal the reply names is a thing
		// the person can undo; a silent one is a thing they will find weeks later.
		var unannounced []string
		for _, name := range gone {
			if !strings.Contains(told, name) {
				unannounced = append(unannounced, name)
			}
		}
		if len(unannounced) > 0 {
			t.Errorf("run %d: %d part(s) vanished with no word to the reader: %s\nnotice was: %q",
				n, len(unannounced), strings.Join(unannounced, ", "), told)
		} else {
			t.Logf("run %d: removed %s — and said so", n, strings.Join(gone, ", "))
		}
	}

	faults := after.Faults()
	for _, f := range faults {
		// Logged, not failed: see the rate note on the parent test. The repair
		// pass has already had its go by the time this runs, so a fault here is
		// one it could not cure.
		t.Logf("run %d: does not build after repair: %s — %s", n, f.Name, f.Detail)
	}

	t.Logf("run %d: %d parts in, %d out, builds=%v; notice: %q; speech: %s",
		n, len(base.Parts), len(after.Parts), len(faults) == 0, told, reply.Speech)
	return len(faults) == 0
}

// missingParts names the parts of before that the after document neither keeps
// nor consumes as a cutting tool, by the label a person would recognise.
func missingParts(before, after *geometry.Document) []string {
	kept := map[string]bool{}
	for _, p := range after.Parts {
		kept[p.ID] = true
	}
	consumed := map[string]bool{}
	for _, f := range append(append([]geometry.Feature{}, before.Features...), after.Features...) {
		for _, id := range f.With {
			consumed[id] = true
		}
	}
	var gone []string
	for _, p := range before.Parts {
		if !kept[p.ID] && !consumed[p.ID] {
			gone = append(gone, p.Label())
		}
	}
	sort.Strings(gone)
	return gone
}

func loadPrototype(t *testing.T, path string) *geometry.Document {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var d geometry.Document
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatal(err)
	}
	return &d
}
