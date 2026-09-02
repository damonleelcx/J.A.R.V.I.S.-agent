-- 0010_rbac_and_collaboration: membership, second factors, trusted devices,
-- and the room.
--
-- PRD SEC-02 (RBAC, MFA, device trust) and COL-01.
--
-- ===========================================================================
-- 1. Membership replaces owner_id as the authorisation truth
-- ===========================================================================
-- Until now every authorisation check in the codebase was the same line:
--
--     where p.owner_id = $caller
--
-- One owner, no members, no roles. That is why memory's organisation layer was
-- documented as "declared, not enforced" — there was no membership model to
-- enforce it with.
--
-- # The decision that matters: which column is the truth
--
-- forge_projects.owner_id still exists and is still useful — it says who created
-- the project. It is NOT consulted for access any more. Keeping it as a second
-- authorisation path would mean two answers to "may this person read this", and
-- the day they disagree is the day somebody sees something they should not.
--
-- So: membership is the single source of truth for access; owner_id records
-- authorship. Every existing project is backfilled with an owner membership
-- below, so nothing loses access at the moment this migration runs.
--
-- # Why roles are rows and permissions are a table in Go
--
-- The role lives here because the database must be able to enforce "a project
-- always has an owner". What each role MAY DO lives in internal/domain/access,
-- because the permission matrix is two-dimensional and a check constraint
-- spelling it out would be unreadable — the same split as the workspace graph's
-- edge pairings.
create table if not exists forge_project_members (
    project_id text        not null references forge_projects(id) on delete cascade,
    user_id    text        not null references forge_users(id)    on delete cascade,
    -- owner | maintainer | contributor | viewer
    role       text        not null,

    -- Who added them. An access grant nobody made cannot be questioned, which is
    -- the same rule as approvals and incident actions.
    granted_by text        not null references forge_users(id),
    granted_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),

    primary key (project_id, user_id),
    constraint forge_project_members_role_check
        check (role in ('owner','maintainer','contributor','viewer'))
);

create index if not exists forge_project_members_user_idx
    on forge_project_members (user_id, role);

drop trigger if exists forge_project_members_updated_at on forge_project_members;
create trigger forge_project_members_updated_at before update on forge_project_members
    for each row execute function forge_set_updated_at();

-- Backfill: every existing project's creator becomes its owner.
--
-- Conditional insert rather than a one-shot UPDATE, so re-running the chain is
-- harmless and a project that already has members is left alone.
insert into forge_project_members (project_id, user_id, role, granted_by, granted_at)
select p.id, p.owner_id, 'owner', p.owner_id, p.created_at
  from forge_projects p
 where not exists (
     select 1 from forge_project_members m where m.project_id = p.id and m.user_id = p.owner_id
 );

comment on column forge_projects.owner_id is
    'Who created the project. NOT an authorisation path — access is decided by forge_project_members (PRD SEC-02).';
comment on table forge_project_members is
    'PRD SEC-02 RBAC. The single source of truth for who may do what in a project. Permissions per role live in internal/domain/access.';

-- ===========================================================================
-- 2. Second factors (PRD SEC-02 MFA)
-- ===========================================================================
-- # The lockout hazard, and the shape that avoids it
--
-- The obvious design enables a second factor the moment somebody enrols. That
-- locks out every user whose authenticator did not actually end up holding the
-- same secret — a mistyped QR scan, a clock miles out, an app that silently
-- failed to save. They cannot sign in to fix it, because fixing it needs a code.
--
-- So enrolment is two steps and the state is a column: `pending` until the user
-- has produced one correct code, `active` afterwards. Only an active factor is
-- ever required at sign-in, and a pending one blocks nothing.
--
-- # Why the secret is stored, when wave 5 refused to store secrets
--
-- A TOTP secret is not a brokered credential; it is a shared secret this system
-- must hold in order to verify anything at all. There is no environment variable
-- an operator could put it in — it belongs to the user, not to the deployment.
--
-- So it is encrypted at rest (SEC-02's other half) with a key from the process
-- environment. The honest claim: this defends a stolen database. An attacker
-- with the database AND the process environment has both halves. That is the
-- boundary, and it is stated rather than implied.
create table if not exists forge_mfa_factors (
    id         text        primary key,
    user_id    text        not null references forge_users(id) on delete cascade,
    -- totp. One value; the column exists so WebAuthn has somewhere to go.
    kind       text        not null default 'totp',
    -- A name the user gives it, so "which of these is my old phone" is answerable.
    label      text        not null default '',

    -- AES-GCM ciphertext of the shared secret. Never the secret.
    secret_ciphertext bytea not null,
    -- Which key encrypted it, so a future rotation can tell what needs re-wrapping.
    key_id     text        not null default 'default',

    -- pending | active. A pending factor is never required at sign-in.
    status     text        not null default 'pending',
    activated_at timestamptz,
    last_used_at timestamptz,
    -- The last counter step accepted, so a code cannot be replayed inside its
    -- own validity window. Without this, an observed code works for 30 seconds
    -- for anybody who saw it.
    last_step  bigint,

    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),

    constraint forge_mfa_factors_kind_check check (kind in ('totp')),
    constraint forge_mfa_factors_status_check check (status in ('pending','active')),
    constraint forge_mfa_factors_active_has_time
        check (status <> 'active' or activated_at is not null)
);

create index if not exists forge_mfa_factors_user_idx on forge_mfa_factors (user_id, status);
-- One active factor of a kind per user. Two would make "which one did they use"
-- unanswerable and doubles the surface an attacker has to guess against.
create unique index if not exists forge_mfa_factors_one_active
    on forge_mfa_factors (user_id, kind) where status = 'active';

drop trigger if exists forge_mfa_factors_updated_at on forge_mfa_factors;
create trigger forge_mfa_factors_updated_at before update on forge_mfa_factors
    for each row execute function forge_set_updated_at();

-- Recovery codes: the way back in when the authenticator is gone.
--
-- Hashed like passwords and single-use. Storing them in plaintext would make the
-- database a list of second factors, which is exactly the thing MFA is protecting
-- against.
create table if not exists forge_mfa_recovery_codes (
    id         text        primary key,
    user_id    text        not null references forge_users(id) on delete cascade,
    code_hash  text        not null,
    used_at    timestamptz,
    created_at timestamptz not null default now(),
    unique (user_id, code_hash)
);

create index if not exists forge_mfa_recovery_unused_idx
    on forge_mfa_recovery_codes (user_id) where used_at is null;

-- ===========================================================================
-- 3. Device trust (PRD SEC-02)
-- ===========================================================================
-- A device the user has vouched for, so a second factor is not demanded on every
-- sign-in from the same laptop.
--
-- # The rule that keeps this from being a hole
--
-- Trusting a device REQUIRES passing the second factor at that moment. Without
-- that rule, "trust this device" is a way to opt out of MFA: sign in with a
-- password, mark the device trusted, and never be challenged again.
--
-- Trust also expires. A device trusted once and never re-checked is a device
-- somebody sold two years ago.
create table if not exists forge_devices (
    id         text        primary key,
    user_id    text        not null references forge_users(id) on delete cascade,
    -- A hash of the client's fingerprint, never the fingerprint itself: it is a
    -- correlatable identifier and the database has no use for the original.
    fingerprint_hash text  not null,
    label      text        not null default '',
    user_agent text        not null default '',

    first_seen_at timestamptz not null,
    last_seen_at  timestamptz not null,

    -- Null means "seen, not trusted" — which is most devices, and the correct
    -- default for one that has just appeared.
    trusted_at    timestamptz,
    trust_expires_at timestamptz,
    revoked_at    timestamptz,
    revoked_reason text       not null default '',

    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),

    -- Trust has a beginning and an end together. A trusted device with no expiry
    -- is trusted forever, which is the thing this column exists to prevent.
    constraint forge_devices_trust_bounded
        check ((trusted_at is null) = (trust_expires_at is null)),
    unique (user_id, fingerprint_hash)
);

create index if not exists forge_devices_user_idx on forge_devices (user_id, last_seen_at desc);

drop trigger if exists forge_devices_updated_at on forge_devices;
create trigger forge_devices_updated_at before update on forge_devices
    for each row execute function forge_set_updated_at();

-- ===========================================================================
-- 4. The room (PRD COL-01)
-- ===========================================================================
-- "Multi-user voice room with identified speakers and a record of who approved
-- what."
--
-- What is built here is the RECORD: who was present, who said what, and which
-- approvals happened while they were. What is NOT built is a realtime
-- multi-party audio transport — nothing in this architecture carries one, and a
-- room whose transcript is real is useful long before its audio is.
--
-- The record is deliberately transport-agnostic: a turn arrives with a speaker
-- and text, and where it came from is a field. A WebRTC bridge, a phone gateway
-- and somebody typing all write the same row.
create table if not exists forge_rooms (
    id         text        primary key,
    project_id text        not null references forge_projects(id) on delete cascade,
    goal_id    text        references forge_goals(id) on delete set null,
    title      text        not null default '',
    -- open | closed
    status     text        not null default 'open',
    opened_by  text        not null references forge_users(id),
    opened_at  timestamptz not null,
    closed_at  timestamptz,
    created_at timestamptz not null default now(),
    updated_at timestamptz not null default now(),
    constraint forge_rooms_status_check check (status in ('open','closed')),
    constraint forge_rooms_closed_has_time check (status <> 'closed' or closed_at is not null)
);

create index if not exists forge_rooms_project_idx on forge_rooms (project_id, opened_at desc);
create index if not exists forge_rooms_open_idx on forge_rooms (project_id) where status = 'open';

drop trigger if exists forge_rooms_updated_at on forge_rooms;
create trigger forge_rooms_updated_at before update on forge_rooms
    for each row execute function forge_set_updated_at();

-- Who was in the room, and when. Left_at rather than a delete, because "who was
-- present when that was approved" is the question a room record is kept for.
create table if not exists forge_room_participants (
    room_id   text        not null references forge_rooms(id) on delete cascade,
    user_id   text        not null references forge_users(id) on delete cascade,
    joined_at timestamptz not null,
    left_at   timestamptz,
    primary key (room_id, user_id)
);

-- One thing said. The speaker is NOT NULL and there is no anonymous option:
-- an unattributed utterance in a multi-user room is the exact failure COL-01
-- exists to prevent.
--
-- FORGE's own turns are recorded with speaker_kind = 'forge' and no user, which
-- is why the attribution rule is a check constraint rather than a NOT NULL on
-- user_id: "nobody said this" and "FORGE said this" must not look the same.
create table if not exists forge_room_turns (
    id        text        primary key,
    room_id   text        not null references forge_rooms(id) on delete cascade,
    seq       integer     not null,

    -- human | forge
    speaker_kind text     not null,
    speaker_id   text     references forge_users(id) on delete set null,
    -- The speaker's name AS RECORDED AT THE TIME. A transcript that renders
    -- names by joining to forge_users shows a renamed or deleted account's
    -- current state, which is not what was said in the room.
    speaker_label text    not null,

    text      text        not null,
    -- voice | text | api. Where it arrived from, so a transcript can say which
    -- turns were spoken aloud once a transport exists.
    channel   text        not null default 'text',
    said_at   timestamptz not null,

    constraint forge_room_turns_speaker_kind_check check (speaker_kind in ('human','forge')),
    constraint forge_room_turns_channel_check check (channel in ('voice','text','api')),
    -- A human turn names a human. FORGE's turns name no user and say so.
    constraint forge_room_turns_attributed check (
        (speaker_kind = 'human' and speaker_id is not null and length(trim(speaker_label)) > 0)
        or (speaker_kind = 'forge' and speaker_id is null)),
    constraint forge_room_turns_text_nonempty check (length(trim(text)) > 0),
    unique (room_id, seq)
);

create index if not exists forge_room_turns_room_idx on forge_room_turns (room_id, seq);

-- Which approvals happened in which room. A join table rather than a column on
-- forge_approvals, because an approval exists whether or not a room did, and
-- adding a nullable room_id to it would put a collaboration concern inside the
-- engine's own aggregate.
create table if not exists forge_room_approvals (
    room_id     text        not null references forge_rooms(id) on delete cascade,
    approval_id text        not null references forge_approvals(id) on delete cascade,
    linked_at   timestamptz not null default now(),
    primary key (room_id, approval_id)
);

comment on table forge_rooms is
    'PRD COL-01. The durable record of a shared session: who was present, who said what, and which approvals were made. Transport-agnostic; no realtime audio is implemented.';
comment on table forge_room_turns is
    'One utterance, always attributed. A human turn names a human; FORGE names none and says so. There is no anonymous speaker.';
