-- 0013_transcript_search: finding what was said in a room.
--
-- PRD AUD-06 (accessibility: captions, transcript search, keyboard-only, screen
-- reader, adjustable rate, a non-audio path for every critical interaction).
--
-- ===========================================================================
-- 1. Why the database searches at all
-- ===========================================================================
-- The room page could already filter the transcript in the browser, because
-- GET /v1/rooms/{id} returns every turn in the room. That is correct for as long
-- as it is true, and it names its own expiry: a windowed transcript would leave
-- the page searching the window while calling it "the transcript".
--
-- What it cannot do at any size is match words rather than characters. A
-- substring filter is asymmetric in a way that surprises people: searching
-- "bracket" finds "brackets", and searching "brackets" finds nothing, because
-- one string contains the other and not the reverse. In a room where the record
-- is what people are agreeing to, a search that quietly returns nothing is worse
-- than one that returns too much.
--
-- Full text stems both to the same lexeme, so the two searches agree.
--
-- ===========================================================================
-- 2. Why full text and not a trigram index on LIKE
-- ===========================================================================
-- Trigram would have preserved substring semantics and indexed them. It needs
-- pg_trgm, and this chain refuses extensions — see TestNoExtensionsAreRequired,
-- which exists because CREATE EXTENSION IF NOT EXISTS is evaluated per DATABASE
-- and installs into ONE schema, so it silently does nothing in every schema
-- after the first and leaves its operators unresolvable.
--
-- tsvector, to_tsvector and GIN are core Postgres. Nothing here has to be
-- installed, which is what makes it deployable into a customer's database
-- without asking them for superuser.
--
-- ===========================================================================
-- 3. Why a generated column rather than a trigger
-- ===========================================================================
-- A trigger is a second place the value can be wrong: it maintains a copy, and a
-- copy can be missed by an UPDATE path that forgets it. A generated column is
-- computed by the database from the row itself, so it cannot disagree with the
-- text it indexes — there is no write path that can skip it.
--
-- That property is what makes redaction safe here, and it is worth being
-- explicit about because it is doing real work:
--
--   SEC-06 redaction sets text = '' and stamps redacted_at. The generated column
--   recomputes on that UPDATE to an empty tsvector, which matches no query at
--   all. A redacted turn therefore cannot be found by searching for what it used
--   to say — not because a query remembered to exclude it, but because the words
--   are not in the index any more.
--
-- The read path filters redacted turns anyway. Two independent reasons a
-- deletion holds is the right number for a deletion.
--
-- ===========================================================================
-- 4. Why the regconfig is written out
-- ===========================================================================
-- to_tsvector(text) — the one-argument form — resolves its configuration from
-- default_text_search_config, a per-SESSION setting. It is therefore STABLE
-- rather than IMMUTABLE and cannot be used in a generated column at all; had it
-- been allowed, the same sentence would index differently depending on who
-- connected. to_tsvector('english'::regconfig, text) resolves the configuration
-- once, at DDL time, and stores the OID in the expression.
--
-- 'english' is stated rather than chosen: the product's interface language is
-- English (see the code-and-UI-language principle). When that stops being true
-- this column is where it is decided, and changing it is a rewrite of the index
-- rather than a setting.
--
-- ===========================================================================
-- 5. What is NOT indexed, and why
-- ===========================================================================
-- Only the text of what was said. Not speaker_label: a search for "Priya" that
-- returned every turn Priya spoke would be a filter by speaker wearing the
-- clothes of a text search, and the two want different interfaces — one is a
-- roster click, the other is a query. Indexing the label would also make a turn
-- ABOUT somebody indistinguishable from a turn BY them.

alter table forge_room_turns
    add column if not exists search_vector tsvector
    generated always as (to_tsvector('english'::regconfig, text)) stored;

comment on column forge_room_turns.search_vector is
    'Full-text index of the turn text for transcript search (PRD AUD-06). Generated, so it cannot drift from text and is emptied automatically when SEC-06 redaction blanks it.';

-- GIN rather than GiST: this index is read far more often than it is written,
-- and GIN is the faster of the two for lookups at the cost of a slower build.
-- Turns are written one short sentence at a time, so the write cost is noise.
--
-- Named and guarded per schema. Index names live in pg_class scoped by
-- namespace, so IF NOT EXISTS resolves against the table's own schema — unlike
-- a hand-written guard against a per-database catalogue, which is how every
-- schema after the first once silently lost its triggers
-- (docs/bugfix/2026-09-02-trigger-guards-were-not-schema-scoped.md).
create index if not exists forge_room_turns_search_idx
    on forge_room_turns using gin (search_vector);
