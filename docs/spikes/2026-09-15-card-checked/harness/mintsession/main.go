// Command mintsession creates the local test principal for the card check and
// hands back a session token, without a password ever being typed into a form.
//
// # Why this exists
//
// Checking the proposal card in a real browser needs an authenticated page. The
// obvious route — open /auth/sign-up and type an address and a password — is one
// this agent will not take: typing a credential into a form is exactly the class
// of action that has to stay with a person. The previous attempt at this spike
// stopped there.
//
// The way out is the way the test suites already make a principal: call the
// domain's own constructors against the local development database. Nothing here
// is a new code path — it is identity.Repository.CreateUser, the same
// access.Service.EnsureOwner every test fixture calls, and a session row built
// exactly as identity.Service.SignIn builds one. The password hash it writes is
// for a throwaway value that is never printed, never stored outside the hash and
// never used: sign-in is not the route in, the session row is.
//
// # Why the session is minted here rather than by signing in
//
// SignIn's contract is "prove you know the password, receive a session". This
// program already holds the authority that check protects — it is writing the
// user row itself — so re-deriving it through a password would add a credential
// to the world for no gain. What it must NOT do is invent a different kind of
// session: the token is auth.NewToken (32 random bytes, URL-safe), only its
// SHA-256 is stored, created_at and expires_at come from the application clock,
// and the row goes in through Repository.CreateSession. A session minted any
// other way would be testing a fiction.
//
// # Why the email is marked verified
//
// identity.User.CanSignIn lets an unverified account in, but the consequential
// surface — creating goals, running tools — is gated on verification. A build
// goal is the whole point of the check, so the address is marked proven through
// the repository's own MarkEmailVerified rather than by reaching into the column.
//
// It is idempotent: run it again and it reuses the user and the project and
// mints a fresh session, so a session can be replaced without disturbing the
// work already filed under the account.
//
// Usage:
//
//	FORGE_DATABASE_URL=... go run ./docs/spikes/2026-09-15-card-checked/harness/mintsession \
//	  -email cardcheck@forge.local -project "card check" -out session.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// sessionTTL is how long the minted session lasts.
//
// Fixed here rather than read from FORGE_SESSION_TTL because this program loads
// only the database section of the configuration: it has no business asserting
// anything about how the server it is feeding is configured. A day is longer
// than any card check and short enough that a forgotten token in a scratchpad
// stops working on its own.
const sessionTTL = 24 * time.Hour

type output struct {
	Token     string `json:"token"`
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	ProjectID string `json:"project_id"`
	SessionID string `json:"session_id"`
	ExpiresAt string `json:"expires_at"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "mintsession: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	const op = "mintsession.run"

	email := flag.String("email", "cardcheck@forge.local", "the local test principal's address")
	projectName := flag.String("project", "card check", "the project to own")
	out := flag.String("out", "", "write the session JSON here as well as to stdout")
	flag.Parse()

	ctx := context.Background()
	// Only the database section is required. This program talks to Postgres and
	// to nothing else: demanding a model endpoint or a session secret it will
	// never use would make it fail for reasons that have nothing to do with it.
	cfg, _, err := config.Load(config.SectionDB)
	if err != nil {
		return err
	}
	log := logx.New(logx.Options{Level: slog.LevelWarn, Format: "text", Service: "mintsession"})
	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	clk := clock.System{}
	now := clk.Now()
	repo := identity.NewRepository()
	acc := access.NewService(pool, clk, log)

	// ---- the user ----------------------------------------------------------
	user, err := repo.FindUserByEmail(ctx, pool, *email)
	if err != nil && !errs.Is(err, errs.CodeNotFound) {
		return err
	}
	if user == nil {
		// A throwaway value. It is hashed because forge_users.password_hash is
		// NOT NULL and because a row with a hash of something nobody knows is
		// safer than a row with a hash of something somebody chose. The
		// plaintext is generated, used once and never leaves this scope.
		throwaway, err := auth.NewToken()
		if err != nil {
			return err
		}
		hash, err := auth.HashPassword(throwaway.Plaintext)
		if err != nil {
			return err
		}
		user = &identity.User{
			ID:                id.New(id.PrefixUser),
			Email:             *email,
			DisplayName:       "Card check",
			Status:            identity.StatusActive,
			PasswordHash:      hash,
			PasswordAlgo:      auth.AlgoArgon2id,
			PasswordChangedAt: now,
			CreatedAt:         now,
		}
		if err := repo.CreateUser(ctx, pool, user); err != nil {
			return err
		}
	}
	// Idempotent by its own guard: re-running must not move the timestamp.
	if err := repo.MarkEmailVerified(ctx, pool, user.ID, now); err != nil {
		return err
	}

	// ---- the project -------------------------------------------------------
	//
	// Written the way httpapi's newProject fixture writes one: the row AND the
	// owner membership, because authorisation reads forge_project_members and a
	// project with only the row is one its own creator cannot see.
	var projectID string
	err = pool.QueryRow(ctx,
		`select id from forge_projects where owner_id = $1 and name = $2 and archived_at is null
		  order by created_at limit 1`, user.ID, *projectName).Scan(&projectID)
	if err != nil {
		projectID = id.New(id.PrefixProject)
		if _, err := pool.Exec(ctx, `
			insert into forge_projects (id, owner_id, name, created_at, updated_at)
			values ($1,$2,$3,$4,$4)`, projectID, user.ID, *projectName, now); err != nil {
			return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
		}
	}
	if err := acc.EnsureOwner(ctx, pool, projectID, user.ID); err != nil {
		return err
	}

	// ---- the session -------------------------------------------------------
	//
	// Exactly identity.Service.SignIn's session, minus the password check: a
	// fresh token, only its hash stored, both timestamps from the application
	// clock (Session.Live compares created_at against password_changed_at, and
	// a database now() on one side of that is the bug CreateSession documents).
	token, err := auth.NewToken()
	if err != nil {
		return err
	}
	session := &identity.Session{
		ID:        id.New(id.PrefixSession),
		UserID:    user.ID,
		TokenHash: token.Hash,
		CreatedAt: now,
		ExpiresAt: now.Add(sessionTTL),
		UserAgent: "card-check harness (docs/spikes/2026-09-15-card-checked)",
	}
	if err := repo.CreateSession(ctx, pool, session); err != nil {
		return err
	}

	body, err := json.MarshalIndent(output{
		Token:     token.Plaintext,
		UserID:    user.ID,
		Email:     user.Email,
		ProjectID: projectID,
		SessionID: session.ID,
		ExpiresAt: session.ExpiresAt.Format(time.RFC3339),
	}, "", "  ")
	if err != nil {
		return err
	}
	if *out != "" {
		// 0600: it is a live credential for as long as the session lasts.
		if err := os.WriteFile(*out, body, 0o600); err != nil {
			return err
		}
	}
	fmt.Println(string(body))
	return nil
}
