-- 0007_workspace_model: the reasoning structure, the project graph, and the
-- artifact lifecycle.
--
-- PRD RSN-01, WRK-03 and WRK-04.
--
-- ===========================================================================
-- 1. Why RSN-01 and WRK-03 are ONE graph and not two systems
-- ===========================================================================
-- RSN-01 asks for goals, requirements, constraints, assumptions, decisions,
-- risks and success criteria. WRK-03 asks for requirements, components,
-- interfaces, files, tests, hazards, decisions, owners and evidence.
--
-- Both lists contain REQUIREMENTS and DECISIONS. Building them as two tables
-- means those two exist twice from the first day, and the copies start
-- disagreeing as soon as anybody edits one — which is the failure that a
-- "reasoning model" and a "project graph" would each be blamed for separately.
--
-- So: one node table with a closed kind vocabulary, one typed edge table, and a
-- rule table in Go saying which edge kinds may connect which node kinds. RSN-01
-- is the reasoning half of the vocabulary; WRK-03 is the structural half plus
-- the edges. Neither is a system in its own right.
--
-- ===========================================================================
-- 2. Why some nodes hold content and others only hold identity
-- ===========================================================================
-- Goals, decisions, owners, tasks and artifacts already have tables. Copying
-- them into the graph would be the same split this design exists to avoid, one
-- level down.
--
-- So a node is one of two things, declared per kind in Go (internal/domain/
-- workspace/model.go) and enforced here:
--
--   OWNED   — the graph is where this lives. Title and body are in the row.
--             requirement, constraint, assumption, risk, hazard, criterion,
--             component, interface, test, evidence.
--
--   ANCHOR  — the content lives in its own table and this row is an identity
--             anchor so edges can reach it. Exactly one ref column is set, and
--             it is a real foreign key with a real cascade.
--             goal, decision, owner, artifact.
--
-- The alternative was polymorphic (kind, id) endpoints on the edge table, which
-- no database can enforce: every edge to a deleted goal would survive as a
-- dangling pointer and the graph would slowly fill with references to things
-- that are gone. Anchoring costs one find-or-create on a necessary path and buys
-- referential integrity for the whole graph.
--
-- Note on vocabulary: WRK-03 says "files" and WRK-04 says "artifacts". They are
-- the same thing and the term used throughout is ARTIFACT, because a file on
-- disk is only one of the shapes it takes.

create table if not exists forge_nodes (
    id         text        primary key,
    project_id text        not null references forge_projects(id) on delete cascade,

    -- The closed vocabulary. Owned kinds carry content; anchor kinds carry a ref.
    kind       text        not null,

    -- Content, for owned kinds. Empty on anchors, where the real row has it.
    title      text        not null default '',
    body       text        not null default '',

    -- How this is known (PRD RSN-05, internal/domain/claim). An assumption is
    -- ASSUMED by construction and the Go layer refuses anything else for that
    -- kind; the point of labelling something an assumption is so that later
    -- somebody can ask what was built on top of a guess.
    how        text        not null default 'proposed',
    source     text        not null default '',

    -- open | accepted | rejected | retired. Deliberately not "done": a
    -- requirement is not finished, it is agreed to or it is not.
    status     text        not null default 'open',

    -- Anchor references. Exactly one is set on an anchor kind, none on an owned
    -- kind. Real foreign keys, so deleting a goal takes its anchor and every
    -- edge touching it, rather than leaving a pointer to nothing.
    goal_id     text references forge_goals(id)     on delete cascade,
    decision_id text references forge_decisions(id) on delete cascade,
    owner_id    text references forge_users(id)     on delete cascade,
    artifact_id text,  -- forward reference; the constraint is added below

    created_by text        not null references forge_users(id),
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),

    constraint forge_nodes_kind_check check (kind in (
        'requirement','constraint','assumption','risk','hazard','criterion',
        'component','interface','test','evidence',
        'goal','decision','owner','artifact')),
    constraint forge_nodes_how_check check (how in
        ('observed','retrieved','calculated','simulated','inferred','assumed','proposed')),
    constraint forge_nodes_status_check check (status in ('open','accepted','rejected','retired')),

    -- An owned node with no title is unreadable, and an unreadable node in a
    -- graph is worse than a missing one: it shows up in every traversal and
    -- tells nobody anything.
    constraint forge_nodes_owned_has_title check (
        kind in ('goal','decision','owner','artifact') or length(trim(title)) > 0),

    -- Exactly one ref for an anchor, none for an owned node. Written out rather
    -- than counted, because the error a reader needs is "this anchor points at
    -- the wrong kind of thing", not "the number of non-null columns was two".
    constraint forge_nodes_anchor_ref check (
        (kind = 'goal'     and goal_id     is not null and decision_id is null and owner_id is null and artifact_id is null) or
        (kind = 'decision' and decision_id is not null and goal_id     is null and owner_id is null and artifact_id is null) or
        (kind = 'owner'    and owner_id    is not null and goal_id     is null and decision_id is null and artifact_id is null) or
        (kind = 'artifact' and artifact_id is not null and goal_id     is null and decision_id is null and owner_id is null) or
        (kind not in ('goal','decision','owner','artifact')
             and goal_id is null and decision_id is null and owner_id is null and artifact_id is null)
    )
);

-- One anchor per external thing per project. Without these, find-or-create races
-- and the graph ends up with two anchors for one goal — every traversal then
-- returns each edge twice and neither anchor is wrong.
create unique index if not exists forge_nodes_goal_anchor
    on forge_nodes (project_id, goal_id)     where kind = 'goal';
create unique index if not exists forge_nodes_decision_anchor
    on forge_nodes (project_id, decision_id) where kind = 'decision';
create unique index if not exists forge_nodes_owner_anchor
    on forge_nodes (project_id, owner_id)    where kind = 'owner';
create unique index if not exists forge_nodes_artifact_anchor
    on forge_nodes (project_id, artifact_id) where kind = 'artifact';

create index if not exists forge_nodes_project_kind_idx
    on forge_nodes (project_id, kind, created_at desc);

-- Dropped and re-created rather than guarded on pg_trigger.tgname.
--
-- tgname is not unique across schemas, so `if not exists (select from pg_trigger
-- where tgname = ...)` is true as soon as ANY schema in the database has a
-- trigger by that name — and every schema after the first silently gets none.
-- That is invisible in production, which has one schema, and wrong everywhere
-- the integration harness runs, which builds a schema per test.
-- See docs/bugfix/2026-09-02-trigger-guards-were-not-schema-scoped.md.
drop trigger if exists forge_nodes_updated_at on forge_nodes;
create trigger forge_nodes_updated_at before update on forge_nodes
    for each row execute function forge_set_updated_at();

-- ---------------------------------------------------------------------------
-- edges
-- ---------------------------------------------------------------------------
-- Typed relations. The type is not decoration: an untyped graph answers "is
-- there a line between these two" and nothing else, and the questions worth
-- asking are "what verifies this requirement" and "what does this rest on".
--
-- Which edge kinds may connect which node kinds is a TABLE in Go rather than a
-- check constraint, because the rule is two-dimensional and a constraint
-- spelling it out would be unreadable and unmaintainable. The database enforces
-- the vocabulary; the Go table enforces the pairings, with a fence over both.
create table if not exists forge_edges (
    id         text        primary key,
    project_id text        not null references forge_projects(id) on delete cascade,
    kind       text        not null,
    from_id    text        not null references forge_nodes(id) on delete cascade,
    to_id      text        not null references forge_nodes(id) on delete cascade,
    -- Why this relation was drawn. A graph of unexplained lines is a diagram.
    note       text        not null default '',
    created_by text        not null references forge_users(id),
    created_at timestamptz not null default now(),

    constraint forge_edges_kind_check check (kind in
        ('derives_from','satisfies','verifies','depends_on','constrains',
         'mitigates','owns','evidences')),
    -- Nothing relates to itself. Every one of the eight kinds is irreflexive:
    -- a requirement does not satisfy itself and a component does not depend on
    -- itself, and a self-edge is always a bug in the caller.
    constraint forge_edges_no_self check (from_id <> to_id)
);

-- One edge of a given kind between two nodes. A second is not more true, and
-- duplicates make every count in the graph wrong.
create unique index if not exists forge_edges_unique
    on forge_edges (kind, from_id, to_id);
create index if not exists forge_edges_from_idx on forge_edges (from_id, kind);
create index if not exists forge_edges_to_idx   on forge_edges (to_id, kind);
create index if not exists forge_edges_project_idx on forge_edges (project_id, kind);

-- ===========================================================================
-- 3. Artifacts and their lifecycle (PRD WRK-04)
-- ===========================================================================
-- "Every change identifies initiator, agent, tool, inputs, diff, verification
-- state, human disposition." Read literally, and it is: a version missing any
-- of the seven is refused rather than stored with a blank.
--
-- Versions are appended and never edited, and the CURRENT version is derived
-- from the highest version number rather than stored in a column. A stored
-- "is_current" flag is a second source of truth about the same fact, and the
-- day it disagrees with the version numbers is the day nobody can tell which
-- one is lying.
create table if not exists forge_artifacts (
    id         text        primary key,
    project_id text        not null references forge_projects(id) on delete cascade,
    -- Where it lives inside the authorised boundary (WRK-04's "authorized
    -- boundary" is the project's workspace; the path is relative to it).
    path       text        not null,
    -- file | document | model | drawing | dataset | report. Text plus a check
    -- rather than an enum, because domain packs will add to it.
    kind       text        not null default 'file',
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),

    constraint forge_artifacts_path_nonempty check (length(trim(path)) > 0),
    constraint forge_artifacts_kind_check
        check (kind in ('file','document','model','drawing','dataset','report')),
    -- One artifact per path per project. Two rows for one path would give the
    -- same file two independent histories.
    unique (project_id, path)
);

create index if not exists forge_artifacts_project_idx
    on forge_artifacts (project_id, path);

drop trigger if exists forge_artifacts_updated_at on forge_artifacts;
create trigger forge_artifacts_updated_at before update on forge_artifacts
    for each row execute function forge_set_updated_at();

create table if not exists forge_artifact_versions (
    id          text        primary key,
    artifact_id text        not null references forge_artifacts(id) on delete cascade,
    version     integer     not null,

    -- --- WRK-04's seven, in order ---

    -- 1. INITIATOR: the human whose intent this serves. Never nullable and never
    --    'system': every change traces to somebody who wanted it, even when a
    --    scheduler ran it, because the scheduler was configured by a person.
    initiator_id text       not null references forge_users(id),
    -- 2. AGENT: which part of FORGE produced it. Same vocabulary as
    --    forge_events.actor, so a version and its event agree about who acted.
    agent        text       not null,
    -- 3. TOOL: the call that made it. Null only when the agent is 'human' —
    --    a person editing by hand used no tool, and inventing one would be a
    --    fabricated tool call in the ledger.
    tool_call_id text       references forge_tool_calls(id) on delete set null,
    -- 4. INPUTS: what it was made from.
    inputs       jsonb      not null default '{}'::jsonb,
    -- 5. DIFF: what changed. Empty string is legal for version 1 of a new
    --    artifact and is stated as such; NULL is not, because "no diff recorded"
    --    and "nothing changed" must not look the same.
    diff         text       not null,

    -- 6. VERIFICATION STATE — what a machine determined.
    --    unverified | passed | failed
    verification_state text  not null default 'unverified',
    verification_note  text  not null default '',

    -- 7. HUMAN DISPOSITION — what a person decided. SEPARATE from verification
    --    on purpose (PRD SAF-05, AGT-08): "the tests passed" and "a human
    --    accepted it" are different facts, and a system that stores one column
    --    for both will eventually report a machine's opinion as a person's.
    --    pending | accepted | rejected | superseded
    human_disposition   text not null default 'pending',
    dispositioned_by    text references forge_users(id),
    dispositioned_at    timestamptz,
    disposition_reason  text not null default '',

    -- The audit chain's payoff (PRD SAF-06). The event exists already; this is
    -- the first thing that points at one. Nullable because an artifact can be
    -- created outside a goal — a human uploading a spec — and forcing an event
    -- would mean inventing one.
    event_id   text        references forge_events(id) on delete set null,

    created_at timestamptz not null default now(),

    constraint forge_artifact_versions_version_positive check (version > 0),
    constraint forge_artifact_versions_agent_check check (agent in
        ('planner','executor','verifier','human','scheduler','system')),
    constraint forge_artifact_versions_verification_check
        check (verification_state in ('unverified','passed','failed')),
    constraint forge_artifact_versions_disposition_check
        check (human_disposition in ('pending','accepted','rejected','superseded')),
    -- A human decision names the human. Same rule as forge_approvals: there is
    -- deliberately no way to record an acceptance without one.
    constraint forge_artifact_versions_disposition_attributed check (
        human_disposition not in ('accepted','rejected')
        or (dispositioned_by is not null and dispositioned_at is not null)),
    -- Only a human works without a tool. Anything else claiming no tool call is
    -- a change nobody can trace to an action.
    constraint forge_artifact_versions_tool_or_human check (
        tool_call_id is not null or agent = 'human'),
    unique (artifact_id, version)
);

create index if not exists forge_artifact_versions_artifact_idx
    on forge_artifact_versions (artifact_id, version desc);
-- Answering "what is waiting on a person?" without scanning every version.
create index if not exists forge_artifact_versions_pending_idx
    on forge_artifact_versions (artifact_id) where human_disposition = 'pending';

-- The forward reference from the node anchor, now that the table exists.
-- Dropped and re-added for the same reason as the triggers above: conname is not
-- unique across schemas either.
alter table forge_nodes drop constraint if exists forge_nodes_artifact_fk;
alter table forge_nodes add  constraint forge_nodes_artifact_fk
    foreign key (artifact_id) references forge_artifacts(id) on delete cascade;

comment on table forge_nodes is
    'PRD RSN-01 + WRK-03. One graph. Owned kinds hold content; anchor kinds hold identity for a row that lives elsewhere.';
comment on table forge_edges is
    'Typed relations. Which kinds may connect which is a table in internal/domain/workspace, fenced against this vocabulary.';
comment on table forge_artifact_versions is
    'PRD WRK-04. Append-only. Verification state is what a machine found; human disposition is what a person decided. They are never the same column.';
