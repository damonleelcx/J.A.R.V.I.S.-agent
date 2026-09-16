package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The workbench hands the mesh reply to the studio whole. Phase 6, stage W1.
//
// # What this holds
//
// Until W1 the workbench expanded every placed copy into its own triangles
// (Forge3D.expandMeshInstances) and wrote them onto the document's parts, so a
// 30,000-occurrence car put 30,000 copies of a rivet's vertices in memory before the
// renderer drew any of it — and the instanced draw would still work, just on the
// flattened copies, one batch per part. Nothing in the renderer's own fences can see
// that: they drive forge3d.js, and this is workbench.js choosing not to use it.
//
// A text check, like TestCameraButtonsAreScopedToTheirContainer, because the wiring
// is one call and the realistic regression is somebody restoring the old one.
func TestWorkbenchDrawsTheMeshReplyInstanced(t *testing.T) {
	b, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatalf("reading workbench.js: %v", err)
	}
	js := codeOnly(string(b))
	if strings.Contains(js, "expandMeshInstances(") {
		t.Error("workbench.js expands the mesh reply into placed triangles again; since Phase 6, stage W1 " +
			"the reply goes to studio.load whole, so each definition is uploaded once and drawn instanced")
	}
	if !strings.Contains(js, "studio.load(proto, b)") {
		t.Error("workbench.js no longer hands the mesh reply to studio.load, so the kernel's definitions and " +
			"matrices never reach the instanced draw and the primitives stay on screen")
	}
}

// The workbench page carries the tree browser's mount points. Phase 6, stage W2.
//
// renderTree and initTree return quietly when an element is missing — the workbench
// must not break on a page without a tree — so a renamed id would leave a design
// written as a tree with an empty Parts panel and no tree, and nothing would say so.
func TestTheWorkbenchHasTheTreeBrowsersMountPoints(t *testing.T) {
	pages := NewPageHandlers(testDeps())
	rr := httptest.NewRecorder()
	pages.Workbench(rr, httptest.NewRequest(http.MethodGet, "/workbench", nil))
	body := rr.Body.String()
	if len(body) < 200 {
		t.Fatalf("the workbench rendered only %d bytes; this fence would pass vacuously", len(body))
	}
	b, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatalf("reading workbench.js: %v", err)
	}
	js := string(b)
	for _, id := range []string{"tree-head", "tree-tools", "tree-search", "tree-showall", "tree"} {
		if !strings.Contains(body, `id="`+id+`"`) {
			t.Errorf("the workbench has no element with id %q, so the assembly tree cannot be shown", id)
		}
		if !strings.Contains(js, "$('"+id+"')") {
			t.Errorf("workbench.js never reads #%s, so the mount point has no producer", id)
		}
	}
}
