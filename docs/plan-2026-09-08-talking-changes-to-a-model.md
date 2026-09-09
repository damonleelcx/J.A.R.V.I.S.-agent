# Talking a change into a model that already exists

**Status:** EXECUTED, 2026-09-08. From the requirement "the user should be able
to talk to the agent to add and remove parts of the 3d model".

## What was already true

Talking to FORGE *could* add and remove parts. Each turn saved a new version,
part ids were asked to stay stable across turns, and Compare lined the versions
up. The capability existed. It was not **trustworthy**, and the reason was one
structural fact:

> A revision was expressed as a **full rewrite from a names-only summary.**

The agent received this and nothing else:

```
Sports Car Concept — 6 part(s): Main Body [id: chassis-body], Cabin [id: cabin],
… (units: mm). Keep these part ids when you revise it.
```

Names and ids. **No dimension, no position, no profile, no feature.** The
conversation record stores `text` and `detail` — the spoken half and the
on-screen prose — so the JSON it emitted last turn was never replayed to it
either. To add a spoiler it had to reconstruct all six parts from memory of its
own prose.

Two defects followed from that one shape:

| | What happened | Why it matters |
|---|---|---|
| **Drift** | Untouched parts were retyped from recall | Ask for a spoiler, the wheelbase could change, silently |
| **Ambiguity** | Removal was *omission* | A part forgotten and a part deleted produce the same document |

## What was built

### A — the agent sees the model it is revising

The server loads the current variant from **its own record** and hands the
document to the turn (`agent/currentmodel.go`).

**Why the server and not the page.** The page used to send the conversation
history and no longer does: the server builds it from the record it wrote itself,
so a client cannot put words in FORGE's mouth. That reasoning applies with more
force to geometry — a page that supplied the current model could describe one
that was never built, and the reply would be a revision of a fiction that then
gets **saved as the next version**. The page says nothing about this.

**Which model.** The newest variant that is not superseded — the same rule
`drawRestoredVariant` uses to decide what to draw. Two rules would be two answers
to "what am I revising", and the day they disagreed the agent would edit
something the person could not see.

**What is included.** The document's own schema — parts, features, parameters,
derived, name, units. Handing back exactly what the agent writes is the least
confusing representation available and needs no second renderer that could drift
from the stored form. Assumptions and not-verified notes are left out: they are
prose about the design rather than the design.

Measured cost: real documents are **759–1792 characters for 3–5 parts**, so this
is a few hundred tokens a turn.

### B — a change can be expressed as a change

`prototype_edit` (`geometry/edit.go`):

```json
"prototype_edit": {
  "remove": {"parts": ["cabin"], "features": ["drill"]},
  "patch":  { "parts": [ … ], "features": [ … ], "parameters": [ … ] }
}
```

- **`patch` merges by id.** An existing id is replaced *wholesale*; a new id is
  appended. Wholesale and not field-by-field because a field merge has no way to
  say "clear this", so a part could never lose a profile once it had one.
- **Omission means unchanged, never deleted.** That is what makes drift
  *impossible* rather than unlikely: an untouched part is not in the payload, so
  there is nothing to retype.
- **`remove` is a statement.** Removal now appears in the record as an intent.
  Naming something absent is an **error**, not a no-op — "remove the cabin" when
  there is no cabin means the agent and the person disagree about what is on
  screen, and continuing quietly hides that behind a version that looks
  deliberate.
- **Prose is appended.** An assumption made two turns ago still holds.

### Why this did not spread through the system

The edit exists **on the way in only**. It is resolved into a whole `Document`
before the reply leaves the agent, so the viewport, the store, Compare, export
and the CAD kernel are untouched — they all consume a Document and none of them
learned a second shape. That is what kept a change to *how FORGE is asked* from
becoming a change to *what FORGE is*.

### The three refusals

Each is a case where continuing produces a version that looks deliberate and is
not:

1. **Both forms at once** — "here is the whole model" and "here is a change to
   it" cannot both be authoritative; choosing would be a guess.
2. **An edit with nothing to edit** — applying a patch to nothing invents a
   design from a fragment.
3. **An edit that changes nothing** — a version recording a change nobody made
   is a false entry in a history somebody will later try to read.

## Verification

| Fence | What it holds |
|---|---|
| `TestCurrentModel_CarriesEverythingARevisionMustNotInvent` | dimensions, positions, parameters, features and units all reach the agent |
| `TestBuildMessages_PrefersTheRecordOverThePagesSummary` | the record reaches the prompt **and the page's claim does not** |
| `TestBuildMessages_FallsBackToThePageWhenThereIsNoRecord` | a deployment with no database is not made worse |
| `TestEdit_LeavesUntouchedPartsByteIdentical` | the drift this exists to stop, asserted directly; and `Apply` never mutates the stored document |
| `TestEdit_RemovesOnlyWhatItNames` | omission is not deletion |
| `TestEdit_RefusesToRemoveWhatIsNotThere` | a disagreement about what is on screen surfaces |
| `TestResolveEdit_*` | an edit always becomes a whole document, or the turn refuses and says why |
| `dimensionsSurviveARevision` (eval) | whether any of it actually works, over runs |

That last one is the honest one. `partIDsSurviveARevision` records that ids ran
at **1 of 4** until the agent was shown the ids, then **4 of 4**. Dimensions are
the same story one level down, and the number is tracked with no floor because
the capability is new — a floor set from one good measurement is a target dressed
as an observation.

## Known limits, stated

- **The agent chooses whether to edit.** Nothing forces the edit form over a full
  rewrite. The prompt says when to use which; the eval measures whether it does.
- **Removing a part that a feature uses** is caught by the existing validator
  (*"applies to %q, which is not a part of this assembly"*), not by the edit —
  the edit will apply and the document will then report the broken feature.
- **No undo.** Versions accumulate and Compare shows the difference, but there is
  no "revert to v2" beyond `adopt`.
