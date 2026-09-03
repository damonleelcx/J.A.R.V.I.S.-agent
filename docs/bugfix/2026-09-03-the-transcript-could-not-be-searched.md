# The transcript could not be searched

**Date:** 2026-09-03 · **Phase:** 9.7 · **Severity:** medium — AUD-06 unmet, and
unmet in the half of it that exists for people who cannot simply scroll and skim
· **Owner:** the room transcript UI, `assets/room-page.js`

## Symptom

A room's transcript is a flat list that only grows. To find where somebody
mentioned the wall thickness, you scrolled and read.

For a sighted user with a short meeting that is an annoyance. AUD-06 is an
**accessibility** requirement, and for the people it is written for it is not an
annoyance: a screen-reader user has no equivalent of skimming. Reaching the
fortieth turn means hearing thirty-nine. "Find what was said about the bore" had
no answer that did not involve listening to the whole meeting again.

Nothing was broken. Every other part of AUD-06 was built — captions, keyboard
operation, ARIA live regions, adjustable speech rate, a typed path for every
critical interaction. Search was simply never built, and its absence produces no
error, no log line and no failing test.

## Root cause

Not a regression. AUD-06 names six things and five were implemented; there was
no search over turns anywhere in the codebase.

Worth naming why it stayed missing: the other five are all properties of a
surface that exists, so building the surface produced them. Search is a
capability nobody notices the lack of while the transcripts under test are five
turns long. It is the requirement's only line item with no natural moment of
discovery.

## Fix

Search filters the transcript **in the page**, in `assets/room-page.js`, with
markup in `pages.go` and styles in `assets/room.css`.

### Why not a search endpoint

`GET /v1/rooms/{id}` already returns *every* turn in the room — `collab.Find`
runs `order by seq` with no limit, and `toRoomDTO` serialises all of it. The
whole transcript is therefore in the browser before anybody types.

A `?q=` parameter or a `/search` endpoint would have been a second way to ask a
question the page can already answer, and a slower one: a network round trip per
keystroke to filter an array that is in memory. It would also have needed its own
authorisation path, its own SQL, and — to be worth having — an index, which means
a migration. The rule about not adding API surface or tables that earn nothing
applies exactly here.

The honest limit of that choice: search is bounded by what the page already
loaded, which today is everything. **If room transcripts are ever paginated, this
becomes wrong on the same day** — the page would then hold a window, and
searching a window while presenting it as searching the transcript is the kind of
quiet half-truth this codebase avoids elsewhere. That is the condition to watch,
and it is recorded in the code at the markup.

### What it does

- Filters on every keystroke; no Enter, nothing to wait for.
- Case-insensitive, matching the **text of what was said** and nothing else.
- Matched runs are wrapped in `<mark>`, built as DOM nodes. The page uses no
  `innerHTML` anywhere and a transcript is the last place to start: every
  character of it is something a person typed or said.
- Escape clears from the keyboard; a Clear button clears without one and returns
  focus, so neither path strands anybody.

### Three decisions worth stating

**A redacted turn can never match.** Its content is gone. The word its row shows
instead — "deleted" — belongs to this interface, not to anybody in the room, so
matching on it would be searching our own vocabulary and presenting the hit as
something a person said.

**The count is a separate live region.** `#turns` is `aria-live="polite"` so that
turns *arriving* are announced. A filter rewrites every row at once, which is not
news — without care, a screen reader reads the entire transcript back on every
keystroke. The rebuild is wrapped in `aria-busy`, and what is actually announced
is one line: `3 of 5 turns match "wall"`. The denominator is there so that a
search matching nothing is distinguishable from a transcript that is empty.

**A turn arriving mid-search still joins the record.** It is appended to the
in-memory array whether or not it matches, and the count's denominator moves, so
the room does not appear frozen. Clearing the search brings it back with no
refetch.

## Verification

Driven in a real browser against the real page, served by `forged` on live
Postgres — not a stub of it, and not a reimplementation of the filter:

- 5 turns, search `wall` → **3 of 5**, three `<mark>`s, matching `wall`, `wall`
  and `WALL` (case-insensitive, each highlighted in its own case).
- The redacted turn is excluded, and so is the turn that mentions the bore.
- `aria-busy` is **not** left set after a rebuild — if it were, the live region
  would be muted permanently and arriving turns would stop being announced.
- Highlights are element nodes (`nodeType === 1`), confirming no markup path.
- A non-matching turn arriving mid-search: rows stay 3, denominator moves 5 → 6.
- A matching turn arriving mid-search: rows 4, denominator 7, four marks.
- Escape and the Clear button both restore all rows; sequence after clearing is
  `1,2,3,4,5,6,7`, so the two turns that arrived *during* the search came back
  without a refetch. Clear returns focus to the field.
- A whitespace-only query is not a search — trimmed, so the transcript is whole.
- No matches → `No turns match "zzz-nothing".`

## Prevention

`TestTheTranscriptCanBeSearched` in `internal/httpapi/pages_test.go` fences the
markup the script and the reader both depend on: the input, `type="search"`, the
label, the clear control, the count element, and `role="status"`. Remove any of
them and the script silently does nothing — which is precisely the failure that
has no other signal.

**Drill run:** deleting the label and `role="status"` from `pages.go` turns it
red, naming which one went and why AUD-06 needs it. The file was restored
byte-identical afterwards.

The fence cannot check the filtering itself; there is no JavaScript test harness
in this repository. That is why the browser run above is written out in full
rather than summarised — it is the only record of what was actually observed.

## Related

- PRD **AUD-06**; `docs/bugfix/2026-09-03-coordinates-read-back-without-axes-or-sign.md`
  is the other half of the audio surface closed the same day.
