# A goal could be drafted into a project its caller was not in

**Date:** 2026-09-15 · **Status:** fixed on main (found building #90, the build goal entry; the stacked build branches carry the same fix) · **Severity:** high — anyone signed in could write work into a stranger's project and spend a model call on it

## Summary

`POST /v1/goals` accepts a `project_id`. The handler passed it to `agent.Intake.Draft`, which hands it to
`workspace.Service.EnsureProject`, and `EnsureProject` returns at once for any id it is given. Nothing on the way
asked whether the caller could plan work in that project. So a signed-in account could draft a goal into any project
whose id it knew, and the planner's model call ran for it.

The fix checks `goal.create` on a named project, through `requirePermission`, before anything is written or asked:
a project the caller is not in is 404, a member without the permission (a viewer) is 403 — the same answers every
other goal endpoint gives.

## Symptom

Reproduced on main with a stub model before the fix:

- A caller who is not a member of project P posts a goal naming P: **404** — but only from the handler's own read
  of the goal it had just written, which the caller could not see. By then P held **2 goals** (its own and the
  stranger's, planned, with a task) and the model had been **asked once**.
- A **viewer** of P posts the same: **201** with the whole plan. P held 2 goals, the model was asked once.

## Impact

- Any account could put draft goals, with planned tasks, into any project it had an id for. A draft runs nothing,
  and starting one needs `goal.start` in that project, which is checked. But the goal, its tasks, its plan, any
  clarifying question and the planner's industry-reading note are written under someone else's project, and they
  appear in its console and timeline.
- The planner's call is made on the stranger's behalf, against the operator's model budget, for a goal in a project
  the caller has no standing in.
- A viewer of a project could plan work in it, although `goal.create` is not a viewer's permission (contributor and
  above).

Project ids are not published, but they appear in URLs, exports and the console of every member.

## Preconditions

A signed-in account, a model configured on the server, and the id of a project the account is not a member of (or is
only a viewer in).

## Root cause

`EnsureProject` is "a project to put this in, making one if there is not one": its early return is right for a
caller that has already authorised the id. `CreateGoal` was written before membership replaced `owner_id`
(PRD SEC-02) and never did; every other goal endpoint goes through `requireGoalPermission`, but a create has no
goal yet to resolve a project from, so it was the one path the migration to membership did not reach.

## Why it did not show up before

The endpoint's only fence covered the no-model refusal. The workbench always sends the project of the conversation
the person is in, so a person never hits it. And the stranger's case even LOOKED refused — a 404 — because the
handler's closing read failed; only counting rows shows the write.

## Fix

`CreateGoal` calls `h.deps.requirePermission(r, req.ProjectID, user.ID, access.PermGoalCreate)` when a project is
named, before `Draft` and before the model deadline starts. An unnamed project is created for the caller, as before,
and needs no check: the caller becomes its owner.

`goal.create` is the permission that already exists on main for this act ("plan work"), and the one `Replan` asks
for.

## Audit of the other goal routes on main

Main's goal routes are the ones below; the later routes named in the build stack (answer, criteria, options, choose)
do not exist on main.

| Route | Check | Evidence |
|---|---|---|
| `GET /v1/goals` | projects from `visibleProjects` (membership) | `goals.go` ListGoals |
| `POST /v1/goals` | **none on the named project — this defect** | fixed here |
| `POST /v1/goals/{id}/plan` | `loadGoalFor(..., goal.create)`, project from the goal row, before the model call | `goals_start.go` Replan; now fenced, see below |
| `POST /v1/goals/{id}/start` | `loadGoalFor(..., goal.start)` | `TestStartGoal_OtherOwnersGoalIsNotFound` |
| `GET /v1/goals/{id}`, `GET /v1/goals/{id}/timeline` | `loadGoal` → `project.read` | `goals.go` |
| `GET /v1/approvals`, `POST /v1/approvals/{id}` | `visibleProjects`; `approval.decide` on the approval's goal | `goals.go` |

Every route that takes a goal id resolves the project from the goal's row (`requireGoalPermission`), so a caller
cannot name a project they are in and a goal they are not. Create was the only route taking a project id from the
request.

## Verification

- `TestCreateGoal_RefusesAProjectTheCallerIsNotAMemberOf`: 404, the project still holds only its own goal, and the
  model was not asked. On main before the fix: 404, **two goals, one call**.
- `TestCreateGoal_RefusesAViewerOfTheProject`: 403, no goal, no call. Before the fix: 201, two goals, one call.
- `TestCreateGoal_AContributorPlansIntoTheProjectAndNoProjectStillMakesOne`: a contributor is 201 into the project
  with one call, and a request with no `project_id` still lands in a new project the caller owns.
- `TestReplan_RefusesAStrangerAndAViewerOfTheGoalsProject`: the replan route's existing check — 404 for a stranger,
  403 for a viewer, no call, no tasks. Green before and after; it holds the route that was already right.

## Regression prevention

Two drills in `scripts/drill-fences.sh` ("Goal project permission"): one removes the new check on create, and both
create fences go red; one lowers replan's permission to `project.read`, and the replan fence goes red.

## Not in this fix

`forgectl goal new --project` is an operator command with database access and is not checked, as before.
