/* FORGE voice — full-duplex conversation with barge-in.
 *
 * Uses the browser's own speech stack (SpeechRecognition + speechSynthesis).
 * That is a deliberate choice, not a shortcut: it keeps audio on the device,
 * needs no streaming media server, and — the part that actually matters — lets
 * barge-in be handled locally, in the same event loop as playback, rather than
 * over a network round trip. A stop that has to travel to a server and back is
 * not a barge-in, it is a delay.
 *
 * PRD requirements this implements:
 *   AUD-01  listen while speaking; interruption without losing state
 *   AUD-02  ≤250ms to stop speech on detected barge-in
 *   AUD-03  push-to-talk or hands-free, selectable
 *   AUD-04  numbers, units and identifiers read back unambiguously
 *   AUD-05  identifies itself as AI; voice and rate are choosable
 *   AUD-06  captions for everything spoken, keyboard operation, non-audio path
 *   AUD-07  mute, stop-speaking and end-session always reachable
 *
 * Honest limits, stated because a voice interface that overstates itself is
 * worse than one that admits its edges:
 *   - Browser SpeechRecognition is Chrome/Edge (and it sends audio to Google's
 *     service). Firefox and Safari do not implement it usefully. Where it is
 *     missing the UI says so and the text path stays fully functional.
 *   - AUD-02's ≤700ms first-audio target is NOT verified. It depends on the
 *     model, the network, and the device. The workbench measures and displays
 *     the real figure rather than claiming the target.
 */
(function (global) {
  'use strict';

  var SR = global.SpeechRecognition || global.webkitSpeechRecognition;

  /* A short silent MP3, played once on the first gesture so a later reply can
   * play without one.
   *
   * ‼️ Not a WAV, and not a zero-length file. The first version of this was a
   * 44-byte WAV header with NO samples, and browsers reject an empty media file
   * outright — so the unlock threw, unlocked nothing, and left the very problem
   * it was added to remove. It has to be a file the browser will actually
   * decode, which is also why it is MP3: the same reason /v1/speech serves MP3. */
  var SILENT_MP3 = 'data:audio/mpeg;base64,SUQzBAAAAAAAI1RTU0UAAAAPAAADTGF2ZjU4Ljc2LjEwMAAAAAAAAAAAAAAA//tQxAADB8AhSmxhIIEVCSiJrDCQBTcu3UrAIwUdkRgQbFAZC1CQEwTJ9mjRvBA4UOLD8nKVOWfh+UlK3z/177OXrfOdKl7pyn3Xf//WreyTRUoAWgBgkOAGbZHBgG1OF6zM82DWbZaUmMBptgQhGjsyYqc9ae9XFz280948NMBWInljyzsNRFLPWdnZGWrddDsjK1unuSrVN9jJsK8KuQtQCtMBjCEtImISdNKJOouhYnb17nJfrfvltIQAAAAAAAA=';

  function Voice(opts) {
    opts = opts || {};
    this.onTranscript = opts.onTranscript || function () {};
    this.onPartial = opts.onPartial || function () {};
    this.onState = opts.onState || function () {};
    this.onBargeIn = opts.onBargeIn || function () {};
    this.onError = opts.onError || function () {};
    /* onLevel receives a REAL microphone level, 0..1, while listening.
     *
     * It is strictly optional and strictly additive: it drives the orb's
     * waveform, and everything about the conversation works without it. If the
     * browser has no AudioContext, or the user's permission covers only what
     * SpeechRecognition asked for, the meter never opens and nothing is said
     * about it — the alternative would be an error message about a decoration. */
    this.onLevel = opts.onLevel || function () {};

    this.mode = 'push';         // 'push' | 'hands-free'
    this.listening = false;
    this.speaking = false;
    this.muted = false;
    this.rate = 1.0;
    this.voiceName = null;
    this.available = !!SR;
    this.synthAvailable = !!global.speechSynthesis;
    /* undefined = not yet tried, false = this deployment has no usable server
     * voice and we stop asking. Never persisted: a vendor that was down when
     * the page loaded may be up on the next load. */
    this.remoteVoice = undefined;
    this._audio = null;
    this._remoteAbort = null;
    this._spoken = '';
    this._echoTail = null;

    if (SR) this._initRecognition();

    /* Take the first user gesture as permission to play audio later.
     *
     * Enabling hands-free, granting the microphone, pressing send — all of them
     * are gestures, and one silent play() during any of them is what lets her
     * speak without a click later on. Cheap, once per page, and it removes the
     * autoplay refusal rather than only recovering from it. */
    var self = this;
    var unlock = function () {
      global.removeEventListener('pointerdown', unlock, true);
      global.removeEventListener('keydown', unlock, true);
      if (self._unlocked) return;
      self._unlocked = true;
      try {
        var a = new Audio(SILENT_MP3);
        a.volume = 0;
        var p = a.play();
        if (p && p.catch) p.catch(function () { /* nothing to recover */ });
      } catch (e) { /* no Audio here; the browser voice still works */ }
    };
    global.addEventListener('pointerdown', unlock, true);
    global.addEventListener('keydown', unlock, true);
  }

  Voice.prototype._initRecognition = function () {
    var self = this;
    var rec = new SR();
    rec.continuous = true;
    // Interim results are what make barge-in possible at all: waiting for a
    // final transcript means waiting for the speaker to stop, which is exactly
    // what interruption is not.
    rec.interimResults = true;
    rec.lang = 'en-US';

    rec.onresult = function (event) {
      var interim = '', final = '';
      for (var i = event.resultIndex; i < event.results.length; i++) {
        var text = event.results[i][0].transcript;
        if (event.results[i].isFinal) final += text;
        else interim += text;
      }

      /* Is this her own voice coming back through the microphone?
       *
       * In hands-free the mic is open while she speaks, and it hears the
       * speakers. Every word she says was then transcribed as though the person
       * had said it, submitted, answered, spoken — and heard again. That is an
       * endless conversation with herself, and it is what an open mic plus a
       * loudspeaker does unless something tells them apart.
       *
       * Barge-in and echo look identical at this layer: both are "speech while
       * FORGE is talking". What separates them is WHOSE WORDS THEY ARE, so that
       * is what gets checked — the heard text against the text she is currently
       * saying. Hers, discard it: she is not interrupted by herself, and it must
       * never be submitted. Not hers, it is a real interruption and behaves
       * exactly as before.
       *
       * Deliberately not solved by closing the mic while she speaks. That would
       * end the loop and end barge-in with it, and AUD-01 asks for full duplex —
       * being able to interrupt her is the point. */
      var heard = (final || interim);
      if (heard && self.speaking && self._isOwnEcho(heard)) {
        if (interim) return;            // her own words, still forming
        return;                         // her own words, final — never submit
      }

      // Barge-in: the moment ANY speech is detected while FORGE is talking,
      // stop talking. Measured from here to silence, this is a single
      // synchronous call — no network, no scheduler — which is what keeps it
      // inside the 250ms budget.
      if ((interim || final) && self.speaking) {
        var t0 = performance.now();
        self.stopSpeaking();
        self.onBargeIn(performance.now() - t0);
      }

      if (interim) self.onPartial(interim);
      if (final.trim()) self.onTranscript(final.trim());
    };

    rec.onerror = function (e) {
      // 'no-speech' and 'aborted' are ordinary in hands-free use and are not
      // worth showing a user.
      if (e.error === 'no-speech' || e.error === 'aborted') return;
      if (e.error === 'not-allowed' || e.error === 'service-not-allowed') {
        self.available = false;
        self.onError('Microphone access was refused. Voice is unavailable; the text box still works.');
        self._setState();
        return;
      }
      self.onError('Speech recognition error: ' + e.error);
    };

    rec.onend = function () {
      // The browser ends recognition on its own schedule. In hands-free mode it
      // is restarted, so a session does not silently stop listening after a
      // pause — which looks exactly like the app having crashed.
      if (self.mode === 'hands-free' && self.listening && !self.muted) {
        try { rec.start(); } catch (err) { /* already starting */ }
      } else {
        self.listening = false;
        self._setState();
      }
    };

    this.rec = rec;
  };

  Voice.prototype._setState = function () {
    this.onState({
      listening: this.listening,
      speaking: this.speaking,
      muted: this.muted,
      mode: this.mode,
      available: this.available,
      synthAvailable: this.synthAvailable
    });
  };

  Voice.prototype.setMode = function (mode) {
    this.mode = mode;
    if (mode === 'push' && this.listening) this.stopListening();
    this._setState();
  };

  Voice.prototype.startListening = function () {
    if (!this.rec || this.muted) return;
    try {
      this.rec.start();
      this.listening = true;
      this._setState();
      this._openMeter();
    } catch (e) { /* already running */ }
  };

  Voice.prototype.stopListening = function () {
    if (!this.rec) return;
    this.listening = false;
    try { this.rec.stop(); } catch (e) {}
    this._closeMeter();
    this._setState();
  };

  /* The level meter.
   *
   * # Why the stream is opened and closed with each listen rather than held
   *
   * Holding it would be cheaper and would remove the small delay before the
   * first sample. It is not done, because a held stream keeps the operating
   * system's microphone indicator lit for as long as the tab is open — and the
   * whole point of push-to-talk here is that the microphone is demonstrably not
   * on when nobody is holding it. A visual nicety must not be the reason a
   * privacy property stops being true.
   *
   * # Why every failure is silent
   *
   * This measures something for the sake of a drawing. Nothing in the
   * conversation depends on it, so a failure produces no message, no state
   * change, and no interruption to speech recognition — which has already
   * started by the time this runs.
   */
  Voice.prototype._openMeter = function () {
    var self = this;
    if (this._meter || !global.AudioContext || !navigator.mediaDevices) return;
    this._meter = { stopped: false };

    navigator.mediaDevices.getUserMedia({ audio: true }).then(function (stream) {
      if (!self._meter || self._meter.stopped) {
        stream.getTracks().forEach(function (t) { t.stop(); });
        return;
      }
      var ctx = new global.AudioContext();
      var src = ctx.createMediaStreamSource(stream);
      var analyser = ctx.createAnalyser();
      analyser.fftSize = 512;
      analyser.smoothingTimeConstant = 0.6;
      src.connect(analyser);

      var buf = new Uint8Array(analyser.frequencyBinCount);
      self._meter.stream = stream;
      self._meter.ctx = ctx;

      // A plain interval rather than rAF: this is a measurement, and it must
      // keep its own time rather than inherit the rendering clock's stalls.
      self._meter.timer = global.setInterval(function () {
        analyser.getByteFrequencyData(buf);
        var sum = 0;
        for (var i = 0; i < buf.length; i++) sum += buf[i] * buf[i];
        var rms = Math.sqrt(sum / buf.length) / 255;
        // Speech sits low in a linear 0..1 scale; the curve puts an ordinary
        // speaking voice in the middle of the range instead of at the floor.
        self.onLevel(Math.min(1, Math.pow(rms * 2.6, 0.75)));
      }, 50);
    }).catch(function () {
      self._meter = null; // no meter, no message: see above
    });
  };

  Voice.prototype._closeMeter = function () {
    var m = this._meter;
    if (!m) return;
    m.stopped = true;
    this._meter = null;
    if (m.timer) global.clearInterval(m.timer);
    if (m.stream) m.stream.getTracks().forEach(function (t) { t.stop(); });
    if (m.ctx && m.ctx.close) { try { m.ctx.close(); } catch (e) {} }
    this.onLevel(0);
  };

  Voice.prototype.toggleMute = function () {
    this.muted = !this.muted;
    if (this.muted) {
      this.stopListening();
      this.stopSpeaking();
    } else if (this.mode === 'hands-free') {
      this.startListening();
    }
    this._setState();
    return this.muted;
  };

  /* speak reads text aloud.
   *
   * The text is normalised first so that identifiers, versions and units are
   * spoken unambiguously (PRD AUD-04): "v0.2.0" read as "vee zero point two
   * point zero" is a number a listener can write down; read as "vee zero point
   * twenty" it is not. */
  Voice.prototype.speak = function (text, onDone) {
    if (this.muted || !text) {
      if (onDone) onDone();
      return;
    }
    var self = this;
    this.stopSpeaking();

    /* FORGE's own voice first, the browser's as the fallback.
     *
     * speechSynthesis reads her in whatever voice the machine has — Samantha on
     * a Mac, something else on Windows — so the character sounds different on
     * every device. Where the deployment has a speech vendor she has ONE voice,
     * and it is the same one the media plane uses in rooms.
     *
     * The fallback is not a degradation to apologise for: it needs no vendor,
     * no key and no network, and it is what a deployment without a vendor has.
     * Which is why a failure here is silent and immediate — the answer is
     * already on screen, and a person waiting to hear it must not wait through
     * a retry to find out the vendor is down. */
    /* The echo guard needs to know what she is about to say BEFORE the audio
     * starts, because recognition can hear the first syllable before play()
     * resolves. */
    this._nowSpeaking(text);

    if (this.remoteVoice !== false) {
      this._speakRemote(text, onDone);
      return;
    }
    this._speakLocal(text, onDone);
  };

  /* What a media element means when it refuses. Spelled out because the number
   * alone is unreadable, and the DIFFERENCE between 3 and 4 is the whole
   * diagnosis: 3 means the decoder got the bytes and choked on them, 4 means it
   * would not even accept the source. */
  var MEDIA_ERR = { 1: 'ABORTED', 2: 'NETWORK', 3: 'DECODE', 4: 'SRC_NOT_SUPPORTED' };

  /* Everything needed to tell the plausible failures apart, in one line.
   *
   * ‼️ Written because "could not decode the audio (audio/mpeg)" was true and
   * useless. It was reported from production and cost a full investigation that
   * excluded, one at a time: the vendor (a frame walk proved the MP3 well
   * formed), the handler (the access log counts bytes actually written to the
   * socket), the ingress (large assets arrive byte-identical) and the decoder
   * (that exact file plays in Chromium). All four were sound, and the message
   * had narrowed nothing — because it reported the CONTENT TYPE, which was
   * never in doubt, and not the size, which nobody could see.
   *
   * The three things that can actually be wrong, and what separates them:
   *   - truncation      received < declared
   *   - wrong container blob.type is not what the element was given
   *   - decoder refusal media error 3 or 4 on a complete body
   *
   * A body with no declared length is called out as chunked rather than
   * compared against nothing, so "no Content-Length" cannot read as "matched".
   *
   * ‼️ A complete body plus media error 4 is the signature of a load the
   * browser REFUSED rather than failed to decode — a Content Security Policy
   * block looks exactly like an unplayable file from here, and that is what
   * this actually was: docs/bugfix/2026-09-08-csp-blocked-her-own-voice.md.
   * Check the console for a CSP violation before suspecting the audio. */
  function describeWire(wire, blob, mediaError) {
    var bits = [];
    var got = blob && typeof blob.size === 'number' ? blob.size : 0;
    bits.push(blob && blob.type ? blob.type : 'unknown type');
    if (wire && wire.declared != null && wire.declared !== '') {
      bits.push(got + ' bytes received of ' + wire.declared + ' declared' +
        (String(got) === String(wire.declared) ? '' : ' — TRUNCATED'));
    } else {
      bits.push(got + ' bytes received, no declared length (chunked)');
    }
    if (wire && wire.encoding && wire.encoding !== 'identity') {
      bits.push('content-encoding ' + wire.encoding);
    }
    if (wire && wire.ctype && blob && blob.type && wire.ctype.indexOf(blob.type) !== 0) {
      bits.push('header said ' + wire.ctype);
    }
    if (mediaError) {
      bits.push('media error ' + mediaError.code + ' ' +
        (MEDIA_ERR[mediaError.code] || 'UNKNOWN'));
    }
    return bits.join(', ');
  }

  /* Ask the server to synthesise. On any failure — no vendor configured, vendor
   * down, audio that will not play — fall through to the browser once and
   * remember, so one outage does not cost a round trip per utterance. */
  Voice.prototype._speakRemote = function (text, onDone) {
    var self = this;
    var ctl = new AbortController();
    this._remoteAbort = ctl;

    /* ‼️ One failed load fires TWO handlers, and each used to fall back.
     *
     * A media element whose source will not decode fires `error` on the element
     * AND rejects the play() promise with NotSupportedError — both, for one
     * file. Each path called _speakLocal, so the browser voice read the whole
     * reply twice, while _fellBack's own latch still showed a single banner:
     * the symptom people reported was "she repeats herself", with nothing on
     * screen to connect it to the audio failure that caused it.
     *
     * Measured in Chrome against an undecodable blob typed audio/mpeg:
     *   ['onerror', 'play-rejected:NotSupportedError']  — two, every time.
     *
     * So the fallback is latched per utterance: the FIRST path to fail owns it
     * and owns the reason, and any later path is the same failure seen a second
     * time. Note this is a DIFFERENT latch from `remoteVoice`, which spans the
     * page — this one spans one utterance, so an autoplay refusal can still
     * decline to disable her voice for the rest of the session.
     *
     * See docs/bugfix/2026-09-08-she-said-every-reply-twice.md
     * Fence: scripts/echo-guard-check.js (single-fallback rule). */
    var handled = false;
    var url = null;
    /* Read off the response and kept for the failure messages: by the time a
     * media element refuses, the Response is long gone. */
    var wire = null;
    var failOver = function (why, latch) {
      if (handled) return;
      handled = true;
      if (url) { URL.revokeObjectURL(url); url = null; }
      self._audio = null;
      if (latch) self.remoteVoice = false;
      self._fellBack(why);
      self._speakLocal(text, onDone);
    };

    global.fetch('/v1/speech', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify({ text: readable(text) }),
      signal: ctl.signal
    }).then(function (r) {
      if (!r.ok) throw new Error('speech ' + r.status);
      wire = {
        ctype: (r.headers && r.headers.get('content-type')) || '',
        encoding: (r.headers && r.headers.get('content-encoding')) || 'identity',
        declared: r.headers ? r.headers.get('content-length') : null
      };
      return r.blob();
    }).then(function (blob) {
      /* An empty body is a distinct fault and must say so. It reaches the
       * element as a source it cannot open, which reports as a DECODE failure
       * and sends the reader hunting for a codec problem that is not there. */
      if (!blob || !blob.size) {
        failOver('the server sent no audio (' + describeWire(wire, blob, null) + ')', true);
        return;
      }
      url = URL.createObjectURL(blob);
      var audio = new Audio(url);
      /* playbackRate, not rate. `rate` is the SpeechSynthesisUtterance spelling
       * and it is not a property of a media element, so assigning it merely
       * added a stray field to the object and the chosen speech rate was
       * silently ignored on this path — she read at 1.0 whatever the setting
       * said, while the browser-voice path honoured it. Nothing errored, which
       * is why it survived: a typo'd property on a JS object is not a fault.
       * Fenced by scripts/voice-fallback-check.js. */
      audio.playbackRate = self.rate;
      self._audio = audio;

      var done = function () {
        if (url) { URL.revokeObjectURL(url); url = null; }
        self.speaking = false;
        self._audio = null;
        self._doneSpeaking();
        self._setState();
        if (onDone) onDone();
      };
      audio.onplay = function () { self.speaking = true; self._setState(); };
      audio.onended = done;
      audio.onerror = function () {
        /* The bytes arrived and would not decode. That is this path failing,
         * not the vendor, so it falls back — and latches, because a file the
         * browser cannot decode will not decode next time either. */
        failOver('this browser could not decode the audio (' +
          describeWire(wire, blob, audio.error) + ')', true);
      };
      audio.play().catch(function (err) {
        /* ‼️ An autoplay refusal is NOT a vendor failure and must not latch.
         *
         * A browser rejects programmatic play() until the page has had a user
         * gesture. In hands-free the person SPOKE rather than clicked, so there
         * may never have been one — and the first reply of the session hit
         * exactly that. Latching on it disabled her voice for the rest of the
         * page after a synthesis that had already succeeded and been paid for:
         * observed once in production, 200 from /v1/speech, and never heard.
         *
         * So this falls back for THIS utterance only and tries again on the
         * next, by which time a gesture has almost certainly happened. */
        if (err && err.name === 'NotAllowedError') {
          failOver('the browser blocked audio until you interact with the page — ' +
            'click anywhere and she will use her own voice from the next reply', false);
          return;
        }
        failOver('playback failed: ' + (err && err.name ? err.name : 'unknown') +
          (err && err.message ? ' — ' + err.message : '') +
          ' (' + describeWire(wire, blob, audio.error) + ')', true);
      });
    }).catch(function (err) {
      if (err && err.name === 'AbortError') return;   // interrupted, not failed
      /* Latched, so a deployment with no vendor pays one request per page
       * rather than one per utterance. */
      failOver('could not fetch her voice: ' +
        (err && err.message ? err.message : 'request failed'), true);
    });
  };

  /* Say WHY her own voice was not used.
   *
   * Falling back to the browser's voice is the correct behaviour and it is also
   * silent, which made it undiagnosable: the server logged 200 and a synthesis
   * it had been paid for, and the only symptom anybody could report was "still
   * the browser voice". Every signal on the server said success; the failure was
   * entirely in the browser and left no trace anywhere.
   *
   * Reported once per page — a reason repeated on every reply is noise, and the
   * first one is the one that explains it. */
  Voice.prototype._fellBack = function (why) {
    if (this._toldFallback) return;
    this._toldFallback = true;
    if (global.console && console.warn) console.warn('FORGE voice fell back:', why);
    this.onError("Using the browser's voice instead of FORGE's: " + why);
  };

  Voice.prototype._speakLocal = function (text, onDone) {
    if (!this.synthAvailable) {
      if (onDone) onDone();
      return;
    }
    var self = this;

    var utter = new SpeechSynthesisUtterance(readable(text));
    utter.rate = this.rate;
    utter.pitch = 1.0;

    if (this.voiceName) {
      var match = global.speechSynthesis.getVoices().filter(function (v) {
        return v.name === self.voiceName;
      })[0];
      if (match) utter.voice = match;
    }

    utter.onstart = function () { self.speaking = true; self._setState(); };
    utter.onend = function () { self.speaking = false; self._doneSpeaking(); self._setState(); if (onDone) onDone(); };
    utter.onerror = function () { self.speaking = false; self._doneSpeaking(); self._setState(); if (onDone) onDone(); };

    global.speechSynthesis.speak(utter);
  };

  /* Stop, whichever path is speaking.
   *
   * AUD-07 requires stop-speaking to be always reachable, and AUD-02 requires a
   * barge-in to silence her within 250ms — so this cancels the browser voice,
   * stops any audio element, AND aborts a synthesis request that has not
   * arrived yet. Leaving that request in flight would let her start speaking
   * AFTER the person interrupted her, which is the one thing a barge-in must
   * never do. */

  /* ---- telling her own voice from an interruption ----------------------- */

  /* What she is saying right now, normalised for comparison, plus a short tail
   * after she stops.
   *
   * The tail matters: recognition lags the audio by a beat, so the last words
   * of an utterance arrive AFTER speaking has finished. Without it the final
   * fragment of every reply gets submitted, which is the loop again — just one
   * message per turn instead of continuously. 1.6s covers the lag without
   * swallowing a person who answers immediately.
   */
  Voice.prototype._nowSpeaking = function (text) {
    this._spoken = normaliseForEcho(text || '');
    if (this._echoTail) { global.clearTimeout(this._echoTail); this._echoTail = null; }
  };

  Voice.prototype._doneSpeaking = function () {
    var self = this;
    if (this._echoTail) global.clearTimeout(this._echoTail);
    this._echoTail = global.setTimeout(function () {
      self._spoken = '';
      self._echoTail = null;
    }, 1600);
  };

  /* True when the heard text is made of the words she is currently saying.
   *
   * Word overlap rather than substring: recognition drops words, mishears
   * others and never punctuates, so "I am FORGE your engineering partner" comes
   * back as "hello I am" or "Forge" — both real examples from the loop this
   * fixes. A substring test misses those; asking what fraction of the heard
   * words appear in hers catches them.
   *
   * The threshold errs toward treating speech as HERS. Getting it wrong in that
   * direction drops one interruption and the person repeats themselves; getting
   * it wrong the other way restarts an infinite loop. Short heard fragments —
   * one or two words, which is what echo usually produces — need every word to
   * match, because at that length a coincidence is likely. */
  Voice.prototype._isOwnEcho = function (heard) {
    if (!this._spoken) return false;
    var h = normaliseForEcho(heard).split(' ').filter(Boolean);
    if (!h.length) return false;
    var mine = ' ' + this._spoken + ' ';
    var hits = 0;
    for (var i = 0; i < h.length; i++) {
      if (mine.indexOf(' ' + h[i] + ' ') !== -1) hits++;
    }
    var ratio = hits / h.length;
    return h.length <= 2 ? ratio === 1 : ratio >= 0.6;
  };

  function normaliseForEcho(s) {
    return String(s).toLowerCase().replace(/[^a-z0-9\s]/g, ' ')
      .replace(/\s+/g, ' ').trim();
  }

  Voice.prototype.stopSpeaking = function () {
    this._doneSpeaking();
    if (this._remoteAbort) {
      try { this._remoteAbort.abort(); } catch (e) { /* already settled */ }
      this._remoteAbort = null;
    }
    if (this._audio) {
      try { this._audio.pause(); } catch (e) { /* not started */ }
      this._audio = null;
    }
    if (this.synthAvailable) global.speechSynthesis.cancel();
    this.speaking = false;
    this._setState();
  };

  Voice.prototype.voices = function () {
    return this.synthAvailable ? global.speechSynthesis.getVoices() : [];
  };

  /* One axis value: a signed number and, optionally, the unit written after it.
   * Composed into COORD three times rather than typed out three times, because
   * the three have to stay identical. Mirrors coordinateSegment and
   * coordinateExpr in internal/media/readback.go. */
  var COORD_SEGMENT = '(-?\\d+(?:\\.\\d+)?(?:\\s?[A-Za-z°]+)?)';
  var COORD = new RegExp(
    '\\(\\s*' + COORD_SEGMENT +
    '\\s*,\\s*' + COORD_SEGMENT +
    '\\s*,\\s*' + COORD_SEGMENT + '\\s*\\)', 'g');

  /* spokenSign makes a leading minus audible. "-" is silent in the voices this
   * is synthesised through, so "-40 mm" is heard as "40 millimetres": a
   * coordinate on the wrong side of the datum, the same class of failure as the
   * silent ± below.
   *
   * Scoped to coordinates on purpose. A general rule would also rewrite dates
   * ("2026-09-03") and ranges ("5-10mm"), where the hyphen is not a sign. */
  function spokenSign(seg) {
    return seg.charAt(0) === '-' ? 'minus ' + seg.slice(1) : seg;
  }

  /* readable rewrites text so a listener can transcribe it correctly.
   *
   * PRD AUD-04 requires numbers, units, tolerances and identifiers to be read
   * back unambiguously. These substitutions are the ones that actually change
   * meaning when a listener writes down what they heard.
   *
   * THIS FUNCTION HAS A TWIN: internal/media/readback.go. The workbench speaks
   * through the browser and a room speaks through the server, two runtimes under
   * one requirement, so the rules are written twice. A rule added here and not
   * there is read back correctly in the workbench and wrongly in a room — which
   * is exactly what happened when the room voice shipped. Change both, and add a
   * case to TestReadbackMakesTextTranscribable.
   *
   * TestTheReadbackRulesHaveNotDrifted counts the .replace() calls below and
   * fails when the two copies stop agreeing, so adding a rule here alone turns
   * the Go suite red rather than going unnoticed. */
  function readable(text) {
    return text
      // Dotted-number strings — versions, IP addresses, build numbers — segment
      // by segment. Any number of segments: written as exactly three, it read
      // "1.2.3.4" as "1 point 2 point 3.4", the first three spoken and the
      // fourth left as a decimal.
      //
      // Two dots minimum, so a plain decimal is left alone: "2.5" and "$2.50"
      // are already read correctly. No \b at the front, deliberately — there is
      // no word boundary between the "v" and the "0" of "v0.2.0", so a leading
      // \b skipped the most common way a version is written. It did, on both
      // copies, until it was noticed. The \b at the end keeps "1.2.3mm" out of
      // here and leaves it to the unit rules.
      .replace(/\d+(?:\.\d+){2,}\b/g, function (m) {
        return m.split('.').join(' point ');
      })
      // Coordinates, spoken with their axes and their signs. A position leaves
      // the geometry model as "(12.5 mm, 0 mm, -40 mm)"; parentheses and commas
      // are silent, so a listener hears three numbers with nothing to say which
      // is which axis, and the silent minus puts the part 80mm from where it
      // is. The frame those axes belong to is deliberately not added here — it
      // is not in the text, and this layer must not invent a datum (RSN-06).
      // Three segments only: a pair in prose is more likely to be a list.
      // Why: docs/bugfix/2026-09-03-coordinates-read-back-without-axes-or-sign.md
      .replace(COORD, function (_, x, y, z) {
        return 'X ' + spokenSign(x) + ', Y ' + spokenSign(y) + ', Z ' + spokenSign(z);
      })
      // Units, spelled out. "5mm" heard as "five em em" is not a measurement.
      .replace(/(\d)\s?mm\b/g, '$1 millimetres')
      .replace(/(\d)\s?cm\b/g, '$1 centimetres')
      .replace(/(\d)\s?kg\b/g, '$1 kilograms')
      .replace(/(\d)\s?ms\b/g, '$1 milliseconds')
      .replace(/(\d)\s?Nm\b/g, '$1 newton metres')
      .replace(/(\d)\s?°C\b/g, '$1 degrees Celsius')
      // ± is silent in most voices, which turns a tolerance into a bare number.
      .replace(/±/g, ' plus or minus ')
      // Identifiers are read as letters and digits rather than as a word.
      .replace(/\b([a-z]{3})_([0-9A-Z]{6})[0-9A-Z]*\b/g, function (_, prefix, head) {
        return prefix.split('').join(' ') + ' ' + head.split('').join(' ') + ' and so on';
      })
      // Markdown that would otherwise be read aloud as punctuation.
      .replace(/[*_`#]/g, '')
      .replace(/\s+/g, ' ')
      .trim();
  }

  Voice.prototype.readable = readable;

  /* whyUnavailable explains the gap rather than leaving a dead button.
   * "Voice is off" with no reason is the kind of thing people file bugs about. */
  Voice.prototype.whyUnavailable = function () {
    if (this.available) return '';
    if (!SR) {
      return 'This browser has no speech recognition. Chrome and Edge have it; ' +
             'Firefox and Safari do not. Everything works by typing.';
    }
    return 'Microphone access was refused. Everything works by typing.';
  };

  global.ForgeVoice = { Voice: Voice, readable: readable, supported: !!SR };
})(window);
