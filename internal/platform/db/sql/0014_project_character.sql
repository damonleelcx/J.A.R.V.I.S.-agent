-- 0014_project_character: how hard FORGE argues, and how much it explains.
--
-- PRD RSN-04 (constructive dissent; safety-critical dissent cannot be disabled).
--
-- ===========================================================================
-- 1. What was actually missing
-- ===========================================================================
-- Not the mechanism. persona.Character has carried CritiqueIntensity and
-- Verbosity since the soul was written, persona.SystemPrompt turns them into
-- real instructions ("Challenge assumptions actively. Argue against the plan
-- where you see a weakness, including the user's own"), and that prompt reaches
-- every model call the planner, executor, verifier and conversation make.
--
-- What was missing is anybody able to set them. Character was constructed in
-- exactly one place — DefaultCharacter(), hardcoded to {normal, normal} — so the
-- "low" and "high" branches were unreachable code and the requirement's
-- configurable half had a consumer and no producer.
--
-- ===========================================================================
-- 2. Why the project, and not the deployment or the person
-- ===========================================================================
-- "How hard should FORGE push back on this work" is a judgement about the work,
-- not about the installation and not about who is logged in. A structural review
-- and an exploratory sketch want different answers on the same day from the same
-- person, and the thing that distinguishes them is which project they are in.
--
-- Deployment-wide configuration would force one answer on every team sharing an
-- installation. Per-user would make FORGE argue differently with two people
-- looking at the same design, which is worse than either setting: the record
-- would show dissent that depended on who was in the room.
--
-- Character.Address is deliberately NOT here. "Call me Priya" is a fact about a
-- person and would be wrong on a project; it stays unset until somebody wants it
-- enough to add a per-user home for it.
--
-- ===========================================================================
-- 3. Why columns and not a settings table
-- ===========================================================================
-- Two enumerated values with defaults are a property of a project, the same
-- shape as forge_rooms.transcribing. A settings table would add a join, a
-- key-value schema nobody can constrain, and a second place a project's state
-- lives. Adding a third setting later is another column.
--
-- ===========================================================================
-- 4. Why CHECK constraints rather than trusting the writer
-- ===========================================================================
-- These values are rendered into a system prompt. An unrecognised value does not
-- fail — persona.SystemPrompt switches on them and an unknown string simply
-- matches no branch — so a typo would silently produce the default behaviour
-- while the row claimed otherwise. That is the failure mode this project refuses
-- everywhere else: the setting would read as applied and would not be.
--
-- Enumerated in the database so the answer is the same whichever path writes it.

alter table forge_projects
    add column if not exists critique_intensity text not null default 'normal';
alter table forge_projects
    add column if not exists verbosity text not null default 'normal';

-- Dropped and recreated rather than added alongside: two constraints over one
-- column eventually disagree about which is authoritative. Both statements are
-- idempotent, so re-running the chain is harmless.
alter table forge_projects
    drop constraint if exists forge_projects_critique_intensity_check;
alter table forge_projects
    add constraint forge_projects_critique_intensity_check
    check (critique_intensity in ('low', 'normal', 'high'));

alter table forge_projects
    drop constraint if exists forge_projects_verbosity_check;
alter table forge_projects
    add constraint forge_projects_verbosity_check
    check (verbosity in ('terse', 'normal', 'explanatory'));

comment on column forge_projects.critique_intensity is
    'How hard FORGE argues on this project: low | normal | high (PRD RSN-04). Bounded, never disabled — safety-relevant objections are an immutable commitment in the soul and no value here relaxes them.';

comment on column forge_projects.verbosity is
    'How much FORGE explains on this project: terse | normal | explanatory.';
