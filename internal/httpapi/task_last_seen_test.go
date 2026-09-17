package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
)

// A task a worker holds carries when that worker last said it is still at work, so a
// client following a long build step has progress to show (PRD NFR-02); a task nobody
// holds carries nothing, because a stamp on a finished or waiting task would read as a
// worker still busy with it.
// docs/bugfix/2026-09-17-a-long-build-step-showed-no-progress-for-its-whole-length.md
func TestTaskDTO_AHeldTaskSaysWhenItsWorkerWasLastSeenAndOtherTasksDoNot(t *testing.T) {
	seen := time.Date(2026, 9, 17, 13, 4, 5, 0, time.UTC)
	for _, c := range []struct {
		status engine.TaskStatus
		want   bool
	}{
		{engine.StatusClaimed, true},
		{engine.StatusRunning, true},
		{engine.StatusVerifying, true},
		{engine.StatusPending, false},
		{engine.StatusReady, false},
		{engine.StatusAwaitingApproval, false},
		{engine.StatusSucceeded, false},
		{engine.StatusFailed, false},
	} {
		t.Run(string(c.status), func(t *testing.T) {
			dto := toTaskDTO(&engine.Task{ID: "tsk_1", Status: c.status, UpdatedAt: seen}, nil)
			body, err := json.Marshal(dto)
			if err != nil {
				t.Fatal(err)
			}
			has := strings.Contains(string(body), `"last_seen_at":"2026-09-17T13:04:05Z"`)
			if has != c.want {
				t.Fatalf("a %s task's JSON has last_seen_at %v, want %v: %s", c.status, has, c.want, body)
			}
		})
	}
}
