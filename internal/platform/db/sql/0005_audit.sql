-- 0005_audit: the timeline becomes tamper-evident.
--
-- PRD SAF-06 asks for a tamper-evident audit of inputs, plans, tool calls,
-- versions, approvals, policies and evidence. Every one of those already writes
-- to forge_events, so the audit surface is that table — what it lacked was any
-- way to tell whether what it says now is what it said then.
--
-- # What these columns give you
--
-- Each event carries the hash of the one before it, per goal. That makes the
-- timeline a chain: editing a summary, deleting an event, or reordering two of
-- them breaks every link after the change, and the break points at the first
-- altered row rather than merely reporting that something is wrong somewhere.
--
-- # What it does NOT give you
--
-- This is tamper-EVIDENT, not tamper-proof. Anyone who can write to this table
-- can also recompute the chain. It defends against silent edits — a row changed
-- by a bug, a migration, a careless UPDATE, or someone who did not know the
-- chain existed — which is the realistic threat for an audit log inside its own
-- database. Defending against an attacker who owns the database requires
-- shipping digests off-box, and that is not claimed here.
--
-- # Why the columns are nullable
--
-- Rows written before this migration have no hash and cannot be given one
-- honestly: their content is known but their ORDER of writing is only inferable.
-- Backfilling a chain over them would manufacture evidence that nobody actually
-- recorded, which is exactly the kind of thing an audit log must never do. They
-- stay null, verification reports them as "unchained", and the chain starts at
-- the first event that has one.

alter table forge_events add column if not exists prev_hash      text;
alter table forge_events add column if not exists hash           text;
-- The payload is hashed separately from the rest of the row. jsonb is stored
-- normalised — key order and number formatting are the database's choice, not
-- ours — so the digest is taken over a canonical form produced in Go, and a
-- payload mismatch is reported as its own finding rather than as a chain break.
alter table forge_events add column if not exists payload_digest text;

-- Verification walks a goal's events in sequence order; this is that walk.
create index if not exists forge_events_chain_idx on forge_events (goal_id, seq)
    where hash is not null;
