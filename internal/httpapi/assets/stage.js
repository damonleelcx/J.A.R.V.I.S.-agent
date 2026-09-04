/* FORGE workbench — the stage's panels (PRD WRK-01).
 *
 * # What this is for
 *
 * WRK-01 asks the workspace to carry "code, CAD/EDA previews, diagrams,
 * telemetry, requirements, diffs, simulations, test results". The stage carried
 * one of them: the 3D model. Everything else the system records — every version
 * of every file, every diff, what a machine found and what a person decided —
 * existed in the database and in the API and nowhere a person was looking.
 *
 * These panels put the recorded work on the same surface as the render, without
 * taking the middle of the screen away from the model, which is still what the
 * product is for.
 *
 * # The rules this file follows, which are product decisions rather than plumbing
 *
 *   - A partial listing is worse than none. If a file's history cannot be read,
 *     the file still appears and says so. Dropping it would produce a list that
 *     looks complete and is not, which nobody checks.
 *   - Verification and disposition are never merged, never derived from one
 *     another, and never summarised into a single "status" (PRD SAF-05). The
 *     server already computes "may this be relied on" and says why not; that
 *     sentence is shown, not recomputed here.
 *   - Nothing checked must not look like nothing wrong. A column of "unverified"
 *     is the honest state of this build and it is stated in words, because a
 *     tidy list of grey chips reads as a clean bill of health.
 *   - The two panels this build cannot fill are rendered by the server from the
 *     connectors' own refusals (see stage.go). Nothing here writes that text.
 *
 * Exposed as window.ForgeStage. It knows nothing about the conversation, so a
 * failure here cannot take the conversation down; workbench.js mounts it inside
 * its own isolation wrapper.
 */
(function (global) {
  'use strict';

  function $(id) { return document.getElementById(id); }

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  var state = {
    projectID: null,
    panel: 'model',
    /* One store, two readers. Files and Checks are two views of the same
     * histories: fetching them twice would let the panels disagree about what
     * the project contains, which is the sort of disagreement nobody notices
     * until it matters. */
    files: [],       // [{artifactID, path, kind, versions, error}]
    selected: null,  // artifact id shown in the detail pane
    phase: 'idle',   // idle | loading | ready | failed
    error: null,
    mounted: false,
    onPanel: null
  };

  /* ---- reading the project ------------------------------------------------
   *
   * Two hops, and both are existing endpoints. The graph knows which artifacts
   * belong to the project — every artifact has been anchored into it since
   * RecordChange started doing so in the same transaction as the version — and
   * each artifact's own history carries WRK-04's seven facts.
   *
   * No project-wide history endpoint was added for this. It would be a public
   * contract to maintain for a fan-out the browser can do, and the fan-out is
   * bounded by the number of files in one project. If that stops being true the
   * endpoint is the answer, and this is the code that will say so.
   */

  function get(path) {
    return fetch(path).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (b) {
        if (!r.ok) {
          var e = (b && b.error) || {};
          throw new Error(e.message || ('Request failed (' + r.status + ')'));
        }
        return b;
      });
    });
  }

  function load() {
    if (!state.projectID) {
      state.phase = 'idle';
      state.files = [];
      render();
      return Promise.resolve();
    }
    state.phase = 'loading';
    state.error = null;
    render();

    var projectAtStart = state.projectID;
    return get('/v1/workspace/graph?project_id=' + encodeURIComponent(projectAtStart))
      .then(function (g) {
        var anchors = (g.nodes || []).filter(function (n) {
          return n.kind === 'artifact' && n.anchor_id;
        });
        return Promise.all(anchors.map(function (n) {
          return get('/v1/workspace/artifacts/' + encodeURIComponent(n.anchor_id))
            .then(function (h) {
              return {
                artifactID: n.anchor_id,
                path: (h.artifact && h.artifact.path) || n.title || n.anchor_id,
                kind: (h.artifact && h.artifact.kind) || '',
                versions: h.versions || [],
                error: null
              };
            })
            .catch(function (e) {
              /* Kept, not dropped. A file whose history failed to load is a
               * fact about this reading; a file missing from the list is a
               * false statement about the project. */
              return {
                artifactID: n.anchor_id,
                path: n.title || n.anchor_id,
                kind: '',
                versions: [],
                error: e.message || 'its history could not be read'
              };
            });
        }));
      })
      .then(function (files) {
        if (state.projectID !== projectAtStart) return; // switched underneath us
        files.sort(function (a, b) { return a.path < b.path ? -1 : a.path > b.path ? 1 : 0; });
        state.files = files;
        state.phase = 'ready';
        if (!state.selected || !fileOf(state.selected)) {
          state.selected = files.length ? files[0].artifactID : null;
        }
        render();
      })
      .catch(function (e) {
        if (state.projectID !== projectAtStart) return;
        /* Loud. Unlike the requirements rail, which is a convenience beside the
         * conversation, this panel IS what the person opened — an empty one
         * would answer their question with a wrong answer. */
        state.phase = 'failed';
        state.error = e.message || 'the project could not be read';
        render();
      });
  }

  function fileOf(id) {
    for (var i = 0; i < state.files.length; i++) {
      if (state.files[i].artifactID === id) return state.files[i];
    }
    return null;
  }

  /* ---- rendering: shared pieces ------------------------------------------ */

  /* Timestamps are rendered from the server's RFC3339 string with no locale
   * involved. A browser-local rendering would make two people reading the same
   * ledger describe the same change with two different times, and a ledger is
   * exactly the place that must not happen. */
  function when(iso) {
    if (!iso) return '';
    var s = String(iso).replace('T', ' ');
    var dot = s.indexOf('.');
    if (dot > 0) s = s.slice(0, dot) + s.slice(s.indexOf('Z') >= 0 ? s.indexOf('Z') : s.length);
    return esc(s.replace('Z', ' UTC'));
  }

  function chip(kind, value, extra) {
    return '<span class="wbchip wbchip-' + esc(kind) + '-' + esc(value) + '">' +
      esc(value) + (extra ? ' <i>' + esc(extra) + '</i>' : '') + '</span>';
  }

  function empty(text) { return '<div class="wbempty">' + esc(text) + '</div>'; }

  function problem(text) {
    return '<div class="wbproblem" role="alert">' + esc(text) + '</div>';
  }

  /* A unified diff, coloured by line. Escaped first and always: a diff is the
   * contents of somebody's file, which is untrusted input in exactly the way
   * PRD SEC-04 means. */
  function diffHTML(diff) {
    if (!diff) {
      /* "" is a recorded value meaning "nothing textual to show", not a gap —
       * the column is NOT NULL for that reason. Said in those words so it is
       * not read as "this change did nothing". */
      return '<p class="wbver-nodiff">No textual diff was recorded for this change. ' +
        'That is a statement, not a missing field.</p>';
    }
    var lines = String(diff).split('\n').map(function (line) {
      var cls = '';
      if (line.indexOf('+++') === 0 || line.indexOf('---') === 0) cls = 'wbd-file';
      else if (line.indexOf('@@') === 0) cls = 'wbd-hunk';
      else if (line.charAt(0) === '+') cls = 'wbd-add';
      else if (line.charAt(0) === '-') cls = 'wbd-del';
      return '<span class="wbd-l ' + cls + '">' + esc(line === '' ? ' ' : line) + '</span>';
    });
    return '<pre class="wbdiff">' + lines.join('\n') + '</pre>';
  }

  /* ---- the Files panel ---------------------------------------------------- */

  function renderFiles() {
    var list = $('files-list'), detail = $('files-detail');
    if (!list || !detail) return;

    if (state.phase === 'loading') {
      list.innerHTML = empty('Reading the project…');
      detail.innerHTML = '';
      return;
    }
    if (state.phase === 'failed') {
      list.innerHTML = problem('The project could not be read: ' + state.error);
      detail.innerHTML = '';
      return;
    }
    if (!state.projectID) {
      list.innerHTML = empty('No project yet. The first thing you describe creates one, ' +
        'and everything FORGE writes into it appears here.');
      detail.innerHTML = '';
      return;
    }
    if (!state.files.length) {
      list.innerHTML = empty('This project has no files yet.');
      detail.innerHTML = '';
      return;
    }

    list.innerHTML = state.files.map(function (f) {
      var sub = f.error
        ? 'history unavailable'
        : f.versions.length + (f.versions.length === 1 ? ' version' : ' versions') +
          (f.kind ? ' · ' + f.kind : '');
      return '<button type="button" class="wbfile" data-artifact="' + esc(f.artifactID) + '"' +
        ' aria-current="' + (state.selected === f.artifactID) + '">' +
        '<span class="wbfile-p">' + esc(f.path) + '</span>' +
        '<span class="wbfile-m">' + esc(sub) + '</span>' +
        '</button>';
    }).join('');

    Array.prototype.forEach.call(list.querySelectorAll('[data-artifact]'), function (b) {
      b.addEventListener('click', function () {
        state.selected = b.getAttribute('data-artifact');
        renderFiles();
      });
    });

    detail.innerHTML = fileDetail(fileOf(state.selected));
  }

  function fileDetail(f) {
    if (!f) return empty('Pick a file.');
    var head = '<div class="wbhead">' +
      '<h2 class="wbhead-t">' + esc(f.path) + '</h2>' +
      '<div class="wbhead-s">' + esc(f.kind || 'artifact') + ' · ' +
      esc(String(f.versions.length)) + (f.versions.length === 1 ? ' version' : ' versions') +
      '</div></div>';
    if (f.error) return head + problem('This file is in the project graph, but its history could not be read: ' + f.error);
    if (!f.versions.length) {
      /* Anchored with no versions should not happen — an artifact is created by
       * its first version — so it is reported rather than rendered as an empty
       * list, which would read as normal. */
      return head + problem('This file is anchored in the project graph but has no recorded versions. ' +
        'That should not be possible: a file comes into existence with its first change.');
    }
    var versions = f.versions.slice().sort(function (a, b) { return b.version - a.version; });
    return head + '<ol class="wbvers">' + versions.map(versionHTML).join('') + '</ol>';
  }

  /* One version, carrying WRK-04's seven facts.
   *
   * All seven, every time. The requirement is the list, and a version rendered
   * with five of them looks like a complete record of a change that half of
   * nobody can trace. */
  function versionHTML(v) {
    var tool = v.tool_call_id
      ? 'tool ' + v.tool_call_id
      : 'no tool call — this agent works without one';
    return '<li class="wbver">' +
      '<div class="wbver-h">' +
        '<span class="wbver-n">v' + esc(String(v.version)) + '</span>' +
        '<span class="wbver-w">' + when(v.created_at) + '</span>' +
        '<span class="wbver-a">' + esc(v.agent) + '</span>' +
      '</div>' +
      '<div class="wbver-f">' +
        chip('v', v.verification_state, v.verification_note) +
        chip('d', v.human_disposition, v.dispositioned_by || '') +
      '</div>' +
      /* Computed by the server, quoted here. "May this be relied on" needs both
       * facts and the rule that combines them lives in Go — a browser deciding
       * it for itself would eventually decide a passing test suite is a
       * sign-off. */
      '<p class="wbver-u' + (v.usable ? ' ok' : '') + '">' + esc(v.usable_why) + '</p>' +
      '<div class="wbver-m">' +
        '<span>initiator ' + esc(v.initiator_id) + '</span>' +
        '<span>' + esc(tool) + '</span>' +
        (v.event_id ? '<span>event ' + esc(v.event_id) + '</span>' : '') +
      '</div>' +
      (v.disposition_reason ? '<p class="wbver-r">' + esc(v.disposition_reason) + '</p>' : '') +
      '<details class="wbver-i"><summary>Inputs</summary>' +
        '<pre class="wbjson">' + esc(JSON.stringify(v.inputs, null, 2)) + '</pre>' +
      '</details>' +
      diffHTML(v.diff) +
      '</li>';
  }

  /* ---- the Checks panel --------------------------------------------------- */

  /* Order: failed, then unverified, then passed.
   *
   * Not chronological. Somebody opens this panel to find out whether anything is
   * wrong, and a failure four screens down is a failure nobody read. */
  var CHECK_ORDER = { failed: 0, unverified: 1, passed: 2 };

  function renderChecks() {
    var el = $('checks-body');
    if (!el) return;

    if (state.phase === 'loading') { el.innerHTML = empty('Reading the project…'); return; }
    if (state.phase === 'failed') {
      el.innerHTML = problem('The project could not be read: ' + state.error);
      return;
    }
    if (!state.projectID) {
      el.innerHTML = empty('No project yet. Verification state appears here as soon as ' +
        'there is something to have checked.');
      return;
    }

    var rows = [], counts = { passed: 0, failed: 0, unverified: 0 }, unreadable = 0;
    state.files.forEach(function (f) {
      if (f.error) { unreadable++; return; }
      f.versions.forEach(function (v) {
        var s = v.verification_state;
        if (counts[s] === undefined) counts[s] = 0;
        counts[s]++;
        rows.push({ path: f.path, artifactID: f.artifactID, v: v });
      });
    });

    if (!rows.length && !unreadable) {
      el.innerHTML = empty('No changes have been recorded in this project yet.');
      return;
    }

    rows.sort(function (a, b) {
      var o = (CHECK_ORDER[a.v.verification_state] === undefined ? 9 : CHECK_ORDER[a.v.verification_state]) -
              (CHECK_ORDER[b.v.verification_state] === undefined ? 9 : CHECK_ORDER[b.v.verification_state]);
      if (o !== 0) return o;
      if (a.path !== b.path) return a.path < b.path ? -1 : 1;
      return b.v.version - a.v.version;
    });

    var head = '<div class="wbsum">' +
      '<span class="wbsum-n">' + rows.length + '</span> ' +
      (rows.length === 1 ? 'change' : 'changes') + ' across ' +
      '<span class="wbsum-n">' + state.files.length + '</span> ' +
      (state.files.length === 1 ? 'file' : 'files') +
      '<span class="wbsum-c">' +
        chip('v', 'failed', String(counts.failed)) +
        chip('v', 'unverified', String(counts.unverified)) +
        chip('v', 'passed', String(counts.passed)) +
      '</span></div>';

    /* Said in words, always, and loudest in the case that looks calmest. A list
     * of grey "unverified" chips is the most misreadable thing on this screen:
     * it is orderly, it is not red, and it means nothing has been checked. */
    var note = counts.failed > 0
      ? 'A machine found a problem with ' + counts.failed +
        (counts.failed === 1 ? ' change' : ' changes') + '. Nothing here is a sign-off either way: ' +
        'what a machine found and what a person decided are separate facts and are shown separately.'
      : counts.passed === 0
        ? 'Nothing in this project has been checked by a machine. Every change below is ' +
          'unverified — which is the honest state, not a clean result.'
        : 'What a machine found and what a person decided are separate facts. A passing check is ' +
          'not an approval, and an approval is not a check.';

    if (unreadable) {
      head += problem(unreadable + (unreadable === 1 ? ' file was' : ' files were') +
        ' left out of these counts because its history could not be read. The numbers below are ' +
        'therefore incomplete.');
    }

    el.innerHTML = head + '<p class="wbnote">' + esc(note) + '</p>' +
      '<ol class="wbchecks">' + rows.map(function (r) {
        return '<li class="wbcheck">' +
          '<button type="button" class="wbcheck-f" data-artifact="' + esc(r.artifactID) + '">' +
            esc(r.path) + ' <span class="wbver-n">v' + esc(String(r.v.version)) + '</span>' +
          '</button>' +
          '<div class="wbver-f">' +
            chip('v', r.v.verification_state, r.v.verification_note) +
            chip('d', r.v.human_disposition, r.v.dispositioned_by || '') +
          '</div>' +
          '<p class="wbver-u' + (r.v.usable ? ' ok' : '') + '">' + esc(r.v.usable_why) + '</p>' +
          '</li>';
      }).join('') + '</ol>';

    /* The path is a way back to the change itself: a person who sees a failure
     * here wants the diff, and hunting for the file again is the step that
     * makes them not bother. */
    Array.prototype.forEach.call(el.querySelectorAll('[data-artifact]'), function (b) {
      b.addEventListener('click', function () {
        state.selected = b.getAttribute('data-artifact');
        select('files');
      });
    });
  }

  function render() {
    if (state.panel === 'files') renderFiles();
    if (state.panel === 'checks') renderChecks();
  }

  /* ---- the tab strip ------------------------------------------------------ */

  function tabs() {
    return Array.prototype.slice.call(document.querySelectorAll('.stagetab'));
  }

  function select(id) {
    var stage = $('stage');
    if (!stage) return;
    var found = false;
    tabs().forEach(function (t) {
      var on = t.getAttribute('data-panel') === id;
      if (on) found = true;
      t.setAttribute('aria-selected', String(on));
      t.tabIndex = on ? 0 : -1;
      var panel = $('panel-' + t.getAttribute('data-panel'));
      if (panel) panel.classList.toggle('on', on);
      if (on) {
        var gloss = $('stagegloss');
        if (gloss) gloss.textContent = t.getAttribute('data-gloss') || '';
      }
    });
    if (!found) return;
    state.panel = id;
    stage.setAttribute('data-panel', id);

    /* Read on every activation rather than once. The alternative is a cache
     * with no invalidation for work done in another tab, by forgectl, or by a
     * goal running in the background — and a file list that is quietly one
     * session out of date is the partial listing again, wearing a hat. */
    if ((id === 'files' || id === 'checks') && state.projectID) load();
    else render();

    if (state.onPanel) state.onPanel(id);
  }

  /* Arrow keys move between tabs, which is what a tablist owes a keyboard user
   * (PRD AUD-06) and what browsers do not provide for a row of buttons. */
  function onTabKey(e) {
    var all = tabs();
    var at = all.indexOf(document.activeElement);
    if (at < 0) return;
    var to = -1;
    if (e.key === 'ArrowRight') to = (at + 1) % all.length;
    else if (e.key === 'ArrowLeft') to = (at - 1 + all.length) % all.length;
    else if (e.key === 'Home') to = 0;
    else if (e.key === 'End') to = all.length - 1;
    if (to < 0) return;
    e.preventDefault();
    all[to].focus();
    select(all[to].getAttribute('data-panel'));
  }

  var Stage = {
    /* mount wires the tabs. onPanel is called with the selected panel's id —
     * the workbench uses it to dock the voice surface, which must stay reachable
     * on every panel (PRD AUD-06, AUD-07) and cannot sit over a file list. */
    mount: function (opts) {
      if (state.mounted) return;
      var strip = document.querySelector('.stagetabs');
      if (!strip) return;
      state.onPanel = (opts && opts.onPanel) || null;
      tabs().forEach(function (t) {
        t.addEventListener('click', function () { select(t.getAttribute('data-panel')); });
      });
      strip.addEventListener('keydown', onTabKey);
      state.mounted = true;
      select('model');
    },

    setProject: function (id) {
      if (!id || id === state.projectID) return;
      state.projectID = id;
      state.files = [];
      state.selected = null;
      state.phase = 'idle';
      if (state.panel === 'files' || state.panel === 'checks') load();
    },

    /* changed() says something was written. Called when a turn records a
     * version, so the panel a person is looking at is not showing the state of
     * the project as it was before FORGE answered. */
    changed: function () {
      if (state.panel === 'files' || state.panel === 'checks') load();
      else state.phase = 'idle';
    },

    panel: function () { return state.panel; }
  };

  global.ForgeStage = Stage;
})(window);
