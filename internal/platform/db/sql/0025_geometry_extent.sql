-- 0025_geometry_extent: a design's corners, worked out once when it is stored.
--
-- GET /v1/geometry/{id} carries the design's overall dimensions (geometry.Measure),
-- and finding them places every occurrence the design has: 2.1-3.1 s for a stored
-- 1,020,782-part fleet on every open of it (docs/spikes/2026-09-17-large-designs).
-- A version is immutable, so its corners never change once it is written. They are
-- kept here, and a read measures from them (internal/domain/geometry/stored_extent.go).
--
-- # Why this is not a second truth
--
-- 0011 made forge_geometry append-only with no updated_at, because a version is one
-- immutable change. This column is DERIVED from the immutable document, carries the
-- revision of the arithmetic that derived it ({"rev": n, ...}), and is written in
-- exactly two places: in the insert, and once on the first read of a row that has
-- none (stored before this migration) or holds one from an earlier revision. Nothing
-- else about the row is ever updated.
--
-- Null means "not worked out yet", never "no extent": a design whose tree places
-- nothing sizable has {"empty": true}.
alter table forge_geometry add column if not exists extent jsonb;

comment on column forge_geometry.extent is
    'The corners geometry.bounds finds, kept when the version is stored so reading it places no part. Null: not worked out yet (stored before 0025).';
