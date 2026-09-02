-- 0004_engine: the durable task DAG, job queue, checkpoints, timeline,
-- tool-call ledger, approvals, and layered memory.
--
-- This is where "a long-running agent must not be a long-running LLM call"
-- becomes a schema. Everything the agent knows between one wake-up and the next
-- lives in these tables; nothing survives in a process.

-- ---------------------------------------------------------------------------
-- tasks
-- ---------------------------------------------------------------------------
create table if not exists forge_tasks (
    id          text        primary key,
    goal_id     text        not null references forge_goals(id) on delete cascade,
    plan_id     text        not null references forge_plans(id) on delete cascade,
    -- Set when a task was created by decomposing another. Depth is carried
    -- explicitly rather than computed by walking parents, because the limit on
    -- recursive decomposition has to be enforced at INSERT time — a recursive
    -- CTE per task creation would be a self-inflicted denial of service.
    parent_task_id text     references forge_tasks(id) on delete cascade,
    depth       integer     not null default 0,

    title       text        not null,
    -- What this task must accomplish, in prose the executor is given.
    instruction text        not null,
    -- Typed inputs and the shape of the expected result. Bullet B2 asks for
    -- "clear inputs and outputs"; without a declared expectation the verifier
    -- has nothing to check the result against and degenerates into asking the
    -- model whether it is happy with itself.
    inputs          jsonb   not null default '{}'::jsonb,
    expected_output jsonb   not null default '{}'::jsonb,

    -- pending  : created, dependencies not yet satisfied
    -- ready    : dependencies satisfied, waiting for a worker
    -- claimed  : leased by a worker, not yet started
    -- running  : actively executing
    -- awaiting_approval : blocked on a human gate (PRD AGT-07)
    -- verifying: work done, evidence being checked (PRD AGT-06)
    -- succeeded / failed / cancelled / skipped
    --
    -- PRD AGT-08 requires these to stay distinct and never be implied falsely.
    -- In particular 'succeeded' means the executor finished; it does NOT mean
    -- verified. That is what verified_at is for.
    status      text        not null default 'pending',

    -- Idempotency (bullet B8). Unique per goal: a retry that finds a completed
    -- record for its key returns the recorded result instead of acting again.
    idempotency_key text    not null,

    attempt_count integer   not null default 0,
    max_attempts  integer   not null default 5,

    -- Lease (bullet B22). Two workers must never run one task. The lease is a
    -- timestamp rather than a boolean so a crashed worker's task becomes
    -- available on its own, with no janitor process required to notice.
    lease_owner      text,
    lease_expires_at timestamptz,

    -- Scheduling and wake-up (bullet B11). A task is invisible to the queue
    -- until this instant: retry backoff, a timer, or an external wait all
    -- reduce to setting it.
    not_before  timestamptz not null default now(),
    -- Lower runs first. Ties break on created_at, so a plan's order is honoured.
    priority    integer     not null default 100,

    risk_tier   text        not null default 'r1',
    -- Whether a human gate is required before this task may act. Derived from
    -- risk tier and the pack's policy at planning time, then frozen: a task must
    -- not lose its gate because policy was edited mid-run.
    requires_approval boolean not null default false,

    result      jsonb,
    -- Verification is a separate fact from completion. A task can succeed and
    -- fail verification; PRD AGT-06 forbids treating one as the other.
    verified_at timestamptz,
    verdict     jsonb,

    error_code   text,
    error_detail text,

    started_at timestamptz,
    ended_at   timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),

    constraint forge_tasks_status_check check (status in
        ('pending','ready','claimed','running','awaiting_approval','verifying',
         'succeeded','failed','cancelled','skipped')),
    constraint forge_tasks_risk_check
        check (risk_tier in ('r0','r1','r2','r3','r4','r5')),
    constraint forge_tasks_depth_nonneg check (depth >= 0),
    constraint forge_tasks_attempts_nonneg
        check (attempt_count >= 0 and max_attempts > 0),
    -- A leased task must say when the lease dies. A lease with no expiry is a
    -- task that is stuck forever if its worker disappears.
    constraint forge_tasks_lease_pairs
        check ((lease_owner is null) = (lease_expires_at is null)),
    -- Only these states may hold a lease. Catching it here means a bug that
    -- forgets to clear a lease fails loudly instead of silently hiding the task
    -- from the queue.
    constraint forge_tasks_lease_only_when_active
        check (lease_owner is null or status in ('claimed','running','verifying')),
    constraint forge_tasks_terminal_has_end
        check (status not in ('succeeded','failed','cancelled','skipped')
               or ended_at is not null),
    constraint forge_tasks_failed_has_code
        check (status <> 'failed' or error_code is not null),
    unique (goal_id, idempotency_key)
);

-- The queue's claim query. Partial index because the queue only ever looks at
-- runnable rows, and by the end of a long goal the vast majority are terminal.
create index if not exists forge_tasks_claimable_idx
    on forge_tasks (not_before, priority, created_at)
    where status = 'ready' and lease_owner is null;

-- Lease reaping: find tasks whose worker died.
create index if not exists forge_tasks_expired_lease_idx
    on forge_tasks (lease_expires_at)
    where lease_owner is not null;

-- Dependency resolution walks from a task to its dependents.
create index if not exists forge_tasks_goal_status_idx
    on forge_tasks (goal_id, status);
create index if not exists forge_tasks_plan_idx
    on forge_tasks (plan_id, created_at);
create index if not exists forge_tasks_parent_idx
    on forge_tasks (parent_task_id) where parent_task_id is not null;

do $$ begin
    if not exists (select 1 from pg_trigger where tgname = 'forge_tasks_updated_at') then
        create trigger forge_tasks_updated_at before update on forge_tasks
            for each row execute function forge_set_updated_at();
    end if;
end $$;

-- ---------------------------------------------------------------------------
-- task dependencies (the DAG edges)
-- ---------------------------------------------------------------------------
-- Bullet B12. Edges are their own table rather than an array column on the task
-- because the readiness query reads them from the *dependency* side ("which
-- tasks were waiting on the one that just finished?"), and an array column
-- cannot be indexed usefully in that direction.
create table if not exists forge_task_deps (
    task_id       text not null references forge_tasks(id) on delete cascade,
    depends_on_id text not null references forge_tasks(id) on delete cascade,
    created_at    timestamptz not null default now(),
    primary key (task_id, depends_on_id),
    -- A task depending on itself is a deadlock the engine cannot detect at run
    -- time without a cycle walk. Refusing the trivial case here is free; the
    -- general cycle check lives in the plan validator.
    constraint forge_task_deps_no_self check (task_id <> depends_on_id)
);

create index if not exists forge_task_deps_reverse_idx
    on forge_task_deps (depends_on_id);

-- ---------------------------------------------------------------------------
-- checkpoints
-- ---------------------------------------------------------------------------
-- Bullet B7: save progress after every meaningful step so the agent resumes
-- after a crash, restart, or model failure.
--
-- Append-only and sequenced rather than a single mutable "current state" column.
-- A crash halfway through overwriting one state row leaves nothing to resume
-- from; an append leaves the previous checkpoint intact, which is the whole
-- point of having one.
create table if not exists forge_checkpoints (
    id         text        primary key,
    task_id    text        not null references forge_tasks(id) on delete cascade,
    seq        integer     not null,
    -- What kind of moment this captures: iteration_end, tool_result,
    -- approval_granted, verification_done, ...
    kind       text        not null,
    -- The resumable state. Everything the next cycle needs that is not already
    -- derivable from the task row and the timeline.
    state      jsonb       not null,
    created_at timestamptz not null default now(),
    unique (task_id, seq)
);

create index if not exists forge_checkpoints_task_idx
    on forge_checkpoints (task_id, seq desc);

-- ---------------------------------------------------------------------------
-- events (the execution timeline)
-- ---------------------------------------------------------------------------
-- Bullet B26: make it possible to answer what happened, why, when, and what
-- happens next. Append-only; nothing here is ever updated or deleted, because a
-- timeline that can be edited answers none of those questions credibly.
create table if not exists forge_events (
    id         text        primary key,
    goal_id    text        not null references forge_goals(id) on delete cascade,
    task_id    text        references forge_tasks(id) on delete cascade,
    seq        bigint      not null,
    kind       text        not null,
    -- Who caused this: planner | executor | verifier | human | scheduler | system.
    -- PRD requires "Forge proposed", "human approved" and "Forge executed" to be
    -- distinguishable, which is impossible if authorship is not recorded.
    actor      text        not null,
    actor_id   text,
    summary    text        not null default '',
    payload    jsonb       not null default '{}'::jsonb,
    created_at timestamptz not null default now(),
    constraint forge_events_actor_check check (actor in
        ('planner','executor','verifier','human','scheduler','system')),
    unique (goal_id, seq)
);

create index if not exists forge_events_goal_idx on forge_events (goal_id, seq desc);
create index if not exists forge_events_task_idx on forge_events (task_id, created_at)
    where task_id is not null;
create index if not exists forge_events_kind_idx on forge_events (goal_id, kind, created_at desc);

-- ---------------------------------------------------------------------------
-- tool calls
-- ---------------------------------------------------------------------------
-- Bullets B8 and B14: every side-effecting call records its idempotency key
-- BEFORE execution and checks it after, so a retry that finds a completed record
-- returns the recorded result instead of acting again. This is what makes
-- "retry on failure" safe in a system that can send email, write files, open
-- pull requests, and spend money.
--
-- Separate from forge_events because an event is an immutable fact while a tool
-- call is a state machine: it is claimed, then runs, then resolves. Storing a
-- mutable thing in an append-only log means either lying about immutability or
-- reconstructing state by replay on every check.
create table if not exists forge_tool_calls (
    id              text        primary key,
    task_id         text        not null references forge_tasks(id) on delete cascade,
    -- Globally unique, not per task: the same logical action must deduplicate
    -- even if a replan moved it to a different task. Uniqueness across the whole
    -- table is what makes that true.
    idempotency_key text        not null unique,
    tool_name       text        not null,
    input           jsonb       not null default '{}'::jsonb,

    -- pending | running | succeeded | failed | refused
    -- 'refused' is distinct from 'failed': the policy plane declined to run it
    -- (PRD SAF-04), which is a correct outcome, not an error to retry.
    status          text        not null default 'pending',

    output          jsonb,
    error_code      text,
    error_detail    text,

    -- PRD AGT-06: preserve raw outputs and distinguish tool evidence from model
    -- inference. This holds the unedited result, whatever the executor made of it.
    raw_output      text,

    attempt         integer     not null default 1,
    started_at      timestamptz,
    ended_at        timestamptz,
    duration_ms     bigint,
    created_at      timestamptz not null default now(),

    constraint forge_tool_calls_status_check
        check (status in ('pending','running','succeeded','failed','refused')),
    constraint forge_tool_calls_terminal_has_end
        check (status not in ('succeeded','failed','refused') or ended_at is not null),
    constraint forge_tool_calls_failed_has_code
        check (status not in ('failed','refused') or error_code is not null)
);

create index if not exists forge_tool_calls_task_idx
    on forge_tool_calls (task_id, created_at);
create index if not exists forge_tool_calls_tool_idx
    on forge_tool_calls (tool_name, created_at desc);

-- ---------------------------------------------------------------------------
-- approvals
-- ---------------------------------------------------------------------------
-- Bullet B23 and PRD AGT-07. A human gate is a durable row, not a prompt: the
-- agent may be asleep for days between asking and being answered, and the answer
-- must survive every restart in between.
create table if not exists forge_approvals (
    id          text        primary key,
    goal_id     text        not null references forge_goals(id) on delete cascade,
    task_id     text        not null references forge_tasks(id) on delete cascade,
    risk_tier   text        not null,
    -- What is being asked for, in terms a reviewer can decide on without
    -- reading the code: the intent, the affected artifacts, the expected
    -- outputs, the risks, and what cannot be undone (PRD AGT-02).
    summary     text        not null,
    preview     jsonb       not null default '{}'::jsonb,

    requested_at timestamptz not null default now(),
    -- A gate that can never time out will hold a goal forever when the reviewer
    -- goes on leave. Expiry converts that into an explicit, visible outcome.
    expires_at   timestamptz,

    -- pending | approved | rejected | expired | withdrawn
    decision     text        not null default 'pending',
    -- PRD SAF-05: the accountable human is named. There is deliberately no way
    -- to record an approval without one.
    decided_by   text        references forge_users(id),
    decided_at   timestamptz,
    decision_reason text     not null default '',

    constraint forge_approvals_decision_check
        check (decision in ('pending','approved','rejected','expired','withdrawn')),
    constraint forge_approvals_risk_check
        check (risk_tier in ('r0','r1','r2','r3','r4','r5')),
    -- A human decision must name the human. 'expired' and 'withdrawn' are
    -- system outcomes and carry no decider, which is why they are excluded.
    constraint forge_approvals_human_decisions_are_attributed
        check (decision not in ('approved','rejected')
               or (decided_by is not null and decided_at is not null))
);

create index if not exists forge_approvals_goal_idx
    on forge_approvals (goal_id, requested_at desc);
create index if not exists forge_approvals_pending_idx
    on forge_approvals (expires_at) where decision = 'pending';
-- One live gate per task. Two pending approvals for one action would let a
-- rejection and an approval both be true.
create unique index if not exists forge_approvals_one_pending_per_task
    on forge_approvals (task_id) where decision = 'pending';

-- ---------------------------------------------------------------------------
-- memory
-- ---------------------------------------------------------------------------
-- Bullet B20 and PRD MEM-01/MEM-02: distinguish task state, episodic history,
-- reusable knowledge, and user preferences — each with its own retention, and
-- all of it inspectable and deletable by the person it belongs to.
--
-- Task state lives in forge_tasks and forge_checkpoints; episodic history is
-- forge_events. This table is the other two: durable knowledge and preferences.
create table if not exists forge_memory (
    id         text        primary key,
    -- project | user | organisation. The scope decides who sees it and how long
    -- it lives, so it is a column rather than a naming convention.
    scope      text        not null,
    project_id text        references forge_projects(id) on delete cascade,
    user_id    text        references forge_users(id) on delete cascade,

    key        text        not null,
    value      jsonb       not null,
    -- Where this came from, so PRD MEM-02's "see why an item was retrieved" has
    -- something to show. An unattributed memory cannot be judged, only trusted.
    source     text        not null default '',
    -- Pinned items survive compaction and expiry sweeps.
    pinned     boolean     not null default false,
    expires_at timestamptz,

    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),

    constraint forge_memory_scope_check check (scope in ('project','user','organisation')),
    -- A scope must have the thing it is scoped to, or the item is unreachable
    -- and undeletable by the person it belongs to.
    constraint forge_memory_scope_has_owner check (
        (scope = 'project' and project_id is not null) or
        (scope = 'user' and user_id is not null) or
        (scope = 'organisation')
    ),
    constraint forge_memory_key_nonempty check (length(trim(key)) > 0)
);

create unique index if not exists forge_memory_project_key
    on forge_memory (project_id, key) where scope = 'project';
create unique index if not exists forge_memory_user_key
    on forge_memory (user_id, key) where scope = 'user';
create index if not exists forge_memory_expiry_idx
    on forge_memory (expires_at) where expires_at is not null and pinned = false;

do $$ begin
    if not exists (select 1 from pg_trigger where tgname = 'forge_memory_updated_at') then
        create trigger forge_memory_updated_at before update on forge_memory
            for each row execute function forge_set_updated_at();
    end if;
end $$;

comment on table forge_tasks is 'DAG nodes. Leases prevent double execution; not_before drives scheduling and backoff.';
comment on table forge_checkpoints is 'Append-only resumable state. Never overwritten: a crash mid-overwrite would leave nothing to resume from.';
comment on table forge_events is 'Append-only execution timeline. Answers what happened, why, when, and by whom.';
comment on table forge_tool_calls is 'Idempotency ledger. A key is recorded BEFORE execution so a retry cannot repeat a side effect.';
comment on table forge_approvals is 'Durable human gates. A decision must name the human who made it.';
