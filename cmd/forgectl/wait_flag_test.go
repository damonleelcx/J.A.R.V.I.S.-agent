package main

import (
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// The migrate initContainer's only protection against racing postgres is
// `--wait 90s` in the manifest. These cover the two ways that protection could
// be lost without anything appearing to be wrong.
func TestDurationFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want time.Duration
		bad  bool
	}{
		{"absent means do not wait", []string{"migrate"}, 0, false},
		{"separate value", []string{"migrate", "--wait", "90s"}, 90 * time.Second, false},
		{"equals form", []string{"migrate", "--wait=2m"}, 2 * time.Minute, false},
		{"other flags are not confused for it", []string{"migrate", "--dry-run"}, 0, false},
		// The hazard the error message exists for. Go's ParseDuration rejects a
		// bare number, and returning zero for it would mean a manifest that says
		// `--wait 90` silently does not wait at all — the deployment would look
		// configured and behave as though it were not.
		{"a number with no unit is refused, not read as zero", []string{"migrate", "--wait", "90"}, 0, true},
		{"garbage is refused", []string{"migrate", "--wait", "soon"}, 0, true},
		// A trailing flag with nothing after it must not panic on args[i+1].
		{"no value at the end", []string{"migrate", "--wait"}, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := durationFlag(tc.args, "--wait")
			if tc.bad {
				if err == nil {
					t.Fatalf("durationFlag(%q) = %v, want an error — a malformed duration read as zero "+
						"silently removes the wait it was meant to configure", tc.args, got)
				}
				if code := errs.CodeOf(err); code != errs.CodeValidationFailed {
					t.Errorf("code = %v, want %v", code, errs.CodeValidationFailed)
				}
				return
			}
			if err != nil {
				t.Fatalf("durationFlag(%q): %v", tc.args, err)
			}
			if got != tc.want {
				t.Errorf("durationFlag(%q) = %v, want %v", tc.args, got, tc.want)
			}
		})
	}
}
