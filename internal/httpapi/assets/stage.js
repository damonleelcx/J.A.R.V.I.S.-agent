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
    graph: null,     // {nodes, edges} — what the Diagram draws
    focus: null,     // node id whose relations the Diagram is showing alone
    phase: 'idle',   // idle | loading | ready | failed
    error: null,
    /* Turns measured this session, oldest first. Held in the page and nowhere
     * else, because nothing in this build stores them: the server logs each
     * turn's timings and has no endpoint that reads them back. Labelled as a
     * session in the panel rather than presented as the project's history. */
    turns: [],
    models: null,    // GET /v1/meta/models, read once
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
      state.graph = null;
      render();
      return Promise.resolve();
    }
    state.phase = 'loading';
    state.error = null;
    render();

    var projectAtStart = state.projectID;
    return get('/v1/workspace/graph?project_id=' + encodeURIComponent(projectAtStart))
      .then(function (g) {
        /* Kept, not discarded. The Diagram draws this graph and Files reads the
         * artifacts out of it; fetching it twice would let one panel show a node
         * the other has never heard of. */
        state.graph = { nodes: g.nodes || [], edges: g.edges || [] };
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

  /* ---- the Diagram panel --------------------------------------------------
   *
   * WRK-03's project graph, drawn. WRK-01 asks the shared canvas to carry
   * diagrams; this is the one diagram the system can draw from what it knows,
   * rather than one somebody would have to author separately and keep in step.
   *
   * # Boxes in HTML, lines in SVG
   *
   * The nodes are buttons, absolutely positioned, over an SVG that draws only
   * the edges. Text in SVG neither wraps nor ellipsises, so a title in a <text>
   * element has to be truncated by counting characters and guessing at the font
   * — and the guess is wrong for every proportional face. Buttons also give
   * keyboard focus and hit-testing for nothing, which PRD AUD-06 would otherwise
   * make me build.
   *
   * # The layout is deterministic, and that is the point
   *
   * No force simulation. A physics layout puts the same graph in a different
   * place every time it is opened, which makes "has anything changed?" — the
   * question somebody actually brings to a diagram — unanswerable by looking.
   * Columns come from the vocabulary's own grouping; order within a column is
   * kind then name. Same graph, same picture, every time.
   */

  var DIA = { colW: 178, colGap: 62, nodeH: 58, nodeGap: 12, pad: 16, headH: 30 };

  /* The columns, read off the page. Declared in Go beside the node vocabulary
   * (stageNodeRoles) so that a kind added there cannot be drawn nowhere here. */
  function diagramRoles() {
    var el = $('diagram-roles');
    if (!el) return [];
    return Array.prototype.map.call(el.querySelectorAll('[data-role]'), function (i) {
      return {
        id: i.getAttribute('data-role'),
        label: i.getAttribute('data-label') || '',
        kinds: (i.getAttribute('data-kinds') || '').split(/\s+/).filter(Boolean)
      };
    });
  }

  /* Nodes into columns.
   *
   * A kind that matches no column lands in one called "not in any column"
   * rather than being dropped. The fence in Go should make that impossible; if
   * it ever happens anyway, a visibly odd column is a bug somebody reports and a
   * missing node is a bug nobody sees. */
  function diagramColumns(nodes) {
    var roles = diagramRoles();
    var cols = roles.map(function (r) { return { role: r, nodes: [] }; });
    var spare = { role: { id: 'other', label: 'Not in any column', kinds: [] }, nodes: [] };

    nodes.forEach(function (n) {
      for (var i = 0; i < roles.length; i++) {
        if (roles[i].kinds.indexOf(n.kind) >= 0) { cols[i].nodes.push(n); return; }
      }
      spare.nodes.push(n);
    });
    if (spare.nodes.length) cols.push(spare);

    cols = cols.filter(function (c) { return c.nodes.length > 0; });
    cols.forEach(function (c, ci) {
      c.x = DIA.pad + ci * (DIA.colW + DIA.colGap);
      c.nodes.sort(function (a, b) {
        if (a.kind !== b.kind) return a.kind < b.kind ? -1 : 1;
        var an = nodeLabel(a), bn = nodeLabel(b);
        return an < bn ? -1 : an > bn ? 1 : 0;
      });
      c.nodes.forEach(function (n, ni) {
        n._x = c.x;
        n._y = DIA.pad + DIA.headH + ni * (DIA.nodeH + DIA.nodeGap);
        n._role = c.role.id;
      });
    });
    return cols;
  }

  /* The server's own label. Title is what was STORED, and it is empty for a node
   * whose content lives in another table — a decision, an owner. Composing a
   * name here would be a second producer of the string the edge sentences are
   * already built with, and the two would eventually disagree about what the
   * same node is called. */
  function nodeLabel(n) { return n.label || n.title || n.id; }

  function edgePath(a, b) {
    var ay = a._y + DIA.nodeH / 2, by = b._y + DIA.nodeH / 2;
    var ax, bx, dx;
    if (b._x === a._x) {
      /* Within one column: bow out to the right rather than draw a straight
       * line through every box between them. */
      ax = a._x + DIA.colW; bx = b._x + DIA.colW;
      var bow = 30 + Math.abs(by - ay) / 3;
      return 'M' + ax + ',' + ay + ' C' + (ax + bow) + ',' + ay + ' ' +
        (bx + bow) + ',' + by + ' ' + bx + ',' + by;
    }
    if (b._x < a._x) { ax = a._x; bx = b._x + DIA.colW; }
    else { ax = a._x + DIA.colW; bx = b._x; }
    dx = (bx - ax) / 2;
    return 'M' + ax + ',' + ay + ' C' + (ax + dx) + ',' + ay + ' ' +
      (bx - dx) + ',' + by + ' ' + bx + ',' + by;
  }

  function renderDiagram() {
    var el = $('diagram-body');
    if (!el) return;

    if (state.phase === 'loading') { el.innerHTML = empty('Reading the project…'); return; }
    if (state.phase === 'failed') {
      el.innerHTML = problem('The project could not be read: ' + state.error);
      return;
    }
    if (!state.projectID) {
      el.innerHTML = empty('No project yet. The first thing you describe creates one, ' +
        'and what it is made of appears here.');
      return;
    }
    var g = state.graph;
    if (!g) { el.innerHTML = empty('Nothing read yet.'); return; }
    if (!g.nodes.length) {
      el.innerHTML = empty('This project has no recorded requirements, components, risks or ' +
        'files yet. There is nothing to draw — which is different from a drawing that is empty.');
      return;
    }

    var cols = diagramColumns(g.nodes);
    var by = {};
    g.nodes.forEach(function (n) { by[n.id] = n; });

    /* An edge whose endpoint is not in this project's node list cannot be drawn
     * anywhere truthful. Counted and reported rather than skipped in silence:
     * a picture missing a relation is a picture that answers "what is connected
     * to what" incorrectly. */
    var drawn = [], dangling = 0;
    g.edges.forEach(function (e) {
      if (by[e.from_id] && by[e.to_id]) drawn.push(e); else dangling++;
    });

    var rows = cols.reduce(function (m, c) { return Math.max(m, c.nodes.length); }, 0);
    var w = DIA.pad * 2 + cols.length * DIA.colW + (cols.length - 1) * DIA.colGap;
    var h = DIA.pad * 2 + DIA.headH + rows * (DIA.nodeH + DIA.nodeGap);

    var svg = '<svg class="wbedges" width="' + w + '" height="' + h + '" aria-hidden="true">' +
      '<defs><marker id="wbarrow" viewBox="0 0 8 8" refX="7" refY="4" markerWidth="7" ' +
      'markerHeight="7" orient="auto-start-reverse">' +
      '<path class="wbarrowhead" d="M0,0 L8,4 L0,8 z"></path></marker></defs>' +
      drawn.map(function (e) {
        var on = state.focus && (e.from_id === state.focus || e.to_id === state.focus);
        return '<path class="wbe' + (state.focus ? (on ? ' on' : ' off') : '') + '" d="' +
          edgePath(by[e.from_id], by[e.to_id]) + '" marker-end="url(#wbarrow)"></path>';
      }).join('') + '</svg>';

    var boxes = cols.map(function (c) {
      return '<div class="wbcolhead" style="left:' + c.x + 'px;top:' + DIA.pad + 'px;width:' +
          DIA.colW + 'px">' + esc(c.role.label) + '</div>' +
        c.nodes.map(function (n) {
          var on = !state.focus || n.id === state.focus ||
            drawn.some(function (e) {
              return (e.from_id === state.focus && e.to_id === n.id) ||
                     (e.to_id === state.focus && e.from_id === n.id);
            });
          return '<button type="button" class="wbn wbn-' + esc(n._role) +
            (state.focus === n.id ? ' sel' : '') + (on ? '' : ' off') + '"' +
            ' data-node="' + esc(n.id) + '"' +
            ' aria-pressed="' + (state.focus === n.id) + '"' +
            /* The box clamps to two lines, so a long name — and every anchored
             * node's name is "kind <identifier>" — is shown truncated. The whole
             * of it has to be reachable without opening something else. */
            ' title="' + esc(nodeLabel(n) + ' — ' + n.kind +
              (n.status ? ', ' + n.status : '')) + '"' +
            ' style="left:' + n._x + 'px;top:' + n._y + 'px;width:' + DIA.colW +
            'px;height:' + DIA.nodeH + 'px">' +
            '<span class="wbn-k">' + esc(n.kind) + '</span>' +
            '<span class="wbn-t">' + esc(nodeLabel(n)) + '</span>' +
            '</button>';
        }).join('');
    }).join('');

    var head = '<div class="wbsum">' +
      '<span class="wbsum-n">' + g.nodes.length + '</span> ' +
      (g.nodes.length === 1 ? 'node' : 'nodes') + ', ' +
      '<span class="wbsum-n">' + drawn.length + '</span> ' +
      (drawn.length === 1 ? 'relation' : 'relations') +
      '</div>';

    if (dangling) {
      head += problem(dangling + (dangling === 1 ? ' relation points' : ' relations point') +
        ' at a node that is not in this project. They are not drawn, and the picture is ' +
        'therefore incomplete.');
    }

    el.innerHTML = head +
      '<div class="wbdia" style="width:' + w + 'px;height:' + h + 'px">' + svg + boxes + '</div>' +
      relationsHTML(drawn, by);

    Array.prototype.forEach.call(el.querySelectorAll('[data-node]'), function (b) {
      b.addEventListener('click', function () {
        var id = b.getAttribute('data-node');
        state.focus = state.focus === id ? null : id;
        renderDiagram();
      });
    });
    var open = el.querySelector('[data-open-artifact]');
    if (open) {
      open.addEventListener('click', function () {
        state.selected = open.getAttribute('data-open-artifact');
        select('files');
      });
    }
  }

  /* The relations in words.
   *
   * Not a caption. Somebody who cannot see the picture gets the whole of it from
   * this list, which is what PRD AUD-06 asks for, and the sentences are the
   * server's own (EdgeDTO.reads) so the direction of each relation is stated by
   * the side that knows which directions are legal.
   *
   * The isolated nodes are here for a different reason: a node joined to nothing
   * is invisible as an absence. It looks exactly like a node whose relations are
   * off screen, and the question "what has nobody connected up yet" is one of
   * the two questions a project graph exists to answer. */
  function relationsHTML(edges, by) {
    var groups = {};
    edges.forEach(function (e) {
      if (state.focus && e.from_id !== state.focus && e.to_id !== state.focus) return;
      (groups[e.kind] = groups[e.kind] || []).push(e);
    });
    var kinds = Object.keys(groups).sort();

    var head = '';
    if (state.focus) {
      var n = by[state.focus];
      head = '<div class="wbrel-h">Relations of <b>' + esc(n ? nodeLabel(n) : state.focus) +
        '</b>' + (n && n.kind === 'artifact' && n.anchor_id
          ? ' <button type="button" class="wbopen" data-open-artifact="' + esc(n.anchor_id) +
            '">Open in Files</button>'
          : '') +
        '<span class="wbrel-c">Select it again to show all.</span></div>';
    } else {
      head = '<div class="wbrel-h">Every relation</div>';
    }

    var body = kinds.length
      ? kinds.map(function (k) {
          return '<div class="wbrel-k">' + esc(k.replace(/_/g, ' ')) + '</div><ul class="wbrel-l">' +
            groups[k].map(function (e) {
              return '<li>' + esc(e.reads) + (e.note ? ' <i>' + esc(e.note) + '</i>' : '') + '</li>';
            }).join('') + '</ul>';
        }).join('')
      : '<p class="wbnote">' + (state.focus
          ? 'Nothing is joined to this one.'
          : 'No relations have been recorded in this project. The nodes above are all there ' +
            'is; nothing says how they bear on each other.') + '</p>';

    /* Only when there ARE relations. With none recorded at all, the sentence
     * above already says so, and re-listing every node in the project under
     * "joined to nothing" is the same statement made a second time at length —
     * which reads as a second, worse problem. */
    var isolated = [];
    if (!state.focus && state.graph && edges.length) {
      var joined = {};
      edges.forEach(function (e) { joined[e.from_id] = joined[e.to_id] = true; });
      isolated = state.graph.nodes.filter(function (n) { return !joined[n.id]; });
    }
    var lonely = isolated.length
      ? '<div class="wbiso"><div class="wbiso-h">Joined to nothing (' + isolated.length + ')</div>' +
        '<ul class="wbrel-l">' + isolated.map(function (n) {
          return '<li>' + esc(nodeLabel(n)) + ' <i>' + esc(n.kind) + '</i></li>';
        }).join('') + '</ul>' +
        '<p class="wbnote">Nothing records how these bear on the rest of the project. That is a ' +
        'statement about the record, not about the work.</p></div>'
      : '';

    return '<div class="wbrel">' + head + body + lonely + '</div>';
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
    if (state.panel === 'diagram') renderDiagram();
    if (state.panel === 'telemetry') renderTelemetry();
  }

  /* ---- the Telemetry panel ------------------------------------------------
   *
   * PRD NFR-05 asks for observability across latency, model selection,
   * retrieval, plans, tool calls, renders, policy, approvals and failures.
   * AUD-02 puts a number on one of them.
   *
   * # Every figure here is measured, and says which clock measured it
   *
   * Two clocks report on the same turn and they answer different questions. The
   * server measures from the moment the request arrives to the moment it emits
   * the first speech token — the model's part. The browser measures from the
   * press of Send to the moment speech is actually audible — the person's part,
   * which additionally contains the network, the parse, and the browser starting
   * its speech synthesiser. Averaging them would produce a number that is
   * neither, so they are separate columns with separate names.
   *
   * # What this panel refuses to do
   *
   * AUD-02's threshold is end-of-utterance to first audio. Nothing here measures
   * end-of-utterance: the browser's clock starts at Send, and for a typed turn
   * there is no utterance at all. So the threshold is shown as CONTEXT beside
   * the measurement, and the panel does not mark a turn as passing or failing a
   * requirement it is not measuring. A tick against the wrong metric is worse
   * than no tick.
   */

  function median(xs) {
    if (!xs.length) return null;
    var v = xs.slice().sort(function (a, b) { return a - b; });
    var m = Math.floor(v.length / 2);
    return v.length % 2 ? v[m] : Math.round((v[m - 1] + v[m]) / 2);
  }

  function ms(v) { return v == null ? '—' : String(Math.round(v)) + 'ms'; }

  /* A measurement that does not exist renders as an em dash and a reason, never
   * as a zero. Zero is a number somebody will read as "instant", and a metric
   * nobody collected looks best of all when it is drawn as one. */
  function stat(label, value, note) {
    return '<div class="wbstat">' +
      '<div class="wbstat-n">' + esc(value) + '</div>' +
      '<div class="wbstat-l">' + esc(label) + '</div>' +
      (note ? '<div class="wbstat-w">' + esc(note) + '</div>' : '') +
      '</div>';
  }

  function renderTelemetry() {
    var el = $('telemetry-body');
    if (!el) return;

    var turns = state.turns;
    var served = turns.filter(function (t) { return t.serverFirstMS != null; })
      .map(function (t) { return t.serverFirstMS; });
    var audible = turns.filter(function (t) { return t.audioMS != null; })
      .map(function (t) { return t.audioMS; });
    var barges = turns.filter(function (t) { return t.bargeInMS != null; })
      .map(function (t) { return t.bargeInMS; });

    var head = '<div class="wbtel-h">This browser session</div>' +
      '<div class="wbtel-g">' +
      stat('turns measured', String(turns.length),
        turns.length ? 'since this tab was opened' : 'nothing has been asked yet') +
      stat('median to first token', ms(median(served)),
        served.length ? 'server clock, n=' + served.length : 'no turn reported one') +
      stat('median to first audio', ms(median(audible)),
        audible.length ? 'browser clock, n=' + audible.length
                       : 'no reply has been spoken aloud') +
      stat('fastest barge-in', ms(barges.length ? Math.min.apply(null, barges) : null),
        barges.length ? 'browser clock, n=' + barges.length : 'nobody has interrupted') +
      '</div>' +
      '<p class="wbnote">PRD AUD-02 names ≤700ms from end of utterance to first audio, and ' +
      '≤250ms to stop on interruption. Neither figure above is that measurement: the browser\'s ' +
      'clock starts when the turn is sent, not when you stopped speaking, and a typed turn has ' +
      'no utterance to end. The threshold is here for scale, not as a verdict.</p>';

    var models = '';
    if (state.models && state.models.error) {
      models = problem('The model roles could not be read: ' + state.models.error);
    } else if (state.models && !state.models.configured) {
      models = '<div class="wbtel-h">Models</div>' +
        '<p class="wbnote">No model is configured in this deployment. The workbench holds a ' +
        'conversation with nothing.</p>';
    } else if (state.models) {
      var r = state.models.roles || {};
      models = '<div class="wbtel-h">Models</div><div class="wbmeas">' +
        ['converse', 'planner', 'executor', 'verifier', 'summarizer', 'vision'].map(function (k) {
          return r[k] ? '<div class="wbmeas-k">' + esc(k) + '</div>' +
            '<div class="wbmeas-v">' + esc(r[k]) + '</div>' : '';
        }).join('') + '</div>' +
        /* SAF-03 asks for a verification method INDEPENDENT of the generative
         * path. Whether this deployment has one is a fact the server already
         * decides; showing it here rather than deciding it again. */
        (state.models.verifier_independent
          ? '<p class="wbnote">The verifier is a different model from the one that writes ' +
            '(PRD SAF-03).</p>'
          : '<p class="wbnote wbwarn">The verifier is the SAME model that writes. A model ' +
            'checking its own work is not independent verification (PRD SAF-03).</p>');
    } else {
      models = '<div class="wbtel-h">Models</div>' + empty('Reading the model roles…');
    }

    var list = turns.length
      ? '<div class="wbtel-h">Every turn, newest first</div><ol class="wbturns">' +
        turns.slice().reverse().map(turnHTML).join('') + '</ol>'
      : '<div class="wbtel-h">Every turn, newest first</div>' +
        empty('Nothing has been asked yet this session. Ask FORGE something and the turn is ' +
          'timed here.');

    /* Named, not left blank. The half of NFR-05 this build does not collect is
     * the half somebody would otherwise assume was fine. */
    var missing = '<div class="wbunmeas"><div class="wbunmeas-h">Not measured here</div>' +
      '<ul class="wbrel-l">' +
      '<li><b>Anything before this tab was opened.</b> The server writes each turn\'s timings ' +
      'to its log and has no endpoint that reads them back, so there is no history to show ' +
      'and this panel empties on reload.</li>' +
      '<li><b>End of utterance to first audio</b>, which is the figure AUD-02 actually names. ' +
      'What is measured starts at Send.</li>' +
      '<li><b>Retrieval time.</b> A turn is marked as having quoted memory or not; how long ' +
      'that took is not separated from the rest of the turn.</li>' +
      '<li><b>Tool calls, plans, approvals and failures</b> (NFR-05). Those belong to goals, ' +
      'which run in the worker and report on their own timeline — see Operations, not here.</li>' +
      '<li><b>Anybody else\'s turns.</b> This is one browser session, not the deployment.</li>' +
      '</ul></div>';

    el.innerHTML = head + models + list + missing;
  }

  function turnHTML(t, i) {
    var marks = [];
    if (t.retrieval) marks.push('quoted memory');
    if (t.geometry) marks.push('produced geometry');
    if (t.failed) marks.push('failed');
    return '<li class="wbturn' + (t.failed ? ' bad' : '') + '">' +
      '<div class="wbturn-h">' +
        '<span class="wbver-n">' + esc(t.prompt || 'turn') + '</span>' +
        '<span class="wbver-w">' + esc(t.at) + '</span>' +
        (t.model ? '<span class="wbver-a">' + esc(t.model) + '</span>' : '') +
      '</div>' +
      '<div class="wbturn-m">' +
        '<span>first token ' + ms(t.serverFirstMS) + ' <i>server</i></span>' +
        '<span>first audio ' + ms(t.audioMS) + ' <i>browser</i></span>' +
        '<span>full reply ' + ms(t.serverTotalMS != null ? t.serverTotalMS : t.browserTotalMS) +
          ' <i>' + (t.serverTotalMS != null ? 'server' : 'browser') + '</i></span>' +
        (t.tokens ? '<span>' + esc(String(t.tokens)) + ' tokens</span>' : '') +
        (t.bargeInMS != null ? '<span>barge-in ' + ms(t.bargeInMS) + ' <i>browser</i></span>' : '') +
      '</div>' +
      (marks.length ? '<div class="wbturn-t">' + esc(marks.join(' · ')) + '</div>' : '') +
      '</li>';
  }

  /* Which panels are a reading of the project, and so must re-read it when they
   * are opened. Telemetry is not one of them: it is a measurement of this
   * browser session, and re-reading the project would tell it nothing. */
  var NEEDS_PROJECT = { files: true, checks: true, diagram: true };

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
    if (NEEDS_PROJECT[id] && state.projectID) load();
    else render();

    /* Telemetry names which model answered and whether the verifier is an
     * independent one (PRD SAF-03). Read once, on first sight of the panel:
     * it is a property of the deployment, not of the turn. */
    if (id === 'telemetry' && !state.models) {
      get('/v1/meta/models')
        .then(function (m) { state.models = m; if (state.panel === 'telemetry') renderTelemetry(); })
        .catch(function (e) {
          state.models = { error: e.message || 'the model roles could not be read' };
          if (state.panel === 'telemetry') renderTelemetry();
        });
    }

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
      state.graph = null;
      state.selected = null;
      state.focus = null;
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

    /* turn records what one exchange actually cost.
     *
     * Pushed by the workbench rather than measured here, because the two clocks
     * that matter both start there: the request goes out from that module and
     * the speech synthesiser reports to it. A telemetry panel that timed the
     * turn itself would be timing its own view of the turn.
     *
     * Nothing is derived on the way in. A field the workbench could not measure
     * arrives null and is rendered as an em dash — see stat(). */
    turn: function (rec) {
      if (!rec) return;
      state.turns.push(rec);
      if (state.panel === 'telemetry') renderTelemetry();
    },

    /* bargeIn attaches an interruption to the turn it interrupted. Its own
     * measurement, on the browser's clock, taken by the voice layer. */
    bargeIn: function (msTaken) {
      var last = state.turns[state.turns.length - 1];
      if (!last) return;
      last.bargeInMS = msTaken;
      if (state.panel === 'telemetry') renderTelemetry();
    },

    panel: function () { return state.panel; }
  };

  global.ForgeStage = Stage;
})(window);
