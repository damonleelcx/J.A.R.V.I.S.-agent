# `CREATE EXTENSION IF NOT EXISTS` broke every fresh database

**Date:** 2026-09-02 · **Phase:** 1→2 · **Severity:** high — the schema could not
be created on a clean database · **Owner:** platform/db

## Symptom

`make check` passed locally. The same commit failed in CI:

```
--- FAIL: TestMigrationsAreIdempotent
    first migration run failed: migration 0002_identity failed and was rolled back:
    ERROR: type "citext" does not exist (SQLSTATE 42704)
--- FAIL: TestMigrationLedgerCountsRuns
--- FAIL: TestChecksumDriftIsReportedNotFatal
```

A brand-new deployment would have hit the same thing: **the product could not
create its own schema on a clean database.**

## Root cause

`0001_bootstrap.sql` ran:

```sql
create extension if not exists citext;
```

and `0002_identity.sql` declared `email citext`.

`CREATE EXTENSION IF NOT EXISTS` has an asymmetry that is easy to miss:

- the **existence check is database-wide** — it asks "is this extension
  installed anywhere in this database?"
- the **installation is schema-local** — the types land in one schema, chosen
  from `search_path`.

So with the test suite creating several schemas concurrently:

1. Run A (schema `forge_it_x`) creates `citext` **inside `forge_it_x`**.
2. Run B (schema `forge_test_y`, `search_path=forge_test_y`) executes the same
   statement. The extension already exists *in the database*, so `IF NOT EXISTS`
   does nothing.
3. Run B then reaches `email citext` — and `citext` is not on its `search_path`.
   `type "citext" does not exist`.

## Why local passed and CI failed

A developer machine has run `make migrate` against the default schema at some
point, which installs `citext` into **`public`**. The test harness sets
`search_path=<test schema>,public`, so `citext` always resolves through `public`
and the bug is invisible.

CI's Postgres service container is fresh on every run: nothing in `public`,
nothing to mask it. **This is the failure mode CI exists for, and the reason
"it passes on my machine" is not evidence about a first install.**

## Fix

The extension was removed entirely, because it was never buying anything.

`identity.NormalizeEmail` already lower-cases and trims every address before
every read and write, so the case-insensitive *type* was redundant. What was
genuinely needed — a database-level backstop for a code path that forgets to
normalise — is a functional index, which requires no extension:

```sql
email text not null,
...
create unique index if not exists forge_users_email_lower_key
    on forge_users (lower(email));
```

Queries changed to `where lower(email) = $1` so they use that index, with the
argument already normalised by the caller.

**Rejected alternatives:**

| Option | Rejected because |
|---|---|
| `CREATE EXTENSION ... WITH SCHEMA public` | Still skipped by `IF NOT EXISTS` when the extension exists elsewhere, and makes every migration depend on `public` being writable and on the search path. |
| Install extensions as a deployment prerequisite | Pushes a manual step into every install, which is precisely the "not reproducible on a clean machine" problem. |
| Schema-qualify every use (`public.citext`) | Works, but hardcodes a schema into the schema — and buys nothing over a functional index. |

## Regression fences

**`TestMigrationsRunInTwoIsolatedSchemas`** migrates the chain into schema A and
then schema B, each with `search_path` excluding `public`. It generalises past
citext to *any* dependency on a database-wide object one schema creates and
another assumes.

The two-schema shape is load-bearing. **A single isolated schema does not
reproduce the bug** — with the extension absent everywhere, that schema simply
creates its own and passes. The defect lives only in the gap between two runs.
The first version of this fence used one schema, was drilled against restored
citext, and **passed against genuinely broken code**. It was rewritten until it
failed for the right reason:

```
migration run 2 of 2 (schema forge_test_iso_b) failed:
  ERROR: type "citext" does not exist (SQLSTATE 42704)
```

**`TestNoExtensionsAreRequired`** states the resulting rule directly: no
migration may contain `create extension`. Cheap, exact, and it fires on the
first line of a reintroduction rather than on its downstream consequence.

## Measurement traps hit while fixing this

- The first version of the isolation guard used `strings.Contains(searchPath,
  "public")` and matched the word *public* inside the test's **own schema name**
  (`forge_test_testmigrationsneednothingfrompublic`), failing a correctly
  isolated connection. Now it compares `search_path` entries, not substrings.
- The first drill "passed" — see above. A fence is not proven until it has been
  seen to fail *for the reason it exists*, not merely to fail.

## Rule this establishes

> A migration may only use objects it creates itself, or that are schema-qualified.
> Nothing may depend on an object that happens to already exist in another schema.

Corollary: verify a schema chain against an **empty** database, not a developer
database. The developer database is a mask.
