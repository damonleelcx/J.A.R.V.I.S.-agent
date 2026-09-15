# A goal could be drafted into a project its caller was not in

**Date:** 2026-09-15 · **Status:** fixed (stacked on #85, A1 follow-ups) · **Severity:** high — anyone signed in could write work into a stranger's project and spend a model call on it

## Summary

`POST /v1/goals` accepts a `project_id`. The handler passed it to `agent.Intake.Draft`, which hands it to
`workspace.Service.EnsureProject`, and `EnsureProject` returns at once for any id it is given. Nothing on the way
asked whether the caller could plan work in that project. So a signed-in account could draft a goal into any project
whose id it knew, and the planner's model call ran for it.

The fix checks `goal.create` on a named project, through `requirePermission`, before anything is written or asked:
a project the caller is not in is 404, a member without the permission (a viewer) is 403 — the same answers every
other goal endpoint gives.

## Symptom

- Found while adding `build: true` to the endpoint (A1 follow-ups). A fence posting another account's `project_id`
  got **502** (the stub's reply was not a plan), and afterwards the stranger's project held **2 goals** and the model
  had been **asked once**.
- With a real model the caller got **404** instead: the plan landed, then the handler's own read of the goal it had
  just written was refused, because the caller could not read that project. The goal stayed.

## Impact

- Any account could put draft goals, with plans, into any project it had an id for. A draft runs nothing, and
  starting one needs `goal.start` in that project, which is checked. But the goal, its tasks and the planner's
  project-graph notes are written under someone else's project, and they appear in its console.
- The planner's call is charged to the stranger's goal, and for `build: true` the planning call is recorded in that
  goal's budget.
- A viewer of a project could plan work in it, although `goal.create` is not a viewer's permission.

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
the person is in, so a person never hits it.

## Fix

`CreateGoal` calls `h.deps.requirePermission(r, req.ProjectID, user.ID, access.PermGoalCreate)` when a project is
named, before `Draft`. An unnamed project is created for the caller, as before.

## Verification

- `TestCreateGoal_RefusesAProjectTheCallerIsNotAMemberOf` (plain and build): 404, the project still holds only its
  own goal, and the model was not asked. On the handler before the fix: 502, two goals, one call.
- `TestCreateGoal_RefusesAViewerOfTheProject`: 403, no goal, no call.

## Regression prevention

A drill in `scripts/drill-fences.sh` ("a goal is drafted into a project its caller is not in") removes the check;
both fences go red.

## Not in this fix

**Main has the same defect** and needs the same change in its own PR. `forgectl goal new --project` is an operator
command with database access and is not checked, as before.
