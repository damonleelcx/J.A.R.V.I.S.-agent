package httpapi

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/conversation"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// A workbench turn that failed BEFORE any reply arrived, through the endpoint.
//
// # What was wrong
//
// Migration 0023 closed half of this: a reply that ARRIVED and could not be used
// leaves a row. The other half stayed open, and it is the half that matters most
// when somebody reports "it just stopped responding" — a model that could not be
// reached, a turn cut off by its own deadline, a reader that went away. None of
// those left a row. The person's own half was in the record and FORGE's half was
// nowhere, so the absence of a row was indistinguishable from the person never
// having spoken. The telemetry panel disclosed the hole rather than hiding it,
// which made it honest and did not make it smaller.
//
// # Why this is fenced through Converse and against Postgres
//
// Every claim here is about the ROW the endpoint writes, at the end of a stream
// that has already failed. A helper that is correct and never called on that
// path looks exactly like one that works.
//
// # What this deliberately does NOT assert
//
// That the row knows what the turn cost. Nothing came back and no provider
// reported usage, so there is no number; the fence asserts the token count is
// absent rather than zero, because zero is the one value a reader takes as good
// news.

// unreachableLLM is a model endpoint that cannot be reached. It fails before a
// single chunk, which is the case migration 0023 left unrecorded.
type unreachableLLM struct{}

func (s *unreachableLLM) Complete(context.Context, llm.Request) (*llm.Response, error) {
	return nil, unreachable()
}

func (s *unreachableLLM) ModelFor(r llm.Role) string {
	if r == llm.RoleVision {
		return ""
	}
	return "stub-model"
}

func (s *unreachableLLM) Stream(context.Context, llm.Request, func(llm.Chunk) error) error {
	return unreachable()
}

func unreachable() error {
	return errs.New("llm.Stream", errs.CodeExternalUnavailable).
		WithDetail("cannot reach the model endpoint")
}

// unreachableTurn asks one turn of a model that cannot be reached and returns
// the stream, having checked that the turn did in fact fail.
func unreachableTurn(t *testing.T, w *wsHarness, message string) string {
	t.Helper()
	deps := w.h.deps
	deps.LLM = &unreachableLLM{}
	h := NewConverseHandlers(deps)
	body, _ := json.Marshal(map[string]string{"message": message})
	r := httptest.NewRequest("POST", "/v1/converse", strings.NewReader(string(body)))
	r = r.WithContext(context.WithValue(context.Background(), ctxKeyUser, w.owner))
	rec := httptest.NewRecorder()
	h.Converse(rec, r)
	stream := rec.Body.String()
	if !strings.Contains(stream, `"kind":"error"`) {
		t.Fatalf("a model that could not be reached did not fail the turn:\n%s", stream)
	}
	return stream
}

// The turn is recorded even though nothing came back.
func TestConverse_ATurnThatFailedBeforeAnyReplyArrivedIsStillInTheRecord(t *testing.T) {
	w := workspaceHarness(t)
	ctx := context.Background()

	convID := conversationIDFrom(t, unreachableTurn(t, w, "Build a small desk lamp."))

	turns, err := conversation.NewRepository().List(ctx, w.pool, convID, w.owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("the record holds %d turn(s); a turn that failed before replying left only the "+
			"person's half, so somebody reporting that it stopped responding generates no evidence",
			len(turns))
	}
	forge := turns[1]
	if forge.Role != conversation.RoleForge {
		t.Fatalf("the second turn is %s, not FORGE's half", forge.Role)
	}
	if forge.Failure != string(errs.CodeExternalUnavailable) {
		t.Errorf("the turn was recorded with failure %q; want %s, which is what failed",
			forge.Failure, errs.CodeExternalUnavailable)
	}
	if forge.Text == "" {
		t.Error("the recorded turn says nothing; it must hold what the person was shown in " +
			"place of a reply, or the record has a turn nobody can read")
	}
	// ‼️ Nothing arrived, so there is nothing to keep. A row with an invented
	// reply in it would be worse than the row that used to be missing.
	if forge.UnusableReply != "" {
		t.Errorf("a reply was kept for a turn where none arrived: %q", forge.UnusableReply)
	}
	// The one thing the server did measure is its own elapsed time.
	if forge.Timing == nil || forge.Timing.RoundTripMS == nil {
		t.Fatalf("the failed turn carries no measurement at all (%+v); the handler measured how "+
			"long it waited, and without it the turn is invisible to the telemetry read",
			forge.Timing)
	}
	// And the one thing it did NOT: what it cost. Absent, not zero.
	if forge.Timing.Tokens != nil {
		t.Errorf("the turn claims it cost %d tokens; no provider reported any, and a measured "+
			"zero is a different fact from an unmeasured one", *forge.Timing.Tokens)
	}
}

// Both failures are listed, told apart, and kept out of the medians.
func TestTelemetry_AFailureBeforeAnyReplyIsListedAndToldApartFromARefusedReply(t *testing.T) {
	w := workspaceHarness(t)

	unreachableTurn(t, w, "Build a small desk lamp.")
	refusedTurn(t, w, "Build a small desk lamp as a model, three steps at most.")

	tel := NewTelemetryHandlers(w.h.deps)
	rec := httptest.NewRecorder()
	tel.Turns(rec, getAs(w.owner, "/v1/telemetry/turns"))

	var got struct {
		Turns []struct {
			Tokens       *int64 `json:"tokens"`
			Failed       bool   `json:"failed"`
			ReplyArrived bool   `json:"reply_arrived"`
			Failure      string `json:"failure"`
		} `json:"turns"`
		MedianFirst *int   `json:"median_first_token_ms"`
		MedianTotal *int   `json:"median_total_ms"`
		Excludes    string `json:"excludes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("%v: %s", err, rec.Body.String())
	}
	if len(got.Turns) != 2 {
		t.Fatalf("telemetry lists %d turn(s); two failed and both are evidence: %s",
			len(got.Turns), rec.Body.String())
	}

	// Newest first, so the refused reply is [0] and the unreachable one is [1].
	var arrived, never int
	for _, turn := range got.Turns {
		if !turn.Failed {
			t.Errorf("a turn that failed is not marked failed: %s", rec.Body.String())
		}
		if turn.Failure == "" {
			t.Errorf("a failed turn is listed without saying what failed, which is the one " +
				"thing that makes it actionable")
		}
		if turn.ReplyArrived {
			arrived++
			if turn.Tokens == nil || *turn.Tokens != 11275 {
				t.Errorf("the refused reply is listed without the tokens it spent: %+v", turn.Tokens)
			}
			continue
		}
		never++
		if turn.Tokens != nil {
			t.Errorf("a turn where no reply arrived is listed as costing %d tokens; nobody "+
				"reported a cost, and a zero would read as free", *turn.Tokens)
		}
	}
	if arrived != 1 || never != 1 {
		t.Errorf("the two failures are not told apart: %d with a reply, %d without; a reader "+
			"sent to look at model output that never existed is sent to the wrong place",
			arrived, never)
	}

	// Neither is a reply, so neither is in a statistic about replies.
	if got.MedianFirst != nil || got.MedianTotal != nil {
		t.Errorf("a failed turn was counted into the medians (first %v, total %v); a failure's "+
			"timing is not a reply's", got.MedianFirst, got.MedianTotal)
	}
	// And the panel no longer tells the reader these are missing.
	if strings.Contains(got.Excludes, "Nothing is recorded for one") {
		t.Errorf("the reply still discloses a hole that has been filled: %q", got.Excludes)
	}
}

// The Telemetry panel draws the two failures as two different things.
//
// # Why this is a fence on the asset's text rather than on its behaviour
//
// failedLabel is a closure inside stage.js's module, reachable only through a
// rendered panel and a DOM. The property worth holding is narrower than the
// render: that the panel CONSULTS which kind of failure it is, and has separate
// words for each. Both are visible in the source, and a change that collapsed
// them would be a change to these lines. Modelled on TestHandsFreeEchoGuards
// ArePresent, which guards its asset the same way and for the same reason.
func TestTheTelemetryPanelTellsAFailedReplyApartFromNoReplyAtAll(t *testing.T) {
	b, err := assetFS.ReadFile("assets/stage.js")
	if err != nil {
		t.Fatalf("reading stage.js: %v", err)
	}
	js := string(b)

	// ‼️ Scoped to failedLabel's own body, not to the whole file.
	//
	// The first version of this fence searched all of stage.js, and its drill
	// stayed GREEN: the panel's explanatory prose beside the list uses the same
	// words, so the phrase survived a mutation that changed the label itself.
	// A fence that asserts a phrase which occurs twice is asserting the wrong
	// one half the time.
	const fn = "function failedLabel(t) {"
	at := strings.Index(js, fn)
	if at < 0 {
		t.Fatal("failedLabel is gone; this test no longer checks what it thinks")
	}
	label := js[at:]
	if end := strings.Index(label, "\n  }"); end > 0 {
		label = label[:end]
	}

	if !strings.Contains(label, "reply_arrived") {
		t.Error("the panel never asks whether a reply arrived, so both failures are drawn " +
			"the same way. A reader told the reply could not be used, when none came back, " +
			"is sent to look at model output that does not exist.")
	}
	if !strings.Contains(label, "failed before any reply arrived") {
		t.Error("the panel has no words for a turn that failed before any reply arrived; " +
			"the row is now written and nothing on screen says what it is")
	}
	if !strings.Contains(label, "failed — the reply could not be used") {
		t.Error("the panel has lost the words for a refused reply, which is the failure " +
			"migration 0023 exists to show")
	}
	// The error code is the one thing that makes a failed turn actionable.
	if !strings.Contains(js, "t.failure") {
		t.Error("the panel drops the error code, so every failed turn reads as the same " +
			"failure and a report of one cannot be acted on")
	}
	// And the old disclosure must be gone: a panel that still says these are
	// unrecorded is a panel that lies in the safe direction, which is still a lie.
	if strings.Contains(js, "Nothing is recorded for one") {
		t.Error("the panel still tells the reader that a turn failing before any reply is " +
			"recorded nowhere; it is recorded now, and the note sends them to the log for " +
			"something that is in front of them")
	}
}

// A restored transcript shows the failure where it happened.
//
// A failed turn's text is the sentence the person was shown INSTEAD of a reply.
// Painted into the ordinary bubble on reload it is indistinguishable from an
// answer, which makes the record say FORGE said something it did not — the same
// substitution historyFor refuses to make towards the model, made towards the
// person instead. The live stream already draws a failure in --bad; the restore
// must agree with it, and now has more to draw, because a turn that failed
// before any reply arrived is recorded too.
func TestTheRestoredTranscriptDrawsAFailedTurnAsAFailure(t *testing.T) {
	b, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatalf("reading workbench.js: %v", err)
	}
	js := string(b)

	restore := strings.Index(js, "function restoreConversation()")
	if restore < 0 {
		t.Fatal("restoreConversation is gone; this test no longer checks what it thinks")
	}
	rest := js[restore:]
	// The end of the function, near enough: the next top-level declaration.
	if end := strings.Index(rest, "\n  /* ‼️ A failed turn is not something FORGE said."); end > 0 {
		rest = rest[:end]
	}
	if !strings.Contains(rest, "markRestoredFailure(") {
		t.Error("the restore never marks a failed turn, so a failure comes back as an " +
			"ordinary FORGE bubble and the record shows FORGE saying a sentence it never said")
	}
	if !strings.Contains(js, "function markRestoredFailure(el, t)") {
		t.Fatal("markRestoredFailure is gone; this test no longer checks what it thinks")
	}
	mark := js[strings.Index(js, "function markRestoredFailure(el, t)"):]
	if !strings.Contains(mark[:400], "t.failure") {
		t.Error("the mark never looks at whether the turn failed, so it marks every turn " +
			"or none")
	}
	if !strings.Contains(mark[:400], "var(--bad)") {
		t.Error("a restored failure is not drawn as one. The live stream paints a failed " +
			"turn in --bad; a transcript that comes back without it disagrees with what " +
			"the person saw at the time")
	}
}
