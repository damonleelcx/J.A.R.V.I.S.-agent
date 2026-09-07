-- 0022_turn_timings: keep the numbers the Telemetry panel measures.
--
-- PRD NFR-05 (observability across latency, model selection, retrieval, plans,
-- tool calls, renders, policy, approvals, failures), AUD-02 (a threshold on
-- one of them).
--
-- ===========================================================================
-- 1. What was wrong
-- ===========================================================================
-- Every turn was already measured. The server timed how long the model took to
-- produce its first token and how long the whole reply took, counted the tokens,
-- and knew which model answered — and then wrote all four to the LOG and
-- nothing else. There was no endpoint that read them back.
--
-- So the Telemetry panel could only show what the browser itself had watched
-- happen: it emptied on reload, it knew nothing about yesterday, and its own
-- "Not measured here" list had to say so. NFR-05 was the one PRD line with a
-- measurement behind it and no way to look at it. What was missing was never the
-- measurement. It was a store.
--
-- ===========================================================================
-- 2. Why these are COLUMNS and not a forge_turn_telemetry table
-- ===========================================================================
-- There is already exactly one row per FORGE turn: forge_conversation_turns.
-- The timings are facts ABOUT that turn — the same turn, the same owner, the
-- same moment — and a second table keyed on the same thing would be a second
-- place for "which turn" to be wrong, plus a second thing to delete.
--
-- That last part is not a convenience. AUD-07 requires deletion to always be
-- reachable, and DELETE /v1/conversations/{id} is the path. Timings living on
-- the turn are deleted BY the existing path, on the day it is written, with no
-- second sweeper to forget. A separate table would have needed its own cascade
-- and its own test, and the failure mode of getting that wrong is a person
-- deleting a conversation while a record of when they had it survives.
--
-- ===========================================================================
-- 3. Why every one of them is NULLABLE
-- ===========================================================================
-- Absence has to keep meaning absence. Turns recorded before this migration were
-- genuinely not measured, and so is any turn where the model produced no speech
-- to time. A default of 0 would make an unmeasured turn indistinguishable from
-- an instantaneous one — and the panel's own rule, written before this existed,
-- is that a measurement that does not exist renders as an em dash and a reason,
-- never as a zero, because zero is a number somebody reads as "instant".
--
-- ===========================================================================
-- 4. What is still NOT stored, and why it is not being invented here
-- ===========================================================================
-- A turn that FAILED. Nothing is written for one today: the conversation record
-- is appended when the reply lands, and a turn that produced no reply has
-- nothing to append — the table's own constraint refuses a turn that said
-- nothing, which is correct for a RECORD OF WHAT WAS SAID. Failures are in the
-- log with their error code. Making them visible in the panel needs somewhere
-- that is not the said-record to put them, and that is a table this migration
-- deliberately does not create for a feature nobody has asked for yet.

alter table forge_conversation_turns
    -- Which model answered. NFR-05 names "model selection" and the panel already
    -- shows it per turn from the live stream; this is the same fact, kept.
    add column if not exists model text,
    -- Server clock, in milliseconds: request in, to first speech token out. The
    -- model's part of the wait, and the only half of AUD-02 this system is in a
    -- position to measure — the browser's clock starts at Send, which is a
    -- different question and stays in the browser.
    add column if not exists first_token_ms integer,
    -- Server clock: the whole turn, including the structured tail.
    add column if not exists total_ms integer,
    -- Server clock, measured OUTSIDE the model call: what the handler took end
    -- to end. Kept apart from total_ms because the difference between them is
    -- this system's own overhead, and averaging the two would hide it.
    add column if not exists round_trip_ms integer,
    -- Tokens the provider reported for the turn. bigint because a budget is
    -- counted in these and an integer overflows at two billion, which one long
    -- conversation can reach.
    add column if not exists tokens bigint;

-- Nonsense is refused rather than stored. A negative duration is a clock that
-- went backwards or an arithmetic error, and it would drag a median somewhere
-- no measurement can be.
alter table forge_conversation_turns
    drop constraint if exists forge_conversation_turns_timings_nonneg;
alter table forge_conversation_turns
    add constraint forge_conversation_turns_timings_nonneg check (
        (first_token_ms is null or first_token_ms >= 0) and
        (total_ms       is null or total_ms       >= 0) and
        (round_trip_ms  is null or round_trip_ms  >= 0) and
        (tokens         is null or tokens         >= 0)
    );

-- A human turn is not timed. There is no model call behind it, so a figure there
-- would be a measurement of nothing attributed to a person.
alter table forge_conversation_turns
    drop constraint if exists forge_conversation_turns_only_forge_is_timed;
alter table forge_conversation_turns
    add constraint forge_conversation_turns_only_forge_is_timed check (
        role = 'forge' or (model is null and first_token_ms is null and
                           total_ms is null and round_trip_ms is null and tokens is null)
    );

-- The panel's read is "this person's measured turns, newest first". Partial, so
-- the index holds only the rows that HAVE a measurement — which is what is being
-- read, and which keeps every human turn and every pre-migration turn out of it.
--
-- Keyed on round_trip_ms and not on first_token_ms, which was the first attempt.
-- The handler measures its own elapsed time unconditionally, so round_trip_ms is
-- present for every turn recorded from now on; first_token_ms is absent whenever
-- the model did not STREAM, and a deployment whose model returns whole replies
-- would have had an empty telemetry panel and no way to tell that from an idle
-- one. "Was anything measured about this turn" is the question, and the round
-- trip is the figure that always answers it.
create index if not exists forge_conversation_turns_timed_idx
    on forge_conversation_turns (owner_id, said_at desc)
    where round_trip_ms is not null;

comment on column forge_conversation_turns.first_token_ms is
    'Server clock, ms: request in to first speech token out (PRD AUD-02, NFR-05). '
    'Null means NOT MEASURED, never zero — see migration 0022 section 3.';
