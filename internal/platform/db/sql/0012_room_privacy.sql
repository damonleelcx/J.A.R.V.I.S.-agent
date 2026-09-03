-- 0012_room_privacy: what happens to what is said in a room, and undoing it.
--
-- PRD SEC-06 (visible recording state, retention-free mode, independent audio
-- deletion) and AUD-07 (end-recording, delete-session).
--
-- ===========================================================================
-- 1. The fact that shapes all of this: NO AUDIO IS STORED
-- ===========================================================================
-- The media plane forwards RTP and never writes it anywhere. Transcription
-- buffers a few seconds of Opus in memory, sends it to the speech provider, and
-- drops it. Nothing in this schema holds audio, and nothing in wave 9.x ever
-- added a place to put it.
--
-- That makes SEC-06's three requirements land differently from how they read:
--
--   "retention-free mode"        -> audio retention is already zero. What a room
--                                   can choose is whether what is said is
--                                   TRANSCRIBED, because the transcript is the
--                                   only thing that persists.
--   "independent audio deletion" -> there is no audio to delete independently of
--                                   the transcript. What can be deleted
--                                   independently is the VOICE-DERIVED half of
--                                   the transcript, leaving typed turns and the
--                                   room itself intact.
--   "visible recording state"    -> the honest statement is not "recording" at
--                                   all. It is: audio is forwarded live, is sent
--                                   to a speech provider while transcribing is
--                                   on, and is never stored.
--
-- Stated here rather than in a comment on one column, because a future reader
-- who assumes there is an audio table will design the wrong thing.
--
-- ===========================================================================
-- 2. Transcribing is per ROOM, and it is a privacy control
-- ===========================================================================
-- A meeting that is off the record is an ordinary thing to want, and it is the
-- only meaningful form "retention-free" can take here. It lives on the room
-- rather than in configuration because it is a decision the people in the room
-- make about that conversation, not one an operator makes about the deployment.
--
-- Default TRUE, matching the deployment default: a room that carries audio and
-- records nothing fails COL-01, which asks for a record of who said what.
-- Turning it off is a choice somebody makes and everybody in the room can see.
alter table forge_rooms
    add column if not exists transcribing boolean not null default true;

comment on column forge_rooms.transcribing is
    'Whether speech in this room is transcribed into forge_room_turns (PRD SEC-06). No audio is stored either way; this decides whether the TRANSCRIPT is. Visible to everybody in the room.';

-- ===========================================================================
-- 3. Deletion is redaction, and the fact of it survives
-- ===========================================================================
-- SEC-06 says a person may delete what they said. COL-01 says the room record is
-- an auditable account of who said what and which approvals were made while they
-- said it. Those pull in opposite directions and this is where they are
-- reconciled rather than averaged.
--
-- The resolution: the CONTENT goes, the FACT does not. A redacted turn keeps its
-- sequence number, its speaker, and its timestamp, and loses its text. So the
-- transcript can still say "Priya spoke at 14:02 and that turn was deleted by
-- Priya at 15:10" — which is both a real deletion and an honest record.
--
-- The alternative, deleting the row, would silently renumber nothing and leave a
-- gap in a sequence that other rows reference. An auditor reading it would see a
-- conversation that never had those seconds in it, which is a different and
-- worse kind of untrue.
alter table forge_room_turns
    add column if not exists redacted_at timestamptz;
alter table forge_room_turns
    add column if not exists redacted_by text references forge_users(id) on delete set null;

-- The text rule now has an exception, and only one: a turn has text unless it
-- was redacted.
--
-- Dropped and recreated rather than added alongside, because two constraints
-- covering the same column would eventually disagree about which is authoritative.
-- Both statements are idempotent, so re-running the chain is harmless.
alter table forge_room_turns
    drop constraint if exists forge_room_turns_text_nonempty;
alter table forge_room_turns
    add constraint forge_room_turns_text_nonempty
    check (redacted_at is not null or length(trim(text)) > 0);

-- Redaction names who did it, always. An erasure nobody made cannot be
-- questioned, which is the same rule as approvals and incident actions.
alter table forge_room_turns
    drop constraint if exists forge_room_turns_redaction_attributed;
alter table forge_room_turns
    add constraint forge_room_turns_redaction_attributed
    check ((redacted_at is null and redacted_by is null)
        or (redacted_at is not null and redacted_by is not null));

create index if not exists forge_room_turns_redacted_idx
    on forge_room_turns (room_id) where redacted_at is not null;

comment on column forge_room_turns.redacted_at is
    'When this turn''s content was deleted (PRD SEC-06). The row survives so the transcript can say a turn was here and was removed; deleting the row would leave a gap an auditor would read as silence.';

-- ===========================================================================
-- 4. How a participant is taking part right now
-- ===========================================================================
-- AUD-07 asks for mute and pause as always-reachable controls. Both are states
-- of a live connection rather than facts about the meeting, so neither is stored
-- here: they live in memory with the stream and are published to the room.
--
-- Named here so the next person does not go looking for a column. What IS
-- durable about somebody's participation — that they were present, and when — is
-- already forge_room_participants.
