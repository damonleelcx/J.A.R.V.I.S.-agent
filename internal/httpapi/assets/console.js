/* FORGE console.
 *
 * Plain ES5-compatible JavaScript, served as a file. No framework and no build
 * step, for the same reason the stylesheet has none: this is the surface someone
 * opens when something is wrong, and it must not depend on a toolchain or a
 * request that can fail.
 *
 * Served rather than inlined because the CSP is `script-src 'self'` — see
 * docs/bugfix/2026-09-02-csp-blocked-inline-page-script.md, where inlining it
 * silently disabled two pages.
 */
(function () {
  'use strict';

  var state = { goals: [], projects: [], conversations: [], artifacts: [], selected: null, timer: null, everSignedIn: false };

  function $(id) { return document.getElementById(id); }

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  /* NotAuthenticated is thrown rather than rendered, so every caller does not
   * have to check for it. The one handler at the top turns it into a sign-in
   * form — which is the only useful response to it. */
  function NotAuthenticated() { this.name = 'NotAuthenticated'; }
  NotAuthenticated.prototype = Object.create(Error.prototype);

  function api(path, options) {
    return fetch(path, options || {}).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (body) {
        if (r.status === 401 || r.status === 403) {
          var code = (body && body.error && body.error.code) || '';
          if (code === 'NOT_AUTHENTICATED' || code === 'SESSION_EXPIRED' || code === 'SESSION_REVOKED') {
            throw new NotAuthenticated();
          }
        }
        if (!r.ok) {
          var e = (body && body.error) || {};
          throw new Error(e.message || ('Request failed with ' + r.status));
        }
        return body;
      });
    });
  }

  /* ---- sign in ---------------------------------------------------------- */

  /* The console is a long-lived page. A session that expires while it is open
   * must not leave it showing stale data or an unexplained error: it swaps to a
   * sign-in form in place, and signing back in resumes exactly where the reader
   * was, including the selected goal. */
  function showSignIn(message) {
    if (state.timer) { clearInterval(state.timer); state.timer = null; }
    $('signin').classList.remove('hidden');
    $('main').classList.add('hidden');
    $('err').classList.add('hidden');
    var note = $('signin-note');
    if (message) { note.textContent = message; note.classList.remove('hidden'); }
    else { note.classList.add('hidden'); }
    var email = $('email');
    if (email) email.focus();
  }

  function hideSignIn() {
    $('signin').classList.add('hidden');
    $('main').classList.remove('hidden');
  }

  function submitSignIn(ev) {
    ev.preventDefault();
    var btn = $('signin-go');
    btn.disabled = true;
    btn.textContent = 'Signing in…';

    fetch('/v1/auth/sign-in', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ email: $('email').value, password: $('password').value })
    }).then(function (r) {
      return r.json().catch(function () { return {}; }).then(function (b) {
        if (!r.ok) {
          var e = (b && b.error) || {};
          throw new Error(e.message || 'Sign-in failed.');
        }
        return b;
      });
    }).then(function (b) {
      $('password').value = '';
      $('whoami').textContent = (b.user && b.user.email) || '';

      /* Go back where the person came from, if they came from somewhere.
       *
       * The workbench sends people here to sign in and says it will bring them
       * back. Without this they arrive at the operations console instead — a
       * different surface, for a different job — and have to find their own way
       * to the thing they were trying to use.
       *
       * ‼️ ONLY a same-origin absolute PATH is accepted. `next` arrives in the
       * URL, so anyone can put anything in it: without this check a link to
       * /console?next=https://elsewhere.example would sign a person in here and
       * then hand them to an attacker's page wearing our flow. A leading `//`
       * is rejected too — the browser reads //host/path as protocol-relative
       * and would leave the origin. */
      var next = new URLSearchParams(location.search).get('next');
      if (next && next.charAt(0) === '/' && next.charAt(1) !== '/') {
        location.assign(next);
        return;
      }
      hideSignIn();
      return refresh().then(startPolling);
    }).catch(function (err) {
      var note = $('signin-note');
      note.textContent = err.message;
      note.className = 'note bad';
    }).then(function () {
      btn.disabled = false;
      btn.textContent = 'Sign in';
    });
  }

  /* Relative time, because "3 minutes ago" is what a reader actually wants from
   * a timeline. Absolute time stays in the title attribute for when it matters. */
  function ago(iso) {
    var then = new Date(iso).getTime();
    if (isNaN(then)) return '';
    var s = Math.max(0, Math.floor((Date.now() - then) / 1000));
    if (s < 60) return s + 's ago';
    if (s < 3600) return Math.floor(s / 60) + 'm ago';
    if (s < 86400) return Math.floor(s / 3600) + 'h ago';
    return Math.floor(s / 86400) + 'd ago';
  }

  /* The sigil is fetched from the server rather than drawn here, so there is one
   * implementation of FORGE's mark and one place the state rules live. */
  function sigil(stateName, size) {
    return '<img class="sig" src="/v1/meta/sigil?state=' + encodeURIComponent(stateName) +
           '&size=' + size + '" width="' + size + '" height="' + size +
           '" alt="" aria-hidden="true">';
  }

  /* ---- your projects ---------------------------------------------------- */

  /* # Why this is here and not on the workbench
   *
   * Membership is the single authorisation path in this build, so "which
   * projects am I in, and as what" is the first question somebody opening this
   * page has — and it was answerable only from a terminal. The workbench is one
   * conversation about one project; this is the view of what exists, which is
   * where a list of them belongs.
   *
   * Each row carries the DOMAIN and its ceiling, not just a name. "Which of
   * these is the one I want" is answered badly by four names and well by four
   * names with what each is for.
   */
  function renderProjects() {
    var el = $('projects');
    if (!state.projects.length) {
      el.innerHTML = '<div class="empty">You are not in any projects yet.<br><br>' +
        'One is created with your first goal:<br>' +
        '<code style="font-size:12px">forgectl goal new --industry …</code></div>';
      return;
    }
    el.innerHTML = state.projects.map(function (p) {
      var bits = [p.role];
      if (p.unrecognised_pack) {
        /* Said rather than left blank. A project whose domain this build does
         * not recognise selects NO rules, and a blank would read as one that
         * simply had not been given a domain. */
        bits.push('<b style="color:var(--warn)">domain not recognised</b>');
      } else if (p.industry) {
        bits.push(esc(p.industry) + ' · ceiling ' + esc(p.ceiling));
      }
      /* A link, not a button with a handler. It navigates, the address is
       * visible on hover, and it opens in a new tab the way a person expects a
       * link to — none of which a scripted click gives for free. */
      return '<a class="goal" href="/workbench?project=' + encodeURIComponent(p.id) + '">' +
        '<div><div class="t">' + esc(p.name) + '</div>' +
        '<div class="m">' + bits.join(' · ') + '</div></div></a>';
    }).join('');
  }

  function loadProjects() {
    return api('/v1/projects').then(function (b) {
      state.projects = b.projects || [];
      renderProjects();
    });
  }

  /* ---- conversations ----------------------------------------------------- */

  /* What was said, and how to get back into it.
   *
   * # Why this panel exists
   *
   * Every turn has been durable since the workbench record shipped, and the only
   * route back to one was a key in a single browser's localStorage: the
   * workbench reopened the LAST conversation and nothing could ask for the
   * others. A new browser, a new profile or a closed private window reached none
   * of them while every turn sat in the table. The record was never the problem;
   * being able to ASK for it was.
   *
   * # Why the first line is the label
   *
   * A conversation has no title. Giving it one would mean asking a person to
   * name every thread they start, or asking a model to — which files a guess in
   * a permanent record. The first thing the person said is what they came to do,
   * in their words, and it is read back rather than written, so it cannot drift. */
  function renderConversations() {
    var el = $('conversations');
    if (!state.conversations.length) {
      el.innerHTML = '<div class="empty">Nothing said yet.<br><br>' +
        'Talk to FORGE at the <a href="/workbench">workbench</a> and it will be here.</div>';
      return;
    }
    el.innerHTML = state.conversations.map(function (c) {
      var bits = [c.turns + (c.turns === 1 ? ' turn' : ' turns'), when(c.last_at)];
      if (c.project_id) {
        var p = state.projects.filter(function (x) { return x.id === c.project_id; })[0];
        if (p) bits.push(esc(p.name));
      }
      /* Opens the workbench ON this conversation rather than merely opening the
       * workbench, which would drop the person into whichever one their browser
       * happened to remember — the exact behaviour this panel exists to fix. */
      return '<a class="goal" href="/workbench?conversation=' + encodeURIComponent(c.id) + '">' +
        '<div><div class="t">' + esc(c.opening || 'Untitled conversation') + '</div>' +
        '<div class="m">' + bits.join(' · ') + '</div></div></a>';
    }).join('');
  }

  function loadConversations() {
    return api('/v1/conversations').then(function (b) {
      state.conversations = b.conversations || [];
      renderConversations();
    });
  }

  /* ---- artifacts ---------------------------------------------------------- */

  /* What was built, across every project this person is in.
   *
   * Reuses GET /v1/geometry?project_id= rather than adding an endpoint: the
   * projects are already listed here, and a variant is already addressable by
   * project. One request per project is acceptable for a page with a handful of
   * them and is honest about what it costs; if a deployment ever has enough
   * projects for that to hurt, the fix is one endpoint that spans them, not a
   * cache here. */
  function renderArtifacts() {
    var el = $('artifacts');
    if (!state.artifacts.length) {
      el.innerHTML = '<div class="empty">Nothing built yet.<br><br>' +
        'Describe something at the <a href="/workbench">workbench</a> and the shape will be here.</div>';
      return;
    }
    el.innerHTML = state.artifacts.map(function (v) {
      var bits = ['v' + v.version];
      if (v.units) bits.push(esc(v.units));
      /* Said plainly, and not softened. An unverified shape that lists like a
       * checked one is the one thing this panel must not do. */
      bits.push(v.verification === 'verified' ? 'verified' :
        '<span style="color:var(--warn)">' + esc(v.verification || 'unverified') + '</span>');
      bits.push(when(v.created_at));
      var name = v.name || v.path || v.version_id;
      return '<a class="goal" href="/workbench?project=' + encodeURIComponent(v.project_id) + '">' +
        '<div><div class="t">' + esc(name) + '</div>' +
        '<div class="m">' + bits.join(' · ') + '</div></div></a>';
    }).join('');
  }

  function loadArtifacts() {
    /* Depends on the projects already being loaded — see refresh(). A project
     * whose variants cannot be read does not fail the panel: the others are
     * still worth showing, and a console that goes blank because one project
     * errored tells the reader less than one that shows what it could. */
    var projects = state.projects || [];
    if (!projects.length) { state.artifacts = []; renderArtifacts(); return Promise.resolve(); }
    return Promise.all(projects.map(function (p) {
      return api('/v1/geometry?project_id=' + encodeURIComponent(p.id))
        .then(function (b) { return b.variants || []; })
        .catch(function () { return []; });
    })).then(function (lists) {
      var all = [];
      lists.forEach(function (l) { all = all.concat(l); });
      all.sort(function (a, b) { return String(b.created_at).localeCompare(String(a.created_at)); });
      state.artifacts = all;
      renderArtifacts();
    });
  }

  /* A date a person can read, in en-US, and never "just now" for something that
   * is not. Locale is pinned rather than taken from the browser so that a
   * timestamp means the same thing in a screenshot as it does on the screen. */
  function when(iso) {
    if (!iso) return 'unknown time';
    var d = new Date(iso);
    if (isNaN(d.getTime())) return esc(iso);
    return d.toLocaleString('en-US', {
      year: 'numeric', month: 'short', day: 'numeric',
      hour: '2-digit', minute: '2-digit'
    });
  }

  /* ---- goals ------------------------------------------------------------ */

  function renderGoals() {
    var el = $('goals');
    if (!state.goals.length) {
      el.innerHTML = '<div class="empty">No goals yet.<br><br>' +
        'Create one from a terminal:<br><code style="font-size:12px">forgectl goal new --owner …</code></div>';
      return;
    }
    el.innerHTML = state.goals.map(function (g) {
      var bits = [g.tasks_done + '/' + g.tasks_total + ' done'];
      if (g.pending_approvals > 0) bits.push('<b style="color:var(--warn)">' + g.pending_approvals + ' waiting for you</b>');
      if (g.tasks_failed > 0) bits.push('<span style="color:var(--bad)">' + g.tasks_failed + ' failed</span>');
      return '<div class="goal" role="button" tabindex="0" data-id="' + esc(g.id) + '"' +
             ' aria-current="' + (state.selected === g.id) + '">' +
             sigil(g.avatar_state, 26) +
             '<div><div class="t">' + esc(g.title) + '</div>' +
             '<div class="m">' + esc(g.state_label) + ' · ' + bits.join(' · ') + '</div></div></div>';
    }).join('');

    Array.prototype.forEach.call(el.querySelectorAll('.goal'), function (node) {
      function open() { select(node.getAttribute('data-id')); }
      node.addEventListener('click', open);
      node.addEventListener('keydown', function (ev) {
        if (ev.key === 'Enter' || ev.key === ' ') { ev.preventDefault(); open(); }
      });
    });
  }

  function select(goalID) {
    state.selected = goalID;
    renderGoals();
    $('detail').classList.remove('hidden');
    loadDetail();
  }

  function loadGoals() {
    return api('/v1/goals').then(function (b) {
      state.goals = b.goals || [];
      if (!state.selected && state.goals.length) state.selected = state.goals[0].id;
      renderGoals();
      if (state.selected) { $('detail').classList.remove('hidden'); return loadDetail(); }
    });
  }

  /* ---- detail ----------------------------------------------------------- */

  function loadDetail() {
    if (!state.selected) return Promise.resolve();
    var id = state.selected;
    return Promise.all([
      api('/v1/goals/' + encodeURIComponent(id)),
      api('/v1/goals/' + encodeURIComponent(id) + '/timeline')
    ]).then(function (res) {
      if (state.selected !== id) return; // the user moved on mid-request
      renderDetail(res[0].goal, res[0].tasks || [], res[1].events || []);
    }).catch(function (err) {
      $('detail').innerHTML = '<div class="card"><div class="empty">' + esc(err.message) + '</div></div>';
    });
  }

  function renderDetail(goal, tasks, events) {
    var head =
      '<div class="card"><div class="forge-presence" style="margin-bottom:14px">' +
      '<span class="forge-portrait" style="width:64px;height:64px">' +
      '<img src="/v1/meta/portrait?state=' + encodeURIComponent(goal.avatar_state) +
      '" alt="" aria-hidden="true" width="64" height="64">' +
      '<span class="forge-portrait__badge" style="width:24px;height:24px">' +
      sigil(goal.avatar_state, 20) + '</span></span>' +
      '<div><div style="font-size:17px;font-weight:650">' + esc(goal.title) + '</div>' +
      '<div class="m" style="color:var(--ink-dim);font-size:13px;margin-top:3px">' +
      esc(goal.state_label) + ' · ' + esc(goal.status) + '</div></div></div>' +
      '<p class="stmt">' + esc(goal.statement) + '</p>' +
      '<div class="meta">' +
      '<span><b>' + goal.tasks_done + '</b> / ' + goal.tasks_total + ' tasks done</span>' +
      '<span><b>' + goal.tokens_spent.toLocaleString('en-US') + '</b> tokens</span>' +
      '<span>autonomy <b>' + esc(goal.autonomy) + '</b></span>' +
      '<span>ceiling <b>' + esc(goal.risk_tier) + '</b></span>' +
      (goal.outcome_summary ? '<span>' + esc(goal.outcome_summary) + '</span>' : '') +
      '</div></div>';

    var taskRows = tasks.map(function (t) {
      /* Verified is shown as its own tag, never folded into the status. A task
       * that succeeded without verification must not read as one that was
       * checked — that distinction is the engine's whole point. */
      var tag = '';
      if (t.verified) tag = '<span class="tag ok">verified</span>';
      else if (t.status === 'succeeded' && t.verification_required) tag = '<span class="tag warn">unverified</span>';
      else if (t.status === 'succeeded') tag = '<span class="tag">check not required</span>';
      else if (t.requires_approval) tag = '<span class="tag warn">needs approval</span>';

      return '<div class="task"><span class="st st-' + esc(t.status) + '">' + esc(t.status) + '</span>' +
        '<div>' + esc(t.title) +
        (t.error_code ? '<div class="err">' + esc(t.error_code) + ': ' + esc(t.error_detail) + '</div>' : '') +
        '</div>' + tag + '</div>';
    }).join('');

    var timeline = events.map(function (e) {
      return '<div class="event"><div class="who who-' + esc(e.actor) + '">' + esc(e.actor) + '</div>' +
        '<div><div class="k">' + esc(e.kind) + ' · <span title="' + esc(e.created_at) + '">' +
        ago(e.created_at) + '</span></div>' +
        '<div class="s">' + esc(e.summary) + '</div></div></div>';
    }).join('');

    $('detail').innerHTML = head +
      '<div class="card"><h2>Tasks</h2>' + (taskRows || '<div class="empty">No tasks yet.</div>') + '</div>' +
      '<div class="card"><h2>Timeline</h2>' + (timeline || '<div class="empty">Nothing has happened yet.</div>') + '</div>';
  }

  /* ---- approvals -------------------------------------------------------- */

  function loadApprovals() {
    return api('/v1/approvals').then(function (b) {
      var list = b.approvals || [];
      var el = $('approvals');
      if (!list.length) {
        el.innerHTML = '<div class="empty">Nothing is waiting on you.</div>';
        return;
      }
      el.innerHTML = list.map(function (a) {
        return '<div class="approval" data-id="' + esc(a.id) + '">' +
          '<div class="tier">' + esc(a.risk_tier) + ' · ' + esc(a.goal_title) + '</div>' +
          '<div style="margin-top:7px;font-size:13px;line-height:1.5">' + esc(a.summary) + '</div>' +
          '<pre>' + esc(JSON.stringify(a.preview, null, 2)) + '</pre>' +
          '<div class="acts">' +
          '<button class="btn-sm go" data-act="approve">Approve</button>' +
          '<button class="btn-sm no" data-act="reject">Reject</button>' +
          '</div><div class="note hidden"></div></div>';
      }).join('');

      Array.prototype.forEach.call(el.querySelectorAll('.approval'), function (card) {
        Array.prototype.forEach.call(card.querySelectorAll('[data-act]'), function (btn) {
          btn.addEventListener('click', function () { decide(card, btn.getAttribute('data-act')); });
        });
      });
    });
  }

  function decide(card, action) {
    var id = card.getAttribute('data-id');
    var buttons = card.querySelectorAll('button');
    Array.prototype.forEach.call(buttons, function (b) { b.disabled = true; });

    var reason = action === 'reject'
      ? (window.prompt('Why are you rejecting this? (recorded in the audit trail)') || '')
      : (window.prompt('Any note for the record? (optional)') || '');

    api('/v1/approvals/' + encodeURIComponent(id), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ decision: action, reason: reason })
    }).then(function (b) {
      var note = card.querySelector('.note');
      note.className = 'note ok';
      note.textContent = b.message;
      card.querySelector('.acts').classList.add('hidden');
      return refresh();
    }).catch(function (err) {
      var note = card.querySelector('.note');
      note.className = 'note bad';
      note.textContent = err.message;
      Array.prototype.forEach.call(buttons, function (b) { b.disabled = false; });
    });
  }

  /* ---- polling ---------------------------------------------------------- */

  function refresh() {
    /* Artifacts are read per project, so the projects have to exist first.
     * Everything else is independent and runs together. */
    return Promise.all([
      loadProjects().then(loadArtifacts),
      loadConversations(),
      loadGoals(),
      loadApprovals()
    ]).then(function () {
      $('err').classList.add('hidden');
    }).catch(function (err) {
      if (err instanceof NotAuthenticated) {
        showSignIn(state.everSignedIn ? 'Your session ended. Sign in to continue.' : '');
        return;
      }
      $('err').textContent = err.message;
      $('err').classList.remove('hidden');
    });
  }

  /* Polling rather than a stream. A long-running agent's console is left open
   * for hours; an SSE connection that silently dies leaves a page showing stale
   * state that looks live, which is worse than a page that is visibly a few
   * seconds behind. Polling fails loudly and recovers by itself.
   *
   * Paused while the tab is hidden, so a console left open overnight is not
   * still querying every three seconds in the morning. */
  function startPolling() {
    if (state.timer) clearInterval(state.timer);
    state.timer = setInterval(function () {
      if (!document.hidden) refresh();
    }, 3000);
  }

  document.addEventListener('visibilitychange', function () {
    if (!document.hidden) refresh();
  });

  var form = $('signin-form');
  if (form) form.addEventListener('submit', submitSignIn);

  /* Establish who we are before painting. A console that renders empty panels
   * and then swaps to a sign-in form looks broken for the moment in between. */
  api('/v1/auth/me').then(function (b) {
    state.everSignedIn = true;
    $('whoami').textContent = (b.user && b.user.email) || '';
    hideSignIn();
    return refresh().then(startPolling);
  }).catch(function (err) {
    if (err instanceof NotAuthenticated) { showSignIn(''); return; }
    $('err').textContent = err.message;
    $('err').classList.remove('hidden');
  });
})();
