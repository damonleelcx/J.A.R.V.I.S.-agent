-- Drop every schema a test or drill run left behind.  `make db-clean-test-schemas`
--
-- Test schemas carry a random per-process id (internal/platform/db/testschema.go)
-- so that two worktrees running `go test ./...` against one Postgres cannot drop
-- each other's schema mid-run.  The cost of that is a run killed part-way leaving
-- its schemas behind, where before the next run of the same test would have
-- reused the name.  This is the sweep.
--
-- Only forge_* schemas, and never `public` or `forge_migrations`: run against the
-- wrong database this should lose scratch, not data.
--
-- A schema another run still holds is SKIPPED with a notice rather than failing
-- the sweep.  It belongs to somebody; the next sweep will get it.  Failing here
-- would make a routine tidy-up depend on nobody else testing at the same time,
-- which is the class of problem this file exists because of.
do $$
declare s text;
begin
  for s in
    select nspname from pg_namespace
     where nspname like 'forge\_%'
       and nspname <> 'forge_migrations'
     order by nspname
  loop
    begin
      execute format('drop schema if exists %I cascade', s);
      raise notice 'dropped %', s;
    exception when others then
      raise notice 'skipped % (%)', s, sqlerrm;
    end;
  end loop;
end $$;
