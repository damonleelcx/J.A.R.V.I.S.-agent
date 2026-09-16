package geometry

import (
	"strings"
	"testing"
)

// A placement written FROM the root may name the root, and every attachment refusal
// says which child it is about and what to write instead.
//
// Live car run 2026-09-15 (docs/spikes/2026-09-15-car-verified): a build step declared
// {"id":"suspension-mounts","ref":"suspension-mounts","at":"chassis/cockpit-floor"} on
// a model whose root is "chassis", and was refused `assembly "chassis" has no child
// "chassis"`. #111 said a leading root id was dropped; it was dropped only when
// SUGGESTING a path, never when resolving one.
// docs/bugfix/2026-09-15-a-placement-from-the-root-could-not-name-the-root.md

// partPlaced is the flattened part with this id, and whether the document places it.
func partPlaced(d Document, id string) (Part, bool) {
	for _, p := range d.PlacedParts() {
		if p.ID == id {
			return p, true
		}
	}
	return Part{}, false
}

// samePlacement reports whether two placed parts sit in exactly the same frame.
func samePlacement(a, b Part) bool {
	if a.Mirrored != b.Mirrored || len(a.Position) != len(b.Position) || len(a.Rotation) != len(b.Rotation) {
		return false
	}
	for _, pair := range [][2][]float64{{a.Position, b.Position}, {a.Rotation, b.Rotation}} {
		for i := range pair[0] {
			if d := pair[0][i] - pair[1][i]; d > 1e-9 || d < -1e-9 {
				return false
			}
		}
	}
	return true
}

// ‼️ From the root, "chassis/cockpit-floor" IS "cockpit-floor": a path a child of the
// root writes may repeat the root's own id, and resolves to the same frame.
func TestInterface_APathFromTheRootMayBeginWithTheRootsOwnID(t *testing.T) {
	for _, tc := range []struct{ name, withRoot, plain string }{
		{"the root's own interface, which is what the live step wrote", "car/hub-face", "hub-face"},
		{"a sibling's interface", "car/suspension-left/hub", "suspension-left/hub"},
		{"a sibling's nested interface, through a mirror", "car/suspension-right/knuckle/hub", "suspension-right/knuckle/hub"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := carWithSuspension("")
			d.Assemblies[0].Children = append(d.Assemblies[0].Children,
				Child{ID: "with-root", Ref: "block", At: tc.withRoot},
				Child{ID: "without", Ref: "block", At: tc.plain})
			for _, p := range d.Faults() {
				t.Fatalf("%q was refused: %s %s", tc.withRoot, p.Name, p.Detail)
			}
			withRoot, placed := partPlaced(d, "with-root")
			if !placed {
				t.Fatalf("nothing was placed for %q", tc.withRoot)
			}
			without, placed := partPlaced(d, "without")
			if !placed {
				t.Fatalf("nothing was placed for %q", tc.plain)
			}
			if !samePlacement(withRoot, without) {
				t.Errorf("%q put the part at %v %v mirrored=%v; %q put it at %v %v mirrored=%v",
					tc.withRoot, withRoot.Position, withRoot.Rotation, withRoot.Mirrored,
					tc.plain, without.Position, without.Rotation, without.Mirrored)
			}
		})
	}

	// ‼️ A real child named like the root keeps its meaning. The leading id is dropped
	// only where nothing of that name is placed, so the leniency can never take a path
	// away from the child it already named.
	t.Run("a real child of the root named like the root wins", func(t *testing.T) {
		d := carWithSuspension("")
		d.Assemblies[0].Children = append(d.Assemblies[0].Children,
			Child{ID: "car", Ref: "knuckle", Position: []float64{7, 0, 0}},
			Child{ID: "on-the-child", Ref: "block", At: "car/hub"},
			Child{ID: "past-the-child", Ref: "block", At: "car/hub-face"})
		refused := map[string]string{}
		for _, p := range d.Faults() {
			refused[p.Name] = p.Detail
		}
		if why, bad := refused["on-the-child"]; bad {
			t.Errorf(`"car/hub" no longer reaches the child "car": %s`, why)
		}
		// And the root's own "hub-face" is NOT reachable through it: the child was found,
		// so nothing was dropped, and the knuckle declares no such interface.
		if why, bad := refused["past-the-child"]; !bad {
			t.Error(`"car/hub-face" was read past a real child "car" to the root's own interface`)
		} else if !strings.Contains(why, `declares no interface "hub-face"`) {
			t.Errorf("the refusal is not about the child's own interfaces: %s", why)
		}
	})

	// ‼️ And the leniency stops at the root. A child written INSIDE another assembly
	// that names the root is still told to attach from the root instead — silently
	// resolving it would let an assembly written once reach out of itself, which is the
	// rule D1d rests on (interface.go, leaves).
	t.Run("a child inside another assembly is still told to attach from the root", func(t *testing.T) {
		d := carWithSuspension("car/hub-face")
		var detail string
		for _, p := range d.Faults() {
			if p.Name == "wheels/left" {
				detail = p.Detail
			}
		}
		for _, want := range []string{`outside its assembly "wheels"`,
			`attach it from the root "car" instead, as a child there with "at": "hub-face"`} {
			if !strings.Contains(detail, want) {
				t.Errorf("the refusal does not say %q:\n%s", want, detail)
			}
		}
		if _, placed := partPlaced(d, "wheels/left"); placed {
			t.Error("the child inside another assembly was attached to the root's own interface anyway")
		}
	})
}

// ‼️ Every attachment refusal names the child it is about and says what to write
// instead. The live run's repair was shown `- is attached at "…", but …` — no child,
// no instruction — and spent both attempts moving the path somewhere else wrong.
func TestInterface_EveryAttachmentFaultNamesTheChildAndWhatToWriteInstead(t *testing.T) {
	d := carWithSuspension("suspension-left/hub") // wheels/left, reaching outside itself
	d.Assemblies[0].Children = append(d.Assemblies[0].Children,
		Child{ID: "lost", Name: "Lost Wheel", Ref: "block", At: "nowhere"},
		Child{ID: "ghosted", Ref: "block", At: "ghost/hub"})
	details := map[string]string{}
	for _, p := range d.Faults() {
		details[p.Name] = p.Detail
	}
	if len(details) != 3 {
		t.Fatalf("want the three refused children, got %v", details)
	}

	// The child's own name, before the verb, so the sentence says which child wherever
	// it travels; the id is the fault's Name, which every reader already prints.
	const remedy = `write "at" on this child as one of hub-face, suspension-left/hub, ` +
		`suspension-left/knuckle/hub, suspension-right/hub, suspension-right/knuckle/hub, ` +
		`or leave it out to place it in "car"'s own frame`
	for _, tc := range []struct {
		child string
		says  []string
	}{
		{"lost", []string{`(Lost Wheel) is attached at "nowhere"`,
			`assembly "car" declares no interface "nowhere"`, remedy}},
		{"ghosted", []string{`is attached at "ghost/hub"`, `has no child "ghost"`, remedy}},
		// The one that leaves its assembly keeps #111's fix, which is its remedy.
		{"wheels/left", []string{`outside its assembly "wheels"`,
			`attach it from the root "car" instead, as a child there with "at": "suspension-left/hub"`}},
	} {
		for _, want := range tc.says {
			if !strings.Contains(details[tc.child], want) {
				t.Errorf("the refusal of %s does not say %q:\n%s", tc.child, want, details[tc.child])
			}
		}
	}
	// Said as a property, not only of the three above: nothing refuses an attachment
	// without telling the reader what to do about it.
	for child, detail := range details {
		if !strings.Contains(detail, "attach it from the root") && !strings.Contains(detail, `write "at" on this child`) {
			t.Errorf("the refusal of %s carries no remedy:\n%s", child, detail)
		}
	}

	// Bounded: a build step's note clips a fault at 200 characters, so a root with forty
	// mounting points offers the first few and counts the rest.
	t.Run("the paths offered are bounded and the rest counted", func(t *testing.T) {
		wide := carWithSuspension("")
		for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
			wide.Assemblies[0].Interfaces = append(wide.Assemblies[0].Interfaces, Interface{ID: id})
		}
		wide.Assemblies[0].Children = append(wide.Assemblies[0].Children, Child{ID: "lost", Ref: "block", At: "nowhere"})
		var detail string
		for _, p := range wide.Faults() {
			if p.Name == "lost" {
				detail = p.Detail
			}
		}
		// Nine interfaces on the root and four on what it places, six offered, seven counted.
		if !strings.Contains(detail, "(and 7 more)") {
			t.Errorf("the refusal does not count the paths it left out:\n%s", detail)
		}
		_, offered, found := strings.Cut(detail, "write \"at\" on this child as one of ")
		offered, _, _ = strings.Cut(offered, " (and ")
		if !found || strings.Count(offered, ", ") != maxRemedyPaths-1 {
			t.Errorf("the refusal offers %q; want %d paths", offered, maxRemedyPaths)
		}
	})

	// An assembly with nothing to attach to says so rather than offering an empty list.
	t.Run("an assembly that offers nothing says to leave the path out", func(t *testing.T) {
		bare := Document{Name: "bare", Units: "mm", Root: "bare", Definitions: []Part{block("block")},
			Assemblies: []Assembly{{ID: "bare", Children: []Child{{ID: "c", Ref: "block", At: "nowhere"}}}}}
		var detail string
		for _, p := range bare.Faults() {
			detail = p.Detail
		}
		if !strings.Contains(detail, `leave "at" out to place this child in "bare"'s own frame`) {
			t.Errorf("the refusal does not say to leave the path out:\n%s", detail)
		}
	})
}
