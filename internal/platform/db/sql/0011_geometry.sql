-- 0011_geometry: geometry that survives the turn that proposed it.
--
-- PRD VIS-04 (variants side by side, each render linking geometry version,
-- inputs, units, assumptions, generator, verification status) and VIS-05
-- (mesh export, labelled).
--
-- ===========================================================================
-- 1. Why there is no forge_variants table
-- ===========================================================================
-- VIS-04 asks for VARIANTS. The obvious reading is a variant table with its own
-- identifiers, its own versioning and its own review state — and it would
-- duplicate forge_artifact_versions, which already carries WRK-04's seven facts
-- for exactly this reason. Five of the six things VIS-04 wants a render to link
-- to are already columns there:
--
--     geometry version    -> forge_artifact_versions.version
--     inputs              -> forge_artifact_versions.inputs
--     generator           -> forge_artifact_versions.agent (+ generator below)
--     verification status -> forge_artifact_versions.verification_state
--     (and the human's separate ruling in human_disposition)
--
-- Only the geometry itself has nowhere to live. So:
--
--   A VARIANT IS A GEOMETRY VERSION. Comparing is picking several of them.
--
-- That means there is nothing to store for a comparison either: the side-by-side
-- view is DERIVED from the versions it names, the same rule the COL-02 handoff
-- follows. A stored comparison goes stale the moment any variant moves, which is
-- the worst possible property for a document whose whole job is to be believed.
--
-- ===========================================================================
-- 2. Why an extension table rather than a content column
-- ===========================================================================
-- The alternative was `alter table forge_artifact_versions add column content`.
-- Rejected: a file artifact's content lives on disk inside the project's
-- workspace, so that column would be empty for almost every row — and an empty
-- content column cannot be told apart from "this artifact's content is
-- elsewhere". A nullable column would assert that any version MAY carry
-- geometry, which is false.
--
-- forge_geometry is a 1:0..1 extension: a row exists exactly when a version has
-- geometry, and its absence is a fact rather than a gap. It is append-only for
-- the same reason its parent is — a version is one immutable change — so there
-- is no updated_at and no trigger.

-- ===========================================================================
-- 3. 'converse' joins the actor vocabulary
-- ===========================================================================
-- planner | executor | verifier | human | scheduler | system was written when
-- every actor in the system was part of the goal engine. The workbench
-- conversation is none of them: it proposes geometry, outside any goal, and
-- nothing in the engine runs it.
--
-- The two wrong answers, and why:
--
--   'human'  — a person did not draw it. FORGE did. Recording a machine's
--              proposal as a human's work is the misattribution this whole
--              schema is arranged to prevent (see forge_room_turns).
--   'system' — attributes a proposal to infrastructure. "Forge proposed" and
--              "the system did something" are different claims, and the PRD
--              requires the first to be distinguishable.
--
-- So the vocabulary gains the actor it was missing. It is widened in BOTH
-- places that spell it out, because forge_artifact_versions.agent and
-- forge_events.actor are documented as agreeing about who acted, and a
-- migration that widened one would make that comment false.
alter table forge_events drop constraint if exists forge_events_actor_check;
alter table forge_events add constraint forge_events_actor_check check (actor in
    ('planner','executor','verifier','human','scheduler','system','converse'));

alter table forge_artifact_versions drop constraint if exists forge_artifact_versions_agent_check;
alter table forge_artifact_versions add constraint forge_artifact_versions_agent_check check (agent in
    ('planner','executor','verifier','human','scheduler','system','converse'));

-- The tool-call rule has to widen with it. Its purpose is that no change is
-- untraceable to an action: a human's action is the edit itself, and the
-- conversation's action is the turn, whose prompt is required in `inputs` and
-- whose model is required in forge_geometry.generator below. Neither is a tool
-- invocation, and inventing a tool_call_id to satisfy the constraint would put a
-- fabricated row in the ledger — the exact thing the constraint exists to stop.
--
-- Not added here, deliberately: a foreign key to forge_room_turns. The workbench
-- does not join rooms yet (COL-01 built the record; nothing writes to it from
-- /v1/converse), and a nullable key to a table nothing populates declares a link
-- that does not exist. When the workbench joins a room, that key is the natural
-- addition and this is where it goes.
alter table forge_artifact_versions drop constraint if exists forge_artifact_versions_tool_or_human;
alter table forge_artifact_versions add constraint forge_artifact_versions_tool_or_human check (
    tool_call_id is not null or agent in ('human','converse'));

-- ===========================================================================
-- 4. The geometry itself
-- ===========================================================================
create table if not exists forge_geometry (
    -- The version IS the identity. One geometry per version, and it dies with
    -- it — there is no independent lifecycle to get out of step.
    version_id text        primary key references forge_artifact_versions(id) on delete cascade,

    -- What the assembly is called, as proposed. Copied out of the document
    -- because it is what a person scans a variant list by, and a list that has
    -- to open a jsonb blob per row to render a title is a list nobody indexes.
    name       text        not null,

    -- The proposal: parts, assumptions, and what it does NOT establish. Stored
    -- whole rather than shredded into rows, because the document is what the
    -- renderer replays and a reconstructed one could differ from what the
    -- person actually saw.
    document   jsonb       not null,

    -- --- units, twice, on purpose (PRD WRK-05) ---
    -- units          is the RESOLVED unit, '' when there is none.
    -- units_declared is what the model actually said.
    --
    -- Both, because "no unit was stated" and "a unit was stated and FORGE cannot
    -- convert it" are different failures with different fixes, and one column
    -- collapses them into a blank. An unrecognised unit is never rounded to
    -- millimetres: a wrong guess about scale is the difference between a bracket
    -- and a building.
    units          text    not null,
    units_declared text    not null,

    -- The coordinate frame the positions are in. WRK-05 asks the frame to travel
    -- with the coordinate; until now it existed only as a Go constant, which is
    -- the frame being hoped for rather than the frame travelling.
    frame      text        not null,

    -- VIS-04's "generator": the model that proposed this shape, by id. The
    -- version's `agent` column says WHICH PART of FORGE acted; this says what
    -- actually produced the geometry, and the two answer different questions
    -- when the model behind the workbench changes.
    generator  text        not null,

    created_at timestamptz not null default now(),

    constraint forge_geometry_name_nonempty check (length(trim(name)) > 0),
    -- VIS-04 names the generator as a thing every render must link to. A row
    -- that cannot say what made it is refused at write time, so no reader ever
    -- has to handle a render with an unknown origin.
    constraint forge_geometry_generator_nonempty check (length(trim(generator)) > 0),
    -- The closed unit set, '' meaning unspecified. Kept in step with
    -- internal/agent/units.go by TestGeometrySchema_UnitVocabularyMatchesGo.
    constraint forge_geometry_units_check check (units in ('','mm','cm','m','in')),
    constraint forge_geometry_frame_nonempty check (length(trim(frame)) > 0),
    -- A geometry document is an object, never an array or a bare scalar. Cheap
    -- here, and it stops a malformed write from reaching every reader.
    constraint forge_geometry_document_is_object check (jsonb_typeof(document) = 'object')
);

-- "Show me this project's variants, newest first" is the only listing query,
-- and it joins through forge_artifact_versions to forge_artifacts. The index
-- that matters is already on (artifact_id, version desc) there; this one lets
-- the geometry rows be found from the version side without a sequential scan.
create index if not exists forge_geometry_created_idx on forge_geometry (created_at desc);

comment on table forge_geometry is
    'PRD VIS-04. The geometry a version carries. A VARIANT IS A VERSION: comparison is derived from the versions it names and is never stored.';
comment on column forge_geometry.units is
    'The RESOLVED unit; empty means unspecified. Never guessed — see forge_geometry.units_declared for what was said.';
comment on column forge_geometry.generator is
    'What produced the shape, by model id. forge_artifact_versions.agent says which part of FORGE acted; this says what drew it.';
