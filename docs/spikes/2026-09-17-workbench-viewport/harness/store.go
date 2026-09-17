//go:build ignore

// Stores a generated design into an EXISTING project, and optionally grants a second
// principal the viewer role on it — for the workbench check in README.md beside this
// directory. docs/spikes/2026-09-15-subtree-loading/store.go explains why this is
// geometry.Service.Save and not an HTTP call; this copy adds -project (so the owner and
// the viewer look at one project) and -viewer (access.Service.SetRole, granted by the
// owner through the same check a person's grant passes).
//
//	. env.sh && go run docs/spikes/2026-09-17-workbench-viewport/harness/store.go \
//	    -user usr_... -project prj_... -design car30k.json [-viewer usr_...]
//
// FORGE_GEOMETRY_MAX_OCCURRENCES is read as forged reads it, so a 1,000,000-part design
// is stored only where that storage limit was raised for the check.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/workspace"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

func main() {
	user := flag.String("user", "", "the owner proposing it")
	project := flag.String("project", "", "the owner's project")
	design := flag.String("design", "", "a geometry document as JSON (omit to only grant -viewer)")
	viewer := flag.String("viewer", "", "a user to make a viewer of the project")
	generator := flag.String("generator", "scripts/viewport-car.js carDocument, workbench check 2026-09-17", "what produced it")
	flag.Parse()
	if *user == "" || *project == "" {
		fmt.Fprintln(os.Stderr, "usage: store.go -user <id> -project <id> [-design <json>] [-viewer <id>]")
		os.Exit(2)
	}
	cfg, _, err := config.Load(config.SectionDB)
	if err != nil {
		fail(err)
	}
	ctx := context.Background()
	log := logx.New(logx.Options{Format: "text", Service: "vpcheck-store"})
	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		fail(err)
	}
	defer pool.Close()
	if *viewer != "" {
		if err := access.NewService(pool, clock.System{}, log).SetRole(ctx, access.Grant{
			ProjectID: *project, UserID: *viewer, Role: access.RoleViewer, By: *user}); err != nil {
			fail(err)
		}
		fmt.Printf("{\"viewer\":%q,\"project_id\":%q}\n", *viewer, *project)
	}
	if *design == "" {
		return
	}
	body, err := os.ReadFile(*design)
	if err != nil {
		fail(err)
	}
	var doc geometry.Document
	if err := json.Unmarshal(body, &doc); err != nil {
		fail(err)
	}
	full, _, err := config.Load(config.SectionDB)
	if err != nil {
		fail(err)
	}
	geometry.ConfigureLimits(full.Geometry)
	v, err := geometry.NewService(pool, clock.System{}, log).Save(ctx, geometry.NewVariant{
		InitiatorID: *user, ProjectID: *project, Agent: workspace.AgentHuman,
		Generator: *generator, Document: doc, Inputs: map[string]any{"design": *design},
	})
	if err != nil {
		fail(err)
	}
	fmt.Printf("{\"version_id\":%q,\"project_id\":%q,\"occurrences\":%d}\n", v.VersionID, v.ProjectID, len(doc.Expanded().Parts))
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
