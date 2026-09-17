//go:build ignore

// A stand-in for the OpenAI-compatible model endpoint, so forged and forge-worker can be
// driven end to end with ZERO live tokens. Stdlib only; cross-compiled and run in the
// same Linux image as the worker.
//
// Every reply is scripted from knobs written into the goal's statement, which reaches
// every call the goal makes (planning, each step, each look, the executor, the verifier):
//
//	tag=<word>        keys "the first request" per goal, so a hold happens once
//	steps=<n>         how many steps a build plan has (default 3)
//	parts=<n>         occurrences the LAST step's document places (default 8); step k
//	                  places ceil(parts*k/steps), in rows of 64 boxes that only grow
//	stepdelay=<ms>    how long every step reply takes (a slow model)
//	hold=<k>:<ms>     step k's FIRST request is held this long before it is answered
//	lookdelay=<ms>    how long the look after a step takes (a look carries no statement, so
//	                  it takes the knobs of the step call before it: one worker, in order)
//	approvalplan      an ordinary (non-build) plan: an r1 task, then an r2 task after it
//
// Each call is logged as one JSON line with the tokens it reports and whether the reply
// was delivered ("answered") or the caller hung up first ("abandoned"), so the goal's
// tokens_spent can be reconciled against what was actually served.
//
//	go build -o standin ./docs/spikes/2026-09-17-unverified-paths/harness/standin/main.go
//	standin -addr 0.0.0.0:18390 -log /data/standin.jsonl
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type msg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

var (
	stepRe  = regexp.MustCompile(`Step (\d+) of (\d+)`)
	knobRe  = regexp.MustCompile(`\b(tag|steps|parts|stepdelay|lookdelay)=([A-Za-z0-9_-]+)`)
	holdRe  = regexp.MustCompile(`\bhold=(\d+):(\d+)`)
	usageOf = map[string]int{"plan": 400, "step": 1000, "look": 900, "executor": 600,
		"verifier": 500, "work-plan": 450, "other": 50}
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

func knobs(all string) map[string]string {
	k := map[string]string{"steps": "3", "parts": "8", "stepdelay": "0"}
	for _, m := range knobRe.FindAllStringSubmatch(all, -1) {
		k[m[1]] = m[2]
	}
	if m := holdRe.FindStringSubmatch(all); m != nil {
		k["holdstep"], k["holdms"] = m[1], m[2]
	}
	return k
}

func atoi(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// rows of 64 4-mm boxes, 10 mm apart: n occurrences, only ever grown by adding rows.
func document(n int) map[string]any {
	const per = 64
	parts := []any{}
	for r := 0; n > 0; r++ {
		c := min(per, n)
		n -= c
		parts = append(parts, map[string]any{
			"id": fmt.Sprintf("row-%03d", r), "name": fmt.Sprintf("Row %d", r), "shape": "box",
			"size":     map[string]float64{"width": 4, "height": 4, "depth": 4},
			"position": []float64{0, float64(10 * r), 0}, "rotation": []float64{0, 0, 0},
			"repeat": map[string]any{"count": c, "offset": []float64{10, 0, 0}},
		})
	}
	return map[string]any{"name": "Stand-in rows", "units": "mm", "parts": parts,
		"assumptions":  []string{"stand-in model: every size chosen by a script"},
		"not_verified": []string{"nothing: this document exists to exercise the build path"}}
}

func main() {
	addr := flag.String("addr", "0.0.0.0:18390", "listen address")
	logPath := flag.String("log", "standin.jsonl", "JSON lines, one per call")
	flag.Parse()

	lf, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	var mu sync.Mutex
	seen := map[string]bool{}
	var n int
	lastStep := map[string]string{}
	record := func(o map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		o["at"] = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
		b, _ := json.Marshal(o)
		_, _ = lf.Write(append(b, '\n'))
	}

	http.HandleFunc("/v1/models", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"si-converse"},{"id":"si-vision"}]}`))
	})
	http.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		var req struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []msg  `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		all, last := "", ""
		hasImage := false
		for _, m := range req.Messages {
			if strings.Contains(string(m.Content), `"image_url"`) {
				hasImage = true
			}
			t := text(m.Content)
			all += t + "\n"
			if m.Role == "user" {
				last = t
			}
		}
		k := knobs(all)
		mu.Lock()
		n++
		call := n
		mu.Unlock()

		kind, content, step, of := "other", `{"speech":"stand-in: unscripted request"}`, 0, 0
		var delay time.Duration
		switch {
		case hasImage || strings.Contains(req.Model, "vision"):
			kind, content = "look", `{"problems": []}`
			mu.Lock()
			delay = time.Duration(atoi(lastStep["lookdelay"], 0)) * time.Millisecond
			k["tag"] = lastStep["tag"]
			mu.Unlock()
		case strings.HasPrefix(strings.TrimSpace(last), "Plan the build of:"):
			steps := atoi(k["steps"], 3)
			out := []any{}
			for i := 1; i <= steps; i++ {
				out = append(out, map[string]string{"name": fmt.Sprintf("Rows %d", i),
					"what": fmt.Sprintf("Add rows until the model places %d boxes.", atoi(k["parts"], 8)*i/steps)})
			}
			b, _ := json.Marshal(map[string]any{"steps": out})
			kind, content = "plan", string(b)
		case strings.Contains(last, "Building:") && stepRe.MatchString(last):
			s := stepRe.FindStringSubmatch(last)
			step, of = atoi(s[1], 1), atoi(s[2], 1)
			total := atoi(k["parts"], 8)
			want := (total*step + of - 1) / of
			b, _ := json.Marshal(map[string]any{"speech": fmt.Sprintf("Stand-in: step %d of %d, %d boxes.", step, of, want),
				"prototype": document(max(1, want))})
			kind, content = "step", string(b)
			delay = time.Duration(atoi(k["stepdelay"], 0)) * time.Millisecond
			key := k["tag"] + "/" + s[1]
			mu.Lock()
			first := !seen[key]
			seen[key] = true
			lastStep = k
			mu.Unlock()
			if first && k["holdstep"] == s[1] {
				delay = time.Duration(atoi(k["holdms"], 0)) * time.Millisecond
			}
		case strings.Contains(req.Model, "verifier"):
			kind, content = "verifier", `{"verified":true,"confidence":"high","reasoning":"Stand-in verifier: the summary names what was done and the evidence supports it.","unsupported_claims":[],"missing_checks":[],"recommendation":"accept"}`
		case strings.Contains(req.Model, "planner") && strings.Contains(all, "approvalplan"):
			kind, content = "work-plan", `{"rationale":"two tasks: prepare, then the gated one","clarification_needed":"","tasks":[
				{"key":"prepare","title":"Prepare the release notes","instruction":"Write down what changed.","inputs":{},"expected_output":{"description":"notes"},"depends_on":[],"risk_tier":"r1"},
				{"key":"publish","title":"Publish the release notes","instruction":"Publish the notes where the team reads them.","inputs":{},"expected_output":{"description":"published"},"depends_on":["prepare"],"risk_tier":"r2"}]}`
		case strings.Contains(req.Model, "executor"):
			kind, content = "executor", `{"status":"completed","summary":"Stand-in executor: done as instructed.","result":{"done":true},"evidence":["stand-in: no tool was needed"],"assumptions":[]}`
		}

		tokens := usageOf[kind]
		entry := map[string]any{"call": call, "kind": kind, "model": req.Model, "tag": k["tag"],
			"tokens": tokens, "stream": req.Stream}
		if step > 0 {
			entry["step"], entry["of"] = step, of
		}
		if delay > 0 {
			select {
			case <-time.After(delay):
			case <-r.Context().Done():
				entry["outcome"], entry["held_ms"] = "abandoned", time.Since(started).Milliseconds()
				record(entry)
				return
			}
		}
		usage := map[string]int{"prompt_tokens": tokens - 10, "completion_tokens": 10, "total_tokens": tokens}
		var werr error
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			frame := func(o any) {
				b, _ := json.Marshal(o)
				if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil && werr == nil {
					werr = err
				}
			}
			frame(map[string]any{"model": req.Model, "choices": []any{map[string]any{"index": 0,
				"delta": map[string]string{"content": content}, "finish_reason": nil}}})
			frame(map[string]any{"model": req.Model, "choices": []any{map[string]any{"index": 0,
				"delta": map[string]string{}, "finish_reason": "stop"}}})
			frame(map[string]any{"model": req.Model, "choices": []any{}, "usage": usage})
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		} else {
			b, _ := json.Marshal(map[string]any{"id": fmt.Sprintf("si-%d", call), "object": "chat.completion",
				"model": req.Model, "usage": usage,
				"choices": []any{map[string]any{"index": 0, "finish_reason": "stop",
					"message": map[string]string{"role": "assistant", "content": content}}}})
			w.Header().Set("Content-Type", "application/json")
			_, werr = w.Write(b)
		}
		entry["outcome"], entry["took_ms"] = "answered", time.Since(started).Milliseconds()
		if werr != nil {
			entry["outcome"], entry["write_error"] = "write-failed", werr.Error()
		}
		record(entry)
	})
	log.Printf("stand-in model on %s, logging to %s", *addr, *logPath)
	log.Fatal(http.ListenAndServe(*addr, nil))
}
