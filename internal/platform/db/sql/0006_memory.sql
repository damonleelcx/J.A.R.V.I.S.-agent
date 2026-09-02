-- 0006_memory: layered memory becomes usable, and decisions get a record.
--
-- PRD MEM-01, MEM-02 and MEM-03. forge_memory has existed since 0004 with no
-- code behind it; this migration gives it the two things the PRD requires and
-- the original table could not express, and adds the decision log, which needs
-- a table of its own.
--
-- ---------------------------------------------------------------------------
-- 1. Five layers, not three
-- ---------------------------------------------------------------------------
-- MEM-01 names five: turn context, session notes, project knowledge, org
-- knowledge, personal preferences. 0004 shipped three. The two missing ones are
-- the SHORT-lived ones, which is the half that matters most: without them every
-- passing detail of a conversation either gets written to project knowledge —
-- where it outlives its relevance and then misleads — or is not written at all.
--
-- `turn` and `session` both belong to a goal, so the owner column is goal_id.
-- FORGE's unit of work is a goal, not a login: `forge_sessions` is an
-- authentication session and has nothing to do with a working session, and
-- pointing memory at it would tie what FORGE remembers to when somebody last
-- signed in.
--
-- The two existing names are kept as they are. `user` is MEM-01's "personal"
-- and `organisation` is its "org"; renaming a shipped column value to match the
-- PRD's prose would break every row for the sake of a synonym. The Go layer
-- table carries the PRD's name alongside the stored one.
--
-- ---------------------------------------------------------------------------
-- 2. Forgetting has to leave a mark
-- ---------------------------------------------------------------------------
-- MEM-02 requires a user to be able to delete an item. A plain DELETE does not
-- achieve that here, because FORGE writes memory on its own: the agent would
-- observe the same thing again on the next turn and write the row back, and the
-- deletion would have been theatre. So a forgotten item keeps its row and its
-- key, and loses its value. The key going on occupying the unique index is what
-- makes the deletion hold — a later `Remember` for that key is refused rather
-- than silently resurrecting it.
--
-- What is kept is deliberately the minimum: THAT the user asked to forget it,
-- when, and why. The content itself is cleared, because the user asked us to
-- forget the content. Purging the row entirely is a separate, operator-level
-- act, and it re-opens the key.
--
-- ---------------------------------------------------------------------------
-- 3. Every item says how it is known
-- ---------------------------------------------------------------------------
-- The seven categories of PRD RSN-05 (internal/domain/claim). A memory item is
-- a claim that outlives the conversation that produced it, which makes it the
-- one place where an unlabelled statement does the most damage: it is read back
-- later, by which time nobody remembers whether it was measured or guessed.
--
-- Note what `source` and `how` each answer, because they are easily confused and
-- 0004's comment on `source` blurred them. `source` is provenance — where the
-- content came from. `how` is epistemic status — what kind of knowing it is.
-- Neither answers "why did this query return this item", which is the third
-- thing MEM-02 asks for; that is per-retrieval, so it is derived at read time
-- and never stored.

alter table forge_memory add column if not exists goal_id text
    references forge_goals(id) on delete cascade;

-- Nullable, because a memory that has never been forgotten has no date on which
-- it was. `forgotten_at is null` is the live predicate everywhere.
alter table forge_memory add column if not exists forgotten_at    timestamptz;
alter table forge_memory add column if not exists forgotten_by    text references forge_users(id);
alter table forge_memory add column if not exists forgotten_reason text not null default '';

-- 'assumed' as the backfill value: an item already in the table was written
-- with nobody stating how it was known, and "chosen by FORGE because nobody
-- said" is exactly what that is. New rows always carry an explicit label; the
-- service refuses to write without one.
alter table forge_memory add column if not exists how text not null default 'assumed';

-- Constraints are dropped and re-added rather than altered, because Postgres has
-- no ADD CONSTRAINT IF NOT EXISTS and this migration, like every other in the
-- chain, is replayed on every boot.
alter table forge_memory drop constraint if exists forge_memory_scope_check;
alter table forge_memory add  constraint forge_memory_scope_check
    check (scope in ('turn','session','project','user','organisation'));

alter table forge_memory drop constraint if exists forge_memory_scope_has_owner;
alter table forge_memory add  constraint forge_memory_scope_has_owner
    check (
        (scope in ('turn','session') and goal_id    is not null) or
        (scope = 'project'           and project_id is not null) or
        (scope = 'user'              and user_id    is not null) or
        (scope = 'organisation')
    );

-- The vocabulary is closed in Go and closed here too. A label the database
-- accepts but the code does not recognise reads back as corrupt state, which is
-- a worse failure than a rejected write.
alter table forge_memory drop constraint if exists forge_memory_how_check;
alter table forge_memory add  constraint forge_memory_how_check
    check (how in ('observed','retrieved','calculated','simulated','inferred','assumed','proposed'));

-- A forgotten row must say who and when together: one without the other is a
-- deletion nobody can account for.
alter table forge_memory drop constraint if exists forge_memory_forgotten_complete;
alter table forge_memory add  constraint forge_memory_forgotten_complete
    check ((forgotten_at is null) = (forgotten_by is null));

-- One value per key per owner, for the two new layers. scope is part of the key
-- because turn and session are different layers: the same key may legitimately
-- hold a passing value in one and a durable note in the other.
create unique index if not exists forge_memory_goal_key
    on forge_memory (goal_id, scope, key) where scope in ('turn','session');

-- 0004 gave project and user scopes a unique key and left organisation without
-- one, so two org-wide items could claim the same key and a read would get
-- whichever the planner happened to return. Closed here.
create unique index if not exists forge_memory_org_key
    on forge_memory (key) where scope = 'organisation';

-- Recall reads live rows in one layer, newest first. The partial index keeps
-- forgotten rows out of the hot path while leaving them fully readable to the
-- inspection surface, which asks for them by name.
create index if not exists forge_memory_live_idx
    on forge_memory (scope, key) where forgotten_at is null;

comment on column forge_memory.how is
    'Epistemic status (PRD RSN-05): how this item is known. See internal/domain/claim.';
comment on column forge_memory.forgotten_at is
    'Set when a user forgets the item. The row and key survive so the deletion cannot be undone by the agent re-learning it; the value is cleared.';

-- ---------------------------------------------------------------------------
-- decisions (PRD MEM-03)
-- ---------------------------------------------------------------------------
-- A decision log is not an event log. forge_events records what happened;
-- this records what was CHOSEN, which alternatives were rejected and why, and
-- what evidence stood behind it. Those are not recoverable from a timeline: by
-- the time anyone asks "why is it built this way", the reasoning is the part
-- that has been lost, and the timeline only shows the consequence.
--
-- Supersession rather than mutation, because "we changed our minds" is itself a
-- decision with a date and an author. Editing the old row in place would erase
-- the fact that the old answer was ever believed, which is the single most
-- useful thing the log holds.
create table if not exists forge_decisions (
    id         text        primary key,
    project_id text        not null references forge_projects(id) on delete cascade,
    -- The goal this was decided during, when there was one. Decisions outlive
    -- goals — a project-level standard is chosen once and applies to everything
    -- after it — so this is nullable and the project is not.
    goal_id    text        references forge_goals(id) on delete set null,
    author_id  text        not null references forge_users(id),

    title      text        not null,
    -- What was decided, in a sentence somebody can act on.
    decision   text        not null,
    -- Why. Prose, because a rationale that fits a schema was not a real one.
    rationale  text        not null default '',

    -- [{"option": "...", "why_not": "..."}]. Rejected options are the half of a
    -- decision that is never written down and always asked for later: without
    -- them a reader cannot tell a considered choice from an unexamined default.
    alternatives jsonb not null default '[]'::jsonb,
    -- []claim.Claim — the evidence, each item carrying how it is known. A
    -- decision resting on a recalled figure and one resting on a measurement
    -- must not look the same in the log.
    evidence     jsonb not null default '[]'::jsonb,
    -- Artifact ids or paths this decision governs, so the blast radius of
    -- revisiting it is readable rather than remembered.
    affected     jsonb not null default '[]'::jsonb,

    -- The decision this one replaces. Set at insert and never updated: a
    -- supersession that can be re-pointed is a history that can be rewritten.
    supersedes_id text references forge_decisions(id),

    decided_at timestamptz not null,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),

    constraint forge_decisions_title_nonempty    check (length(trim(title)) > 0),
    constraint forge_decisions_decision_nonempty check (length(trim(decision)) > 0),
    -- A decision cannot supersede itself. The longer cycles are structurally
    -- impossible: supersedes_id may only name a row that already exists, and
    -- nothing updates it.
    constraint forge_decisions_no_self_supersede check (supersedes_id is null or supersedes_id <> id)
);

-- At most one successor per decision, so "what is the current answer?" has
-- exactly one. Without this, two people could each supersede the same decision
-- and the log would hold two contradictory currents with no way to choose.
create unique index if not exists forge_decisions_supersedes_once
    on forge_decisions (supersedes_id) where supersedes_id is not null;

create index if not exists forge_decisions_project_idx
    on forge_decisions (project_id, decided_at desc);
create index if not exists forge_decisions_goal_idx
    on forge_decisions (goal_id, decided_at desc) where goal_id is not null;

do $$ begin
    if not exists (select 1 from pg_trigger where tgname = 'forge_decisions_updated_at') then
        create trigger forge_decisions_updated_at before update on forge_decisions
            for each row execute function forge_set_updated_at();
    end if;
end $$;

comment on table forge_decisions is
    'PRD MEM-03. What was chosen, what was rejected, why, and on what evidence. Superseded, never edited.';
