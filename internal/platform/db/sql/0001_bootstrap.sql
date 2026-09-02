-- 0001_bootstrap: extensions and shared helpers.
--
-- Every statement here is idempotent because Migrate re-runs the whole chain on
-- every boot (see db.Migrate for why). "Idempotent" is asserted by
-- TestMigrationsAreIdempotent, which runs the full chain twice against a live
-- database and fails if the second run errors or changes the schema.

-- No extensions.
--
-- An earlier version of this file created `citext` for case-insensitive email.
-- It was removed after CI failed with `type "citext" does not exist` on a fresh
-- database: CREATE EXTENSION IF NOT EXISTS is evaluated DATABASE-wide but
-- installs the type into ONE schema. With tests running concurrently in
-- separate schemas, the first creates it in its own schema and every later one
-- sees "already exists", skips, and then cannot resolve the type. A developer
-- machine hides this completely, because an earlier migration has usually put
-- citext in `public`, which is on the search path.
--
-- Case-insensitive uniqueness is now a functional unique index on lower(email)
-- (see 0002_identity). That needs no extension, is portable, and cannot be
-- defeated by search_path.
-- set_updated_at keeps updated_at honest without every writer remembering to.
-- Why a trigger rather than application code: updated_at is read by the
-- reconciliation paths that decide whether a record is stale. A writer that
-- forgets to bump it makes a stale row look fresh, and that class of bug is
-- invisible until a reconciliation silently does nothing.
create or replace function forge_set_updated_at() returns trigger
language plpgsql as $$
begin
    new.updated_at := now();
    return new;
end;
$$;

-- forge_assert_utc is a documentation-bearing check used by table constraints.
-- All timestamps in this schema are timestamptz and all sessions run in UTC
-- (set in db.Connect); this exists so the intent survives a future reader.
comment on function forge_set_updated_at() is
    'Sets NEW.updated_at to now() on UPDATE. Attach via: create trigger <t>_updated_at before update on <t> for each row execute function forge_set_updated_at();';
