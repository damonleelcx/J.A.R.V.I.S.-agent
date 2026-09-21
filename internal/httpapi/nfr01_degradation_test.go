package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NFR-01, the availability half of the PRD: "99.9% monthly; mute/stop/end remain
// available during cloud degradation" (docs/prd.md).
//
// # What is actually being fenced
//
// The second clause is the one with teeth, and it is a claim about what a
// control DOES when the cloud is in trouble — not about what it looks like. A
// mute button that greys itself out while the network is down has not "remained
// available"; neither has one that spins forever waiting for a request that
// will never answer, nor one that shows itself as applied because the request
// it depends on has not failed YET.
//
// So the question each fence below asks is the same one, in two halves:
//
//	(a) does the control make a network request at all? and
//	(b) does it take effect with the network broken?
//
// Both halves are needed. (a) alone would pass for a button that does nothing.
// (b) alone would pass for a button that happens to work because the test's
// fake server answered.
//
// # Three shapes of degradation, because they fail differently
//
// "The cloud is degraded" is not one state, and a control can survive one shape
// and hang on another:
//
//   - SLOW — the request is accepted and never answers. This is the shape that
//     hangs a UI, because nothing has failed and there is nothing to catch.
//   - ERRORING — the request is refused outright, either by rejecting (no route
//     to the host) or by answering 500. Fast and loud.
//   - DISCONNECTING — the request starts, the server takes it, and the
//     connection dies mid-flight. The distinction from ERRORING matters because
//     the code has already committed to the request by the time it fails.
//
// Each of the JS fences below runs its controls under all three.
//
// # The precise claim about stopSpeaking
//
// stopSpeaking aborts an in-flight POST /v1/speech. Aborting a request that is
// already in flight is NOT the same as making one: nothing new is sent, nothing
// is waited on, and the abort is a synchronous local call. The assertions
// therefore count NEW requests across the control, never total network
// activity, and an abort of the pending request is expected and asserted.
//
// # Where the server-authoritative room mute lands (the Gap 1 ruling)
//
// See TestNFR01_ARefusedRoomMuteSaysItIsNotInForceRatherThanShowingItApplied
// and docs/spikes/2026-09-20-nfr01-availability/README.md. In short: NFR-01's
// "remain available" is honoured by the SINGLE-USER controls, which are local
// and provably network-free; the MULTI-PARTY room controls cannot honour it
// without lying to the other people in the room, and they are scoped out
// rather than defective.

// ---------------------------------------------------------------------------
// The browser these scenarios run in.
// ---------------------------------------------------------------------------

// nfr01World is a deliberate near-copy of voiceWorld in voice_input_test.go,
// not a shared helper, and the duplication is the point: that world models a
// browser whose network WORKS, because the bug it fences is about the
// microphone path succeeding. This one models a browser whose network is in
// one of three broken states, and it also loads room.js. Merging them would
// make one file's fakes answerable to the other file's failures.
//
// Two things it has that voiceWorld does not:
//
//  1. a speechSynthesis stub, so Voice.synthAvailable is TRUE. Without it
//     _speakLocal returns immediately, she never speaks, and "stop speaking
//     still works" would be asserted about a no-op.
//  2. a degrading fetch, installed on BOTH window.fetch (voice.js reads
//     global.fetch, where global is the window) and globalThis.fetch (room.js
//     reads a bare `fetch`, which resolves to the real one in node). One
//     recorder behind both, so world.fetches counts every request either asset
//     makes.
//
// ‼️ No backticks anywhere below: the whole thing is a Go raw string and one
// backtick would end it. Same rule as voiceWorld; see the note at its
// Object.defineProperty call for why navigator is defined rather than assigned.
const nfr01World = `
'use strict';
const fs = require('fs'), path = require('path');
const dir = process.argv[2];
const failures = [];
function expect(cond, why) { if (!cond) failures.push(why); }
function finish() {
  if (failures.length) { console.log(failures.map(function (f) { return '  - ' + f; }).join('\n')); process.exit(1); }
}
function crash(e) {
  console.log('the scenario could not run against these assets: ' + ((e && e.stack) || e));
  process.exit(2);
}
async function settle() { for (let i = 0; i < 30; i++) await new Promise(function (r) { setImmediate(r); }); }

const world = {
  // Every request either asset makes, in order. The NFR-01 assertions are all
  // about how this array does or does not grow across a control.
  fetches: [],
  // How the cloud is broken right now: 'ok' | 'slow' | 'erroring' | 'refusing'
  // | 'disconnecting'. Set through degrade() so a scenario reads as prose.
  mode: 'slow',
  recorders: [], recordBytes: [1, 2, 3, 4, 5, 6, 7, 8], tracksStopped: 0,
  synth: { spoken: [], cancels: 0, speaking: false },
  roomEvents: []
};

world.degrade = function (mode) { world.mode = mode; };
world.since = function () { return world.fetches.length; };
// grew reports how many NEW requests were made since a mark taken with since().
world.grew = function (mark) { return world.fetches.length - mark; };

// The degraded network. Every mode records the request first: a control that
// makes a request and then has it fail has still made a request, and that is
// exactly what these fences are counting.
function degradedFetch(url, init) {
  init = init || {};
  const entry = { url: url, init: init, signal: init.signal || null };
  world.fetches.push(entry);
  switch (world.mode) {
    case 'slow':
      // Accepted and never answered. The one shape with nothing to catch.
      // An abort still settles it, because that is what a real fetch does and
      // the whole point of the abort in stopSpeaking is that it does not need
      // the server's cooperation to take effect.
      return new Promise(function (resolve, reject) {
        if (init.signal) {
          init.signal.addEventListener('abort', function () {
            const e = new Error('The user aborted a request.');
            e.name = 'AbortError';
            reject(e);
          });
        }
      });
    case 'erroring':
      return Promise.reject(new TypeError('Failed to fetch'));
    case 'refusing':
      // A server that is up and answering 500 is degradation too, and it fails
      // on a different line from a rejected fetch.
      return Promise.resolve({
        ok: false, status: 500,
        headers: { get: function () { return null; } },
        json: function () { return Promise.resolve({ error: { message: 'the model endpoint is unreachable', remedy: 'try again shortly' } }); },
        blob: function () { return Promise.resolve({ size: 0 }); }
      });
    case 'disconnecting':
      // Taken, then dropped mid-flight.
      return new Promise(function (resolve, reject) {
        setImmediate(function () { reject(new TypeError('The network connection was lost.')); });
      });
    default:
      return Promise.resolve({
        ok: true, status: 200,
        headers: { get: function () { return null; } },
        json: function () { return Promise.resolve({ text: 'make the wall thicker', model: 'asr-test' }); },
        blob: function () { return Promise.resolve({ size: 0 }); }
      });
  }
}

function fakeStream() {
  const track = { kind: 'audio', enabled: true, label: 'Test microphone',
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
    if (self.ondataavailable) self.ondataavailable({ data: new Blob([bytes], { type: self.mimeType }) });
    if (self.onstop) self.onstop();
  });
};

// The browser's own voice. Synchronous start, because that is what matters
// here: by the time speak() returns she IS speaking, so a stop has something
// real to stop.
function FakeUtterance(text) { this.text = text; }
const fakeSynth = {
  speak: function (u) {
    world.synth.spoken.push(u.text);
    world.synth.speaking = true;
    fakeSynth._utter = u;
    if (u.onstart) u.onstart();
  },
  cancel: function () { world.synth.cancels++; world.synth.speaking = false; },
  getVoices: function () { return []; }
};

const win = {
  addEventListener: function () {}, removeEventListener: function () {},
  setTimeout: setTimeout, clearTimeout: clearTimeout, setInterval: setInterval, clearInterval: clearInterval,
  console: console, Blob: Blob, MediaRecorder: FakeMediaRecorder,
  speechSynthesis: fakeSynth,
  RTCPeerConnection: function () {},
  navigator: { mediaDevices: {
    getUserMedia: function () { return Promise.resolve(fakeStream()); },
    enumerateDevices: function () { return Promise.resolve([]); }
  } },
  fetch: degradedFetch
};

// load brings up voice.js in this world. The synthesis stub has to be on the
// window BEFORE the constructor runs, because Voice reads synthAvailable once.
function load() {
  Object.defineProperty(globalThis, 'navigator', {
    value: win.navigator, configurable: true, writable: true
  });
  Object.defineProperty(globalThis, 'SpeechSynthesisUtterance', {
    value: FakeUtterance, configurable: true, writable: true
  });
  ['audio-input.js', 'voice.js'].forEach(function (name) {
    new Function('window', fs.readFileSync(path.join(dir, name), 'utf8'))(win);
  });
  return win.ForgeVoice;
}

// loadRoom brings up room.js. It reads a BARE fetch rather than global.fetch,
// so the degraded one has to be defined on globalThis as well; node 18+ ships a
// real fetch global, so this is defineProperty for the same reason navigator is.
function loadRoom() {
  Object.defineProperty(globalThis, 'fetch', {
    value: degradedFetch, configurable: true, writable: true
  });
  new Function('window', fs.readFileSync(path.join(dir, 'room.js'), 'utf8'))(win);
  return win.ForgeRoom;
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

// makeRoom is a room with a microphone and a live stream, which is the only
// state in which setState has anything to do.
function makeRoom(FR) {
  const track = { kind: 'audio', enabled: true };
  const r = new FR.Room('room-1', { on: function (k, d) { world.roomEvents.push({ kind: k, data: d }); } });
  r.mic = { getAudioTracks: function () { return [track]; } };
  r.streamID = 'stream-1';
  r.pc = {};
  r.track = track;
  return r;
}
function roomEvents(kind) { return world.roomEvents.filter(function (e) { return e.kind === kind; }); }
`

// runNFR01Scenario runs one scenario against the embedded assets.
//
// A near-copy of runVoiceScenario for the same reason nfr01World is a near-copy
// of voiceWorld, plus one difference that matters: it writes room.js too, so a
// single scenario can exercise the workbench controls and the room controls in
// one world and compare them.
func runNFR01Scenario(t *testing.T, scenario string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; the NFR-01 degradation fences run voice.js and room.js in node")
	}
	dir := t.TempDir()
	for _, name := range []string{"audio-input.js", "voice.js", "room.js"} {
		b, err := assetFS.ReadFile("assets/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(dir, "scenario.js")
	src := nfr01World + "\n(async function () {\n" + scenario + "\n})().then(finish, crash);\n"
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

// ---------------------------------------------------------------------------
// The single-user controls: mute, stop-speaking, cancel-listening.
// ---------------------------------------------------------------------------

// TestNFR01_MuteAndStopTakeEffectWithNoNetworkAtAll is the baseline: with the
// browser's own voice speaking and a microphone hold in progress, the three
// controls NFR-01 names must complete without a single request.
//
// It runs with the network in its most hostile shape (slow — accepted and never
// answered), so a control that made a request would not merely be slow, it
// would never return. The count is the assertion; the mode is there so a
// regression cannot be masked by a fast fake server.
//
// cancelListening is the one control here whose network-freedom is a CHOICE
// rather than an accident, and the test states the contrast explicitly:
// stopListening deliberately DOES upload what was already said, because ending
// a hold is a delivery. cancelListening discards, so nothing is owed and
// nothing is sent. Confusing the two is how "cancel" would quietly become a
// control that needs the cloud.
func TestNFR01_MuteAndStopTakeEffectWithNoNetworkAtAll(t *testing.T) {
	runNFR01Scenario(t, `
world.degrade('slow');
const FV = load();
const m = makeVoice(FV), v = m.v;
expect(v.synthAvailable === true, 'the scenario has no browser voice, so nothing below would be stopping anything real');

// --- stop-speaking, while she is speaking in the browser's own voice --------
// remoteVoice false is a deployment with no speech vendor, or one whose vendor
// has already failed over. It is the path with no network in it at all, which
// is exactly the path NFR-01 leans on.
v.remoteVoice = false;
v.speak('the wall is now four millimetres thick');
await settle();
expect(v.speaking === true, 'she is not speaking, so the stop control below has nothing to stop');
expect(world.fetches.length === 0, 'speaking in the browser voice reached the network, which means the fallback path is not network-free after all');

let mark = world.since();
v.stopSpeaking();
expect(world.grew(mark) === 0, 'stopSpeaking made ' + world.grew(mark) + ' network request(s); NFR-01 requires stop to remain available while the cloud is degraded, and a control that has to ask the cloud for permission to stop is not available during an outage');
expect(v.speaking === false, 'stopSpeaking left the voice reading itself as still speaking, so the caption and the barge-in guard both still believe she is talking');
expect(world.synth.cancels > 0, 'stopSpeaking never cancelled the browser speech synthesis, so she keeps talking however the button looks');
expect(world.synth.speaking === false, 'the browser is still speaking after stopSpeaking; the control reported success without taking effect');

// --- mute -------------------------------------------------------------------
mark = world.since();
const muted = v.toggleMute();
expect(muted === true, 'toggleMute did not report the muted state it had just entered');
expect(v.muted === true, 'the voice is not muted after toggleMute, so the control did not take effect');
expect(world.grew(mark) === 0, 'toggleMute made ' + world.grew(mark) + ' network request(s); mute is a local state flip and NFR-01 requires it to work while the cloud is unreachable');

// A mute that can be entered but not left is half a control.
mark = world.since();
expect(v.toggleMute() === false, 'toggleMute did not come back out of mute');
expect(v.muted === false, 'the voice is still muted after a second toggleMute');
expect(world.grew(mark) === 0, 'unmuting made ' + world.grew(mark) + ' network request(s), so coming back from mute needs the cloud even though entering it does not');

// Muted, a press of the mic button must not open one either.
v.toggleMute();
mark = world.since();
v.startListening();
await settle();
expect(world.grew(mark) === 0, 'listening started while muted and reached the network; mute must stop the microphone path before it asks anything of the cloud');
v.toggleMute();

// --- cancel-listening -------------------------------------------------------
// A real hold: the server path, a recorder running, audio captured.
v.setServerTranscription({ model: 'asr-test' });
v.startListening();
await settle();
expect(v._session !== null, 'no recording session is open, so cancelling below would be cancelling nothing');
expect(world.recorders.length === 1, 'the microphone never started recording');

mark = world.since();
v.cancelListening();
await settle();
expect(world.grew(mark) === 0, 'cancelListening made ' + world.grew(mark) + ' network request(s); a discarded hold owes the server nothing and must not need it to be abandoned');
expect(v.listening === false, 'the voice still reads as listening after cancelListening');

// --- the contrast that keeps the claim honest -------------------------------
// stopListening is NOT network-free and is not claimed to be: ending a hold
// hands on what was already said, which is a delivery. Asserted so that the
// line between the two controls stays where it was drawn on purpose.
v.startListening();
await settle();
mark = world.since();
v.stopListening();
await settle();
expect(world.grew(mark) === 1, 'stopListening no longer delivers the hold it ended (it made ' + world.grew(mark) + ' requests); what was already said must still be sent, and if that has changed then cancelListening and stopListening have quietly become the same control');
expect(world.fetches[world.fetches.length - 1].url === '/v1/transcribe', 'the delivered hold went somewhere other than /v1/transcribe');
`)
}

// TestNFR01_MuteAndStopStillWorkWhileTheEndpointIsSlowErroringOrDisconnecting
// runs the same controls with a request already in flight to /v1/speech, under
// each of the three shapes of degradation in turn.
//
// This is the case the baseline test cannot reach. stopSpeaking has an abort in
// it, and an abort only means anything when there is something to abort — so
// here she is asked to speak in FORGE's own voice, the request is left hanging
// or is killed by the degraded network, and the stop control is used on top of
// that.
//
// ‼️ The assertion is about NEW requests, deliberately. stopSpeaking aborts the
// in-flight POST /v1/speech, and aborting a request that has already been sent
// is not the same as making one: no bytes go out, nothing is waited on, and the
// abort takes effect whether or not the server ever answers. Counting total
// network activity here would fence the wrong thing — it would forbid the
// abort, which is the very mechanism that keeps the stop inside AUD-02's 250ms
// budget when the vendor is hanging.
func TestNFR01_MuteAndStopStillWorkWhileTheEndpointIsSlowErroringOrDisconnecting(t *testing.T) {
	runNFR01Scenario(t, `
const FV = load();
const m = makeVoice(FV), v = m.v;

for (const mode of ['slow', 'erroring', 'refusing', 'disconnecting']) {
  world.degrade(mode);
  world.fetches.length = 0;
  world.synth.cancels = 0;
  world.synth.speaking = false;
  m.errors.length = 0;
  // Fresh per mode: remoteVoice latches to false after a vendor failure, and
  // _toldFallback latches the one-per-page explanation. Left alone, only the
  // first mode would exercise the remote path at all.
  v.remoteVoice = undefined;
  v._toldFallback = false;
  v.muted = false;

  v.speak('the wall is now four millimetres thick');
  await settle();

  // speak() itself asks the cloud. That request is speaking, not stopping, and
  // it is the thing the controls below have to survive.
  expect(world.fetches.length === 1, mode + ': speaking should have made exactly one request to her voice, made ' + world.fetches.length);
  expect(world.fetches[0].url === '/v1/speech', mode + ': her voice was fetched from ' + world.fetches[0].url);
  const inFlight = world.fetches[0].signal;
  expect(inFlight !== null, mode + ': the speech request carries no abort signal, so stopSpeaking cannot call it back and an outage would let her start talking after she was interrupted');

  // --- stop, with the endpoint in this state --------------------------------
  let mark = world.since();
  v.stopSpeaking();
  await settle();
  expect(world.grew(mark) === 0, mode + ': stopSpeaking made ' + world.grew(mark) + ' NEW network request(s); NFR-01 requires stop to remain available during degradation, and a stop that has to reach a degraded endpoint is the one control guaranteed to fail exactly when it is needed');
  expect(inFlight.aborted === true, mode + ': the in-flight speech request was left running after stopSpeaking, so the audio can still arrive and she starts talking AFTER being interrupted — the one thing a stop must never allow');
  expect(v.speaking === false, mode + ': the voice still reads as speaking after stopSpeaking');
  expect(world.synth.speaking === false, mode + ': the browser voice is still reading after stopSpeaking');

  // --- mute, with the endpoint in this state --------------------------------
  mark = world.since();
  expect(v.toggleMute() === true, mode + ': toggleMute did not enter mute');
  expect(v.muted === true, mode + ': mute did not take effect');
  expect(world.grew(mark) === 0, mode + ': muting made ' + world.grew(mark) + ' NEW network request(s) while the endpoint was ' + mode);
  mark = world.since();
  expect(v.toggleMute() === false, mode + ': toggleMute did not leave mute');
  expect(world.grew(mark) === 0, mode + ': unmuting made ' + world.grew(mark) + ' NEW network request(s) while the endpoint was ' + mode);

  // --- cancel a hold, with the endpoint in this state -----------------------
  v.setServerTranscription({ model: 'asr-test' });
  v.startListening();
  await settle();
  expect(v._session !== null, mode + ': no recording session opened, so the cancel below proves nothing');
  mark = world.since();
  v.cancelListening();
  await settle();
  expect(world.grew(mark) === 0, mode + ': cancelListening made ' + world.grew(mark) + ' NEW network request(s) while the endpoint was ' + mode);
  expect(v.listening === false, mode + ': the voice still reads as listening after cancelListening');

  // Nothing above may hang. If a control had awaited the degraded request, the
  // scenario would not have reached this line under 'slow' at all — which is
  // the shape this loop exists to catch.
  expect(true, '');
}
`)
}

// ---------------------------------------------------------------------------
// The multi-party control: room mute. The Gap 1 ruling, as a fence.
// ---------------------------------------------------------------------------

// TestNFR01_ARefusedRoomMuteSaysItIsNotInForceRatherThanShowingItApplied is the
// server side of NFR-01, and it is deliberately NOT a test that the room mute
// keeps working during an outage — because it does not, and must not pretend to.
//
// # The conflict, and the ruling
//
// Room mute is server-authoritative on purpose: media.SFU.forward drops the
// packets, and internal/httpapi/rooms_media.go says why — "a mute that only
// stops the browser sending is a picture of a mute". Room.setState disables the
// local track first as a latency optimisation and then POSTs the control that
// actually enforces it. That is in direct tension with NFR-01's "remain
// available during cloud degradation", and the tension is real rather than a
// bug: a client that reported a mute as applied on the strength of its local
// track alone would be telling the person a room full of people cannot hear
// them, with no way to know whether that is true.
//
// The ruling recorded here and in docs/spikes/2026-09-20-nfr01-availability:
// NFR-01's guarantee is met by the SINGLE-USER controls — toggleMute,
// stopSpeaking, cancelListening — which are local and network-free, fenced
// above. The MULTI-PARTY controls are scoped OUT of it, on the ground that
// during real degradation the SFU is not forwarding the audio either, so there
// is no one to be overheard by. What is owed in that state is not a working
// mute; it is the truth. This test fences the truth.
//
// So the invariant is: when the server refuses, the person is TOLD the change
// is not in force. A silent catch here — or a client that assumed success —
// would turn an availability shortfall into a privacy failure, which is a much
// worse thing to be down.
//
// Run as JS rather than Go because the honesty lives in the browser: the Go
// handler's job is to refuse, and it is the .catch in room.js setState that
// decides whether the refusal reaches a person or is swallowed.
func TestNFR01_ARefusedRoomMuteSaysItIsNotInForceRatherThanShowingItApplied(t *testing.T) {
	runNFR01Scenario(t, `
const FR = loadRoom();
expect(FR.supported === true, 'this world has no RTCPeerConnection, so room.js would not have loaded the way a browser loads it');

for (const mode of ['erroring', 'refusing', 'disconnecting']) {
  world.degrade(mode);
  world.fetches.length = 0;
  world.roomEvents.length = 0;
  const r = makeRoom(FR);

  const settled = r.setState('muted');

  // The local half happens first and without waiting, which is the latency
  // optimisation room.js describes. It is asserted because it is what makes the
  // outcome ambiguous: the microphone really has stopped feeding the encoder,
  // so a client that stopped here would look entirely correct.
  expect(r.track.enabled === false, mode + ': the local audio track was not disabled before the server was asked, so the mute waits on a round trip it does not need to wait on');
  expect(roomEvents('state').length === 1, mode + ': muting announced no state change locally');

  await settled;
  await settle();

  expect(world.fetches.length === 1, mode + ': room mute made ' + world.fetches.length + ' requests, expected exactly one');
  expect(world.fetches[0].url === '/v1/rooms/room-1/media/state', mode + ': the room mute was sent to ' + world.fetches[0].url);

  const told = roomEvents('error');
  expect(told.length === 1, mode + ': the server refused the mute and the person was told ' + told.length + ' times. Room mute is enforced at the server (media.SFU.forward drops the packets), so a refusal means the mute is NOT in force — and a control that silently failed is worse than one that was never offered, because the person now believes a room full of people cannot hear them');
  expect(told.length > 0 && /did not accept/.test(told[0].data.message), mode + ': the refusal was reported without saying the server did not accept the change: ' + (told.length ? told[0].data.message : 'nothing was reported'));

  // And it does not throw: the failure has to reach the UI as an event it can
  // render, not as an unhandled rejection in the console where nobody is.
  expect(true, '');
}

// end-recording (AUD-07's "end"), the other multi-party control, is the same
// ruling: it is a server control and it REJECTS rather than resolving, so a
// caller cannot mistake a failure for a stop. Fenced because the alternative —
// swallowing it — would let a room believe it had stopped being written down.
world.degrade('erroring');
world.fetches.length = 0;
const r2 = makeRoom(FR);
let rejected = false;
await r2.setTranscribing(false).then(function () {}, function () { rejected = true; });
expect(world.fetches.length === 1, 'end-recording made ' + world.fetches.length + ' requests, expected exactly one');
expect(world.fetches[0].url === '/v1/rooms/room-1/transcribing', 'end-recording was sent to ' + world.fetches[0].url);
expect(rejected === true, 'end-recording resolved while the server was unreachable, so a caller cannot tell a stopped transcription from an unreachable one — and the room would be told it had stopped being written down when nothing had changed');
`)
}

// ---------------------------------------------------------------------------
// The measurement the 99.9% is computed from.
// ---------------------------------------------------------------------------

// nfr01UnreachablePool is a pool pointed at a port nothing is listening on.
//
// Why not a nil pool or a closed one: nil panics inside pgx rather than
// returning an error, and closing a real pool needs a real database, which
// would make this fence skip on a laptop without one. A pool that exists, is
// configured, and cannot reach anything is precisely the state NFR-01's
// measurement has to survive — the process is fine, the database is gone.
//
// Port 1 is reserved (tcpmux) and refuses immediately on every platform this
// builds for, so the readiness check fails fast rather than sitting out its
// three-second budget.
func nfr01UnreachablePool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig("postgres://forge:nothing@127.0.0.1:1/forge?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	cfg.MinConns = 0
	cfg.ConnConfig.ConnectTimeout = 2 * time.Second
	// NewWithConfig does not connect, which is the whole point: db.Connect pings
	// and would fail here, and a pool that never existed cannot be asked whether
	// the database is reachable.
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestNFR01_LivenessAnswersWithoutTouchingTheDatabase fences the shape the
// availability measurement is defined in terms of.
//
// docs/spikes/2026-09-20-nfr01-availability/README.md computes the monthly
// percentage from GET /readyz, and it can only do that because GET /healthz is
// a DIFFERENT question with a different answer. /healthz says "this process is
// running"; it deliberately does not touch the database, because if it did, a
// database outage would make every orchestrator restart every instance and turn
// a recoverable dependency failure into a restart storm — see the comment on
// HealthHandlers.Live.
//
// That is a measurement property as well as an operational one. If /healthz
// ever started reading the database, a database outage would read as the
// process being dead: the k8s liveness probe (deploy/k8s/30-forged.yaml) would
// kill healthy processes, and the availability number would be computed over a
// fleet that was being restarted BY its own monitoring. This fence is what
// stops a future refactor from folding the two probes into one because they
// look so similar.
//
// It is asserted twice over, because the two failures look different: once with
// no pool configured at all (any database read panics) and once with a pool
// that cannot reach anything (any database read blocks and then errors).
func TestNFR01_LivenessAnswersWithoutTouchingTheDatabase(t *testing.T) {
	for _, tc := range []struct {
		name string
		pool func(t *testing.T) *pgxpool.Pool
	}{
		{"with no database configured at all", func(t *testing.T) *pgxpool.Pool { return nil }},
		{"with a database that cannot be reached", nfr01UnreachablePool},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := testDeps()
			d.Pool = tc.pool(t)
			h := NewHealthHandlers(d)

			rec := httptest.NewRecorder()
			start := time.Now()
			h.Live(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
			took := time.Since(start)

			if rec.Code != http.StatusOK {
				t.Fatalf("liveness answered %d %s. /healthz must answer that the PROCESS is alive whatever the database is doing: if a database outage makes liveness fail, every orchestrator restarts every instance and a recoverable dependency failure becomes a restart storm — and the availability measurement in docs/spikes/2026-09-20-nfr01-availability is then taken over a fleet its own monitoring is killing", rec.Code, tc.name)
			}
			// A liveness probe that reads the database would not only fail here,
			// it would take the connect timeout to do it. The budget is far
			// below any plausible database round trip and far above any
			// plausible JSON encode, so it separates the two without being
			// flaky on a loaded laptop.
			if took > time.Second {
				t.Fatalf("liveness took %s %s. /healthz answered, but it took long enough to have waited on something: it must not touch the database, because the liveness probe's deadline is what decides whether a running process is killed", took, tc.name)
			}

			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("liveness body did not decode as JSON %s: %v", tc.name, err)
			}
			if body["status"] != "ok" {
				t.Fatalf("liveness reported status %v %s, want \"ok\". An external checker reads this field; a probe whose body changed shape would be scored as a failed sample and would spend the error budget on nothing", body["status"], tc.name)
			}
			// version and commit are what tie a sample to a build. Without them
			// an availability figure cannot be attributed to the release that
			// earned it.
			if body["version"] != "test" || body["commit"] != "test" {
				t.Fatalf("liveness dropped version/commit (%v/%v) %s. They are how an outage is attributed to a build, and a measurement that cannot name the release it measured cannot be acted on", body["version"], body["commit"], tc.name)
			}
			if _, reported := body["checks"]; reported {
				t.Fatalf("liveness reported dependency checks %s. /healthz answering dependency questions is exactly how it acquires a database read: the two probes are separate because the answers have different consequences, and /readyz is where a dependency belongs", tc.name)
			}
		})
	}
}

// TestNFR01_ReadinessReportsTheDatabaseAndRefusesTrafficWhenItIsUnreachable
// fences the other half of the measurement.
//
// /readyz is the probe the availability number is computed from, so its
// contract is load-bearing in a way a health endpoint's usually is not:
//
//   - it must actually check the database, or the number measures nothing;
//   - it must answer 503 when the check fails, because that is the signal an
//     external checker scores as a failed sample and what makes k8s take the
//     instance out of the service (deploy/k8s/30-forged.yaml, deploy/verify.sh);
//   - it must report WHICH check failed, because "unavailable" without a cause
//     turns every outage into an investigation that starts from nothing.
//
// A refactor that made /readyz answer 200-with-a-warning would not break any
// user-visible behaviour and would silently take the availability figure to
// 100%. This is the fence that stops it.
func TestNFR01_ReadinessReportsTheDatabaseAndRefusesTrafficWhenItIsUnreachable(t *testing.T) {
	d := testDeps()
	d.Pool = nfr01UnreachablePool(t)
	h := NewHealthHandlers(d)

	rec := httptest.NewRecorder()
	h.Ready(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness answered %d with an unreachable database, want 503. 503 is the whole signal: it is what takes the instance out of the service and what an external checker scores as a failed sample, so a readiness probe that answers 200 while the database is gone makes the 99.9%% in NFR-01 a number about nothing", rec.Code)
	}

	var body struct {
		Status string `json:"status"`
		Checks struct {
			Database struct {
				OK        bool   `json:"ok"`
				LatencyMS int64  `json:"latency_ms"`
				Error     string `json:"error"`
			} `json:"database"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("readiness body did not decode as JSON: %v", err)
	}
	if body.Status != "unavailable" {
		t.Fatalf("readiness reported status %q, want \"unavailable\". The status field is what a checker reads without parsing the tree; changing it changes what every sample means", body.Status)
	}
	if body.Checks.Database.OK {
		t.Fatalf("readiness reported the database check as ok while the database was unreachable. The check is the measurement: a probe that reports success for a dependency it never reached is worse than no probe, because it produces a confident wrong number")
	}
	if body.Checks.Database.Error == "" {
		t.Fatalf("readiness refused without saying which check failed. \"unavailable\" with no cause makes every outage start from nothing, and the reason a failing dependency is named here is so the person reading the sample knows whether the model endpoint, the database or the process is the problem")
	}
	// latency_ms is reported whether or not the check passed, because "healthy"
	// and "answering in four seconds" are different states and only one of them
	// precedes an outage. A failed check that reports no timing cannot
	// distinguish a refused connection from a database that timed out.
	if body.Checks.Database.LatencyMS < 0 {
		t.Fatalf("readiness reported a negative database latency (%d ms)", body.Checks.Database.LatencyMS)
	}

	// And liveness, on the same broken deps, still says the process is alive.
	// The pair is the point: an outage of the database must be distinguishable
	// from an outage of the process, or the measurement cannot tell the reader
	// which one they are in.
	liveRec := httptest.NewRecorder()
	h.Live(liveRec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if liveRec.Code != http.StatusOK {
		t.Fatalf("with the database unreachable, liveness answered %d and readiness answered %d. Both failing says the process is dead when only its database is, which is the restart storm HealthHandlers.Live exists to avoid — and it also collapses the two signals the availability method is defined in terms of into one", liveRec.Code, rec.Code)
	}
}
