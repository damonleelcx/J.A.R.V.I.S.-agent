package httpapi

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The workbench microphone, driven for real.
//
// # What was wrong
//
// The owner held the round mic button and nothing reached FORGE. Four causes,
// each silent on its own:
//
//  1. Push-to-talk used only the browser's SpeechRecognition, which in Chrome
//     streams audio to Google — unreachable from mainland China. The server had
//     a transcriber (rooms use it) and the workbench never called it.
//  2. A transcript that arrived while a turn was in flight hit send()'s
//     `if (state.busy) return` and was dropped.
//  3. The hold ended on `mouseleave`, so drifting off a 36px circle ended it,
//     and a quick second press threw InvalidStateError inside a try/catch that
//     swallowed it.
//  4. `no-speech`, `aborted` and the swallowed start error showed nothing.
//
// # Why node and not a string search
//
// Every one of those looked correct at its call site. What breaks is the
// sequence — press, release, press again, the session ending later — so these
// run the real voice.js against a stubbed browser and watch what it DOES.
//
// See docs/bugfix/2026-09-15-the-microphone-sent-nothing.md

// voiceWorld is the browser these scenarios run in.
//
// ‼️ The fakes model the behaviour that caused the bug, not an idealised one:
// the recogniser throws InvalidStateError on a start() while its previous
// session is still open, and that session ends only when the scenario says so —
// which is what Chrome does, on its own schedule, after stop().
const voiceWorld = `
'use strict';
const fs = require('fs'), path = require('path');
const dir = process.argv[2];
const failures = [];
function expect(cond, why) { if (!cond) failures.push(why); }
function finish() {
  if (failures.length) { console.log(failures.map(function (f) { return '  - ' + f; }).join('\n')); process.exit(1); }
}
function crash(e) {
  console.log('the scenario could not run against this voice.js: ' + ((e && e.stack) || e));
  process.exit(2);
}
async function settle() { for (let i = 0; i < 30; i++) await new Promise(function (r) { setImmediate(r); }); }

const world = {
  gumCalls: 0, gumError: null, tracksStopped: 0,
  recorders: [], recordBytes: [1, 2, 3, 4, 5, 6, 7, 8],
  fetches: [], fetchError: null,
  reply: { status: 200, body: { text: 'make the wall thicker', model: 'asr-test' } },
  recognitions: [], srStarts: 0
};

function fakeStream() {
  const track = { kind: 'audio', label: 'Test microphone',
    stop: function () { world.tracksStopped++; }, getSettings: function () { return {}; } };
  return { getTracks: function () { return [track]; }, getAudioTracks: function () { return [track]; } };
}

function FakeMediaRecorder(stream, opts) {
  this.stream = stream;
  this.mimeType = (opts && opts.mimeType) || 'audio/webm';
  this.state = 'inactive';
  world.recorders.push(this);
}
FakeMediaRecorder.isTypeSupported = function (t) { return t === 'audio/webm;codecs=opus' || t === 'audio/webm'; };
FakeMediaRecorder.prototype.start = function () { this.state = 'recording'; };
FakeMediaRecorder.prototype.stop = function () {
  if (this.state === 'inactive') { const e = new Error('not recording'); e.name = 'InvalidStateError'; throw e; }
  this.state = 'inactive';
  const self = this, bytes = Uint8Array.from(world.recordBytes);
  setImmediate(function () {
    if (self.ondataavailable) self.ondataavailable({ data: new Blob(bytes.length ? [bytes] : [], { type: self.mimeType }) });
    if (self.onstop) self.onstop();
  });
};

function FakeRecognition() { this.active = false; world.recognitions.push(this); }
FakeRecognition.prototype.start = function () {
  if (this.active) { const e = new Error('recognition has already started'); e.name = 'InvalidStateError'; throw e; }
  this.active = true; world.srStarts++;
};
FakeRecognition.prototype.stop = function () { /* ends later, on the browser's schedule */ };
FakeRecognition.prototype.abort = function () { /* likewise */ };
world.endRecognition = function () { const r = world.recognitions[0]; r.active = false; if (r.onend) r.onend(); };
// hear delivers one recognition result, as Chrome does while a session is open.
world.hear = function (text, isFinal) {
  const r = world.recognitions[0], res = [[{ transcript: text }]];
  res[0].isFinal = !!isFinal;
  if (r.onresult) r.onresult({ resultIndex: 0, results: res });
};
world.recognitionError = function (code) { const r = world.recognitions[0]; if (r.onerror) r.onerror({ error: code }); };

const win = {
  addEventListener: function () {}, removeEventListener: function () {},
  setTimeout: setTimeout, clearTimeout: clearTimeout, setInterval: setInterval, clearInterval: clearInterval,
  console: console, Blob: Blob, MediaRecorder: FakeMediaRecorder,
  navigator: { mediaDevices: {
    getUserMedia: function () {
      world.gumCalls++;
      return world.gumError ? Promise.reject(world.gumError) : Promise.resolve(fakeStream());
    },
    enumerateDevices: function () { return Promise.resolve([]); }
  } },
  fetch: function (url, init) {
    world.fetches.push({ url: url, init: init || {} });
    if (world.fetchError) return Promise.reject(world.fetchError);
    const r = typeof world.reply === 'function' ? world.reply(world.fetches.length) : world.reply;
    return Promise.resolve({ ok: r.status >= 200 && r.status < 300, status: r.status,
      headers: { get: function () { return null; } },
      json: function () { return Promise.resolve(r.body); } });
  }
};

function load(opts) {
  opts = opts || {};
  if (opts.recognition !== false) win.webkitSpeechRecognition = FakeRecognition;
  // voice.js reads navigator as a free variable, so the fake has to be visible
  // globally. ‼️ Plain assignment works only on Node 20 and older: Node 21 added
  // a real navigator global, defined as an accessor with a getter and no
  // setter, so assigning globalThis.navigator throws
  //   TypeError: Cannot set property navigator of #<Object> which has only a getter
  // and every scenario in this file fails with "the scenario could not run".
  // defineProperty replaces the accessor with a plain value on both, and the
  // property is configurable on Node 21+ so redefining it is allowed.
  // ‼️ No backticks in this comment: the whole harness is a Go raw string.
  Object.defineProperty(globalThis, 'navigator', {
    value: win.navigator, configurable: true, writable: true
  });
  ['audio-input.js', 'voice.js'].forEach(function (name) {
    new Function('window', fs.readFileSync(path.join(dir, name), 'utf8'))(win);
  });
  return win.ForgeVoice;
}

function makeVoice(FV) {
  const out = { said: [], errors: [], states: [] };
  out.v = new FV.Voice({
    onTranscript: function (t) { out.said.push(t); },
    onError: function (m) { out.errors.push(m); },
    onState: function (s) { out.states.push(Object.assign({}, s)); }
  });
  return out;
}

function fakeButton() {
  const on = {};
  return {
    disabled: false, captured: null,
    addEventListener: function (t, fn) { (on[t] = on[t] || []).push(fn); },
    setPointerCapture: function (id) { this.captured = id; },
    releasePointerCapture: function () { this.captured = null; },
    hasPointerCapture: function (id) { return this.captured === id; },
    fire: function (t, e) {
      e = Object.assign({ type: t, preventDefault: function () {} }, e || {});
      (on[t] || []).forEach(function (fn) { fn(e); });
    }
  };
}
`

// renderWorkbench is the workbench page as served.
func renderWorkbench(t *testing.T) string {
	t.Helper()
	rr := httptest.NewRecorder()
	NewPageHandlers(testDeps()).Workbench(rr, httptest.NewRequest(http.MethodGet, "/workbench", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("the workbench page answered %d", rr.Code)
	}
	return rr.Body.String()
}

// runVoiceScenario runs one scenario against the embedded voice.js.
func runVoiceScenario(t *testing.T, scenario string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; the voice input fences run voice.js in node")
	}
	dir := t.TempDir()
	for _, name := range []string{"audio-input.js", "voice.js"} {
		b, err := assetFS.ReadFile("assets/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(dir, "scenario.js")
	src := voiceWorld + "\n(async function () {\n" + scenario + "\n})().then(finish, crash);\n"
	if err := os.WriteFile(script, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, script, dir)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("\n%s\n(%v)", strings.TrimSpace(out.String()), err)
	}
}

// Cause 1: push-to-talk must record and upload when the deployment transcribes.
func TestVoiceInput_PushToTalkRecordsAndUploadsToTheServer(t *testing.T) {
	runVoiceScenario(t, `
const FV = load();
const m = makeVoice(FV), v = m.v;
v.setServerTranscription({ model: 'asr-test' });
expect(v.inputPath() === 'server', 'push-to-talk does not use the server on a deployment that transcribes (path ' + v.inputPath() + ')');

v.startListening();
await settle();
expect(world.srStarts === 0, 'the hold started the browser recogniser — in Chrome that streams to Google, which mainland China cannot reach');
expect(world.gumCalls === 1, 'the microphone was not opened to record');
expect(world.recorders.length === 1 && world.recorders[0].state === 'recording', 'nothing is recording while the button is held');

v.stopListening();
await settle();
expect(m.states.some(function (s) { return s.transcribing; }), 'no transcribing state between release and the text arriving');
expect(world.fetches.length === 1, 'the recording was not uploaded (' + world.fetches.length + ' requests)');
const f = world.fetches[0] || { init: {} };
expect(f.url === '/v1/transcribe' && f.init.method === 'POST', 'the recording went to ' + f.url + ' ' + f.init.method);
const ct = f.init.headers && f.init.headers['Content-Type'];
expect(/^audio\/webm/.test(ct || ''), 'the upload does not carry the recording content type: ' + ct);
expect(f.init.body instanceof Blob && f.init.body.size === 8, 'the upload body is not the recorded audio');
expect(m.said.length === 1 && m.said[0] === 'make the wall thicker', 'the transcript never reached the conversation: ' + JSON.stringify(m.said));
expect(world.tracksStopped >= 1, 'the microphone was left open after the hold ended');
expect(!v.transcribing && !v.listening, 'still claims to be listening or transcribing after the text arrived');
expect(m.errors.length === 0, 'unexpected notes: ' + JSON.stringify(m.errors));
`)
}

// If neither path can work the mic says so, and the path in use is named.
func TestVoiceInput_TheMicUsesAPathThatCanWorkAndSaysWhich(t *testing.T) {
	t.Run("browser recognition when the server has no transcriber", func(t *testing.T) {
		runVoiceScenario(t, `
const FV = load();
const v = makeVoice(FV).v;
v.setServerTranscription(null);
expect(v.inputPath() === 'browser', 'path ' + v.inputPath() + ', want browser');
expect(/Google/.test(v.describePath()), 'the page does not say the browser path sends audio to Google: ' + v.describePath());
v.startListening();
await settle();
expect(world.srStarts === 1, 'the browser recogniser did not start');
expect(world.fetches.length === 0, 'an upload was attempted with no server transcriber');
`)
	})
	t.Run("the server path names its model", func(t *testing.T) {
		runVoiceScenario(t, `
const FV = load();
const v = makeVoice(FV).v;
v.setServerTranscription({ model: 'qwen3-asr-flash-2026-02-10' });
expect(/qwen3-asr-flash-2026-02-10/.test(v.describePath()), 'the page does not say which path is in use: ' + v.describePath());
`)
	})
	t.Run("neither path says what would turn one on", func(t *testing.T) {
		runVoiceScenario(t, `
const FV = load({ recognition: false });
const v = makeVoice(FV).v;
v.setServerTranscription(null);
expect(v.inputPath() === 'none', 'path ' + v.inputPath() + ', want none');
expect(/FORGE_LLM_TRANSCRIBER_MODEL/.test(v.whyUnavailable()), 'the disabled mic does not name the setting that enables it: ' + v.whyUnavailable());
`)
	})
}

// Cause 4, and every failure the new path adds: each one produces a note.
func TestVoiceInput_EveryFailureReachesTheNote(t *testing.T) {
	serverHold := `
const FV = load();
const m = makeVoice(FV), v = m.v;
v.setServerTranscription({ model: 'asr-test' });
async function hold() { v.startListening(); await settle(); v.stopListening(); await settle(); }
function noted(re) { return m.errors.some(function (e) { return re.test(e); }); }
`
	browserHold := `
const FV = load();
const m = makeVoice(FV), v = m.v;
v.setServerTranscription(null);
function noted(re) { return m.errors.some(function (e) { return re.test(e); }); }
`
	for _, tc := range []struct{ name, scenario string }{
		{"permission_refused", serverHold + `
world.gumError = Object.assign(new Error('Permission denied'), { name: 'NotAllowedError' });
await hold();
expect(noted(/refused/i), 'a refused microphone left no readable note: ' + JSON.stringify(m.errors));
expect(world.fetches.length === 0, 'something was uploaded without a microphone');
expect(!v.listening && !v.transcribing, 'the mic still claims to be listening or transcribing');
`},
		{"nothing_captured", serverHold + `
world.recordBytes = [];
await hold();
expect(noted(/no audio/i), 'an empty recording left no note: ' + JSON.stringify(m.errors));
expect(world.fetches.length === 0, 'an empty recording was uploaded');
`},
		{"provider_error", serverHold + `
world.reply = { status: 503, body: { code: 'EXTERNAL_UNAVAILABLE', message: 'An external service could not be reached or returned a server error.', request_id: 'req_1' } };
await hold();
expect(noted(/could not be reached/), 'a provider failure left no readable note: ' + JSON.stringify(m.errors));
expect(m.said.length === 0, 'a failure was sent as a transcript');
expect(!v.transcribing, 'still transcribing after the failure');
`},
		{"not_served_here", serverHold + `
world.reply = { status: 501, body: { code: 'CONNECTOR_UNAVAILABLE', message: 'Not available here.',
  details: { detail: 'This endpoint currently serves: qwen3.7-plus. Set FORGE_LLM_TRANSCRIBER_MODEL to one of those' } } };
await hold();
expect(noted(/FORGE_LLM_TRANSCRIBER_MODEL/), 'the setting that fixes it never reached the page: ' + JSON.stringify(m.errors));
expect(v.inputPath() !== 'server', 'the server path is still used after the deployment said it has no transcriber');
`},
		{"network", serverHold + `
world.fetchError = new TypeError('Failed to fetch');
await hold();
expect(noted(/reach the server/i), 'a network failure left no note: ' + JSON.stringify(m.errors));
expect(!v.transcribing, 'still transcribing after the network failed');
`},
		{"nothing_recognised", serverHold + `
world.reply = { status: 200, body: { text: '   ', model: 'asr-test' } };
await hold();
expect(noted(/no words/i), 'a recording with no words left no note: ' + JSON.stringify(m.errors));
expect(m.said.length === 0, 'an empty transcript was sent');
`},
		{"browser_recognition_blocked", browserHold + `
v.startListening();
world.recognitionError('network');
await settle();
expect(noted(/Google/), 'a blocked recogniser did not say why: ' + JSON.stringify(m.errors));
expect(v.inputPath() === 'none', 'the mic still offers a recogniser that cannot reach its service (path ' + v.inputPath() + ')');
`},
		// ‼️ service-not-allowed is the browser refusing its own speech service,
		// not the person refusing the microphone. Read as a refusal it said
		// "Microphone access was refused" and turned the server path off too.
		{"service_not_allowed_is_not_a_refused_microphone", serverHold + `
world.recognitionError('service-not-allowed');
await settle();
expect(!noted(/refused/i), 'the browser refusing its speech service was reported as a refused microphone: ' + JSON.stringify(m.errors));
expect(noted(/service-not-allowed/), 'service-not-allowed left no note that names it: ' + JSON.stringify(m.errors));
expect(v.inputPath() === 'server', 'the server path was turned off by the browser recogniser (path ' + v.inputPath() + ')');
expect(!v.handsFreeAvailable(), 'hands-free is still offered on a recogniser whose service is not allowed');
`},
		{"no_speech_while_holding", browserHold + `
v.startListening();
world.recognitionError('no-speech');
await settle();
expect(noted(/no speech/i), 'no-speech during a hold is silent: ' + JSON.stringify(m.errors));
`},
	} {
		t.Run(tc.name, func(t *testing.T) { runVoiceScenario(t, tc.scenario) })
	}
}

// Cause 3b: a second press before the last session ended must still listen.
func TestVoiceInput_PressingAgainBeforeTheLastSessionEndedStillListens(t *testing.T) {
	t.Run("browser_recognition", func(t *testing.T) {
		runVoiceScenario(t, `
const FV = load();
const m = makeVoice(FV), v = m.v;
v.setServerTranscription(null);
v.startListening();
v.stopListening();      // released; Chrome has not ended the session yet
v.startListening();     // pressed again straight away
world.endRecognition(); // the first session ends on the browser's schedule
await settle();
expect(world.srStarts === 2, 'the second press did not listen (' + world.srStarts + ' starts): InvalidStateError was swallowed and the press did nothing');
expect(v.listening, 'the second press is not listening');
`)
	})
	t.Run("server_recording", func(t *testing.T) {
		runVoiceScenario(t, `
const FV = load();
const m = makeVoice(FV), v = m.v;
v.setServerTranscription({ model: 'asr-test' });
world.reply = function (n) { return { status: 200, body: { text: n === 1 ? 'first' : 'second' } }; };
v.startListening(); await settle();
v.stopListening();              // upload not finished
v.startListening(); await settle();
expect(world.recorders.length === 2 && world.recorders[1].state === 'recording', 'the second press is not recording');
v.stopListening(); await settle();
expect(JSON.stringify(m.said) === '["first","second"]', 'two holds did not arrive, in order: ' + JSON.stringify(m.said));
`)
	})
}

// Cause 3a: drifting off the button must not end the hold.
func TestVoiceInput_TheHoldSurvivesTheCursorLeavingTheButton(t *testing.T) {
	runVoiceScenario(t, `
const FV = load();
const v = makeVoice(FV).v;
const calls = [];
v.startListening = function () { calls.push('start'); };
v.stopListening = function () { calls.push('stop'); };
v.cancelListening = function () { calls.push('cancel'); };
let now = 1000;
const notes = [];
const btn = fakeButton();
FV.bindHold(btn, v, { now: function () { return now; }, note: function (s) { notes.push(s); } });

btn.fire('pointerdown', { pointerId: 7, button: 0 });
expect(btn.captured === 7, 'the button does not capture the pointer, so the hold ends when the cursor drifts off it');
now += 2000;
btn.fire('pointerleave', { pointerId: 7 });
btn.fire('pointerout', { pointerId: 7 });
btn.fire('mouseleave', {});
expect(calls.join() === 'start', 'leaving the button ended the hold: ' + calls.join());
btn.fire('pointerup', { pointerId: 7, button: 0 });
btn.fire('lostpointercapture', { pointerId: 7 });
expect(calls.join() === 'start,stop', 'release did not end the hold exactly once: ' + calls.join());
`)
}

// Cause 3c: a click is not a hold, and must say so rather than do nothing.
func TestVoiceInput_AQuickPressSaysHoldToTalk(t *testing.T) {
	runVoiceScenario(t, `
const FV = load();
const v = makeVoice(FV).v;
v.setServerTranscription({ model: 'asr-test' });
let now = 1000;
const notes = [];
const hold = FV.makeHold(v, { now: function () { return now; }, note: function (s) { notes.push(s); } });

hold.press('space');
await settle();
now += 80;
hold.release('space');
await settle();
expect(notes.some(function (s) { return /hold/i.test(s); }), 'a quick press said nothing: ' + JSON.stringify(notes));
expect(world.fetches.length === 0, 'a click uploaded a fragment of a recording');
expect(world.tracksStopped >= 1, 'a click left the microphone open');
expect(!v.listening && !v.transcribing, 'a click left the mic listening or transcribing');
`)
}

// Cause 2: what was said while a turn is in flight is kept, never dropped.
//
// # Why the text box and not a queue
//
// A queued utterance is sent later, against a reply the person had not heard
// when they spoke, and they cannot see or edit it first. The text box keeps it
// visible and editable, never overwrites what they typed, and sends only when
// they decide — and it is the same place a typed message waits.
func TestVoiceInput_WhatWasSaidDuringATurnIsKeptInTheTextBox(t *testing.T) {
	runVoiceScenario(t, `
const FV = load();
const input = { value: '' };
const sent = [], notes = [];
const ctx = function (busy) { return { busy: busy, input: input, send: function (s) { sent.push(s); }, note: function (s) { notes.push(s); } }; };

FV.deliverSpoken('make it thicker', ctx(true));
expect(input.value === 'make it thicker', 'a transcript during a turn was not put in the text box: ' + JSON.stringify(input.value));
expect(sent.length === 0, 'a transcript was sent while a turn is in flight — send() drops it');
expect(notes.length === 1 && /text box/.test(notes[0]), 'nothing told the person where their words went: ' + JSON.stringify(notes));

input.value = 'use aluminium ';
FV.deliverSpoken('and round the corners', ctx(true));
expect(input.value === 'use aluminium and round the corners', 'typed text was overwritten or run together: ' + JSON.stringify(input.value));

input.value = 'draft';
FV.deliverSpoken('a bracket', ctx(false));
expect(JSON.stringify(sent) === '["a bracket"]', 'an idle transcript was not sent: ' + JSON.stringify(sent));
expect(input.value === 'draft', 'sending a transcript touched the text box');
`)
}

// The workbench is wired to all of the above.
//
// voice.js can be right and the page still wire the old handlers, so the glue is
// checked where it lives. codeOnly strips comments, so the history of why
// mouseleave was removed can stay written down beside the code.
func TestWorkbench_TheMicIsWiredToTheHoldAndKeepsWhatWasSaid(t *testing.T) {
	b, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	js := codeOnly(string(b))

	if strings.Contains(js, "'mouseleave'") {
		t.Error("workbench.js still listens for mouseleave: drifting off the round button ends the hold")
	}
	if !strings.Contains(js, "ForgeVoice.bindHold(") {
		t.Error("the mic is not bound through ForgeVoice.bindHold, so it has no pointer capture and no quick-press note")
	}
	if !strings.Contains(js, "ForgeVoice.deliverSpoken(") {
		t.Error("transcripts do not go through ForgeVoice.deliverSpoken: one arriving during a turn is dropped by send()")
	}
	if strings.Contains(js, "clearPartial(); send(text); }") {
		t.Error("onTranscript still calls send(text) directly, which returns early and drops speech while a turn is in flight")
	}
	if !strings.Contains(js, "setServerTranscription(") {
		t.Error("the workbench never tells the voice layer whether the server transcribes, so push-to-talk cannot use it")
	}

	// The typed twin of the dropped transcript: the box must not be cleared
	// before the busy check, or a message typed during a turn is erased unsent.
	if submit := strings.Index(js, "$('sayform').addEventListener('submit'"); submit < 0 {
		t.Error("the text box's submit handler was not found; this fence no longer checks what it thinks")
	} else {
		handler := js[submit:]
		if end := strings.Index(handler, "\n    });"); end > 0 {
			handler = handler[:end]
		}
		busy, clear := strings.Index(handler, "if (state.busy)"), strings.Index(handler, "input.value = '';")
		if busy < 0 || clear < 0 || busy > clear {
			t.Error("the submit handler clears the text box before checking whether a turn is in flight: " +
				"send() returns early while busy, so a message typed during a turn is erased and never sent")
		}
	}

	page := renderWorkbench(t)
	if !strings.Contains(page, `id="voice-path"`) {
		t.Error("the workbench has no #voice-path, so nothing says which speech path is in use")
	}
	ai, vj := strings.Index(page, "audio-input.js"), strings.Index(page, "voice.js")
	if ai < 0 || vj < 0 || ai > vj {
		t.Error("audio-input.js is not loaded before voice.js on the workbench, so recording cannot use its microphone constraints")
	}
}

// Problem 2 of 2026-09-17: after the server said it cannot transcribe, the mic
// still did not work.
//
// # What production showed
//
// The page said "voice: transcribed by FORGE (qwen3-asr-flash-2026-02-10)", the
// first hold came back 501, the note promised "the microphone uses the browser's
// own speech recognition from now on" — and the next holds produced nothing.
//
// # Why these scenarios and not one
//
// The flag was set and the next press did reach the recogniser; the state was
// not stuck. What failed was everything a real recogniser does that the old fake
// did not: in Chrome on a network that cannot reach Google a session can end
// with no result AND no error, and it can end with interim words and no final
// one. Both reached nothing and said nothing. And in a browser with no
// recogniser at all the note still read as if a fallback existed. Each of those
// is a scenario here, driven through the same bindHold the page uses, in the
// exact order: server advertised, press, 501, fallback, press again.
//
// ‼️ The fake recogniser's stop() does not end the session; world.endRecognition
// does, when the scenario says — which is Chrome's order, not an idealised one.
func TestVoiceInput_AServerThatCannotTranscribeFallsBackAndTheNextHoldIsHeard(t *testing.T) {
	sequence := `
const m = makeVoice(FV), v = m.v;
function noted(re) { return m.errors.some(function (e) { return re.test(e); }); }
v.setServerTranscription({ model: 'qwen3-asr-flash-2026-02-10' });
world.reply = { status: 501, body: { code: 'CONNECTOR_UNAVAILABLE', message: 'Not available here.',
  details: { detail: 'the transcription provider returned 404: Model not exist.' } } };
let now = 1000;
const btn = fakeButton();
FV.bindHold(btn, v, { now: function () { return now; }, note: function (s) { m.errors.push(s); } });
async function holdOnce(id, during) {
  btn.fire('pointerdown', { pointerId: id, button: 0 }); await settle();
  if (during) await during();
  now += 2000;
  btn.fire('pointerup', { pointerId: id, button: 0 }); await settle();
}

// Press 1: the server path, as advertised.
expect(v.inputPath() === 'server', 'setup: the server path is not in use (' + v.inputPath() + ')');
await holdOnce(1);
expect(world.fetches.length === 1, 'the first hold was not uploaded (' + world.fetches.length + ')');
expect(!v.listening && !v.transcribing, 'the mic is stuck listening or transcribing after the 501');
expect(!m.errors.some(function (e) { return /from now on/.test(e); }), 'the 501 note still promises a fallback that may not work: ' + JSON.stringify(m.errors));
expect(noted(/not transcribed/), 'the 501 note does not say the words just spoken were lost: ' + JSON.stringify(m.errors));
`
	for _, tc := range []struct{ name, scenario string }{
		{"the_next_hold_is_heard", `const FV = load();` + sequence + `
expect(v.inputPath() === 'browser', 'after the 501 the path is ' + v.inputPath() + ', want browser');
expect(noted(/Google/), 'the 501 note does not say where the browser sends the audio: ' + JSON.stringify(m.errors));
await holdOnce(2);
expect(world.fetches.length === 1, 'the second hold went to the server again (' + world.fetches.length + ' uploads)');
expect(world.recorders.length === 1, 'the second hold recorded for an upload instead of recognising');
expect(world.srStarts === 1, 'the second hold did not start the browser recogniser (' + world.srStarts + ')');
world.hear('make it thicker', true);
world.endRecognition(); await settle();
expect(JSON.stringify(m.said) === '["make it thicker"]', 'what the recogniser heard never reached the conversation: ' + JSON.stringify(m.said));
expect(!v.listening && !v.transcribing, 'the mic is stuck after the fallback hold');
`},
		{"the_recogniser_returns_nothing", `const FV = load();` + sequence + `
await holdOnce(2);
world.endRecognition(); await settle();
expect(m.said.length === 0, 'something was sent from a session that heard nothing');
expect(noted(/returned nothing/), 'a fallback hold that came back empty, with no error, said nothing: ' + JSON.stringify(m.errors));
expect(!v.listening, 'the mic is stuck listening');
`},
		{"only_interim_words_arrive", `const FV = load();` + sequence + `
await holdOnce(2, async function () { world.hear('round the corners', false); });
world.endRecognition(); await settle();
expect(JSON.stringify(m.said) === '["round the corners"]', 'words the recogniser showed as interim were dropped when the session ended: ' + JSON.stringify(m.said));
`},
		{"the_recogniser_cannot_reach_google", `const FV = load();` + sequence + `
await holdOnce(2);
world.recognitionError('network');
world.endRecognition(); await settle();
expect(noted(/could not reach its service/), 'a blocked recogniser did not say why: ' + JSON.stringify(m.errors));
expect(!noted(/returned nothing/), 'an error the recogniser reported was also reported as silence');
expect(v.inputPath() === 'none', 'the mic still offers a recogniser that cannot reach its service (' + v.inputPath() + ')');
`},
		{"no_recogniser_in_this_browser", `const FV = load({ recognition: false });` + sequence + `
expect(v.inputPath() === 'none', 'with no server and no recogniser the path is ' + v.inputPath());
expect(noted(/no speech recognition of its own/) && noted(/typing/), 'a browser with no recogniser was not told plainly that the mic is off: ' + JSON.stringify(m.errors));
m.errors.length = 0;
await holdOnce(2);
expect(world.fetches.length === 1 && world.recorders.length === 1, 'a press with no path recorded or uploaded anyway');
expect(noted(/microphone is off/), 'a press with no path said nothing: ' + JSON.stringify(m.errors));
`},
		{"the_server_said_so_before_the_press", `const FV = load();
const m = makeVoice(FV), v = m.v;
v.setServerTranscription(null, 'server transcription is off: the transcription endpoint does not serve qwen3-asr-flash-2026-02-10');
expect(v.inputPath() === 'browser', 'the path is ' + v.inputPath() + ', want browser');
expect(/does not serve qwen3-asr-flash-2026-02-10/.test(v.serverWhy()), 'the server reason is not kept for the page: ' + v.serverWhy());
v.startListening(); await settle();
expect(world.fetches.length === 0 && world.recorders.length === 0, 'a deployment that said it cannot transcribe was still recorded for');
expect(world.srStarts === 1, 'the browser recogniser did not start');
`},
	} {
		t.Run(tc.name, func(t *testing.T) { runVoiceScenario(t, tc.scenario) })
	}
}

// Problem 3 of 2026-09-17: push-to-talk stole the space bar.
//
// # What was wrong
//
// The page took Space for push-to-talk whenever focus was not the text box. So
// Space on a focused Delete, New conversation or Send button held the
// microphone instead of pressing the button, and Space on a checkbox did not
// tick it. The shortcut exists for keyboard users (PRD AUD-06), and it made the
// page's other controls unusable for exactly them.
//
// # The rule
//
// Space is push-to-talk only where it would otherwise do nothing but scroll.
// Anything Space operates — buttons, checkboxes, links, selects, anything typed
// into, and their ARIA equivalents — keeps it: not prevented, not pressed. The
// mic button itself is the exception, because Space on it is holding it.
func TestVoiceInput_SpaceOperatesAFocusedControlAndTalksEverywhereElse(t *testing.T) {
	runVoiceScenario(t, `
const FV = load();
const on = {};
const doc = { addEventListener: function (t, fn) { (on[t] = on[t] || []).push(fn); } };
function el(tag, attrs) {
  attrs = attrs || {};
  return { tagName: tag, isContentEditable: !!attrs.editable,
    getAttribute: function (n) { return attrs[n] == null ? null : attrs[n]; } };
}
const calls = [];
let held = null;
const hold = {
  press: function (who) { if (held) return false; held = who; calls.push('press'); return true; },
  release: function (who) { if (!held || held !== who) return false; held = null; calls.push('release'); return true; }
};
const mic = el('BUTTON');
let micDisabled = false;
FV.bindSpaceHold(doc, hold, { mic: mic, enabled: function () { return !micDisabled; } });
function key(type, target, extra) {
  const e = Object.assign({ type: type, code: 'Space', key: ' ', target: target, prevented: false,
    preventDefault: function () { this.prevented = true; } }, extra || {});
  (on[type] || []).forEach(function (fn) { fn(e); });
  return e;
}

const operated = {
  'a Delete button': el('BUTTON'), 'the New conversation button': el('BUTTON'), 'the Send button': el('BUTTON'),
  'a checkbox': el('INPUT', { type: 'checkbox' }), 'the text box': el('INPUT', { type: 'text' }),
  'a textarea': el('TEXTAREA'), 'a select': el('SELECT'), 'a link': el('A', { href: '#' }),
  'a contenteditable': el('DIV', { editable: true }), 'a div with role=button': el('DIV', { role: 'button' }),
  'a tree row with role=treeitem': el('LI', { role: 'treeitem' })
};
Object.keys(operated).forEach(function (name) {
  calls.length = 0;
  const down = key('keydown', operated[name]), up = key('keyup', operated[name]);
  expect(!down.prevented && !up.prevented, 'Space on ' + name + ' was prevented, so it no longer operates it');
  expect(calls.length === 0, 'Space on ' + name + ' held the microphone: ' + calls.join());
});

calls.length = 0;
const body = el('BODY');
const down = key('keydown', body);
key('keydown', body, { repeat: true });
const up = key('keyup', body);
expect(down.prevented && up.prevented, 'Space with focus on the page itself was not taken for push-to-talk (the page would scroll)');
expect(calls.join() === 'press,release', 'Space with focus on the page did not hold exactly once: ' + calls.join());

calls.length = 0;
key('keydown', mic); key('keyup', mic);
expect(calls.join() === 'press,release', 'Space on the mic button is not holding it: ' + calls.join());

calls.length = 0;
key('keydown', null); key('keyup', null);
expect(calls.join() === 'press,release', 'Space with no focused element did not hold: ' + calls.join());

calls.length = 0;
key('keydown', body, { ctrlKey: true }); key('keyup', body);
expect(calls.length === 0, 'Ctrl+Space was taken for push-to-talk: ' + calls.join());

calls.length = 0;
micDisabled = true;
const off = key('keydown', body); key('keyup', body);
expect(calls.length === 0 && !off.prevented, 'Space held a disabled microphone: ' + calls.join());
micDisabled = false;

// A hold begun on the page ends when Space is let go, wherever focus is by then.
calls.length = 0;
key('keydown', body);
key('keyup', operated['the Send button']);
expect(calls.join() === 'press,release', 'a Space hold did not end on release after focus moved: ' + calls.join());
`)
}

// The page uses both of the above.
//
// voice.js can be right and workbench.js still wire the old key handler, or
// drop the server's reason on the floor. codeOnly strips comments, so the history
// of the old handler can stay written beside the new one.
func TestWorkbench_SpaceAndTheServerReasonAreWiredThroughVoiceJS(t *testing.T) {
	b, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	js := codeOnly(string(b))
	if !strings.Contains(js, "ForgeVoice.bindSpaceHold(document, hold") {
		t.Error("the space bar is not bound through ForgeVoice.bindSpaceHold, so it is taken from focused buttons")
	}
	if strings.Contains(js, "e.code === 'Space'") {
		t.Error("workbench.js still handles Space itself, beside or instead of bindSpaceHold")
	}
	if !strings.Contains(js, "voice.setServerTranscription(tr.server ? tr : null, tr.server ? '' : tr.reason)") {
		t.Error("the workbench does not pass the server's reason to the voice layer, so nobody is told why FORGE does not transcribe")
	}
}
