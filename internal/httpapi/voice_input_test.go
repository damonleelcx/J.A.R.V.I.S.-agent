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

// spaceWorld is the page the space-bar scenarios below run in: a document and
// its window that record listeners, elements that answer :focus-visible, and a
// hold that records what it was asked to do.
//
// ‼️ The fake models Chromium's :focus-visible TIMING, because that is the
// trap. After a mouse click on a button, :focus-visible is false at focusin —
// and becomes TRUE the moment any non-modifier key goes down, BEFORE the
// keydown listeners run (Chromium records "had a keyboard event" first, then
// dispatches). Measured in Chrome on 2026-09-17 with real input through the
// DevTools protocol: click Send, focusin says false; press Space, the keydown
// listener sees true. So a handler that asks :focus-visible at keydown sees
// every focused button as keyboard-focused and fixes nothing. key() below flips
// the focused element to visible before dispatching keydown, the way Chrome
// does; the focus-origin must be read at focusin.
const spaceWorld = `
const on = {}, winOn = {};
const win = { addEventListener: function (t, fn) { (winOn[t] = winOn[t] || []).push(fn); } };
const doc = { visibilityState: 'visible', defaultView: win,
  addEventListener: function (t, fn) { (on[t] = on[t] || []).push(fn); } };
function el(tag, attrs) {
  attrs = attrs || {};
  return { tagName: tag, isContentEditable: !!attrs.editable, fv: false, throws: !!attrs.throws,
    type: attrs.type, id: attrs.id || tag,
    getAttribute: function (n) { return attrs[n] == null ? null : attrs[n]; },
    matches: function (sel) {
      if (this.throws) throw new SyntaxError("'" + sel + "' is not a valid selector");
      if (sel !== ':focus-visible') throw new Error('unexpected selector ' + sel);
      return this.fv;
    } };
}
let focused = null;
// focus moves focus to target: 'pointer' is a mouse click (not focus-visible),
// 'keyboard' is Tab or an arrow key (focus-visible).
function focus(target, how) {
  focused = target;
  target.fv = how === 'keyboard';
  (on.focusin || []).forEach(function (fn) { fn({ type: 'focusin', target: target }); });
}
function key(type, target, extra) {
  // Chromium: a key press makes the focused element :focus-visible before any
  // listener hears it. See the note above.
  if (type === 'keydown' && target && target.matches && !target.throws) target.fv = true;
  const e = Object.assign({ type: type, code: 'Space', key: ' ', target: target, prevented: false,
    preventDefault: function () { this.prevented = true; } }, extra || {});
  (on[type] || []).forEach(function (fn) { fn(e); });
  return e;
}
function blurWindow() { (winOn.blur || []).forEach(function (fn) { fn({ type: 'blur' }); }); }
function hide() {
  doc.visibilityState = 'hidden';
  (on.visibilitychange || []).forEach(function (fn) { fn({ type: 'visibilitychange' }); });
}
const calls = [];
let held = null;
const hold = {
  holding: function () { return !!held; },
  press: function (who) { if (held) return false; held = who; calls.push('press:' + who); return true; },
  release: function (who) { if (!held || (who != null && held !== who)) return false; calls.push('release:' + held); held = null; return true; }
};
const mic = el('BUTTON', { id: 'mic' });
let micDisabled = false;
const refusals = [];
FV.bindSpaceHold(doc, hold, { mic: mic, enabled: function () { return !micDisabled; },
  refused: function () { refusals.push('refused'); } });
`

// Space after a MOUSE click on a button is push-to-talk, and does not press
// that button again.
//
// # What was wrong
//
// The owner: "fix the push-to-talk space key when not focused". Nothing looked
// focused — but in Chrome a mouse click on a button leaves keyboard focus on
// it, with no ring drawn. PR 138 gave Space back to every focused button, so
// after clicking Send, New conversation, Delete, a stage panel tab or the
// hands-free checkbox, Space did not talk: it pressed that button AGAIN — a
// second new conversation, a re-send, the checkbox toggled back. Reproduced in
// Chrome with real input: click Send, press Space, Send's click handler ran and
// no hold began.
//
// # The rule
//
// How focus ARRIVED decides it, read at focusin with :focus-visible:
//
//   - reached from the keyboard (Tab, arrow keys): Space operates the control,
//     as PR 138 made it — the keyboard user it was for is unchanged;
//   - reached by a pointer, on something Space ACTIVATES (button, link,
//     summary, checkbox, radio, and their ARIA roles, tabs included): Space is
//     push-to-talk, and the control does not receive it;
//   - anything Space EDITS — a text box, a textarea, contenteditable, a select,
//     a slider, a listbox — keeps Space however it was focused. Typing a space
//     is the whole point of a text box.
//
// ‼️ Not :focus-visible at keydown: see spaceWorld. And a browser that throws
// on the selector keeps PR 138's behaviour, never the other way round.
func TestVoiceInput_SpaceAfterAMouseClickTalksAndDoesNotPressTheButtonAgain(t *testing.T) {
	runVoiceScenario(t, `
const FV = load();
`+spaceWorld+`
function press(target) {
  calls.length = 0;
  const down = key('keydown', target), rep = key('keydown', target, { repeat: true }), up = key('keyup', target);
  return { down: down, rep: rep, up: up, calls: calls.join() };
}

const activated = {
  'the Send button': el('BUTTON'), 'the New conversation button': el('BUTTON'), 'a Delete button': el('BUTTON'),
  'a stage panel tab': el('BUTTON', { role: 'tab' }), 'the hands-free checkbox': el('INPUT', { type: 'checkbox' }),
  'a link': el('A', { href: '#' }), 'a div with role=button': el('DIV', { role: 'button' }),
  'a submit input': el('INPUT', { type: 'submit' })
};
Object.keys(activated).forEach(function (name) {
  focus(activated[name], 'pointer');
  const r = press(activated[name]);
  expect(r.calls === 'press:space,release:space', 'Space after a mouse click on ' + name + ' did not hold the microphone exactly once: [' + r.calls + ']');
  expect(r.down.prevented && r.rep.prevented && r.up.prevented, 'Space after a mouse click on ' + name + ' was not prevented, so it presses ' + name + ' again');

  focus(activated[name], 'keyboard');
  const k = press(activated[name]);
  expect(k.calls === '', 'Space on ' + name + ' reached with Tab held the microphone: [' + k.calls + ']');
  expect(!k.down.prevented && !k.up.prevented, 'Space on ' + name + ' reached with Tab was prevented, so a keyboard user cannot press it');
});

const edited = {
  'the text box': el('INPUT', { type: 'text' }), 'an input with no type': el('INPUT'), 'a textarea': el('TEXTAREA'),
  'a contenteditable': el('DIV', { editable: true }), 'a select': el('SELECT'), 'a range slider': el('INPUT', { type: 'range' }),
  'a div with role=slider': el('DIV', { role: 'slider' }), 'a div with role=textbox': el('DIV', { role: 'textbox' }),
  'a listbox option': el('LI', { role: 'option' })
};
Object.keys(edited).forEach(function (name) {
  ['pointer', 'keyboard'].forEach(function (how) {
    focus(edited[name], how);
    const r = press(edited[name]);
    expect(r.calls === '' && !r.down.prevented && !r.up.prevented,
      'Space in ' + name + ' focused by ' + how + ' was taken for push-to-talk, so it cannot be typed or edited: [' + r.calls + ']');
  });
});

// Tab away from a clicked button: the next control was reached by keyboard.
const send = el('BUTTON'), newConv = el('BUTTON');
focus(send, 'pointer'); focus(newConv, 'keyboard');
let r = press(newConv);
expect(r.calls === '' && !r.down.prevented, 'Space on a button tabbed to after a click held the microphone: [' + r.calls + ']');

// Keyboard focus, then a click elsewhere: the clicked one is pointer-focused.
focus(newConv, 'keyboard'); focus(send, 'pointer');
r = press(send);
expect(r.calls === 'press:space,release:space', 'Space after clicking away from a tabbed-to button did not talk: [' + r.calls + ']');

// A pointer-focused record is for THAT element only.
focus(send, 'pointer');
r = press(newConv);
expect(r.calls === '' && !r.down.prevented, 'Space on a different button than the clicked one held the microphone: [' + r.calls + ']');

// A browser that cannot answer :focus-visible keeps PR 138's rule.
const old = el('BUTTON', { throws: true });
focus(old, 'pointer');
r = press(old);
expect(r.calls === '' && !r.down.prevented, 'Space on a button where :focus-visible throws held the microphone: [' + r.calls + ']');

// PR 138's cases still stand: the page itself, the mic, nothing focused.
[['the page itself', el('BODY')], ['the mic button', mic], ['no focused element', null]].forEach(function (c) {
  r = press(c[1]);
  expect(r.calls === 'press:space,release:space' && r.down.prevented && r.up.prevented, 'Space on ' + c[0] + ' no longer holds: [' + r.calls + ']');
});
`)
}

// A Space hold ends when the page loses focus, exactly once.
//
// # What was wrong
//
// A keyup goes to the window that has focus. Hold Space, alt-tab away (or click
// another app, or the browser's address bar) and let go: the page never hears
// the release, and the microphone stays open — listening, and in Chrome sending
// audio to Google — until the next time Space happens to be pressed and let go
// on the page. The mic button already guards this (lostpointercapture); the
// space bar had nothing.
//
// # The rule
//
// A window blur, or the page becoming hidden, ends a hold SPACE began. Not a
// hold the button began — pointer capture already owns that one — and never a
// second release: the keyup that may still arrive later finds nothing to end.
func TestVoiceInput_ASpaceHoldEndsWhenThePageLosesFocus(t *testing.T) {
	runVoiceScenario(t, `
const FV = load();
`+spaceWorld+`
const body = el('BODY');

key('keydown', body);
blurWindow();
expect(calls.join() === 'press:space,release:space', 'a window blur mid-hold did not end the Space hold: [' + calls.join() + ']');
blurWindow();
const late = key('keyup', body);
expect(calls.join() === 'press:space,release:space', 'the hold was ended twice (blur, then a late blur or keyup): [' + calls.join() + ']');
expect(!late.prevented, 'a keyup after the blur ended the hold was still prevented');

calls.length = 0;
key('keydown', body);
hide();
expect(calls.join() === 'press:space,release:space', 'the page going hidden mid-hold did not end the Space hold: [' + calls.join() + ']');
doc.visibilityState = 'visible';

// ‼️ A pointer hold is the button's to end.
calls.length = 0;
hold.press('pointer');
blurWindow(); hide(); doc.visibilityState = 'visible';
expect(calls.join() === 'press:pointer', 'a blur ended a hold the mic button began: [' + calls.join() + ']');
hold.release('pointer');

// Blur with nothing held does nothing.
calls.length = 0;
blurWindow();
expect(calls.length === 0, 'a blur with nothing held did something: [' + calls.join() + ']');
`)
}

// A Space hold on the real voice layer ends the microphone exactly once when
// the window blurs, in both of the ways the microphone hears.
//
// The scenario above uses a stand-in hold that only notes its calls. This one
// uses makeHold and Voice as the page does, and compares a Space hold to a button hold: the same
// listening starts, the same thing is delivered, and a blur releases the
// microphone once — the recorder stopped once, the recogniser stopped, nothing
// left listening.
func TestVoiceInput_ASpaceHoldListensTheSameWayAsTheButton(t *testing.T) {
	for _, mode := range []string{"browser", "server"} {
		t.Run(mode, func(t *testing.T) {
			runVoiceScenario(t, `
const FV = load();
const mode = '`+mode+`';
let now = 1000;
async function run(how) {
  world.gumCalls = 0; world.recorders.length = 0; world.fetches.length = 0; world.srStarts = 0; world.recognitions.length = 0;
  const m = makeVoice(FV), v = m.v;
  v.setServerTranscription(mode === 'server' ? { model: 'asr-test' } : null);
  const btn = fakeButton();
  const hold = FV.bindHold(btn, v, { now: function () { return now; }, note: function (s) { m.errors.push(s); } });
  const docOn = {}, winOn = {};
  const doc = { visibilityState: 'visible', addEventListener: function (t, fn) { (docOn[t] = docOn[t] || []).push(fn); },
    defaultView: { addEventListener: function (t, fn) { (winOn[t] = winOn[t] || []).push(fn); } } };
  FV.bindSpaceHold(doc, hold, { mic: btn });
  let stops = 0;
  const stop = v.stopListening;
  v.stopListening = function () { stops++; return stop.apply(this, arguments); };
  function fireDoc(t, e) { (docOn[t] || []).forEach(function (fn) { fn(Object.assign({ type: t, preventDefault: function () {} }, e)); }); }
  if (how === 'space') fireDoc('keydown', { code: 'Space', target: null });
  else btn.fire('pointerdown', { pointerId: 1, button: 0 });
  await settle();
  const listening = v.listening;
  now += 2000;
  if (how === 'space') (winOn.blur || []).forEach(function (fn) { fn({ type: 'blur' }); });
  else btn.fire('lostpointercapture', { pointerId: 1 });
  const stopsAtBlur = stops;
  fireDoc('keyup', { code: 'Space', target: null });
  (winOn.blur || []).forEach(function (fn) { fn({ type: 'blur' }); });
  await settle();
  if (mode === 'browser') { world.hear('make the wall thicker', true); if (world.recognitions[0].onend) world.endRecognition(); await settle(); }
  return { listening: listening, stopsAtBlur: stopsAtBlur, stops: stops, gum: world.gumCalls, recorders: world.recorders.length,
    uploads: world.fetches.length, sr: world.srStarts, said: JSON.stringify(m.said), stillListening: v.listening };
}
const bySpace = await run('space'), byButton = await run('pointer');
expect(bySpace.listening, 'a Space hold did not start listening on the ' + mode + ' path');
expect(bySpace.stopsAtBlur === 1, 'a blur mid-Space-hold did not stop the microphone (' + bySpace.stopsAtBlur + ' stops): it stays open until Space is next let go on the page');
expect(bySpace.stops === 1, 'a Space hold stopped the microphone ' + bySpace.stops + ' times after a blur and a late keyup, want once');
expect(!bySpace.stillListening, 'the microphone is still listening after the blur');
expect(bySpace.said === '["make the wall thicker"]', 'a Space hold ended by a blur delivered ' + bySpace.said);
expect(JSON.stringify(bySpace) === JSON.stringify(byButton),
  'a Space hold and a button hold differ on the ' + mode + ' path:\n    space  ' + JSON.stringify(bySpace) + '\n    button ' + JSON.stringify(byButton));
`)
		})
	}
}

// Space on a microphone that is off says why, the way the page says it when
// the microphone goes off.
//
// # What was wrong
//
// The mic is disabled when no way of hearing can work — in production that is
// the browser's recogniser reporting it cannot reach Google, which is the
// owner's situation — or when nobody is signed in. The reason is written to the
// voice note once, at the moment the mic goes off; anything later (a sent
// transcript) clears it. After that, Space did nothing and said nothing: it
// looked exactly like the key not being heard.
//
// Space now puts the reason back in the note — once per press, not per key
// repeat, and only where Space would have been push-to-talk. A Space that types
// or presses a control is not a refused press and says nothing.
func TestVoiceInput_SpaceOnAMicrophoneThatIsOffSaysWhy(t *testing.T) {
	runVoiceScenario(t, `
const FV = load();
`+spaceWorld+`
micDisabled = true;
const body = el('BODY');
const down = key('keydown', body); key('keydown', body, { repeat: true }); key('keyup', body);
expect(calls.length === 0 && !down.prevented, 'Space held a microphone that is off: [' + calls.join() + ']');
expect(refusals.length === 1, 'Space on a microphone that is off said why ' + refusals.length + ' times, want once per press');

refusals.length = 0;
const box = el('INPUT', { type: 'text' });
focus(box, 'pointer'); key('keydown', box); key('keyup', box);
const tabbed = el('BUTTON');
focus(tabbed, 'keyboard'); key('keydown', tabbed); key('keyup', tabbed);
expect(refusals.length === 0, 'Space typed into the text box or pressed a tabbed-to button, and the page said the microphone is off');
`)
}

// The page uses both of the above.
//
// voice.js can be right and workbench.js still wire the old key handler, or
// drop the server's reason on the floor. codeOnly strips comments, so the history
// of the old handler can stay written beside the new one.
//
// And the page must give bindSpaceHold something to say when Space is refused,
// or a microphone that is off is silent to the space bar again.
func TestWorkbench_SpaceAndTheServerReasonAreWiredThroughVoiceJS(t *testing.T) {
	b, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	js := codeOnly(string(b))
	if !strings.Contains(js, "ForgeVoice.bindSpaceHold(document, hold") {
		t.Error("the space bar is not bound through ForgeVoice.bindSpaceHold, so it is taken from focused buttons")
	}
	// Line endings removed: a Windows checkout embeds CRLF.
	if !strings.Contains(strings.ReplaceAll(js, "\r", ""), "refused: function () {\n        voiceNote(state.signedOut ? 'Sign in from the console to talk to FORGE.' : voice.whyUnavailable());") {
		t.Error("Space on a microphone that is off does not say why on the page (bindSpaceHold is given no refused note)")
	}
	if strings.Contains(js, "e.code === 'Space'") {
		t.Error("workbench.js still handles Space itself, beside or instead of bindSpaceHold")
	}
	if !strings.Contains(js, "voice.setServerTranscription(tr.server ? tr : null, tr.server ? '' : tr.reason)") {
		t.Error("the workbench does not pass the server's reason to the voice layer, so nobody is told why FORGE does not transcribe")
	}
}
