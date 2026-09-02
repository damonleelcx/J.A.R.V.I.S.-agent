/* FORGE workbench — the conversational, 3D-first surface.
 *
 * Wires four things together: the voice layer, the orb that gives voice a
 * visible state, the 3D studio, and the conversation endpoint. Everything
 * structural lives in those; this file is the choreography between them
 * (PRD §5.3).
 *
 * The choreography rules, which are product decisions rather than plumbing:
 *   - Speech is short and the screen carries detail. Reading a parts table aloud
 *     is worse than useless.
 *   - Barge-in stops speech immediately and locally, and the measured latency is
 *     shown rather than the target claimed.
 *   - Geometry always arrives with what it does NOT establish, and that banner
 *     is not dismissible.
 *   - The voice surface owns the middle of the screen until there is something
 *     to look at, then it moves to the corner. Nothing is removed by the move.
 *   - A conversation proposes work. A PERSON starts it, in two deliberate steps,
 *     and sees the plan in between.
 */
(function () {
  'use strict';

  var $ = function (id) { return document.getElementById(id); };

  var studio = null;
  var voice = null;
  var orb = null;
  var state = {
    history: [],
    prototype: null,
    selectedPart: null,
    speak: true,
    lastLatency: null,
    firstToken: null,
    lastBargeIn: null,
    model: null,
    busy: false,
    /* The proposal's lifecycle, which is also the AGT-08 state machine this
     * card is allowed to display: nothing → proposed → planned → active.
     * Held in one field so the card cannot render two states at once. */
    recalled: [],         // standards figures FORGE quoted from memory this turn
    proposal: null,       // the ProposedGoal from the conversation
    goal: null,           // the created goal, once it exists
    planTasks: null,      // its tasks, once planning has run
    goalPhase: 'none'     // none | proposed | planning | planned | starting | active | failed
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
    setCaption(text, true);
  }
  function clearPartial() {
    if (partialEl) { partialEl.remove(); partialEl = null; }
  }

  /* ---- the voice surface ------------------------------------------------- */

  /* The caption under the orb is the last thing SAID, not a status line. Keeping
   * those two separate is what lets the state word below it be read as state
   * rather than as more transcript. */
  function setCaption(text, partial) {
    var el = $('caption');
    el.textContent = text || '';
    el.classList.toggle('partial', !!partial);
  }

  var STATES = {
    idle:      'Ready',
    listening: 'Listening…',
    thinking:  'Thinking…',
    speaking:  'Speaking…'
  };

  function setStatus(s) {
    var el = $('statusword');
    el.textContent = STATES[s] || STATES.idle;
    el.className = 'voice-state ' + s;
    if (orb) orb.setState(s === 'idle' ? 'idle' : s);
    setPresence();
  }

  /* ---- presence ---------------------------------------------------------- */

  /* The header presence carries FORGE's own state, in FORGE's own vocabulary —
   * the six the sigil defines (persona/avatar.go), not the four the voice
   * surface uses.
   *
   * Only three of the six can honestly occur here, and the mapping is exact
   * rather than decorative:
   *
   *   thinking  a model call is in flight. Literally what StateThinking means.
   *   blocked   "waiting for you": a proposal is on screen and nothing moves
   *             until the person decides. This is the state the sigil is most
   *             deliberately distinct for, because an agent waiting unnoticed
   *             looks exactly like one that died.
   *   idle      neither of those.
   *
   * `working` is NOT used. It means a tool is running outside this process, and
   * nothing on this page does that — a worker does. `done` and `failed` belong
   * to a goal's outcome, which this page does not observe. Borrowing either
   * would be the interface asserting something the system did not do.
   *
   * Both images come from the server, so the state-to-expression rule stays in
   * persona.ExpressionFor rather than being re-derived here. */
  var presenceState = null;

  function forgeState() {
    if (state.busy || state.goalPhase === 'planning' || state.goalPhase === 'starting') {
      return 'thinking';
    }
    if (state.goalPhase === 'proposed' || state.goalPhase === 'planned') return 'blocked';
    return 'idle';
  }

  function setPresence() {
    var want = forgeState();
    if (want === presenceState) return;
    presenceState = want;

    /* Both assets are re-requested from the server rather than swapped between
     * variants held here, so persona.ExpressionFor stays the only place that
     * decides which face belongs to which state. They are small, cached for
     * five minutes, and there are three of them. */
    var portrait = $('orb-portrait');
    if (portrait) portrait.src = '/v1/meta/portrait?state=' + encodeURIComponent(want);

    ['orb-badge', 'top-sigil'].forEach(function (id) {
      var badge = $(id);
      if (!badge) return;
      badge.innerHTML = '<img src="/v1/meta/sigil?state=' + encodeURIComponent(want) +
        '&size=64" alt="FORGE: ' + want + '">';
    });
  }

  /* The dock. Triggered by geometry existing, which is the moment the middle of
   * the screen acquires a subject that is not the conversation. One condition,
   * one place — a second trigger elsewhere would eventually disagree with this
   * one about which state the interface is in. */
  function setPlace(building) {
    $('voice').setAttribute('data-place', building ? 'dock' : 'hero');
    $('stage').classList.toggle('building', building);
  }

  /* ---- the stage -------------------------------------------------------- */

  function describeOnScreen() {
    if (!state.prototype) return '';
    var names = state.prototype.parts.map(function (p) { return p.name || p.id; });
    return state.prototype.name + ' — ' + state.prototype.parts.length + ' part(s): ' + names.join(', ') +
           ' (units: ' + (state.prototype.units || 'NOT STATED — every dimension here is unitless') + ')';
  }

  function loadPrototype(proto) {
    state.prototype = proto;
    state.selectedPart = null;
    studio.load(proto);
    setPlace(true);
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
      return '<div class="part" data-id="' + esc(p.id) + '" aria-current="' + (state.selectedPart === p.id) + '">' +
        '<span class="sw" style="background:' + esc(p.color || '#b8bcc4') + '"></span>' +
        '<span class="nm">' + esc(p.name || p.id) +
        '<div class="dim">' + esc(p.shape) + dimensionsOf(p) + '</div>' +
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

  /* Dimensions, with every number carrying its unit (PRD WRK-05).
   *
   * The previous version joined bare numbers and appended the assembly's unit
   * string once at the end — correct while the unit was declared, and silently
   * wrong when it was not: "50×5×50 " reads as millimetres to anyone who has
   * seen millimetres before. Now each number is formatted with its unit, and a
   * prototype with no usable unit says so on every value rather than nowhere.
   *
   * Rendered per shape rather than by listing every field: a cylinder shown as
   * "0×20×0 r12.5" is noise, and noise in a dimension is worse than none,
   * because it reads as a measurement. */
  function qty(v) {
    if (v == null) return '?';
    var u = state.prototype && state.prototype.units;
    return u ? (v + ' ' + u) : (v + ' (unit not stated)');
  }

  function dimensionsOf(p) {
    var s = p.size || {};
    var dims = [];
    if (p.shape === 'cylinder' || p.shape === 'cone' || p.shape === 'tube') {
      if (s.radius != null) dims.push('⌀' + qty(s.radius * 2));
      if (s.radius_top != null && s.radius_top !== s.radius) dims.push('top ⌀' + qty(s.radius_top * 2));
      if (s.height != null) dims.push('h ' + qty(s.height));
    } else if (p.shape === 'sphere') {
      if (s.radius != null) dims.push('⌀' + qty(s.radius * 2));
    } else if (s.width != null || s.height != null || s.depth != null) {
      dims.push([s.width, s.height, s.depth].map(qty).join(' × '));
    }
    return dims.length ? ' · ' + esc(dims.join(' · ')) : '';
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
    /* Sits with everything else this picture does not establish. A dimension
     * recalled from a standard is not a dimension that was looked up, and the
     * banner is where that distinction belongs. */
    if (state.recalled && state.recalled.length) {
      /* The banner is a SUMMARY — which standards this reply leaned on, and
       * every figure it quoted for each. The sentences live in the transcript
       * block, where there is room for them. Nine bullets repeating "M3" is how
       * a warning gets scrolled past. */
      var byStandard = {};
      state.recalled.forEach(function (c) {
        standardsOf(c).forEach(function (name) {
          var k = name.toUpperCase().replace(/\s+/g, '');
          if (!byStandard[k]) byStandard[k] = { name: name, figures: [] };
          figuresOf(c).forEach(function (f) {
            if (byStandard[k].figures.indexOf(f) < 0) byStandard[k].figures.push(f);
          });
        });
      });
      html += '<div style="margin-top:7px"><b>Quoted from memory, not checked:</b><ul>' +
        Object.keys(byStandard).map(function (k) {
          var e = byStandard[k];
          return '<li>' + esc(e.name) +
            (e.figures.length ? ' — ' + esc(e.figures.join(', ')) : ' — named, no figure quoted') + '</li>';
        }).join('') + '</ul></div>';
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
    setCaption(text, false);
    state.history.push({ role: 'user', content: text });

    var bubble = addTurn('forge', '…');
    state.recalled = [];
    var t0 = performance.now();
    setStatus('thinking');

    streamTurn(text, function (ev) {
      switch (ev.kind) {
        case 'speech':
          bubble.querySelector('.body').textContent = ev.text;
          state.history.push({ role: 'forge', content: ev.text });
          state.firstToken = ev.first_token_ms || Math.round(performance.now() - t0);
          updateMeta();
          // Speaking starts HERE, not at 'done'. This is the whole reason the
          // endpoint streams.
          if (state.speak && ev.text) {
            voice.speak(ev.text, function () { if (!state.busy) setStatus('idle'); });
          }
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
          proposeGoal(ev.goal);
          break;

        case 'claims':
          // The epistemic ledger (PRD RSN-05). Every statement in the turn with
          // how FORGE came to hold it — derived server-side from the reply, not
          // asked of the model, because a component cannot be its own guard.
          bubble.appendChild(claimsBlock(ev.claims || []));
          break;

        case 'recalled':
          // Attached to the turn that made the claim AND folded into the
          // provenance banner if geometry is on screen. A figure quoted in
          // prose is exactly as unverifiable as one quoted in an assumption,
          // so it must not depend on there being a model to hang it on.
          state.recalled = ev.recalled || [];
          bubble.appendChild(recalledBlock(state.recalled));
          renderProvenance();
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
      /* Resolved from what the voice layer is ACTUALLY doing, not from whether
       * speak() was called.
       *
       * The earlier version only reset the state word when nothing had been
       * spoken, on the assumption that speech would always reach its own end
       * event and reset it there. speechSynthesis does not guarantee that: an
       * utterance that never starts — no audio device, a policy block, one of
       * Chrome's long-standing silent drops — fires neither onstart nor onend,
       * and the workbench sat on "Thinking…" forever with the turn finished and
       * the model paid for. Observed in a browser with no audio output. */
      setStatus(voice.speaking ? 'speaking' : 'idle');
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

  /* How each statement is known.
   *
   * Ordered weakest-last so the eye lands on what was actually checked first,
   * and the categories nothing in this deployment can honestly produce
   * (observed, calculated from measurements, simulated) simply never appear —
   * their absence is the honest signal. */
  var HOW = {
    observed:   ['ok',   'seen directly'],
    calculated: ['ok',   'derived from known values'],
    retrieved:  ['warn', 'from a source outside FORGE'],
    simulated:  ['warn', 'produced by a model'],
    inferred:   ['dim',  'concluded from context'],
    assumed:    ['dim',  'chosen because nobody said'],
    proposed:   ['dim',  'offered for a decision']
  };

  function claimsBlock(claims) {
    var el = document.createElement('div');
    el.className = 'claims';
    var order = ['observed', 'calculated', 'retrieved', 'simulated', 'inferred', 'assumed', 'proposed'];
    claims = claims.slice().sort(function (a, b) {
      return order.indexOf(howOf(a)) - order.indexOf(howOf(b));
    });
    el.innerHTML =
      '<div class="cl-h">How this is known</div>' +
      claims.map(function (c) {
        var how = howOf(c);
        var meta = HOW[how] || ['dim', how];
        return '<div class="cl-i cl-' + esc(meta[0]) + '">' +
          '<span class="cl-t">' + esc(how) + '</span>' +
          '<span class="cl-s">' + esc(c.statement || c.Statement || '') + '</span>' +
          (sourceOf(c) ? '<span class="cl-src">' + esc(sourceOf(c)) + '</span>' : '') +
          '</div>';
      }).join('');
    return el;
  }

  function howOf(c) { return String(c.how || c.How || '').toLowerCase(); }
  function sourceOf(c) { return c.source || c.Source || ''; }

  /* Recalled figures.
   *
   * FORGE quoted a published standard. There is no reference source in this
   * deployment, so the figure came out of the model's memory and nothing here
   * checked it — which is worth saying loudly, because a wrong number attached
   * to a real standard is specific enough to be acted on. Observed: a NEMA 17
   * bolt pattern given as "±20.5 mm on both axes", which is a 41mm pattern
   * against a real 31mm one.
   *
   * The wording names what is unknown rather than implying the figure is wrong:
   * most of the time it is right, and crying wolf on every citation would train
   * people to skip the one that matters. */
  function recalledBlock(claims) {
    var el = document.createElement('div');
    el.className = 'recalled';
    el.innerHTML =
      '<div class="rc-h">Quoted from memory · not checked</div>' +
      claims.map(function (c) {
        var figs = figuresOf(c);
        return '<div class="rc-i"><b>' + esc(standardsOf(c).join(' · ')) + '</b>' +
          (figs.length ? ' — ' + esc(figs.join(', ')) : ' — conformance claimed, no figure given') +
          // The sentence, verbatim. The figures are listed against the SENTENCE
          // and never paired with an individual standard, so the reader does the
          // pairing by reading it — see the note on StandardsClaim.
          '<div class="rc-q">“' + esc(c.Text || c.text) + '”</div>' +
          '<div class="rc-w">in the ' + esc(c.Where || c.where) + '</div></div>';
      }).join('') +
      '<div class="rc-f">There is no standards reference in this deployment. ' +
      'Check these against the published standard before anything is cut.</div>';
    return el;
  }

  function figuresOf(c) { return c.Figures || c.figures || []; }
  function standardsOf(c) { return c.Standards || c.standards || []; }

  /* ---- proposed work ----------------------------------------------------- */

  /* # Why starting work takes two presses
   *
   * PRD AGT-02 requires a scoped plan and preview before material action, and
   * AGT-04 forbids autonomy being raised without the person seeing it. So:
   *
   *   "Start this"  creates a DRAFT goal and plans it. Nothing is claimable,
   *                 no worker can touch it, and the plan comes back and is shown.
   *   "Start it"    activates. This is the material act, and by then the person
   *                 has read the list of tasks they are authorising.
   *
   * The conversation itself never reaches either endpoint. It emits a proposal;
   * a human presses a button. Two things on one screen are still two acts.
   */

  function proposeGoal(goal) {
    state.proposal = goal;
    state.goal = null;
    state.planTasks = null;
    state.goalPhase = 'proposed';
    renderProposal();
  }

  function api(path, body) {
    return fetch(path, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body || {})
    }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (b) {
        if (!r.ok) {
          var e = (b && b.error) || {};
          throw new Error((e.message || ('Request failed (' + r.status + ')')) +
                          (e.remedy ? ' — ' + e.remedy : ''));
        }
        return b;
      });
    });
  }

  function startThis() {
    if (state.goalPhase !== 'proposed' && state.goalPhase !== 'failed') return;
    state.goalPhase = 'planning';
    state.error = null;
    renderProposal();

    // Planning is a model call and takes tens of seconds. The elapsed counter
    // reports only what is actually known — how long this has been running —
    // for the same reason the CLI's ticker does: a progress bar here would be
    // claiming to see inside the model.
    var t0 = Date.now();
    var tick = setInterval(function () {
      var el = document.getElementById('plan-elapsed');
      if (el) el.textContent = Math.round((Date.now() - t0) / 1000) + 's';
    }, 1000);

    api('/v1/goals', {
      title: state.proposal.title,
      statement: state.proposal.statement,
      risk_tier: state.proposal.risk_tier || 'r1'
    }).then(function (b) {
      state.goal = b.goal;
      state.planTasks = b.tasks || [];
      state.clarification = b.clarification_needed || null;
      state.rationale = b.rationale || '';
      state.goalPhase = state.clarification ? 'proposed' : 'planned';
      if (state.clarification) {
        addTurn('forge', 'Before I can plan that, I need an answer: ' + state.clarification);
        setCaption(state.clarification, false);
      }
    }).catch(function (err) {
      state.goalPhase = 'failed';
      state.error = err.message;
    }).then(function () {
      clearInterval(tick);
      renderProposal();
    });
  }

  function startIt() {
    if (state.goalPhase !== 'planned' || !state.goal) return;
    // Cleared before the attempt, so a previous failure is not still on screen
    // beside a success.
    state.error = null;
    state.goalPhase = 'starting';
    renderProposal();

    api('/v1/goals/' + encodeURIComponent(state.goal.id) + '/start', {})
      .then(function (b) {
        state.goal = b.goal;
        state.startMessage = b.message;
        state.goalPhase = 'active';
        addTurn('forge', b.message);
      })
      .catch(function (err) {
        state.goalPhase = 'planned';
        state.error = err.message;
      })
      .then(renderProposal);
  }

  function renderProposal() {
    var el = $('proposal');
    var head = $('proposal-head');
    if (state.goalPhase === 'none' || !state.proposal) {
      el.classList.add('hidden');
      head.style.display = 'none';
      return;
    }
    head.style.display = '';
    el.classList.remove('hidden');

    var g = state.proposal;
    var phase = state.goalPhase;
    var cls = 'proposal' + (phase === 'active' ? ' live' : phase === 'planned' ? ' armed' : '');

    var label = {
      proposed: (g.risk_tier || 'r1') + ' · proposed',
      planning: 'planning…',
      planned:  (g.risk_tier || 'r1') + ' · planned, not running',
      starting: 'starting…',
      active:   'active',
      failed:   (g.risk_tier || 'r1') + ' · proposed'
    }[phase];

    var html = '<div class="' + cls + '">' +
      '<div class="tier">' + esc(label) + '</div>' +
      '<div class="ttl">' + esc(g.title) + '</div>' +
      '<div class="say">' + esc(g.statement) + '</div>';

    if (state.goal) {
      html += '<div class="foot"><code>' + esc(state.goal.id) + '</code></div>';
    }

    if (phase === 'planning') {
      html += '<div class="foot">Planning with the planner model. ' +
              'Nothing is running. <span id="plan-elapsed">0s</span> elapsed.</div>';
    }

    if ((phase === 'planned' || phase === 'starting' || phase === 'active') && state.planTasks) {
      html += '<ul class="steps">' + state.planTasks.map(function (t) {
        return '<li><span class="rt">' + esc(t.risk_tier) + '</span>' +
               '<span>' + esc(t.title) + '</span>' +
               (t.requires_approval ? '<span class="gate">gate</span>' : '') + '</li>';
      }).join('') + '</ul>';
      if (state.rationale) {
        html += '<div class="foot">' + esc(state.rationale) + '</div>';
      }
    }

    html += '<div class="acts">';
    if (phase === 'proposed' || phase === 'failed') {
      html += '<button class="btn-sm go" id="do-plan">Start this</button>';
    } else if (phase === 'planning') {
      html += '<button class="btn-sm" disabled>Planning…</button>';
    } else if (phase === 'planned') {
      html += '<button class="btn-sm go" id="do-start">Start it — run ' +
              state.planTasks.length + ' task' + (state.planTasks.length === 1 ? '' : 's') + '</button>';
    } else if (phase === 'starting') {
      html += '<button class="btn-sm" disabled>Starting…</button>';
    }
    if (state.goal) {
      html += '<a class="btn-sm" href="/console#goal=' + encodeURIComponent(state.goal.id) +
              '" target="_blank" rel="noopener">Open in operations</a>';
    }
    html += '</div>';

    /* What each state does NOT mean, said out loud. PRD AGT-08 makes proposed,
     * planned and running distinct, and the difference between them is invisible
     * unless the interface states it. */
    if (phase === 'proposed' || phase === 'failed') {
      html += '<div class="foot">Nothing has been created. Starting this writes a draft ' +
              'goal and plans it — it does not run it.</div>';
    } else if (phase === 'planned') {
      html += '<div class="foot">The goal is a <b>draft</b>. These tasks exist and no worker ' +
              'can claim them until you start it.</div>';
    } else if (phase === 'active') {
      html += '<div class="foot">' + esc(state.startMessage || 'Started.') + '</div>';
    }

    if (state.error) {
      html += '<div class="note bad">' + esc(state.error) + '</div>';
    }
    html += '</div>';

    el.innerHTML = html;
    setPresence();
    var plan = document.getElementById('do-plan');
    if (plan) plan.addEventListener('click', startThis);
    var start = document.getElementById('do-start');
    if (start) start.addEventListener('click', startIt);
  }

  /* ---- voice glue -------------------------------------------------------- */

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
    /* The text path is wired FIRST, before anything that can fail.
     *
     * This form's input carries no `name`, so if its submit handler is ever
     * missing the browser performs a native GET submit: the page navigates and
     * the whole conversation is destroyed by someone pressing send. That exact
     * failure has happened in this codebase before — see
     * docs/bugfix/2026-09-02-csp-blocked-inline-page-script.md, where a
     * password-reset form ate its own token the same way.
     *
     * It lives with the microphone rather than with the viewport sliders,
     * because they are one feature: the audio path and the non-audio path PRD
     * AUD-06 requires for every critical interaction. A missing slider in the
     * 3D viewport must not be able to take typing down with it, and neither
     * must a browser with no speech recognition. */
    $('sayform').addEventListener('submit', function (e) {
      e.preventDefault();
      var input = $('say');
      var text = input.value.trim();
      if (text) { input.value = ''; send(text); }
    });

    voice = new ForgeVoice.Voice({
      onPartial: function (text) { showPartial(text); },
      onTranscript: function (text) { clearPartial(); send(text); },
      // A real measurement, and only while listening. See orb.js for why the
      // orb refuses to draw this shape at any other time.
      onLevel: function (v) { if (orb) orb.setLevel(v); },
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
        else if (state.busy) setStatus('thinking');
        else setStatus('idle');
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
  }

  /* ---- the soul ---------------------------------------------------------- */

  /* Rendered by the server into the page; this only opens and closes it.
   * Escape closes it — and does NOT fall through to stopping speech, because a
   * person pressing Escape with a dialog open means "close this". */
  function initSoul() {
    var panel = $('soul');
    var button = $('whois');
    if (!panel || !button) return;

    function open(yes) {
      panel.classList.toggle('hidden', !yes);
      button.setAttribute('aria-expanded', String(yes));
      if (yes) $('soul-close').focus();
    }
    button.addEventListener('click', function () { open(panel.classList.contains('hidden')); });
    $('soul-close').addEventListener('click', function () { open(false); button.focus(); });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && !panel.classList.contains('hidden')) {
        e.stopPropagation();
        open(false);
        button.focus();
      }
    }, true);
  }

  /* ---- boot -------------------------------------------------------------- */

  /* Each stage of boot is isolated. The conversation must survive a failure in
   * the viewport, and the viewport must survive a failure in the voice layer —
   * one throw taking down the rest is how an interface loses the one control
   * the user actually needed. The failure is reported rather than swallowed. */
  function safely(name, fn) {
    try {
      fn();
    } catch (e) {
      if (window.console) window.console.warn('forge.workbench.' + name + '_failed', e);
    }
  }

  function boot() {
    safely('orb', function () { orb = new ForgeOrb.Orb($('orb')); });
    studio = new Forge3D.Studio($('canvas'), {
      onError: function (msg) {
        // A renderer that cannot start is stated as a failure, in its own words,
        // rather than left to read as "nothing modelled yet".
        $('stage-empty').textContent = msg;
        $('stage-empty').classList.remove('hidden');
      }
    });
    safely('voice', initVoice);
    safely('controls', initControls);
    safely('soul', initSoul);
    setStatus('idle');
    setPlace(false);
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
      setCaption('Sign in from the console to start.', false);
      addTurn('forge', 'You are not signed in. Sign in from the console, then come back — ' +
        'I cannot hold a conversation without knowing whose workspace this is.');
      $('say').disabled = true;
      $('mic').disabled = true;
    });
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', boot);
  else boot();
})();
