-- 0017_goal_clarification: the question FORGE asked, and whether anybody answered.
--
-- PRD RSN-02 (Socratic clarification before consequential work; labeled
-- assumptions permitted for low-risk exploration).
--
-- ===========================================================================
-- 1. What existed: an asking with nothing behind it
-- ===========================================================================
-- The planner could already refuse to guess. When a goal was ambiguous in a way
-- that changed what should be built, it returned `clarification_needed` instead
-- of a plan, and forgectl printed the question.
--
-- Then the question was gone. It was never written down, so:
--
--   * nothing recorded that FORGE had asked, which means nothing could tell
--     "waiting for an answer" apart from "planning failed";
--   * there was no answer to give — no field to put one in and nothing that
--     would read it;
--   * and nothing was held. `goal replan` simply asked the model again, and a
--     second roll that did not ask produced a plan built on the ambiguity
--     nobody resolved.
--
-- An instruction to ask is not a gate. This is the same distinction SAF-02 turns
-- on: putting something in front of a model is necessary and is not sufficient,
-- and what makes it a rule is a check somewhere the model cannot reach.
--
-- ===========================================================================
-- 2. Why two columns and not a table
-- ===========================================================================
-- A goal has at most one outstanding question. The planner returns one string,
-- and a second question replaces the first rather than queueing behind it —
-- there is no "answer question 2 of 3" state in the product, and inventing the
-- schema for one would be building a shape nothing produces.
--
-- History is not lost by this. The timeline already records planning events, and
-- the audit chain covers the transitions; what these columns carry is the
-- CURRENT state, which is what the gate reads.
--
-- ===========================================================================
-- 3. Why the answer is a column and not a status
-- ===========================================================================
-- Because "is this goal waiting on somebody" must be answerable without a join
-- and without interpreting a status. A goal blocked on a question is still a
-- draft — the same state it was in — and adding a status for it would put a
-- fifth value into a transition table every reader already switches on, to
-- express something two nullable columns say exactly.
--
-- question IS NULL           -> nothing was asked
-- question, answer IS NULL   -> asked and outstanding; this is what the gate reads
-- question and answer set    -> answered, and the answer is what it was

alter table forge_goals
    add column if not exists clarification_question text;
alter table forge_goals
    add column if not exists clarification_answer text;

-- An answer to a question nobody asked is not a state this product has, and it
-- would read as "answered" to the gate.
alter table forge_goals
    drop constraint if exists forge_goals_answer_needs_question;
alter table forge_goals
    add constraint forge_goals_answer_needs_question
    check (clarification_answer is null or clarification_question is not null);

comment on column forge_goals.clarification_question is
    'The question the planner refused to guess past (PRD RSN-02). NULL means none is outstanding. Consequential work is held while this is set and the answer is not.';

comment on column forge_goals.clarification_answer is
    'The answer somebody gave. NULL alongside a question means the goal is waiting on a person.';
