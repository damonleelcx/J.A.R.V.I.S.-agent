package httpapi

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// AUD-02 is measured from the end of the utterance (issue 19).
//
// > AUD-02 Median end-of-utterance → first audio ≤700 ms (≤1.5 s with
// > retrieval); show retrieval when slower
//
// # What was wrong
//
// The browser's clock started at SEND. Everything between a person finishing
// speaking and the request leaving the page was outside the figure — the
// recogniser settling, and on the deployment's own transcription path a whole
// /v1/transcribe round trip, which is the slowest part of the whole thing. The
// number compared against 700 ms was a different quantity from the one the
// requirement names, and it read low. converse_stream.go was already careful to
// say "measured, not targeted", and deliberation_test.go named the failure
// directly: "and nothing fails, so nothing notices".
//
// # What these fence
//
// The MEASUREMENT POINTS, not a wall-clock threshold. A fence on a duration
// would fail on a slow laptop and pass on a fast one without either telling you
// anything about whether the right two moments are being subtracted. So: that
// the clock starts at the release of the hold and not at the arrival of the
// text, that a turn with no utterance reports nothing rather than zero, and that
// the panel reports the median and the retrieval case AUD-02 separates.

// Cause: the server path timed from the transcript, so the round trip was free.
func TestVoiceLatency_TheServerPathTimesFromTheHoldNotTheTranscript(t *testing.T) {
	runVoiceScenario(t, `
const FV = load();
const said = [];
const v = new FV.Voice({
  onTranscript: function (text, endedAt) { said.push({ text: text, endedAt: endedAt }); },
  onError: function () {}, onState: function () {}
});
v.setServerTranscription({ model: 'asr-test' });

v.startListening();
await settle();
const released = performance.now();
v.stopListening();
// The transcription round trip. On a real deployment this is the model, over a
// network; here it is enough that measurable time passes before the words land.
const spin = performance.now();
while (performance.now() - spin < 30) { /* the wait a person actually feels */ }
await settle();

expect(said.length === 1, 'the transcript never arrived: ' + JSON.stringify(said));
const got = said[0] || {};
expect(got.endedAt != null, 'the transcript carries no end-of-utterance moment, so AUD-02 cannot ' +
  'be measured at all — which is the state issue 19 found');
expect(Math.abs(got.endedAt - released) < 15,
  'the utterance is recorded as ending ' + Math.round(got.endedAt - released) + 'ms after the ' +
  'button was released. It must be the RELEASE: the upload and the provider are time the ' +
  'person spent waiting after they stopped speaking, and AUD-02 counts it');
expect(got.endedAt < performance.now() - 25,
  'the end of the utterance is the moment the TEXT arrived. That is exactly the gap the old ' +
  'figure omitted, moved one function along');
`)
}

// The browser path: a final result ends the utterance, and a hold ends it earlier.
func TestVoiceLatency_TheBrowserPathEndsTheUtteranceWhenSpeechEnds(t *testing.T) {
	t.Run("hands-free: the recogniser's final result", runVoice(`
const FV = load();
const said = [];
const v = new FV.Voice({
  onTranscript: function (t, endedAt) { said.push(endedAt); },
  onError: function () {}, onState: function () {}
});
v.setServerTranscription(null);
v.setMode('hands-free');
v.startListening();
await settle();
const before = performance.now();
world.hear('make the wall thicker', true);
await settle();
expect(said.length === 1, 'nothing was delivered: ' + said.length);
expect(said[0] != null, 'a hands-free transcript carries no end-of-utterance moment. There is no ' +
  'button to release, so the recogniser calling the result FINAL is the only end there is');
expect(said[0] >= before - 5, 'the end of the utterance predates the final result by ' +
  Math.round(before - said[0]) + 'ms');
`))

	t.Run("push-to-talk: the release, not the final result", runVoice(`
const FV = load();
const said = [];
const v = new FV.Voice({
  onTranscript: function (t, endedAt) { said.push(endedAt); },
  onError: function () {}, onState: function () {}
});
v.setServerTranscription(null);
v.startListening();
await settle();
const released = performance.now();
v.stopListening();
const spin = performance.now();
while (performance.now() - spin < 30) { /* the recogniser settling */ }
world.hear('make the wall thicker', true);
await settle();
expect(said.length === 1, 'nothing was delivered: ' + said.length);
expect(Math.abs(said[0] - released) < 15,
  'a held transcript is timed from the recogniser finalising, ' +
  Math.round(said[0] - released) + 'ms after the button came up. The person stopped speaking ' +
  'and let go; everything after that is what AUD-02 measures');
`))
}

// A transcript that waited in the text box reports NO figure, not a huge one.
//
// The gap between speaking and pressing send is somebody reading a reply and
// deciding. That is not latency, and averaging it in as though it were would
// make the panel's median meaningless in exactly the sessions where somebody
// was talking over a long answer.
func TestVoiceLatency_ATranscriptKeptInTheBoxCarriesNoMeasurement(t *testing.T) {
	runVoiceScenario(t, `
const FV = load();
const sends = [];
const box = { value: '' };
const kept = FV.deliverSpoken('make it taller', {
  busy: true, input: box, note: function () {},
  send: function (text, endedAt) { sends.push({ text: text, endedAt: endedAt }); },
  endedAt: performance.now()
});
expect(kept === 'kept', 'a transcript arriving during a turn was not kept in the box: ' + kept);
expect(sends.length === 0, 'it was sent anyway');
expect(box.value === 'make it taller', 'the box holds ' + JSON.stringify(box.value));

const now = performance.now();
const out = FV.deliverSpoken('make it taller', {
  busy: false, input: box, note: function () {},
  send: function (text, endedAt) { sends.push({ text: text, endedAt: endedAt }); },
  endedAt: now
});
expect(out === 'sent', 'a transcript arriving between turns was not sent: ' + out);
expect(sends.length === 1 && sends[0].endedAt === now,
  'deliverSpoken dropped the end-of-utterance moment on the way to send(), so every spoken ' +
  'turn would be measured from Send again: ' + JSON.stringify(sends));
`)
}

func runVoice(scenario string) func(*testing.T) {
	return func(t *testing.T) { runVoiceScenario(t, scenario) }
}

// figure renders a measurement that may not exist, for a failure message.
//
// "nothing" rather than a nil pointer's address, and rather than 0 — the same
// distinction the panel itself draws with an em dash.
func figure(v *float64) string {
	if v == nil {
		return "nothing"
	}
	return strconv.FormatFloat(*v, 'f', -1, 64) + "ms"
}

// ---- the workbench's two clocks -------------------------------------------

// The workbench times a spoken turn from the end of the utterance, and a typed
// one from nothing at all.
//
// audioClock is the join between voice.js's moment and the Telemetry panel's
// figures. Without this, the whole chain could be right at both ends and still
// record the wrong subtraction in the middle.
func TestWorkbenchTimesATurnFromTheEndOfTheUtterance(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; this fence drives workbench.js in node")
	}
	js, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	harness := filepath.Join(dir, "harness.js")
	asset := filepath.Join(dir, "workbench.js")
	script := `
  const fs = require('fs');
  const src = fs.readFileSync(process.argv[2], 'utf8');
  function lift(src, name) {
    const at = src.indexOf('function ' + name + '(');
    if (at < 0) throw new Error('workbench.js has no function ' + name);
    for (let k = src.indexOf('{', at), depth = 0; k < src.length; k++) {
      if (src[k] === '{') depth++;
      else if (src[k] === '}' && --depth === 0) return src.slice(at, k + 1);
    }
    throw new Error('unbalanced function ' + name);
  }
  const made = new Function(lift(src, 'audioClock') + '; return audioClock;')();
  // A spoken turn: speech ended at 1000, Send at 1300, audible at 2000.
  const spoken = { audioMS: null, spokenMS: null };
  made(spoken, 1300, 1000)(2000);
  // A typed turn: no utterance.
  const typed = { audioMS: null, spokenMS: null };
  made(typed, 1300, null)(2000);
  // Only the first moment counts: a second speech event is a later sentence of
  // the same reply, not a second first-audio.
  const twice = { audioMS: null, spokenMS: null };
  const c = made(twice, 1300, 1000);
  c(2000); c(5000);
  process.stdout.write(JSON.stringify({ spoken: spoken, typed: typed, twice: twice }));
`
	for path, data := range map[string][]byte{harness: []byte(script), asset: js} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(node, harness, asset)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("workbench.js could not be driven: %v\n%s", err, stderr.String())
	}
	var got struct {
		Spoken, Typed, Twice struct{ AudioMS, SpokenMS *float64 }
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable harness output: %s (%v)", out, err)
	}
	// Printed through a helper: a *float64 in a %v is an address, and a fence
	// whose failure message is a pointer teaches nobody anything.
	if got.Spoken.SpokenMS == nil || *got.Spoken.SpokenMS != 1000 {
		t.Errorf("a turn whose speech ended at 1000 and was answered aloud at 2000 reports %s "+
			"from the end of the utterance, want 1000.\nThis is AUD-02's own figure; measuring it "+
			"from Send instead gives 700, which is the number that used to be compared against "+
			"a 700ms threshold it was not measuring.", figure(got.Spoken.SpokenMS))
	}
	if got.Spoken.AudioMS == nil || *got.Spoken.AudioMS != 700 {
		t.Errorf("the Send-based figure is %s, want 700. Both are kept: the DIFFERENCE between "+
			"them is what the old measurement was hiding.", figure(got.Spoken.AudioMS))
	}
	if got.Typed.SpokenMS != nil {
		t.Errorf("a typed turn reports %s from the end of an utterance it never had. It must be "+
			"null: a zero is the best-looking number on the panel.", figure(got.Typed.SpokenMS))
	}
	if got.Typed.AudioMS == nil || *got.Typed.AudioMS != 700 {
		t.Errorf("a typed turn's Send-based figure is %s, want 700", figure(got.Typed.AudioMS))
	}
	if got.Twice.SpokenMS == nil || *got.Twice.SpokenMS != 1000 {
		t.Errorf("a second speech event overwrote first-audio: %s. Later sentences of one reply "+
			"are not a second first.", figure(got.Twice.SpokenMS))
	}
}

// ---- the panel ------------------------------------------------------------

// telemetryHarness lifts the Telemetry panel out of stage.js and renders it.
//
// Lifted rather than loaded whole: stage.js is a page module that wants a
// document, and what is being fenced is which figures the panel computes from a
// list of turns — which is a pure function of `state.turns` and the three
// helpers it shares.
const telemetryHarness = `
  const fs = require('fs');
  const src = fs.readFileSync(process.argv[2], 'utf8');
  const input = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
  function lift(src, name) {
    const at = src.indexOf('function ' + name + '(');
    if (at < 0) throw new Error('stage.js has no function ' + name);
    for (let k = src.indexOf('{', at), depth = 0; k < src.length; k++) {
      if (src[k] === '{') depth++;
      else if (src[k] === '}' && --depth === 0) return src.slice(at, k + 1);
    }
    throw new Error('unbalanced function ' + name);
  }
  const names = ['esc', 'empty', 'problem', 'median', 'ms', 'stat',
                 'turnHTML', 'historyRow', 'renderHistory', 'renderTelemetry'];
  const body = document.body;
  const made = new Function('$', 'state',
    names.map((n) => lift(src, n)).join('\n') + '; return { renderTelemetry: renderTelemetry };')(
    (id) => (id === 'telemetry-body' ? body : null),
    { turns: input.turns, history: null, models: null, panel: 'telemetry' });
  made.renderTelemetry();
  process.stdout.write(JSON.stringify({ html: body.innerHTML,
    text: body.innerHTML.replace(/<[^>]*>/g, ' ').replace(/&amp;/g, '&').replace(/\s+/g, ' ').trim() }));
`

func renderTelemetryPanel(t *testing.T, turns []map[string]any) (string, string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; the telemetry panel fence runs stage.js in node")
	}
	src, err := assetFS.ReadFile("assets/stage.js")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := json.Marshal(map[string]any{"turns": turns})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	asset := filepath.Join(dir, "stage.js")
	harness := filepath.Join(dir, "harness.js")
	input := filepath.Join(dir, "input.json")
	// `document` is a single element with an innerHTML: the panel writes to one.
	prelude := "globalThis.document = { body: { innerHTML: '' } };\n"
	for path, data := range map[string][]byte{
		asset: src, harness: []byte(prelude + telemetryHarness), input: spec,
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(node, harness, asset, input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the Telemetry panel could not be rendered: %v\n%s", err, stderr.String())
	}
	var got struct{ HTML, Text string }
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unreadable harness output: %s (%v)", out, err)
	}
	return got.HTML, got.Text
}

// The panel reports AUD-02's own median, and the retrieval case beside it.
//
// Both numbers, because AUD-02 gives retrieval its own threshold (1.5s against
// 700ms). One median over both would answer neither question, and a panel that
// reported only the plain case would look worst exactly on the turns the
// requirement expects to be slower.
func TestTelemetryPanelReportsTheEndOfUtteranceMedianAndTheRetrievalCase(t *testing.T) {
	turns := []map[string]any{
		// Spoken, no retrieval: 600 and 800 → median 700.
		{"prompt": "a", "at": "10:00:00 UTC", "spokenMS": 600, "audioMS": 200, "retrieval": false},
		{"prompt": "b", "at": "10:00:01 UTC", "spokenMS": 800, "audioMS": 300, "retrieval": false},
		// Spoken, with retrieval: 1200 and 1400 → median 1300.
		{"prompt": "c", "at": "10:00:02 UTC", "spokenMS": 1200, "audioMS": 400, "retrieval": true},
		{"prompt": "d", "at": "10:00:03 UTC", "spokenMS": 1400, "audioMS": 500, "retrieval": true},
		// Typed: no utterance to end. Must be in NEITHER median.
		{"prompt": "e", "at": "10:00:04 UTC", "spokenMS": nil, "audioMS": 50, "retrieval": false},
	}
	html, text := renderTelemetryPanel(t, turns)

	for _, want := range []string{"end of utterance to first audio", "700ms", "1.5s", "1300ms"} {
		if !strings.Contains(text, want) {
			t.Errorf("the Telemetry panel does not show %q.\n"+
				"AUD-02 is the most-cited latency requirement in this project and its own figure "+
				"has to be on the panel where latency is read, with the retrieval case beside it.\n"+
				"panel: %s", want, text)
		}
	}
	// The plain median is 700 over 600 and 800 — NOT 1000, which is what it
	// becomes if a typed turn is counted as zero, and not 1000 either if the
	// retrieval turns are folded in.
	if !strings.Contains(text, "n=2, AUD-02 names 700ms") {
		t.Errorf("the plain median is not taken over the two spoken turns that did not retrieve.\n"+
			"A typed turn counted as zero would be the best-looking number on the panel.\npanel: %s",
			text)
	}
	if !strings.Contains(text, "n=2, AUD-02 names 1.5s") {
		t.Errorf("the retrieval median is not taken over the two turns that quoted memory: %s", text)
	}
	// And the Send-based figure is still there, named for what it is, because
	// the DIFFERENCE between the two is what the old number was hiding.
	if !strings.Contains(text, "Send to first audio") {
		t.Errorf("the panel dropped the Send-based figure instead of naming it. Keeping both is "+
			"what makes the gap the old measurement omitted visible: %s", text)
	}
	if !strings.Contains(html, "from end of speech") {
		t.Errorf("no turn row carries its own end-of-utterance figure: %s", html)
	}
}

// A session of typed turns reports no AUD-02 figure at all.
//
// Not a zero, and not a small number. A typed turn never had an utterance, and a
// panel that scored one against a voice requirement would report a perfect
// result for a person who has never spoken to it.
func TestTelemetryPanelReportsNothingForASessionThatNeverSpoke(t *testing.T) {
	_, text := renderTelemetryPanel(t, []map[string]any{
		{"prompt": "a", "at": "10:00:00 UTC", "spokenMS": nil, "audioMS": 120, "retrieval": false},
		{"prompt": "b", "at": "10:00:01 UTC", "spokenMS": nil, "audioMS": 140, "retrieval": true},
	})
	if !strings.Contains(text, "no spoken turn has been answered aloud") {
		t.Errorf("a session with no spoken turn does not say so: %s", text)
	}
	// A figure nothing measured is an em dash. Matched with the leading space so
	// this does not fire on an honest 130ms elsewhere on the panel.
	if strings.Contains(text, " 0ms") {
		t.Errorf("a turn nothing measured is drawn as 0ms, which reads as instant: %s", text)
	}
	if !strings.Contains(text, "median end of utterance to first audio no spoken turn") {
		t.Errorf("the AUD-02 median is not drawn as an em dash with a reason beside it: %s", text)
	}
}
