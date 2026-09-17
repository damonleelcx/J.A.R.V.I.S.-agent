//go:build ignore

// principals makes the three people this spike drives forged as — an owner, a viewer of
// the owner's project and a stranger — and hands back a session for each, then stores
// the designs the export runs need. No password is typed or known by anyone.
//
// It is #119's mintsession (docs/spikes/2026-09-15-card-checked/harness/mintsession)
// widened to three principals: identity.Repository.CreateUser over a hashed throwaway,
// MarkEmailVerified, access.Service.EnsureOwner and SetRole for the membership, and a
// session row built exactly as identity.Service.SignIn builds one. Designs are stored
// with geometry.Service.Save, the one call /v1/converse makes, because FORGE has no
// endpoint that stores geometry a client sends.
//
// The output holds live session tokens: it is written 0600 to the scratchpad and never
// committed.
//
//	FORGE_DATABASE_URL=...&search_path=forge_unverified go run principals/main.go \
//	    -out principals.json -design barrel-8192.json -design barrel-30400.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/auth"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/identity"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

type designs []string

func (d *designs) String() string     { return strings.Join(*d, ",") }
func (d *designs) Set(s string) error { *d = append(*d, s); return nil }

type person struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Token  string `json:"token"`
}

func main() {
	var files designs
	out := flag.String("out", "principals.json", "where the sessions and stored designs are written (0600)")
	flag.Var(&files, "design", "a geometry document to store in the owner's project (repeatable)")
	flag.Parse()
	if err := run(*out, files); err != nil {
		fmt.Fprintln(os.Stderr, "principals:", err)
		os.Exit(1)
	}
}

func run(out string, files []string) error {
	ctx := context.Background()
	cfg, _, err := config.Load(config.SectionDB)
	if err != nil {
		return err
	}
	log := logx.New(logx.Options{Level: slog.LevelWarn, Format: "text", Service: "principals"})
	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer pool.Close()
	geometry.ConfigureLimits(cfg.Geometry)

	clk := clock.System{}
	repo := identity.NewRepository()
	acc := access.NewService(pool, clk, log)

	mk := func(email, name string) (person, error) {
		now := clk.Now()
		throwaway, err := auth.NewToken()
		if err != nil {
			return person{}, err
		}
		hash, err := auth.HashPassword(throwaway.Plaintext)
		if err != nil {
			return person{}, err
		}
		u := &identity.User{ID: id.New(id.PrefixUser), Email: email, DisplayName: name,
			Status: identity.StatusActive, PasswordHash: hash, PasswordAlgo: auth.AlgoArgon2id,
			PasswordChangedAt: now, CreatedAt: now}
		if err := repo.CreateUser(ctx, pool, u); err != nil {
			return person{}, err
		}
		if err := repo.MarkEmailVerified(ctx, pool, u.ID, now); err != nil {
			return person{}, err
		}
		token, err := auth.NewToken()
		if err != nil {
			return person{}, err
		}
		if err := repo.CreateSession(ctx, pool, &identity.Session{ID: id.New(id.PrefixSession), UserID: u.ID,
			TokenHash: token.Hash, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour),
			UserAgent: "unverified-paths harness (docs/spikes/2026-09-17-unverified-paths)"}); err != nil {
			return person{}, err
		}
		return person{UserID: u.ID, Email: email, Token: token.Plaintext}, nil
	}

	stamp := time.Now().UTC().Format("150405")
	owner, err := mk("owner-"+stamp+"@unverified.local", "Owner")
	if err != nil {
		return err
	}
	viewer, err := mk("viewer-"+stamp+"@unverified.local", "Viewer")
	if err != nil {
		return err
	}
	stranger, err := mk("stranger-"+stamp+"@unverified.local", "Stranger")
	if err != nil {
		return err
	}

	now := clk.Now()
	projectID := id.New(id.PrefixProject)
	if _, err := pool.Exec(ctx, `insert into forge_projects (id, owner_id, name, created_at, updated_at)
		values ($1,$2,$3,$4,$4)`, projectID, owner.UserID, "unverified paths "+stamp, now); err != nil {
		return err
	}
	if err := acc.EnsureOwner(ctx, pool, projectID, owner.UserID); err != nil {
		return err
	}
	if err := acc.SetRole(ctx, access.Grant{ProjectID: projectID, UserID: viewer.UserID,
		Role: access.RoleViewer, By: owner.UserID}); err != nil {
		return err
	}

	stored := map[string]string{}
	svc := geometry.NewService(pool, clk, log)
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		var doc geometry.Document
		if err := json.Unmarshal(body, &doc); err != nil {
			return err
		}
		v, err := svc.Save(ctx, geometry.NewVariant{ProjectID: projectID, InitiatorID: owner.UserID,
			Agent: workspace.AgentHuman, Generator: "barrel.go (#125), unverified-paths export runs",
			Document: doc, Inputs: map[string]any{"design": filepath.Base(f)}})
		if err != nil {
			return fmt.Errorf("storing %s: %w", f, err)
		}
		stored[filepath.Base(f)] = v.VersionID
	}

	body, _ := json.MarshalIndent(map[string]any{"project_id": projectID, "owner": owner,
		"viewer": viewer, "stranger": stranger, "designs": stored}, "", "  ")
	if err := os.WriteFile(out, body, 0o600); err != nil {
		return err
	}
	fmt.Printf("project %s; owner %s, viewer %s, stranger %s; %d designs stored\n",
		projectID, owner.UserID, viewer.UserID, stranger.UserID, len(stored))
	return nil
}
