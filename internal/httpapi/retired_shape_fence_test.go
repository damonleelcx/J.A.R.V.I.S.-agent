package httpapi

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// The browser's copy of the retirement table has to agree with Go's.
//
// # Why there are two copies at all
//
// The renderer is browser code with no Go in it, so it cannot import
// internal/domain/geometry. It holds its own RETIRED table for the same reason
// it holds its own tessellation constants — and for the same reason those are
// fenced: a word that resolves to a cylinder in the viewport and to something
// else in the exported file is a picture that does not match the file, which is
// the one failure this whole layer exists to prevent.
//
// This drives the renderer's REAL dispatch in node, rather than reading the
// table as text. A table that agreed perfectly while buildGeometry stopped
// consulting it would pass a text comparison and draw a bounding box.
func TestTheRendererRetiresTheSameShapeWords(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping the renderer retirement comparison")
	}
	src, err := assetFS.ReadFile("assets/forge3d.js")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	asset := filepath.Join(dir, "forge3d.js")
	if err := os.WriteFile(asset, src, 0o600); err != nil {
		t.Fatal(err)
	}
	harness := filepath.Join(dir, "run.js")
	script := `
      const fs = require('fs'), vm = require('vm');
      const sandbox = { window: {}, console };
      vm.createContext(sandbox);
      vm.runInContext(fs.readFileSync(process.argv[2], 'utf8'), sandbox);
      const F = sandbox.window.Forge3D;
      const word = process.argv[3];
      // Driven through the real dispatch: a table nothing consults would pass a
      // comparison of tables.
      const built = F.buildGeometry({ shape: word, size: { radius: 10, height: 40 } });
      const round = F.buildGeometry({ shape: 'cylinder', size: { radius: 10, height: 40 } });
      process.stdout.write(JSON.stringify({
        retired: Object.keys(F.retiredShapes),
        supported: F.supportedShapes,
        note: built.approximated || '',
        sameAsCylinder: JSON.stringify(built.geo.positions) === JSON.stringify(round.geo.positions)
      }));
    `
	if err := os.WriteFile(harness, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command(node, harness, asset, "tube").Output()
	if err != nil {
		t.Fatalf("the renderer could not be driven: %v", err)
	}
	var got struct {
		Retired        []string `json:"retired"`
		Supported      []string `json:"supported"`
		Note           string   `json:"note"`
		SameAsCylinder bool     `json:"sameAsCylinder"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}

	want := geometry.RetiredShapeWords()
	if len(got.Retired) != len(want) {
		t.Errorf("the browser retires %v; Go retires %v. A word retired in one and not the "+
			"other draws differently from what it exports.", got.Retired, want)
	}
	for _, w := range want {
		found := false
		for _, g := range got.Retired {
			if g == w {
				found = true
			}
		}
		if !found {
			t.Errorf("the browser does not retire %q, which Go does", w)
		}
		// And it must not still be advertised as a supported shape: that list
		// went stale once already (three outline shapes were drawn correctly and
		// named nowhere), and a retired word left in it is the same defect
		// pointing the other way.
		for _, s := range got.Supported {
			if s == w {
				t.Errorf("%q is retired but still in the renderer's supportedShapes list", w)
			}
		}
	}

	if !got.SameAsCylinder {
		t.Error("the renderer drew a retired `tube` as something other than the cylinder the " +
			"exporter builds for it. The picture and the file now disagree about one part.")
	}
	if !strings.Contains(got.Note, "solid cylinder") {
		t.Errorf("the renderer drew a retired shape without saying what it read it as (%q). "+
			"The note is what reaches the provenance banner; without it the viewer sees a "+
			"bar and is told nothing.", got.Note)
	}
}
