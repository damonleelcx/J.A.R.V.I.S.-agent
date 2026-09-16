package cad

import (
	"os"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad/cadtest"
)

// TestMain lets this test binary stand in for a kernel process.
//
// ‼️ RunIfAsked must come before m.Run: the kernel starts this binary as its
// "python" in the timeout fences, and the child has to become the fake kernel
// rather than run the package's tests again. See cadtest.
func TestMain(m *testing.M) {
	cadtest.RunIfAsked()
	os.Exit(m.Run())
}
