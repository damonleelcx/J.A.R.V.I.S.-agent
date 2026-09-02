-- 0002_identity: accounts, credentials, sessions, and single-use auth tokens.
--
-- Re-runnable: every statement guards itself, because db.Migrate replays the
-- whole chain on every boot.

-- ---------------------------------------------------------------------------
-- users
-- ---------------------------------------------------------------------------
-- Password material lives on this table rather than in a separate credentials
-- table. Why: a second table would only earn its keep if one account could hold
-- several passwords, which it cannot. Federated identity (PRD SEC-02) will
-- arrive as a distinct forge_user_identities table keyed by (provider, subject),
-- because that genuinely is a one-to-many relationship — a different shape, not
-- a bigger version of this one.
create table if not exists forge_users (
    id                  text        primary key,
    -- Plain text rather than the citext extension: see 0001_bootstrap for why
    -- that extension was removed. Case-insensitive uniqueness is enforced by
    -- the functional index forge_users_email_lower_key below, which needs no
    -- extension and cannot be defeated by search_path.
    --
    -- Application code normalises to lower case before every read and write
    -- (identity.NormalizeEmail). The index is the backstop for the code path
    -- that forgets: without it, "Ada@x.com" and "ada@x.com" would become two
    -- accounts that render identically in every interface.
    email               text        not null,
    -- Null until the address is proven. Kept as a timestamp rather than a
    -- boolean so "when did this become trusted?" is answerable during an audit.
    email_verified_at   timestamptz,
    display_name        text        not null default '',
    -- active | locked | disabled. Text + check rather than a Postgres enum:
    -- adding a value to an enum is a migration that cannot run inside a
    -- transaction on older servers, and this set will grow.
    status              text        not null default 'active',
    password_hash       text        not null,
    -- The algorithm and parameters used, so a future rehash-on-login can tell
    -- which rows are stale without trying to parse them first.
    password_algo       text        not null default 'argon2id',
    -- Every session issued before this instant is invalid. This is how a
    -- password change or reset revokes access everywhere at once without having
    -- to find and delete each session row — a delete that could partially fail.
    password_changed_at timestamptz not null default now(),
    created_at          timestamptz not null default now(),
    updated_at          timestamptz not null default now(),
    constraint forge_users_status_check
        check (status in ('active', 'locked', 'disabled')),
    constraint forge_users_email_nonempty
        check (length(trim(email::text)) > 0),
    constraint forge_users_password_hash_nonempty
        check (length(password_hash) > 0)
);

-- Case-insensitive uniqueness without an extension.
create unique index if not exists forge_users_email_lower_key
    on forge_users (lower(email));

do $$ begin
    if not exists (select 1 from pg_trigger where tgname = 'forge_users_updated_at') then
        create trigger forge_users_updated_at
            before update on forge_users
            for each row execute function forge_set_updated_at();
    end if;
end $$;

-- ---------------------------------------------------------------------------
-- sessions
-- ---------------------------------------------------------------------------
-- Only the SHA-256 of a session token is stored. A database dump therefore
-- cannot be replayed as a set of live sessions.
create table if not exists forge_sessions (
    id            text        primary key,
    user_id       text        not null references forge_users(id) on delete cascade,
    token_hash    bytea       not null unique,
    created_at    timestamptz not null default now(),
    last_seen_at  timestamptz not null default now(),
    -- Absolute expiry, fixed at issue time.
    expires_at    timestamptz not null,
    revoked_at    timestamptz,
    revoked_reason text,
    user_agent    text        not null default '',
    ip            inet,
    constraint forge_sessions_expiry_after_creation
        check (expires_at > created_at)
);

create index if not exists forge_sessions_user_idx
    on forge_sessions (user_id, created_at desc);
-- Partial index: expiry sweeps only ever look at sessions that are still live,
-- so indexing the revoked ones would be paying to store rows nobody queries.
create index if not exists forge_sessions_live_idx
    on forge_sessions (expires_at)
    where revoked_at is null;

-- ---------------------------------------------------------------------------
-- auth tokens (email verification and password reset)
-- ---------------------------------------------------------------------------
-- One table, discriminated by purpose, because both kinds have an identical
-- lifecycle: issued once, hashed at rest, expiring, single-use. Two tables
-- would be the same columns twice and two places to get the redemption race
-- wrong.
create table if not exists forge_auth_tokens (
    id            text        primary key,
    user_id       text        not null references forge_users(id) on delete cascade,
    purpose       text        not null,
    token_hash    bytea       not null unique,
    created_at    timestamptz not null default now(),
    expires_at    timestamptz not null,
    -- Set exactly once, by a conditional UPDATE, which is what makes redemption
    -- single-use under concurrency. See identity.ConsumeToken.
    consumed_at   timestamptz,
    requested_ip  inet,
    constraint forge_auth_tokens_purpose_check
        check (purpose in ('email_verify', 'password_reset')),
    constraint forge_auth_tokens_expiry_after_creation
        check (expires_at > created_at)
);

create index if not exists forge_auth_tokens_user_purpose_idx
    on forge_auth_tokens (user_id, purpose, created_at desc);
create index if not exists forge_auth_tokens_live_idx
    on forge_auth_tokens (expires_at)
    where consumed_at is null;

-- ---------------------------------------------------------------------------
-- sign-in attempts
-- ---------------------------------------------------------------------------
-- Keyed by email rather than user_id, deliberately: attempts against an address
-- that does not exist are exactly the ones worth rate-limiting, and those have
-- no user row to point at.
create table if not exists forge_signin_attempts (
    id         text        primary key,
    email      text        not null,
    succeeded  boolean     not null,
    ip         inet,
    user_agent text        not null default '',
    created_at timestamptz not null default now()
);

-- Callers normalise before querying, so the index is on the normalised form.
create index if not exists forge_signin_attempts_email_idx
    on forge_signin_attempts (lower(email), created_at desc);
create index if not exists forge_signin_attempts_created_idx
    on forge_signin_attempts (created_at);

comment on table forge_users is 'Accounts. password_changed_at revokes every session issued before it.';
comment on table forge_sessions is 'Live sessions. Only the SHA-256 of the token is stored.';
comment on table forge_auth_tokens is 'Single-use, hashed, expiring tokens for email verification and password reset.';
comment on table forge_signin_attempts is 'Sign-in audit and lockout input. Keyed by email so attempts on non-existent accounts are counted too.';
