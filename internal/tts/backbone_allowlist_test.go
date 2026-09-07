package tts_test

import (
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/tts"
)

// The backbone allowlist exists in two places and they must agree.
//
// internal/tts owns it, because that package is what calls the vendor.
// platform/config duplicates the names so it can print `tts_trains_on_input` at
// startup without importing a vendor package — config sits below the domain and
// inverting that would be worse than the duplication.
//
// The duplication is only safe with this fence. A disagreement would print a
// reassurance that is FALSE: an operator reading `tts_trains_on_input: false`
// would believe the answers FORGE speaks are not trained on, while the provider
// happily called a backbone whose terms say otherwise. That is the exact shape
// of failure this codebase keeps writing fences against — a claim nobody
// checked, rendered as fact.
//
// The unknown-backbone case is asserted too, in both directions: the safe
// answer for a name neither table has heard of is "it trains".
func TestConfigAndProviderAgreeOnWhichBackbonesTrain(t *testing.T) {
	cases := []string{
		"s2.1-pro-free", // the free backbone: trains
		"s2.1-pro",      // paid: does not
		"s2-pro",        // paid: does not
		"s1",            // terms unchecked: must warn
		"",              // empty resolves to the default, which trains
		"some-backbone-that-does-not-exist",
	}

	for _, model := range cases {
		fromProvider := tts.TrainsOnRequests(model)

		cfg := config.TTSConfig{Provider: "fish", APIKey: "k", VoiceID: "v", Model: model}
		fromConfig, ok := config.TTSTrainsForTest(cfg).(bool)
		if !ok {
			t.Fatalf("config reported a non-boolean training status for %q", model)
		}

		if fromProvider != fromConfig {
			t.Errorf("model %q: internal/tts says trains=%v, platform/config prints trains=%v. "+
				"The startup line and the vendor call disagree, so one of them is telling an "+
				"operator something untrue about whether FORGE's speech is trained on.",
				model, fromProvider, fromConfig)
		}
	}
}

// An unknown backbone must resolve to "trains", not to "safe".
func TestAnUnknownBackboneIsTreatedAsTraining(t *testing.T) {
	if !tts.TrainsOnRequests("a-backbone-nobody-has-vetted") {
		t.Error("an unrecognised backbone was reported as NOT training on requests. " +
			"The allowlist must fail toward warning: over-warning costs caution nobody " +
			"needed, under-warning costs something that cannot be taken back.")
	}
}
