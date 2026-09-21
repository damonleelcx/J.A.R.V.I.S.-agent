-- 0028_autonomy_is_write_once: the ladder only ever goes down (PRD AGT-04).
--
-- Issue 21.
--
-- ===========================================================================
-- 1. The property, and why it was not held by anything
-- ===========================================================================
-- AGT-04: "Progressive autonomy; never silently raises its own autonomy level."
--
-- It is true today. Autonomy is set once, at goal creation, and there is no
-- mutation path: no UpdateAutonomy in Go, and until this migration no `update
-- forge_goals` anywhere in the tree touched the column. The ladder itself is
-- carefully built — ordered so levels compare, with `prohibited` ranked -1 so it
-- satisfies nothing including itself — and three tests hold that ordering.
--
-- None of the three holds the property. All of them stay green on the day
-- somebody adds an endpoint that bumps a running goal's autonomy. The headline
-- guarantee rested on the ABSENCE of a code path, and absence is not fenced:
-- the next person adding "let the user raise it without re-planning" had nothing
-- telling them they were touching a PRD guarantee.
--
-- ===========================================================================
-- 2. Why the rule is in the database and not only in Go
-- ===========================================================================
-- Because a rule in Go is a rule for the writers who found it. This one has to
-- survive a writer who did not: a new handler, a repair script, a migration
-- somebody wrote at 2am, a hand-edited row. The fence in
-- internal/httpapi/autonomy_fence_test.go catches a new writer in this
-- repository by name; the trigger catches the write itself, including the ones
-- that never went through a review.
--
-- It refuses rather than recording, because a trigger cannot do both: an
-- exception rolls back anything the same transaction wrote, so a row logging the
-- attempt would roll back with it. The refusal is the enforcement and the Go
-- surfaces own the audit event (logx.EventAutonomyRaiseRefused).
--
-- ===========================================================================
-- 3. The ranking, and why prohibited is not in it
-- ===========================================================================
-- Mirrors engine.RaisesAutonomy exactly, and the two are held together by
-- TestSchemaAndCodeAgreeOnWhatARaiseIs.
--
--   discuss < draft < sandbox_execute < approval_gated
--
-- `prohibited` is off the ladder rather than at the bottom of it. Moving OFF
-- prohibited to anything is the largest raise there is — a refusal becoming a
-- permission — and moving TO prohibited is never a raise, whatever it came from.
-- Giving it an ordinal inside the ladder is exactly how an off-by-one enables
-- it, which is why state.go ranks it -1 and why this function does not rank it
-- at all.
--
-- ===========================================================================
-- 4. What is deliberately still allowed
-- ===========================================================================
-- Lowering, and setting the level at INSERT. Narrowing a goal mid-run is a
-- safety act — it is what an incident response would reach for — and refusing it
-- here would make the database the reason FORGE could not calm something down.
-- Creation is where autonomy is chosen, in front of the person choosing it,
-- which is what AGT-04 asks for.

create or replace function forge_autonomy_is_write_once()
returns trigger
language plpgsql
as $$
declare
    rank_old int;
    rank_new int;
begin
    if new.autonomy is not distinct from old.autonomy then
        return new;
    end if;

    -- To prohibited is a stop, never a raise.
    if new.autonomy = 'prohibited' then
        return new;
    end if;

    -- Off prohibited is a refusal becoming a permission.
    if old.autonomy = 'prohibited' then
        raise exception
            'goal %: autonomy may not be raised from % to % (PRD AGT-04). Autonomy is fixed when a goal is created; to work at a higher level, draft a new goal at that level so somebody sees the plan before it runs. See internal/domain/engine/autonomy_write.go.',
            old.id, old.autonomy, new.autonomy
            using errcode = 'check_violation';
    end if;

    rank_old := case old.autonomy
        when 'discuss' then 0 when 'draft' then 1
        when 'sandbox_execute' then 2 when 'approval_gated' then 3 end;
    rank_new := case new.autonomy
        when 'discuss' then 0 when 'draft' then 1
        when 'sandbox_execute' then 2 when 'approval_gated' then 3 end;

    -- A level this build does not recognise is never the safe side of the
    -- comparison. The same rule engine.RaisesAutonomy and access.Role.Allows
    -- follow: an unknown value permits nothing.
    if rank_old is null or rank_new is null or rank_new > rank_old then
        raise exception
            'goal %: autonomy may not be raised from % to % (PRD AGT-04). Autonomy is fixed when a goal is created; to work at a higher level, draft a new goal at that level so somebody sees the plan before it runs. See internal/domain/engine/autonomy_write.go.',
            old.id, old.autonomy, new.autonomy
            using errcode = 'check_violation';
    end if;

    return new;
end;
$$;

drop trigger if exists forge_goals_autonomy_write_once on forge_goals;
create trigger forge_goals_autonomy_write_once
    before update on forge_goals
    for each row execute function forge_autonomy_is_write_once();

comment on function forge_autonomy_is_write_once() is
    'Refuses any update that raises a goal''s autonomy level (PRD AGT-04). Lowering and setting it at insert are allowed; moving off ''prohibited'' is the largest raise there is and is refused like any other.';
