-- 0018_goal_options: the criteria, the options argued against them, and the choice.
--
-- PRD RSN-03 (materially different options with tradeoffs and stated criteria).
--
-- ===========================================================================
-- 1. What existed: alternatives, but only after the fact
-- ===========================================================================
-- The decision log (MEM-03) already records what was chosen and what was
-- rejected, with a required reason per rejection. That is the record of a
-- choice ALREADY MADE. It is written by whoever made it, afterwards, and it
-- cannot be the mechanism RSN-03 asks for, because by the time it exists the
-- person the options were supposed to help has already decided.
--
-- Nothing in the system offered options. FORGE picked an approach inside the
-- planner's rationale paragraph and the alternatives it did not take were never
-- named, so "we considered X and Y" was unfalsifiable in exactly the direction
-- that flatters the system.
--
-- ===========================================================================
-- 2. Why the criteria are a separate column, written first
-- ===========================================================================
-- "Stated criteria" is the whole requirement. Options generated and criteria
-- generated in one breath is the failure mode, not the feature: a model asked
-- for both will write the criteria its preferred option wins on, and the result
-- reads exactly like a considered comparison. There is no way to inspect an
-- answer and tell the two apart.
--
-- So criteria are prior in the record, not merely prior in the prompt. They are
-- written by a separate command before any option exists, and `goal options`
-- refuses to run when this column is null. Prior is then a fact about the row
-- rather than a claim about the reasoning.
--
-- Restating the criteria clears the options (see 4). Options were argued against
-- a specific set of criteria, and criteria edited afterwards would let somebody
-- keep the options and change the basis they were judged on — which is the same
-- laundering, one step later.
--
-- ===========================================================================
-- 3. Why the ratings are an ordinal scale and not a score
-- ===========================================================================
-- Two of the three checks in agent/options.go are comparisons: are any two
-- options identical on every criterion (then they are one option written twice),
-- and does one option beat every other on everything (then it is a
-- recommendation surrounded by strawmen). Both need an ORDER over how an option
-- stands on a criterion, and nothing finer.
--
-- Three levels — strong / adequate / weak — is the coarsest scale that can say
-- better, same and worse. A 1-10 score would be finer and would be worse: the
-- distance between a 6 and a 7 is not a fact about the world, it is a token the
-- model emitted, and putting it in a column invites arithmetic on it.
--
-- ===========================================================================
-- 4. Why three columns and not a table
-- ===========================================================================
-- Same reasoning as 0017. A goal has at most one open choice; a new option set
-- replaces the previous one rather than queueing behind it. The set is read
-- whole, by one gate and one renderer, and is never queried across goals or
-- joined against — a table would buy indexing nothing asks for and cost a
-- lifecycle (orphans, cascade, "which set is current") that the product does
-- not have.
--
-- option_criteria IS NULL                 -> nothing stated; `goal options` refuses
-- criteria set, options IS NULL           -> criteria stated, no options yet
-- options set, chosen_option IS NULL      -> offered and outstanding; the gate reads this
-- options and chosen_option set           -> chosen, and which one is on the row

alter table forge_goals
    add column if not exists option_criteria jsonb;
alter table forge_goals
    add column if not exists options jsonb;
alter table forge_goals
    add column if not exists chosen_option text;

-- A choice with nothing to choose from is not a state this product has, and it
-- would read as "chosen" to the gate — releasing a goal that was never offered
-- anything.
alter table forge_goals
    drop constraint if exists forge_goals_choice_needs_options;
alter table forge_goals
    add constraint forge_goals_choice_needs_options
    check (chosen_option is null or options is not null);

-- Options with no criteria cannot have been argued against any, and the
-- validator that refuses them runs in the application. This is the same rule at
-- the storage layer, so a row written by hand cannot present an option set that
-- no criteria ever judged.
alter table forge_goals
    drop constraint if exists forge_goals_options_need_criteria;
alter table forge_goals
    add constraint forge_goals_options_need_criteria
    check (options is null or option_criteria is not null);

comment on column forge_goals.option_criteria is
    'The stated basis for choosing (PRD RSN-03), written BEFORE any option exists. Restating it clears options and chosen_option, because options were argued against the criteria that stood when they were written.';

comment on column forge_goals.options is
    'The option set FORGE offered, each option rating every criterion above. NULL means no choice is open. Consequential work is held while this is set and chosen_option is not.';

comment on column forge_goals.chosen_option is
    'The key of the option somebody chose. NULL alongside options means the goal is waiting on a person.';
