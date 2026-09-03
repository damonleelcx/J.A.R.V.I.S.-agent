/* FORGE orb — the voice surface's presence.
 *
 * # What this draws and why it is drawn rather than styled
 *
 * The workbench's primary interface is speech (PRD §1.2), and a speech interface
 * with no visible state is one where the only way to find out whether it heard
 * you is to wait. The orb is that state, made continuous: it is quiet when FORGE
 * is idle, it moves with YOUR VOICE while it is listening, it turns over while it
 * is thinking, and it pulses while it is speaking.
 *
 * # What it is NOT
 *
 * It is not FORGE's avatar and it does not draw one. FORGE already has an avatar
 * — the character portrait in internal/httpapi/assets/portrait/, and the sigil in
 * internal/persona/avatar.go — and both are served by the application. This
 * canvas draws only the AURA around them: the glow, the waveform, and the two
 * rings. The portrait sits on top of it as a real element (see `.orb-face` in
 * workbench.css), so what a person sees in the middle of the workbench is the
 * character, not an interpretation of her.
 *
 * An earlier version drew the sigil's three blades here as vector paths. That was
 * wrong: it was a second, hand-maintained copy of an identity that already has
 * exactly one source, and it would have drifted from the real mark the first time
 * either changed.
 *
 * # The one honesty rule in here
 *
 * While `listening`, the waveform is driven by a real measurement — an
 * AnalyserNode on the microphone stream, handed in through setLevel(). In every
 * other state there is nothing to measure (the browser's speech synthesiser
 * exposes no output level), so the motion is ambient and is deliberately a
 * different shape: a slow symmetric breath rather than the ragged band that
 * speech produces. It must never be possible to mistake decoration for a reading,
 * so decoration is not drawn in the shape of a reading.
 *
 * # Why it does not rely on requestAnimationFrame alone
 *
 * An rAF-only loop has no recovery. If a frame throws, or the tab returns from
 * bfcache with the chain un-rearmed, or rAF is simply never serviced, the canvas
 * holds frame one forever — which looks exactly like a correctly finished render
 * and reports nothing. So: the loop is re-armed BEFORE the frame body, a
 * visibility-gated watchdog takes over with setTimeout if no frame has run for a
 * second, and the first frame is drawn synchronously at construction so the orb
 * is never a blank rectangle even if no clock ever ticks.
 */
(function (global) {
  'use strict';

  var TAU = Math.PI * 2;
  var REDUCED = global.matchMedia && global.matchMedia('(prefers-reduced-motion: reduce)').matches;

  /* Fixed jitter per particle. Regenerated every frame it would shimmer like
   * static; seeded once it reads as a cloud with depth. */
  function seededJitter(n) {
    var out = new Float32Array(n * 3);
    var s = 0x2f6e2b1;
    for (var i = 0; i < n * 3; i++) {
      s = (s * 1103515245 + 12345) & 0x7fffffff;
      out[i] = (s / 0x7fffffff);
    }
    return out;
  }

  var PARTICLES = 300;
  var JITTER = seededJitter(PARTICLES);

  function Orb(canvas) {
    this.canvas = canvas;
    this.ctx = canvas.getContext('2d');
    this.state = 'idle';
    this.level = 0;        // 0..1, the measured microphone level while listening
    this.smooth = 0;
    this.levelAt = 0;      // when the last measurement arrived
    this.t = 0;
    this.w = 0;
    this.h = 0;
    this.faults = 0;
    this.lastFrame = 0;
    this.running = false;

    var self = this;
    this._resize();
    if (global.ResizeObserver) {
      this._ro = new ResizeObserver(function () { self._resize(); self.draw(); });
      this._ro.observe(canvas);
    } else {
      global.addEventListener('resize', function () { self._resize(); self.draw(); });
    }

    // Frame one, synchronously. Everything below is recovery for a clock that
    // may never tick; this is the guarantee that something is on screen anyway.
    this.draw();
    this.start();

    // Re-kick on every event that can leave the chain un-armed.
    ['visibilitychange', 'pageshow', 'focus'].forEach(function (ev) {
      global.addEventListener(ev, function () { self.start(); });
    });
  }

  Orb.prototype._resize = function () {
    var dpr = Math.min(global.devicePixelRatio || 1, 2);
    var r = this.canvas.getBoundingClientRect();
    var w = Math.max(1, Math.round(r.width));
    var h = Math.max(1, Math.round(r.height));
    if (w === this.w && h === this.h && this._dpr === dpr) return;
    this.w = w; this.h = h; this._dpr = dpr;
    this.canvas.width = Math.round(w * dpr);
    this.canvas.height = Math.round(h * dpr);
    this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
  };

  Orb.prototype.setState = function (s) {
    if (this.state === s) return;
    this.state = s;
    this.start();
    if (REDUCED) this.draw();
  };

  /* setLevel takes a REAL measurement, 0..1. Stamped with its arrival time so
   * that a measurement stream which stops (the mic released, the analyser torn
   * down) decays back to ambient instead of freezing at whatever the last
   * sample happened to be. A frozen reading is a lie that looks like data. */
  Orb.prototype.setLevel = function (v) {
    this.level = Math.max(0, Math.min(1, v || 0));
    this.levelAt = Date.now();
  };

  Orb.prototype.start = function () {
    if (REDUCED || this.running) return;
    this.running = true;
    this._arm();
    this._watch();
  };

  Orb.prototype._arm = function () {
    var self = this;
    if (!this.running) return;
    // Re-armed BEFORE the work, so an exception inside a frame costs one frame
    // rather than the whole animation.
    this._raf = global.requestAnimationFrame(function () { self._frame('raf'); });
  };

  Orb.prototype._frame = function (clock) {
    if (clock === 'raf') this._rafAlive = true;
    this.lastFrame = Date.now();
    this._arm();
    try {
      this.t += 1 / 60;
      this.draw();
      this.faults = 0;
    } catch (e) {
      // Counted rather than ignored. A frame that throws every time is a bug
      // that must stop rather than burn a core forever.
      if (++this.faults > 30) {
        this.running = false;
        if (global.console) global.console.warn('forge.orb.frame_failed', e);
      }
    }
  };

  /* The watchdog. Gated on document visibility: a genuinely backgrounded tab
   * should be left alone, but a VISIBLE page whose rAF is not being serviced is
   * a stuck animation and must be driven by another clock. */
  Orb.prototype._watch = function () {
    var self = this;
    if (this._watchdog) return;
    this._watchdog = global.setInterval(function () {
      if (!self.running) return;
      if (global.document.visibilityState !== 'visible') return;
      if (Date.now() - self.lastFrame < 1000) return;
      self._rafAlive = false;
      self._frame('timeout');
      if (!self._rafAlive) {
        // rAF is not honouring us; keep frames coming on the timer until it is.
        global.setTimeout(function () { if (!self._rafAlive) self._frame('timeout'); }, 120);
      }
    }, 500);
  };

  Orb.prototype.destroy = function () {
    this.running = false;
    if (this._raf) global.cancelAnimationFrame(this._raf);
    if (this._watchdog) global.clearInterval(this._watchdog);
    if (this._ro) this._ro.disconnect();
  };

  /* amplitude resolves what the waveform should be showing right now.
   *
   * Measured while listening, ambient otherwise, and the two are shaped
   * differently on purpose — see the honesty note at the top of the file. */
  Orb.prototype.amplitude = function () {
    var target;
    var fresh = (Date.now() - this.levelAt) < 300;
    if (this.state === 'listening' && fresh) {
      target = 0.10 + this.level * 0.90;
    } else if (this.state === 'listening') {
      // Listening, but no analyser is available (permission, an old browser).
      // Shown as a low steady band rather than a fake voice.
      target = 0.14;
    } else if (this.state === 'speaking') {
      target = 0.30 + 0.12 * Math.sin(this.t * 3.1) + 0.06 * Math.sin(this.t * 7.7);
    } else if (this.state === 'thinking') {
      target = 0.20 + 0.08 * Math.sin(this.t * 1.7);
    } else {
      target = 0.09 + 0.03 * Math.sin(this.t * 0.9);
    }
    // Smoothed so a single loud sample does not snap the band.
    this.smooth += (target - this.smooth) * 0.18;
    return this.smooth;
  };

  /* The character's three colours, from assets/portrait/README.md:
   *   --shell #f7f8fa  the uniform white, blade highlights
   *   --gold  #d9b25c  trim; the sigil's blade shadow and boundary ring
   *   --core  #4fd8e8  the ornament, collar gem, wrist display — means "active"
   * Nothing outside them is introduced. `thinking` shifts the rim toward gold
   * because that is what the sigil already does when a model call is in flight. */
  var GOLD = '217,178,92';

  var COLORS = {
    idle:      { rim: '79,216,232', wave: [[79,216,232], [120,150,235]] },
    listening: { rim: '79,216,232', wave: [[79,216,232], [200,108,224]] },
    thinking:  { rim: GOLD,         wave: [[217,178,92], [79,216,232]] },
    speaking:  { rim: '124,232,245', wave: [[124,232,245], [200,108,224]] }
  };


  Orb.prototype.draw = function () {
    var ctx = this.ctx, w = this.w, h = this.h;
    if (!w || !h) return;
    ctx.clearRect(0, 0, w, h);

    var cx = w / 2, cy = h / 2;
    var r = Math.min(w, h) * 0.30;
    var amp = REDUCED ? 0.12 : this.amplitude();
    var pal = COLORS[this.state] || COLORS.idle;

    this._glow(ctx, cx, cy, r, amp, pal);
    this._wave(ctx, cx, cy, r, amp, pal, w);
    this._sphere(ctx, cx, cy, r, amp, pal);
    this._boundary(ctx, cx, cy, r, amp);
  };

  /* The gold boundary ring. In the sigil this is "the containing ring — the
   * boundary the work may not leave", and it is present in every state,
   * including failure and completion, because the boundary does not disappear
   * when the work does. Same rule here: it is never conditional. */
  Orb.prototype._boundary = function (ctx, cx, cy, r, amp) {
    var rr = r * 1.14;
    ctx.save();
    ctx.beginPath();
    ctx.arc(cx, cy, rr, 0, TAU);
    ctx.lineWidth = Math.max(1, r * 0.016);
    ctx.strokeStyle = 'rgba(' + GOLD + ',' + (0.30 + amp * 0.22).toFixed(3) + ')';
    ctx.stroke();
    ctx.restore();
  };


  Orb.prototype._glow = function (ctx, cx, cy, r, amp, pal) {
    var g = ctx.createRadialGradient(cx, cy, r * 0.55, cx, cy, r * (1.75 + amp * 0.35));
    g.addColorStop(0, 'rgba(' + pal.rim + ',' + (0.16 + amp * 0.16).toFixed(3) + ')');
    g.addColorStop(0.55, 'rgba(' + pal.rim + ',0.05)');
    g.addColorStop(1, 'rgba(' + pal.rim + ',0)');
    ctx.fillStyle = g;
    ctx.fillRect(cx - r * 2.2, cy - r * 2.2, r * 4.4, r * 4.4);
  };

  /* The particle band. Runs THROUGH the sphere and out past it on both sides,
   * which is what makes the sphere read as translucent rather than as a disc
   * sitting on top of a line. */
  Orb.prototype._wave = function (ctx, cx, cy, r, amp, pal, w) {
    var span = Math.min(w * 0.46, r * 3.1);
    var t = this.t;
    var a = pal.wave[0], b = pal.wave[1];

    for (var i = 0; i < PARTICLES; i++) {
      var u = (i / (PARTICLES - 1)) * 2 - 1;            // -1 .. 1
      var x = cx + u * span;
      // Envelope: the band tapers to nothing at both ends, so it reads as one
      // continuous form rather than a rectangle of dots.
      var env = Math.pow(Math.cos(u * Math.PI / 2), 1.6);
      var jx = JITTER[i * 3], jy = JITTER[i * 3 + 1], jz = JITTER[i * 3 + 2];

      var phase = t * (1.1 + jz * 0.5);
      var y = cy
        + Math.sin(u * 5.2 + phase) * r * 0.34 * amp * env
        + Math.sin(u * 11.7 - phase * 1.6) * r * 0.19 * amp * env
        + Math.sin(u * 2.1 + phase * 0.6) * r * 0.22 * amp * env
        + (jy - 0.5) * r * 0.30 * amp * env;

      // Magenta at the centre, cyan at the edges — the colour tells you where
      // the energy is without needing a scale.
      var m = 1 - Math.abs(u);
      var cr = Math.round(a[0] + (b[0] - a[0]) * m);
      var cg = Math.round(a[1] + (b[1] - a[1]) * m);
      var cb = Math.round(a[2] + (b[2] - a[2]) * m);
      var alpha = (0.20 + env * 0.55) * (0.45 + amp * 0.75) * (0.55 + jx * 0.45);

      ctx.beginPath();
      ctx.fillStyle = 'rgba(' + cr + ',' + cg + ',' + cb + ',' + Math.min(0.95, alpha).toFixed(3) + ')';
      ctx.arc(x, y, 0.5 + jx * 1.4 * (0.5 + env), 0, TAU);
      ctx.fill();
    }
  };

  /* The halo around the portrait.
   *
   * Deliberately has no opaque body and no meridians. Both were drawn when this
   * canvas was the whole subject; now the character's own portrait fills that
   * space, and anything painted underneath it is either invisible or a fringe
   * around her edge. What is left is what a halo actually is: a rim on the
   * portrait's boundary and light behind it. */
  Orb.prototype._sphere = function (ctx, cx, cy, r, amp, pal) {
    var rr = r * (1 + amp * 0.03);

    ctx.save();
    ctx.globalCompositeOperation = 'lighter';

    // Light behind her, so the portrait sits IN the aura rather than on it.
    var back = ctx.createRadialGradient(cx, cy, r * 0.2, cx, cy, r * 1.05);
    back.addColorStop(0, 'rgba(' + pal.rim + ',' + (0.20 + amp * 0.26).toFixed(3) + ')');
    back.addColorStop(1, 'rgba(' + pal.rim + ',0)');
    ctx.beginPath();
    ctx.arc(cx, cy, r * 1.05, 0, TAU);
    ctx.fillStyle = back;
    ctx.fill();

    // Rim. Two passes: a wide soft halo, then a thin bright edge that lands on
    // the portrait's own circular border.
    ctx.beginPath();
    ctx.arc(cx, cy, rr, 0, TAU);
    ctx.lineWidth = Math.max(3, rr * 0.05);
    ctx.strokeStyle = 'rgba(' + pal.rim + ',' + (0.14 + amp * 0.18).toFixed(3) + ')';
    ctx.stroke();

    ctx.beginPath();
    ctx.arc(cx, cy, rr, 0, TAU);
    ctx.lineWidth = Math.max(1, rr * 0.014);
    ctx.strokeStyle = 'rgba(' + pal.rim + ',' + (0.55 + amp * 0.35).toFixed(3) + ')';
    ctx.stroke();
    ctx.restore();
  };

  global.ForgeOrb = { Orb: Orb, reducedMotion: REDUCED };
})(window);
