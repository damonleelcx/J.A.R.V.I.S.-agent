-- 0003_workspace: projects, goals, milestones, and versioned plans.
--
-- This is the "what are we trying to do" half of the engine. 0004 adds the
-- "how is it being done" half. Every statement is re-runnable, because
-- db.Migrate replays the whole chain on every boot.

-- ---------------------------------------------------------------------------
-- projects
-- ---------------------------------------------------------------------------
-- The authorisation and retrieval boundary. PRD AGT-03 requires permissions to
-- be project-scoped, and PRD MEM-01 separates project knowledge from
-- organisation knowledge — both need a project to point at. Goals never float
-- free; a goal with no boundary has no answer to "which artifacts may this
-- touch?".
create table if not exists forge_projects (
    id          text        primary key,
    owner_id    text        not null references forge_users(id) on delete cascade,
    name        text        not null,
    description text        not null default '',
    -- The domain pack in force. Packs carry schemas, validators, safety policy
    -- and approval rules (PRD §7), so this is not a label: it selects which
    -- rules apply to everything inside the project.
    pack        text        not null default 'general',
    archived_at timestamptz,
    created_at  timestamptz not null default now(),
    updated_at  timestamptz not null default now(),
    constraint forge_projects_name_nonempty check (length(trim(name)) > 0)
);

create index if not exists forge_projects_owner_idx
    on forge_projects (owner_id, created_at desc);
create index if not exists forge_projects_active_idx
    on forge_projects (owner_id) where archived_at is null;

do $$ begin
    if not exists (select 1 from pg_trigger where tgname = 'forge_projects_updated_at') then
        create trigger forge_projects_updated_at before update on forge_projects
            for each row execute function forge_set_updated_at();
    end if;
end $$;

-- ---------------------------------------------------------------------------
-- goals
-- ---------------------------------------------------------------------------
-- A long-running objective. The row carries everything needed to answer, from
-- storage alone and with no model involved: what are we doing, how would we know
-- it was done, how much may it spend, how far may it act without asking.
create table if not exists forge_goals (
    id           text        primary key,
    project_id   text        not null references forge_projects(id) on delete cascade,
    created_by   text        not null references forge_users(id),
    title        text        not null,
    -- The objective in the user's own words, preserved verbatim. Every summary
    -- downstream is derived; this is the thing they can be checked against.
    statement    text        not null,

    -- draft | active | paused | succeeded | failed | cancelled
    status       text        not null default 'draft',

    -- PRD AGT-04. Text + check rather than an enum because the ladder will grow,
    -- and because "never silently raises its autonomy level" is easier to audit
    -- when the value is legible in a plain SELECT.
    autonomy     text        not null default 'draft',
    -- PRD §8.1 risk tier, r0..r5.
    risk_tier    text        not null default 'r1',

    -- Machine-checkable completion criteria (bullet B1: "measurable completion
    -- criteria"). Stored as JSON rather than prose so the verifier has something
    -- to evaluate rather than interpret.
    completion_criteria jsonb not null default '[]'::jsonb,

    -- Expected duration, for the operator's sake and for the wall-clock budget.
    target_completion_at timestamptz,

    -- Per-goal budget ceilings. NULL means "inherit the process default", so a
    -- deployment-wide change reaches goals that never overrode it.
    max_tokens        bigint,
    max_cost_cents    bigint,
    max_wallclock_ms  bigint,
    max_tasks         integer,

    -- Running totals. Denormalised deliberately: the budget check runs before
    -- every model call, and summing a tool-call table on each one would put an
    -- aggregate on the hottest path in the engine.
    tokens_spent     bigint  not null default 0,
    cost_cents_spent bigint  not null default 0,
    tasks_created    integer not null default 0,

    -- Why the goal ended. Recorded rather than inferred, because "why did this
    -- stop?" is the first question anyone asks about a long-running agent.
    outcome_summary text,
    failure_code    text,

    started_at   timestamptz,
    ended_at     timestamptz,
    created_at   timestamptz not null default now(),
    updated_at   timestamptz not null default now(),

    constraint forge_goals_status_check
        check (status in ('draft','active','paused','succeeded','failed','cancelled')),
    constraint forge_goals_autonomy_check
        check (autonomy in ('discuss','draft','sandbox_execute','approval_gated','prohibited')),
    constraint forge_goals_risk_check
        check (risk_tier in ('r0','r1','r2','r3','r4','r5')),
    constraint forge_goals_title_nonempty check (length(trim(title)) > 0),
    constraint forge_goals_statement_nonempty check (length(trim(statement)) > 0),
    -- A terminal goal must record when it ended. Without this a "succeeded" row
    -- with a null ended_at looks identical to one still running.
    constraint forge_goals_terminal_has_end
        check (status not in ('succeeded','failed','cancelled') or ended_at is not null),
    constraint forge_goals_spend_nonnegative
        check (tokens_spent >= 0 and cost_cents_spent >= 0 and tasks_created >= 0)
);

create index if not exists forge_goals_project_idx
    on forge_goals (project_id, created_at desc);
-- The scheduler's hot query: which goals still need attention?
create index if not exists forge_goals_runnable_idx
    on forge_goals (status) where status in ('active','paused');

do $$ begin
    if not exists (select 1 from pg_trigger where tgname = 'forge_goals_updated_at') then
        create trigger forge_goals_updated_at before update on forge_goals
            for each row execute function forge_set_updated_at();
    end if;
end $$;

-- ---------------------------------------------------------------------------
-- milestones
-- ---------------------------------------------------------------------------
-- Bullet B1 asks for milestones alongside the goal. They are a separate table
-- rather than a JSON array on the goal because each one is achieved at a
-- distinct moment by a distinct piece of work, and "when did we pass milestone
-- 3?" is a question the timeline has to answer precisely.
create table if not exists forge_milestones (
    id          text        primary key,
    goal_id     text        not null references forge_goals(id) on delete cascade,
    ordinal     integer     not null,
    title       text        not null,
    -- How a human or a verifier would know this milestone was reached.
    criterion   text        not null default '',
    status      text        not null default 'pending',
    achieved_at timestamptz,
    created_at  timestamptz not null default now(),
    constraint forge_milestones_status_check
        check (status in ('pending','achieved','abandoned')),
    constraint forge_milestones_achieved_has_time
        check (status <> 'achieved' or achieved_at is not null),
    unique (goal_id, ordinal)
);

create index if not exists forge_milestones_goal_idx
    on forge_milestones (goal_id, ordinal);

-- ---------------------------------------------------------------------------
-- plans
-- ---------------------------------------------------------------------------
-- A versioned decomposition of a goal into work.
--
-- Replanning (bullet B18) supersedes rather than mutates: the old plan stays
-- readable so an auditor can see what was believed at the time and why it
-- changed. A plan that is edited in place erases exactly the information that
-- makes a long run explicable after the fact.
create table if not exists forge_plans (
    id         text        primary key,
    goal_id    text        not null references forge_goals(id) on delete cascade,
    version    integer     not null,
    -- Why this plan exists. For version 1 it is the initial decomposition; for
    -- later versions it is what changed and what triggered the change.
    rationale  text        not null default '',
    -- planner | human. Who authored this version. PRD SAF-05: "the AI approved
    -- it" is never acceptable authority, so authorship is never inferred.
    author     text        not null default 'planner',
    superseded_at timestamptz,
    created_at timestamptz not null default now(),
    constraint forge_plans_author_check check (author in ('planner','human')),
    constraint forge_plans_version_positive check (version > 0),
    unique (goal_id, version)
);

create index if not exists forge_plans_goal_idx
    on forge_plans (goal_id, version desc);
-- At most one live plan per goal. A second one would mean two sources of truth
-- about what work exists, and the engine would follow whichever it read first.
create unique index if not exists forge_plans_one_live_per_goal
    on forge_plans (goal_id) where superseded_at is null;

comment on table forge_projects is 'Authorisation and retrieval boundary. Selects the domain pack whose rules apply.';
comment on table forge_goals is 'A long-running objective, with its completion criteria, budgets, and autonomy ceiling.';
comment on table forge_plans is 'Versioned decompositions. Replanning supersedes; it never edits in place.';
