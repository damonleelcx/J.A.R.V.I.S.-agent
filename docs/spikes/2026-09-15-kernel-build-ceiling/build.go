//go:build ignore

// Times cad.Kernel.BuildMesh in-process, for the interference check's own numbers.
//
// measure.py times the mesh endpoint of a real forged, which is the product path, but
// neither the mesh reply nor its log line says whether the interference check ran to the
// end or was stopped by its boolean budget, or how many box tests and booleans it made.
// cad.Build does. This is the same call the endpoint makes, with the same pool of one,
// reporting those fields beside the kernel's phases.
//
// geometry/limits.go refuses a build past maxDrawnParts, so this is run from a binary
// built with that constant (and cad.go buildTimeout) lifted, never committed:
//
//	go build -o build.exe docs/spikes/2026-09-15-kernel-build-ceiling/build.go
//	FORGE_CAD_PYTHON=... build.exe car-30000.json barrel-30400.json
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/cad"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/geometry"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

func main() {
	python := os.Getenv("FORGE_CAD_PYTHON")
	if python == "" || len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: FORGE_CAD_PYTHON=... build <document.json> ...")
		os.Exit(2)
	}
	log := logx.New(logx.Options{Format: "text", Service: "ceiling-build"})
	k := cad.New(python, log).WithPool(1)
	ctx := context.Background()

	warm := geometry.Document{Name: "warm-up", Units: "mm", Parts: []geometry.Part{{ID: "b", Shape: "box",
		Size: map[string]float64{"width": 10, "height": 10, "depth": 10}}}}
	if _, err := k.BuildMesh(ctx, warm, geometry.Millimetre); err != nil {
		fmt.Fprintln(os.Stderr, "warm-up:", err)
		os.Exit(1)
	}
	for _, path := range os.Args[1:] {
		body, err := os.ReadFile(path)
		if err != nil {
			panic(err)
		}
		var doc geometry.Document
		if err := json.Unmarshal(body, &doc); err != nil {
			panic(err)
		}
		runtime.GC()
		start := time.Now()
		b, err := k.BuildMesh(ctx, doc, geometry.Millimetre)
		wall := time.Since(start)
		row := map[string]any{"design": strings.TrimSuffix(filepath.Base(path), ".json"), "wall_s": wall.Seconds(),
			"at": time.Now().Format(time.RFC3339)}
		if err != nil {
			row["error"] = err.Error()
		} else {
			payload, _ := json.Marshal(map[string]any{"definitions": b.MeshDefinitions, "instances": b.MeshInstances, "parts": b.Mesh})
			var m runtime.MemStats
			runtime.ReadMemStats(&m)
			row["instances"] = len(b.MeshInstances)
			row["phases_s"] = map[string]float64{"shapes": b.Phases.Shapes.Seconds(), "features": b.Phases.Features.Seconds(),
				"assembly": b.Phases.Assembly.Seconds(), "interferences": b.Phases.Interferences.Seconds(), "mesh": b.Phases.Mesh.Seconds()}
			row["interferences"] = len(b.Interferences)
			row["interferences_truncated"] = b.InterferencesTruncated
			row["interference_box_tests"] = b.InterferenceBoxTests
			row["interference_pairs"] = b.InterferencePairs
			row["interference_booleans"] = b.InterferenceBooleans
			row["interference_reused"] = b.InterferenceReused
			row["payload_bytes_json"] = len(payload)
			row["go_sys_mib"] = m.Sys >> 20
		}
		out, _ := json.Marshal(row)
		fmt.Println(string(out))
	}
}
