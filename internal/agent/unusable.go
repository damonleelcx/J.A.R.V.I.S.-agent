package agent

import (
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/text"
)

// A reply the model sent that this build could not use.
//
// # What was wrong (2026-09-15)
//
// The first workbench turn of a live build exercise failed after 2.7 s with
// "An external service replied in a shape this build cannot use". The model had
// answered — about 11,300 tokens, paid for — and Reply.validate refused the
// reply because it carried neither speech nor detail. RespondStream returned the
// validation error and nothing else, so the handler had no reply to keep and no
// usage to record: the person's half of the turn was in the record, FORGE's half
// was nowhere, the tokens were counted nowhere, and why the reply was unusable
// could only be guessed at.
// docs/bugfix/2026-09-15-a-failed-workbench-turn-left-no-trace.md
//
// # Why an error type rather than a second return value
//
// RespondStream's only way out is its error; its signature is threaded through
// the workbench and the rooms. An error that carries the reply and its cost
// travels through both unchanged, keeps its code for errs.CodeOf (Unwrap), and a
// caller that does not care about it sees exactly the error it saw before.

// UnusableReply is a turn the model answered and this build could not use.
type UnusableReply struct {
	// Err is why it could not be used, with its code.
	Err error
	// Raw is the reply as it arrived, bounded by UnusableReplyLimit.
	//
	// ‼️ Untrusted model output (PRD SEC-04). It is kept so somebody can see why
	// the turn failed, and it must never be put back in front of a model: it is
	// neither what FORGE said nor anything the person saw.
	Raw string
	// Model is which model answered, when the provider said.
	Model string
	// Tokens is what the provider reported for the call. Zero when it reported
	// nothing, which the caller must keep distinct from a measured zero.
	Tokens int64
}

func (u *UnusableReply) Error() string { return u.Err.Error() }
func (u *UnusableReply) Unwrap() error { return u.Err }

// UnusableReplyLimit bounds the raw reply kept, in characters.
//
// Half of converseMaxTokens' worth of text is far more than it takes to see why
// a reply had nothing to say, and small enough that a failed turn stays a row.
// The cut is marked, never silent (text.Clip).
const UnusableReplyLimit = 8000

// unusable wraps err with the reply that caused it and what it cost.
func unusable(err error, raw, model string, usage llm.Usage) error {
	return &UnusableReply{
		Err:    err,
		Raw:    text.Clip(strings.TrimSpace(raw), UnusableReplyLimit),
		Model:  model,
		Tokens: usage.TotalTokens,
	}
}
