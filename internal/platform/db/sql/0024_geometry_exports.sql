-- 0024_geometry_exports: a STEP file written off-node, by forge-worker, and kept
-- in blob storage.
--
-- docs/plan-2026-09-13-millions-of-parts.md; the owner decision "STEP export at
-- 1M stays refused until an off-node export job exists". Code:
-- internal/agent/stepexport.go.
--
-- ===========================================================================
-- 1. Why the export has no status column
-- ===========================================================================
-- The work is an engine task (forge_tasks), with the lease, the reaper and the
-- attempts every other task has. Its status IS the task's status. A second
-- status column here would be a second authority for one fact, and the first
-- worker to die between writing one and the other would leave an export that is
-- "succeeded" on one row and "running" on the next. So a reader joins the task
-- and derives queued / running / succeeded / failed on every read.
--
-- What this row adds is the FILE: its blob key, size and what the kernel said
-- about it. Those are written in the same transaction that marks the task
-- succeeded, so a stored file and a succeeded task never exist without each
-- other. The check constraint below holds the half of that this table can see.
--
-- ===========================================================================
-- 2. Why "live" is a partial unique index
-- ===========================================================================
-- Asking for the same version as STEP twice returns the export already queued,
-- running or done: a double click must not build a 90,000-part file twice. A
-- FAILED export is superseded instead, so asking again is how a person retries.
-- The index is what makes two requests racing past the lookup find each other
-- rather than both queueing a job: the loser's insert is refused and it re-reads.

create table if not exists forge_geometry_exports (
    id            text        primary key,
    -- The version the file is written from. A version is immutable, so a stored
    -- export of it is never stale; it goes when the version goes.
    version_id    text        not null references forge_geometry(version_id) on delete cascade,
    -- Copied from the version when asked for, because authorisation reads it on
    -- every status and download, and a version never moves between projects.
    project_id    text        not null references forge_projects(id) on delete cascade,
    format        text        not null check (format in ('step')),
    requested_by  text        not null references forge_users(id),
    goal_id       text        not null references forge_goals(id) on delete cascade,
    task_id       text        not null unique references forge_tasks(id) on delete cascade,
    -- What the download is called, fixed when it was asked for.
    filename      text        not null,

    -- --- the file, once stored ---
    blob_key      text        check (blob_key is null or blob_key ~ '^sha256:[0-9a-f]{64}$'),
    size_bytes    bigint      check (size_bytes is null or size_bytes >= 0),
    parts         integer     check (parts is null or parts >= 0),
    -- What the kernel left out (skipped parts, features it could not apply) and
    -- its time per phase. The download's label is read from it.
    report        jsonb       not null default '{}'::jsonb,
    stored_at     timestamptz,

    superseded_at timestamptz,
    created_at    timestamptz not null,

    -- A file is its key, its size and when it was stored, all three or none.
    constraint forge_geometry_exports_file_whole check (
        (blob_key is null) = (size_bytes is null) and (blob_key is null) = (stored_at is null)
    )
);

create unique index if not exists forge_geometry_exports_live
    on forge_geometry_exports (version_id, format) where superseded_at is null;

create index if not exists forge_geometry_exports_project
    on forge_geometry_exports (project_id, created_at desc);
