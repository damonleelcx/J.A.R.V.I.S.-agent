package agent_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
)

// fixedCostClient answers every call at the same cost, optionally holding each
// call until release is closed so several can be in flight at once.
type fixedCostClient struct {
	cost    int64
	release chan struct{}
	entered chan struct{}
}

func (f *fixedCostClient) Complete(ctx context.Context, _ llm.Request) (*llm.Response, error) {
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if f.release != nil {
		<-f.release
	}
	return &llm.Response{Content: "{}", FinishReason: "stop", Usage: llm.Usage{TotalTokens: f.cost}}, nil
}

func (f *fixedCostClient) ModelFor(llm.Role) string { return "fixed" }

// The live car harness's budget is a ceiling, not a line to cross once. The
// 2026-09-15 verified run spent 301,142 of a 300,000 cap, because a call was
// placed whenever anything at all was left. A call is now placed only if the
// costliest call seen so far (with a margin) still fits.
func TestCarMeter_NeverSpendsPastItsBudget(t *testing.T) {
	m := &meteredClient{inner: &fixedCostClient{cost: 11_000}, budget: 30_000}
	placed := 0
	for i := 0; i < 10; i++ {
		if _, err := m.Complete(context.Background(), llm.Request{Role: llm.RoleConverse}); err == nil {
			placed++
		}
	}
	spent, calls, refused := m.report()
	if spent > 30_000 {
		t.Fatalf("spent %d of a 30,000 budget over %d calls: the meter placed a call it could not pay for",
			spent, calls)
	}
	if placed != 2 || refused != 8 {
		t.Fatalf("placed %d and refused %d calls of 11,000 under 30,000, want 2 and 8", placed, refused)
	}
}

// Calls placed at once must each leave room for the others: a vision check of
// several sub-assemblies can put its calls in flight together.
func TestCarMeter_CallsInFlightReserveTheirShare(t *testing.T) {
	inner := &fixedCostClient{cost: 11_000, release: make(chan struct{}), entered: make(chan struct{}, 8)}
	m := &meteredClient{inner: inner, budget: 30_000}
	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Complete(context.Background(), llm.Request{Role: llm.RoleVision}); err == nil {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	// Two may enter (2 x 12,000 reserved of 30,000); wait for them, then let all go.
	<-inner.entered
	<-inner.entered
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, refused := m.report(); refused == 2 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(inner.release)
	wg.Wait()
	spent, _, refused := m.report()
	if spent > 30_000 || admitted != 2 || refused != 2 {
		t.Fatalf("4 calls at once under 30,000: admitted %d, refused %d, spent %d; want 2, 2 and at most 30,000",
			admitted, refused, spent)
	}
}
