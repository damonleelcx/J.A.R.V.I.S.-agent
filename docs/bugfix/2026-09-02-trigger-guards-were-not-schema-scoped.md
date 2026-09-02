# Every integration test ran against a schema with no `updated_at` triggers

**Date:** 2026-09-02 · **Phase:** wave 4 (workspace model) · **Severity:** medium
— production was never affected; the test harness was wrong about what it was
testing · **Owner:** platform/db

## Symptom

A new fence in wave 4 asserted that deleting an artifact takes its graph anchor
with it, via `on delete cascade`. It failed:

```
--- FAIL: TestArtifacts_JoinTheGraphAsAnchors
    deleting the artifact left its graph anchor pointing at nothing
```

The foreign key was in the migration and applied cleanly. It was simply not
present in the schema the test ran against.

## Root cause

Migrations 0002, 0003, 0004, 0006 and (as first written) 0007 attach their
`updated_at` triggers like this:

```sql
do $$ begin
    if not exists (select 1 from pg_trigger where tgname = 'forge_x_updated_at') then
        create trigger forge_x_updated_at before update on forge_x ...
    end if;
end $$;
```

`pg_trigger` is a **per-database** catalogue, not a per-schema one. `tgname` is
unique per table, not globally — so the guard matches a trigger of that name in
*any* schema. Once `public` holds one, the guard is true everywhere, and every
subsequent schema in that database gets no trigger at all. The migration reports
success. Migration 0007 had the same bug in `pg_constraint` for the artifact
foreign key.

Reproduced directly:

```
$ psql -c "create schema probe_a"
$ FORGE_DATABASE_URL="...&search_path=probe_a" forgectl migrate      # reports success
  nspname | artifact_fk | memory_trigger | projects_trigger
  probe_a |           0 |              0 |                0
  public  |           1 |              1 |                1
```

## Who this affected

**Production: nobody.** A deployment has one schema. The first migration run
creates everything and the guards never fire again.

**The integration test harness: everything.** It builds a schema per test in a
shared database, so every schema after the first has been running without any
`updated_at` trigger. The harness's stated claim — "the real migration chain, not
a hand-written CREATE TABLE: a fixture that invents its own schema tests the
fixture" — was true about tables, indexes and check constraints, and false about
triggers.

Nothing went falsely green. The application clock owns timestamps in this
codebase, so almost every write sets `updated_at` explicitly and could not tell
the difference. The exception is `identity.MarkEmailVerified`, which updates a
row without setting it and therefore relied on the trigger — no test asserts the
resulting `updated_at`, so nothing was wrong, but nothing would have caught it.

## What was fixed

`0008_schema_scoped_triggers.sql` re-attaches every trigger, and 0007 now uses
the same shape for its own trigger and constraint:

```sql
drop trigger if exists forge_x_updated_at on forge_x;
create trigger forge_x_updated_at before update on forge_x ...
```

`drop … if exists … on <table>` resolves the table through the search path, so it
names exactly the object in the schema being migrated. Followed by an
unconditional create, it is idempotent **with no guard at all** — which is better
than a corrected guard, because it removes the failure mode instead of fixing one
instance of it. Migrations 0002/0003/0004/0006 are not edited: they are applied
and checksummed, and 0008 repairs what they left out.

## Acceptance, and a fence that was not one

Two tests, and the second exists because the first was proved insufficient.

`TestTwoSchemasGetTheSameObjects` compares the catalogues of two migrated
schemas. It is useful, and a mutation drill showed it **cannot catch this bug**:
both of its schemas are created after `public` already holds every trigger name,
so the guard skips the triggers in *both* and the two compare equal. Restoring
the original buggy guard left it green.

`TestEveryUpdatedAtColumnHasItsTrigger` checks a freshly migrated schema against
a rule instead: an `updated_at` column is a promise that the row records when it
last changed, and the trigger is the only thing that keeps it. A table with the
column and nothing maintaining it fails, whether that schema is the first in the
database or the fiftieth. Under the restored buggy guard it goes red.

The pre-existing `TestMigrationsRunInTwoIsolatedSchemas` was green throughout.
It asserts the chain *runs* in two schemas. It did.

## Prevention

Three things generalise:

1. **A per-database catalogue cannot answer a per-schema question.** `pg_trigger`,
   `pg_constraint`, `pg_proc` and `pg_type` are all database-wide. Guard on the
   relation, or do not guard — prefer `drop … if exists` + create.
2. **"It ran without error" is not "it built the right thing."** The chain
   reported success in every schema for as long as this bug existed.
3. **A fence that compares two subjects is blind to a defect that affects both
   equally.** Compare against the rule, not against a sibling. This one was found
   by drilling the fence, not by reading it.
