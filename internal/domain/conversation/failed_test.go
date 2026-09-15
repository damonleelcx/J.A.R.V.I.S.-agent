package conversation

import (
	"strings"
	"testing"
)

// The rules migration 0023 checks, mirrored so a caller gets a sentence.
func TestTurn_OnlyAFailedForgeTurnKeepsARefusedReply(t *testing.T) {
	base := func() Turn {
		return Turn{ConversationID: "cnv_1", OwnerID: "usr_1", Role: RoleForge, Text: "That failed."}
	}
	for _, tc := range []struct {
		name string
		edit func(*Turn)
		ok   bool
	}{
		{"a failed forge turn with its reply", func(t *Turn) { t.Failure = "EXTERNAL_PROTOCOL_ERROR"; t.UnusableReply = "{}" }, true},
		{"a human turn that failed", func(t *Turn) { t.Role = RoleHuman; t.Failure = "EXTERNAL_PROTOCOL_ERROR" }, false},
		{"a kept reply with no failure", func(t *Turn) { t.UnusableReply = "{}" }, false},
		{"a kept reply over the bound", func(t *Turn) {
			t.Failure = "EXTERNAL_PROTOCOL_ERROR"
			t.UnusableReply = strings.Repeat("é", MaxUnusableReply+1)
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			turn := base()
			tc.edit(&turn)
			if err := turn.Validate(); (err == nil) != tc.ok {
				t.Errorf("Validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}
