package engine

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The fence AGT-04 did not have (issue 21).
//
// # What was missing
//
// "Progressive autonomy; never silently raises its own autonomy level" held
// because autonomy was write-once BY ABSENCE: it was set at goal creation and no
// code path changed it. The three tests over the ladder —
// TestAutonomyLadderIsOrdered, TestProhibitedIsNotALowAutonomyLevel,
// TestSchemaAndCodeAgreeOnAutonomyAndRisk — hold ladder SEMANTICS, and all three
// would have stayed green on the day somebody added an endpoint that raised a
// running goal's autonomy. The headline guarantee rested on a comment.
//
// # What this holds, and why it is a table
//
// Every place in the tree that WRITES the autonomy column, or the review
// authority that raises a risk ceiling, is declared below with what keeps it
// honest. The fence then finds the write sites for itself and compares the two
// lists in both directions, so:
//
//   - a new writer nobody declared fails here BY NAME, with its file and
//     function, rather than shipping;
//   - a declared entry whose code has gone fails too, so the table cannot
//     quietly become a list of things that used to exist.
//
// The same shape internal/domain/workspace/model.go uses for its legal
// transitions, and for the same reason: a rule that can be READ is a rule
// somebody can say is wrong.

// writeSite is one declared writer of a guarded column.
type writeSite struct {
	// File is a path relative to the repository root, with forward slashes.
	File string
	// Func is the enclosing function or method name.
	Func string
	// Insert is true for a creation — the one shape a raise cannot take, because
	// there is no earlier level for the row to be raised FROM.
	Insert bool
	// Why is what makes this writer safe. Read it before adding a neighbour.
	Why string
}

// autonomyWriters is every place a goal's autonomy is written.
//
// All of them are INSERTs. That is the property: a level chosen when the goal is
// created, in front of the person choosing it, and never moved afterwards.
var autonomyWriters = []writeSite{
	{
		File: "internal/agent/intake.go", Func: "Draft", Insert: true,
		Why: "goal creation. The level comes from the request and is validated against the " +
			"ladder; drafting authorises nothing and starting is the separate act (AGT-02).",
	},
	{
		File: "internal/agent/stepexport.go", Func: "create", Insert: true,
		Why: "creates the goal that carries an off-node export. Written as a constant " +
			"engine.AutonomyDraft, not from any request: an export produces a file and " +
			"changes nothing in the design.",
	},
}

// authorityWriters is every place the qualified-review claim is written.
//
// It raises a project's risk CEILING rather than a goal's autonomy, and it is
// the other half of the same guarantee: a widening that has to be attributed and
// has to expire.
var authorityWriters = []writeSite{
	{
		File: "internal/domain/workspace/service.go", Func: "RecordReviewAuthority",
		Why: "the single writer. Refuses an unattributed claim, refuses a domain with no raised " +
			"ceiling to reach, and writes an expiry that ReviewAuthorityFor enforces on every " +
			"read (AGT-03, AGT-07).",
	},
}

var (
	writesGoals    = regexp.MustCompile(`(?is)(insert\s+into\s+forge_goals|update\s+forge_goals)`)
	insertsGoals   = regexp.MustCompile(`(?is)insert\s+into\s+forge_goals`)
	writesProjects = regexp.MustCompile(`(?is)update\s+forge_projects`)
	mentionsAuto   = regexp.MustCompile(`(?i)\bautonomy\b`)
	mentionsAuthor = regexp.MustCompile(`(?i)review_authority_`)
)

// found is a write site the scan actually located.
type found struct {
	site writeSite
	pos  string
}

func TestAutonomyIsWriteOnceAndEveryWriterIsDeclared(t *testing.T) {
	root := filepath.Join("..", "..", "..")

	var autonomyFound, authorityFound []found
	var scanned int

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "spikes", "testdata":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		scanned++
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			// Not fatal: a file this fence cannot parse is one the compiler will
			// complain about far more clearly.
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				value, uerr := strconv.Unquote(lit.Value)
				if uerr != nil {
					value = lit.Value
				}
				at := rel + ":" + strconv.Itoa(fset.Position(lit.Pos()).Line)
				if writesGoals.MatchString(value) && mentionsAuto.MatchString(value) {
					autonomyFound = append(autonomyFound, found{
						site: writeSite{File: rel, Func: fn.Name.Name,
							Insert: insertsGoals.MatchString(value)},
						pos: at,
					})
				}
				if writesProjects.MatchString(value) && mentionsAuthor.MatchString(value) {
					authorityFound = append(authorityFound, found{
						site: writeSite{File: rel, Func: fn.Name.Name},
						pos:  at,
					})
				}
				return true
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Without this the whole fence passes on a walk that found nothing, which is
	// how a check over "every file" goes quietly vacuous.
	if scanned < 100 {
		t.Fatalf("only %d source files were scanned; this fence would pass vacuously", scanned)
	}

	t.Run("autonomy", func(t *testing.T) {
		checkDeclared(t, "autonomy", autonomyWriters, autonomyFound)
		// The property itself, not just the bookkeeping: every writer is a
		// creation. An UPDATE that touches the column is the mutation path AGT-04
		// says must not exist, and it fails here whether or not somebody
		// remembered to declare it.
		for _, f := range autonomyFound {
			if !f.site.Insert {
				t.Errorf("%s (%s) UPDATES a goal's autonomy.\n"+
					"PRD AGT-04: autonomy is fixed when a goal is created and nothing raises it "+
					"afterwards. If this is a deliberate lowering, it still needs a human "+
					"pressing something and an audit event — see "+
					"internal/domain/engine/autonomy_write.go, and note the database refuses a "+
					"raise regardless (0028_autonomy_is_write_once.sql).", f.pos, f.site.Func)
			}
		}
	})

	t.Run("review authority", func(t *testing.T) {
		checkDeclared(t, "the review authority", authorityWriters, authorityFound)
	})
}

// checkDeclared compares the declared table against what the scan found, both
// ways round.
func checkDeclared(t *testing.T, what string, declared []writeSite, got []found) {
	t.Helper()

	seen := map[string]bool{}
	for _, f := range got {
		key := f.site.File + "#" + f.site.Func
		seen[key] = true
		var known bool
		for _, d := range declared {
			if d.File == f.site.File && d.Func == f.site.Func {
				known = true
				break
			}
		}
		if !known {
			t.Errorf("%s (%s) writes %s and is not in the table in this file.\n"+
				"That table is the only written record of what keeps each of these honest "+
				"(PRD AGT-04, AGT-03). Add it with the reason it is safe, or do not write the "+
				"column.", f.pos, f.site.Func, what)
		}
	}
	for _, d := range declared {
		if !seen[d.File+"#"+d.Func] {
			t.Errorf("the table declares %s#%s as a writer of %s and the scan found no such "+
				"write.\nA table that outlives its code stops being a description and becomes "+
				"a claim.", d.File, d.Func, what)
		}
	}
}

// The trigger and RaisesAutonomy answer the same question the same way.
//
// 0028 has to spell the ladder out in plpgsql because a trigger cannot read Go.
// Two copies of one rule drift, and the drift here is the worst kind: the
// database would refuse a move the code thought fine, or permit one the code
// thought a raise, and only one of those is noisy.
func TestSchemaAndCodeAgreeOnWhatARaiseIs(t *testing.T) {
	sql := migrationSQL(t, "autonomy_is_write_once")

	ranks := map[Autonomy]int{}
	for _, m := range regexp.MustCompile(`when '([a-z_]+)' then (\d+)`).FindAllStringSubmatch(sql, -1) {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			t.Fatal(err)
		}
		ranks[Autonomy(m[1])] = n
	}
	if len(ranks) == 0 {
		t.Fatal("0028 no longer spells its ladder as `when '<level>' then <n>`; this fence reads " +
			"that shape to hold the trigger against engine.RaisesAutonomy")
	}
	// Prohibited is deliberately absent from the trigger's CASE — it is off the
	// ladder, handled before the ranking — so it must be absent here too.
	if _, ok := ranks[AutonomyProhibited]; ok {
		t.Error("0028 gives 'prohibited' an ordinal inside the ladder. It is a refusal, not a " +
			"low setting, and an ordinal is exactly how an off-by-one enables it")
	}

	// What the trigger does, restated in Go from what was parsed out of it.
	triggerRaises := func(from, to Autonomy) bool {
		if from == to || to == AutonomyProhibited {
			return false
		}
		if from == AutonomyProhibited {
			return true
		}
		rf, okF := ranks[from]
		rt, okT := ranks[to]
		return !okF || !okT || rt > rf
	}

	levels := append(AllAutonomyLevels(), Autonomy("something_new"))
	for _, from := range levels {
		for _, to := range levels {
			if got, want := RaisesAutonomy(from, to), triggerRaises(from, to); got != want {
				t.Errorf("%s → %s: RaisesAutonomy says %v, the 0028 trigger says %v",
					from, to, got, want)
			}
		}
	}
}

// Prohibited is off the ladder in both directions.
//
// The single easiest thing to get wrong here, and the quietest: deriving "is
// this a raise" from autonomyRank alone makes moving OFF prohibited look like a
// move from -1, which every comparison then treats as an ordinary step up — a
// refusal turning into a permission by arithmetic.
func TestRaisesAutonomy_ProhibitedIsARefusalNotALevel(t *testing.T) {
	for _, to := range AllAutonomyLevels() {
		if to == AutonomyProhibited {
			continue
		}
		if !RaisesAutonomy(AutonomyProhibited, to) {
			t.Errorf("moving from prohibited to %s is not counted as a raise. It is the largest "+
				"one there is: a refusal becoming a permission", to)
		}
		if RaisesAutonomy(to, AutonomyProhibited) {
			t.Errorf("moving from %s to prohibited is counted as a raise; stopping work is never "+
				"a raise", to)
		}
	}
	if RaisesAutonomy(AutonomyProhibited, AutonomyProhibited) {
		t.Error("prohibited → prohibited is counted as a raise")
	}
}

// An unrecognised level is never the safe side of the comparison.
func TestRaisesAutonomy_AnUnknownLevelIsTreatedAsARaise(t *testing.T) {
	unknown := Autonomy("full_autonomy")
	if !RaisesAutonomy(AutonomyDiscuss, unknown) {
		t.Error("a level this build does not recognise is accepted as not-a-raise. An unknown " +
			"value must permit nothing, the same rule Role.Allows follows")
	}
	if !RaisesAutonomy(unknown, AutonomyDiscuss) {
		t.Error("a goal stored at a level this build does not recognise can be moved anywhere " +
			"without it counting as a raise")
	}
}
