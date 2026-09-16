# A goal started from the proposal card landed in a new project

**Date:** 2026-09-15 · **Status:** fixed (follow-up to #107, found by checking the card in a browser) ·
**Severity:** medium — the work ran, and then was invisible where the person was looking

## Summary

The workbench's proposal card posts `POST /v1/goals` without a `project_id`. The conversation's project was
therefore never named, so `agent.Intake.Draft` created a **brand new project** for every goal started from the
card, named after the goal's title and filed under the `general` pack.

The industry the person chose was dropped on the same journey: the request already blanks `industry` whenever
`state.projectID` is set — because the server refuses the two together — so a conversation that had a project sent
**neither**, and the new project got the default pack rather than the declared one.

The fix sends `project_id: state.projectID || ''`. The industry line beside it was already written for this.

## Symptom

Seen in a real browser against a stand-in model (`docs/spikes/2026-09-15-card-checked`):

- A conversation in project `prj_01M2KWQNC11A1VBJ1KT7E0Y77J` ("card check") proposed a lamp. "Start this" →
  goal `gol_01M2KWYKFNWPZ7N8CES44N1A0J`, created into a **second** project
  `prj_01M2KWYKFENHFCSZHPY15V1S7Z`, named "Build a small desk lamp model (stand-in)", pack `general`.
- The build ran to `succeeded` and wrote two artifact versions — `geometry/desk-lamp-base.forge.json` v1 and
  `geometry/desk-lamp.forge.json` v1 — **into that second project**.
- The workbench's own **Files panel**, which reads the conversation's project, said
  *"This project has no files yet."* while those two versions existed.
- The operations console listed **both** projects for one account, the conversation filed under "card check"
  ("4 turns · card check") and the artifacts under the other.

## Impact

- The model a person just watched being built does not appear in the workbench panels of the conversation that
  built it: Files, Parts and the requirements graph all read `state.projectID`. The work is only findable through
  "Open in operations" or by knowing the goal id.
- A project accumulates per goal. An account that starts five builds from the card owns six projects.
- The industry chosen in the picker — which selects the **rules, validators and approval policy** applied to the
  work (PRD §7, SEC-07) — is silently replaced by `general` for any conversation that already had a project.
  `general` lowers autonomy and triggers expert review, so the effect is conservative rather than unsafe, but it
  is not what was chosen.
- Membership is still correct: `EnsureProject` creates the project through the one producer, with the owner row,
  so nothing became unreachable or shared by accident.

## Preconditions

A workbench conversation that already has a project — because a variant was kept in it, because the page was
opened as `/workbench?project=<id>`, or because a previous turn created one — and "Start this" pressed on a
proposal card.

## Root cause

`startThis()` in `internal/httpapi/assets/workbench.js` built its request body with `title`, `statement`,
`risk_tier`, `industry` and `build`, and no `project_id`. `Draft` hands whatever it is given to
`workspace.Service.EnsureProject`, whose contract is "a project to put this in, making one if there is not one" —
so an empty id is not an error, it is a new project.

The omission is visible in the file itself: the line below the gap blanks the industry *conditionally on
`state.projectID`*, which only makes sense if the project id is being sent. The guard for a field that was never
there.

## Why it did not show up before

- No fence asserted what the card POSTs. The card's fences cover what it does with the reply — it follows the goal
  it started, stops polling when it settles — and the wiring fence reads `startIt`, not `startThis`.
- Every server-side fence for `POST /v1/goals` supplies `project_id` itself, so the handler's project path is well
  covered and the *caller's* omission is not.
- The exercise in #104 never noticed: its conversation had no project when the goal was started, so creating one
  was the correct outcome and the second project never appeared.
- ‼️ The sibling fix
  [`…-a-goal-could-be-drafted-into-a-project-its-caller-was-not-in.md`](2026-09-15-a-goal-could-be-drafted-into-a-project-its-caller-was-not-in.md)
  records under "Why it did not show up before" that *"the workbench always sends the project of the conversation
  the person is in, so a person never hits it"*. That was not true of this path, and the permission check it added
  is what now runs for a card-started goal.

## The fix

`internal/httpapi/assets/workbench.js`, in `startThis()`:

```js
project_id: state.projectID || '',
```

The same expression the conversation turn already sends (`api('/v1/converse', { project_id: state.projectID …`),
so the two requests name the project the same way. With it sent, blanking the industry beside it becomes correct
rather than a guard over a gap: a conversation with a project keeps its project and its pack, and a conversation
without one still creates a project from the chosen industry.

## Fence

`TestWorkbench_TheCardStartsAGoalInTheConversationsProject` (`internal/httpapi/goal_progress_fence_test.go`) reads
the page's own source between `function startThis()` and `function startIt()` and requires that the body sends
`project_id: state.projectID`, and that the industry stays conditional on the same field — the two belong
together, and a fence on one of them would let the pair drift apart again.

## Drill

"the card starts a goal with no project" (`scripts/drill-fences.sh`, section "Build goal UX") renames the key in
the request body. The fence goes red.
