-- 0009_containment: secret handles, and the incident record.
--
-- PRD SEC-03 and SAF-07.
--
-- ===========================================================================
-- 1. Secrets — a declaration, not a value
-- ===========================================================================
-- SEC-03: "model receives scoped handles, not raw secrets."
--
-- What this table holds is a DECLARATION: a name the model may reference, the
-- project it belongs to, the tools allowed to receive it, and the environment
-- variable the value is read from at the moment of use. The value itself never
-- lands in Postgres.
--
-- # Why FORGE brokers rather than stores
--
-- The alternative is to hold encrypted values here. That genuinely defends one
-- case — a stolen backup — and costs three things: a key, which would live in the
-- same process environment an attacker with the database usually also has; a
-- rotation and re-encryption path; and, most importantly, it makes FORGE a place
-- worth attacking for credentials rather than a thing that borrows them.
--
-- Brokering keeps custody where it already is. Private deployments run under
-- systemd, Kubernetes or a vault agent, all of which already put secrets in a
-- process environment; FORGE reads one variable at the moment a granted tool
-- needs it and forgets it again.
--
-- `source` is an enum with one value today so that a deployment which does want
-- FORGE to hold values has somewhere to add it — with SEC-02's key management in
-- wave 6, not before.
--
-- # What is NOT claimed
--
-- A tool that runs a command can still exfiltrate a value it was legitimately
-- given; scoping decides who gets it, not what they do with it afterwards. What
-- IS enforced is that the value never reaches the model: handles are substituted
-- at the tool boundary and every resolved value is redacted out of the tool's
-- output before it reaches either the model or the ledger.
create table if not exists forge_secrets (
    id         text        primary key,
    project_id text        not null references forge_projects(id) on delete cascade,
    -- The name the model sees, as `secret://<name>`. Lowercase, so a handle
    -- cannot be two different secrets depending on how somebody typed it.
    name       text        not null,
    -- env — read from the process environment at the moment of use.
    source     text        not null default 'env',
    -- The variable to read. Not the value.
    env_var    text        not null,
    -- What this is for, shown to the model beside the handle so it knows when to
    -- reach for it. Never contains the value.
    description text       not null default '',

    -- Revocation is a timestamp rather than a delete: "when did this stop being
    -- usable, and who stopped it" is the first question after an incident, and a
    -- deleted row answers neither (PRD SAF-07).
    revoked_at timestamptz,
    revoked_by text        references forge_users(id),
    revoked_reason text    not null default '',

    created_by text        not null references forge_users(id),
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),

    constraint forge_secrets_source_check check (source in ('env')),
    constraint forge_secrets_name_shape check (name ~ '^[a-z0-9][a-z0-9_]{0,62}$'),
    constraint forge_secrets_env_var_nonempty check (length(trim(env_var)) > 0),
    -- A revocation names who and when together; one without the other is a
    -- withdrawal nobody can account for.
    constraint forge_secrets_revocation_complete check ((revoked_at is null) = (revoked_by is null)),
    unique (project_id, name)
);

create index if not exists forge_secrets_project_idx
    on forge_secrets (project_id) where revoked_at is null;

drop trigger if exists forge_secrets_updated_at on forge_secrets;
create trigger forge_secrets_updated_at before update on forge_secrets
    for each row execute function forge_set_updated_at();

-- Which tools may receive which secret. Deny by default: a secret with no grants
-- is readable by nothing, which is the correct state for one somebody has just
-- declared and not yet thought about.
--
-- Per TOOL rather than per capability, because "may read" and "may receive the
-- production database password" are not the same question, and a capability
-- broad enough to cover a whole class is broad enough to cover the wrong member
-- of it.
create table if not exists forge_secret_grants (
    secret_id  text        not null references forge_secrets(id) on delete cascade,
    tool_name  text        not null,
    granted_by text        not null references forge_users(id),
    granted_at timestamptz not null default now(),
    primary key (secret_id, tool_name)
);

comment on table forge_secrets is
    'PRD SEC-03. A handle the model may reference and the environment variable it resolves to. Never the value.';
comment on table forge_secret_grants is
    'Which tools may receive which secret. Deny by default.';

-- ===========================================================================
-- 2. Incidents (PRD SAF-07)
-- ===========================================================================
-- "stop, revoke, quarantine, roll back, preserve evidence, notify, review."
--
-- Seven verbs, and the order between two of them is the whole point: evidence is
-- preserved BEFORE anything destructive. An incident response that stops and
-- rolls back first and gathers evidence afterwards has gathered the evidence of
-- its own response.
--
-- The record is append-only for the same reason the timeline is: an incident log
-- that can be edited is a log that will be edited, and the edit will happen
-- during the part of the incident nobody wants to explain later.
create table if not exists forge_incidents (
    id         text        primary key,
    project_id text        not null references forge_projects(id) on delete cascade,
    -- The goal that prompted it, when there is one. Incidents outlive goals and
    -- some have no goal at all — a leaked credential is an incident about a
    -- project, not about a run.
    goal_id    text        references forge_goals(id) on delete set null,

    title      text        not null,
    -- What happened, in the words of the person who opened it. Preserved
    -- verbatim: every summary afterwards is derived, and this is the thing they
    -- can be checked against.
    statement  text        not null,
    severity   text        not null default 'medium',

    -- open | contained | closed. Contained is a real state rather than a note:
    -- "the bleeding stopped" and "we understand what happened" are different
    -- days, and a two-state model forces one of them to be a lie.
    status     text        not null default 'open',

    opened_by  text        not null references forge_users(id),
    opened_at  timestamptz not null,

    -- Closing requires a review. There is deliberately no way to close an
    -- incident without writing what was learned (SAF-07's "review").
    review     text        not null default '',
    closed_by  text        references forge_users(id),
    closed_at  timestamptz,

    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),

    constraint forge_incidents_severity_check check (severity in ('low','medium','high','critical')),
    constraint forge_incidents_status_check check (status in ('open','contained','closed')),
    constraint forge_incidents_title_nonempty check (length(trim(title)) > 0),
    constraint forge_incidents_statement_nonempty check (length(trim(statement)) > 0),
    -- A closed incident has a review, a closer and a time. All three or none:
    -- a closure missing any of them is a closure nobody can audit.
    constraint forge_incidents_closure_complete check (
        status <> 'closed'
        or (length(trim(review)) > 0 and closed_by is not null and closed_at is not null))
);

create index if not exists forge_incidents_project_idx
    on forge_incidents (project_id, opened_at desc);
create index if not exists forge_incidents_open_idx
    on forge_incidents (project_id) where status <> 'closed';

drop trigger if exists forge_incidents_updated_at on forge_incidents;
create trigger forge_incidents_updated_at before update on forge_incidents
    for each row execute function forge_set_updated_at();

-- One action taken during an incident. Append-only.
--
-- Each names the human who took it — SAF-05 again: an incident response that
-- cannot say who stopped the goal is not a response, it is an outage with notes.
create table if not exists forge_incident_actions (
    id          text        primary key,
    incident_id text        not null references forge_incidents(id) on delete cascade,
    seq         integer     not null,

    -- The seven verbs of SAF-07, as a closed vocabulary.
    kind        text        not null,
    -- What it acted on: a goal id, a secret id, an artifact version, a session.
    -- Text rather than a foreign key because the targets live in six tables and
    -- the record must survive the target being deleted — which, for `quarantine`
    -- and `revoke`, is sometimes the point.
    target      text        not null default '',
    detail      text        not null default '',
    -- What actually happened, so a dry run and a real one are distinguishable
    -- and a partial failure is not recorded as a success.
    outcome     text        not null default 'done',

    taken_by    text        not null references forge_users(id),
    taken_at    timestamptz not null,

    constraint forge_incident_actions_kind_check check (kind in
        ('stop','revoke','quarantine','roll_back','preserve_evidence','notify','review')),
    constraint forge_incident_actions_outcome_check
        check (outcome in ('done','partial','failed','dry_run')),
    unique (incident_id, seq)
);

create index if not exists forge_incident_actions_incident_idx
    on forge_incident_actions (incident_id, seq);

comment on table forge_incidents is
    'PRD SAF-07. Open, contain, close with a review. Append-only; the actions are in forge_incident_actions.';
comment on table forge_incident_actions is
    'The seven verbs of SAF-07, each naming the human who took it. Evidence is preserved before anything destructive.';
