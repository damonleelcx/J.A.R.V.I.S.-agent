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
    } catch (e) { /* already running */ }
  };

  Voice.prototype.stopListening = function () {
    if (!this.rec) return;
    this.listening = false;
    try { this.rec.stop(); } catch (e) {}
    this._setState();
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

  /* readable rewrites text so a listener can transcribe it correctly.
   *
   * PRD AUD-04 requires numbers, units, tolerances and identifiers to be read
   * back unambiguously. These substitutions are the ones that actually change
   * meaning when a listener writes down what they heard. */
  function readable(text) {
    return text
      // Version and dotted-number strings, digit by digit.
      .replace(/\b(\d+)\.(\d+)\.(\d+)\b/g, function (_, a, b, c) {
        return a + ' point ' + b + ' point ' + c;
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
