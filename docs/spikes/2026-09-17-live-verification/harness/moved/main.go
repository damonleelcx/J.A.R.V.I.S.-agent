//go:build ignore

// moved compares two versions of a design, the one a respec started from and the one it
// made, and says which placed parts moved or resized. With -kernel it also builds both
// in the real kernel and reports the interference of each. No model is asked.
//
//	go run docs/spikes/2026-09-17-live-verification/harness/moved/main.go \
//	    -before before.json (-after after.json | -set '{"name": value}') [-kernel C:/.../python.exe]
//
// Either file may be a bare geometry document or a GET /v1/geometry/{id} body, whose
// document is under "variant"."document".
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sort"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

func load(path string) (geometry.Document, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return geometry.Document{}, err
	}
	var wrapped struct {
		Variant struct {
			Document *geometry.Document `json:"document"`
		} `json:"variant"`
	}
	if json.Unmarshal(raw, &wrapped) == nil && wrapped.Variant.Document != nil {
		return *wrapped.Variant.Document, nil
	}
	var d geometry.Document
	return d, json.Unmarshal(raw, &d)
}

func differs(a, b []float64) bool {
	for i := 0; i < 3; i++ {
		var x, y float64
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if math.Abs(x-y) > 1e-6 {
			return true
		}
	}
	return false
}

func main() {
	before := flag.String("before", "", "the version a respec started from")
	after := flag.String("after", "", "the version it made")
	python := flag.String("kernel", "", "the kernel's python, to build both and compare interference")
	set := flag.String("set", "", `instead of -after: respec -before with these values, as the respec endpoint does ({"name": value})`)
	flag.Parse()
	a, err := load(*before)
	if err != nil {
		fmt.Fprintln(os.Stderr, "before:", err)
		os.Exit(1)
	}
	var b geometry.Document
	if *set != "" {
		var overrides map[string]float64
		if err := json.Unmarshal([]byte(*set), &overrides); err != nil {
			fmt.Fprintln(os.Stderr, "set:", err)
			os.Exit(1)
		}
		// The function geometry.Service.Respec calls (POST /v1/geometry/{id}/respec).
		next, problems := a.WithParameters(overrides)
		for _, p := range problems {
			fmt.Printf("CAVEAT %s: %s\n", p.Name, p.Detail)
		}
		b = *next
	} else if b, err = load(*after); err != nil {
		fmt.Fprintln(os.Stderr, "after:", err)
		os.Exit(1)
	}
	params := func(d geometry.Document) map[string]float64 {
		m := map[string]float64{}
		for _, p := range d.Parameters {
			m[p.Name] = p.Value
		}
		return m
	}
	pa, pb := params(a), params(b)
	var names []string
	for k := range pa {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if pa[k] != pb[k] {
			fmt.Printf("PARAMETER %s %g -> %g\n", k, pa[k], pb[k])
		}
	}
	ea, eb := a.Expanded(), b.Expanded()
	byID := map[string]geometry.Part{}
	for _, p := range ea.Parts {
		byID[p.ID] = p
	}
	moved, resized, same, missing := 0, 0, 0, 0
	for _, q := range eb.Parts {
		p, ok := byID[q.ID]
		if !ok {
			missing++
			continue
		}
		m := differs(p.Position, q.Position)
		sizeChanged := false
		for k, v := range p.Size {
			if math.Abs(q.Size[k]-v) > 1e-6 {
				sizeChanged = true
			}
		}
		switch {
		case m:
			moved++
			fmt.Printf("MOVED %s %v -> %v\n", q.ID, p.Position, q.Position)
		case sizeChanged:
			resized++
			fmt.Printf("RESIZED %s\n", q.ID)
		default:
			same++
		}
		if m && sizeChanged {
			resized++
		}
	}
	fmt.Printf("RESPEC placed_before=%d placed_after=%d moved=%d resized=%d unchanged=%d not_in_before=%d\n",
		len(ea.Parts), len(eb.Parts), moved, resized, same, missing)
	fmt.Printf("FAULTS before=%d after=%d\n", len(a.Faults()), len(b.Faults()))

	if *python == "" {
		return
	}
	log := logx.New(logx.Options{Level: slog.LevelError, Output: os.Stderr, Service: "moved"})
	k := cad.New(*python, log)
	defer k.Close()
	for _, v := range []struct {
		name string
		doc  geometry.Document
	}{{"before", a}, {"after", b}} {
		unit, ok := geometry.ParseUnit(v.doc.Units)
		if !ok {
			unit = geometry.Millimetre
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		build, err := k.BuildDocument(ctx, v.doc, unit, "step")
		cancel()
		if err != nil {
			fmt.Printf("KERNEL %s builds=no %v\n", v.name, err)
			continue
		}
		fmt.Printf("KERNEL %s builds=yes volume=%.0f skipped=%d interference_pairs=%d buried=%d\n", v.name,
			build.Volume, len(build.Skipped), len(build.Interferences),
			len(geometry.InterferenceProblems(build.Interferences)))
	}
}
