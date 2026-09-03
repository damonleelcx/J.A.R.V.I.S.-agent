-- 0015_tool_call_risk_tier: the tier a call actually ran at.
--
-- PRD SAF-01 (dynamic risk classification), SAF-06 (tamper-evident audit).
--
-- ===========================================================================
-- 1. Why the ledger has to carry this
-- ===========================================================================
-- Until SAF-01 the tier was a constant on the tool, so the ledger did not need
-- to record it: the tool name told you the tier, forever. Once the tier depends
-- on the call — whether the effect was reversible, which permissions it
-- exercised, whether this was production — the tool name no longer tells you
-- anything, and "why did that run without an approval" stops being answerable
-- from rows.
--
-- A classification nobody can audit is a classification that cannot be
-- questioned, which is the same as not having one.
--
-- ===========================================================================
-- 2. Why nullable, and why nothing is back-filled
-- ===========================================================================
-- Every call recorded before this column existed ran under the static scheme.
-- Its tier is not unknown-but-guessable; it is simply not a fact that was
-- captured. Filling it in from the tool's current contract would be inventing
-- evidence about calls nobody classified — the same mistake SAF-06 refuses when
-- it reports eleven pre-chain events as unattestable rather than minting
-- attestations for them.
--
-- So NULL means "this call predates dynamic classification", it is a true
-- statement, and a reader can tell it apart from r0.

alter table forge_tool_calls
    add column if not exists risk_tier text;

-- Constrained but not required: a row either carries a tier this build
-- classified, or carries none at all. An unrecognised string is neither.
alter table forge_tool_calls
    drop constraint if exists forge_tool_calls_risk_tier_check;
alter table forge_tool_calls
    add constraint forge_tool_calls_risk_tier_check
    check (risk_tier is null or risk_tier in ('r0', 'r1', 'r2', 'r3', 'r4', 'r5'));

comment on column forge_tool_calls.risk_tier is
    'The tier this call was classified at when it ran, which may be higher than the tool declares (PRD SAF-01). NULL means the call predates dynamic classification and was never classified — not that it was r0.';
