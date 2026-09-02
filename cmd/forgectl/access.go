package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/access"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/collab"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// cmdAccessMatrix prints the permission grid.
//
// No database: it describes the build. The grid is the thing people audit — "who
// can decide approvals" should be one line to scan, not four lookups — so it is
// printed by permission rather than by role.
func cmdAccessMatrix() error {
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PERMISSION\tOWNER\tMAINTAINER\tCONTRIBUTOR\tVIEWER")
	for _, row := range access.Matrix() {
		held := map[access.Role]bool{}
		for _, r := range row.Roles {
			held[r] = true
		}
		mark := func(r access.Role) string {
			if held[r] {
				return "yes"
			}
			return "—"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", row.Permission,
			mark(access.RoleOwner), mark(access.RoleMaintainer),
			mark(access.RoleContributor), mark(access.RoleViewer))
	}
	fmt.Fprintln(w)
	for _, d := range access.Roles() {
		fmt.Fprintf(w, "%s\t%s\t\t\t\n", d.Role, d.Gloss)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	fmt.Println("\nMembership decides access. forge_projects.owner_id records who CREATED a project")
	fmt.Println("and is not consulted — two authorisation paths means two answers to the same question.")
	return nil
}

func accessFor(ctx context.Context, cfg *config.Config, log *logx.Logger) (*access.Service, *db.Pool, error) {
	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return nil, nil, err
	}
	return access.NewService(pool, clock.System{}, log), pool, nil
}

// cmdAccessMembers lists who is in a project.
func cmdAccessMembers(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdAccessMembers"

	fs := newFlagSet("access members")
	project := fs.String("project", "", "project id (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl access members --project <id>")
	}
	svc, pool, err := accessFor(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	members, err := svc.Members(ctx, *project)
	if err != nil {
		return err
	}
	if len(members) == 0 {
		fmt.Println("this project has no members, so nobody can administer it")
		return nil
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "USER\tROLE\tGRANTED BY\tSINCE")
	for _, m := range members {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.UserID, m.Role, m.GrantedBy, m.GrantedAt)
	}
	return w.Flush()
}

// cmdAccessGrant adds somebody or changes their role.
func cmdAccessGrant(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdAccessGrant"

	fs := newFlagSet("access grant")
	project := fs.String("project", "", "project id (required)")
	user := fs.String("user", "", "the user id being given the role (required)")
	role := fs.String("role", "", "owner|maintainer|contributor|viewer (required)")
	as := fs.String("as", "", "the user id making the grant (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" || *user == "" || *role == "" || *as == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl access grant --project <id> --user <id> --role <role> --as <user-id>\n"+
				"The roles are: %s", strings.Join(roleNames(), ", "))
	}
	svc, pool, err := accessFor(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := svc.SetRole(ctx, access.Grant{
		ProjectID: *project, UserID: *user, Role: access.Role(*role), By: *as}); err != nil {
		return err
	}
	fmt.Printf("%s is now a %s in %s\n", *user, *role, *project)
	return nil
}

// cmdAccessRevoke removes somebody from a project.
func cmdAccessRevoke(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdAccessRevoke"

	fs := newFlagSet("access revoke")
	project := fs.String("project", "", "project id (required)")
	user := fs.String("user", "", "the user id being removed (required)")
	as := fs.String("as", "", "the user id doing it (required)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *project == "" || *user == "" || *as == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl access revoke --project <id> --user <id> --as <user-id>")
	}
	svc, pool, err := accessFor(ctx, cfg, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	if err := svc.Remove(ctx, *project, *user, *as); err != nil {
		return err
	}
	fmt.Printf("%s no longer has access to %s\n", *user, *project)
	return nil
}

func roleNames() []string {
	out := make([]string, 0, 4)
	for _, d := range access.Roles() {
		out = append(out, string(d.Role))
	}
	return out
}

// cmdHandoff prints the document somebody picking up the work needs.
func cmdHandoff(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdHandoff"

	if len(args) != 1 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl handoff <goal-id>")
	}
	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	doc, err := collab.NewService(pool, clock.System{}, log).TakeHandoff(ctx, args[0])
	if err != nil {
		return err
	}
	fmt.Print(doc.Render())
	return nil
}

// cmdRoomShow prints a room's transcript, with every turn attributed.
func cmdRoomShow(ctx context.Context, cfg *config.Config, log *logx.Logger, args []string) error {
	const op = "forgectl.cmdRoomShow"

	if len(args) != 1 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("usage: forgectl rooms show <room-id>")
	}
	pool, err := db.Connect(ctx, cfg.DB, log)
	if err != nil {
		return err
	}
	defer pool.Close()

	room, err := collab.NewService(pool, clock.System{}, log).Find(ctx, args[0])
	if err != nil {
		return err
	}
	fmt.Printf("%s  %s  (%s)\nOpened %s by %s\n\n",
		room.ID, room.Title, room.Status, room.OpenedAt.UTC().Format("2006-01-02 15:04"), room.OpenedBy)

	fmt.Println("PRESENT")
	for _, p := range room.Participants {
		until := "still in the room"
		if p.LeftAt != nil {
			until = "left " + p.LeftAt.UTC().Format("15:04")
		}
		fmt.Printf("  %s  joined %s, %s\n", p.UserID, p.JoinedAt.UTC().Format("15:04"), until)
	}
	if len(room.Turns) > 0 {
		fmt.Println("\nTRANSCRIPT")
		for _, t := range room.Turns {
			who := t.SpeakerLabel
			if t.Speaker == collab.SpeakerForge {
				// PRD AUD-05: FORGE always identifies itself as AI, in the
				// transcript as much as in the room.
				who = "FORGE (AI)"
			}
			fmt.Printf("  %3d  %-14s %-6s %s\n", t.Seq, who, t.Channel, t.Text)
		}
	}
	if len(room.ApprovalIDs) > 0 {
		fmt.Println("\nAPPROVALS DECIDED IN THIS ROOM")
		for _, a := range room.ApprovalIDs {
			fmt.Printf("  %s\n", a)
		}
	}
	fmt.Println("\nNo audio is recorded or transported: this is the durable record, and a transport")
	fmt.Println("writes turns into it. See docs/implementation-plan.md, wave 6.")
	return nil
}
