package cad

import (
	"strings"
	"testing"
)

// A script process starts with one thread for every native thread pool, and with
// nothing else it does not need. On a machine with 4 or more CPUs a multi-threaded
// build under the script's 1 GiB address-space cap hung until the 30 s wall clock.
// docs/bugfix/2026-09-14-scripts-hung-on-machines-with-four-or-more-cores.md
func TestScriptEnv_RunsTheKernelOnOneThread(t *testing.T) {
	have := map[string]string{}
	for _, kv := range scriptEnv("/tmp/forge-script") {
		k, v, _ := strings.Cut(kv, "=")
		have[k] = v
	}
	for _, k := range []string{"OMP_NUM_THREADS", "OPENBLAS_NUM_THREADS", "TBB_NUM_THREADS", "MKL_NUM_THREADS"} {
		if have[k] != "1" {
			t.Errorf("%s is %q; a script must run its kernel on one thread, or it hangs under the address-space cap on a 4+ CPU machine", k, have[k])
		}
	}
	allowed := map[string]bool{"PATH": true, "HOME": true, "LC_ALL": true,
		"OMP_NUM_THREADS": true, "OPENBLAS_NUM_THREADS": true, "TBB_NUM_THREADS": true, "MKL_NUM_THREADS": true}
	for k := range have {
		if !allowed[k] {
			t.Errorf("the script environment carries %s; it holds only what Python and the thread pools need", k)
		}
	}
}
