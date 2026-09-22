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

  var state = { goals: [], projects: [], conversations: [], artifacts: [], selected: null, timer: null, everSignedIn: false,
    /* The New goal form: whether a create is in flight, and the server's own
     * words for one that was refused. Held on state rather than read back off
     * the note element, so a poll that re-renders the panels cannot wipe a
     * refusal the person has not read yet. */
    creating: false, createError: '' };

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
          /* The sentence written for THIS refusal, not the error CODE's general
           * words (2026-09-22). `message` for a refused goal is "One or more
           * request fields failed validation."; what actually happened - "viewer
           * cannot goal.create here - a viewer reads. Ask an owner to change
           * your role." - is in details.detail. ForgeNewGoal.refusal holds the
           * rule, because the two forms that create a goal must read a refusal
           * the same way. Fence: TestNewGoalForm_ShowsTheServersOwnRefusal. */
          var err = new Error(window.ForgeNewGoal
            ? window.ForgeNewGoal.refusal(e, r.status)
            : (e.message || ('Request failed with ' + r.status)));
          err.status = r.status;
          throw err;
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
   * implementation of FORGE's mark and one place the state rules live.
   *
   * A placeholder rather than the markup, because these rows are built as
   * strings and assigned in one go: ForgeSigil.hydrate fills every slot in the
   * container once the assignment has happened. It must be inlined and not an
   * <img> — the mark's colours and state animation come from avatar.css, which
   * an SVG behind an <img> cannot see. See assets/sigil.js. */
  function sigil(stateName, size) {
    return window.ForgeSigil.slot(stateName, size, 'sig');
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
      renderNewGoal();
    });
  }

  /* ---- new goal (2026-09-22) ---------------------------------------------
   *
   * # Why this is here
   *
   * This page listed goals and could create none. Its empty state told the
   * reader to open a terminal - and the browser's ONLY route to POST /v1/goals
   * was the workbench's proposal card, which appears when FORGE happens to
   * propose work inside a conversation. So somebody who knew exactly what they
   * wanted done either had to talk her into offering it or leave the product.
   *
   * # What it is and is not
   *
   * The same four things `forgectl goal new` takes - a title, a statement, a
   * ceiling, and whether the statement is a BUILD - plus the project, which is
   * the one field the workbench's form does not need because a conversation
   * already has one.
   *
   * It DRAFTS and PLANS. Nothing runs: POST /v1/goals writes a draft and plans
   * it, and starting it is a separate deliberate act (PRD AGT-02) taken on the
   * goal's own detail pane. The button says "Plan it" for that reason.
   *
   * Every rule it checks before sending lives in assets/newgoal.js, shared with
   * the workbench's form, and every one of them is enforced again by the server.
   * The list of projects is the SERVER's answer to "where may this person plan
   * work" (can_create_goal from GET /v1/projects), never a permission matrix
   * copied into the browser.
   */
  function newGoalFields() {
    return {
      title: $('newgoal-title') ? $('newgoal-title').value : '',
      statement: $('newgoal-statement') ? $('newgoal-statement').value : '',
      risk_tier: $('newgoal-risk') ? $('newgoal-risk').value : 'r1',
      build: !!($('newgoal-build') && $('newgoal-build').checked),
      project_id: $('newgoal-project') ? $('newgoal-project').value : '',
      /* Never sent from this form. Every option in the project list is an
       * EXISTING project, and the server refuses an industry alongside a project
       * id rather than dropping it - the industry belongs to the project. A
       * project's first goal is made at the workbench or with forgectl, where
       * there is an industry to choose. */
      industry: ''
    };
  }

  function renderNewGoal() {
    var form = $('newgoal-form');
    if (!form || !window.ForgeNewGoal) return;
    var G = window.ForgeNewGoal;

    var risk = $('newgoal-risk');
    if (risk && !risk.options.length) {
      risk.innerHTML = G.TIERS.map(function (t) {
        return '<option value="' + esc(t.tier) + '"' + (t.tier === 'r1' ? ' selected' : '') + '>' +
          esc(t.tier) + ' — ' + esc(t.gloss) + '</option>';
      }).join('');
    }

    /* Only the projects this person may plan work in. A project they can READ
     * but not write is not offered, because offering it offers a refusal. */
    var mine = G.writable(state.projects);
    var pick = $('newgoal-project');
    if (pick) {
      var was = pick.value;
      pick.innerHTML = mine.map(function (p) {
        return '<option value="' + esc(p.id) + '">' + esc(p.name) +
          (p.industry ? ' — ' + esc(p.industry) : '') + '</option>';
      }).join('');
      if (was && mine.filter(function (p) { return p.id === was; }).length) pick.value = was;
    }

    var none = $('newgoal-none');
    var open = $('newgoal-open');
    if (!mine.length) {
      /* Not a generic error, and not a disabled button with no explanation: the
       * answer to "why can I not make a goal" is either that there is nowhere to
       * put one or that the role they hold does not plan work - and the role is
       * on every row of Your projects beside this. */
      if (none) {
        none.textContent = state.projects.length
          ? 'None of your projects lets you plan work. Creating a goal needs goal.create, which ' +
            'an owner, a maintainer or a contributor holds - your role is on each project above. ' +
            'Ask an owner to change it.'
          : 'You are not in any project yet, so there is nowhere to put a goal. Start one at the ' +
            'workbench, where a project is created with its industry, or run forgectl goal new.';
        none.classList.remove('hidden');
      }
      if (open) open.disabled = true;
      form.classList.add('hidden');
    } else {
      if (none) { none.textContent = ''; none.classList.add('hidden'); }
      if (open) open.disabled = false;
    }

    var f = newGoalFields();
    var why = G.check(f);
    if (!why && !f.project_id) why = 'choose the project this goal belongs to';
    var go = $('newgoal-go');
    if (go) go.disabled = !!(why || state.creating);
    var note = $('newgoal-why');
    if (note && !state.createError) {
      var typed = !!(f.title || f.statement);
      note.textContent = typed && why ? why : '';
      note.className = 'note bad' + (typed && why ? '' : ' hidden');
    }
  }

  function openNewGoal(yes) {
    var form = $('newgoal-form');
    var open = $('newgoal-open');
    if (!form || !open) return;
    form.classList.toggle('hidden', !yes);
    open.setAttribute('aria-expanded', String(!!yes));
    if (yes) {
      renderNewGoal();
      if ($('newgoal-title')) $('newgoal-title').focus();
    }
  }

  function submitNewGoal(ev) {
    if (ev) ev.preventDefault();
    var G = window.ForgeNewGoal;
    var f = newGoalFields();
    if (!G || G.check(f) || !f.project_id || state.creating) { renderNewGoal(); return; }

    state.creating = true;
    state.createError = '';
    var go = $('newgoal-go');
    var note = $('newgoal-why');
    if (go) { go.disabled = true; go.textContent = 'Planning…'; }
    if (note) {
      /* Planning is a model call of tens of seconds to minutes. Saying so is the
       * difference between a page that is working and one that looks stuck. */
      note.textContent = 'Drafting and planning it. This is a model call and takes a while; ' +
        'nothing runs until you start it.';
      note.className = 'note';
    }

    api('/v1/goals', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(G.body(f))
    }).then(function (b) {
      if ($('newgoal-title')) $('newgoal-title').value = '';
      if ($('newgoal-statement')) $('newgoal-statement').value = '';
      openNewGoal(false);
      if (note) { note.textContent = ''; note.className = 'note hidden'; }
      /* Straight to the goal that was just made, on the same pane every other
       * goal is read in: the plan, the tasks, and the button that starts it. A
       * goal created and then not shown is a goal somebody has to go and find. */
      return refresh().then(function () {
        if (b && b.goal && b.goal.id) select(b.goal.id);
      });
    }).catch(function (err) {
      if (err instanceof NotAuthenticated) {
        showSignIn('Your session ended. Sign in to continue.');
        return;
      }
      /* The server's own sentence, put where the person is looking rather than
       * in the page-wide error strip: the form is still open and still holds
       * what they typed. */
      state.createError = err.message;
      if (note) { note.textContent = err.message; note.className = 'note bad'; }
    }).then(function () {
      state.creating = false;
      if (go) go.textContent = 'Plan it';
      renderNewGoal();
    });
  }

  function initNewGoal() {
    var form = $('newgoal-form');
    var open = $('newgoal-open');
    if (!form || !open) return;
    open.addEventListener('click', function () { openNewGoal(form.classList.contains('hidden')); });
    var cancel = $('newgoal-cancel');
    if (cancel) cancel.addEventListener('click', function () {
      state.createError = '';
      openNewGoal(false);
      open.focus();
    });
    form.addEventListener('submit', submitNewGoal);
    ['newgoal-title', 'newgoal-statement', 'newgoal-risk', 'newgoal-build', 'newgoal-project']
      .forEach(function (id) {
        var el = $(id);
        if (!el) return;
        el.addEventListener('input', function () { state.createError = ''; renderNewGoal(); });
        el.addEventListener('change', function () { state.createError = ''; renderNewGoal(); });
      });
    renderNewGoal();
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
      /* ‼️ This used to say "Create one from a terminal:" and print a forgectl
       * line. It was the console admitting that the one thing this page is about
       * could not be started from it - and it was the only thing the empty state
       * said, so the answer to "how do I begin" was "you cannot, here". The form
       * above is the answer now; the terminal is still there for people who
       * prefer it and no longer the only door. */
      el.innerHTML = '<div class="empty">No goals yet.<br><br>' +
        'Use <b>New goal</b> above to define one, or run ' +
        '<code style="font-size:12px">forgectl goal new</code> from a terminal.</div>';
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
    window.ForgeSigil.hydrate(el);

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
    showDetail();
    loadDetail();
  }

  /* Reveal the reading pane and put the page into its two-column shape.
   *
   * The class goes on the layout rather than being inferred from the pane's own
   * visibility, so the grid has one switch instead of the stylesheet trying to
   * reason about a sibling's state. */
  function showDetail() {
    $('detail').classList.remove('hidden');
    $('main').classList.add('with-detail');
  }

  function loadGoals() {
    return api('/v1/goals').then(function (b) {
      state.goals = b.goals || [];
      if (!state.selected && state.goals.length) state.selected = state.goals[0].id;
      renderGoals();
      if (state.selected) { showDetail(); return loadDetail(); }
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
    window.ForgeSigil.hydrate($('detail'));
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
  initNewGoal();

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
