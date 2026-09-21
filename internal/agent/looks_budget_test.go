package agent_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// The looks benchmark's two ceilings, proven offline. 2026-09-20, looks stage D2.
//
// # Why these have fences at all
//
// Both rules here were WRONG in the first live run of the benchmark, and both
// failures look exactly like something else:
//
//   - The per-prompt ceiling was checked against a reserve — the largest call seen
//     anywhere in the run, plus a quarter. One build turn cost 18,046 tokens, so
//     the reserve became 22,557, and every one of the four remaining prompts was
//     refused before placing a single call. The record read "the build did not
//     answer" four times, which is what a provider outage reads like.
//   - The total ceiling was per-RUN. damon approved 100,000 tokens for stage D,
//     not 100,000 per attempt; three attempts were needed, and under a per-run
//     ceiling those three would have been entitled to 300,000 between them with
//     nothing in the harness saying so.
//
// Neither can be caught by a live run: a live run that spends too much has already
// spent it. So the metering is exercised here against a stub, with no call
// leaving the machine.

// budgetStub answers every call with a fixed cost and counts what it was asked.
type budgetStub struct {
	mu    sync.Mutex
	cost  int64
	calls int
}

func (s *budgetStub) Complete(context.Context, llm.Request) (*llm.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return &llm.Response{Content: "{}", FinishReason: "stop",
		Usage: llm.Usage{TotalTokens: s.cost}}, nil
}

func (s *budgetStub) ModelFor(llm.Role) string { return "budget-stub" }

func (s *budgetStub) placed() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

func ask(t *testing.T, m *twoCeilingClient) error {
	t.Helper()
	_, err := m.Complete(context.Background(), llm.Request{Role: llm.RoleConverse})
	return err
}

// A per-prompt ceiling bounds ONE prompt; it cannot bound how many prompts run.
func TestTheLooksBenchmarksPerPromptCeilingCannotStopEveryPromptAfterTheFirst(t *testing.T) {
	// The measured case, exactly: a build turn worth more than the per-prompt
	// ceiling, with plenty of room in the total.
	stub := &budgetStub{cost: 18_046}
	m := &twoCeilingClient{inner: stub, total: 100_000, perPrompt: 14_000}

	for _, id := range []string{"bracket", "gear", "car", "enclosure", "lever"} {
		m.start(id)
		if err := ask(t, m); err != nil {
			t.Fatalf("%s was refused its FIRST call: %v\n\nA prompt that has spent nothing is "+
				"weighed against the run's total alone. Otherwise one expensive turn early on "+
				"raises the reserve past the per-prompt ceiling and silently ends the benchmark "+
				"— the 2026-09-20 run built one prompt of five and read like a provider outage.",
				id, err)
		}
	}
	if stub.placed() != 5 {
		t.Fatalf("%d calls were placed for five prompts", stub.placed())
	}
	spent, calls, refused := m.report()
	if refused != 0 || calls != 5 || spent != 5*18_046 {
		t.Errorf("spent=%d calls=%d refused=%d", spent, calls, refused)
	}

	// And it still does its job: a prompt that keeps asking is stopped, and only
	// that prompt.
	small := &budgetStub{cost: 3_000}
	n := &twoCeilingClient{inner: small, total: 100_000, perPrompt: 10_000}
	n.start("car")
	placed := 0
	for i := 0; i < 20; i++ {
		if err := ask(t, n); err != nil {
			if !strings.Contains(err.Error(), `on "car"`) {
				t.Errorf("the refusal does not name the prompt it is about: %v", err)
			}
			break
		}
		placed++
	}
	if placed == 0 || placed >= 20 {
		t.Fatalf("the car placed %d calls; a prompt that keeps asking must be stopped, and not "+
			"on its first call", placed)
	}
	if on := n.spentOn("car"); on > 10_000+3_000 {
		t.Errorf("the car spent %d against a 10,000 ceiling", on)
	}
	// The next prompt is unharmed.
	n.start("lever")
	if err := ask(t, n); err != nil {
		t.Errorf("the lever was refused because the car ran away: %v", err)
	}
}

// A cheap call is not refused because an expensive one was made.
//
// # What this holds
//
// The whole point of this benchmark is the judge, and on 2026-09-20 the judge
// never ran on two of the prompts it had budget for. One build turn cost 18,133
// tokens; the reserve — the largest call anywhere, plus a quarter — became
// 22,666; and each of the judge's 810-token questions was then refused as
// unaffordable. The record said "the judge could not decide", which reads like a
// tight budget rather than like arithmetic that cannot tell a build from a
// question.
func TestTheLooksBenchmarkDoesNotPriceAJudgesQuestionLikeABuild(t *testing.T) {
	// A build, then the four questions the judge asks about it.
	builds := &budgetStub{cost: 18_133}
	m := &twoCeilingClient{inner: builds, total: 100_000, perPrompt: 24_000}
	m.start("gear")
	if _, err := m.Complete(context.Background(), llm.Request{Role: llm.RoleConverse}); err != nil {
		t.Fatalf("the build was refused: %v", err)
	}
	m.inner = &budgetStub{cost: 810}
	for i := 1; i <= 4; i++ {
		if _, err := m.Complete(context.Background(), llm.Request{Role: llm.RoleVision}); err != nil {
			t.Fatalf("the judge's question %d was refused after a build had been paid for: %v\n\n"+
				"An 810-token question is not priced at what an 18,000-token build cost. One "+
				"reserve for every role is right where calls cost roughly the same; this harness "+
				"mixes calls that differ by more than twenty times.", i, err)
		}
	}
	if _, _, refused := m.report(); refused != 0 {
		t.Errorf("%d calls were refused", refused)
	}

	// And a role's OWN history still bounds it: vision calls that turn out to be
	// expensive are reserved for at their own cost, not at the cheap first guess.
	big := &twoCeilingClient{inner: &budgetStub{cost: 9_000}, total: 100_000, perPrompt: 20_000}
	big.start("car")
	placed := 0
	for i := 0; i < 10; i++ {
		if _, err := big.Complete(context.Background(), llm.Request{Role: llm.RoleVision}); err != nil {
			break
		}
		placed++
	}
	if placed == 0 || placed >= 10 {
		t.Fatalf("%d expensive vision calls were placed against a 20,000 per-prompt ceiling; a "+
			"role that turns out to be costly must be reserved for at its own measured cost", placed)
	}
}

// The total ceiling is all-time: an earlier run's spend counts against it.
func TestTheLooksBenchmarksTotalCeilingCountsEarlierRuns(t *testing.T) {
	stub := &budgetStub{cost: 10_000}
	m := &twoCeilingClient{inner: stub, total: 100_000, perPrompt: 50_000, already: 95_000}
	m.start("bracket")
	err := ask(t, m)
	if err == nil {
		t.Fatal("a call was placed although earlier runs had spent 95,000 of a 100,000 ceiling. " +
			"The ceiling damon approved is for the whole of stage D, not for each attempt at it.")
	}
	if !strings.Contains(err.Error(), "95000 of 100000") {
		t.Errorf("the refusal reports %q; it must state the ALL-TIME figure, or a reader will "+
			"think the run had spent nothing", err.Error())
	}
	if stub.placed() != 0 {
		t.Errorf("%d calls reached the provider", stub.placed())
	}
}

// The ledger is what carries that spend between runs, and it is never read
// optimistically.
func TestTheLooksBenchmarksLedgerIsNeverReadOptimistically(t *testing.T) {
	t.Run("a missing ledger is the first run", func(t *testing.T) {
		got, err := readLedger(t.TempDir())
		if err != nil || got != 0 {
			t.Fatalf("readLedger = %d, %v", got, err)
		}
	})

	t.Run("runs accumulate", func(t *testing.T) {
		dir := t.TempDir()
		for _, spend := range []int64{18_046, 12_000, 400} {
			if err := appendLedger(dir, spend, 3, 1); err != nil {
				t.Fatal(err)
			}
		}
		got, err := readLedger(dir)
		if err != nil {
			t.Fatal(err)
		}
		if got != 30_446 {
			t.Fatalf("readLedger = %d, want 30446: a ceiling that forgets what earlier runs spent "+
				"is not a ceiling", got)
		}
		raw, err := os.ReadFile(filepath.Join(dir, ledgerFile))
		if err != nil {
			t.Fatal(err)
		}
		var l ledger
		if err := json.Unmarshal(raw, &l); err != nil {
			t.Fatal(err)
		}
		if len(l.Runs) != 3 || l.Total != 30_446 {
			t.Fatalf("the ledger holds %d runs totalling %d", len(l.Runs), l.Total)
		}
		for i, r := range l.Runs {
			if r.At == "" || r.Calls != 3 || r.Refused != 1 {
				t.Errorf("run %d is recorded as %+v", i, r)
			}
		}
	})

	t.Run("a model an earlier run paid for is not paid for again", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "models"), 0o755); err != nil {
			t.Fatal(err)
		}
		if _, ok := reuseModel(t, dir, "bracket"); ok {
			t.Fatal("a model that does not exist was reported as reusable")
		}
		doc := geometry.Document{Name: "bracket", Units: "mm",
			Parts: []geometry.Part{{ID: "plate", Shape: "box",
				Size: map[string]float64{"length": 120, "width": 80, "height": 8}}}}
		raw, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "models", "bracket.json"), raw, 0o644); err != nil {
			t.Fatal(err)
		}
		got, ok := reuseModel(t, dir, "bracket")
		if !ok || got == nil {
			t.Fatal("a model this benchmark already paid for was not reused; the prompts are fixed, " +
				"so building one twice measures nothing and spends the ceiling twice")
		}
		if got.Name != "bracket" || len(got.Parts) != 1 || got.Parts[0].ID != "plate" {
			t.Errorf("the reused model is %+v", got)
		}
		// Unreadable and empty files are rebuilt rather than failing the run — but
		// never silently passed off as a model.
		for name, body := range map[string]string{
			"corrupt": "{not json",
			"empty":   `{"name":"bracket","units":"mm"}`,
		} {
			if err := os.WriteFile(filepath.Join(dir, "models", "bracket.json"), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if d, ok := reuseModel(t, dir, "bracket"); ok || d != nil {
				t.Errorf("a %s model file was reused as a model", name)
			}
		}
	})

	t.Run("a ledger nobody can read refuses rather than reading as zero", func(t *testing.T) {
		for name, body := range map[string]string{
			"not JSON at all":              "{ this is not json",
			"a total that does not add up": `{"total_spent": 5, "runs": [{"spent": 90000}]}`,
		} {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, ledgerFile), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := readLedger(dir)
			if err == nil {
				t.Errorf("%s: read as %d with no error. A corrupt ledger read as zero is a ceiling "+
					"that silently resets, which is exactly when the next run is about to start.",
					name, got)
			}
		}
	})
}
