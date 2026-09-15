-- 0023_failed_turns: a workbench turn whose reply could not be used leaves a trace.
--
-- PRD NFR-05 (observability, failures named), RSN-07 (the record of what was
-- said), SEC-04 (model output is untrusted), AUD-07 (deletion reachable).
--
-- ===========================================================================
-- 1. What was wrong (2026-09-15)
-- ===========================================================================
-- A live workbench turn failed after 2.7 s: the model answered, about 11,300
-- tokens were paid for, and the reply was refused because it had nothing to say
-- or show. The record held the person's half of the turn and nothing else. The
-- tokens were counted nowhere and the reply was gone, so why it was unusable
-- could only be guessed at. Migration 0022 §4 had named exactly this gap and
-- left it for "a feature nobody has asked for yet"; a live exercise asked.
-- docs/bugfix/2026-09-15-a-failed-workbench-turn-left-no-trace.md
--
-- ===========================================================================
-- 2. Why on the turn, and not in a log or a table of its own
-- ===========================================================================
-- For the reason 0022 §2 put the timings here: a failed reply is a fact about
-- this turn, and AUD-07's delete path (DELETE /v1/conversations/{id}) must take
-- it along. A log line holding a model's reply cannot be deleted with the
-- conversation it belongs to, and a reply can quote what the person said. The
-- raw reply therefore never goes to the log; the row does.
--
-- The FORGE half of a failed turn is what the person SAW: the sentence the
-- workbench showed in red. That keeps forge_conversation_turns_said_something
-- true without inventing speech, and a restored transcript shows the failure
-- where it happened.
--
-- ===========================================================================
-- 3. ‼️ The raw reply is untrusted and is not history
-- ===========================================================================
-- unusable_reply is model output that was refused. It is kept so somebody can
-- see why. It is never replayed to a model: the handler leaves failed turns out
-- of the history it builds, because a refused reply shown back as something
-- FORGE said would be a turn that never happened (SEC-04, RSN-06).

alter table forge_conversation_turns
    -- The error code the turn failed with (errs.Code). Null for every turn that
    -- did not fail, which is every turn recorded before this migration.
    add column if not exists failure text,
    -- The reply as it arrived, bounded by the application (agent.
    -- UnusableReplyLimit characters plus a truncation marker). Null when the
    -- model sent nothing at all.
    add column if not exists unusable_reply text;

-- Only FORGE's half fails. A human turn is what somebody typed; there is no
-- reply behind it to be unusable.
alter table forge_conversation_turns
    drop constraint if exists forge_conversation_turns_only_forge_fails;
alter table forge_conversation_turns
    add constraint forge_conversation_turns_only_forge_fails check (
        role = 'forge' or (failure is null and unusable_reply is null)
    );

-- A kept reply belongs to a failure. A successful turn's words are text and
-- detail; a second copy of them here would be a second record to disagree.
alter table forge_conversation_turns
    drop constraint if exists forge_conversation_turns_unusable_reply_is_a_failure;
alter table forge_conversation_turns
    add constraint forge_conversation_turns_unusable_reply_is_a_failure check (
        unusable_reply is null or failure is not null
    );

-- Bounded here as well as in the application, with room for the marker, so a
-- caller that forgot the bound is refused rather than filling the row.
alter table forge_conversation_turns
    drop constraint if exists forge_conversation_turns_unusable_reply_bounded;
alter table forge_conversation_turns
    add constraint forge_conversation_turns_unusable_reply_bounded check (
        unusable_reply is null or char_length(unusable_reply) <= 8100
    );

comment on column forge_conversation_turns.failure is
    'The error code a FORGE turn failed with, null when it did not fail (migration 0023).';
comment on column forge_conversation_turns.unusable_reply is
    'Untrusted: the refused model reply of a failed turn, bounded. Never replayed to a model (migration 0023 section 3).';
