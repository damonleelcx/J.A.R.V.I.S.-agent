#!/usr/bin/env node
/* Does ONE failed audio load produce exactly ONE spoken reply?
 *
 * A media element whose source will not decode fires `error` on the element AND
 * rejects the play() promise — both, for one file. Each of those paths in
 * _speakRemote fell back to the browser voice, so FORGE read every reply TWICE
 * whenever her own voice failed to decode. Reported from production on
 * 2026-09-08 alongside "could not decode the audio (audio/mpeg)": one banner on
 * screen, two voices in the room.
 *
 * The banner latched (_fellBack reports once per page), which is exactly why
 * nothing on screen connected the repetition to the audio failure underneath it.
 *
 * A string fence cannot catch this — the old code "looked" correct in both
 * handlers, and each was individually right. Only running both and counting the
 * utterances shows it. So this drives the real _speakRemote against a stubbed
 * browser and counts.
 *
 *   make test-voice-fallback   (or: node scripts/voice-fallback-check.js)
 *
 * See docs/bugfix/2026-09-08-she-said-every-reply-twice.md
 */
const fs = require('fs');
/* Path is overridable so the fence can be proven to go RED against a known-bad
 * copy WITHOUT editing the tree under test — a drill that mutates the working
 * copy produces failures indistinguishable from real ones. */
const target = process.argv[2] || (__dirname + '/../internal/httpapi/assets/voice.js');
const src = fs.readFileSync(target, 'utf8');

/* voice.js reaches for these as free variables, not through `global`. */
URL.createObjectURL = () => 'blob:stub';
URL.revokeObjectURL = () => {};
globalThis.SpeechSynthesisUtterance = function (t) { this.text = t; };

let spokenLocally = [];      // what the BROWSER voice was asked to say
const speechSynthesis = {
  speak(u) {
    spokenLocally.push(u.text);
    // The browser fires onstart/onend; without them onDone never runs.
    if (u.onstart) u.onstart();
    if (u.onend) u.onend();
  },
  cancel() {}, getVoices() { return []; }
};

const g = { window: {}, document: { addEventListener(){}, removeEventListener(){} },
            addEventListener(){}, removeEventListener(){}, setTimeout, clearTimeout,
            speechSynthesis, console: { warn(){} } };
g.global = g;
new Function('global', src.replace('})(window);', '})(global);'))(g);
const V = g.ForgeVoice.Voice;

/* An <audio> whose source will not decode, in both orders the browser can
 * deliver the two failures. Chrome was measured emitting onerror first; the
 * order is not specified anywhere, so both are fenced. */
function stubAudio(order) {
  globalThis.Audio = function () {
    this.play = () => {
      const err = new Error('no supported source');
      err.name = 'NotSupportedError';
      /* A real element populates .error before firing onerror. The stub must
       * too, or the fence silently stops covering the media-error code — the
       * field that separates "the decoder choked" (3) from "the source was
       * refused outright" (4). */
      this.error = { code: 4 };
      if (order === 'error-first') {
        if (this.onerror) this.onerror();
        return Promise.reject(err);
      }
      return Promise.reject(err).catch(e => {
        if (this.onerror) this.onerror();
        throw e;
      });
    };
  };
}

/* A response shaped like the real one: a body with a SIZE, and headers the
 * diagnostics read. A stub blob with no `size` would trip the empty-body guard
 * and quietly test a different path than the one named. */
function okResponse(size, declared, ctype) {
  const headers = { get: (k) => ({
    'content-type': ctype || 'audio/mpeg',
    'content-length': declared === undefined ? String(size) : declared,
    'content-encoding': null
  })[String(k).toLowerCase()] };
  return { ok: true, headers,
           blob: () => Promise.resolve({ type: ctype || 'audio/mpeg', size }) };
}

function run(label, order, expectSpoken, expectDone) {
  spokenLocally = [];
  stubAudio(order);
  g.fetch = () => Promise.resolve(okResponse(147956));

  const v = new V({});
  let doneCalls = 0;
  v.speak('Hello. I am ready to work on a design goal.', () => { doneCalls++; });

  return new Promise(r => setTimeout(r, 60)).then(() => {
    const okSpoken = spokenLocally.length === expectSpoken;
    const okDone = doneCalls === expectDone;
    const ok = okSpoken && okDone;
    console.log(`  ${ok ? 'PASS' : 'FAIL'}  ${label.padEnd(38)} spoken=${spokenLocally.length} (want ${expectSpoken})  onDone=${doneCalls} (want ${expectDone})`);
    if (!ok && spokenLocally.length > 1) {
      console.log(`        she said it ${spokenLocally.length} times: the failed load fell back once per handler`);
    }
    return ok;
  });
}

(async () => {
  console.log('--- one undecodable file must produce ONE spoken reply ---');
  const results = [];
  results.push(await run('onerror, then play() rejects', 'error-first', 1, 1));
  results.push(await run('play() rejects, then onerror', 'reject-first', 1, 1));

  console.log('--- audio that plays must NOT use the browser voice at all ---');
  spokenLocally = [];
  let element = null;
  globalThis.Audio = function () {
    element = this;
    this.play = () => { if (this.onplay) this.onplay(); return Promise.resolve(); };
  };
  g.fetch = () => Promise.resolve(okResponse(147956));
  const v = new V({});
  v.rate = 1.5;
  v.speak('Hello.', () => {});
  await new Promise(r => setTimeout(r, 60));
  const quiet = spokenLocally.length === 0;
  console.log(`  ${quiet ? 'PASS' : 'FAIL'}  ${'her own voice played'.padEnd(38)} spoken=${spokenLocally.length} (want 0)`);
  results.push(quiet);

  /* The chosen speech rate must reach the element. It was assigned to `rate`,
   * the SpeechSynthesisUtterance spelling, which is not a property of a media
   * element — so the setting was silently dropped on this path and nothing
   * errored. Asserted on the element rather than by grepping the source,
   * because the bug was a plausible-looking assignment, not a missing one. */
  console.log('--- the speech rate must reach the audio element ---');
  const rated = element && element.playbackRate === 1.5;
  console.log(`  ${rated ? 'PASS' : 'FAIL'}  ${'rate 1.5 applied to the element'.padEnd(38)} playbackRate=${element && element.playbackRate} (want 1.5)`);
  if (!rated) {
    console.log('        the rate never reached playback: check it is not being assigned to `rate`');
  }
  results.push(!!rated);

  /* An empty body reaches the element as an unopenable source and reports as a
   * decode failure, which sends the reader after a codec problem that is not
   * there. It must be named for what it is, and still fall back exactly once. */
  console.log('--- an empty body must be reported as empty, not as a decode failure ---');
  spokenLocally = [];
  let reason = '';
  stubAudio('error-first');
  g.fetch = () => Promise.resolve(okResponse(0));
  const v2 = new V({ onError: (m) => { reason = m; } });
  v2.speak('Hello.', () => {});
  await new Promise(r => setTimeout(r, 60));
  const named = /no audio/.test(reason) && /0 bytes/.test(reason);
  const once = spokenLocally.length === 1;
  console.log(`  ${named && once ? 'PASS' : 'FAIL'}  ${'empty body named, spoken once'.padEnd(38)} spoken=${spokenLocally.length} (want 1)`);
  console.log(`        reason: ${reason || '(none)'}`);
  results.push(named && once);

  /* Truncation is the failure the old message could not distinguish from a
   * codec problem, so the diagnosis must say so in words. */
  console.log('--- a short body must be called TRUNCATED ---');
  spokenLocally = []; reason = '';
  stubAudio('error-first');
  g.fetch = () => Promise.resolve(okResponse(1024, '147956'));
  const v3 = new V({ onError: (m) => { reason = m; } });
  v3.speak('Hello.', () => {});
  await new Promise(r => setTimeout(r, 60));
  const flagged = /TRUNCATED/.test(reason) && /1024 bytes received of 147956/.test(reason);
  console.log(`  ${flagged ? 'PASS' : 'FAIL'}  ${'short body flagged as truncated'.padEnd(38)}`);
  console.log(`        reason: ${reason || '(none)'}`);
  results.push(flagged);

  /* The media error code must survive into the message. */
  console.log('--- the media error code must reach the message ---');
  spokenLocally = []; reason = '';
  stubAudio('error-first');
  g.fetch = () => Promise.resolve(okResponse(147956));
  const v4 = new V({ onError: (m) => { reason = m; } });
  v4.speak('Hello.', () => {});
  await new Promise(r => setTimeout(r, 60));
  const coded = /media error 4 SRC_NOT_SUPPORTED/.test(reason);
  console.log(`  ${coded ? 'PASS' : 'FAIL'}  ${'media error 4 named in the reason'.padEnd(38)}`);
  console.log(`        reason: ${reason || '(none)'}`);
  results.push(coded);

  const failed = results.filter(x => !x).length;
  console.log(failed === 0 ? '\nONE FAILURE, ONE FALLBACK' : `\n${failed} CASE(S) WRONG`);
  process.exit(failed ? 1 : 0);
})();
