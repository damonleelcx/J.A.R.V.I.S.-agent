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

    if (SR) this._initRecognition();
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
    if (!this.synthAvailable || this.muted || !text) {
      if (onDone) onDone();
      return;
    }
    var self = this;
    this.stopSpeaking();

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
    utter.onend = function () { self.speaking = false; self._setState(); if (onDone) onDone(); };
    utter.onerror = function () { self.speaking = false; self._setState(); if (onDone) onDone(); };

    global.speechSynthesis.speak(utter);
  };

  Voice.prototype.stopSpeaking = function () {
    if (!this.synthAvailable) return;
    global.speechSynthesis.cancel();
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
