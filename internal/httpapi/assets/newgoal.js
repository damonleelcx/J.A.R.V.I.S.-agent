/* Defining a goal from the browser (2026-09-22).
 *
 * # Why this file exists at all
 *
 * POST /v1/goals has existed since the first wave, and until today the browser
 * reached it from exactly ONE place: the proposal card, after FORGE had offered
 * a goal inside a conversation. If she never offered one — and she only offers
 * one when the conversation happens to turn that way — there was no way to say
 * "here is the work, plan it". The operations console listed goals and could
 * create none; its empty state told the reader to open a terminal and run
 * `forgectl goal new`, which is the product admitting the interface cannot do
 * the thing the product is for.
 *
 * So there is now a short form in BOTH surfaces: the console, where somebody
 * picks which project the goal belongs to, and the workbench, where the project
 * is the conversation's and is stated rather than chosen.
 *
 * # Why the two forms share this file rather than each having their own
 *
 * Because what the form has to get right is not the markup. It is the set of
 * rules the SERVER holds, and there are five of them:
 *
 *   - a goal needs a title and a statement (agent.Intake.Draft),
 *   - the title is at most 200 characters and the statement at most 8000
 *     (maxGoalTitle / maxGoalStatement in goals_start.go),
 *   - the risk tier must be a recognised tier (engine.RiskTier.Valid),
 *   - an industry may NOT be sent together with a project id — the industry
 *     belongs to the project, and Draft refuses the pair rather than dropping
 *     one of them,
 *   - `goal.create` is checked against the NAMED project before anything is
 *     written, so the project id has to be sent and a project the caller is not
 *     in comes back NOT FOUND.
 *
 * Two copies of those five is one copy that goes stale. One copy, in one file,
 * read by one node fence, is the only version of this that stays true — see
 * TestNewGoalForm_* in internal/httpapi/new_goal_form_test.go.
 *
 * ‼️ And the browser's copy is a COURTESY, never the authority. Every rule above
 * is enforced again by the server on every request; this exists so that typing
 * nothing into the statement box disables the button instead of costing a round
 * trip, and so that the reason is in the form rather than in a console log.
 *
 * # What is deliberately NOT on the form
 *
 * Autonomy. It is fixed at creation and the database enforces it — migration
 * 0028_autonomy_is_write_once installs a trigger that refuses any write to the
 * column, because PRD AGT-04 ("never silently raises its own autonomy level")
 * had been resting on the ABSENCE of a code path. A form field for a value that
 * can never be changed afterwards, offered beside four that can, reads as one
 * more setting to revisit later. So the goal takes the level
 * `forgectl goal new` gives it, the form SAYS that it is set once, and raising
 * it is a decision made deliberately elsewhere.
 *
 * Tier r5. PRD §8.1 makes r5 prohibited — refused, not gated — so a ceiling of
 * r5 authorises nothing that r4 does not. `forgectl goal new --risk` offers
 * "r0 | r1 | r2 | r3 | r4" and this offers exactly the same five, so the two
 * surfaces cannot disagree about what a person may choose.
 *
 * # Why the field names are the SERVER's
 *
 * check() and body() both read `risk_tier` and `project_id`, not camel-cased
 * names of their own. One vocabulary, the wire's, so a reader can put the form,
 * this module, the request in the network panel and goals_start.go side by side
 * and compare them word for word. A translation layer in the middle is one more
 * place the industry/project rule could be got right here and wrong there.
 *
 * Plain ES5, served as a file, no build step: the same reason console.js has
 * none, and the CSP is `script-src 'self'` so it cannot be inlined either.
 */
(function () {
  'use strict';

  /* The tiers a person may choose, with what each one MEANS. A bare "r2" in a
   * dropdown is a number somebody guesses at; PRD §8.1's own gloss is the
   * difference between choosing a ceiling and choosing a letter. */
  var TIERS = [
    { tier: 'r0', gloss: 'discussion only, no project context' },
    { tier: 'r1', gloss: 'reversible draft inside a sandbox' },
    { tier: 'r2', gloss: 'consequential digital action: a merge, a baseline change, a costly run' },
    { tier: 'r3', gloss: 'release or operational preparation' },
    { tier: 'r4', gloss: 'safety-critical support' }
  ];

  /* goals_start.go's maxGoalTitle and maxGoalStatement. Named here so the
   * counter under each box and the refusal say the same number. */
  var MAX_TITLE = 200;
  var MAX_STATEMENT = 8000;

  function text(v) { return String(v == null ? '' : v).trim(); }

  function known(tier) {
    for (var i = 0; i < TIERS.length; i++) {
      if (TIERS[i].tier === tier) return true;
    }
    return false;
  }

  /* What the server would refuse this for, in the server's own words, or ''.
   *
   * The wording is copied from agent.Intake.Draft and CreateGoal deliberately:
   * a person who fixes the form and submits anyway must not be told two
   * different things by the two halves of the same rule. */
  function check(f) {
    f = f || {};
    var title = text(f.title), statement = text(f.statement);
    if (!title || !statement) {
      return 'a goal needs both a title and a statement of what to do';
    }
    if (title.length > MAX_TITLE || statement.length > MAX_STATEMENT) {
      return 'title must be at most ' + MAX_TITLE + ' characters and the statement at most ' + MAX_STATEMENT;
    }
    if (!known(text(f.risk_tier) || 'r1')) {
      return 'risk tier "' + text(f.risk_tier) + '" is not recognised';
    }
    /* Draft's rule, which body() below also makes unreachable by blanking the
     * industry whenever a project is named. Checked as well as prevented,
     * because a caller assembling the fields by hand is exactly who this
     * sentence is for. */
    if (f.project_id && text(f.industry)) {
      return 'an industry cannot be set while adding a goal to the existing project ' +
        f.project_id + ' — the industry belongs to the project, and changing it would ' +
        'change the rules under which its earlier work was done';
    }
    return '';
  }

  /* Whether the submit button is enabled. One function, so the button's state
   * and the reason shown beside it cannot disagree. */
  function ready(f) { return check(f) === ''; }

  /* The POST /v1/goals body, built in one place so what the fence reads is what
   * both forms send.
   *
   * ‼️ project_id is sent whenever there is one. This is the field whose absence
   * caused a goal drafted from the workbench to land in a BRAND NEW project
   * named after its own title — and it is also the field the permission check
   * is made against, so omitting it is both a filing bug and a missing
   * authorisation. See the long note at the /v1/goals call in workbench.js and
   * docs/bugfix/2026-09-15-a-goal-could-be-drafted-into-a-project-its-caller-was-not-in.md
   *
   * industry goes ONLY when there is no project yet: with a project id the
   * server refuses the pair rather than ignoring one.
   *
   * There is no autonomy field. See the header. */
  function body(f) {
    f = f || {};
    return {
      title: text(f.title),
      statement: text(f.statement),
      risk_tier: text(f.risk_tier) || 'r1',
      project_id: f.project_id || '',
      industry: f.project_id ? '' : text(f.industry),
      build: !!f.build
    };
  }

  /* A refused request, in the words written for THAT refusal.
   *
   * The envelope carries the error CODE's general words in `message` ("One or
   * more request fields failed validation.") and the sentence written for this
   * particular refusal in `details.detail` ("viewer cannot goal.create here — a
   * viewer reads. Ask an owner to change your role."). A form that showed only
   * `message` told a viewer that some field was wrong, which is not what
   * happened and not something they can act on. The detail, when there is one,
   * IS the answer. Same rule as refusalText in workbench.js. */
  function refusal(err, status) {
    err = err || {};
    var detail = err.details && typeof err.details.detail === 'string' ? err.details.detail.trim() : '';
    if (detail) return detail;
    if (err.message) return err.message + (err.remedy ? ' — ' + err.remedy : '');
    return 'The goal could not be created (' + (status || 0) + ').';
  }

  /* The projects a person may actually plan work in, out of GET /v1/projects.
   *
   * ‼️ Filtered on the SERVER's answer (can_create_goal), not by comparing the
   * role against a permission matrix copied into the browser. The matrix lives
   * in internal/domain/access/model.go and a second copy here is the copy that
   * would keep offering a project after somebody's role changed — or, worse,
   * stop offering one it should. A project whose entry predates the field is
   * omitted rather than assumed writable: the server refuses it anyway, and a
   * form that offers a refusal is a worse answer than one that says the list is
   * what it is. */
  function writable(projects) {
    return (projects || []).filter(function (p) { return p && p.can_create_goal === true; });
  }

  window.ForgeNewGoal = {
    TIERS: TIERS,
    MAX_TITLE: MAX_TITLE,
    MAX_STATEMENT: MAX_STATEMENT,
    check: check,
    ready: ready,
    body: body,
    refusal: refusal,
    writable: writable
  };
})();
