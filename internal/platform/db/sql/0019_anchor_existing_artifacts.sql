-- 0019_anchor_existing_artifacts: put the artifacts that predate anchoring into
-- their projects' graphs.
--
-- PRD WRK-03 (the project graph spans files) and WRK-01 (the canvas shows them).
--
-- ===========================================================================
-- 1. What this repairs
-- ===========================================================================
-- `KindArtifact` has existed to hold files since the workspace model was
-- written, and nothing ever produced one: workspace.Service.Anchor was called
-- from tests only. So every artifact this system has ever recorded belongs to
-- no graph, and the only way to reach one is an id somebody already has —
-- which is not a starting point any surface has.
--
-- RecordChange now anchors what it records, inside the same transaction as the
-- version, so the invariant holds from here on. This migration is the other
-- half: without it the graph is SILENTLY PARTIAL — new files appear, older
-- ones do not, and nothing distinguishes "this project has no files" from
-- "these files predate the fix". A listing that is usually complete is worse
-- than one that is either complete or absent, because nobody checks the former.
--
-- ===========================================================================
-- 2. Why the node id is derived from the artifact id
-- ===========================================================================
-- Ids here are a prefix plus a 26-character sortable component whose first
-- bits are a millisecond timestamp (internal/platform/id). SQL cannot mint one
-- of those, and a random uuid would not be one — anything reading an id would
-- get something the wrong shape.
--
-- So the anchor takes the artifact's own sortable component: art_01M1H2W95D…
-- becomes nod_01M1H2W95D…. That is:
--
--   * correctly shaped, and in this system's alphabet;
--   * unique, because artifact ids are;
--   * deterministic, which is what makes this migration idempotent — a second
--     run computes the same id and the ON CONFLICT below does nothing;
--   * and sorted by the moment the ARTIFACT was created, which is exactly when
--     its anchor should have been made.
--
-- ===========================================================================
-- 3. What the row says
-- ===========================================================================
-- title      the artifact's path, which cannot go stale: an artifact is
--            identified BY its path, so a rename is a different artifact.
-- how        'observed' — the file demonstrably exists; this is not a guess.
-- status     'accepted' — it is a record of something that happened.
-- created_by the artifact's earliest version's initiator: whoever's intent the
--            file served. Never a service account, and never invented — an
--            artifact with no versions is skipped rather than attributed to
--            somebody who did not make it.

insert into forge_nodes
    (id, project_id, kind, title, body, how, source, status,
     goal_id, decision_id, owner_id, artifact_id, created_by, created_at, updated_at)
select
    'nod_' || substring(a.id from 5),
    a.project_id,
    'artifact',
    a.path,
    '',
    'observed',
    '',
    'accepted',
    null, null, null,
    a.id,
    v.initiator_id,
    a.created_at,
    a.created_at
from forge_artifacts a
join lateral (
    select initiator_id
      from forge_artifact_versions
     where artifact_id = a.id
     order by version
     limit 1
) v on true
where not exists (
    select 1 from forge_nodes n
     where n.kind = 'artifact'
       and n.project_id = a.project_id
       and n.artifact_id = a.id
)
on conflict do nothing;
