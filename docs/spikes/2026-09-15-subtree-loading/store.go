//go:build ignore

// Stores a generated design in a local FORGE database, for the Phase 6, stage W2
// acceptance run recorded in README.md beside this file.
//
// # Why this is not an HTTP call
//
// FORGE has no endpoint that stores geometry a client sends, on purpose: geometry is
// written by /v1/converse at the moment a model produces it, and "a client posting
// geometry would be naming its own generator" (internal/httpapi/router.go). A measured
// 30,000-part design cannot come out of a conversation reliably, so this makes the one
// call /v1/converse makes — geometry.Service.Save — with the person who signed up over
// the API as its initiator and this generator named as its generator. Everything after
// it (signing in, listing, opening the workbench, every mesh) goes over HTTP.
//
//	FORGE_DATABASE_URL=postgres://... go run docs/spikes/2026-09-15-subtree-loading/store.go \
//	    -user usr_... -design car30k.json
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

func main() {
	user := flag.String("user", "", "the id of the user proposing it (from POST /v1/auth/sign-up)")
	design := flag.String("design", "", "a geometry document as JSON")
	generator := flag.String("generator", "scripts/viewport-car.js carDocument(30000), W2 acceptance run", "what produced it")
	flag.Parse()
	if *user == "" || *design == "" {
		fmt.Fprintln(os.Stderr, "usage: store.go -user <user id> -design <document.json>")
		os.Exit(2)
	}
	body, err := os.ReadFile(*design)
	if err != nil {
		fail(err)
	}
	var doc geometry.Document
	if err := json.Unmarshal(body, &doc); err != nil {
		fail(err)
	}
	cfg, _, err := config.Load(config.SectionDB)
	if err != nil {
		fail(err)
	}
	ctx := context.Background()
	log := logx.New(logx.Options{Format: "text", Service: "w2-store"})
	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		fail(err)
	}
	defer pool.Close()
	geometry.ConfigureLimits(cfg.Geometry)
	v, err := geometry.NewService(pool, clock.System{}, log).Save(ctx, geometry.NewVariant{
		InitiatorID: *user,
		Agent:       workspace.AgentHuman,
		Generator:   *generator,
		Document:    doc,
		Inputs:      map[string]any{"design": *design},
	})
	if err != nil {
		fail(err)
	}
	fmt.Printf("{\"version_id\":%q,\"project_id\":%q}\n", v.VersionID, v.ProjectID)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
