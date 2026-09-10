package agent_test

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/agent"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// TestLiveLook asks a real vision model to look at models whose faults are known
// exactly, and at one that is fine.
//
// # Why this cannot be a stub
//
// The whole premise of the visual loop is that a vision model can see a defect
// in a render of FORGE's own geometry. A stub returning findings proves the
// plumbing and nothing about the premise. If the model cannot see a wheel buried
// inside a body — in a picture where it is genuinely invisible — then the loop
// is an expensive way to add latency and should not ship.
//
// The false-positive case matters as much as the true-positive one: a checker
// that complains about a correct model will drive repairs that damage it, and
// this repository has already had to delete a rule that fired on the right
// answer.
func TestLiveLook(t *testing.T) {
	if os.Getenv("FORGE_LIVE_LLM_TESTS") == "" || os.Getenv("FORGE_LLM_API_KEY") == "" {
		t.Skip("set FORGE_LLM_API_KEY and FORGE_LIVE_LLM_TESTS=1 to run the live look test")
	}
	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "look-live-test"})
	client := llm.NewOpenAICompatible(config.LLMConfig{
		BaseURL:        envOrDefault("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"),
		APIKey:         os.Getenv("FORGE_LLM_API_KEY"),
		Converse:       envOrDefault("FORGE_LLM_CONVERSE_MODEL", "qwen3.7-plus"),
		Vision:         envOrDefault("FORGE_LLM_VISION_MODEL", "qwen3.8-max"),
		RequestTimeout: 3 * time.Minute,
		MaxRetries:     2,
	}, log, clock.System{})
	conv := agent.NewConversation(client, persona.DefaultCharacter())

	cases := []struct {
		name   string
		doc    string
		asked  string
		expect bool // should it find something?
		why    string
	}{
		{
			name:  "a wheel completely buried inside the body",
			asked: "a sports car",
			doc: `{"name":"Car","units":"mm","parts":[
			  {"id":"body","name":"Main Body","shape":"box","color":"#d92b2b",
			   "size":{"width":4500,"height":800,"depth":4500},"position":[0,400,0]},
			  {"id":"wheel-fl","name":"Front Left Wheel","shape":"cylinder","color":"#222222",
			   "size":{"radius":350,"depth":250},"position":[-950,350,1600],"rotation":[0,0,90]}]}`,
			expect: true,
			why:    "the wheel is inside a body 4500 wide and does not appear at all",
		},
		{
			name:  "a part floating far away from everything",
			asked: "a sports car",
			doc: `{"name":"Car","units":"mm","parts":[
			  {"id":"body","name":"Main Body","shape":"box","color":"#d92b2b",
			   "size":{"width":1900,"height":800,"depth":4500},"position":[0,400,0]},
			  {"id":"spoiler","name":"Spoiler","shape":"box","color":"#2b6bd9",
			   "size":{"width":1800,"height":60,"depth":400},"position":[0,4000,-2000]}]}`,
			expect: true,
			why:    "the spoiler is three metres above the car, touching nothing",
		},
		{
			name:  "the car as it actually is, which is fine",
			asked: "a sports car",
			doc: `{"name":"Car","units":"mm","parts":[
			  {"id":"body","name":"Main Body","shape":"box","color":"#d92b2b",
			   "size":{"width":1900,"height":800,"depth":4500},"position":[0,400,0]},
			  {"id":"cabin","name":"Cabin","shape":"box","color":"#222222",
			   "size":{"width":1600,"height":500,"depth":2000},"position":[0,1050,-200]},
			  {"id":"wfl","name":"Front Left Wheel","shape":"cylinder","color":"#222222","size":{"radius":350,"depth":250},"position":[-950,350,1600],"rotation":[0,0,90]},
			  {"id":"wfr","name":"Front Right Wheel","shape":"cylinder","color":"#222222","size":{"radius":350,"depth":250},"position":[950,350,1600],"rotation":[0,0,90]},
			  {"id":"wrl","name":"Rear Left Wheel","shape":"cylinder","color":"#222222","size":{"radius":350,"depth":250},"position":[-950,350,-1600],"rotation":[0,0,90]},
			  {"id":"wrr","name":"Rear Right Wheel","shape":"cylinder","color":"#222222","size":{"radius":350,"depth":250},"position":[950,350,-1600],"rotation":[0,0,90]}]}`,
			expect: false,
			why:    "this is the shipped model; a checker that complains here will damage good work",
		},
		{
			// ‼️ The blind spot. geometry.Tessellate does NOT perform a cut — its
			// own inferences say "four solid POSTS standing on the plate" — so a
			// bolt hole is drawn as a solid cylinder buried in the plate. That is
			// question 1 exactly: "a part completely hidden inside another part".
			//
			// It is also CORRECT. A cutting tool belongs inside the thing it
			// cuts. A checker that reports it fires on every mechanical part with
			// a hole in it, which is most of them.
			name:  "a bolt hole cut into a plate, which is what a hole looks like",
			asked: "a steel plate with a bolt hole",
			doc: `{"name":"Plate","units":"mm","parts":[
			  {"id":"plate","name":"Plate","shape":"box","color":"#8899aa",
			   "size":{"width":120,"height":10,"depth":80},"position":[0,0,0]},
			  {"id":"hole","name":"Bolt Hole","shape":"cylinder","color":"#222222",
			   "size":{"radius":5,"height":30},"position":[0,0,0]}],
			 "features":[{"id":"drill","op":"cut","of":"plate","with":["hole"]}]}`,
			expect: false,
			why: "the bolt hole is the TOOL that makes the hole, and being inside the plate " +
				"is what a hole is. Reporting it fires on every part with a hole in it",
		},
		{
			// ‼️ The other half, and the reason tools are NOT simply excluded.
			//
			// A cutting tool that floats clear of the part it cuts removes
			// nothing, so the hole is not there. That is a real defect and this
			// check is the only thing that would notice. It found exactly this on
			// the live deployment: "Bolt Hole 1: The part is floating clear of
			// the L-Bracket … rather than being positioned within the upright."
			//
			// If suppressing the false positive above also silenced this, the fix
			// would have traded a noisy check for a blind one.
			name:  "a bolt hole that MISSES the plate, so there is no hole",
			asked: "a steel plate with a bolt hole",
			doc: `{"name":"Plate","units":"mm","parts":[
			  {"id":"plate","name":"Plate","shape":"box","color":"#8899aa",
			   "size":{"width":120,"height":10,"depth":80},"position":[0,0,0]},
			  {"id":"hole","name":"Bolt Hole","shape":"cylinder","color":"#222222",
			   "size":{"radius":5,"height":30},"position":[0,300,0]}],
			 "features":[{"id":"drill","op":"cut","of":"plate","with":["hole"]}]}`,
			expect: true,
			why: "the tool is 300mm above the plate and cuts nothing, so the hole the " +
				"person asked for does not exist",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			doc := mustDoc(t, tc.doc)
			found, err := agent.LookForTest(ctx, conv, doc, tc.asked)
			if err != nil {
				// The trap this test exists to avoid: a vision model that is
				// misconfigured fails every call, and a swallowed error reads
				// as "looked, saw nothing" on a model with a wheel inside it.
				t.Fatalf("could not look at all: %v\nThis is NOT the same as seeing nothing. "+
					"Check FORGE_LLM_VISION_MODEL names a model this endpoint serves", err)
			}
			var lines []string
			for _, p := range found {
				lines = append(lines, p.Detail)
			}
			t.Logf("saw %d: %s", len(found), strings.Join(lines, " | "))

			switch {
			case tc.expect && len(found) == 0:
				t.Errorf("saw nothing. Expected it to notice: %s.\nIf a vision model cannot "+
					"see this in the picture, the loop is latency with no benefit", tc.why)
			case !tc.expect && len(found) > 0:
				t.Errorf("complained about a model that is fine (%s). A checker that fires on "+
					"the right answer drives repairs that damage good work.\nsaw: %s",
					tc.why, strings.Join(lines, " | "))
			}
		})
	}
}
