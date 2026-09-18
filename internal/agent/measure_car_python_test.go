package agent

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ‼️ `make measure-car` finds the kernel on Windows (2026-09-17 live verification).
//
// The target defaulted FORGE_CAD_PYTHON to .cadvenv/bin/python. A Windows venv has no
// bin/: its interpreter is .cadvenv/Scripts/python.exe, so the live run had to set the
// variable by hand. Read with `make -n`, which prints the recipe and runs nothing — no
// model is called and no key is read.
func TestMeasureCar_PicksTheVenvPythonForThisOS(t *testing.T) {
	mk, err := exec.LookPath("make")
	if err != nil {
		t.Skip("no make on PATH; skipping the measure-car interpreter fence")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	line := regexp.MustCompile(`FORGE_CAD_PYTHON="\$\{FORGE_CAD_PYTHON:-([^}"]*)\}"`)
	for _, c := range []struct{ os, want, not string }{
		{"Windows_NT", "/.cadvenv/Scripts/python.exe", "/bin/python"},
		{"Linux", "/.cadvenv/bin/python", "Scripts"},
	} {
		cmd := exec.Command(mk, "-n", "-C", root, "measure-car", "OS="+c.os)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("make -n measure-car OS=%s: %v", c.os, err)
		}
		m := line.FindStringSubmatch(string(out))
		if m == nil {
			t.Fatalf("OS=%s: measure-car no longer sets FORGE_CAD_PYTHON from the environment first:\n%s", c.os, out)
		}
		if !strings.HasSuffix(m[1], c.want) || strings.Contains(m[1], c.not) {
			t.Errorf("OS=%s: measure-car's kernel defaults to %q; want the venv's %s", c.os, m[1], c.want)
		}
	}
}
