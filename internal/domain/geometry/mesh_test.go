package geometry

import "testing"

// A wire mesh becomes triangles, and a bad index cannot crash the renderer.
//
// This data crosses a process boundary from a Python sidecar. An out-of-range
// index would panic the rasterizer on the path that draws a picture for an
// automated checker — the least explicable place for a crash.
func TestTrianglesFrom(t *testing.T) {
	verts := []float64{
		0, 0, 0,
		1, 0, 0,
		0, 1, 0,
	}
	t.Run("a well-formed facet", func(t *testing.T) {
		got := TrianglesFrom(verts, []int32{0, 1, 2})
		if len(got) != 1 {
			t.Fatalf("got %d triangles, want 1", len(got))
		}
		// The normal must be computed, not zero: the renderer shades by it, and
		// a zero normal draws every facet in one flat colour.
		if n := got[0].Normal; n == ([3]float64{}) {
			t.Error("the facet has no normal, so every surface would shade identically")
		}
	})
	t.Run("an index past the end is skipped, not trusted", func(t *testing.T) {
		for _, idx := range [][]int32{{0, 1, 99}, {-1, 1, 2}, {0, 1}} {
			got := TrianglesFrom(verts, idx)
			if len(got) != 0 {
				t.Errorf("indices %v produced %d triangle(s); a bad index must be dropped",
					idx, len(got))
			}
		}
	})
	t.Run("a degenerate facet still has a usable normal", func(t *testing.T) {
		got := TrianglesFrom([]float64{0, 0, 0, 0, 0, 0, 0, 0, 0}, []int32{0, 1, 2})
		if len(got) != 1 {
			t.Fatalf("got %d triangles, want 1", len(got))
		}
		if n := got[0].Normal; n == ([3]float64{}) {
			t.Error("a zero-area facet produced a zero normal, which divides by zero " +
				"downstream or shades as pure black")
		}
	})
}
