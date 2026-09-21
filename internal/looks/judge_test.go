package looks

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// The looks judge, proven offline. 2026-09-20, looks stage D.
//
// # Why every one of these runs against a stub and not a model
//
// This gate exists to REFUSE, and the ways it can wrongly accept are all
// properties of the code rather than of the model: a swap that is not undone, an
// agreement rule that counts one answer twice, a guard that reads the wrong
// side, a third image that turns a comparison into a gallery. A live run cannot
// hold any of them — it can only show what one model said on one afternoon. So
// the model is a recording stub that answers from a script, and what is fenced
// is what the gate DOES with the answers.
//
// The stub records every request it is given, so the fences can read the actual
// question rather than trusting that it was asked properly. That is the same
// reason look_subassembly_test.go's lookStub keeps its prompts.

// recordingModel answers from a script and keeps every request.
type recordingModel struct {
	mu sync.Mutex
	// answers are returned in order, one per call. A call past the end is a
	// test-authoring mistake and says so rather than repeating the last answer.
	answers []string
	// tokens is charged per call, so the fences can read Decision.Tokens.
	tokens int64
	// vision is what ModelFor(RoleVision) reports; "" means no judge at all.
	vision string
	// err, when set, is returned by every call.
	err error

	seen  []llm.Request
	calls int
}

func (m *recordingModel) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seen = append(m.seen, req)
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	if m.calls > len(m.answers) {
		return nil, errors.New("the stub was asked more questions than it has answers for")
	}
	return &llm.Response{
		Content:      m.answers[m.calls-1],
		FinishReason: "stop",
		Usage:        llm.Usage{TotalTokens: m.tokens},
	}, nil
}

func (m *recordingModel) ModelFor(role llm.Role) string {
	if role == llm.RoleVision {
		return m.vision
	}
	return ""
}

func (m *recordingModel) requests() []llm.Request {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]llm.Request(nil), m.seen...)
}

// answer is one scripted reply in the shape the contract asks for.
func answer(which, why string) string {
	raw, _ := json.Marshal(map[string]string{"verdict": which, "why": why})
	return string(raw)
}

// The four pictures a change is made of. Distinguishable on sight so a fence can
// say WHICH image was in WHICH position.
const (
	oldShaded  = "data:image/png;base64,T0xE"     // "OLD"
	newShaded  = "data:image/png;base64,TkVX"     // "NEW"
	oldNormals = "data:image/png;base64,T0xETg==" // "OLDN"
	newNormals = "data:image/png;base64,TkVXTg==" // "NEWN"
)

// aChange is one design change with a clean check on both sides.
func aChange() Change {
	return Change{
		ID:     "gear-fillets",
		What:   "the tooth roots were filleted",
		Before: Side{Shaded: oldShaded, Faults: 2, Buried: 7, Skipped: 1},
		After:  Side{Shaded: newShaded, Faults: 2, Buried: 7, Skipped: 1},
	}
}

// imagesOf reads what one request actually carried.
func imagesOf(req llm.Request) []string {
	for _, m := range req.Messages {
		if m.Role == llm.User {
			return m.Images
		}
	}
	return nil
}

// A verdict counts only when the SAME answer survives the images being swapped.
//
// # What this holds
//
// Position bias is the one failure a pairwise vision judge has every time: asked
// "which of these two is better", a model prefers the second image far more
// often than the pictures justify. The only defence that costs nothing but a
// second call is to ask again with the order reversed and keep the answer only
// when it moves with the pictures. So this fence checks three separate things
// that are easy to get individually right and collectively wrong: that both
// orders were actually asked, that the swap really swapped, and that the
// recorded verdict is about the NEW render in both rounds rather than about
// whichever image was second.
func TestAVerdictCountsOnlyWhenTheSwapAgrees(t *testing.T) {
	// "image1 is better" then "image2 is better": with the swap, both say the
	// NEW one.
	m := &recordingModel{vision: "stub-vision", tokens: 11,
		answers: []string{answer("image1", "the new one flows"), answer("image2", "the new one flows")}}
	d, err := (Judge{Client: m}).Judge(context.Background(), aChange())
	if err != nil {
		t.Fatalf("the judge failed: %v", err)
	}

	if len(d.Rounds) != 2 {
		t.Fatalf("a shaded pair is %d rounds; it must be exactly two, one each way round", len(d.Rounds))
	}
	if !d.Rounds[0].NewIsFirst || d.Rounds[1].NewIsFirst {
		t.Fatalf("the two rounds were asked in orders %v and %v; they must be opposite",
			d.Rounds[0].NewIsFirst, d.Rounds[1].NewIsFirst)
	}
	reqs := m.requests()
	if len(reqs) != 2 {
		t.Fatalf("%d calls were placed for a shaded pair, want 2", len(reqs))
	}
	if got := imagesOf(reqs[0]); len(got) != 2 || got[0] != newShaded || got[1] != oldShaded {
		t.Errorf("round 1 sent %v; the NEW render must be first", got)
	}
	if got := imagesOf(reqs[1]); len(got) != 2 || got[0] != oldShaded || got[1] != newShaded {
		t.Errorf("round 2 sent %v; the OLD render must be first", got)
	}
	for i, r := range d.Rounds {
		if r.Said != Better {
			t.Errorf("round %d was recorded as %q; both answers named the NEW render, so both "+
				"rounds must read as %q — anything else means the swap was not undone",
				i+1, r.Said, Better)
		}
	}

	if !d.Agreed || d.Verdict != Better || !d.Accepted {
		t.Fatalf("agreed=%v verdict=%q accepted=%v; two agreeing preferences with a clean check "+
			"is the one case that accepts. Reason: %s", d.Agreed, d.Verdict, d.Accepted, d.Reason)
	}
	if d.Tokens != 22 {
		t.Errorf("the decision cost %d tokens; two calls at 11 is 22, and a gate that does not "+
			"count its own spend cannot be capped", d.Tokens)
	}
	// Nothing in the question may name the change: a judge told what was done
	// reports that it worked.
	for i, r := range reqs {
		for _, msg := range r.Messages {
			if strings.Contains(strings.ToLower(msg.Content), "fillet") {
				t.Errorf("call %d told the judge what changed (%q); it must be asked about the "+
					"pictures alone", i+1, msg.Content)
			}
		}
	}
}

// Two identical-looking renders are no difference, and no difference is not an
// acceptance.
//
// The judge is told in its contract that "same" is an ordinary answer, for the
// reason look.go gives about open questions: a model asked to pick a winner
// between two identical pictures will pick one. This holds the other half —
// that a "same" the model DOES give is not quietly rounded up.
func TestNoDifferenceIsNotAnAcceptance(t *testing.T) {
	m := &recordingModel{vision: "stub-vision",
		answers: []string{answer("same", "they look the same to me"), answer("same", "no visible change")}}
	d, err := (Judge{Client: m}).Judge(context.Background(), aChange())
	if err != nil {
		t.Fatalf("the judge failed: %v", err)
	}
	if !d.Agreed {
		t.Errorf("two answers of \"same\" agree with each other; agreed=%v", d.Agreed)
	}
	if d.Verdict != Same || d.Accepted {
		t.Fatalf("verdict=%q accepted=%v; a tie accepts nothing. Reason: %s", d.Verdict, d.Accepted, d.Reason)
	}
	if len(m.requests()) != 2 {
		t.Errorf("%d calls; a tie must not buy a third ask to break it", len(m.requests()))
	}
	if !strings.Contains(d.Reason, "no difference") {
		t.Errorf("the reason given was %q; it must say what was seen", d.Reason)
	}
}

// A judge that changes its answer when the images are swapped is refused, and
// the record says it was the positions it read.
func TestAJudgeThatFlipsWhenSwappedIsRefused(t *testing.T) {
	// Both answers name the SECOND image — which is the new render in round 1
	// and the old one in round 2. That is position bias exactly.
	m := &recordingModel{vision: "stub-vision",
		answers: []string{answer("image2", "the second is better"), answer("image2", "the second is better")}}
	d, err := (Judge{Client: m}).Judge(context.Background(), aChange())
	if err != nil {
		t.Fatalf("the judge failed: %v", err)
	}
	if d.Rounds[0].Said != Worse || d.Rounds[1].Said != Better {
		t.Fatalf("the two rounds read as %q and %q; always naming image 2 is one vote each way",
			d.Rounds[0].Said, d.Rounds[1].Said)
	}
	if d.Agreed {
		t.Error("agreed=true for two opposite answers")
	}
	if d.Verdict != Same || d.Accepted {
		t.Fatalf("verdict=%q accepted=%v; a disagreement is not a verdict", d.Verdict, d.Accepted)
	}
	if !strings.Contains(d.Reason, "swapped") {
		t.Errorf("the reason given was %q; the record must say the judge read the positions", d.Reason)
	}
}

// The check's counts can refuse a change the judge liked, and no count may be
// dropped.
//
// # Why all three, in one fence
//
// Faults, buried pairs and skipped parts are three separate numbers read from
// three separate places in the existing check's output, and the easy mistake is
// to wire two of them and leave the third reading the before value on both
// sides — which looks exactly like a working guard until the day it matters.
// Each subtest raises ONE of them by one, from the same clean change the
// acceptance fence uses, so nothing but that number can explain the refusal.
func TestAPrettierPictureCannotPayForAWorseModel(t *testing.T) {
	worse := map[string]func(*Side){
		"a fault was added":       func(s *Side) { s.Faults++ },
		"a pair became buried":    func(s *Side) { s.Buried++ },
		"a part could not build":  func(s *Side) { s.Skipped++ },
		"all three at once":       func(s *Side) { s.Faults++; s.Buried++; s.Skipped++ },
		"two of the three, again": func(s *Side) { s.Buried += 4; s.Skipped += 2 },
	}
	for name, spoil := range worse {
		t.Run(name, func(t *testing.T) {
			c := aChange()
			spoil(&c.After)
			m := &recordingModel{vision: "stub-vision",
				answers: []string{answer("image1", "flows better"), answer("image2", "flows better")}}
			d, err := (Judge{Client: m}).Judge(context.Background(), c)
			if err != nil {
				t.Fatalf("the judge failed: %v", err)
			}
			if d.Verdict != Better || !d.Agreed {
				t.Fatalf("the judge's own verdict was %q (agreed=%v); this fence is about the "+
					"check overruling a Better", d.Verdict, d.Agreed)
			}
			if d.Guard.OK || d.Accepted {
				t.Fatalf("guard ok=%v accepted=%v; the judge preferred it but the model got worse",
					d.Guard.OK, d.Accepted)
			}
			if len(d.Guard.Rose) == 0 {
				t.Fatal("nothing was named as having risen, so the refusal cannot be checked")
			}
			for _, rose := range d.Guard.Rose {
				if !strings.Contains(d.Reason, rose) {
					t.Errorf("the reason %q does not carry %q", d.Reason, rose)
				}
			}
		})
	}

	// And the other direction: a change that IMPROVES the check is not refused
	// for having moved it.
	t.Run("a check that improves does not refuse", func(t *testing.T) {
		c := aChange()
		c.After.Faults, c.After.Buried, c.After.Skipped = 0, 1, 0
		m := &recordingModel{vision: "stub-vision",
			answers: []string{answer("image1", "flows"), answer("image2", "flows")}}
		d, err := (Judge{Client: m}).Judge(context.Background(), c)
		if err != nil {
			t.Fatalf("the judge failed: %v", err)
		}
		if !d.Guard.OK || !d.Accepted {
			t.Fatalf("guard ok=%v accepted=%v reason=%q", d.Guard.OK, d.Accepted, d.Reason)
		}
	})
}

// One question carries at most two images, and the refusal behind that is real.
//
// # Why this is fenced at all
//
// Two is not a style rule. A third picture turns "which of these two" into an
// open invitation to comment, which is the question internal/agent/look.go was
// written to avoid; and it destroys the swapped-order re-ask, because with three
// images there is no single swap and the position-bias check silently becomes
// theatre. So this reads what the stub was actually SENT on every call of every
// path the gate has, rather than trusting the constant.
func TestOneLooksQuestionCarriesAtMostTwoImages(t *testing.T) {
	if MaxImagesPerQuestion != 2 {
		t.Fatalf("MaxImagesPerQuestion is %d; the pairwise comparison this gate is built on is two",
			MaxImagesPerQuestion)
	}
	c := aChange()
	c.Before.Normals, c.After.Normals = oldNormals, newNormals
	m := &recordingModel{vision: "stub-vision", answers: []string{
		answer("image1", "a"), answer("image2", "b"), // shaded, both Better
		answer("image1", "c"), answer("image2", "d"), // normals, both Better
	}}
	d, err := (Judge{Client: m}).Judge(context.Background(), c)
	if err != nil {
		t.Fatalf("the judge failed: %v", err)
	}
	if !d.Accepted {
		t.Fatalf("accepted=%v reason=%q; this fence needs the longest path the gate has", d.Accepted, d.Reason)
	}
	reqs := m.requests()
	if len(reqs) != 4 {
		t.Fatalf("%d calls; a shaded pair confirmed by a normals pair is 4", len(reqs))
	}
	for i, r := range reqs {
		got := imagesOf(r)
		if len(got) != 2 {
			t.Errorf("call %d carried %d images; every looks question is exactly the two renders "+
				"being compared", i+1, len(got))
		}
		if len(got) > MaxImagesPerQuestion {
			t.Errorf("call %d carried %d images, past the limit of %d", i+1, len(got), MaxImagesPerQuestion)
		}
		if got[0] == got[1] {
			t.Errorf("call %d sent the same picture twice (%q)", i+1, got[0])
		}
	}
	// And the rounds the decision kept say the same thing, so an audit cannot be
	// of a question that was never asked.
	for i, r := range d.Rounds {
		if len(r.Images) != 2 {
			t.Errorf("round %d recorded %d images", i+1, len(r.Images))
		}
	}
}

// The normals pair is asked only where it can change the answer, and it can only
// take an acceptance away.
func TestTheNormalsPairConfirmsAnAcceptanceAndNeverRescuesOne(t *testing.T) {
	withNormals := func() Change {
		c := aChange()
		c.Before.Normals, c.After.Normals = oldNormals, newNormals
		return c
	}

	t.Run("the surfaces disagree with the shading, so nothing is accepted", func(t *testing.T) {
		m := &recordingModel{vision: "stub-vision", answers: []string{
			answer("image1", "shaded: new is better"), answer("image2", "shaded: new is better"),
			answer("image2", "normals: the old surface is smoother"),
			answer("image1", "normals: the old surface is smoother"),
		}}
		d, err := (Judge{Client: m}).Judge(context.Background(), withNormals())
		if err != nil {
			t.Fatalf("the judge failed: %v", err)
		}
		if d.Accepted || d.Verdict != Same {
			t.Fatalf("accepted=%v verdict=%q; the light looked better and the surface did not",
				d.Accepted, d.Verdict)
		}
		if !strings.Contains(d.Reason, "normals") {
			t.Errorf("the reason %q does not say which view overruled the other", d.Reason)
		}
		reqs := m.requests()
		if len(reqs) != 4 {
			t.Fatalf("%d calls, want 4", len(reqs))
		}
		if got := imagesOf(reqs[2]); got[0] != newNormals || got[1] != oldNormals {
			t.Errorf("the normals round sent %v; it must compare the two NORMALS views", got)
		}
	})

	t.Run("a refusal is not re-asked in normals", func(t *testing.T) {
		for name, script := range map[string][]string{
			"the judge preferred the old one": {answer("image2", ""), answer("image1", "")},
			"the judge saw no difference":     {answer("same", ""), answer("same", "")},
			"the judge flipped":               {answer("image2", ""), answer("image2", "")},
		} {
			t.Run(name, func(t *testing.T) {
				m := &recordingModel{vision: "stub-vision", answers: script}
				d, err := (Judge{Client: m}).Judge(context.Background(), withNormals())
				if err != nil {
					t.Fatalf("the judge failed: %v", err)
				}
				if d.Accepted {
					t.Fatal("accepted a change the shaded pair did not prefer")
				}
				if len(m.requests()) != 2 {
					t.Errorf("%d calls; a normals pair cannot turn a refusal into an acceptance, "+
						"so buying one is buying the answer we wanted", len(m.requests()))
				}
			})
		}
	})

	t.Run("no normals view means the shaded pair decides alone", func(t *testing.T) {
		m := &recordingModel{vision: "stub-vision",
			answers: []string{answer("image1", ""), answer("image2", "")}}
		d, err := (Judge{Client: m}).Judge(context.Background(), aChange())
		if err != nil {
			t.Fatalf("the judge failed: %v", err)
		}
		if !d.Accepted || len(m.requests()) != 2 {
			t.Fatalf("accepted=%v calls=%d", d.Accepted, len(m.requests()))
		}
	})

	t.Run("one side without a normals view is not compared against a shaded one", func(t *testing.T) {
		c := aChange()
		c.After.Normals = newNormals // and Before has none
		m := &recordingModel{vision: "stub-vision",
			answers: []string{answer("image1", ""), answer("image2", "")}}
		d, err := (Judge{Client: m}).Judge(context.Background(), c)
		if err != nil {
			t.Fatalf("the judge failed: %v", err)
		}
		if len(m.requests()) != 2 {
			t.Fatalf("%d calls; comparing a normals view against a shaded one asks which picture "+
				"is prettier, not which surface is better", len(m.requests()))
		}
		if !d.Accepted {
			t.Errorf("accepted=%v reason=%q", d.Accepted, d.Reason)
		}
	})
}

// A deployment with no vision model has no styling gate and says so, rather than
// reporting every change as acceptable.
//
// This is look.go's errNoVision distinction, and the reason it exists is written
// there: a swallowed failure read as a clean bill of health on a car with a
// wheel buried inside its body. Here the same collapse would read as "the judge
// had no objection".
func TestNoVisionModelIsSaidRatherThanAccepted(t *testing.T) {
	for name, j := range map[string]Judge{
		"no client at all":  {},
		"no vision model":   {Client: &recordingModel{vision: ""}},
		"nothing to render": {Client: &recordingModel{vision: "stub-vision"}},
	} {
		t.Run(name, func(t *testing.T) {
			c := aChange()
			if name == "nothing to render" {
				c.After.Shaded = ""
			}
			d, err := j.Judge(context.Background(), c)
			if err == nil {
				t.Fatal("no error: a gate that cannot look must not report a verdict")
			}
			if d.Accepted {
				t.Fatal("accepted=true from a gate that could not look")
			}
			if d.Verdict != Same {
				t.Errorf("verdict=%q", d.Verdict)
			}
		})
	}

	t.Run("a provider that fails mid-pair is an error, not a tie", func(t *testing.T) {
		m := &recordingModel{vision: "stub-vision", err: errors.New("model_not_found")}
		if _, err := (Judge{Client: m}).Judge(context.Background(), aChange()); err == nil {
			t.Fatal("a failed call was read as an answer")
		}
	})
}

// An answer the gate cannot read is "no difference", because that refuses.
func TestAnUnreadableAnswerRefuses(t *testing.T) {
	for _, body := range []string{"", "I think the second one, honestly", `{"verdict":`, `{"verdict":"image3"}`} {
		m := &recordingModel{vision: "stub-vision", answers: []string{body, body}}
		d, err := (Judge{Client: m}).Judge(context.Background(), aChange())
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if d.Accepted || d.Verdict != Same {
			t.Errorf("%q was read as verdict=%q accepted=%v", body, d.Verdict, d.Accepted)
		}
	}
	// And the shapes it CAN read, including the one wrapped in prose.
	for body, want := range map[string]sideSaid{
		`{"verdict":"image1","why":"x"}`:                 firstIsBetter,
		`{"verdict":"IMAGE 2","why":"x"}`:                secondIsBetter,
		"here you go:\n{\"verdict\":\"second\"}\nthanks": secondIsBetter,
		`{"verdict":"same"}`:                             neitherIsBetter,
	} {
		if got, _ := readVerdict(body); got != want {
			t.Errorf("%q read as %v, want %v", body, got, want)
		}
	}
}

// Every verdict is written out beside the exact images that produced it.
func TestEveryVerdictIsKeptWithItsImages(t *testing.T) {
	c := aChange()
	c.Before.Normals, c.After.Normals = oldNormals, newNormals
	m := &recordingModel{vision: "stub-vision", tokens: 9, answers: []string{
		answer("image1", "the new body flows"), answer("image2", "the new body flows"),
		answer("image1", "and the surface is smooth"), answer("image2", "and the surface is smooth"),
	}}
	d, err := (Judge{Client: m}).Judge(context.Background(), c)
	if err != nil {
		t.Fatalf("the judge failed: %v", err)
	}
	dir := t.TempDir()
	if err := WriteAudit(dir, d); err != nil {
		t.Fatalf("the audit could not be written: %v", err)
	}
	into := filepath.Join(dir, "gear-fillets")
	raw, err := os.ReadFile(filepath.Join(into, "verdict.json"))
	if err != nil {
		t.Fatalf("no verdict.json: %v", err)
	}
	var rec auditRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("verdict.json is not readable: %v", err)
	}
	if rec.ID != "gear-fillets" || rec.What != c.What || !rec.Accepted || rec.Verdict != Better {
		t.Fatalf("the record says id=%q what=%q accepted=%v verdict=%q", rec.ID, rec.What, rec.Accepted, rec.Verdict)
	}
	if rec.Tokens != 36 {
		t.Errorf("the record says %d tokens; four calls at 9 is 36", rec.Tokens)
	}
	if rec.Check.Faults != "2→2" || rec.Check.Buried != "7→7" || rec.Check.Skipped != "1→1" || !rec.Check.Passes {
		t.Errorf("the record's check reads %+v; a reader must be able to see both sides", rec.Check)
	}
	if len(rec.Rounds) != 4 {
		t.Fatalf("%d rounds recorded, want 4", len(rec.Rounds))
	}
	want := [][2]string{{newShaded, oldShaded}, {oldShaded, newShaded}, {newNormals, oldNormals}, {oldNormals, newNormals}}
	for i, r := range rec.Rounds {
		if len(r.Images) != 2 {
			t.Fatalf("round %d recorded %v", i+1, r.Images)
		}
		for k, name := range r.Images {
			got, err := os.ReadFile(filepath.Join(into, name))
			if err != nil {
				t.Fatalf("round %d image %d (%s) was not written: %v", i+1, k+1, name, err)
			}
			_, payload, _ := strings.Cut(want[i][k], ",")
			expect, _ := base64.StdEncoding.DecodeString(payload)
			if string(got) != string(expect) {
				t.Errorf("round %d image %d holds %q; the audit must keep the picture that was "+
					"actually sent, in the position it was sent in", i+1, k+1, got)
			}
		}
		if r.Raw != d.Rounds[i].Raw {
			t.Errorf("round %d kept %q, not the answer verbatim (%q)", i+1, r.Raw, d.Rounds[i].Raw)
		}
		if r.SaidAboutTheNewOne != string(d.Rounds[i].Said) {
			t.Errorf("round %d says %q, the decision says %q", i+1, r.SaidAboutTheNewOne, d.Rounds[i].Said)
		}
		// The order must be written down in words: a reader who cannot tell which
		// file was image 1 cannot audit a position-bias check.
		if (i%2 == 0) != strings.HasPrefix(r.Order, "image 1 is the NEW") {
			t.Errorf("round %d's order line reads %q", i+1, r.Order)
		}
	}
}

// A change id cannot write outside the audit directory.
func TestAnAuditCannotBeWrittenOutsideItsDirectory(t *testing.T) {
	for _, id := range []string{"../escaped", "a/b", `..\escaped`, "", "C:/windows/x"} {
		dir := t.TempDir()
		d := Decision{Change: Change{ID: id}}
		if err := WriteAudit(dir, d); err != nil {
			t.Fatalf("%q: %v", id, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if !e.IsDir() {
				t.Errorf("%q wrote %q directly into the audit directory", id, e.Name())
			}
			if strings.ContainsAny(e.Name(), `/\`) || e.Name() == ".." {
				t.Errorf("%q became the directory %q", id, e.Name())
			}
		}
		if len(entries) != 1 {
			t.Errorf("%q wrote %d entries", id, len(entries))
		}
	}
}
