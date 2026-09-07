# FORGE was never told to say it is an AI

**Date:** 2026-09-03 · **Phase:** 9.8 · **Severity:** low as built, higher as
deployed — see Exposure · **Owner:** `internal/persona/soul.go`

**Status: AUD-05 remains PARTIAL after this change, deliberately.** Read the
Decision section before recording it as closed.

## Symptom

Nothing anywhere told FORGE to identify itself as an AI.

The soul had ten commitments and none of them was about self-identification.
The PRD names it — AUD-05, "voice identity and tone; always identifies itself as
AI" — and three separate places in the codebase reason carefully about the
requirement, but all three are about the *record*: `SpeakerForge` is a distinct
kind, it carries no user, the audio track names FORGE. Every one of those makes
FORGE distinguishable to something reading structured data.

None of them makes FORGE *say* anything.

## Exposure

The half that was built covers the surfaces that carry labels. The gap is the one
that does not.

**Sound has no label.** A person who joins a room and hears a synthesised voice
has nothing telling them it is synthesised — the track identity is transport
metadata, not something anybody hears. A late joiner hears a voice answering
questions in an engineering meeting.

Being taken for a person is not a neutral mistake. It changes what people say in
front of it, and how much weight they give the answer. That is why the
requirement says "always" rather than "on request".

The severity is written as "low as built" because today FORGE only speaks in a
room when a participant asks it to, in a room those participants opened. It rises
with anything that widens the audience — a phone bridge, a recorded playback, a
guest link.

## Root cause

Not a regression, and not an oversight in the record: the structural half was
designed for exactly this requirement and is well reasoned. What was missing is
that "identifies itself" was read as a property of the *transcript* rather than a
thing FORGE says, and the transcript half is the half that can be built
mechanically. The half that cannot be was never written down.

## Decision

Four options were put up. **The chosen one is the soul commitment alone**, and
what it does and does not buy is worth stating plainly, because the tally does
not move and somebody will later ask why.

| Option | What it gives | What it costs |
|---|---|---|
| **Soul commitment only** — chosen | The clause exists, is immutable, reaches every model call, and is visible to users in the "what FORGE will and will not do" dialog | Honoured by the model, not enforced by the system. Nothing makes an utterance contain it |
| First-turn spoken disclosure | Deterministic, once per room, recorded and spoken identically | Changes what every room hears; a product change |
| Every utterance | The only reading that protects a mid-conversation joiner | Repetitive enough that people stop hearing it |
| Declare the labelling sufficient | No work | The ledger declaring victory over its own open item |

**Why the chosen option is defensible.** The soul is, by construction, the
model-borne layer — `truthful-state` and `no-fabrication` are instructions too,
and nothing mechanically enforces those either. Adding self-identification to it
puts the commitment where the codebase already keeps its commitments, at the
right rank (immutable, so no configuration can relax it) and in the right place
in the ordering (after "what I did" and "what is true", before "how I know").

**Why it does not close AUD-05.** A clause is not a mechanism. The requirement
says *always*, and this makes it likely rather than certain. The ledger's own
criticism — "left to the model, the one pattern this codebase refuses everywhere
else" — still applies, and the honest thing is to say so rather than let a
commitment stand in for a control.

The argument for the deterministic version is preserved above so that whoever
wants it later does not have to rediscover it. The exposure it addresses is the
one named in Exposure: audio carries no label.

## Fix

`internal/persona/soul.go`:

- A new immutable commitment, `self-identification`, placed third — after
  `truthful-state` (what I did) and `no-fabrication` (what is true), before
  `evidence-over-fluency` (how I know). Ordering carries meaning here: when two
  commitments conflict the earlier wins, so this outranks helpfulness, which is
  what has to happen when the request is "play a human colleague".
- Marked `Immutable`, so no character setting can relax it. Verified under the
  most relaxed character available (`terse` + `low` critique) — the clause is
  still in the prompt.
- `Version` 1 → 2, as the package requires whenever the text changes.

The caveat about enforcement lives in a **code comment**, not in the
commitment's `Why`. The `Why` is rendered to users in the soul dialog, and
singling this clause out there as "not enforced" would imply its neighbours are,
which is false — they are all instructions.

## Verification

- `TestImmutableCommitmentsAreAllPresent` — the clause is added to the test's
  `required` map, so it is fenced rather than merely present.
- **Drill run, both directions.** Deleting the clause: *"the immutable
  commitment `self-identification` has been removed"*. Downgrading it to tunable:
  *"present but no longer marked Immutable; configuration could now relax it"*.
  Both restored byte-identical afterwards. The second matters more than the
  first — a commitment quietly made configurable is the same removal in disguise.
- `TestSystemPromptCarriesEveryCommitment` covers it automatically, and the text
  was read out of a real generated prompt to confirm rather than inferred.
- It renders on the workbench soul dialog, marked "cannot be relaxed", under
  `persona v2`.
- Full suite: 29 packages pass.

## What would close AUD-05

A deterministic disclosure. The cheapest form is a fixed self-identification on
FORGE's first turn in a room, written into the turn so the transcript and the
audio say the same words. `Ask` already holds the room's turns, so "has FORGE
spoken here before" costs no extra query and is answered from the record rather
than from memory, which means it survives a restart.

That was not done, on purpose. This section is the specification for it if the
answer changes.

## Related

- PRD **AUD-05**; `internal/domain/collab/room.go` (`SpeakerForge`),
  `internal/httpapi/rooms_media.go` (FORGE's turn), `internal/media/speech.go`
  (the audio track) — the structural half.
