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

## Follow-up, same day: the matching moved to the server

The fix above filtered in the browser, and the reasoning for that still reads
correctly: `GET /v1/rooms/{id}` returns every turn, so the whole transcript was
already in the page, and filtering it needed no round trip and worked the same on
a closed room.

**What it could not do is agree with itself.** `indexOf` is asymmetric —
searching "bracket" finds the turn saying "brackets", and searching "brackets"
finds nothing at all, because containment runs one way only. In a room where the
record is what people are agreeing to, a search that quietly returns nothing is a
worse failure than one that returns too much, and it is not a failure anybody can
see happening.

Postgres stems both words to the same lexeme, so the two searches agree. The
matching now runs in `collab.Service.SearchTurns` behind
`GET /v1/rooms/{id}/search`, indexed by a generated `tsvector` column added in
`0013_transcript_search.sql`.

### What this cost, stated rather than buried

- **A round trip per pause in typing.** Debounced at 180 ms, so it is one request
  per pause and not one per keystroke — verified as one request for eight typed
  characters. Clearing the box still needs no server at all: the record is all
  here, which is the part of the original design that survives.
- **Highlighting is now best effort.** The server decides which turns match, by
  stemming; `fillHighlighted` marks literal substrings. They agree for the
  ordinary case and disagree exactly where stemming earns its place — searching
  "brackets" returns the turn saying "bracket" with nothing marked inside it. The
  alternatives were `ts_headline`, which means rendering server-supplied markup
  on a page that uses no `innerHTML` anywhere, or a second stemmer in JavaScript,
  which is the two-matchers problem this change exists to remove.
- **A failure state that did not exist before.** A local filter cannot fail. A
  request can, and showing the unfiltered transcript under a filled-in search box
  would read as "everything matched". The page shows no turns and says why.

### Why full text rather than a trigram index on LIKE

Trigram would have kept substring semantics and indexed them. It needs
`pg_trgm`, and `TestNoExtensionsAreRequired` refuses extensions in this chain —
`CREATE EXTENSION IF NOT EXISTS` is evaluated per database and installs into one
schema, so it silently does nothing in every schema after the first. `tsvector`
and GIN are core Postgres and need no privileges a customer has to grant.

### Redaction, twice

SEC-06 redaction blanks the text, so the **generated** column recomputes to an
empty tsvector: a deleted turn's words leave the index without anybody
remembering to remove them. The query filters `redacted_at` as well.

That second reason is redundant through the ordinary path, which would normally
make it decoration — a guard no test can fail. It is fenced by
`TestRoom_SearchRefusesARedactedTurnEvenWithItsTextIntact`, which constructs by
hand the state a future half-applied deletion would write (redaction stamped, the
text left behind) and asserts the query still refuses it. Deleting the filter
turns that test red.

### Verification

`TestRoom_TranscriptIsSearchable` and `TestRoom_RedactionEmptiesTheSearchIndexItself`
(live Postgres), `TestAPI_TheTranscriptCanBeSearchedOverHTTP`, and search added to
`TestAPI_ARoomRefusesSomebodyOutsideItsProject` and to the mounted-routes fence.

**Drills, each watched fail:**

| mutation | fence that fired |
|---|---|
| substring matching instead of full text | `TestRoom_TranscriptIsSearchable` (plural returns 1, not 2) |
| the `redacted_at` filter removed | `TestRoom_SearchRefusesARedactedTurnEvenWithItsTextIntact` |
| room scoping removed | `TestRoom_TranscriptIsSearchable` (another room's turn appears) |

Two earlier drill attempts reported "ok" without having applied their mutation at
all — the anchor matched twice in the file and the replacement was skipped. They
are recorded here because a drill that silently does not mutate is a green that
means nothing, which is the same failure shape as the fences it is checking.

**In a browser**, against the real server and database, four turns in a room:

- typing `brackets` → *"2 of 4 turns match"*, returning both the plural turn and
  the singular one. Under the previous filter this returned one.
- exactly **one** network request for eight typed characters.
- `Clear` → all four turns back, no request.
- server stopped, then typing → *"Search is unavailable right now, so no turns
  are shown."* with nothing rendered.

That last check found a real defect: the message first read *"the transcript
above is unfiltered"* while the page was in fact showing nothing. The behaviour
was right and the sentence was wrong, and only a live run could have caught it —
no test asserts the wording. Corrected before commit.

## Related

- PRD **AUD-06**; `docs/bugfix/2026-09-03-coordinates-read-back-without-axes-or-sign.md`
  is the other half of the audio surface closed the same day.
- `internal/platform/db/sql/0013_transcript_search.sql` carries the reasoning for
  the generated column, the regconfig, and what is deliberately not indexed.
