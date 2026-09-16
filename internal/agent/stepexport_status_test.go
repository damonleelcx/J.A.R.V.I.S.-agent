package agent

import (
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/blob"
)

// What a task's state means for the export it writes (exportStatus). Succeeded
// needs the task succeeded AND the file stored: the worker writes the two in one
// transaction, so either without the other is reported as the failure it is, and a
// file on a task still running is not a finished export.
func TestExportStatus_AnExportIsSucceededOnlyWhenItsTaskSucceededAndItsFileIsStored(t *testing.T) {
	stored := blob.Key("sha256:" + strings.Repeat("a", 64))
	for _, tc := range []struct {
		name     string
		task     engine.TaskStatus
		key      blob.Key
		detail   string
		attempts int
		want     string
		reason   string
	}{
		{"succeeded with its file", engine.StatusSucceeded, stored, "", 1, ExportSucceeded, ""},
		{"succeeded with no file", engine.StatusSucceeded, "", "", 1, ExportFailed, "no file stored"},
		{"running with a file already on the row", engine.StatusRunning, stored, "", 1, ExportRunning, ""},
		{"claimed", engine.StatusClaimed, "", "", 1, ExportQueued, ""},
		{"never tried", engine.StatusReady, "", "", 0, ExportQueued, ""},
		{"ready after an attempt that did not finish", engine.StatusReady, "", "", 1, ExportQueued, "attempt 1"},
		{"failed", engine.StatusFailed, "", "no bucket: set FORGE_BLOB_BUCKET", 1, ExportFailed, "FORGE_BLOB_BUCKET"},
		{"cancelled", engine.StatusCancelled, "", "", 0, ExportFailed, "cancelled"},
	} {
		got, reason := exportStatus(tc.task, tc.key, tc.detail, tc.attempts)
		if got != tc.want {
			t.Errorf("%s: status %s, want %s", tc.name, got, tc.want)
		}
		if tc.reason == "" && reason != "" && tc.want != ExportFailed {
			t.Errorf("%s: a reason (%q) where none applies", tc.name, reason)
		}
		if tc.reason != "" && !strings.Contains(reason, tc.reason) {
			t.Errorf("%s: reason %q does not say %q", tc.name, reason, tc.reason)
		}
	}
}
