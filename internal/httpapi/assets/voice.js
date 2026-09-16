/* FORGE voice — full-duplex conversation with barge-in.
 *
 * Two ways in, one way out.
 *
 *   IN, push-to-talk   the page RECORDS while the button is held and FORGE's
 *                      server transcribes the recording (POST /v1/transcribe).
 *   IN, hands-free     the browser's SpeechRecognition, for its interim results:
 *                      barge-in needs to know somebody is talking WHILE they
 *                      talk, and a recording is only text after it ends.
 *   OUT                FORGE's own voice from /v1/speech, the browser's
 *                      speechSynthesis as the fallback.
 *
 * # Why push-to-talk no longer uses the browser's recogniser
 *
 * It used to be the only way in, and "it keeps audio on the device" was the
 * reason given. That was never true where it mattered: Chrome and Edge send
 * SpeechRecognition audio to Google's servers. From mainland China those are
 * unreachable, so a person held the button, spoke, let go — and nothing
 * happened, with no message. Reported from production on 2026-09-15.
 *
 * FORGE's server already transcribed room audio, so push-to-talk now uses that
 * wherever the deployment has a transcriber, and the page says which path is in
 * use. The browser's recogniser remains where it adds something — hands-free,
 * and push-to-talk on a deployment with no transcriber — and is dropped the
 * moment it reports it cannot reach its service.
 *
 * See docs/bugfix/2026-09-15-the-microphone-sent-nothing.md
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
 *   - Hands-free depends on the browser's recogniser: Chrome/Edge, and only on
 *     a network that reaches Google. Firefox and Safari do not implement it
 *     usefully. Where it is missing or blocked the UI says so and push-to-talk
 *     and the text path stay fully functional.
 *   - AUD-02's ≤700ms first-audio target is NOT verified. It depends on the
 *     model, the network, and the device. The workbench measures and displays
 *     the real figure rather than claiming the target.
 */
(function (global) {
  'use strict';

  var SR = global.SpeechRecognition || global.webkitSpeechRecognition;

  /* The longest one hold records. Mirrors maxRecordingSeconds in
   * internal/httpapi/transcribe.go, which names it when refusing. */
  var MAX_RECORDING_MS = 60 * 1000;

  /* Shorter than this is a click, not a hold. A click used to do nothing at
   * all, and "the button does nothing" was part of the report. */
  var MIN_HOLD_MS = 350;

  /* What MediaRecorder is asked for, in order. The first two are Chrome, Edge
   * and Firefox; mp4 is Safari. Whatever is chosen is uploaded as recorded —
   * the server converts nothing. */
  var RECORDING_TYPES = ['audio/webm;codecs=opus', 'audio/webm', 'audio/mp4', 'audio/ogg;codecs=opus'];

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
    this.transcribing = false;  // a released hold is being transcribed
    this.speaking = false;
    this.muted = false;
    this.rate = 1.0;
    this.voiceName = null;
    this.deviceId = null;
    this.synthAvailable = !!global.speechSynthesis;
    /* Whether the server transcribes. undefined = not yet told, which is
     * treated as yes: the upload names its own failure, and asking first would
     * make the first hold of every page wait on a metadata request. */
    this.serverASR = undefined;
    this._serverWhy = '';
    /* Why the browser's recogniser cannot be used, once it has said so. */
    this.browserBroken = '';
    this._refused = false;
    this.available = this.inputPath() !== 'none';
    /* undefined = not yet tried, false = this deployment has no usable server
     * voice and we stop asking. Never persisted: a vendor that was down when
     * the page loaded may be up on the next load. */
    this.remoteVoice = undefined;
    this._audio = null;
    this._remoteAbort = null;
    this._spoken = '';
    this._echoTail = null;

    this._session = null;           // the recording in progress
    this._uploads = 0;              // released holds not yet delivered
    this._delivery = Promise.resolve();
    this._recActive = false;        // the recogniser has a session open
    this._restartWhenEnded = false;

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

    /* Every error says something, except the two that are ours.
     *
     * ‼️ This used to return silently on 'no-speech', and reported 'network' as
     * the bare code. During a hold, 'no-speech' is the only sign the microphone
     * heard nothing; and 'network' is what Chrome reports when Google's service
     * cannot be reached — the whole of the 2026-09-15 report, shown to nobody. */
    rec.onerror = function (e) {
      var code = e && e.error;
      // Ours: cancelListening() aborts a hold that turned out to be a click,
      // and the note for that is already on screen.
      if (code === 'aborted') return;
      if (code === 'no-speech') {
        // Ordinary in hands-free, where it fires on every pause.
        if (self.mode === 'push') {
          self.onError('No speech was heard while the button was held. Check the microphone, then hold and speak.');
        }
        return;
      }
      if (code === 'not-allowed' || code === 'service-not-allowed') {
        self._refused = true;
        self.listening = false;
        self.onError('Microphone access was refused. Voice is unavailable; the text box still works.');
        self._setState();
        return;
      }
      if (code === 'network') {
        self.browserBroken = "The browser's speech recognition could not reach its service: Chrome and Edge " +
          "send the audio to Google's servers, which some networks block, mainland China among them.";
        self.listening = false;
        self._restartWhenEnded = false;
        self.onError(self.browserBroken + ' ' + (self.inputPath() === 'server'
          ? "Push-to-talk uses FORGE's own transcription instead."
          : self.whyUnavailable()));
        self._setState();
        return;
      }
      if (code === 'audio-capture') {
        self.onError('No microphone could be opened for speech recognition. Check which input device the browser is using.');
        return;
      }
      self.onError('Speech recognition error: ' + code);
    };

    rec.onend = function () {
      self._recActive = false;
      /* A press that arrived while the previous session was still closing.
       *
       * ‼️ start() on a recogniser whose last session has not ended throws
       * InvalidStateError. That throw used to be swallowed ("already running"),
       * so pressing again quickly did nothing at all. It is now remembered, and
       * the new session starts here, the moment the old one is gone. */
      if (self.listening && !self.muted && (self._restartWhenEnded || self.mode === 'hands-free')) {
        self._restartWhenEnded = false;
        // The browser ends recognition on its own schedule. In hands-free mode
        // it is restarted, so a session does not silently stop listening after
        // a pause — which looks exactly like the app having crashed.
        self._beginRecognition();
        return;
      }
      self.listening = false;
      self._closeMeter();
      self._setState();
    };

    this.rec = rec;
  };

  /* ---- which way in ------------------------------------------------------ */

  /* setServerTranscription is told by the page what /v1/meta/models said:
   * {model} when the deployment transcribes, null when it does not. */
  Voice.prototype.setServerTranscription = function (info) {
    this.serverASR = info && info.model ? { model: info.model } : null;
    if (!this.serverASR) this._serverWhy = '';
    this._setState();
  };

  Voice.prototype._canRecord = function () {
    var md = global.navigator && global.navigator.mediaDevices;
    return !!(global.MediaRecorder && md && md.getUserMedia);
  };

  Voice.prototype._canRecognise = function () {
    return !!(SR && this.rec && !this.browserBroken && !this._refused);
  };

  /* inputPath is how push-to-talk will hear the next hold:
   * 'server' (record and upload), 'browser' (the recogniser) or 'none'. */
  Voice.prototype.inputPath = function () {
    if (this._refused) return 'none';
    if (this.serverASR !== null && this._canRecord()) return 'server';
    if (this._canRecognise()) return 'browser';
    return 'none';
  };

  Voice.prototype.handsFreeAvailable = function () {
    return this._canRecognise();
  };

  /* describePath is the sentence the page shows about the path in use, so a
   * failure can be read against the path that produced it. */
  Voice.prototype.describePath = function () {
    switch (this.inputPath()) {
      case 'server':
        return 'voice: transcribed by FORGE' +
          (this.serverASR && this.serverASR.model ? ' (' + this.serverASR.model + ')' : '');
      case 'browser':
        return "voice: the browser's speech recognition (Chrome and Edge send it to Google)";
      default:
        return 'voice: unavailable';
    }
  };

  /* whyUnavailable explains the gap rather than leaving a dead button.
   * "Voice is off" with no reason is the kind of thing people file bugs about. */
  Voice.prototype.whyUnavailable = function () {
    if (this.inputPath() !== 'none') return '';
    if (this._refused) {
      return 'Microphone access was refused. Allow the microphone for this site and reload; everything works by typing.';
    }
    var server = this.serverASR === null
      ? (this._serverWhy || 'This deployment has no speech to text: set FORGE_LLM_TRANSCRIBER_MODEL to a ' +
          'transcription model its endpoint serves.')
      : 'This browser cannot record audio for FORGE to transcribe.';
    var browser = this.browserBroken ||
      (SR ? '' : 'This browser has no speech recognition of its own (Chrome and Edge have it; Firefox and Safari do not).');
    return 'The microphone is off. ' + server + (browser ? ' ' + browser : '') + ' Everything works by typing.';
  };

  Voice.prototype.whyNoHandsFree = function () {
    if (this._refused) return 'Microphone access was refused, so hands-free cannot listen.';
    if (this.browserBroken) return 'Hands-free needs the browser’s speech recognition, and it cannot be used here. ' + this.browserBroken;
    return 'Hands-free needs the browser’s own speech recognition, which this browser does not have ' +
      '(Chrome and Edge have it). Push-to-talk still works.';
  };

  Voice.prototype._setState = function () {
    this.available = this.inputPath() !== 'none';
    this.onState({
      listening: this.listening,
      transcribing: this.transcribing,
      speaking: this.speaking,
      muted: this.muted,
      mode: this.mode,
      path: this.inputPath(),
      available: this.available,
      synthAvailable: this.synthAvailable
    });
  };

  Voice.prototype.setMode = function (mode) {
    this.mode = mode;
    if (mode === 'push' && this.listening) this.stopListening();
    this._setState();
  };

  /* ---- listening --------------------------------------------------------- */

  Voice.prototype.startListening = function () {
    if (this.muted) return;
    if (this.mode === 'hands-free') {
      if (this.listening) return;
      if (!this._canRecognise()) { this.onError(this.whyNoHandsFree()); return; }
      this._startRecognition();
      return;
    }
    switch (this.inputPath()) {
      case 'server': this._startRecording(); return;
      case 'browser': this._startRecognition(); return;
      default: this.onError(this.whyUnavailable());
    }
  };

  /* stopListening ends a hold and hands what was said on. */
  Voice.prototype.stopListening = function () {
    if (this._session) { this._finishRecording(false); return; }
    this._restartWhenEnded = false;
    this.listening = false;
    // stop(), not abort(): what was already heard is still delivered.
    if (this.rec && this._recActive) this.rec.stop();
    this._closeMeter();
    this._setState();
  };

  /* cancelListening ends a hold and discards it — a click, not a hold. */
  Voice.prototype.cancelListening = function () {
    if (this._session) { this._finishRecording(true); return; }
    this._restartWhenEnded = false;
    this.listening = false;
    if (this.rec && this._recActive) this.rec.abort();
    this._closeMeter();
    this._setState();
  };

  Voice.prototype._startRecognition = function () {
    this.listening = true;
    this._setState();
    if (this._recActive) {
      // The previous session is still closing; onend starts this one.
      this._restartWhenEnded = true;
      return;
    }
    this._beginRecognition();
  };

  Voice.prototype._beginRecognition = function () {
    try {
      this.rec.start();
    } catch (e) {
      if (e && e.name === 'InvalidStateError') {
        // Still running as far as the browser is concerned: start when it ends.
        this._recActive = true;
        this._restartWhenEnded = true;
        return;
      }
      this.listening = false;
      this._setState();
      this.onError('Speech recognition could not start: ' + describeError(e));
      return;
    }
    this._recActive = true;
    this._openMeter();
  };

  /* ---- push-to-talk by recording ----------------------------------------- */

  Voice.prototype._startRecording = function () {
    var self = this;
    if (this._session) return;   // already held, by the button or the space bar
    var session = { chunks: [], stopped: false, cancelled: false, recorder: null, stream: null, timer: null, failed: '' };
    this._session = session;
    this.listening = true;
    this._setState();

    var opening = global.ForgeAudioInput
      ? global.ForgeAudioInput.open(this.deviceId)
      : global.navigator.mediaDevices.getUserMedia({ audio: true });

    opening.then(function (stream) {
      session.stream = stream;
      if (session.stopped) {
        stopTracks(stream);
        if (!session.cancelled) {
          self._settleUpload();
          self.onError('The microphone was not open yet when the button was released. ' +
            'Hold it until it lights, then speak.');
        }
        return;
      }
      var type = recordingType();
      var recorder;
      try {
        // 32 kbit/s is plenty for speech and keeps a 60-second hold far inside
        // the server's 2 MiB limit.
        recorder = new global.MediaRecorder(stream,
          type ? { mimeType: type, audioBitsPerSecond: 32000 } : { audioBitsPerSecond: 32000 });
      } catch (e) {
        stopTracks(stream);
        self._endSession(session);
        self.onError('This browser could not record from the microphone: ' + describeError(e));
        return;
      }
      session.recorder = recorder;
      recorder.ondataavailable = function (ev) {
        if (ev.data && ev.data.size) session.chunks.push(ev.data);
      };
      recorder.onerror = function (ev) { session.failed = describeError(ev && ev.error); };
      recorder.onstop = function () { self._recordingStopped(session); };
      recorder.start();
      session.timer = global.setTimeout(function () {
        if (self._session !== session) return;
        self.onError('Recording stopped at ' + (MAX_RECORDING_MS / 1000) + ' seconds, the most one hold sends. ' +
          'What you said is being transcribed.');
        self._finishRecording(false);
      }, MAX_RECORDING_MS);
      self._openMeter(stream);
    }, function (err) {
      self._endSession(session);
      self.onError(self._microphoneFailed(err));
    });
  };

  Voice.prototype._endSession = function (session) {
    if (this._session === session) this._session = null;
    session.stopped = true;
    if (session.timer) global.clearTimeout(session.timer);
    this.listening = false;
    this._closeMeter();
    this._setState();
  };

  Voice.prototype._finishRecording = function (cancel) {
    var session = this._session;
    if (!session) return;
    session.cancelled = !!cancel;
    if (!cancel) {
      // Counted at release, not when the upload starts, so the state never
      // flickers to Ready between letting go and the text arriving.
      this._uploads++;
      this.transcribing = true;
    }
    this._endSession(session);
    if (session.recorder && session.recorder.state !== 'inactive') {
      session.recorder.stop();   // onstop → _recordingStopped
    } else if (session.stream) {
      stopTracks(session.stream);
      if (!cancel) this._settleUpload();
    }
    // With neither, the microphone is still opening; opening.then sees
    // session.stopped and says so.
  };

  Voice.prototype._recordingStopped = function (session) {
    if (session.stream) stopTracks(session.stream);
    if (!session.stopped) {
      // The browser ended the recording itself — the device was unplugged, or
      // permission revoked mid-hold. What was captured is still sent.
      this._uploads++;
      this.transcribing = true;
      this._endSession(session);
      this.onError('The recording ended by itself; the microphone may have been disconnected. ' +
        'What was captured is being transcribed.');
    }
    if (session.cancelled) return;
    if (session.failed) {
      this._settleUpload();
      this.onError('Recording failed: ' + session.failed + '. Nothing was sent to FORGE.');
      return;
    }
    var type = (session.recorder && session.recorder.mimeType) ||
      (session.chunks[0] && session.chunks[0].type) || 'audio/webm';
    var blob = new global.Blob(session.chunks, { type: type });
    if (!blob.size) {
      this._settleUpload();
      this.onError('No audio was captured: the microphone produced nothing while the button was held. ' +
        'Check which input device the browser is using. Nothing was sent to FORGE.');
      return;
    }
    this._upload(blob);
  };

  Voice.prototype._upload = function (blob) {
    var self = this;
    var answer = global.fetch('/v1/transcribe', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': blob.type || 'audio/webm' },
      body: blob
    }).then(function (r) {
      return r.json().then(function (body) { return { r: r, body: body || {} }; },
        function () { return { r: r, body: {} }; });
    });

    /* Delivered in the order spoken, whatever order the answers arrive in: two
     * quick holds are one sentence in two halves more often than not. */
    this._delivery = this._delivery.then(function () {
      return answer.then(function (res) {
        self._transcribed(res.r, res.body);
      }, function (err) {
        self.onError('Could not reach the server to transcribe (' + describeError(err) + '). ' +
          'Nothing was sent to FORGE; hold and say it again, or type it.');
      });
    }).then(function () {
      self._settleUpload();
    }, function (err) {
      self._settleUpload();
      self.onError('What was said could not be handed to the conversation: ' + describeError(err));
    });
  };

  Voice.prototype._transcribed = function (r, body) {
    if (!r.ok) {
      var said = (body.details && body.details.detail) || body.message || ('the server answered ' + r.status);
      if (r.status === 501) {
        // This deployment cannot transcribe — configuration, not a fault. The
        // server path is dropped for the page so the next hold does not repeat
        // the round trip, and the reason is kept for whyUnavailable.
        this.serverASR = null;
        this._serverWhy = 'FORGE cannot transcribe on this deployment: ' + said;
        this.onError(this._serverWhy + (this._canRecognise()
          ? " The microphone uses the browser's own speech recognition from now on." : ''));
        this._setState();
        return;
      }
      this.onError('Transcription failed (' + r.status + (body.request_id ? ', request ' + body.request_id : '') +
        '): ' + said + ' Nothing was sent to FORGE; hold and say it again, or type it.');
      return;
    }
    var text = String(body.text == null ? '' : body.text).trim();
    if (!text) {
      this.onError('No words were recognised in that recording. Nothing was sent to FORGE.');
      return;
    }
    this.onTranscript(text);
  };

  Voice.prototype._settleUpload = function () {
    this._uploads = Math.max(0, this._uploads - 1);
    this.transcribing = this._uploads > 0;
    this._setState();
  };

  Voice.prototype._microphoneFailed = function (err) {
    var name = err && err.name;
    if (name === 'NotAllowedError' || name === 'SecurityError') {
      this._refused = true;
      this._setState();
      return 'Microphone access was refused, so nothing can be recorded. Allow the microphone for this site ' +
        'in the browser’s address bar, then reload. The text box still works.';
    }
    if (name === 'NotFoundError' || name === 'OverconstrainedError') {
      return 'No microphone was found. Connect one, or check which input device the browser is using.';
    }
    if (name === 'NotReadableError' || name === 'AbortError') {
      return 'The microphone could not be opened; another application may be using it.';
    }
    return 'Could not open the microphone: ' + describeError(err);
  };

  function recordingType() {
    var MR = global.MediaRecorder;
    if (!MR || !MR.isTypeSupported) return '';
    for (var i = 0; i < RECORDING_TYPES.length; i++) {
      if (MR.isTypeSupported(RECORDING_TYPES[i])) return RECORDING_TYPES[i];
    }
    return '';
  }

  function stopTracks(stream) {
    if (stream && stream.getTracks) stream.getTracks().forEach(function (t) { t.stop(); });
  }

  function describeError(e) {
    if (!e) return 'unknown error';
    if (e.name && e.message) return e.name + ': ' + e.message;
    return String(e.message || e.name || e);
  }

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
   * When a recording is in progress its own stream is measured (borrowed, and
   * not stopped here — the recording owns it), so a hold asks for the
   * microphone once rather than twice.
   *
   * # Why every failure is silent
   *
   * This measures something for the sake of a drawing. Nothing in the
   * conversation depends on it, so a failure produces no message, no state
   * change, and no interruption to speech recognition — which has already
   * started by the time this runs.
   */
  Voice.prototype._openMeter = function (borrowed) {
    var self = this;
    var md = global.navigator && global.navigator.mediaDevices;
    if (this._meter || !global.AudioContext) return;
    if (!borrowed && !md) return;
    var meter = { stopped: false, borrowed: !!borrowed };
    this._meter = meter;

    var attach = function (stream) {
      if (self._meter !== meter || meter.stopped) {
        if (!meter.borrowed) stopTracks(stream);
        return;
      }
      var ctx = new global.AudioContext();
      var src = ctx.createMediaStreamSource(stream);
      var analyser = ctx.createAnalyser();
      analyser.fftSize = 512;
      analyser.smoothingTimeConstant = 0.6;
      src.connect(analyser);

      var buf = new Uint8Array(analyser.frequencyBinCount);
      meter.stream = stream;
      meter.ctx = ctx;

      // A plain interval rather than rAF: this is a measurement, and it must
      // keep its own time rather than inherit the rendering clock's stalls.
      meter.timer = global.setInterval(function () {
        analyser.getByteFrequencyData(buf);
        var sum = 0;
        for (var i = 0; i < buf.length; i++) sum += buf[i] * buf[i];
        var rms = Math.sqrt(sum / buf.length) / 255;
        // Speech sits low in a linear 0..1 scale; the curve puts an ordinary
        // speaking voice in the middle of the range instead of at the floor.
        self.onLevel(Math.min(1, Math.pow(rms * 2.6, 0.75)));
      }, 50);
    };

    if (borrowed) {
      try { attach(borrowed); } catch (e) { this._meter = null; /* no meter, no message: see above */ }
      return;
    }
    md.getUserMedia({ audio: true }).then(attach).catch(function () {
      if (self._meter === meter) self._meter = null; // no meter, no message: see above
    });
  };

  Voice.prototype._closeMeter = function () {
    var m = this._meter;
    if (!m) return;
    m.stopped = true;
    this._meter = null;
    if (m.timer) global.clearInterval(m.timer);
    if (m.stream && !m.borrowed) stopTracks(m.stream);
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

  /* ---- the hold ----------------------------------------------------------- */

  /* makeHold turns presses and releases into listening, for any source — the
   * mic button and the space bar share one, so the two cannot overlap.
   *
   * A release shorter than MIN_HOLD_MS is a click: the recording is discarded
   * and the person is told to hold. It used to do nothing, visibly. */
  function makeHold(voice, opts) {
    opts = opts || {};
    var now = opts.now || function () { return Date.now(); };
    var note = opts.note || function () {};
    var min = opts.minHoldMs == null ? MIN_HOLD_MS : opts.minHoldMs;
    var held = null;
    return {
      holding: function () { return !!held; },
      press: function (who) {
        if (held) return false;
        held = { who: who, at: now() };
        voice.startListening();
        return true;
      },
      release: function (who) {
        if (!held || (who != null && held.who != null && who !== held.who)) return false;
        var took = now() - held.at;
        held = null;
        // Hands-free keeps listening after the button is let go.
        if (voice.mode !== 'push') return true;
        if (took < min) {
          voice.cancelListening();
          note('Hold to talk: keep the button (or the space bar) held down while you speak, then let go.');
          return true;
        }
        voice.stopListening();
        return true;
      }
    };
  }

  /* bindHold wires a button to a hold with pointer events.
   *
   * ‼️ The hold used to end on `mouseleave`. The mic is a 36px circle, and a
   * hand holding a mouse button while talking drifts: the cursor left the
   * circle, the hold ended mid-sentence, and what had been said so far was sent
   * or lost. Pointer capture routes every event for that pointer to the button
   * wherever the cursor goes, so the hold ends when the button is let go and not
   * before. Pointer events also cover touch and pen, which the old
   * mouse/touch pair handled separately.
   *
   * Fenced by TestVoiceInput_TheHoldSurvivesTheCursorLeavingTheButton. */
  function bindHold(button, voice, opts) {
    var hold = makeHold(voice, opts);
    var pointer = null;
    var release = function (e) {
      if (pointer == null || (e && e.pointerId != null && e.pointerId !== pointer)) return;
      pointer = null;
      global.removeEventListener('pointerup', release, true);
      hold.release('pointer');
    };
    button.addEventListener('pointerdown', function (e) {
      if (button.disabled || (e.button != null && e.button !== 0)) return;
      e.preventDefault();
      if (hold.holding()) return;
      pointer = e.pointerId;
      if (button.setPointerCapture && pointer != null) {
        try {
          button.setPointerCapture(pointer);
        } catch (err) {
          // Without capture a release off the button never reaches it, and the
          // microphone would stay open; so the release is watched for on the
          // page instead. Nothing is lost by this, so nothing is said.
          global.addEventListener('pointerup', release, true);
        }
      }
      hold.press('pointer');
    });
    button.addEventListener('pointerup', release);
    button.addEventListener('pointercancel', release);
    // Capture lost without a pointerup (the window lost focus, an alert): end
    // the hold rather than leave the microphone open.
    button.addEventListener('lostpointercapture', release);
    // A long press on a phone opens a context menu and cancels the pointer.
    button.addEventListener('contextmenu', function (e) { e.preventDefault(); });
    return hold;
  }

  /* deliverSpoken hands a transcript to the conversation — or, while FORGE is
   * still answering, to the text box.
   *
   * ‼️ send() returns early while a turn is in flight, and every transcript
   * went straight to it: anything said during "Thinking…" was dropped without a
   * trace. That is where the owner's words went on 2026-09-15, with the first
   * token 33 seconds away.
   *
   * # Why the text box and not a queue
   *
   * A queued utterance is sent later, against a reply the person had not heard
   * when they spoke, and they cannot see or change it first. The text box keeps
   * it visible and editable, appends rather than overwriting anything typed,
   * and sends when the person decides — the same place a typed message waits.
   *
   * Fenced by TestVoiceInput_WhatWasSaidDuringATurnIsKeptInTheTextBox. */
  function deliverSpoken(text, ctx) {
    text = String(text == null ? '' : text).trim();
    if (!text) return 'nothing';
    if (!ctx.busy) {
      ctx.send(text);
      return 'sent';
    }
    var typed = String(ctx.input.value || '');
    var end = typed.length;
    while (end > 0 && /\s/.test(typed.charAt(end - 1))) end--;
    typed = typed.slice(0, end);
    ctx.input.value = typed ? typed + ' ' + text : text;
    ctx.note('FORGE is still answering, so what you said is waiting in the text box instead of being sent. ' +
      'Press send when she has finished.');
    return 'kept';
  }

  /* ---- speaking ---------------------------------------------------------- */

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

  global.ForgeVoice = {
    Voice: Voice,
    readable: readable,
    supported: !!SR,
    makeHold: makeHold,
    bindHold: bindHold,
    deliverSpoken: deliverSpoken,
    MAX_RECORDING_SECONDS: MAX_RECORDING_MS / 1000
  };
})(window);
