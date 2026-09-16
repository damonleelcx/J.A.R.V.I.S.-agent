// A stand-in OpenAI-compatible endpoint for measuring forge-worker's memory.
// Plans a three-step build, answers each step with a whole tree of the size the
// statement names (occurrences=N), grown a third per step, and answers every
// vision call with no problems. Stdlib only.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type msg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

var (
	sizeRe = regexp.MustCompile(`occurrences=(\d+)`)
	stepRe = regexp.MustCompile(`Step (\d+) of (\d+)`)
)

func text(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &parts)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	return b.String()
}

// tree places corners*8+1 occurrences: a chassis, and a corner assembly (upright,
// hub, wheel, five lugs) patterned along x.
func tree(corners int) map[string]any {
	box := func(id string, w, h, d float64) map[string]any {
		return map[string]any{"id": id, "name": id, "shape": "box", "size": map[string]float64{"width": w, "height": h, "depth": d}}
	}
	cyl := func(id string, r, h float64) map[string]any {
		return map[string]any{"id": id, "name": id, "shape": "cylinder", "size": map[string]float64{"radius": r, "height": h}}
	}
	return map[string]any{
		"name": "car", "units": "mm", "root": "car", "parts": []any{},
		"definitions": []any{box("chassis", 4000, 200, 1800), box("upright", 100, 300, 100),
			cyl("hub", 60, 40), cyl("wheel", 300, 200), box("lug", 10, 10, 30)},
		"assemblies": []any{
			map[string]any{"id": "car", "children": []any{
				map[string]any{"id": "chassis", "ref": "chassis", "position": []float64{0, -600, 0}},
				map[string]any{"id": "corner", "ref": "corner", "pattern": map[string]any{"kind": "linear", "count": corners, "offset": []float64{700, 0, 0}}},
			}},
			map[string]any{"id": "corner", "name": "Corner", "children": []any{
				map[string]any{"id": "upright", "ref": "upright"},
				map[string]any{"id": "hub", "ref": "hub", "position": []float64{0, 0, 120}},
				map[string]any{"id": "wheel", "ref": "wheel", "position": []float64{0, 0, 260}},
				map[string]any{"id": "lug", "ref": "lug", "position": []float64{-30, 0, 160}, "pattern": map[string]any{"kind": "linear", "count": 5, "offset": []float64{15, 0, 0}}},
			}},
		},
		"assumptions":  []string{"measurement stub: every size chosen"},
		"not_verified": []string{"nothing: this is a memory measurement"},
	}
}

// distinctTree places the same occurrences as tree, but every corner is its own
// assembly with its own wheel and hub definitions: 2 distinct B-reps per corner
// rather than 5 in the whole design.
func distinctTree(corners int) map[string]any {
	defs := []any{
		map[string]any{"id": "chassis", "name": "chassis", "shape": "box", "size": map[string]float64{"width": 4000, "height": 200, "depth": 1800}},
		map[string]any{"id": "upright", "name": "upright", "shape": "box", "size": map[string]float64{"width": 100, "height": 300, "depth": 100}},
		map[string]any{"id": "lug", "name": "lug", "shape": "box", "size": map[string]float64{"width": 10, "height": 10, "depth": 30}},
	}
	rootKids := []any{map[string]any{"id": "chassis", "ref": "chassis", "position": []float64{0, -600, 0}}}
	asms := []any{}
	for i := 0; i < corners; i++ {
		w, h, c := fmt.Sprintf("wheel%d", i), fmt.Sprintf("hub%d", i), fmt.Sprintf("corner%d", i)
		defs = append(defs,
			map[string]any{"id": w, "name": w, "shape": "cylinder", "size": map[string]float64{"radius": 250 + float64(i%97), "height": 200}},
			map[string]any{"id": h, "name": h, "shape": "cylinder", "size": map[string]float64{"radius": 50 + float64(i%13), "height": 40}})
		asms = append(asms, map[string]any{"id": c, "children": []any{
			map[string]any{"id": "upright", "ref": "upright"},
			map[string]any{"id": "hub", "ref": h, "position": []float64{0, 0, 120}},
			map[string]any{"id": "wheel", "ref": w, "position": []float64{0, 0, 260}},
			map[string]any{"id": "lug", "ref": "lug", "position": []float64{-30, 0, 160}, "pattern": map[string]any{"kind": "linear", "count": 5, "offset": []float64{15, 0, 0}}},
		}})
		rootKids = append(rootKids, map[string]any{"id": c, "ref": c, "position": []float64{float64(700 * i), 0, 0}})
	}
	asms = append([]any{map[string]any{"id": "car", "children": rootKids}}, asms...)
	return map[string]any{"name": "car", "units": "mm", "root": "car", "parts": []any{},
		"definitions": defs, "assemblies": asms,
		"assumptions":  []string{"measurement stub: every size chosen"},
		"not_verified": []string{"nothing: this is a memory measurement"}}
}

func main() {
	addr := "127.0.0.1:55890"
	if len(os.Args) > 1 {
		addr = os.Args[1]
	}
	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model    string `json:"model"`
			Messages []msg  `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		all := ""
		for _, m := range req.Messages {
			all += text(m.Content) + "\n"
		}
		content := `{"speech":"ok"}`
		kind := "other"
		switch {
		case strings.Contains(req.Model, "vision"):
			content, kind = `{"problems":[]}`, "vision"
		case strings.Contains(all, "Plan the build of:"):
			content, kind = `{"steps":[{"name":"chassis","what":"the chassis"},{"name":"corners","what":"the corners"},{"name":"wheels","what":"the wheels"}]}`, "plan"
		case strings.Contains(all, "Building:") && stepRe.MatchString(all):
			n := 40
			if m := sizeRe.FindStringSubmatch(all); m != nil {
				n, _ = strconv.Atoi(m[1])
			}
			s := stepRe.FindStringSubmatch(all)
			k, _ := strconv.Atoi(s[1])
			of, _ := strconv.Atoi(s[2])
			corners := max(1, (n-1)/8*k/of)
			t := tree(corners)
			if strings.Contains(all, "distinct") {
				t = distinctTree(corners)
			}
			doc, _ := json.Marshal(map[string]any{"speech": fmt.Sprintf("step %d: %d corners", k, corners), "prototype": t})
			content, kind = string(doc), fmt.Sprintf("step%d-corners%d", k, corners)
		}
		log.Printf("%s model=%s bytes=%d", kind, req.Model, len(content))
		out, _ := json.Marshal(map[string]any{
			"id": "stub", "model": req.Model,
			"choices": []any{map[string]any{"index": 0, "finish_reason": "stop",
				"message": map[string]any{"role": "assistant", "content": content}}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	})
	log.Printf("stub listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
