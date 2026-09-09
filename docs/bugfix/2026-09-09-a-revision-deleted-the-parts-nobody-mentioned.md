# A revision deleted the parts nobody mentioned

**Found:** 2026-09-09, testing "make the body less boxy" against a finished
thirteen-part sports car (body, cabin, four wheels, a three-piece spoiler, four
wheel-arch cutters).
**Severity:** high on the conversational path — the model on screen lost parts
the person had asked for on an earlier turn, and nothing said so.
**Owner:** agent (conversation contract, and the missing safety net under it).

## Symptom

Three real turns against `qwen3.7-plus`, all from the same saved car, all asking
only for a rounder body:

| Run | What came back |
|---|---|
| 1 | the body returned under a **different id**, so the old body survived beside the new one and the car had two bodies |
| 2 | the **spoiler was gone** — wing and both supports — unmentioned |
| 3 | the spoiler was gone **and** the new outline crossed itself, so the body did not build at all |

0/3. The reply in every case talked about the body. A person who asks about the
body is looking at the body, so the spoiler is precisely the thing they will not
notice going.

## Root cause

Two causes, one structural and one a gap in the contract.

**Structural.** A whole `prototype` reply *is* the model: the document that comes
back replaces the one that was there. So a revision deletes by **omission** — a
part the model does not retype is a part it destroyed. That is not a defect in
the wire format, it is what a rewrite means; `prototype_edit` exists exactly
because an edit cannot lose what it does not mention. But nothing checked, and
nothing told anyone.

**Contract.** The prompt said part ids are stable across turns, which the model
read as being about *dimension* changes. It did not say that a part changing
SHAPE keeps its id, and it did not say anywhere that everything unmentioned must
come back. Both readings are defensible from what was written, which is why the
model produced both failures.

## Fix

Two halves, deliberately: one tells the model what is expected, the other
assumes it will sometimes not do it.

1. **The contract says it** (`internal/agent/converse.go`). Two additions:
   *"RESHAPING A PART DOES NOT CHANGE ITS ID"*, and *"A REVISION KEEPS EVERY
   PART IT WAS NOT ASKED TO REMOVE"* — with the consequence spelled out ("a part
   you simply did not mention is a part you destroyed") and the escape route
   named ("if you find yourself rewriting the whole model to change one thing,
   send `prototype_edit` instead: what it does not mention cannot be lost").

2. **The turn reports what went** (`internal/agent/vanished.go`). Every whole-
   prototype revision is compared against the model the server holds, and any
   part that is neither kept nor consumed as a cutting tool is named to the
   reader before the turn is emitted.

### Why it reports rather than refuses

Sometimes the removal is exactly right. "Take the cabin off" produces a document
without a cabin, and refusing that would break the feature this whole
conversation is for. Nothing in the code can tell an intended removal from an
accidental one — only the person can, and only if they are told. So the turn
proceeds and says what is no longer there.

### The direction the tool guard has to point

A part used as a cutting tool "does not also appear as a solid of its own"
(`geometry.Feature.With`) — it became the void it was drawn to make, so the
reader never saw it as a part. The first version of this code skipped tools
named by the AFTER document, which is unreachable: a document that drops a tool
while keeping the feature that names it is already faulty and is reported as a
fault, not as a loss. The case that actually happens is the reverse — a revision
reworks an arch and drops the cutter it no longer needs. Announcing that as a
lost part would cry wolf on the commonest correct use of a feature, and a notice
that fires on the ordinary case is one people learn to skip.

**That error was found by a mutation drill, not by reading.** The test written to
cover the guard stayed GREEN when the guard was deleted, because its fixture
kept the tool in `Parts` and so never reached the branch. The fence was vacuous
and the code was wrong in the same place. See
`docs/bugfix/`-adjacent practice: a fence is not a fence until it has been seen
red.

## Measured

Two measurements, and the second is the one that carries weight.

**Before and after**, three runs then five, from this fixture:

| | parts lost with no notice | geometry that builds |
|---|---|---|
| before (3 runs, through HTTP, mid-conversation) | 3/3 runs lost something silently | 2/3 |
| after (5 runs) | **0/5** | 4/5 |

Those two are NOT strictly comparable and should not be read as one number
moving: the "before" ran through the HTTP path with the earlier turns of the
conversation in history, and the "after" ran through `Respond` with the model
supplied and no history. Different conditions.

**The control** is the comparable one — same harness, same fixture, same model,
same day, with only the two new contract paragraphs deleted:

| 5 runs each | revisions that dropped a part | announced | that build |
|---|---|---|---|
| contract WITHOUT the new paragraphs | **2/5** (run 2 dropped Cabin *and* Main Body; run 5 dropped Cabin) | 2/2 | 5/5 |
| contract WITH them | **0/5** | — | 4/5 |

Both halves of the fix show up in that table, and they show up separately:

- The contract paragraphs took losses from 2/5 to 0/5.
- The safety net caught **both** of the control's losses and named them. Neither
  reached a reader silently even with the contract at its old wording — which is
  the point of having a net rather than only a rule.

Control run 2 is the original failure exactly: the body came back under a new id,
so `Main Body` was gone while the reply talked about having reshaped it.

The build rate went 5/5 to 4/5, which at n=5 is one run and is not evidence of
anything. The one failure was a self-crossing outline the repair pass could not
cure — a model slip of the kind `georepair.go` exists for and does not always
win. It is counted as a rate rather than asserted, for the reason given in
`TestLiveRevisionKeepsTheRestOfTheModel`.

## Verification and defence against regression

| Fence | What it holds |
|---|---|
| `TestVanished_SaysWhatARevisionRemoved` | a dropped part is named |
| `TestVanished_IsSilentWhenNothingWent` | no notice when nothing went |
| `TestVanished_DoesNotCountARetiredCutterAsALoss` | a cutter the revision no longer needs is not announced |
| `TestVanished_DoesNotCountAnActiveToolAsALoss` | a part still used as a tool is not announced |
| `TestVanished_TheTurnSaysWhatItRemoved` | `Respond` actually calls it |
| `TestVanished_TheStreamingTurnSaysWhatItRemoved` | `RespondStream` actually calls it — the path the product runs, which the fallback would otherwise hide |
| `TestLiveRevisionKeepsTheRestOfTheModel` | the live rate, skipped without `FORGE_LIVE_LLM_TESTS` |

Every one of the six was proven red by deleting the thing it guards. The
streaming fence was proven to exercise the streaming path specifically, by
deleting the call from `RespondStream` alone and watching the `Respond` fence
stay green.

## Related

- `docs/bugfix/2026-09-09-a-turn-could-say-it-built-what-it-did-not.md` — the
  repair round-trip, which runs immediately before this check and is why a
  fault here means "repair could not cure it".
- `docs/plan-2026-09-08-talking-changes-to-a-model.md` — why `prototype_edit`
  exists, and the guarantee it gives that a whole prototype cannot.
