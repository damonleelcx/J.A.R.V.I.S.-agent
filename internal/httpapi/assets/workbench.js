/* FORGE workbench — the conversational, 3D-first surface.
 *
 * Wires three things together: the voice layer, the 3D studio, and the
 * conversation endpoint. Everything structural lives in those; this file is the
 * choreography between them (PRD §5.3).
 *
 * The choreography rules, which are product decisions rather than plumbing:
 *   - Speech is short and the screen carries detail. Reading a parts table aloud
 *     is worse than useless.
 *   - Barge-in stops speech immediately and locally, and the measured latency is
 *     shown rather than the target claimed.
 *   - Geometry always arrives with what it does NOT establish, and that banner
 *     is not dismissible.
 */
(function () {
  'use strict';

  var $ = function (id) { return document.getElementById(id); };

  var studio = null;
  var voice = null;
  var state = {
    history: [],
    prototype: null,
    selectedPart: null,
    speak: true,
    lastLatency: null,
    firstToken: null,
    lastBargeIn: null,
    model: null,
    busy: false
  };

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  /* ---- transcript ------------------------------------------------------- */

  function addTurn(who, body, detail) {
    var el = document.createElement('div');
    el.className = 'turn ' + (who === 'you' ? 'you' : 'forge');
    el.innerHTML = '<div class="lbl">' + (who === 'you' ? 'You' : 'FORGE') + '</div>' +
      '<div class="body">' + esc(body) + '</div>' +
      (detail ? '<div class="detail">' + esc(detail) + '</div>' : '');
    $('transcript').appendChild(el);
    $('transcript').scrollTop = $('transcript').scrollHeight;
    return el;
  }

  var partialEl = null;
  function showPartial(text) {
    if (!partialEl) {
      partialEl = document.createElement('div');
      partialEl.className = 'turn you partial';
      partialEl.innerHTML = '<div class="lbl">You</div><div class="body"></div>';
      $('transcript').appendChild(partialEl);
    }
    partialEl.querySelector('.body').textContent = text;
    $('transcript').scrollTop = $('transcript').scrollHeight;
  }
  function clearPartial() {
    if (partialEl) { partialEl.remove(); partialEl = null; }
  }

  /* ---- the stage -------------------------------------------------------- */

  function describeOnScreen() {
    if (!state.prototype) return '';
    var names = state.prototype.parts.map(function (p) { return p.name || p.id; });
    return state.prototype.name + ' — ' + state.prototype.parts.length + ' part(s): ' + names.join(', ') +
           ' (units: ' + (state.prototype.units || 'unspecified') + ')';
  }

  function loadPrototype(proto) {
    state.prototype = proto;
    state.selectedPart = null;
    $('stage-empty').classList.add('hidden');
    studio.load(proto);
    renderParts();
    renderProvenance();
  }

  function renderParts() {
    var el = $('parts');
    if (!state.prototype) {
      el.innerHTML = '<div class="empty">No geometry yet. Describe something and FORGE will propose a shape.</div>';
      return;
    }
    el.innerHTML = state.prototype.parts.map(function (p) {
      var s = p.size || {};
      var u = state.prototype.units || '';
      /* Rendered per shape rather than by listing every field: a cylinder shown
       * as "0×20×0 r12.5" is noise, and noise in a dimension is worse than none
       * because it reads as a measurement. */
      var dims = [];
      if (p.shape === 'cylinder' || p.shape === 'cone' || p.shape === 'tube') {
        if (s.radius != null) dims.push('⌀' + (s.radius * 2));
        if (s.radius_top != null && s.radius_top !== s.radius) dims.push('top ⌀' + (s.radius_top * 2));
        if (s.height != null) dims.push('h' + s.height);
      } else if (p.shape === 'sphere') {
        if (s.radius != null) dims.push('⌀' + (s.radius * 2));
      } else {
        var w = s.width, h = s.height, d = s.depth;
        if (w != null || h != null || d != null) {
          dims.push([w, h, d].map(function (v) { return v == null ? '?' : v; }).join('×'));
        }
      }
      return '<div class="part" data-id="' + esc(p.id) + '" aria-current="' + (state.selectedPart === p.id) + '">' +
        '<span class="sw" style="background:' + esc(p.color || '#b8bcc4') + '"></span>' +
        '<span class="nm">' + esc(p.name || p.id) +
        '<div class="dim">' + esc(p.shape) + (dims.length ? ' · ' + esc(dims.join(' ')) + ' ' + esc(u) : '') + '</div>' +
        (p.note ? '<div class="dim">' + esc(p.note) + '</div>' : '') +
        '</span></div>';
    }).join('');

    Array.prototype.forEach.call(el.querySelectorAll('.part'), function (node) {
      node.addEventListener('click', function () {
        state.selectedPart = node.getAttribute('data-id');
        studio.select(state.selectedPart);
        renderParts();
      });
    });
  }

  /* The provenance banner. Always present when geometry is; never dismissible.
   * PRD VIS-06: a render must not imply manufacturability, structural adequacy,
   * or compliance — and it is persuasive in inverse proportion to how much has
   * actually been checked. */
  function renderProvenance() {
    var el = $('provenance');
    if (!state.prototype) { el.classList.add('hidden'); return; }
    var p = state.prototype;

    var html = '<b>This is a proposal, not a verified design.</b>';
    if (p.not_verified && p.not_verified.length) {
      html += '<ul>' + p.not_verified.map(function (n) { return '<li>' + esc(n) + '</li>'; }).join('') + '</ul>';
    }
    if (p.assumptions && p.assumptions.length) {
      html += '<div style="margin-top:7px"><b>Assumed, not specified:</b><ul>' +
        p.assumptions.map(function (a) { return '<li>' + esc(a) + '</li>'; }).join('') + '</ul></div>';
    }
    /* Anything the renderer could not draw faithfully belongs here, next to
     * everything else this picture does not establish. A parts list naming a
     * shape the viewport did not draw is the interface asserting something the
     * system did not do. */
    var approx = studio.approximationNotes();
    if (approx.length) {
      html += '<div style="margin-top:7px"><b>Drawn approximately:</b><ul>' +
        approx.map(function (a) { return '<li>' + esc(a) + '</li>'; }).join('') + '</ul></div>';
    }
    html += '<div style="margin-top:7px;opacity:.85">Proposed by ' + esc(p.model_note || 'FORGE') +
            '. No CAD kernel, solver, or interference check exists in this deployment.</div>';
    el.innerHTML = html;
    el.classList.remove('hidden');
  }

  /* ---- talking to FORGE -------------------------------------------------- */

  /* send streams one turn.
   *
   * The point of streaming here is one number: when FORGE starts talking.
   * Measured end to end, a turn that produces geometry takes tens of seconds;
   * the spoken sentence arrives seconds in. Waiting for the whole object before
   * speaking would make the voice interface feel broken even when it is working
   * perfectly.
   *
   * Geometry is still applied only when the complete object has parsed on the
   * server — never from a partial one. */
  function send(text) {
    if (state.busy || !text.trim()) return;
    state.busy = true;
    clearPartial();
    addTurn('you', text);
    state.history.push({ role: 'user', content: text });

    var bubble = addTurn('forge', '…');
    var t0 = performance.now();
    setStatus('thinking');
    var spoke = false;

    streamTurn(text, function (ev) {
      switch (ev.kind) {
        case 'speech':
          bubble.querySelector('.body').textContent = ev.text;
          state.history.push({ role: 'forge', content: ev.text });
          state.firstToken = ev.first_token_ms || Math.round(performance.now() - t0);
          updateMeta();
          // Speaking starts HERE, not at 'done'. This is the whole reason the
          // endpoint streams.
          if (state.speak && ev.text) { spoke = true; voice.speak(ev.text); }
          break;

        case 'detail':
          var d = document.createElement('div');
          d.className = 'detail';
          d.textContent = ev.text;
          bubble.appendChild(d);
          break;

        case 'prototype':
          ev.prototype.model_note = state.model || 'FORGE';
          loadPrototype(ev.prototype);
          break;

        case 'goal':
          renderProposal(ev.goal);
          break;

        case 'error':
          var e = document.createElement('div');
          e.className = 'detail';
          e.style.color = 'var(--bad)';
          e.textContent = ev.error;
          bubble.appendChild(e);
          break;

        case 'done':
          state.model = ev.model || state.model;
          state.lastLatency = ev.total_ms || Math.round(performance.now() - t0);
          updateMeta();
          break;
      }
    }).catch(function (err) {
      bubble.querySelector('.body').textContent = err.message;
      bubble.querySelector('.body').style.color = 'var(--bad)';
    }).then(function () {
      state.busy = false;
      if (!spoke) setStatus('idle');
    });
  }

  /* streamTurn reads the SSE response.
   *
   * EventSource is not used because this is a POST with a body; EventSource can
   * only GET. Reading the fetch body as a stream is the same protocol by hand,
   * and it keeps the request shape honest rather than smuggling a conversation
   * into a query string. */
  function streamTurn(text, onEvent) {
    return fetch('/v1/converse', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        message: text,
        history: state.history.slice(0, -1),
        on_screen: describeOnScreen()
      })
    }).then(function (r) {
      if (!r.ok) {
        return r.json().catch(function () { return {}; }).then(function (b) {
          var e = (b && b.error) || {};
          throw new Error((e.message || 'Request failed') + (e.remedy ? ' — ' + e.remedy : ''));
        });
      }
      if (!r.body || !r.body.getReader) {
        throw new Error('This browser cannot read a streamed response.');
      }
      var reader = r.body.getReader();
      var decoder = new TextDecoder();
      var buffer = '';

      function pump() {
        return reader.read().then(function (res) {
          if (res.done) {
            return;
          }
          buffer += decoder.decode(res.value, { stream: true });
          // SSE frames are separated by a blank line. Anything after the last
          // separator is a partial frame and stays in the buffer.
          var parts = buffer.split('\n\n');
          buffer = parts.pop();
          parts.forEach(function (frame) {
            var line = frame.split('\n').filter(function (l) {
              return l.indexOf('data: ') === 0;
            })[0];
            if (!line) return;
            try { onEvent(JSON.parse(line.slice(6))); }
            catch (e) { /* a frame we cannot read is skipped, not fatal */ }
          });
          return pump();
        });
      }
      return pump();
    });
  }

  /* A proposed goal is shown as a proposal and nothing more. Starting work is a
   * separate, deliberate act — PRD AGT-04: autonomy is never raised silently,
   * and a conversation must not be able to start execution. */
  function renderProposal(goal) {
    var el = $('proposal');
    el.innerHTML =
      '<div class="approval"><div class="tier">' + esc(goal.risk_tier || 'r1') + ' · proposed work</div>' +
      '<div style="margin-top:6px;font-weight:650;font-size:13px">' + esc(goal.title) + '</div>' +
      '<div style="margin-top:6px;font-size:12.5px;line-height:1.55;color:var(--ink-dim)">' +
      esc(goal.statement) + '</div>' +
      '<div style="margin-top:10px;font-size:11.5px;color:var(--ink-dim)">' +
      'Nothing runs from here. To start it:<br><code style="font-size:11px">forgectl goal new --owner &lt;you&gt; ' +
      '--title "' + esc(goal.title) + '" --statement "…" --start</code></div></div>';
    el.classList.remove('hidden');
  }

  /* ---- voice glue -------------------------------------------------------- */

  function setStatus(s) {
    var el = $('status');
    var map = {
      idle: ['off', 'Ready'],
      listening: ['live', 'Listening'],
      thinking: ['live', 'Thinking'],
      speaking: ['live', 'Speaking']
    };
    var v = map[s] || map.idle;
    el.className = 'pill ' + v[0];
    el.textContent = v[1];
  }

  /* Measured figures, never the PRD's targets. AUD-02 names ≤700ms to first
   * audio and ≤250ms to stop on barge-in; showing what actually happened is the
   * only way either claim can be checked. */
  function updateMeta() {
    var bits = [];
    if (state.firstToken != null) bits.push('spoke at ' + state.firstToken + 'ms');
    if (state.lastLatency != null) bits.push('full reply ' + state.lastLatency + 'ms');
    if (state.lastBargeIn != null) bits.push('barge-in ' + state.lastBargeIn + 'ms');
    $('meta').textContent = bits.join(' · ');
  }

  function initVoice() {
    voice = new ForgeVoice.Voice({
      onPartial: function (text) { showPartial(text); },
      onTranscript: function (text) { clearPartial(); send(text); },
      onBargeIn: function (ms) {
        // The measured figure, not the target. PRD AUD-02 asks for ≤250ms; this
        // shows what actually happened so the claim can be checked rather than
        // taken on faith.
        state.lastBargeIn = Math.round(ms);
        updateMeta();
      },
      onState: function (s) {
        $('mic').setAttribute('aria-pressed', String(s.listening));
        $('mic').classList.toggle('listening', s.listening);
        if (s.speaking) setStatus('speaking');
        else if (s.listening) setStatus('listening');
        else if (!state.busy) setStatus('idle');
      },
      onError: function (msg) {
        $('voice-note').textContent = msg;
        $('voice-note').classList.remove('hidden');
      }
    });

    var mic = $('mic');
    if (!voice.available) {
      mic.disabled = true;
      $('voice-note').textContent = voice.whyUnavailable();
      $('voice-note').classList.remove('hidden');
    }

    /* Push-to-talk is HELD, not toggled. A hold cannot be left on by accident,
     * which is the difference between a microphone the user controls and one
     * that quietly stays open. */
    ['mousedown', 'touchstart'].forEach(function (ev) {
      mic.addEventListener(ev, function (e) { e.preventDefault(); voice.startListening(); });
    });
    ['mouseup', 'mouseleave', 'touchend'].forEach(function (ev) {
      mic.addEventListener(ev, function () {
        if (voice.mode === 'push') voice.stopListening();
      });
    });

    // Space bar as push-to-talk, so the interface is usable without a mouse
    // (PRD AUD-06).
    var spaceHeld = false;
    document.addEventListener('keydown', function (e) {
      if (e.code === 'Space' && !spaceHeld && document.activeElement !== $('say')) {
        e.preventDefault(); spaceHeld = true; voice.startListening();
      }
      // Escape always stops FORGE talking — the deterministic silence PRD
      // AUD-07 asks for, reachable without hunting for a button.
      if (e.key === 'Escape') voice.stopSpeaking();
    });
    document.addEventListener('keyup', function (e) {
      if (e.code === 'Space' && spaceHeld) {
        spaceHeld = false;
        if (voice.mode === 'push') voice.stopListening();
      }
    });

    $('handsfree').addEventListener('change', function (e) {
      voice.setMode(e.target.checked ? 'hands-free' : 'push');
      if (e.target.checked) voice.startListening(); else voice.stopListening();
    });
    $('speakback').addEventListener('change', function (e) {
      state.speak = e.target.checked;
      if (!state.speak) voice.stopSpeaking();
    });
    $('stopspeak').addEventListener('click', function () { voice.stopSpeaking(); });
  }

  /* ---- view controls ----------------------------------------------------- */

  function initControls() {
    Array.prototype.forEach.call(document.querySelectorAll('[data-view]'), function (b) {
      b.addEventListener('click', function () {
        studio.viewFrom(b.getAttribute('data-view'));
        document.querySelectorAll('[data-view]').forEach(function (o) {
          o.setAttribute('aria-pressed', String(o === b));
        });
      });
    });
    $('reset').addEventListener('click', function () { studio.resetView(); });
    $('grid').addEventListener('change', function (e) { studio.setGrid(e.target.checked); });

    $('explode').addEventListener('input', function (e) {
      studio.setExplode(parseFloat(e.target.value));
    });
    $('opacity').addEventListener('input', function (e) {
      studio.setTransparency(parseFloat(e.target.value));
    });
    $('section').addEventListener('change', function (e) {
      studio.setSection(e.target.value, parseFloat($('sectionat').value));
    });
    $('sectionat').addEventListener('input', function (e) {
      studio.setSection($('section').value, parseFloat(e.target.value));
    });

    $('sayform').addEventListener('submit', function (e) {
      e.preventDefault();
      var input = $('say');
      var text = input.value.trim();
      if (text) { input.value = ''; send(text); }
    });
  }

  /* ---- boot -------------------------------------------------------------- */

  function boot() {
    studio = new Forge3D.Studio($('canvas'), {
      onError: function (msg) {
        $('stage-empty').innerHTML = '<div>' + esc(msg) + '</div>';
        $('stage-empty').classList.remove('hidden');
      }
    });
    initVoice();
    initControls();
    setStatus('idle');
    renderParts();

    fetch('/v1/meta/models').then(function (r) { return r.json(); }).then(function (m) {
      if (!m.configured) {
        $('models').textContent = 'no model configured';
        return;
      }
      $('models').textContent = (m.roles.converse || m.roles.executor) +
        (m.verifier_independent ? ' · verifier ' + m.roles.verifier : ' · verifier NOT independent');
    }).catch(function () {});

    fetch('/v1/auth/me').then(function (r) {
      if (!r.ok) throw new Error('signed out');
      return r.json();
    }).then(function (b) {
      $('who').textContent = (b.user && b.user.email) || '';
      addTurn('forge',
        'Ready. Describe what you are building and I will propose a shape you can turn around. ' +
        'Hold the microphone or the space bar to talk; press Escape to stop me mid-sentence.');
    }).catch(function () {
      $('who').innerHTML = '<a href="/console">Sign in</a>';
      addTurn('forge', 'You are not signed in. Sign in from the console, then come back — ' +
        'I cannot hold a conversation without knowing whose workspace this is.');
      $('say').disabled = true;
      $('mic').disabled = true;
    });
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  else boot();
})();
