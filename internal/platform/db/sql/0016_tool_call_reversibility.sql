-- 0016_tool_call_reversibility: whether what a call did can be taken back.
--
-- PRD AGT-05 (interrupt, rollback, recovery; partial failure leaves a truthful
-- recovery plan).
--
-- ===========================================================================
-- 1. The question a recovery plan has to answer from rows
-- ===========================================================================
-- After a goal fails partway, the question is not "which tasks failed" — the
-- task rows already answer that. It is "what did it leave behind, and which of
-- it can be undone". A plan that cannot separate an effect somebody can revert
-- from one nobody can is not a recovery plan; it is a list.
--
-- Reversibility is declared on the tool's contract, so it could in principle be
-- looked up at read time. That is the wrong place to read it from, for the same
-- reason 0015 records the risk tier rather than deriving it: a contract states
-- what the tool does TODAY, and a recovery plan is a statement about what
-- happened THEN. A tool whose reversibility was tightened after a failure would
-- otherwise rewrite history in the direction of reassurance.
--
-- It also removes a drift hazard. Deriving reversibility at read time means the
-- reader needs the tool registry, which means every reader assembles the same
-- registration list, and a connector registered in one process and not another
-- reads as "unknown tool" rather than as what it was.
--
-- ===========================================================================
-- 2. Why nullable, and why nothing is back-filled
-- ===========================================================================
-- The same answer as 0015 and for the same reason. Calls recorded before this
-- column existed did not capture it. Filling them in from the tool's current
-- contract would be asserting something about those calls that nobody recorded,
-- and a recovery plan built on invented facts is worse than one that says it
-- does not know.
--
-- NULL therefore means "not captured", and the recovery plan says so in those
-- words rather than treating it as reversible.

alter table forge_tool_calls
    add column if not exists reversibility text;

alter table forge_tool_calls
    drop constraint if exists forge_tool_calls_reversibility_check;
alter table forge_tool_calls
    add constraint forge_tool_calls_reversibility_check
    check (reversibility is null
           or reversibility in ('no_effect', 'automatic', 'manual', 'irreversible'));

comment on column forge_tool_calls.reversibility is
    'Whether this call''s effect could be taken back, as the tool declared it AT THE TIME (PRD AGT-05). NULL means the call predates this column and it was never captured — not that the effect was reversible.';
