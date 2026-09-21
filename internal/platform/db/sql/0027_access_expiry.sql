-- 0027_access_expiry: access that runs out (PRD AGT-03, "time-bound").
--
-- Issue 20. AGT-03 asks for access that is "project-scoped, role-based,
-- revocable, time-bound". Four of those five were built. Time-bound was not:
-- 0010 gave forge_project_members `granted_by`, `granted_at` and `updated_at`
-- and no expiry, so a membership lasted until somebody remembered to revoke it.
--
-- ===========================================================================
-- 1. The sharp case is not a lingering membership
-- ===========================================================================
-- It is the qualified-review authority. 0021 added the four columns that name
-- the human a raised risk ceiling rests on, and said in its own words:
--
--   Recording a named authority raises the pack's ceiling to its ReviewCeiling,
--   and nothing else does.
--
-- Those four had no expiry either. So the named engineer who accepted
-- responsibility for r2 work could leave the company and the project's ceiling
-- stayed raised on the strength of a record nobody had revisited. 0021 is
-- careful that authority is ATTRIBUTABLE — the columns move together so a holder
-- always has an author — but attribution is not currency. This adds the fifth
-- column to that set, and it moves with the others for the same reason.
--
-- ===========================================================================
-- 2. Expiry, not a review interval
-- ===========================================================================
-- The alternative considered was access that goes STALE and is surfaced for
-- re-attestation without ever refusing. It fits this codebase's temperament —
-- loud and truthful rather than automatic — and it is not what AGT-03 says. A
-- requirement that says "time-bound" and is implemented as "time-noticed" is a
-- requirement nothing holds. So: an expired grant refuses, and the refusal says
-- when it lapsed and who to ask.
--
-- ===========================================================================
-- 3. Why nullable, and what null means
-- ===========================================================================
-- Null is "no expiry". It is the value every existing row keeps, because a
-- migration that expired live access on deploy would lock people out of work in
-- progress to satisfy a clause about the future. The DEFAULT is applied by the
-- service when a grant is made (internal/domain/access), not by the column: a
-- column default would silently date the rows written by every other path,
-- including 0010's backfill of every existing project's creator, and the one
-- thing this must not do is expire access nobody decided to time-bound.
--
-- Owner memberships are deliberately exempt from the default — see
-- access.Service.SetRole. An expiring owner is not a stricter control; it is a
-- project nobody can administer, not even to undo it, which is the state
-- wouldStrandProject already refuses to create by any other route.

alter table forge_project_members
    add column if not exists expires_at timestamptz;

-- Found by "whose access has lapsed", which is a scan over the few rows that
-- have an expiry at all, never over the memberships that have none.
create index if not exists forge_project_members_expires_idx
    on forge_project_members (expires_at)
    where expires_at is not null;

comment on column forge_project_members.expires_at is
    'When this grant stops being access (PRD AGT-03, time-bound). Null is no expiry; the default lifetime is applied by internal/domain/access when a grant is made, never by this column. An expired row is refused at permission-check time and is not deleted: who had access, and until when, is the first question after anything goes wrong.';

alter table forge_projects
    add column if not exists review_authority_expires_at timestamptz;

-- Backfill: an authority recorded before this migration gets one default
-- lifetime from the moment it was recorded.
--
-- ‼️ Not from now. Dating it from now would silently extend every raised
-- ceiling in every deployment by a further ninety days at the moment this runs,
-- which is the opposite of what the column is for. An authority recorded more
-- than ninety days ago therefore arrives already expired, and the project falls
-- back to its ordinary pack ceiling — which is the honest reading of a claim
-- nobody has revisited in three months.
--
-- 90 days is access.DefaultGrantLifetime. Written as a literal here because a
-- migration cannot read Go, and fenced against drift by
-- TestMigrationAndCodeAgreeOnTheDefaultGrantLifetime.
update forge_projects
   set review_authority_expires_at = review_authority_recorded_at + interval '90 days'
 where review_authority_holder is not null
   and review_authority_expires_at is null;

-- The five move together or not at all.
--
-- 0021 made four of them do so, to refuse a holder with nobody attesting to
-- them. The same reasoning covers the fifth: an authority with no end is a
-- claim that outlives the person who made it, and this build cannot ask anybody
-- whether it is still true. Replaces 0021's four-column constraint rather than
-- sitting beside it, because two overlapping checks on one invariant is two
-- answers to the same question.
alter table forge_projects
    drop constraint if exists forge_projects_review_authority_complete;
alter table forge_projects
    add constraint forge_projects_review_authority_complete
    check (
        (review_authority_holder is null
         and review_authority_recorded_by is null
         and review_authority_recorded_at is null
         and review_authority_expires_at is null)
        or
        (review_authority_holder is not null
         and length(btrim(review_authority_holder)) > 0
         and review_authority_recorded_by is not null
         and review_authority_recorded_at is not null
         and review_authority_expires_at is not null)
    );

comment on column forge_projects.review_authority_expires_at is
    'When the qualified-review claim stops raising this project''s ceiling (PRD AGT-03). Never null while a holder is recorded: an authority with no end outlives the person who made it, and this build cannot ask anybody whether it is still true. Past this instant the pack''s ordinary ceiling is in force again and the surfaces say the claim lapsed.';
