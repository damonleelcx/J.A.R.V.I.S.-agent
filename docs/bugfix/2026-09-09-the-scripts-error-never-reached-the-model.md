# The one message that could have fixed the gear never reached the model

**Found:** 2026-09-09, testing "make me a gear" end to end against the live
deployment at `forge.heros-agent.space`.
**Severity:** high on the scripted path — the reply was confident, the part was
absent, and the reason existed the whole time in a place nobody could act on.
**Owner:** agent (conversation). Secondary: httpapi (the turn's own deadline).

## Symptom

The chain worked all the way to the last link:

| stage | result |
|---|---|
| the model chose `shape: "script"` | ✅ (it wrote `shape: "gear"` before the availability line was added) |
| the sandbox accepted `import math` / `from build123d import *` | ✅ |
| every builder name resolved | ✅ (223-name manifest) |
| the script executed | ✅ |
| **build123d built the gear** | ❌ `ValueError: A face or sketch must be provided` |
| the viewport would draw it | ✅ once the script succeeds |

FORGE said the gear was made. The gear was not in the model, and the sentence
explaining why arrived later, on an export, phrased for a person.

## Root cause

**Every check in a turn reads the DOCUMENT, and a scripted part's shape is not in
the document.**

- `Faults()` walks outlines, loops and features and reports what cannot be built
  *from what is written down*.
- `turned.go` measures what is written down.
- `look.go` renders what is written down.

A scripted part writes down nothing but Python. Its shape is whatever build123d
makes when the script runs, and the script does not run until `BuildDocument`
asks for the solid — which happens at export or when the viewport asks, after the
turn is over and the reply is stored.

So the failure was structurally invisible to the turn. `repairIfFaulty` works
from `Faults()` and could not see it; the runner's message went into the build's
`Inferred` notes and reached a reader hours later; and it never reached the
**model**, which is the only party that could have fixed the script.

This is not a defect in any of the three checks. It is a check that was never
written, for the one part shape whose correctness cannot be read off the page.

## Why the builder's own words, verbatim

build123d's messages are unusually specific. "A face or sketch must be provided"
names the mistake exactly: a solid operation was handed a wire. Nothing in the
fix paraphrases them and nothing diagnoses — a rule that tried would be a second,
worse copy of a CAD kernel's own knowledge, and it would go stale the first time
build123d improved a message.

## What was built

`internal/agent/scriptrepair.go`.

**`ScriptRunner`** is `RunScript(ctx, source) error` and nothing else. It
deliberately does not want the STEP that came back: the document stays the single
source of truth for what the model is, and the solid is built from it downstream
exactly as before. A shape cached in the agent would be a second answer to the
same question. The kernel is adapted to it in
`internal/httpapi/scriptrunner.go`, which also keeps `internal/agent` free of a
dependency on `internal/domain/cad` — so a test that exercises a repair does not
need Python and OpenCASCADE.

**`repairIfScriptsFail`** runs each scripted part the turn wrote, and on a
refusal hands the model *one script and one message* and asks for a script back.
It runs on both reply paths and inside the multi-pass gauntlet, at the point
`repairIfFaulty` runs, and for the same reason: a rule enforced in one of two
paths holds until somebody uses the other one.

### The two properties that make this different from the other repairs

1. **It can verify itself.** A repaired document is re-checked against the same
   static rules that faulted it — the best that can be done for coordinates. A
   repaired script is **re-run**. Acceptance is not "the model returned something
   different", it is "the kernel built it", so this loop cannot report a fix that
   did not work. It is the only check in a turn that can say that.

2. **The failure `removedSomething` exists for is not available to it.**
   `georepair.go` hands over the whole document and must defend against a model
   that fixes a fault by deleting the part that has it. Here the model is handed
   one script and can return only a script: the part, its id, its position and
   every other part are never in the request and cannot come back changed.

### The rules, and why each one

| Rule | Why |
|---|---|
| Accept only a script that **built** | An unverified rewrite is not an improvement on an unverified original — neither builds, and swapping them discards what the model meant while changing nothing a reader can see. The original is kept. |
| Stop on the **same message twice** | A script is rebuilt from scratch each run, so a repeated message is the same thing *still* wrong. Document faults cascade and a repeated one means progress; script faults do not. Without this a stuck model spends the whole budget on one part. |
| 4 attempts per part, 8 per turn, 4 minutes wall clock | The caps bound *calls*; the clock bounds the *wait*, which is what a person experiences and the one that holds when a kernel is slow rather than wrong. |
| Only run the scripts **this turn wrote** | A twelve-pass build carries the whole document forward every pass. Without it, every script ever written is re-run on every later pass — same bytes, same verdict, up to eleven more times, at 30s of kernel each. Byte identity against the document the turn started from. |
| Say every outcome out loud | Silent only when every script built first time, which is the ordinary case and carries no news. A part that could not be fixed is stated **in the turn**, where it used to reach the reader on an export with the turn long gone. |

## The second defect, found while fixing the first

**A turn was bounded by one model call's timeout.**

`ConverseHandlers.Converse` used `LLM.RequestTimeout + 15s`. The ordering was
deliberate and right — a deadline shorter than the client's own kills a call
mid-backoff and blames the model for a timeout hierarchy — and the *unit* was
wrong. A turn has not been one call for some time: it plans a build, runs a pass
per subsystem, repairs geometry, now runs and rewrites scripts, and looks at the
render.

So the multi-pass build measured at **25 minutes** in a live test could not
finish over HTTP at all. It was cancelled at 3m15s, mid-stream, and from inside
the turn a cancelled context is indistinguishable from a model that failed —
nothing reported it as a timeout. A person saw a build stop moving.

Two changes, and both were needed; either alone leaves a silent cut-off one layer
down:

- **`FORGE_TURN_BUDGET`** (default 30m) bounds the turn. `FORGE_LLM_REQUEST_TIMEOUT`
  still bounds each call, so a hung provider still fails in three minutes; this
  only allows *more* calls, never a longer one. A budget below one call's timeout
  is refused at boot, because it puts the original bug back.
- **The connection's write deadline is lifted for the stream.**
  `http.Server.WriteTimeout` is a deadline on the whole response, set when the
  request arrives — five minutes here. Raising it server-wide would weaken every
  ordinary handler, so it is lifted on the one endpoint whose purpose is to stay
  open, via `http.NewResponseController`.

### And a third, found by the tests the second one broke

`TurnBudget` read literally is zero in any `Config` assembled in code rather than
loaded from the environment. A zero deadline cancels the turn *before its first
call*, every database read inside it then fails, and what surfaces to the person
is "the database could not be reached" — true, and pointing at the wrong thing
entirely. Two existing tests reported exactly that the moment the handler started
reading the field. An unset budget now falls back to `config.DefaultTurnBudget`,
which is the same constant `Load` defaults to, so there is still one number.

## Verification

Thirteen fences, each **drilled** — the product code broken on purpose, the test
confirmed red, the code restored (`-count=1`, no cached PASS).

`internal/agent/scriptrepair_test.go`:

| fence | what breaking it looks like |
|---|---|
| `TestScripts_RewrittenUntilTheyBuild` | a verified fix is not kept |
| `TestScripts_TheBuildersWordsReachTheModel` | the model is asked to fix an unnamed problem |
| `TestScripts_UnfixableKeepsTheOriginalAndIsSaidOutLoud` | an unverified rewrite replaces the original, silently |
| `TestScripts_StopsWhenTheModelIsNotMoving` | one stuck part eats the turn |
| `TestScripts_NoRunnerIsNotAQuietPass` | the contract offers scripts nothing can run |
| `TestScripts_UnchangedScriptsAreNotRerun` | a twelve-pass build re-runs every script eleven times |
| `TestScripts_SelectionAgreesWithTheBuilder` | a part is built and never verified |
| `TestScripts_RefusalIsNeverEmpty` | an empty complaint reaches the model |
| `TestScripts_TheTurnActuallyRunsThem` (buffered, streamed) | the call site is deleted and every unit fence stays green |
| `TestScripts_MultiPassBuildRunsThemToo` | same, for the build loop — and this hole was **real**: the first drill found the two turn fences red and the build loop green |

`internal/httpapi/converse_budget_test.go`:

| fence | what breaking it looks like |
|---|---|
| `TestConverse_TurnBudgetBoundsTheTurnNotOneCall` | a 25-minute build is cancelled at 3m15s |
| `TestConverse_AnUnsetTurnBudgetIsNotAnExpiredOne` | every turn dies instantly and blames the database |
| `TestConverse_AStreamOutlivesTheServerWriteTimeout` | the connection closes under a working turn. A recorder cannot see this, so it runs a **real server** with a write timeout shorter than the turn |

## What the live run found, after it was already "done"

Deployed to `forge.heros-agent.space` and asked, in the workbench, for *"a
20-tooth involute spur gear, module 2, 6mm thick, with an 8mm bore"*. The turn
reported its own work as it went:

```
Gear Body did not build (the script was stopped before it finished…). Rewriting it — attempt 1 of 4.
Gear Body did not build (ValueError: A face or sketch must be provided). Rewriting it — attempt 2 of 4.
…
Gear Body is built by a script that did not build as first written. FORGE rewrote it 2 times
and ran it again, and it builds now.
```

The stored script was pulled back out of Postgres and run against the kernel in
the running pod: **12565.7 mm³**. The exact error from the original report
reached the model, in the turn, and was fixed.

**And it exposed two defects in this change that reading it had not.**

### 1. The verification did not have the last word

It was placed beside `repairIfFaulty`, which reads well and is wrong. Two repairs
run after that point — `repairIfTurned` and `repairIfItLooksWrong` — and **both
hand back a whole new document from the model**. Either can carry a script that
has never been run, and the turn would still say the script builds. The
verification would be about a document that no longer existed.

It now runs **last**. A check that verifies itself is worth nothing if something
rewrites the document after it.

### 2. The visual check cannot see a scripted part, and said so as a defect

The same live turn ended with:

> Looking at the model it had just built, FORGE found a problem and corrected it:
> Gear Body: The part is a rectangular block rather than a circular gear with
> teeth and a bore.

That is a **false positive by construction**. `ContactSheet` draws from the
DOCUMENT, and a scripted part's shape is not in the document — the renderer draws
a bounding box, so every scripted part looks like a plain block whatever it is.
Asked "is this the right shape", the vision model answers the only way it can,
for every scripted part, on every turn, forever. `look.go`'s own doc says what a
repair driven by a wrong complaint does to a good model, and this repository has
already had to delete one rule that fired on the right answer.

`look()` now names the scripted parts and says they are drawn as a plain block
that is not their real shape. They are named rather than removed from the
picture: they still occupy space, and "is it buried inside something" and "is it
floating" are answerable about a block and worth keeping.

Neither of these was findable by reading. Both came from one turn on the real
deployment.

### Both fixes confirmed on the deployment

Redeployed as `forge@sha256:1b803b25…` (`deploy/verify.sh` 8/8) and asked for a
second gear in the same project: *"a 16-tooth involute spur gear, module 2.5,
8mm thick, with a 10mm bore"*.

- **No shape complaint.** The visual check no longer reports the scripted part as
  a block. What it said instead was
  *"Gear Body: The part is completely hidden inside the Gear Body 16T part, as
  only one block is visible in the views despite two parts being listed"* — which
  is question 1, is answerable about a block, was **true** (both gears sat at the
  origin), and was corrected.
- **The verification survived that correction.** The visual repair handed back a
  whole new document and the script check ran after it. Both scripts were then
  pulled out of Postgres and run against the kernel in the running pod:

  | part | volume |
  |---|---|
  | `gear-body` (20-tooth, m2) | 12565.7 mm³ |
  | `gear-body-16t` (16-tooth, m2.5) | 10176.6 mm³ |

  10176.6 is the right *number*, not merely a non-zero one: a plain m2.5 16-tooth
  pitch disc 8mm thick is 10053 mm³, plus teeth, minus a 10mm bore.

Under the old ordering that second document would have been stored unverified.

## How often it works now

Measured against `qwen3.7-plus` with a real build123d, asking for a 20-tooth
module-2 spur gear:

| | before | after |
|---|---|---|
| a gear that builds | 0 — and the reply said it was built | **2 of 4 runs**, kernel-verified |
| a gear that does not | silent; the reason arrived on an export | **loud, in the turn, with the builder's words** |

The two failures were `ValueError: Face can only be created with closed wires`
after 3 rewrites, and `Rotated is not available here` after 4 — the second being
the model guessing at a build123d name. Both ended with the part named as absent
in the reply, which is the honest outcome and not the finished one. What would
raise the number is in "What is still not checked" below.

## What is still not checked

A script that builds a *solid nobody asked for* — a valid cylinder where a gear
was wanted. That is a question about the picture, and `look.go` is what asks it;
it is fed by the rendered document, so the answer for a scripted part is only as
good as its bounding shape. Named here rather than left as an assumption.

**Two things that might raise 2-of-4. One was done; it did not.**

1. ~~**`lambda` is refused while `def` is allowed.**~~ **Done 2026-09-09, on an
   explicit decision.** `ast.Lambda` was absent from `ALLOWED_NODES` while
   `ast.FunctionDef` was present — an inconsistency, not a boundary: a lambda's
   body is one expression and every node in it was already allowed inside a def.
   Binding its parameters is a separate rule from allowing the node, and shares
   `_bind_arguments` with `FunctionDef`; without it, `lambda i: abs(i)` parses,
   passes the whitelist, and is then refused with "i is not available here" — a
   correct script rejected for using its own argument.

   The price is paid in fences: `TestScript_RefusesTheWayOut` now tries the
   documented escape, an unavailable builtin, and a dunder attribute of a given
   object all from **inside a lambda**, and two drills go red.

   ‼️ **It removed a failure mode and did not move the number.** Four live gear
   requests after: **no Lambda refusal at all**, where one in four had one
   before — and 1 of 4 built, against 2 of 4 before, which at n=4 is noise. The
   three failures were `Rotate is not available here`,
   `BuildSketch.__init__() got an unexpected keyword argument 'local_mode'`, and
   `Standard_TypeMismatch: TopoDS::Face`. Two of the three are item 2 below.

2. **An unavailable-name refusal names nothing available.** "Rotate is not
   available here" is precise about the mistake and gives the model nothing to
   move toward; the manifest has 223 names and the near ones are computable.
   Now the dominant failure: the model is not writing bad Python, it is guessing
   at build123d's API.
