# The domain pack was written and never read

**Date:** 2026-09-04 · **Status:** fixed · **Owner:** workbench

## The short version

`forge_projects.pack` records which domain's rules a project is worked under. It
was written on project creation and then **read by nothing**. A project could
record that mechanical engineering rules applied while no rule anywhere applied
them.

Three defects, one root cause. All three are fixed here.

---

## What the user saw

Nothing — which is the point, and why this survived so long.

- A person who worked on a bracket, a bridge and a PCB got **byte-identical**
  instructions in all three. The only domain knowledge in the system was whatever
  the model happened to bring.
- Six of the eight packs could not be selected at all, so most engineering work
  was refused at the door with "this build is not validated for the `mechanical`
  pack".
- Nothing in the product let anyone choose a domain in the first place.

## Root cause

`pack.go` opened by quoting the PRD — "a pack is NOT a prompt" — and then defined
a table that only ever answered one question: *may a project be created in this?*

That question was answered with a boolean, and the boolean was set by the
**riskiest** action in each domain. `mechanical` was unavailable because this
build cannot gate drawing release (PRD tier R3). Correct as far as it goes — and
it also refused concept CAD, a render and a revision, which are R1 and are the
work this product exists to do.

So the granularity was wrong in one direction and the read side was missing
entirely in the other:

| # | Defect | Where |
|---|---|---|
| 1 | Availability is a boolean; risk is per-action | `internal/domain/pack/pack.go` |
| 2 | The pack column has no reader | nowhere — that was the defect |
| 3 | The pack column has no producer | `intake.go` passed the constant `"software"`; `geometry/service.go` passed `"general"` |

Defect 3 is the one that made 1 and 2 invisible. Every project in every
deployment was `software` or `general`, both of which were available, so the
refusal path was never hit by a real user and the missing read never changed an
answer anybody saw.

### Why it looked fine

`pack_test.go` asserted the table said the right things, and it passed. It could
not have failed: it tested the table, and the table was correct. Nothing tested
that anything **consulted** it — the fence and the thing it was guarding were the
same object.

## The fix

**A pack carries a tier ceiling instead of a boolean.** The boundary is
unchanged — nothing in an engineering pack reaches R2 — but it is now expressed
where risk lives. `Requires` reads as *what would raise the ceiling*, and is
shown when somebody asks for work above it rather than at the door.

**The pack is read in two places, with deliberately different failure modes:**

| Reader | Question | On failure |
|---|---|---|
| `workspace.Service.PackFor` → `agent.grantFor` | how far may this work go? | **fails the task** — an unreadable permission is not an unrestricted one |
| `agent.DomainStore.For` → `framingFor` | what vocabulary does it use? | falls back to `general` + WARN — a worse answer, not an unsafe one |

That asymmetry is intentional and is documented at both sites, because it is
exactly the kind of thing that reads as an inconsistency later.

**The pack has producers:** `forgectl goal new --industry`, `forgectl project
industry --project <id> [--set <industry>]`, and `industry` on `POST /v1/goals`.

**An unstated industry is `general`, not a guess.** The old constant filed a
bracket goal under rules about merging code. Inference was considered and
rejected: `Draft` calls no model by design (the workbench depends on the goal id
existing before planning starts), and `general` is not a fallback invented to
fill the hole — it is the pack whose definition *is* an unknown domain.

**The fabrication guard was extended to the new industries.** It only fires on
standards families it can name, and the table was mechanical and electrical. A
civil answer citing ACI 318 or an aerospace one citing DO-178C would have carried
a specific figure, named a published standard, and been reported as ordinary
prose — in nine industries where the product is newest and least trusted.

Two bugs were found while adding them:

- The `IEC` pattern was `\d{2,3}`, sized for motor frames. `IEC 61508` matched its
  leading `IEC 615` — detected, then **named as a standard that does not exist**.
- The new `AASHTO` pattern allowed zero digits, so the bare word `AASHTO` in
  ordinary prose was flagged as a recalled figure. A banner on prose is a banner
  people learn to skip.

## The fence rewrite, and why it is not a weakening

`TestNoRegulatedPackIsAvailableInThisBuild` asserted that mechanical, civil,
electrical, aerospace, robotics and medical were all unavailable. This change
makes four of those six available, so the fence had to change — and a safety
assertion that gets edited by the change it was guarding deserves the scrutiny.

It was **restated, not removed**, and split into two fences that between them
hold strictly more:

- `TestNoEngineeringPackPermitsConsequentialWork` — no engineering pack permits
  R2 or above, and every one names the authority that would raise it.
- `TestMedicalAndRoboticsAreNotWorkableAtAll` — the two packs with no ceiling
  stay uncreatable at every tier, and medical keeps its three PRD phrases.
- `TestNoPackPermitsProhibitedWork` — new. R5 is refused whatever a row says.

What the old fence could not have caught and the new ones do: a pack quietly
acquiring an R2 ceiling, and an R5 row.

## Verification

Every fence below was **mutation-drilled**: the defect was reintroduced, the
fence was confirmed red, the tree was restored and confirmed byte-identical.

| Fence | Drilled by |
|---|---|
| `TestNoEngineeringPackPermitsConsequentialWork` | setting `mechanical` to `RiskR2` |
| `TestEveryIndustryTheSelectorOffersIsWorkable` | deleting the `Architecture` row |
| `TestMedicalAndRoboticsAreNotWorkableAtAll` | giving `medical` a ceiling |
| `TestDraft_CreatesTheProjectInTheIndustryAsked` | restoring the `"software"` constant |

Live, against real Postgres and the real CLI:

```bash
make db-up
FORGE_TEST_DATABASE_URL="postgres://forge:forge_dev_pw@localhost:55840/forge?sslmode=disable" \
  go test -count=1 ./...
make drill
```

```bash
forgectl goal new --owner <email> --title "Studio massing" \
  --statement "Study massing options for a small studio." --industry "Architecture"
forgectl project industry --project <id>
forgectl project industry --project <id> --set "Civil engineering"
```

Observed: the project is created in `architecture`; `project industry` prints the
ceiling and what would raise it; `medical` is refused; an unknown industry is
refused with the list.

## Related

- Requirements: `docs/prd.md` §"Domain packs", §"Implementation carve-outs"
  ("Ceilings, not availability", "Industry list")
- Regression fences: `internal/domain/pack/pack_test.go`,
  `internal/domain/workspace/pack_boundary_test.go`,
  `internal/agent/intake_industry_test.go`, `internal/agent/pack_ceiling_test.go`,
  `internal/agent/domain_framing_test.go`, `internal/agent/standards_test.go`
- Coverage suite: `internal/eval/cases.go` (`industryCoverage`)
