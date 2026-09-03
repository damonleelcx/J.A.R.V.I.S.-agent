-- 0008_schema_scoped_triggers: repair the updated_at triggers that earlier
-- migrations only created in the first schema of a database.
--
-- # The defect
--
-- Migrations 0002, 0003, 0004 and 0006 attach their updated_at triggers like
-- this:
--
--     do $$ begin
--         if not exists (select 1 from pg_trigger where tgname = 'forge_x_updated_at') then
--             create trigger forge_x_updated_at before update on forge_x ...
--         end if;
--     end $$;
--
-- `pg_trigger.tgname` is not unique across schemas. The catalogue is per
-- DATABASE, so once any schema holds a trigger by that name the guard is true
-- everywhere, and every subsequent schema in that database gets no trigger at
-- all — silently, with the migration reporting success.
--
-- # Who this affected
--
-- Production has one schema, so the first run creates everything and nothing was
-- ever wrong there. What was wrong is the integration test harness, which builds
-- a schema per test in a shared database: every one of those schemas after the
-- first has been missing every updated_at trigger. Tests that set updated_at
-- explicitly — which is most of them, because the application clock owns
-- timestamps here — could not tell. See
-- docs/bugfix/2026-09-02-trigger-guards-were-not-schema-scoped.md.
--
-- # The repair, and why it is shaped this way
--
-- `drop trigger if exists ... on <table>` resolves the table through the search
-- path, so it names exactly the trigger in the schema being migrated. Followed by
-- an unconditional `create trigger`, it is idempotent without any guard at all —
-- which is better than a corrected guard, because it removes the failure mode
-- rather than fixing one instance of it. Every trigger added from here on uses
-- this shape.

drop trigger if exists forge_users_updated_at on forge_users;
create trigger forge_users_updated_at before update on forge_users
    for each row execute function forge_set_updated_at();

drop trigger if exists forge_projects_updated_at on forge_projects;
create trigger forge_projects_updated_at before update on forge_projects
    for each row execute function forge_set_updated_at();

drop trigger if exists forge_goals_updated_at on forge_goals;
create trigger forge_goals_updated_at before update on forge_goals
    for each row execute function forge_set_updated_at();

drop trigger if exists forge_tasks_updated_at on forge_tasks;
create trigger forge_tasks_updated_at before update on forge_tasks
    for each row execute function forge_set_updated_at();

drop trigger if exists forge_memory_updated_at on forge_memory;
create trigger forge_memory_updated_at before update on forge_memory
    for each row execute function forge_set_updated_at();

drop trigger if exists forge_decisions_updated_at on forge_decisions;
create trigger forge_decisions_updated_at before update on forge_decisions
    for each row execute function forge_set_updated_at();
