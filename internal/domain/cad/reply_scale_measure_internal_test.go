package cad

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
)

// TestScaleUp_MeasureTheInterferenceReply measures what Go pays to read a kernel
// reply at scale: decoding the line roundTrip reads, turning it into a Build, and
// the repair path's first step over its interferences.
//
// Not a fence: it skips unless FORGE_SCALE_MEASURE_REPLY names a directory holding
// reply-full-<bays>.json files, which measure.py --write-reply writes
// (docs/spikes/2026-09-15-one-million-occurrences/measure.py). It writes one
// go-reply-<file>.json record beside each. docs/spikes/2026-09-15-next-scale-walls.
func TestScaleUp_MeasureTheInterferenceReply(t *testing.T) {
	dir := os.Getenv("FORGE_SCALE_MEASURE_REPLY")
	if dir == "" {
		t.Skip("FORGE_SCALE_MEASURE_REPLY is unset; this measures kernel replies measure.py wrote")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "reply-full-*.json"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no reply-full-*.json in %s (%v)", dir, err)
	}
	mib := func(b uint64) float64 { return float64(b) / (1 << 20) }
	for _, path := range paths {
		rec := map[string]any{"file": filepath.Base(path), "cpus": runtime.NumCPU()}
		start := time.Now()
		line, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		rec["read_ms"] = ms(time.Since(start))
		rec["bytes"] = len(line)

		runtime.GC()
		var before, decoded, built runtime.MemStats
		runtime.ReadMemStats(&before)
		start = time.Now()
		var res reply
		if err := json.Unmarshal(line, &res); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		rec["decode_ms"] = ms(time.Since(start))
		runtime.ReadMemStats(&decoded)
		rec["decode_alloc_mib"] = mib(decoded.TotalAlloc - before.TotalAlloc)
		line = nil
		runtime.GC()
		runtime.ReadMemStats(&decoded)
		rec["decoded_heap_mib"] = mib(decoded.HeapAlloc)

		start = time.Now()
		b, err := buildOf(&res, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		rec["build_ms"] = ms(time.Since(start))
		runtime.ReadMemStats(&built)
		rec["build_alloc_mib"] = mib(built.TotalAlloc - decoded.TotalAlloc)

		rec["parts"] = b.Parts
		rec["listed"] = len(b.Interferences)
		rec["found"] = b.InterferencesFound
		rec["truncated"] = b.InterferencesTruncated

		// What the agent's repair does first with them (agent/interference.go).
		start = time.Now()
		problems := geometry.InterferenceProblems(b.Interferences)
		rec["problems_ms"] = ms(time.Since(start))
		rec["problems"] = len(problems)
		size := 0
		for _, p := range problems {
			size += len(p.Detail) + 3
		}
		rec["problem_lines_bytes"] = size

		out, _ := json.MarshalIndent(rec, "", "  ")
		name := "go-reply-" + strings.TrimSuffix(filepath.Base(path), ".json") + ".json"
		if err := os.WriteFile(filepath.Join(dir, name), out, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("%s", out)
		runtime.KeepAlive(b)
	}
}

func ms(d time.Duration) string { return fmt.Sprintf("%.1f", float64(d.Microseconds())/1000) }
