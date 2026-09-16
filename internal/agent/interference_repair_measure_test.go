package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// TestScaleUp_MeasureTheRepairPrompt measures what an overlap repair asks a model
// about, on the kernel's real replies for the airframe barrel
// (docs/spikes/2026-09-15-check-profile). It runs only when
// FORGE_SCALE_MEASURE_REPAIR names a directory holding, for each size,
// barrel-doc-<bays>.json (TestScaleUp_MeasureAirframeBarrel) and
// reply-full-<bays>.json (measure.py --write-reply). For each it prints and writes
// repair-<bays>.json: the problem lines one per buried clash would have been, and the
// summary sent instead; and repair-prompt-<bays>.txt, the summary itself.
func TestScaleUp_MeasureTheRepairPrompt(t *testing.T) {
	dir := os.Getenv("FORGE_SCALE_MEASURE_REPAIR")
	if dir == "" {
		t.Skip("set FORGE_SCALE_MEASURE_REPAIR to a directory of barrel documents and kernel replies")
	}
	for _, bays := range []int{9, 30, 100} {
		docPath := filepath.Join(dir, fmt.Sprintf("barrel-doc-%d.json", bays))
		replyPath := filepath.Join(dir, fmt.Sprintf("reply-full-%d.json", bays))
		docBody, err := os.ReadFile(docPath)
		if err != nil {
			t.Logf("skipping %d bays: %v", bays, err)
			continue
		}
		replyBody, err := os.ReadFile(replyPath)
		if err != nil {
			t.Logf("skipping %d bays: %v", bays, err)
			continue
		}
		var doc geometry.Document
		if err := json.Unmarshal(docBody, &doc); err != nil {
			t.Fatal(err)
		}
		var reply struct {
			Interferences []geometry.Interference `json:"interferences"`
			Found         int                     `json:"interferences_found"`
		}
		if err := json.Unmarshal(replyBody, &reply); err != nil {
			t.Fatal(err)
		}

		start := time.Now()
		lines := geometry.InterferenceProblems(reply.Interferences)
		linesTook := time.Since(start)
		start = time.Now()
		asks := repairAsks(&doc, reply.Interferences, reply.Found)
		asksTook := time.Since(start)

		text := make([]string, len(asks))
		groups := 0
		for i, p := range asks {
			text[i] = "- " + p.Detail
			if strings.HasPrefix(p.Detail, "Group ") {
				groups++
			}
		}
		rec := map[string]any{
			"bays": bays, "found": reply.Found, "listed": len(reply.Interferences), "buried_listed": len(lines),
			"lines_bytes": problemBytes(lines), "lines_ms": float64(linesTook.Microseconds()) / 1000,
			"summary_bytes": problemBytes(asks), "summary_lines": len(asks), "groups_described": groups,
			"summary_ms": float64(asksTook.Microseconds()) / 1000, "budget_bytes": maxRepairProblemBytes,
		}
		line, _ := json.Marshal(rec)
		fmt.Printf("REPAIR %s\n", line)
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("repair-%d.json", bays)), line, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("repair-prompt-%d.txt", bays)),
			[]byte(strings.Join(text, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if problemBytes(asks) > maxRepairProblemBytes {
			t.Errorf("%d bays: the summary is %d bytes, over the %d budget", bays, problemBytes(asks), maxRepairProblemBytes)
		}

		// Added 2026-09-15 (last hot spots): the same clashes asked within a sweep of
		// candidate budgets, so the 16 KiB constant is chosen against what each buys
		// — how many groups a model is actually told about — instead of against a
		// bytes-per-token guess. Each is written out for tokens.py to tokenize.
		for _, kib := range []int{2, 4, 8, 16, 32, 64} {
			swept := repairAsksWithin(&doc, reply.Interferences, reply.Found, kib<<10)
			lines := make([]string, len(swept))
			groups := 0
			for i, p := range swept {
				lines[i] = "- " + p.Detail
				if strings.HasPrefix(p.Detail, "Group ") {
					groups++
				}
			}
			body := strings.Join(lines, "\n") + "\n"
			if err := os.WriteFile(filepath.Join(dir,
				fmt.Sprintf("repair-prompt-%d-%dkib.txt", bays, kib)), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			sweep, _ := json.Marshal(map[string]any{
				"bays": bays, "budget_kib": kib, "budget_bytes": kib << 10,
				"summary_bytes": problemBytes(swept), "summary_lines": len(swept),
				"groups_described": groups, "over_budget": problemBytes(swept) > kib<<10,
			})
			fmt.Printf("REPAIRSWEEP %s\n", sweep)
		}
	}

	// ‼️ The barrel cannot choose this bound. Its worst 10,000 clashes are ONE
	// group — one child placed in one assembly, copied by patterns — so its summary
	// is three lines and 942 bytes at every budget from 2 KiB to 64 KiB. The budget
	// only ever binds on a model with MANY placements at fault, so that is what is
	// swept here: 600 parts each buried once in a housing, which is 600 groups, the
	// shape the existing fences use (ARepairSummaryIsTheSamePromptEveryTime).
	//
	// What each budget buys is therefore "how many of the 600 groups does the model
	// get told about", which is the number the bound should be chosen on.
	var many []geometry.Interference
	for k := 0; k < 600; k++ {
		many = append(many, geometry.Interference{A: fmt.Sprintf("pin-%c%c", 'a'+k/26, 'a'+k%26),
			B: "housing", ALabel: "Pin", BLabel: "Housing", Volume: 90, Fraction: 0.9})
	}
	for _, kib := range []int{2, 4, 8, 16, 32, 64} {
		swept := repairAsksWithin(barrelLike(), many, len(many), kib<<10)
		groups, covered := 0, 0
		for _, p := range swept {
			if strings.HasPrefix(p.Detail, "Group ") {
				groups++
				covered++
			}
		}
		text := make([]string, len(swept))
		for i, p := range swept {
			text[i] = "- " + p.Detail
		}
		body := strings.Join(text, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(dir,
			fmt.Sprintf("repair-prompt-manygroups-%dkib.txt", kib)), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		sweep, _ := json.Marshal(map[string]any{
			"fixture": "600 groups", "budget_kib": kib, "budget_bytes": kib << 10,
			"summary_bytes": problemBytes(swept), "summary_lines": len(swept),
			"groups_described": groups, "groups_total": 600,
			"over_budget": problemBytes(swept) > kib<<10,
		})
		fmt.Printf("REPAIRSWEEP %s\n", sweep)
	}
}
