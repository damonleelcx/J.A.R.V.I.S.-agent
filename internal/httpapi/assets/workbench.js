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
    prototype: null,
    measured: [],
    states: [],
    images: [],
    requirements: [],
    fromNodes: [],
    selectedPart: null,
    speak: true,
    lastLatency: null,
    firstToken: null,
    lastBargeIn: null,
    turnAudio: null,   // set while a turn is in flight; see send()
    model: null,
    busy: false,
    /* Whether geometry exists. Held rather than passed around because the voice
     * surface's placement now has two causes — geometry, and a stage panel that
     * is not the model — and one field is what stops them disagreeing. */
    building: false,
    /* The proposal's lifecycle, which is also the AGT-08 state machine this
     * card is allowed to display: nothing → proposed → planned → active.
     * Held in one field so the card cannot render two states at once. */
    projectID: null,      // where this conversation's variants are kept
    conversationID: null, // the record this conversation is being kept in
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

  /* The record this workbench is continuing (PRD RSN-07).
   *
   * An IDENTIFIER, like the project key beside it — never the conversation
   * itself. The turns live on the server, every read is scoped to the signed-in
   * person, and a stale or copied value therefore restores nothing rather than
   * somebody else's afternoon. */
  var CONV_KEY = 'forge.workbench.conversation';

  function rememberConversation(id) {
    if (!id || id === state.conversationID) return;
    state.conversationID = id;
    try { window.localStorage.setItem(CONV_KEY, id); } catch (e) { /* not fatal */ }
  }

  function rememberProject(id) {
    var wasNew = id && id !== state.projectID;
    state.projectID = id || state.projectID;
    try { window.localStorage.setItem(PROJECT_KEY, state.projectID); } catch (e) { /* not fatal */ }
    if (window.ForgeStage) window.ForgeStage.setProject(state.projectID);
    /* The graph is worth re-reading once a project exists: the first turn of a
     * session creates it, and requirements recorded elsewhere — the console,
     * forgectl — are what this panel is for. */
    if (wasNew) loadRequirements();
    /* The picker retires the moment a project exists. Its whole job was to be
     * available BEFORE one did. */
    if (wasNew) renderIndustry();
    if (wasNew) loadMembers();
  }

  /* The conversation, brought back (PRD RSN-07).
   *
   * # This paints the screen, and nothing else
   *
   * The page used to keep its own copy of the conversation and send it with
   * every turn. It does not any more: the server builds the model's history from
   * the record it wrote itself, so a client cannot put words in FORGE's mouth by
   * describing a conversation that never happened. What is left here is the
   * transcript a person reads, and losing it would cost a repaint rather than a
   * memory.
   *
   * The record is the server's, not this page's. A conversation id that is not
   * this person's returns nothing, and nothing is what is then shown. */
  function restoreConversation() {
    var id = null;
    try { id = window.localStorage.getItem(CONV_KEY); } catch (e) { return Promise.resolve(false); }
    if (!id) return Promise.resolve(false);
    state.conversationID = id;

    return fetch('/v1/conversations/' + encodeURIComponent(id))
      .then(function (r) {
        if (r.status === 404) {
          /* Deleted, or never this person's. Forgetting the key is right: a
           * workbench that kept asking for a conversation that is gone would
           * send an id the server refuses on every turn. */
          forgetConversationKey();
          return null;
        }
        return r.ok ? r.json() : null;
      })
      .then(function (b) {
        if (!b || !b.turns || !b.turns.length) return false;
        b.turns.forEach(function (t) {
          var who = t.role === 'human' ? 'you' : 'forge';
          var body = t.text || '';
          if (t.images) {
            body += (body ? ' ' : '') + '[' + t.images +
              (t.images === 1 ? ' image' : ' images') + ' attached]';
          }
          addTurn(who, body, t.detail || '', true);
        });
        var note = document.createElement('div');
        note.className = 'turn note';
        note.innerHTML = '<div class="body">' + esc(b.note || '') + '</div>';
        $('transcript').appendChild(note);
        $('transcript').scrollTop = $('transcript').scrollHeight;
        return true;
      })
      .catch(function () { return false; });
  }

  function forgetConversationKey() {
    state.conversationID = null;
    try { window.localStorage.removeItem(CONV_KEY); } catch (e) { /* not fatal */ }
  }

  /* Deleting the record, in two deliberate steps (PRD AUD-07, MEM-01).
   *
   * This layer's retention is "until the person says otherwise", which is only
   * true if saying otherwise is something they can actually do — so the control
   * is beside the conversation rather than in an operator's console. Two steps
   * because it cannot be undone: the first press asks, the second does it, and
   * anything else on the page cancels.
   *
   * It deletes the RECORD. The variants, the project graph and the artifacts
   * this conversation produced are work, not transcript, and they stay — the
   * button says so rather than leaving somebody to guess. */
  function initForget() {
    var btn = $('forget');
    if (!btn) return;
    var armed = false;

    function disarm() {
      armed = false;
      btn.textContent = 'Delete';
      btn.classList.remove('armed');
    }
    document.addEventListener('click', function (e) {
      if (armed && e.target !== btn) disarm();
    }, true);

    btn.addEventListener('click', function () {
      if (!state.conversationID) return;
      if (!armed) {
        armed = true;
        btn.textContent = 'Delete for good?';
        btn.classList.add('armed');
        return;
      }
      btn.disabled = true;
      fetch('/v1/conversations/' + encodeURIComponent(state.conversationID), { method: 'DELETE' })
        .then(function (r) {
          if (!r.ok) throw new Error('the record could not be deleted');
          forgetConversationKey();
          $('transcript').innerHTML = '';
          addTurn('forge', 'The record of this conversation is deleted. What you built — the ' +
            'variants and everything in the project — is still here.');
        })
        .catch(function (err) {
          addTurn('forge', err.message + '. Nothing was deleted.');
        })
        .then(function () { btn.disabled = false; disarm(); });
    });
  }

  /* # Switching project (?project=<id>)
   *
   * The console lists the projects somebody is in and links each one here. This
   * is the receiving end.
   *
   * # Why the conversation does NOT come with you
   *
   * A conversation's variants accumulate IN A PROJECT (PRD VIS-04). Carrying one
   * across a switch would put its history under rules it was not conducted
   * under and its variants in a project they do not belong to — the transcript
   * would straddle two rule sets and the rail would mix work from both. So the
   * switch starts a fresh conversation, and the previous one stays exactly where
   * it was: still in its own project, still restorable by going back to it.
   *
   * The parameter is stripped from the URL afterwards so a refresh does not
   * re-switch — and, more to the point, so a link somebody copies out of their
   * address bar carries the project rather than a one-shot instruction to
   * abandon whatever conversation the recipient was in.
   */
  function switchProjectFromURL() {
    var wanted = null;
    try {
      wanted = new URLSearchParams(window.location.search).get('project');
    } catch (e) { return; }
    if (!wanted) return;

    var current = null;
    try { current = window.localStorage.getItem(PROJECT_KEY); } catch (e) { /* not fatal */ }
    if (wanted !== current) {
      /* A different project means a different conversation. Forgetting the KEY
       * rather than deleting the record: the old conversation is untouched and
       * remains in its project — this only stops it being reopened here. */
      forgetConversationKey();
      try { window.localStorage.setItem(PROJECT_KEY, wanted); } catch (e) { /* not fatal */ }
    }
    try {
      window.history.replaceState({}, '', window.location.pathname);
    } catch (e) { /* an address bar that keeps the parameter is not worth failing over */ }
  }

  function restoreVariants() {
    var id = null;
    try { id = window.localStorage.getItem(PROJECT_KEY); } catch (e) { return; }
    if (!id) return;
    state.projectID = id;
    loadRequirements();
    if (window.ForgeStage) window.ForgeStage.setProject(id);
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
    /* A variant IS an artifact version, so the file list and the checks a person
     * may be looking at are now one turn out of date. Told rather than polled:
     * this is the only moment the page knows a version was written. */
    if (window.ForgeStage) window.ForgeStage.changed();
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

  /* restored marks a turn as coming from the record rather than from this
   * session, and it is not decoration.
   *
   * A live reply arrives with its epistemic labels, the standards it quoted from
   * memory, and the provenance of anything it drew. Those are DERIVED as the
   * reply lands; they are not stored, so they do not come back. A restored turn
   * rendered as an ordinary one would therefore say "FORGE made no claims here",
   * which is a different statement from "nobody kept them" — and the first one
   * is false. */
  function addTurn(who, body, detail, restored) {
    var el = document.createElement('div');
    el.className = 'turn ' + (who === 'you' ? 'you' : 'forge') + (restored ? ' restored' : '');
    el.innerHTML = '<div class="lbl">' + (who === 'you' ? 'You' : 'FORGE') +
      (restored ? '<span class="from-record">from the record</span>' : '') + '</div>' +
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
    if (building !== undefined) state.building = !!building;
    /* Docked when there is something to look at OR when the person is looking at
     * a panel that is not the model. The hero placement takes the middle of the
     * stage, which is where a file list and a diff are; the dock is not a
     * reduced placement (see workbench.css) so nothing becomes unreachable —
     * which is what AUD-06 and AUD-07 require of every path this can take.
     *
     * ONE condition, in one place. The class stays tied to geometry alone: it
     * is what hides the viewport's own controls, and a section slider is no more
     * meaningful over an open diff than over an empty scene. */
    var onModel = !window.ForgeStage || window.ForgeStage.panel() === 'model';
    $('voice').setAttribute('data-place', (state.building || !onModel) ? 'dock' : 'hero');
    $('stage').classList.toggle('building', state.building);
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

  function loadPrototype(proto, measured) {
    state.prototype = proto;
    state.measured = measured || [];
    state.selectedPart = null;
    studio.load(proto);
    /* PRD VIS-03. Authored and derived stay separate all the way here — the
     * server sends two lists and the studio draws them differently, so a
     * dimension somebody took off a drawing never looks like one FORGE worked
     * out from its own guess. */
    studio.setOverlays(proto.overlays || [], state.measured);
    renderStates(proto.states || []);
    setPlace(true);
    renderParts();
    renderProvenance();
  }

  /* PRD VIS-02. The picker only appears when the assembly HAS states: an empty
   * dropdown reading "as modelled" implies somebody could have made more and
   * did not, which is a different claim from "this assembly has one
   * configuration". */
  /* Attaching a sketch or a photograph (PRD VIS-01).
   *
   * Read in the browser as a data URI and sent with the turn, rather than
   * uploaded and referenced: there is nowhere to upload TO — no asset store, no
   * object bucket — and inventing one to carry a picture into a single request
   * would be a lot of storage for something that is used once and never read
   * again.
   *
   * The size cap is the reason this is not a one-liner. A phone photograph is
   * several megabytes, base64 makes it a third larger again, and the server's
   * body limit would reject the turn with a message about JSON rather than
   * about the picture. Refusing here says which file and how big. */
  var MAX_IMAGE_BYTES = 2 * 1024 * 1024;

  function initAttach() {
    var input = $('attach');
    if (!input) return;
    input.addEventListener('change', function () {
      var files = Array.prototype.slice.call(input.files || []);
      input.value = '';
      files.forEach(function (file) {
        if (file.size > MAX_IMAGE_BYTES) {
          renderAttachments('“' + file.name + '” is ' + Math.round(file.size / 1024) +
            'kB. The limit is ' + (MAX_IMAGE_BYTES / 1024) + 'kB — a smaller export or a ' +
            'screenshot of the part you mean will work.');
          return;
        }
        var reader = new FileReader();
        reader.onload = function () {
          state.images.push(String(reader.result));
          renderAttachments('');
        };
        reader.onerror = function () {
          renderAttachments('“' + file.name + '” could not be read.');
        };
        reader.readAsDataURL(file);
      });
    });
  }

  function renderAttachments(problem) {
    var el = $('attached');
    if (!el) return;
    if (!state.images.length && !problem) { el.innerHTML = ''; el.classList.add('hidden'); return; }
    el.classList.remove('hidden');
    var html = state.images.map(function (src, i) {
      return '<span class="att"><img src="' + esc(src) + '" alt="">' +
             '<button type="button" data-drop="' + i + '" aria-label="Remove">×</button></span>';
    });
    if (problem) html.push('<span class="attproblem">' + esc(problem) + '</span>');
    el.innerHTML = html.join('');
    Array.prototype.forEach.call(el.querySelectorAll('[data-drop]'), function (b) {
      b.addEventListener('click', function () {
        state.images.splice(parseInt(b.getAttribute('data-drop'), 10), 1);
        renderAttachments('');
      });
    });
  }

  /* Recorded requirements this conversation can build from (PRD VIS-01).
   *
   * Only requirements and constraints: those are the kinds that say what the
   * thing must do and must stay inside, which is what geometry can be generated
   * FROM. A hazard or a decision is about the work, not about the shape.
   *
   * Loaded once a project exists — before that there is no graph to read, and
   * an empty "Build from" heading over nothing would read as a project whose
   * requirements nobody wrote rather than as no project yet. */
  var BUILDABLE_KINDS = { requirement: true, constraint: true };

  /* # Being bound to a project you are not on
   *
   * ?project= puts the workbench on a project, and nothing stops somebody
   * editing that id by hand. Every endpoint authorises, so nothing leaks — but
   * what the person GOT was a workbench whose panels were all empty, and an
   * empty panel reads as "nothing here yet" rather than "you cannot see this".
   * A surface that answers the wrong question silently is the failure this
   * codebase treats as worse than an error.
   *
   * The graph fetch is the check: it is the first project-scoped call the
   * workbench makes, it already authorises, and a refusal there means every
   * other panel is about to be refused too. No extra request for a state that
   * should never happen.
   *
   * 404 rather than 403 for a non-member is deliberate in access.Service — it
   * refuses to confirm the project exists — so the two are reported the same
   * way here on purpose. Saying "no such project, or not yours" is the whole
   * truth this build is willing to tell, and pretending to know which would
   * undo that decision at the last step.
   */
  function denyProject(status) {
    var el = $('project-denied');
    if (!el) return;
    state.projectDenied = true;
    el.classList.remove('hidden');
    el.innerHTML = '<strong>This project is not one of yours.</strong><br>' +
      'Nothing on this page will load' +
      (status === 403 ? ', because your role here does not allow reading it' :
        ' — either it does not exist, or you are not a member') + '.<br><br>' +
      '<a href="/console">Your projects</a> · ' +
      '<button class="btn-sm" id="project-unbind">Work without a project</button>';
    var unbind = document.getElementById('project-unbind');
    if (unbind) unbind.addEventListener('click', function () {
      /* Offered rather than done automatically. Clearing it silently would send
       * somebody who mistyped an id into a workbench that looks fine, with no
       * sign that the project they asked for was refused. */
      try { window.localStorage.removeItem(PROJECT_KEY); } catch (e) { /* not fatal */ }
      window.location.href = '/workbench';
    });
  }

  function loadRequirements() {
    if (!state.projectID) return;
    fetch('/v1/workspace/graph?project_id=' + encodeURIComponent(state.projectID))
      .then(function (r) {
        if (r.status === 403 || r.status === 404) { denyProject(r.status); return null; }
        return r.ok ? r.json() : null;
      })
      .then(function (g) {
        if (!g) return;
        state.requirements = (g.nodes || []).filter(function (n) { return BUILDABLE_KINDS[n.kind]; });
        renderRequirements();
        /* The rules in force on this project, which arrive with the graph they
         * apply to. Before this the ceiling existed only in a terminal, so
         * somebody in the browser met a refusal with no way to find out what it
         * was about. */
        state.domain = g.domain || null;
        renderIndustry();
      })
      .catch(function () { /* No requirements panel is a smaller loss than a broken workbench. */ });
  }

  function renderRequirements() {
    var el = $('reqs'), head = $('reqs-head');
    if (!el) return;
    if (!state.requirements.length) {
      el.classList.add('hidden');
      if (head) head.classList.add('hidden');
      return;
    }
    el.classList.remove('hidden');
    if (head) head.classList.remove('hidden');
    el.innerHTML = state.requirements.map(function (n) {
      var on = state.fromNodes.indexOf(n.id) >= 0;
      return '<label class="req"><input type="checkbox" data-req="' + esc(n.id) + '"' +
        (on ? ' checked' : '') + '>' +
        '<span><b>' + esc(n.title) + '</b>' +
        '<i>' + esc(n.kind) + '</i></span></label>';
    }).join('');
    Array.prototype.forEach.call(el.querySelectorAll('[data-req]'), function (box) {
      box.addEventListener('change', function () {
        var id = box.getAttribute('data-req');
        var at = state.fromNodes.indexOf(id);
        if (box.checked && at < 0) state.fromNodes.push(id);
        if (!box.checked && at >= 0) state.fromNodes.splice(at, 1);
      });
    });
  }

  function renderStates(states) {
    var el = $('states');
    if (!el) return;
    state.states = states;
    if (!states.length) {
      el.classList.add('hidden');
      el.innerHTML = '';
      studio.setState(null);
      return;
    }
    var opts = ['<option value="">as modelled</option>'];
    states.forEach(function (st, i) {
      opts.push('<option value="' + i + '">' + esc(st.name) + '</option>');
    });
    el.innerHTML = '<label for="statepick">Assembly state</label>' +
      '<select id="statepick">' + opts.join('') + '</select>' +
      '<p class="statenote" id="statenote"></p>';
    el.classList.remove('hidden');
    studio.setState(null);
    $('statepick').addEventListener('change', function (e) {
      var st = e.target.value === '' ? null : state.states[parseInt(e.target.value, 10)];
      studio.setState(st);
      /* What the state claims, said next to the control that applies it. A
       * position FORGE proposed is a guess at how the thing comes apart, and
       * nothing here checked that it can. */
      $('statenote').textContent = st
        ? (st.note ? st.note + ' — ' : '') + 'positions are ' + (st.how || 'proposed') +
          '; no interference or clearance check exists here'
        : '';
    });
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
        /* PRD VIS-02. The material is a claim about what the part IS — cost,
         * weight, whether it can be welded all follow from it — so it is shown
         * with how FORGE came to it, never as a bare noun. */
        (p.material ? '<div class="dim mat">' + esc(p.material.name) +
          ' <i>' + esc(p.material.how || 'assumed') + '</i></div>' : '') +
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
    /* Detached from the turn before the request goes out, so a picture is used
     * once. Leaving it on `state` would re-send it with every later message —
     * silently, at a cost nobody asked for, and answering "make that taller"
     * against a sketch from four turns ago. */
    var sending = state.images.slice();
    state.images = [];
    renderAttachments('');
    var fromNodes = state.fromNodes.slice();

    var bubble = addTurn('forge', '…');
    state.recalled = [];
    var t0 = performance.now();
    setStatus('thinking');

    /* What this turn actually costs, measured as it happens (PRD NFR-05).
     *
     * Two clocks, kept apart. `first_token_ms` and `total_ms` come from the
     * server and describe the model's part; `audioMS` is this browser's, and is
     * the moment speech became audible — which additionally contains the
     * network, the parse, and the synthesiser starting. They are recorded under
     * different names because they answer different questions, and the Telemetry
     * panel refuses to average them. A field nothing measured stays null and is
     * drawn as an em dash rather than a zero. */
    var turn = {
      prompt: text.length > 44 ? text.slice(0, 44) + '…' : text,
      at: new Date().toISOString().slice(11, 19) + ' UTC',
      serverFirstMS: null, serverTotalMS: null, browserTotalMS: null,
      audioMS: null, model: null, tokens: null,
      retrieval: false, geometry: false, failed: false, bargeInMS: null
    };
    /* Where onState writes the moment speech starts. Held on `state` because the
     * voice layer reports to one handler for the whole page, not to this turn. */
    state.turnAudio = function (at) { if (turn.audioMS == null) turn.audioMS = at - t0; };

    streamTurn(text, sending, fromNodes, function (ev) {
      switch (ev.kind) {
        case 'speech':
          bubble.querySelector('.body').textContent = ev.text;
          if (ev.first_token_ms) turn.serverFirstMS = ev.first_token_ms;
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
          turn.geometry = true;
          ev.prototype.model_note = state.model || 'FORGE';
          loadPrototype(ev.prototype, ev.measured);
          break;

        case 'conversation':
          /* The id arrives before the reply, so a turn that then fails still
           * leaves a record the person can come back to. */
          rememberConversation(ev.conversation && ev.conversation.id);
          if (ev.conversation && ev.conversation.not_kept) {
            /* Said in the transcript, quietly, for the same reason a variant
             * that could not be saved is: the answer is on screen and still
             * useful, but what the person can do NEXT has changed — this turn
             * will not be here after a reload — and finding that out by
             * reloading is the worst moment to learn it. */
            var nk = document.createElement('div');
            nk.className = 'detail';
            nk.style.color = 'var(--warn)';
            nk.textContent = ev.conversation.not_kept;
            if (bubble) bubble.appendChild(nk);
          }
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
          turn.retrieval = (ev.recalled || []).length > 0;
          state.recalled = ev.recalled || [];
          bubble.appendChild(recalledBlock(state.recalled));
          renderProvenance();
          break;

        case 'error':
          turn.failed = true;
          var e = document.createElement('div');
          e.className = 'detail';
          e.style.color = 'var(--bad)';
          e.textContent = ev.error;
          bubble.appendChild(e);
          break;

        case 'done':
          state.model = ev.model || state.model;
          turn.model = ev.model || state.model;
          turn.tokens = ev.tokens || null;
          if (ev.total_ms) turn.serverTotalMS = ev.total_ms;
          state.lastLatency = ev.total_ms || Math.round(performance.now() - t0);
          updateMeta();
          break;
      }
    }).catch(function (err) {
      turn.failed = true;
      bubble.querySelector('.body').textContent = err.message;
      bubble.querySelector('.body').style.color = 'var(--bad)';
    }).then(function () {
      state.busy = false;
      turn.browserTotalMS = Math.round(performance.now() - t0);
      state.turnAudio = null;
      /* Handed over whatever happened, including a turn that failed. A telemetry
       * panel that only recorded the turns that worked would report a latency
       * distribution with its worst cases removed. */
      if (window.ForgeStage) window.ForgeStage.turn(turn);
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
  function streamTurn(text, images, fromNodes, onEvent) {
    return fetch('/v1/converse', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        message: text,
        /* The project this conversation's variants accumulate in (PRD VIS-04).
         * Empty on the first turn: the server makes one and returns its id in
         * the `variant` event, and it is sent back on every turn afterwards so
         * a conversation builds ONE history of variants rather than a project
         * per turn. The server checks it against membership every time, so
         * naming somebody else's project is refused rather than trusted. */
        project_id: state.projectID || '',
        /* Sent every turn. The server ignores it once the project exists, so
         * the client does not have to know which turn happened to be the one
         * that created it. */
        industry: state.projectID ? '' : (state.industry || ''),
        /* The record this turn joins (PRD RSN-07). Empty on the first turn: the
         * server mints one and sends it back in the `conversation` event. A id
         * that is not this person's is REFUSED rather than swapped for a new
         * one, so "continue that conversation" cannot quietly become "start a
         * different one". */
        conversation_id: state.conversationID || '',
        /* PRD VIS-01. Attached to THIS turn only; the array is cleared as soon
         * as it is sent, so a sketch does not silently ride along on every
         * later message. */
        images: images && images.length ? images : undefined,
        /* Ids only. The server reads the requirement's own words out of the
         * graph — a client that sent both could name requirement A and paste
         * the words of B, and the variant's provenance would then record a
         * requirement the model never saw. */
        from_nodes: fromNodes && fromNodes.length ? fromNodes : undefined,
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

  /* # The industry selector (PRD §"Domain packs")
   *
   * A project is worked under a domain pack: its units, its vocabulary, and the
   * highest risk tier work may reach inside it. Until this existed the browser
   * could not say which — only `forgectl goal new --industry` could — so every
   * project started from the workbench was filed as "Other", the pack that means
   * UNKNOWN DOMAIN and deliberately carries no conventions at all.
   *
   * The options are fetched from /v1/meta/industries rather than written here.
   * A copy of a closed set in the page is the copy that goes stale, and somebody
   * would then pick an industry the server no longer knows — refused for a name
   * this very file had shown them.
   *
   * Shown only while no project exists. The industry belongs to the PROJECT, and
   * the server refuses one sent alongside an existing project id rather than
   * dropping it silently; offering the control there would be offering an act
   * that cannot happen. Changing it afterwards is deliberate and lives in
   * `forgectl project industry`.
   */
  function loadIndustries() {
    if (state.industries) return Promise.resolve(state.industries);
    return fetch('/v1/meta/industries').then(function (r) {
      return r.ok ? r.json() : { industries: [] };
    }).then(function (b) {
      state.industries = (b && b.industries) || [];
      return state.industries;
    }).catch(function () {
      /* An unreachable catalogue must not block the proposal. The goal is
       * created without an industry, which is `general` — the honest answer
       * when nothing established a domain — rather than no goal at all. */
      state.industries = [];
      return state.industries;
    });
  }

  /* What the domain means once it is settled: which rules, how far work may go,
   * and — when a ceiling has been raised — who that rests on and what was NOT
   * established by it.
   *
   * A statement, not a control. The industry belongs to the project from the
   * moment one exists and the server refuses a change through this path, so
   * offering the select here would offer an act that cannot happen. What a
   * person needs instead is the answer to "why was that refused". */
  function industryStatement() {
    var d = state.domain;
    if (!d) return '';
    var html = '<div class="industry"><div class="settled">' +
      esc(d.industry || d.pack) + ' · ceiling ' + esc(d.ceiling) + '</div>' +
      '<div class="foot">' + esc(d.boundary) +
      ' Work above ' + esc(d.ceiling) + ' here would require ' + esc(d.requires) + '.</div>';
    if (d.authority) {
      /* The holder and the caveat are rendered TOGETHER, always. Showing the
       * name without it would present a claim as a credential. */
      html += '<div class="foot raised">Ceiling raised on ' + esc(d.authority.holder) +
        (d.authority.note ? ' (' + esc(d.authority.note) + ')' : '') + '. ' +
        esc(d.authority.caveat) + '</div>';
    }
    html += authorityControl(d);
    return html + '</div>';
  }

  /* # Recording an authority from the workbench
   *
   * Offered only to somebody the server would actually let do it — the graph
   * response says whether this caller may, and that flag is an affordance rather
   * than the gate: PUT is authorised server-side whatever the panel shows.
   *
   * # Why recording takes two presses
   *
   * The same rule "Start this" follows, for a stronger reason. This is the only
   * control in the product that WIDENS what may be done, and what it records is
   * a claim nothing verifies. So the caveat is put in front of the person BEFORE
   * they commit, not printed at them afterwards: somebody who reads "recorded,
   * not verified" only in the confirmation has already acted on the belief it
   * corrects.
   */
  function authorityControl(d) {
    if (!d.can_record_authority) return '';
    if (d.authority) {
      return '<div class="authority-act">' +
        '<button class="btn-sm" id="authority-clear">Clear authority</button></div>';
    }
    if (!d.asks_for && !state.authorityForm) return '';
    if (!state.authorityForm) {
      return '<div class="authority-act">' +
        '<button class="btn-sm" id="authority-open">Record review authority</button></div>';
    }
    return '<div class="authority-form">' +
      '<label for="authority-holder">Who is accountable</label>' +
      '<input type="text" id="authority-holder" placeholder="Their name" ' +
      'value="' + esc(state.authorityHolder || '') + '">' +
      '<label for="authority-note">What they hold</label>' +
      '<input type="text" id="authority-note" placeholder="Registration, role or scope" ' +
      'value="' + esc(state.authorityNote || '') + '">' +
      /* Read before the act, never only after it. */
      '<div class="foot raised">' + esc(theCaveat()) + '</div>' +
      (state.authorityError ? '<div class="note bad">' + esc(state.authorityError) + '</div>' : '') +
      '<div class="authority-act">' +
      '<button class="btn-sm go" id="authority-save">Record it</button>' +
      '<button class="btn-sm" id="authority-cancel">Cancel</button></div>' +
      '</div>';
  }

  /* The caveat, taken from the server when it has been seen and otherwise stated
   * here in the same words. Not fetched on demand: the person is mid-decision,
   * and a sentence that arrives late is one they may act without. */
  function theCaveat() {
    if (state.domain && state.domain.authority && state.domain.authority.caveat) {
      return state.domain.authority.caveat;
    }
    return 'RECORDED, NOT VERIFIED. This build cannot check a qualification: there is no ' +
      'registry to consult and no credential to validate. The ceiling rises because a named ' +
      'person accepted responsibility, not because a licence was established.';
  }

  function authorityRequest(method, body) {
    return fetch('/v1/projects/' + encodeURIComponent(state.projectID) + '/review-authority', {
      method: method,
      headers: { 'Content-Type': 'application/json' },
      body: body ? JSON.stringify(body) : undefined
    }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (b) {
        if (!r.ok) {
          var e = (b && b.error) || {};
          throw new Error(e.message || ('Request failed (' + r.status + ')'));
        }
        return b;
      });
    });
  }

  function renderIndustry() {
    var el = $('industry');
    var head = $('industry-head');
    if (!el || !head) return;
    var html = industryPicker() || industryStatement();
    if (!html) {
      el.classList.add('hidden');
      head.style.display = 'none';
      return;
    }
    head.style.display = '';
    el.classList.remove('hidden');
    el.innerHTML = html;
    head.textContent = state.projectID ? 'Domain' : 'Industry';
    bindAuthorityControls();
    var pick = document.getElementById('industry-pick');
    if (pick) pick.addEventListener('change', function () {
      state.industry = pick.value;
      /* Re-rendered so the boundary line under the control describes the
       * industry now selected. A control that changes what the work is done
       * under, above a sentence describing a different domain, is worse than
       * no sentence at all. */
      renderIndustry();
    });
  }

  /* # The people on this project
   *
   * Membership decides what everyone here may do, so it is worth seeing next to
   * the domain that decides how far the work may go. Loaded when a project
   * appears and re-read after any change, for the reason the domain panel is
   * re-read: the SERVER decides who holds what, and a panel that patched its own
   * copy would drift from the thing being enforced.
   *
   * `can_manage` from the response decides whether the controls appear. It is an
   * affordance and not the gate — the writes authorise themselves.
   */
  function loadMembers() {
    if (!state.projectID) return;
    fetch('/v1/projects/' + encodeURIComponent(state.projectID) + '/members')
      .then(function (r) { return r.ok ? r.json() : null; })
      .then(function (b) {
        state.members = b || null;
        renderMembers();
      })
      .catch(function () { /* A missing people panel is a smaller loss than a broken workbench. */ });
  }

  function memberRow(m, canManage, roles) {
    var who = esc(m.display_name || m.user_id) + (m.is_you ? ' <span class="you">you</span>' : '');
    var html = '<li><div class="who">' + who +
      (m.email ? '<div class="addr">' + esc(m.email) + '</div>' : '') + '</div>';
    if (!canManage) {
      return html + '<span class="role">' + esc(m.role) + '</span></li>';
    }
    var opts = roles.map(function (r) {
      return '<option value="' + esc(r.role) + '"' +
        (r.role === m.role ? ' selected' : '') + '>' + esc(r.role) + '</option>';
    }).join('');
    return html +
      '<select data-role-pick data-user="' + esc(m.user_id) + '" ' +
      'aria-label="Role for ' + esc(m.display_name || m.user_id) + '">' + opts + '</select>' +
      '<button class="btn-sm" data-member-remove data-user="' + esc(m.user_id) + '" ' +
      'aria-label="Remove ' + esc(m.display_name || m.user_id) + '">Remove</button></li>';
  }

  function renderMembers() {
    var el = $('members');
    var head = $('members-head');
    if (!el || !head) return;
    var m = state.members;
    if (!m || !m.members) {
      el.classList.add('hidden');
      head.style.display = 'none';
      return;
    }
    head.style.display = '';
    el.classList.remove('hidden');

    var roles = m.roles || [];
    var html = '<ul class="members">' + m.members.map(function (row) {
      return memberRow(row, m.can_manage, roles);
    }).join('') + '</ul>';

    if (m.can_manage) {
      html += '<div class="member-add">' +
        '<input type="text" id="member-email" placeholder="Email of somebody to add" ' +
        'aria-label="Email of somebody to add">' +
        '<select id="member-role" aria-label="Role for the person being added">' +
        roles.map(function (r) {
          return '<option value="' + esc(r.role) + '"' +
            (r.role === 'contributor' ? ' selected' : '') + '>' + esc(r.role) + '</option>';
        }).join('') + '</select>' +
        '<button class="btn-sm go" id="member-add">Add</button></div>';
      /* What each role actually permits, from the server's own catalogue. A
       * person choosing between four words needs to know what they mean. */
      var chosen = roles.filter(function (r) { return r.role === (state.memberRole || 'contributor'); })[0];
      if (chosen) html += '<div class="foot">' + esc(chosen.role) + ' ' + esc(chosen.does) + '</div>';
    }
    if (state.membersError) {
      html += '<div class="note bad">' + esc(state.membersError) + '</div>';
    }
    el.innerHTML = html;
    bindMemberControls();
  }

  function memberRequest(method, path, body) {
    return fetch('/v1/projects/' + encodeURIComponent(state.projectID) + '/members' + path, {
      method: method,
      headers: { 'Content-Type': 'application/json' },
      body: body ? JSON.stringify(body) : undefined
    }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (b) {
        if (!r.ok) {
          var e = (b && b.error) || {};
          throw new Error(e.message || ('Request failed (' + r.status + ')'));
        }
        /* Every write answers with the whole list, so the panel never has to
         * guess what it left behind — including when the server refused part of
         * what was asked. */
        state.members = b;
        state.membersError = null;
        renderMembers();
      });
    }).catch(function (err) {
      state.membersError = err.message;
      renderMembers();
    });
  }

  function bindMemberControls() {
    Array.prototype.forEach.call(document.querySelectorAll('#members [data-role-pick]'), function (sel) {
      sel.addEventListener('change', function () {
        memberRequest('PUT', '/' + encodeURIComponent(sel.getAttribute('data-user')),
          { role: sel.value });
      });
    });
    Array.prototype.forEach.call(document.querySelectorAll('#members [data-member-remove]'), function (b) {
      b.addEventListener('click', function () {
        memberRequest('DELETE', '/' + encodeURIComponent(b.getAttribute('data-user')), null);
      });
    });
    var role = document.getElementById('member-role');
    if (role) role.addEventListener('change', function () {
      state.memberRole = role.value;
      renderMembers();
    });
    var add = document.getElementById('member-add');
    if (add) add.addEventListener('click', function () {
      var email = (document.getElementById('member-email') || {}).value || '';
      if (!email.trim()) {
        state.membersError = 'An email address is required to name who is being added.';
        renderMembers();
        return;
      }
      memberRequest('POST', '', { email: email, role: (role && role.value) || 'contributor' });
    });
  }

  function bindAuthorityControls() {
    var open = document.getElementById('authority-open');
    if (open) open.addEventListener('click', function () {
      state.authorityForm = true;
      state.authorityError = null;
      renderIndustry();
    });
    var cancel = document.getElementById('authority-cancel');
    if (cancel) cancel.addEventListener('click', function () {
      state.authorityForm = false;
      state.authorityError = null;
      renderIndustry();
    });
    var save = document.getElementById('authority-save');
    if (save) save.addEventListener('click', function () {
      var holder = (document.getElementById('authority-holder') || {}).value || '';
      var note = (document.getElementById('authority-note') || {}).value || '';
      /* Kept so a refused write does not throw away what was typed. */
      state.authorityHolder = holder;
      state.authorityNote = note;
      if (!holder.trim()) {
        state.authorityError = 'A name is required: the ceiling rests on a named person.';
        renderIndustry();
        return;
      }
      save.disabled = true;
      authorityRequest('PUT', { holder: holder, note: note }).then(function () {
        state.authorityForm = false;
        state.authorityHolder = null;
        state.authorityNote = null;
        state.authorityError = null;
        /* Re-read rather than patch state locally: the ceiling that results is
         * the SERVER's answer, and a panel that computed its own would eventually
         * show a limit that is not the one being enforced. */
        loadRequirements();
      }).catch(function (err) {
        state.authorityError = err.message;
        renderIndustry();
      });
    });
    var clear = document.getElementById('authority-clear');
    if (clear) clear.addEventListener('click', function () {
      clear.disabled = true;
      authorityRequest('DELETE', null).then(loadRequirements).catch(function (err) {
        state.authorityError = err.message;
        renderIndustry();
      });
    });
  }

  function industryPicker() {
    if (state.projectID) return '';
    var list = state.industries || [];
    if (!list.length) return '';
    var opts = list.map(function (d) {
      var sel = d.id === state.industry ? ' selected' : '';
      return '<option value="' + esc(d.id) + '"' + sel + '>' + esc(d.label) + '</option>';
    }).join('');
    var chosen = null;
    for (var i = 0; i < list.length; i++) {
      if (list[i].id === state.industry) { chosen = list[i]; }
    }
    /* No visible <label>: the section heading above already says "Industry",
     * and two of the same word stacked reads as a mistake. The accessible name
     * comes from aria-label so a screen reader is not left with a bare combobox
     * (PRD AUD-06 asks every critical interaction to have a non-audio path, and
     * an unnamed control is not one). */
    return '<div class="industry">' +
      '<select id="industry-pick" aria-label="Industry this project works in">' +
      opts + '</select>' +
      (chosen ? '<div class="foot">' + esc(chosen.boundary) +
                ' Work above ' + esc(chosen.ceiling) + ' here would require ' +
                esc(chosen.requires) + '.</div>' : '') +
      '</div>';
  }

  function proposeGoal(goal) {
    state.proposal = goal;
    state.goal = null;
    state.planTasks = null;
    state.goalPhase = 'proposed';
    renderProposal();
  }

  /* Loaded once, at startup, because the project is created by the first KEPT
   * VARIANT — which can happen long before any work is proposed. A catalogue
   * fetched at proposal time would arrive after the decision it informs. */
  function startIndustry() {
    /* "Other" is the default and it is a real answer, not a placeholder: the
     * `general` pack means unknown domain, lowers autonomy and triggers expert
     * review. Defaulting to a guessed industry would file work under rules
     * nobody chose while looking exactly like a stated one. */
    if (!state.industry) state.industry = 'general';
    loadIndustries().then(renderIndustry);
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
      risk_tier: state.proposal.risk_tier || 'r1',
      /* Only when this conversation has no project yet. The server REFUSES an
       * industry sent with a project id — the industry belongs to the project,
       * and changing it would change the rules its earlier work was done under —
       * so sending one here would turn every follow-up goal into an error. */
      industry: state.projectID ? '' : (state.industry || '')
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
    /* "first token", not "spoke at". What is measured is when the model's first
     * words arrived, which is before the synthesiser has said anything; the
     * Telemetry panel keeps that distinction and this line used to blur it. */
    if (state.firstToken != null) bits.push('first token ' + state.firstToken + 'ms');
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
        if (window.ForgeStage) window.ForgeStage.bargeIn(state.lastBargeIn);
        updateMeta();
      },
      onState: function (s) {
        /* The moment speech became audible, for the turn that is in flight. This
         * is the browser's own clock and it is the closest thing this build has
         * to AUD-02's "first audio" — the panel says so, and says what it still
         * does not include. */
        if (s.speaking && state.turnAudio) state.turnAudio(performance.now());
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
    /* PRD VIS-03. Off by default: a first look at a proposed shape should be the
     * shape, and a viewport that opens covered in numbers trains people to
     * dismiss them. Turning them on is one click, and what they mean is on
     * every label. */
    $('dims').addEventListener('change', function (e) {
      studio.setOverlaysVisible(e.target.checked);
    });

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
      labels: $('dimlayer'),
      onError: function (msg) {
        // A renderer that cannot start is stated as a failure, in its own words,
        // rather than left to read as "nothing modelled yet".
        $('stage-empty').textContent = msg;
        $('stage-empty').classList.remove('hidden');
      }
    });
    /* BEFORE anything reads the stored project or conversation, so a switch is
     * in force by the time the panels restore rather than being applied over
     * the top of the previous project's state. */
    safely('switch', switchProjectFromURL);
    safely('industry', startIndustry);
    safely('voice', initVoice);
    safely('controls', initControls);
    safely('attach', initAttach);
    safely('soul', initSoul);
    safely('compare', initCompare);
    safely('stage', function () {
      window.ForgeStage.mount({ onPanel: function () { setPlace(); } });
    });
    safely('variants', restoreVariants);
    safely('forget', initForget);

    /* Started here and awaited below, so the restored turns are on screen before
     * anything is said about being ready — a greeting above a conversation that
     * is already in progress reads as the conversation having been lost. */
    var restoring = Promise.resolve(false);
    safely('conversation', function () { restoring = restoreConversation(); });
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
      restoring.then(function (resumed) {
        if (resumed) {
          /* Not "Ready". The conversation above is where this left off, and
           * saying so is the difference between a resumed session and one that
           * merely looks like it kept the text. */
          setCaption('Picking up where you left off.', false);
          return;
        }
        addTurn('forge',
          'Ready. Describe what you are building and I will propose a shape you can turn around. ' +
          'Hold the microphone or the space bar to talk; press Escape to stop me mid-sentence.');
      });
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
