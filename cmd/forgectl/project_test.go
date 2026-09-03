package main

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
)

// The values this command accepts are the values the database allows.
//
// # Why this fence exists
//
// project.go lists the legal values so it can reject a typo with a helpful
// message instead of a constraint violation. That list is a second copy of the
// CHECK constraints in 0014_project_character.sql, and a second copy drifts: a
// value added to the schema and not here is rejected by the CLI although it is
// legal, and one removed from the schema and left here is promised and then
// refused by the database.
//
// Read out of the migration rather than restated, so the test cannot agree with
// a mistake by repeating it.
func TestProjectCharacterValuesMatchTheSchema(t *testing.T) {
	migrations, err := db.LoadMigrations(db.Files, db.MigrationsDir)
	if err != nil {
		t.Fatal(err)
	}
	var sql string
	for _, m := range migrations {
		if strings.Contains(m.Name, "project_character") {
			sql = m.SQL
		}
	}
	if sql == "" {
		t.Fatal("no project_character migration found; this fence would pass vacuously")
	}

	for _, tc := range []struct {
		column string
		want   []string
	}{
		{"critique_intensity", critiqueValues},
		{"verbosity", verbosityValues},
	} {
		t.Run(tc.column, func(t *testing.T) {
			// The constraint reads: check (<column> in ('a', 'b', 'c'))
			marker := "check (" + tc.column + " in ("
			at := strings.Index(sql, marker)
			if at < 0 {
				t.Fatalf("no CHECK constraint for %s in the migration; either it lost its "+
					"constraint — in which case a typo now reaches the system prompt — or this "+
					"fence is looking for the wrong shape", tc.column)
			}
			rest := sql[at+len(marker):]
			list := rest[:strings.Index(rest, ")")]

			for _, v := range tc.want {
				if !strings.Contains(list, "'"+v+"'") {
					t.Errorf("the CLI offers %s=%q and the database does not allow it: %s",
						tc.column, v, strings.TrimSpace(list))
				}
			}
			// And the reverse: a value the schema allows that the CLI cannot set is
			// a setting nobody can reach through the only producer there is.
			for _, field := range strings.Split(list, ",") {
				v := strings.Trim(strings.TrimSpace(field), "'")
				if v != "" && !oneOf(v, tc.want) {
					t.Errorf("the database allows %s=%q and the CLI will not set it", tc.column, v)
				}
			}
		})
	}
}
