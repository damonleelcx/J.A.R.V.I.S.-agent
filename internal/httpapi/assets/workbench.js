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
    projectID: null,      // where this conversation's variants are kept
    variants: [],         // what has been proposed so far, newest first
    picked: [],           // version ids chosen for the side-by-side view
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

  /* ---- variants (PRD VIS-04) --------------------------------------------
   *
   * Every shape FORGE proposes is kept as a version of its assembly, so the
   * third bracket does not erase the first. What this half of the interface
   * owes the reader is the six things VIS-04 names — geometry version, inputs,
   * units, assumptions, generator, verification status — beside each render
   * rather than one click away, because the point of putting two designs next
   * to each other is deciding between them and none of those facts can be
   * inferred from the picture.
   */

  /* A rail row, from either of the two producers.
   *
   * Variants reach this list two ways: live, from the `variant` event as a turn
   * finishes, and on reload, from GET /v1/geometry. The two payloads carry the
   * same facts in different shapes — the event sends counts because it already
   * knows them, the endpoint sends the lists because a reader of the API wants
   * them. Normalised once, here, so the row cannot render differently depending
   * on how it arrived. */
  function railRow(v) {
    var parts = (typeof v.parts === 'number')
      ? v.parts
      : ((v.document && v.document.parts) ? v.document.parts.length : 0);
    var assumptions = (typeof v.assumptions === 'number')
      ? v.assumptions
      : ((v.assumptions && v.assumptions.length) || 0);
    return {
      version_id: v.version_id, path: v.path, version: v.version,
      name: v.name, units: v.units, units_note: v.units_note,
      generator: v.generator, parts: parts, assumptions: assumptions,
      verification: v.verification || 'unverified',
      disposition: v.disposition || 'pending'
    };
  }

  /* The project a conversation's variants accumulate in, remembered across a
   * reload.
   *
   * # Why this is stored at all
   *
   * Without it the rail empties every time the tab reloads, while the variants
   * themselves sit safely in the database — visible to forgectl and to the API
   * and to nothing the person is looking at. Comparing what you tried is the
   * whole point of VIS-04, and "what you tried" does not end at a page load.
   *
   * It holds an IDENTIFIER, not data: the variants are re-read from the server
   * on boot and the server checks membership, so a stale or copied value shows
   * an empty rail rather than somebody else's work. Every access is guarded —
   * private browsing, cleared site data and blocked storage all throw, and none
   * of them is a reason to fail the workbench. */
  var PROJECT_KEY = 'forge.workbench.project';

  function rememberProject(id) {
    state.projectID = id || state.projectID;
    try { window.localStorage.setItem(PROJECT_KEY, state.projectID); } catch (e) { /* not fatal */ }
  }

  function restoreVariants() {
    var id = null;
    try { id = window.localStorage.getItem(PROJECT_KEY); } catch (e) { return; }
    if (!id) return;
    state.projectID = id;
    fetch('/v1/geometry?project_id=' + encodeURIComponent(id))
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (b) {
        if (!b || !b.variants || !b.variants.length) return;
        state.variants = b.variants.map(railRow);
        renderVariants();
      })
      .catch(function () {
        /* Silent. The rail being empty is a correct rendering of "nothing was
         * restored", and an error banner about a convenience would be louder
         * than the conversation it sits beside. */
      });
  }

  function noteVariant(v, bubble) {
    if (!v) return;
    if (v.not_kept) {
      /* Said in the transcript, quietly. It is not an error in the
       * conversation — the shape is on screen and still useful — but it changes
       * what the person can do next, so it cannot be silent. */
      var n = document.createElement('div');
      n.className = 'detail';
      n.style.color = 'var(--warn)';
      n.textContent = v.not_kept;
      if (bubble) bubble.appendChild(n);
      return;
    }
    rememberProject(v.project_id);
    /* Taken from the event, not rebuilt from local state. The row on screen has
     * to say what the STORED row says: this event arrives before `done`, so the
     * page does not yet know which model answered, and composing the generator
     * here produced "FORGE" for every variant — one of VIS-04's six facts,
     * quietly wrong. */
    state.variants.unshift(railRow(v));
    renderVariants();
  }

  function renderVariants() {
    var el = $('variants');
    var head = $('variants-head');
    if (!state.variants.length) {
      head.style.display = 'none';
      el.innerHTML = '';
      return;
    }
    head.style.display = '';

    var canCompare = state.picked.length >= 2;
    var html = '<div class="variant-bar">' +
      '<button type="button" class="node" id="cmp-open"' + (canCompare ? '' : ' disabled') + '>' +
      'Compare ' + (canCompare ? state.picked.length : '') + '</button>' +
      '<span class="hint">' +
      (canCompare ? 'Opens them side by side.' : 'Tick two or more to compare.') +
      '</span></div>';

    html += state.variants.map(function (v) {
      var picked = state.picked.indexOf(v.version_id) >= 0;
      /* Units are stated or called out. A blank cell in a column of millimetres
       * reads as millimetres, which is the one reading that must never be
       * available by accident (PRD WRK-05). */
      var units = v.units
        ? esc(v.units)
        : '<span class="nounit">' + esc(v.units_note || 'no unit stated') + '</span>';
      return '<div class="variant' + (picked ? ' picked' : '') + '" data-id="' + esc(v.version_id) + '">' +
        '<label class="sw"><input type="checkbox" data-pick="' + esc(v.version_id) + '"' +
        (picked ? ' checked' : '') + '><span></span></label>' +
        '<span class="nm"><b>' + esc(v.name) + '</b> v' + v.version +
        '<div class="meta">' + v.parts + ' part(s) · ' + units +
        ' · ' + v.assumptions + ' assumption(s)<br>proposed by ' + esc(v.generator) +
        ' · ' + esc(v.verification) + ', ' +
        (v.disposition === 'pending' ? 'nobody has ruled on it' : esc(v.disposition)) + '</div>' +
        '<div class="acts">' +
        /* Adopting is offered only where it is the thing to do. A superseded
         * variant cannot be accepted, and bringing it forward is how you choose
         * it; on the current version the disposition is already available and
         * adopting would append an identical copy. */
        (v.disposition === 'superseded'
          ? '<button type="button" data-adopt="' + esc(v.version_id) + '">Adopt this one</button>'
          : '') +
        '<button type="button" data-export="' + esc(v.version_id) + '" data-format="obj">Export OBJ</button>' +
        '<button type="button" data-export="' + esc(v.version_id) + '" data-format="stl">Export STL</button>' +
        '</div>' +
        '<div class="exportlabel hidden" data-label="' + esc(v.version_id) + '"></div>' +
        '</span></div>';
    }).join('');

    el.innerHTML = html;

    Array.prototype.forEach.call(el.querySelectorAll('[data-pick]'), function (box) {
      box.addEventListener('change', function () {
        var id = box.getAttribute('data-pick');
        var at = state.picked.indexOf(id);
        if (box.checked && at < 0) state.picked.push(id);
        if (!box.checked && at >= 0) state.picked.splice(at, 1);
        renderVariants();
      });
    });
    Array.prototype.forEach.call(el.querySelectorAll('[data-adopt]'), function (b) {
      b.addEventListener('click', function () { adoptVariant(b.getAttribute('data-adopt'), b); });
    });
    Array.prototype.forEach.call(el.querySelectorAll('[data-export]'), function (b) {
      b.addEventListener('click', function () {
        showExportLabel(b.getAttribute('data-export'), b.getAttribute('data-format'));
      });
    });
    var open = $('cmp-open');
    if (open) open.addEventListener('click', openCompare);
  }

  /* Adopting brings an earlier variant forward as the current version.
   *
   * Appending a version supersedes the previous one, and a superseded version
   * can no longer be accepted or rejected — correct for a file, wrong for
   * alternatives you are choosing between. Without this the comparison showed
   * somebody the choice and nothing let them make it.
   *
   * It proposes; it does not sign anything off. The row that appears is
   * unverified and undecided like any other. */
  function adoptVariant(versionID, button) {
    button.disabled = true;
    button.textContent = 'Adopting…';
    fetch('/v1/geometry/' + encodeURIComponent(versionID) + '/adopt', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ reason: 'chosen after comparing variants at the workbench' })
    }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (b) {
        if (!r.ok) {
          var e = (b && b.error) || {};
          throw new Error((e.message || ('Could not adopt (' + r.status + ')')) +
                          (e.remedy ? ' — ' + e.remedy : ''));
        }
        return b;
      });
    }).then(function (b) {
      /* Re-read rather than patch the list in place. Adopting supersedes the
       * previously-current variant, so more than one row changed and the server
       * is the only thing that knows which. */
      restoreVariants();
      addTurn('forge', 'Brought ' + esc(b.variant.name) + ' forward as v' + b.variant.version +
        '. Nothing is accepted yet — it is a proposal like any other.');
    }).catch(function (err) {
      button.disabled = false;
      button.textContent = 'Adopt this one';
      addTurn('forge', err.message);
    });
  }

  /* The conversion label is fetched and SHOWN before the download link appears.
   *
   * Not after: a label a person reads on their way out of the page is a label
   * they have already acted without. This is the only place the tessellation
   * error, the inferred dimensions and the lossy list reach somebody who is
   * about to hand this file to a machine. */
  function showExportLabel(versionID, format) {
    var box = document.querySelector('[data-label="' + versionID + '"]');
    if (!box) return;
    box.classList.remove('hidden');
    box.textContent = 'Working out what this conversion would lose…';

    fetch('/v1/geometry/' + encodeURIComponent(versionID) +
          '/export/label?format=' + encodeURIComponent(format))
      .then(function (r) {
        return r.json().catch(function () { return {}; }).then(function (b) {
          if (!r.ok) {
            var e = (b && b.error) || {};
            throw new Error((e.message || ('Export refused (' + r.status + ')')) +
                            (e.remedy ? ' — ' + e.remedy : ''));
          }
          return b;
        });
      })
      .then(function (b) {
        var l = b.label || {};
        var html = '<b>' + esc(l.headline || '') + '</b>';
        if (l.tessellation && l.tessellation.length) {
          html += '<div style="margin-top:6px"><b>Tessellation</b><ul>' +
            l.tessellation.map(function (d) {
              return '<li>' + esc(d.label) + ' (' + esc(d.shape) + '): ' + d.segments +
                ' faces; the exported surface lies up to ' + esc(d.max_deviation) +
                ' inside the one described.</li>';
            }).join('') + '</ul></div>';
        }
        html += section('Inferred, because the description did not say', l.inference);
        html += section('Lost in this conversion', l.lossy);
        html += section('Assumed, not specified', l.assumptions);
        html += section('This file does not establish', l.not_verified);
        html += '<div>' + b.triangles + ' triangles · ' + esc(l.units) + '</div>' +
          '<a class="go" href="/v1/geometry/' + encodeURIComponent(versionID) +
          '/export?format=' + encodeURIComponent(format) + '">Download the ' +
          esc(String(format).toUpperCase()) + ' →</a>';
        box.innerHTML = html;
      })
      .catch(function (err) {
        box.innerHTML = '<b>Not exported.</b><div>' + esc(err.message) + '</div>';
      });
  }

  function section(title, items) {
    if (!items || !items.length) return '';
    return '<div style="margin-top:6px"><b>' + esc(title) + '</b><ul>' +
      items.map(function (i) { return '<li>' + esc(i) + '</li>'; }).join('') + '</ul></div>';
  }

  /* ---- side by side ------------------------------------------------------ */

  /* Canvases and their Studios are POOLED and reused across openings.
   *
   * Each Studio holds a WebGL context, and browsers cap how many a page may
   * have — commonly 8 to 16, after which the oldest are dropped. Building fresh
   * ones every time the panel opens works for the first few openings and then
   * starts losing contexts, which shows up as viewports that render nothing,
   * several interactions after the cause.
   *
   * So the CANVAS ELEMENTS are pooled too, not just the Studios: a Studio is
   * bound to the canvas it was constructed with, and replacing the element
   * (which re-rendering the panel's markup does) would orphan the context
   * whether or not the Studio object was kept. Each opening moves the same
   * canvas nodes into the new columns. */
  var comparePool = [];

  function poolStudio(i) {
    if (!comparePool[i]) {
      var canvas = document.createElement('canvas');
      comparePool[i] = { canvas: canvas, studio: new Forge3D.Studio(canvas, { onError: function () {} }) };
    }
    return comparePool[i];
  }

  function openCompare() {
    if (state.picked.length < 2) return;
    var panel = $('compare');
    var body = $('compare-body');
    panel.classList.remove('hidden');
    body.textContent = 'Reading the variants…';

    fetch('/v1/geometry/compare?ids=' + state.picked.map(encodeURIComponent).join(','))
      .then(function (r) {
        return r.json().catch(function () { return {}; }).then(function (b) {
          if (!r.ok) {
            var e = (b && b.error) || {};
            throw new Error((e.message || ('Could not compare (' + r.status + ')')) +
                            (e.remedy ? ' — ' + e.remedy : ''));
          }
          return b;
        });
      })
      .then(renderCompare)
      .catch(function (err) {
        body.innerHTML = '<div class="cmp-note" style="color:var(--bad)">' + esc(err.message) + '</div>';
      });
  }

  function closeCompare() { $('compare').classList.add('hidden'); }

  function renderCompare(cmp) {
    var body = $('compare-body');
    var variants = cmp.variants || [];
    $('compare-title').textContent = 'Side by side — ' + variants.length + ' variants';

    var html = '<div class="cmp-note">Every column carries what its render rests on. ' +
      'Dimensions are compared by converting to millimetres, so 60 mm and 6 cm are the same length — ' +
      'and a variant with no convertible unit is not compared at all, which is listed separately below ' +
      'rather than reported as agreement.</div><div class="cmp-cols">';

    html += variants.map(function (v, i) {
      var units = v.units ? esc(v.units) : '<span style="color:var(--warn)">not stated</span>';
      return '<div class="cmp-col">' +
        '<div class="cap"><span class="n">' + (i + 1) + '</span>' + esc(v.name) + ' v' + v.version + '</div>' +
        '<div class="cmp-view" data-cmp="' + i + '"></div>' +
        '<div class="prov">' +
        '<div><span class="k">version</span> ' + esc(v.path) + ' v' + v.version + '</div>' +
        '<div><span class="k">units</span> ' + units + (v.units_note ? ' — ' + esc(v.units_note) : '') + '</div>' +
        '<div><span class="k">generator</span> ' + esc(v.generator) + '</div>' +
        '<div><span class="k">verification</span> ' + esc(v.verification) + ' (machine)</div>' +
        '<div><span class="k">decided</span> ' + esc(v.disposition) + ' (person)</div>' +
        '<div><span class="k">assumptions</span> ' + (v.assumptions || []).length + '</div>' +
        '</div></div>';
    }).join('') + '</div>';

    /* The provenance table repeats what is under each viewport, in a shape that
     * answers the other question: not "what is column 2" but "does this fact
     * differ at all". Both readings are wanted and neither substitutes. */
    html += '<table class="cmp-table"><tr><th>Fact</th>' +
      variants.map(function (v, i) { return '<th>' + (i + 1) + '</th>'; }).join('') + '</tr>';
    (cmp.provenance || []).forEach(function (row) {
      html += '<tr class="' + (row.differs ? 'differs' : '') + '"><td>' + esc(row.field) +
        (row.why ? '<span class="why">' + esc(row.why) + '</span>' : '') + '</td>' +
        row.values.map(function (v) { return '<td>' + esc(v) + '</td>'; }).join('') + '</tr>';
    });
    html += '</table>';

    html += '<table class="cmp-table"><tr><th>Part</th>' +
      variants.map(function (v, i) { return '<th>' + (i + 1) + '</th>'; }).join('') + '</tr>';
    (cmp.parts || []).forEach(function (p) {
      /* A name match is a guess and says so on the row. Nothing in this system
       * keeps a part's id stable between turns, so two rows joined by name may
       * not be the same part — and a reader not told will read it as identity. */
      html += '<tr class="' + (p.differs ? 'differs' : '') + '"><td>' + esc(p.label) +
        (p.matched_by === 'name'
          ? '<span class="why">matched by name, not by identity — FORGE renamed this part between proposals</span>'
          : '') +
        (p.differences && p.differences.length
          ? '<ul class="diffs">' + p.differences.map(function (d) {
              return '<li>' + esc(d) + '</li>';
            }).join('') + '</ul>'
          : '') +
        '</td>' +
        p.cells.map(function (c) {
          if (!c.present) return '<td class="absent">not in this variant</td>';
          return '<td>' + esc(c.shape) + '<br>' + esc(c.dimensions) + '</td>';
        }).join('') + '</tr>';
    });
    html += '</table>';

    if (cmp.match_notes && cmp.match_notes.length) {
      html += '<div class="cmp-uncompared matched"><b>Matched by name, not by identity</b><ul>' +
        cmp.match_notes.map(function (n) { return '<li>' + esc(n) + '</li>'; }).join('') +
        '</ul></div>';
    }
    if (cmp.not_comparable && cmp.not_comparable.length) {
      html += '<div class="cmp-uncompared"><b>Not compared</b><ul>' +
        cmp.not_comparable.map(function (n) { return '<li>' + esc(n) + '</li>'; }).join('') +
        '</ul></div>';
    }
    body.innerHTML = html;

    /* Attach and draw AFTER the markup lands, so each canvas has a size to fit
     * to. A Studio drawn into a zero-sized canvas renders into nothing and the
     * viewport comes up empty with no error anywhere. */
    variants.forEach(function (v, i) {
      var slot = body.querySelector('[data-cmp="' + i + '"]');
      if (!slot) return;
      var pooled = poolStudio(i);
      slot.appendChild(pooled.canvas);
      pooled.studio._resize();
      pooled.studio.load(v.document);
    });
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

  /* What the person is looking at, so "make that taller" resolves against the
   * thing on screen rather than against the transcript (PRD WRK-02).
   *
   * Part IDS are included, not only names. converse.go asks the model to keep
   * ids stable across turns so a revision reads as a revision rather than as two
   * unrelated designs — and until now it had never been SHOWN the ids it was
   * being asked to reuse. The evaluation suite measured that clause working 1
   * time in 4 (internal/eval); this is the cheapest thing that could move it.
   *
   * internal/eval/eval.go builds the same note from the same fields, because an
   * eval that gives the model less context than the product does is measuring a
   * different system. The two are kept in step by
   * TestOnScreen_NamesThePartIDsTheModelIsAskedToReuse. */
  function describeOnScreen() {
    if (!state.prototype) return '';
    var parts = state.prototype.parts.map(function (p) {
      return (p.name || p.id) + ' [id: ' + p.id + ']';
    });
    return state.prototype.name + ' — ' + state.prototype.parts.length + ' part(s): ' + parts.join(', ') +
           ' (units: ' + (state.prototype.units || 'NOT STATED — every dimension here is unitless') + ')' +
           '. Keep these part ids when you revise it.';
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

        case 'variant':
          /* What happened to this turn's geometry. Both outcomes are shown: a
           * workbench that said nothing after a failed save would leave somebody
           * believing they could come back to a shape that was never written
           * down, and they would find out when they went looking for it. */
          noteVariant(ev.variant, bubble);
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
        /* The project this conversation's variants accumulate in (PRD VIS-04).
         * Empty on the first turn: the server makes one and returns its id in
         * the `variant` event, and it is sent back on every turn afterwards so
         * a conversation builds ONE history of variants rather than a project
         * per turn. The server checks it against membership every time, so
         * naming somebody else's project is refused rather than trusted. */
        project_id: state.projectID || '',
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

  /* Close follows the same rule as the soul panel: Escape means "close this"
   * and must NOT fall through to stopping speech, which is what Escape does
   * when no dialog is open. */
  function initCompare() {
    var panel = $('compare');
    var close = $('compare-close');
    if (!panel || !close) return;
    close.addEventListener('click', function () { closeCompare(); });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && !panel.classList.contains('hidden')) {
        e.stopPropagation();
        closeCompare();
      }
    }, true);
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
    safely('compare', initCompare);
    safely('variants', restoreVariants);
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
