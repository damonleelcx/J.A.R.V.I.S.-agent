# History was durable, and unreachable

**Found:** 2026-09-08, reported as *"console is not showing past conversations
or sessions, and past artifacts"*.
**Severity:** medium — nothing was lost, but a person could not get back to any
of their own work from a different browser, a different profile, or after a
private window closed.
**Owner:** httpapi (conversations) + the operations console.

## Symptom

The workbench transcript came back empty after a reload, and the operations
console listed neither conversations nor artifacts.

## What was actually true

**Nothing was being lost.** At the time of the report the production database
held 21 conversation turns, 1 artifact with 1 version, and 1 project. The
console's `Goals` and `Waiting for you` panels were empty because there were
genuinely 0 goals and 0 approvals — those were correct, not broken.

Two separate gaps produced one symptom:

1. **There was no way to ask for a list.** The only conversation routes were
   `GET /v1/conversations/{id}` and `DELETE`. Nothing could answer "what
   conversations do I have", so no UI could show them however well it was
   written.
2. **The one route back was a key in one browser's `localStorage`.** The
   workbench remembered the LAST conversation id under
   `forge.workbench.conversation` and restored that one. That is per-browser and
   per-profile — and in a private window it is discarded when the window closes,
   which is exactly how it was being used. A new browser reached none of it.

The console had never had a conversations or artifacts panel at all. Its four
cards were Sign in, Your projects, Waiting for you, and Goals. So the report was
accurate and this is a missing capability rather than a regression.

## Why it looked like data loss

Because the *project* survived and the *conversation* did not. Both are
localStorage keys, so the Variants rail came back populated while the transcript
came back empty — which reads as "some of my work was kept and some was thrown
away" rather than "one key is missing". The turns were in the table the whole
time.

## What was added

**`GET /v1/conversations`** — this person's conversations, most recently active
first. Scoped to the session; there is deliberately no owner parameter, so the
question "whose?" has exactly one answer this endpoint can give.

A conversation still has **no row of its own** and this does not give it one.
Every field is derived from the turns at read time, so nothing can drift out of
step with what was said — the same reasoning that kept a conversations table out
of the package originally.

`Opening` — the first thing the person said — is the label, because a
conversation has no title. Giving it one would mean asking a person to name
every thread they start, or asking a model to, which files a guess in a
permanent record. The first line is what they came to do, in their words, and it
is read back rather than written.

`ProjectID` is taken from the **last** turn that had one. A project is created by
the first thing worth keeping rather than by the first sentence, so early turns
carry none; reading the first would file nearly every conversation under "no
project", which is the shape of a list where nothing is grouped.

**Console: Conversations and Artifacts panels.** Artifacts adds **no endpoint** —
it reuses `GET /v1/geometry?project_id=` against the projects the console
already lists. One request per project is honest about what it costs; if a
deployment ever has enough projects for that to hurt, the fix is one endpoint
that spans them, not a cache in the page.

**Workbench: `?conversation=<id>`.** The console links here with the id. It only
writes the key and lets the existing `restoreConversation()` do the work —
including the 404 case, where a conversation that is not this person's forgets
the key rather than showing anything. A second copy of that logic would drift.

## Verification

Proven end to end on a live server against a real database, not only in tests:

- `GET /v1/conversations` returned both conversations newest-first with correct
  turn counts (`1 turn`, `3 turns`) and the opening line as the label.
- The console rendered both, with links carrying the id.
- `/workbench?conversation=cnv_demo1` restored all three turns **in order**,
  stripped the parameter from the URL, and left the id remembered.

Fences:

| Fence | What it holds |
|---|---|
| `TestListingBringsBackEveryConversationNewestFirst` | ordering, counts, the opening line, timestamps present |
| `TestAListingIsOnlyItsOwners` | account B gets none of A's; an unowned listing is refused rather than answered "you have none" |
| `TestAConversationTakesTheProjectItEndedIn` | a conversation with no project lists and does not invent one |
| `TestAPI_EveryConversationRouteIsMountedAndRequiresASession` | 401 not 404 — a handler with no route is the original bug again |
| `TestEveryConsolePanelHasAProducer` | every panel has both a mount point and a fetch; either half alone renders an empty card |
| `TestTheConsolePageHasEveryMountPoint` | the markup carries the ids the script writes into |

The last two were proven red by removing the card and the fetch, then restored
byte-identically (checksum verified).

## What this does not do

- **No search, no pagination.** The listing is capped at 200, newest first. A
  person with more than that cannot reach the oldest. Stated rather than hidden;
  the cap is one constant.
- **Artifacts are per project.** Something built before a project existed has no
  project to be listed under, and does not appear in that panel. It is still in
  the workbench's Variants rail.
- **Deleting is still per-conversation, from the workbench.** The console lists
  and links; it does not delete.
