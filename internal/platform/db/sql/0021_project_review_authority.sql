-- 0021_project_review_authority: the named human a raised ceiling rests on.
--
-- PRD §"Domain packs", §8.1 risk tiers, AGT-07 (a consequential transition names
-- the human who authorised it).
--
-- ===========================================================================
-- 1. What this makes possible, and why that is a careful thing to add
-- ===========================================================================
-- Every engineering pack is ceilinged at r1 — reversible draft — because this
-- build implements none of the qualified review that r2 and above require in
-- those domains. That is correct and it is also a dead end: there is no way for
-- a deployment that DOES have a licensed engineer to say so.
--
-- These columns are that way. Recording a named authority raises the pack's
-- ceiling to its ReviewCeiling, and nothing else does.
--
-- ===========================================================================
-- 2. What this build can and cannot establish
-- ===========================================================================
-- It CANNOT verify a licence. There is no registry to check, no credential to
-- validate, and no plausible way to acquire either from inside this codebase.
--
-- So what is stored is a CLAIM, attributed: who holds the authority, who said
-- so, and when. Every surface that reads it must say "recorded, not verified" in
-- those words. Without that sentence the feature becomes a way to launder
-- authority the system never checked — the person doing r2 work would believe a
-- licence had been established, and nothing established one.
--
-- What it buys is real even so: the work becomes traceable to a named person who
-- accepted responsibility for it, in a record nobody can later claim was
-- automatic. That is the same thing AGT-07 asks of every consequential
-- transition, and it is the honest ceiling on what software can do here.
--
-- ===========================================================================
-- 3. Why columns and not a table
-- ===========================================================================
-- One authority per project at a time, replaced rather than accumulated — the
-- same shape as 0014's character columns. A table would add a join and a
-- history nobody has asked to query; the audit chain already records that the
-- value changed, which is where "who set this and when" is answered for real.
--
-- ===========================================================================
-- 4. Why nullable with no default
-- ===========================================================================
-- The absence is the safe state and has to be distinguishable from a value.
-- A default would mean every project in every existing deployment silently
-- acquired an authority nobody recorded, which is precisely the failure the
-- pack column itself was fixed for.

alter table forge_projects
    add column if not exists review_authority_holder text;
alter table forge_projects
    add column if not exists review_authority_note text;
alter table forge_projects
    add column if not exists review_authority_recorded_by text references forge_users(id);
alter table forge_projects
    add column if not exists review_authority_recorded_at timestamptz;

-- The four move together or not at all. A holder with nobody attesting to them
-- is an unattributed claim, which is the one shape this must never store: it
-- would raise a ceiling on the strength of a value with no author.
alter table forge_projects
    drop constraint if exists forge_projects_review_authority_complete;
alter table forge_projects
    add constraint forge_projects_review_authority_complete
    check (
        (review_authority_holder is null
         and review_authority_recorded_by is null
         and review_authority_recorded_at is null)
        or
        (review_authority_holder is not null
         and length(btrim(review_authority_holder)) > 0
         and review_authority_recorded_by is not null
         and review_authority_recorded_at is not null)
    );

comment on column forge_projects.review_authority_holder is
    'The named person holding qualified-review authority in this project''s domain. A CLAIM, recorded and never verified: this build cannot check a licence, and every surface that reads this must say so.';
comment on column forge_projects.review_authority_note is
    'What the holder was recorded as holding — a registration number, a role, a scope. Free text, unverified.';
comment on column forge_projects.review_authority_recorded_by is
    'Who recorded the claim (PRD AGT-07). A raised ceiling rests on an attributed statement, never an anonymous one.';
comment on column forge_projects.review_authority_recorded_at is
    'When the claim was recorded.';
