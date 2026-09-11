package cad_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// scriptKernel is a kernel with scripts turned on, skipped when there is no
// Python to run them.
func scriptKernel(t *testing.T) *cad.Kernel {
	t.Helper()
	py := os.Getenv("FORGE_CAD_KERNEL")
	if py == "" {
		for _, c := range []string{".cadvenv/bin/python", "../../../.cadvenv/bin/python"} {
			if _, err := os.Stat(c); err == nil {
				py = c
				break
			}
		}
	}
	if py == "" {
		if p, err := exec.LookPath("python3"); err == nil {
			py = p
		}
	}
	if py == "" {
		t.Skip("no python to run scripts with")
	}
	return cad.New(py, logx.Discard()).WithScripts(true)
}

// The sandbox refuses what it says it refuses.
//
// # Why each of these is here
//
// This is the one feature in the repository that executes text a model wrote.
// The AST whitelist is the layer that has to hold — the process limits are what
// stands if it does not — so every escape it names is tested by trying it.
//
// A restricted-builtins sandbox ALONE is not a sandbox in CPython:
// ().__class__.__bases__[0].__subclasses__() reaches every loaded class in one
// expression. That is why dunder attributes are refused as a NAME rather than by
// blacklisting the escapes, which are not enumerable — and why the first case
// below is that exact expression.
func TestScript_RefusesTheWayOut(t *testing.T) {
	k := scriptKernel(t)
	cases := []struct {
		name, source, want string
	}{
		{"the documented escape from a restricted namespace",
			"result = ().__class__.__bases__[0].__subclasses__()", "__"},
		{"importing anything at all",
			"import os\nresult = os.environ", "not allowed"},
		{"importing from",
			"from os import environ\nresult = environ", "not allowed"},
		{"reading a file",
			"result = open('/etc/passwd').read()", "not available"},
		{"reaching the network",
			"result = urlopen('http://example.com').read()", "not available"},
		{"a builtin that is not on the list",
			"result = getattr(1, 'real')", "not available"},
		{"a dunder name",
			"result = __builtins__", "__"},
		{"defining a class to get at its bases",
			"class X: pass\nresult = X", "not allowed"},
		{"catching the refusal to hide it",
			"try:\n    import os\nexcept Exception:\n    pass\nresult = 1", "not allowed"},
		// Lambda was allowed on 2026-09-09. These three are the price of that:
		// the escape must still be refused when it is spelled inside one, or the
		// widening moved a rule instead of removing a redundant one. The dunder
		// check walks every node and does not care where it is, and these say so
		// by trying it rather than by asserting that it does.
		{"the documented escape, inside a lambda",
			"f = lambda: ().__class__.__bases__[0].__subclasses__()\nresult = f()", "__"},
		{"an unavailable builtin, inside a lambda",
			"f = lambda p: open(p).read()\nresult = f('/etc/passwd')", "not available"},
		{"a lambda used to reach a dunder attribute of something given",
			"f = lambda x: x.__class__\nresult = f(Box(1, 1, 1))", "__"},
		// `@` was allowed on 2026-09-10. The price: the escape must still be
		// refused when spelled through it, and the explicit dunder call it
		// dispatches to must stay refused — or the widening moved a rule rather
		// than removing a redundant one.
		{"the documented escape, reached through an @ expression",
			"e = Line((0, 0), (10, 0))\nresult = (e @ 0.5).__class__.__bases__[0].__subclasses__()", "__"},
		{"calling the dunder that @ dispatches to, by name",
			"e = Line((0, 0), (10, 0))\nresult = e.__matmul__(0.5)", "__"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := k.RunScript(context.Background(), tc.source, nil)
			if err == nil {
				t.Fatalf("this ran:\n%s\nEverything else this sandbox promises rests on it "+
					"not running", tc.source)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refused for the wrong reason.\nwanted the message to mention %q\ngot: %v",
					tc.want, err)
			}
		})
	}
}

// The environment is not there to be read, even if the AST check were wrong.
//
// Layer 2, tested on its own: a script cannot name os, but this asserts the
// thing that stands if one day it can. This process holds a database URL and a
// provider key.
func TestScript_TheChildHasNoEnvironment(t *testing.T) {
	k := scriptKernel(t)
	t.Setenv("FORGE_DATABASE_URL", "postgres://secret:secret@example/db")
	// print() is allowed, so a script CAN write to stdout; it must find nothing
	// to write. Reaching os is refused by layer 1, so this asserts layer 2 by
	// checking the runner's own view: any leak would appear in the error text.
	_, err := k.RunScript(context.Background(), "result = 1", nil)
	if err != nil && strings.Contains(err.Error(), "secret") {
		t.Fatalf("the child could see this process's environment: %v", err)
	}
}

// Scripts are OFF unless a deployment turns them on.
func TestScript_OffByDefault(t *testing.T) {
	k := cad.New("python3", logx.Discard())
	if k.ScriptsEnabled() {
		t.Fatal("scripts are on by default. This is the one feature that runs text a model " +
			"wrote and it must be a decision somebody made")
	}
	if _, err := k.RunScript(context.Background(), "result = 1", nil); err == nil {
		t.Fatal("a script ran on a kernel that has scripts disabled")
	}
}

// A script that builds nothing says so, rather than yielding an empty part.
func TestScript_RefusesAShapeWithNoVolume(t *testing.T) {
	k := scriptKernel(t)
	if _, err := k.RunScript(context.Background(), "result = 1", nil); err == nil {
		t.Error("a script that assigned a number produced a part")
	}
	if _, err := k.RunScript(context.Background(), "x = 1", nil); err == nil {
		t.Error("a script that never assigned `result` produced a part")
	}
}

// A script builds what the vocabulary cannot say.
//
// # Why this case
//
// `repeat` covers repetition, which was most of the gap. What it cannot do is
// vary each copy — and an involute gear is exactly that: every tooth flank is a
// different curve computed from the same formula. A gear is the smallest honest
// example of why this path exists at all, and if a script cannot build one then
// the sandbox is expensive and pointless.
func TestScript_BuildsWhatTheVocabularyCannot(t *testing.T) {
	k := scriptKernel(t)
	// A spur gear: twenty teeth, each flank sampled from the involute.
	const gear = `
teeth = 20
module = 3.0
pitch_r = module * teeth / 2.0
base_r = pitch_r * cos(radians(20.0))
outer_r = pitch_r + module
root_r = pitch_r - 1.25 * module

def involute(t):
    return (base_r * (cos(t) + t * sin(t)), base_r * (sin(t) - t * cos(t)))

pts = []
steps = 8
for i in range(steps + 1):
    t = 1.2 * i / steps
    pts.append(involute(t))

with BuildPart() as part:
    with BuildSketch() as sk:
        Circle(root_r)
        with PolarLocations(0.0, teeth):
            with BuildSketch(mode=Mode.PRIVATE):
                pass
        for k in range(teeth):
            a = radians(360.0 * k / teeth)
            poly = [(0.0, 0.0)]
            for (x, y) in pts:
                poly.append((x * cos(a) - y * sin(a), x * sin(a) + y * cos(a)))
            for (x, y) in reversed(pts):
                yy = -y
                poly.append((x * cos(a) - yy * sin(a), x * sin(a) + yy * cos(a)))
            Polygon(*poly, align=None)
    extrude(amount=10.0)

result = part
`
	res, err := k.RunScript(context.Background(), gear, nil)
	if err != nil && strings.Contains(err.Error(), "No module named 'build123d'") {
		// A machine with Python but no CAD kernel — CI is one. Skipped by the
		// EXACT reason rather than by guessing from a failure, so a real
		// breakage can never wear this skip's clothes. Everything that does not
		// need the kernel — the nine refusals, the limits — still runs here.
		t.Skip("build123d is not installed, so a script cannot build anything")
	}
	if err != nil {
		t.Fatalf("a script that a person could write by hand did not run: %v\n"+
			"If build123d's own vocabulary cannot be driven from here, this sandbox costs "+
			"a security boundary and buys nothing", err)
	}
	if res.Volume <= 0 {
		t.Errorf("the gear has no volume")
	}
	if !strings.Contains(res.STEP, "ISO-10303") {
		t.Errorf("what came back is not STEP: %.80s", res.STEP)
	}
	t.Logf("gear volume %.0f mm^3, %d bytes of STEP", res.Volume, len(res.STEP))
}

// A script that never finishes is stopped, and told why.
func TestScript_StopsOneThatWillNotFinish(t *testing.T) {
	k := scriptKernel(t)
	_, err := k.RunScript(context.Background(), "x = 0\nwhile True:\n    x = x + 1\nresult = x", nil)
	if err == nil {
		t.Fatal("an endless loop finished, which means nothing is bounding these")
	}
	if !strings.Contains(err.Error(), "time") && !strings.Contains(err.Error(), "memory") {
		t.Errorf("stopped for an unclear reason: %v", err)
	}
}

// The two imports that grant nothing are tolerated; every other one is not.
//
// # Why tolerate any import at all
//
// Every build123d example opens with `import math` and `from build123d import
// *`, and a model writes what it has read. Measured live on the deployed site,
// 2026-09-09: the first script FORGE produced with this path working began with
// exactly those two lines and was refused outright — a correct gear, rejected
// for two no-ops.
//
// They ARE no-ops: both namespaces are populated before the script runs. So they
// are accepted and dropped rather than executed. Nothing else is tolerated, and
// the cases below are the ones that must stay refused.
func TestScript_TheHarmlessImportsAndNoOthers(t *testing.T) {
	k := scriptKernel(t)

	t.Run("the two that every example opens with", func(t *testing.T) {
		_, err := k.RunScript(context.Background(),
			"import math\nfrom build123d import *\nresult = Box(10, 10, 10)", nil)
		if err != nil && strings.Contains(err.Error(), "not allowed") {
			t.Fatalf("a correct script was refused for two no-op imports: %v", err)
		}
		if err != nil && strings.Contains(err.Error(), "No module named 'build123d'") {
			t.Skip("build123d is not installed")
		}
		if err != nil {
			t.Fatalf("unexpected: %v", err)
		}
	})

	t.Run("math is usable as a module after the tolerated import", func(t *testing.T) {
		_, err := k.RunScript(context.Background(),
			"import math\nresult = Box(10, 10, math.floor(10.7))", nil)
		if err != nil && strings.Contains(err.Error(), "not available") {
			t.Errorf("`import math` was tolerated and then math.floor did not exist. "+
				"Tolerating an import that leaves the name unbound is worse than refusing "+
				"it: the script parses and then dies on the first use.\n%v", err)
		}
	})

	for _, bad := range []string{
		"import os\nresult = 1",
		"import sys, math\nresult = 1",
		"import math as m\nresult = 1",
		"from os import environ\nresult = 1",
		"from subprocess import run\nresult = 1",
		"from . import x\nresult = 1",
	} {
		t.Run("still refused: "+strings.SplitN(bad, "\n", 2)[0], func(t *testing.T) {
			_, err := k.RunScript(context.Background(), bad, nil)
			if err == nil {
				t.Fatalf("this ran:\n%s", bad)
			}
			if !strings.Contains(err.Error(), "not allowed") {
				t.Errorf("refused for the wrong reason: %v", err)
			}
		})
	}
}

// build123d's own builders are available; the modules it re-exports are not.
//
// # The hole this closes, which was nearly opened deliberately
//
// The first version of the whitelist named forty builders by hand. It refused
// `cylinder`, and a real script from the live site died on it — a hand-list is a
// guess about what a model will reach for, and the guess is always short.
//
// The obvious simplification was "allow everything build123d exports". That
// would have been a serious hole: `from build123d import *` re-exports real
// modules, and its public namespace contains **ctypes** — arbitrary memory and
// arbitrary code — along with copy, contextvars and colorsys.
//
// So the rule is build123d's OWN classes and functions (what __module__ says),
// never a module, minus its own file access. This test is the reason to trust
// that sentence: it tries the module by name.
func TestScript_HasTheBuildersButNotTheModules(t *testing.T) {
	k := scriptKernel(t)

	// Refused, and this is the important half.
	for _, name := range []string{"ctypes", "copy", "contextvars", "colorsys"} {
		t.Run("no "+name, func(t *testing.T) {
			_, err := k.RunScript(context.Background(), "result = "+name, nil)
			if err == nil {
				t.Fatalf("a script reached %s — build123d re-exports it, and allowing "+
					"everything build123d exports would hand a script arbitrary memory", name)
			}
			if !strings.Contains(err.Error(), "not available") {
				t.Errorf("refused for the wrong reason: %v", err)
			}
		})
	}

	// And build123d's own file access stays out, by rule rather than by list.
	for _, name := range []string{"import_step", "export_stl", "available_fonts"} {
		t.Run("no "+name, func(t *testing.T) {
			if _, err := k.RunScript(context.Background(), "result = "+name, nil); err == nil {
				t.Fatalf("a script reached %s. A CAD library legitimately touches files; "+
					"a script here must not", name)
			}
		})
	}

	// Available, including the ones the hand-list had missed.
	res, err := k.RunScript(context.Background(),
		"with BuildPart() as p:\n    Cylinder(radius=10, height=5)\nresult = p", nil)
	if err != nil && strings.Contains(err.Error(), "No module named 'build123d'") {
		t.Skip("build123d is not installed")
	}
	if err != nil {
		t.Fatalf("a plain cylinder did not build: %v", err)
	}
	if res.Volume <= 0 {
		t.Error("the cylinder has no volume")
	}
}

// The checked-in manifest still matches the installed library.
//
// # Why the list is a file at all
//
// Deriving it live from build123d means a machine WITHOUT build123d has no
// list — and then either every builder is refused with "Box is not available
// here" (false: Box is fine, the kernel is missing) or the name check is skipped
// and `open` and `urlopen` stop being refused. CI has Python and no kernel and
// hit both in turn, in that order.
//
// A generated, checked-in manifest gives the same answer everywhere, so the
// REFUSALS hold on a machine that cannot build anything. The cost is that it can
// go stale, and this is what stops that: where a kernel does exist, the file must
// still be what the rule produces.
func TestScript_TheManifestMatchesTheLibrary(t *testing.T) {
	py := kernelPython(t)
	const prog = `
import build123d as b, inspect, json
DENY = {"available_fonts","FontManager","brep_from_stl","RWStl","StlAPI_Writer",
        "ExportSVG","export_to_pcbway","svgpathtools"}
def denied(n): return n in DENY or n.startswith("import_") or n.startswith("export_")
out=[]
for n in dir(b):
    if n.startswith("_") or denied(n): continue
    v=getattr(b,n)
    if inspect.ismodule(v): continue
    if not str(getattr(v,"__module__","") or "").startswith("build123d"): continue
    out.append(n)
print(json.dumps(sorted(out)))
`
	got, err := exec.Command(py, "-c", prog).Output()
	if err != nil {
		t.Skip("build123d is not installed, so there is nothing to compare against")
	}
	var live []string
	if err := json.Unmarshal(got, &live); err != nil {
		t.Fatalf("reading the library's names: %v", err)
	}

	raw, err := os.ReadFile("builders.txt")
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		have[line] = true
	}

	var missing []string
	for _, n := range live {
		if !have[n] {
			missing = append(missing, n)
		}
		delete(have, n)
	}
	if len(missing) > 0 {
		t.Errorf("builders.txt is missing %d name(s) the library has, so correct scripts "+
			"will be refused for names that exist: %s", len(missing), strings.Join(missing, ", "))
	}
	if len(have) > 0 {
		var extra []string
		for n := range have {
			extra = append(extra, n)
		}
		sort.Strings(extra)
		t.Errorf("builders.txt allows %d name(s) the rule would NOT: %s. This is the "+
			"direction that matters — the rule is what keeps ctypes out",
			len(extra), strings.Join(extra, ", "))
	}
}

// kernelPython is the interpreter, or a skip.
func kernelPython(t *testing.T) string {
	t.Helper()
	if py := os.Getenv("FORGE_CAD_KERNEL"); py != "" {
		return py
	}
	for _, c := range []string{"../../../.cadvenv/bin/python", ".cadvenv/bin/python"} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	if p, err := exec.LookPath("python3"); err == nil {
		return p
	}
	t.Skip("no python")
	return ""
}

// A lambda runs, and its parameter is a name the script was given.
//
// # Why this is a fence and not a convenience
//
// `ast.Lambda` was absent from ALLOWED_NODES while `ast.FunctionDef` was present
// — an inconsistency rather than a boundary, since a lambda's body is one
// expression and every node in it is already allowed inside a def. It was
// removed deliberately on 2026-09-09 (see the note beside ast.Lambda).
//
// The second half matters as much as the first: binding the PARAMETERS is a
// separate rule from allowing the node. Without it `lambda i: abs(i)` parses,
// passes the whitelist, and is then refused with "i is not available here" — a
// correct script rejected for using its own argument, and a refusal that reads
// as a broken sandbox because no rule stands behind it.
//
// The source is the shape a real model wrote: on a live run, an involute gear
// script used min(range(n), key=lambda i: ...) and spent a whole repair round
// being told Lambda was not allowed.
func TestScript_ALambdaRunsAndItsParameterResolves(t *testing.T) {
	k := scriptKernel(t)
	source := `pts = [(3.0, 0.0), (1.0, 0.0), (2.0, 0.0)]
nearest = min(range(len(pts)), key=lambda i: abs(pts[i][0] - 2.0))
side = 10.0 + nearest
result = Box(side, side, side)`

	res, err := k.RunScript(context.Background(), source, nil)
	if err != nil {
		t.Fatalf("a lambda did not run: %v\n%s", err, source)
	}
	// nearest is index 2, so the box is 12mm on a side. Asserted rather than
	// "it built", because a lambda whose parameter silently resolved to
	// something else would still build a box.
	if want := 12.0 * 12.0 * 12.0; res.Volume < want-1 || res.Volume > want+1 {
		t.Errorf("volume %.1f, want %.1f — the lambda ran but did not compute what it says",
			res.Volume, want)
	}
}

// A refusal that names an unavailable builder says which ones are close.
//
// # What this closes
//
// "Rotate is not available here" is precise about the mistake and gives the
// reader nothing to move toward. The reader is usually a MODEL correcting its
// own script, and it cannot enumerate the 209 names it may use — so it guesses
// again. Measured live 2026-09-09: after the script-repair loop shipped, the
// remaining gear failures were `Rotate is not available here` and
// `InvoluteGear is not available here`, and the loop spent its whole budget
// re-guessing at an API instead of fixing geometry.
//
// # Why the second half matters as much
//
// A name that is close to NOTHING must get no suggestion. `urlopen` and
// `getattr` are refusals about reaching outside the sandbox, and a "did you
// mean" on one would read as a spelling correction — and would be the first step
// toward a refusal that helpfully enumerates what it is protecting.
func TestScript_AnUnavailableNameSuggestsTheCloseOnes(t *testing.T) {
	k := scriptKernel(t)
	cases := []struct {
		name, source string
		want         []string // must appear
		absent       []string // must not
	}{
		{
			name:   "the live failure: Rotate, when the name is Rotation",
			source: "result = Rotate(Box(1, 1, 1), 90)",
			want:   []string{"Rotate is not available here.", "Did you mean", "Rotation"},
		},
		{
			// Getting the CASE wrong is its own common miss, and a case-sensitive
			// comparison scores it no better than a typo.
			name:   "the right name in the wrong case",
			source: "result = BOX(10, 10, 10)",
			want:   []string{"BOX is not available here.", "Did you mean", "Box"},
		},
		{
			name:   "a near-miss on a maths function",
			source: "result = Box(sqrtt(4.0), 1, 1)",
			want:   []string{"Did you mean", "sqrt"},
		},
		{
			// The other live failure. Nothing in the manifest is close to it, so
			// the refusal says so and stops — a suggestion invented for a name
			// with no neighbour would be worse than none.
			name:   "a builder that was never a builder suggests nothing",
			source: "result = InvoluteGear(module=2, teeth=20, thickness=6)",
			want:   []string{"InvoluteGear is not available here."},
			absent: []string{"Did you mean"},
		},
		{
			// ‼️ The live one. A model wrote `m = module`, reaching for the gear
			// parameter, and was told to consider build123d's `Mode` enum — an
			// edit-distance score of 0.800, HIGHER than Rotate -> Rotation at
			// 0.714, which is why no cutoff can separate them and the suggestion
			// has to share the start of the word instead.
			name:   "a word that is not a misspelling of anything",
			source: "m = module\nresult = Box(m, m, m)",
			want:   []string{"module is not available here."},
			absent: []string{"Did you mean"},
		},
		{
			// The same shape, and the reason it happens: these are the DOCUMENT's
			// parameters, which a script cannot see. Suggesting `thicken` for
			// `thickness` sends the repair somewhere useless.
			name:   "another of the document's parameters",
			source: "result = Box(thickness, thickness, thickness)",
			want:   []string{"thickness is not available here."},
			absent: []string{"Did you mean"},
		},
		{
			// ‼️ At difflib's default cutoff of 0.60 this came back "Did you
			// mean len?" — three shared letters out of ten. The fence caught it
			// before it shipped; see the note on the cutoff in script.py.
			name:   "reaching the network suggests nothing",
			source: "result = urlopen('http://example.com').read()",
			want:   []string{"urlopen is not available here."},
			absent: []string{"Did you mean"},
		},
		{
			name:   "a module name suggests nothing",
			source: "result = socket.socket()",
			want:   []string{"socket is not available here."},
			absent: []string{"Did you mean"},
		},
		{
			name:   "a builtin that is not on the list suggests nothing",
			source: "result = getattr(1, 'real')",
			want:   []string{"getattr is not available here."},
			absent: []string{"Did you mean"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := k.RunScript(context.Background(), tc.source, nil)
			if err == nil {
				t.Fatalf("this ran, and it must not:\n%s", tc.source)
			}
			got := err.Error()
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("the refusal does not contain %q.\ngot: %s", w, got)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Errorf("the refusal contains %q and must not — a refusal about reaching "+
						"outside the sandbox is not a spelling mistake.\ngot: %s", a, got)
				}
			}
		})
	}
}

// A failure says how the builder it blames is actually CALLED.
//
// # What this closes
//
// Once the "did you mean" suggestion fixed the NAMES, what remained across four
// live gear requests was the model guessing at the API behind a name that
// exists:
//
//	TypeError: BuildSketch.__init__() got an unexpected keyword argument 'local_mode'
//	Standard_TypeMismatch: TopoDS::Face
//
// The model has the 209 names it may use and none of their signatures, so the
// repair loop spent its whole budget re-guessing rather than fixing geometry.
// "X is not available here" cannot help when X is available.
//
// The signature is produced at the moment of failure from the build123d that is
// actually installed — the only place it is guaranteed right. A list generated
// at build time would be a second artifact to keep in step with the library, and
// a wrong signature is worse than none because it reads as authoritative.
func TestScript_AFailureSaysHowTheBuilderIsCalled(t *testing.T) {
	k := scriptKernel(t)
	cases := []struct {
		name, source string
		want         []string
	}{
		{
			// The live failure, verbatim in shape: a real builder, a keyword it
			// does not take. The message names it, so the signature can be found
			// from the message alone.
			name:   "a keyword the builder does not take",
			source: "with BuildSketch(local_mode=True) as sk:\n    Circle(radius=5)\nresult = extrude(sk.sketch, amount=3)",
			want:   []string{"local_mode", "BuildSketch(", "workplanes"},
		},
		{
			// The other live failure shape: the message blames a C++ type and
			// names no Python at all, so the FAILING LINE is the only thing that
			// points back at a builder.
			name:   "an error that names no builder at all",
			source: "profile = Polyline((0, 0), (10, 0), (10, 10))\nresult = make_face(profile)",
			want:   []string{"make_face("},
		},
		{
			name:   "a required argument that was not given",
			source: "result = Cylinder(radius=5)",
			want:   []string{"Cylinder(", "height"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := k.RunScript(context.Background(), tc.source, nil)
			if err == nil {
				t.Fatalf("this built, so the test proves nothing:\n%s", tc.source)
			}
			got := err.Error()
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("the failure does not mention %q, so the model is left to guess "+
						"how to call it again.\ngot: %s", w, got)
				}
			}
		})
	}
}

// A refusal that suggests a name also says how that name is called.
//
// "Did you mean Rotation, Rot?" fixes the NAME and leaves the call to be guessed
// at — the same failure one step later, and a second round trip out of a small
// budget. Both questions are answered in one refusal.
func TestScript_ASuggestedNameComesWithItsSignature(t *testing.T) {
	k := scriptKernel(t)
	_, err := k.RunScript(context.Background(), "result = Cylindr(radius=5, height=10)", nil)
	if err == nil {
		t.Fatal("Cylindr built, which it must not")
	}
	got := err.Error()
	for _, w := range []string{"Did you mean", "Cylinder", "They take:", "radius", "height"} {
		if !strings.Contains(got, w) {
			t.Errorf("the refusal does not contain %q, so fixing the name still leaves the "+
				"call to guess at.\ngot: %s", w, got)
		}
	}
}

// A refusal about reaching OUTSIDE the sandbox stays about that.
//
// The signature help must not turn a security refusal into an API tutorial, and
// must not enumerate what is being protected. urlopen and getattr are close to
// nothing, so they suggest nothing, so there is nothing to describe.
func TestScript_ReachingOutsideStillSaysNothingHelpful(t *testing.T) {
	k := scriptKernel(t)
	for _, src := range []string{
		"result = urlopen('http://example.com').read()",
		"result = getattr(1, 'real')",
		"result = open('/etc/passwd').read()",
	} {
		_, err := k.RunScript(context.Background(), src, nil)
		if err == nil {
			t.Fatalf("this ran, and it must not:\n%s", src)
		}
		if got := err.Error(); strings.Contains(got, "They take:") {
			t.Errorf("a refusal about reaching outside the sandbox came back with a "+
				"signature lesson:\n%s", got)
		}
	}
}

// A refused construct is named in the words it was WRITTEN in.
//
// # What this closes
//
// The refusal named the AST class: "MatMult is not allowed here". That is the
// parser's word for it and nobody else's. Measured live: a model reached for
// build123d's own `@` idiom — `edge @ 0.5` is a point along a curve — was told
// "MatMult", and had to guess what that referred to out of a small repair budget.
//
// A message naming something the author never typed cannot be acted on.
func TestScript_ARefusalNamesWhatWasWritten(t *testing.T) {
	k := scriptKernel(t)
	// No `@` case: it was the example that motivated this and is now ALLOWED
	// (see ast.MatMult in script.py). The table entry for it stays, because the
	// table is the general facility and a construct can be refused again.
	cases := []struct{ name, source, want string }{
		{"a class", "class X:\n    pass\nresult = X", "`class`"},
		{"try/except", "try:\n    result = Box(1, 1, 1)\nexcept Exception:\n    pass", "`try`"},
		{"the walrus", "result = (n := Box(1, 1, 1))", "`:=`"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := k.RunScript(context.Background(), tc.source, nil)
			if err == nil {
				t.Fatalf("this ran and must not:\n%s", tc.source)
			}
			got := err.Error()
			if !strings.Contains(got, tc.want) {
				t.Errorf("the refusal does not name %s in the words it was written in.\n"+
					"got: %s", tc.want, got)
			}
			// And the AST class name must NOT be what the reader is handed.
			for _, parser := range []string{"MatMult", "ClassDef", "NamedExpr"} {
				if strings.Contains(got, parser) {
					t.Errorf("the refusal hands back the parser's word %q:\n%s", parser, got)
				}
			}
		})
	}
}

// The `@` operator runs, and it is build123d's own idiom.
//
// # Why this is a fence and not a convenience
//
// `ast.MatMult` was absent while every other binary operator was present, and
// all of them dispatch to a dunder method — Add to __add__, Mod to __mod__.
// MatMult dispatches to __matmul__ and is not different in kind.
//
// Its sibling was already allowed and is the point: in build123d
//
//	edge @ 0.5   is the point half way along
//	edge % 0.5   is the tangent there
//
// `%` is ast.Mod and has worked since the sandbox was written. Refusing the other
// half of a documented pair was arbitrary, and it cost a real run — a model
// reached for `@`, was refused, and spent repair attempts on it.
func TestScript_TheAtOperatorRunsAndComputesTheRightPoint(t *testing.T) {
	k := scriptKernel(t)
	// The point half way along a 10mm line is (5,0,0); the box is then 5mm on a
	// side. Asserted through the VOLUME, because an `@` that silently produced
	// something else would still build a box.
	source := `edge = Line((0, 0), (10, 0))
mid = edge @ 0.5
side = mid.X
result = Box(side, side, side)`
	res, err := k.RunScript(context.Background(), source, nil)
	if err != nil {
		t.Fatalf("build123d's own `@` idiom did not run: %v\n%s", err, source)
	}
	if want := 125.0; res.Volume < want-1 || res.Volume > want+1 {
		t.Errorf("volume %.1f, want %.1f — `@` ran but did not compute the point it says",
			res.Volume, want)
	}
}

// A script can read the document's own parameters.
//
// # What this closes
//
// A scripted part belongs to a document, and that document declares the numbers
// the part is made of — the panel shows them, a person edits them, every other
// shape is built from them. The script could not see any of it. Measured live
// 2026-09-10, a model asked for a gear wrote
//
//	m = module
//	t = teeth_count
//	pa = pressure_angle_deg * math.pi / 180
//	thick = thickness
//
// and every one was refused as an unavailable name. Without them a scripted part
// is the one shape in a document that cannot be parametric, which is the
// opposite of why scripts exist.
func TestScript_ReadsTheDocumentsParameters(t *testing.T) {
	k := scriptKernel(t)
	params := map[string]float64{"module": 2, "teeth_count": 20, "thickness": 6}

	// The volume is asserted, not just "it built": a parameter that silently
	// arrived as something else would still build a box.
	res, err := k.RunScript(context.Background(),
		"side = module * teeth_count\nresult = Box(side, side, thickness)", params)
	if err != nil {
		t.Fatalf("a script could not read its own document's parameters: %v", err)
	}
	if want := 40.0 * 40.0 * 6.0; res.Volume < want-1 || res.Volume > want+1 {
		t.Errorf("volume %.1f, want %.1f — the parameters are in scope but did not carry "+
			"their values", res.Volume, want)
	}
}

// ‼️ A parameter cannot take a name the language already owns, or reach past it.
//
// The names come from a model, and they go straight into the namespace a script
// executes in. Each of these is a way that could go wrong.
func TestScript_AParameterCannotHijackTheNamespace(t *testing.T) {
	k := scriptKernel(t)

	t.Run("a builder wins over a parameter of the same name", func(t *testing.T) {
		// A document with a parameter called Box must not turn Box(...) into a
		// call on a number — that would let a document change the language
		// rather than use it.
		res, err := k.RunScript(context.Background(),
			"result = Box(10, 10, 10)", map[string]float64{"Box": 3})
		if err != nil {
			t.Fatalf("a parameter named Box broke the builder: %v", err)
		}
		if want := 1000.0; res.Volume < want-1 || res.Volume > want+1 {
			t.Errorf("volume %.1f, want %.1f — the parameter shadowed the builder",
				res.Volume, want)
		}
	})

	t.Run("a name beginning with an underscore never becomes a name", func(t *testing.T) {
		// ‼️ Asserted as "the script cannot READ it", which is the property the
		// underscore rule actually holds on its own.
		//
		// The first version of this asserted that a parameter called
		// `__builtins__` could not open a file — and stayed green under a drill,
		// because THREE rules stop that: this one, the setdefault that will not
		// overwrite, and the AST check refusing `open` as an unavailable name
		// before the namespace is even built. A test that cannot see its own
		// rule break is not testing that rule.
		//
		// What the rule is really for: the namespace holds `__builtins__`, and a
		// parameter able to take that name would replace the restricted builtins
		// with a float. That grants nothing — the AST check stands in front of
		// it — but it breaks the script in a way nobody could explain.
		_, err := k.RunScript(context.Background(), "result = Box(_secret, 1, 1)",
			map[string]float64{"_secret": 10})
		if err == nil {
			t.Fatal("a parameter beginning with an underscore became a readable name")
		}
		if !strings.Contains(err.Error(), "_secret is not available here") {
			t.Errorf("refused for the wrong reason: %v", err)
		}
	})

	t.Run("a name that is not an identifier is simply not there", func(t *testing.T) {
		_, err := k.RunScript(context.Background(), "result = Box(1, 1, 1)",
			map[string]float64{"not a name": 1, "class": 2, "": 3})
		if err != nil {
			t.Fatalf("unusable parameter names broke a correct script: %v", err)
		}
	})

	t.Run("a maths function is not shadowed either", func(t *testing.T) {
		res, err := k.RunScript(context.Background(),
			"result = Box(sqrt(100), 10, 10)", map[string]float64{"sqrt": 2})
		if err != nil {
			t.Fatalf("a parameter named sqrt broke the maths function: %v", err)
		}
		if want := 1000.0; res.Volume < want-1 || res.Volume > want+1 {
			t.Errorf("volume %.1f, want %.1f — the parameter shadowed sqrt", res.Volume, want)
		}
	})
}

// ‼️ A whole-numbered parameter arrives as an INT, not a float.
//
// # The regression this closes
//
// The first version of the parameter feature handed every value over as a float.
// So `teeth_count` was 20.0, the model wrote `range(teeth_count)`, and Python
// raised "'float' object cannot be interpreted as an integer" — in 6 of 9 live
// runs. That is WORSE than before parameters existed, because the model had been
// writing `num_teeth = 20` as a literal and it worked.
//
// A count is an integer. The document has no type field to say so; the value
// does. Build123d takes an int wherever it wants a float, and Python 3 division
// is true division, so nothing downstream can tell except the places that
// require an int — which is exactly the point.
func TestScript_AWholeNumberedParameterIsAnInt(t *testing.T) {
	k := scriptKernel(t)

	t.Run("range() over a count", func(t *testing.T) {
		res, err := k.RunScript(context.Background(),
			"total = 0\nfor i in range(teeth_count):\n    total = total + 1\nresult = Box(total, 1, 1)",
			map[string]float64{"teeth_count": 20})
		if err != nil {
			t.Fatalf("a whole-numbered parameter could not be used as a count: %v", err)
		}
		if want := 20.0; res.Volume < want-0.5 || res.Volume > want+0.5 {
			t.Errorf("volume %.1f, want %.1f", res.Volume, want)
		}
	})

	t.Run("a fractional parameter stays fractional", func(t *testing.T) {
		res, err := k.RunScript(context.Background(),
			"result = Box(module, 10, 10)", map[string]float64{"module": 2.5})
		if err != nil {
			t.Fatalf("a fractional parameter failed: %v", err)
		}
		if want := 250.0; res.Volume < want-1 || res.Volume > want+1 {
			t.Errorf("volume %.1f, want %.1f — 2.5 was rounded", res.Volume, want)
		}
	})

	t.Run("division still behaves", func(t *testing.T) {
		// Python 3 true division: 5/2 is 2.5 whether 5 arrived as int or float.
		res, err := k.RunScript(context.Background(),
			"side = teeth / 2\nresult = Box(side, 10, 10)", map[string]float64{"teeth": 5})
		if err != nil {
			t.Fatalf("division on an integral parameter failed: %v", err)
		}
		if want := 250.0; res.Volume < want-1 || res.Volume > want+1 {
			t.Errorf("volume %.1f, want %.1f — 5/2 did not give 2.5", res.Volume, want)
		}
	})
}

// A misused `with` is told which names it can actually use.
//
// # What this closes
//
// Measured live, twice in one ten-run batch: the model wrote
// `with Rotation(...)` and got "'Rotation' object does not support the context
// manager protocol". Rotation's SIGNATURE — Rotation(*args, **kwargs) — answers
// nothing about that, so the general signature help was no help here. What the
// model needs is which names a `with` can take, and the library knows.
func TestScript_AMisusedWithIsToldWhatItCanUse(t *testing.T) {
	k := scriptKernel(t)
	_, err := k.RunScript(context.Background(),
		"with Rotation(0, 0, 45) as r:\n    Box(1, 1, 1)\nresult = r", nil)
	if err == nil {
		t.Fatal("`with Rotation(...)` ran, which it cannot")
	}
	got := err.Error()
	for _, want := range []string{"not something you can use `with`", "BuildPart", "BuildSketch"} {
		if !strings.Contains(got, want) {
			t.Errorf("the failure does not contain %q, so the model has to guess again.\n"+
				"got: %s", want, got)
		}
	}
}

// A name that is a METHOD is answered as one, not as a spelling mistake.
//
// # What this closes
//
// `rotate` was refused live, and the closest global name is `Rotation` — which
// is a Location, not what was wanted, so the suggestion sent the repair
// somewhere useless. `rotate` is real; it is shape.rotate(...).
//
// The defining class is reported rather than the classes that merely have it:
// the first version answered "a method on Airfoil and ArcArcTangentArc", which
// are alphabetically-first leaves inheriting it from Shape. True and useless.
func TestScript_AMethodIsNotASpellingMistake(t *testing.T) {
	k := scriptKernel(t)
	_, err := k.RunScript(context.Background(),
		"b = Box(10, 10, 10)\nresult = rotate(b, 45)", nil)
	if err == nil {
		t.Fatal("a bare rotate() ran")
	}
	got := err.Error()
	for _, want := range []string{"rotate is not a function here", "method on", "Shape",
		"shape.rotate(...)"} {
		if !strings.Contains(got, want) {
			t.Errorf("the failure does not contain %q.\ngot: %s", want, got)
		}
	}
	// ‼️ And the spelling suggestion is SUPPRESSED. Pointing at `Rotation` when
	// the answer is `shape.rotate` is worse than saying nothing.
	if strings.Contains(got, "Did you mean") {
		t.Errorf("a method was offered a spelling correction as well, which sends the "+
			"repair at the wrong answer:\n%s", got)
	}
	// The useless inherited leaves must not be what is named.
	for _, leaf := range []string{"Airfoil", "ArcArcTangentArc"} {
		if strings.Contains(got, leaf) {
			t.Errorf("the failure names %q, an alphabetically-first leaf that merely "+
				"inherits the method:\n%s", leaf, got)
		}
	}
}
