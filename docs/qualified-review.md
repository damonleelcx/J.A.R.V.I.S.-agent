# Raising a ceiling: qualified review

**Date:** 2026-09-04 · **Status:** implemented · **Owner:** workbench

## The problem this solves, and the one it does not

Every engineering domain pack stops at **r1** — reversible draft. Concept
geometry, a render, a revision, a conversation. That is correct: this build
implements none of the qualified review that r2 and above require in mechanical,
civil, aerospace or the rest, and work recorded as having had review that never
happened is the worst thing this system could produce.

It was also a **dead end**. A deployment that genuinely has a licensed engineer
in the room had no way to say so, so the ceiling was a wall rather than a
boundary — and a limit with no path is exactly what the pack table refuses to
have anywhere else.

**What this adds:** recording a named, attributed authority on a project raises
that domain's ceiling to its `ReviewCeiling`. Nothing else does.

**What it deliberately does not add:** verification. This build cannot check a
licence. There is no registry to consult from inside it and no credential to
validate. Every surface says so.

## Why "recorded, not verified" is load-bearing, not a disclaimer

Two things happen when an authority is recorded, and only one of them is worth
anything:

| | Established? |
|---|---|
| A named person accepted responsibility for work above r1, in a record with an author and a timestamp | **Yes** |
| That person holds the qualification the domain asks for | **No** |

The second is what somebody will *assume* from the first if nothing says
otherwise. That assumption is the whole risk: a person doing r2 work in a civil
project would believe a licence had been established, and none was. The system
would have laundered authority it never checked.

So the phrase **"RECORDED, NOT VERIFIED"** appears verbatim in the refusal the
model reads and in the CLI a person reads, and
`TestGrantFor_ARaisedCeilingSaysItWasNotVerified` fails if it stops appearing.
It is not politeness. It is the difference between traceability — which is real,
and which PRD **AGT-07** asks of every consequential transition — and a claim of
review that nobody performed.

## The three rules that keep it honest

**1. An unattributed claim raises nothing.** A holder with nobody attesting to
them is refused by the check constraint in `0021_project_review_authority.sql`
*and* by `RecordReviewAuthority` before the write, and `grantFor` ignores one if
it somehow existed. A ceiling resting on a value with no author rests on nobody.

**2. A domain that offers no raised ceiling refuses the claim outright.**
`general` — "who is qualified in an unknown domain?" has no meaning — and
`medical` and `robotics`, which permit no work at any tier. Storing an inert
record would be worse than refusing: the project would carry something that looks
exactly like a raised ceiling and changes nothing.

**3. Clearing is as easy as setting.** `--clear`, no authority needed. A
mechanism that raises a ceiling and cannot lower it is one nobody should switch
on.

## How it composes with the goal's own tier

The grant takes the **lower** of the goal's tier and the domain's ceiling, and
the domain's ceiling is `CeilingWith(authorityRecorded)`. So:

- A raised domain ceiling never widens a goal deliberately created at r1.
- A goal at r3 in a raised civil project still stops at r2 — the raise reaches
  `ReviewCeiling`, not the sky.
- r5 is refused everywhere regardless (`TestPackCeilingWith_NeverReachesProhibited`).

## Using it

```bash
forgectl project review-authority --project prj_... \
  --holder "R. Okonkwo" --note "CEng MICE 481920" --as usr_...
forgectl project review-authority --project prj_...            # read
forgectl project review-authority --project prj_... --clear    # back down
```

Over HTTP, where the people accountable for the work actually are:

```
GET    /v1/projects/{id}/review-authority     any project member
PUT    /v1/projects/{id}/review-authority     owner only   {"holder": "...", "note": "..."}
DELETE /v1/projects/{id}/review-authority     owner only
```

**Owner-only for the writes** (`access.PermProjectManage`). A maintainer decides
individual approvals; recording an authority changes the ceiling for every piece
of work in the project from then on, which is administration of the project
rather than a decision inside it. Members below owner can still **read** it —
they are governed by the ceiling and need to know what it rests on.

**The recorder is the authenticated user, never a field in the body.** Attempting
to name the attester is *refused*, not ignored: `DecodeJSON` rejects unknown
fields, so a client that thought it was choosing who attested is told it was not
rather than being quietly overruled. Attribution is the entire value of the
record — a caller who could name the attester could put a claim in somebody
else's mouth, and the ceiling would rest on a statement its supposed author never
made.

**An empty holder on PUT is refused, not treated as a clear.** `DELETE` is the
way down. An empty body is far more likely to be a client bug than an intention
to lower a ceiling, and guessing wrong in that direction silently removes a
control.

In the **workbench**, the domain panel offers the control to somebody the server
would actually let use it — the graph response carries `can_record_authority`,
which is an *affordance and not the gate*: PUT authorises server-side whatever
the panel showed, and `TestGraph_TheRecordAffordanceIsNotThePermission` asserts
both halves.

Recording takes **two presses**, the same rule "Start this" follows and for a
stronger reason: the caveat is put in front of the person *before* they commit,
not printed at them afterwards. Somebody who reads "recorded, not verified" only
in the confirmation has already acted on the belief it corrects.

**The caveat is on every response**, recorded or not. A client that only received
it alongside a holder would have to remember to render it; one that always has it
cannot show a holder without it by accident.

## Verification

Fences in `internal/agent/pack_ceiling_test.go` and
`internal/domain/workspace/review_authority_test.go`. Three mutation drills, each
confirmed to turn a fence red and then restored byte-identical:

| Mutation | Fence that caught it |
|---|---|
| `CeilingWith` ignores whether an authority was recorded | `AnAuthorityRaisesTheDomainCeiling`, `AnUnattributedClaimRaisesNothing` |
| The refusal drops the not-verified sentence | `ARaisedCeilingSaysItWasNotVerified` |
| `medical` gains a raised ceiling | `NoAuthorityRaisesADomainThatOffersNone` |
| Writes gated on the maintainer permission instead of owner | `ReviewAuthorityHTTP_OnlyAnOwnerMayRecord` |
| The caveat is sent only when something is recorded | `ReviewAuthorityHTTP_TheCaveatIsAlwaysPresent` |

## Related

- `docs/prd.md` §"Domain packs", §8.1 risk tiers, AGT-07
- `docs/bugfix/2026-09-04-the-pack-was-written-and-never-read.md` — how the pack
  came to be read at all
- `internal/platform/db/sql/0021_project_review_authority.sql`
