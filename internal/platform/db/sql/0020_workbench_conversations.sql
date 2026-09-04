-- 0020_workbench_conversations: keep what was said at the workbench.
--
-- PRD RSN-07 (resume from a structured checkpoint, not a conversation summary),
-- AUD-01 (barge-in without losing project state), MEM-01 (layered memory with
-- distinct retention), AUD-07 (delete-session always reachable).
--
-- ===========================================================================
-- 1. What was wrong
-- ===========================================================================
-- The workbench conversation was never written down. `history` was posted BY
-- THE BROWSER on every turn and the only turn table in this schema was
-- forge_room_turns, which belongs to rooms. So a reload lost the conversation:
-- the variants came back, the requirements came back, the project came back,
-- and the thread that produced all of them did not.
--
-- RSN-07 asks to resume from a structured checkpoint rather than a summary. The
-- agentic side has exactly that — checkpoints, resume state, a recovery drill.
-- The conversational side, which is the product's front door, had nothing.
--
-- ===========================================================================
-- 2. Why there is no forge_conversations table beside this one
-- ===========================================================================
-- The same reason there is no forge_variants table: the thing already exists as
-- its parts. A conversation IS its turns — it has no title, no members, no
-- lifecycle and no state of its own, and a row holding only an id and an owner
-- would be a second place for those two facts to disagree with the turns.
--
-- So a conversation exists exactly when it has a turn, its owner is its turns'
-- owner, and deleting its turns deletes it. The id is minted by the SERVER on
-- the first turn: a client that could name a conversation could name somebody
-- else's.
--
-- ===========================================================================
-- 3. Retention (PRD MEM-01)
-- ===========================================================================
-- Kept until the person deletes it. No expiry, because an expiry nobody chose
-- is a promise this schema cannot keep on its own — a sweeper would have to
-- exist and be running. What DOES exist is the delete path, which AUD-07
-- requires to be reachable at all times: DELETE /v1/conversations/{id}, and the
-- control beside the conversation in the workbench.
--
-- Deleting a person's account takes their conversations with it (cascade).
-- Deleting a PROJECT does not: the turns are still what was said, and their
-- project reference goes null rather than the record going with it.

create table if not exists forge_conversation_turns (
    id              text        primary key,
    conversation_id text        not null,
    owner_id        text        not null references forge_users(id) on delete cascade,

    -- The project this turn belonged to AT THE TIME, and null before there was
    -- one. A project is created by the first thing worth keeping, not by the
    -- first sentence, so early turns genuinely belong to no project — which is
    -- a fact about the conversation rather than a gap in it.
    project_id      text        references forge_projects(id) on delete set null,

    seq             integer     not null,

    -- human | forge. The same two-value vocabulary the room record uses, and
    -- deliberately not the llm role names: this is what was SAID in a product,
    -- not what was sent to a provider.
    role            text        not null,

    -- What was said. For FORGE this is the spoken half, which is kept short on
    -- purpose (PRD §5.3).
    text            text        not null default '',
    -- The long-form half of a reply, which the screen carries while the speech
    -- stays short. Always empty for a human turn.
    detail          text        not null default '',

    -- How many pictures were attached to the turn. The bytes are NOT kept: an
    -- image is an input to one turn (PRD VIS-01), there is no asset store to put
    -- one in, and a count is what can be recorded truthfully.
    images          integer     not null default 0,

    said_at         timestamptz not null,

    constraint forge_conversation_turns_role_check
        check (role in ('human','forge')),
    -- A turn that said nothing at all is not a turn. Either half may be empty;
    -- both may not.
    constraint forge_conversation_turns_said_something
        check (length(trim(text)) > 0 or length(trim(detail)) > 0),
    -- The detail is FORGE's half of the screen. A human turn carrying one would
    -- mean somebody's message had been split by something that does not exist.
    constraint forge_conversation_turns_human_has_no_detail
        check (role <> 'human' or length(trim(detail)) = 0),
    constraint forge_conversation_turns_images_nonneg check (images >= 0),

    unique (conversation_id, seq)
);

-- Every read is "this person's conversation, in order", and every write reads
-- the last seq first. One index serves both.
create index if not exists forge_conversation_turns_owner_idx
    on forge_conversation_turns (owner_id, conversation_id, seq);

comment on table forge_conversation_turns is
    'What was said at the workbench (PRD RSN-07). A conversation is its turns: there is no '
    'conversations table, its owner is its turns owner, and deleting the turns deletes it. '
    'Kept until the person deletes it — see DELETE /v1/conversations/{id}.';
