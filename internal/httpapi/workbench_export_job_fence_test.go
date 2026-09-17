package httpapi

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The workbench reaches the off-node STEP export job (2026-09-17).
//
// PR #99 built POST /v1/geometry/{id}/exports, its status and its download, and no
// button in the product reached them, so a design past the 4,096 parts a request builds
// could be looked at and never taken away as a B-Rep. This runs workbench.js's own
// watcher in node — loaded as a page loads it — against stubbed replies, and counts the
// requests: a poller that never stops, or one that asks through POST /v1/goals (which
// a viewer is refused, PR #123), looks the same on screen as one that does not.

const exportJobHarness = `
  const fs = require('fs'), vm = require('vm');
  const src = fs.readFileSync(process.argv[2], 'utf8');
  const sandbox = { window: {}, document: { readyState: 'loading', addEventListener() {} }, console };
  vm.createContext(sandbox);
  vm.runInContext(src, sandbox);
  const X = sandbox.window.ForgeExportJob;
  const input = JSON.parse(fs.readFileSync(process.argv[3], 'utf8'));
  if (!X) { process.stdout.write(JSON.stringify({ missing: true })); return; }
` + liftFunctions + `
  const flush = () => new Promise((res) => setImmediate(res));
  async function run(sc) {
    let n = 0, pending = null;
    const out = { requests: [], updates: [], delays: [] };
    const fetch = (url, init) => {
      out.requests.push(((init && init.method) || 'GET') + ' ' + url);
      const r = sc.replies[Math.min(n++, sc.replies.length - 1)];
      return Promise.resolve({ ok: r.status < 300, status: r.status, json: () => Promise.resolve(r.body) });
    };
    const w = X.watch('ver_1', {
      fetch, limit: sc.limit,
      setTimeout: (fn, ms) => { out.delays.push(ms); pending = fn; return out.delays.length; },
      clearTimeout: () => { pending = null; },
      onUpdate: (u) => out.updates.push({ status: u.export ? u.export.status : '', error: u.error, code: u.status,
        stopped: u.stopped, settled: u.settled, html: X.html(u.export, u) })
    });
    for (let i = 0; i < 40; i++) {
      await flush(); await flush();
      if (sc.stopAfter && out.requests.length >= sc.stopAfter) { w.stop(); }
      if (!pending) break;
      const fn = pending; pending = null; fn();
    }
    await flush(); await flush();
    out.pendingLeft = !!pending;
    return out;
  }
  (async () => {
    const results = {};
    for (const name of Object.keys(input.scenarios)) results[name] = await run(input.scenarios[name]);
    // The button, lifted from the rail as it renders: with no formats read, and with
    // STEP unavailable on this forged (no kernel), the worker's route is still offered.
    const buttons = new Function('state', ['esc', 'exportButtons'].map((n) => lift(src, n)).join('\n') +
      '; return exportButtons;');
    results.buttons = {
      none: buttons({ formats: [] })('ver_1'),
      noKernel: buttons({ formats: [{ name: 'step', available: false, reason: 'no CAD kernel' }] })('ver_1')
    };
    process.stdout.write(JSON.stringify({ results, interval: X.interval, limit: X.limit }));
  })();
`

type exportReply struct {
	Status int            `json:"status"`
	Body   map[string]any `json:"body"`
}

type exportScenario struct {
	Replies   []exportReply `json:"replies"`
	Limit     int           `json:"limit,omitempty"`
	StopAfter int           `json:"stopAfter,omitempty"`
}

type exportUpdate struct {
	Status, Error, HTML string
	Code                int
	Stopped, Settled    bool
}

type exportRun struct {
	Requests    []string
	Updates     []exportUpdate
	Delays      []int
	PendingLeft bool
}

func exportJob(status string, extra map[string]any) map[string]any {
	e := map[string]any{"id": "exp_1", "version_id": "ver_1", "status": status, "attempts": 1, "max_attempts": 3,
		"filename": "car-v1.step", "status_url": "/v1/geometry/exports/exp_1", "skipped": []string{}, "feature_failures": []string{}}
	for k, v := range extra {
		e[k] = v
	}
	return map[string]any{"export": e}
}

func refusal(message, remedy string) map[string]any {
	return map[string]any{"error": map[string]any{"message": message, "remedy": remedy}}
}

func TestWorkbenchExportsSTEPThroughTheWorkerJob(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping the workbench export-job fence")
	}
	src, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatal(err)
	}
	ok := func(body map[string]any) exportReply { return exportReply{200, body} }
	succeeded := exportJob("succeeded", map[string]any{"size_bytes": 5_400_000, "sha256": "ab12cd34ef567890", "parts": 30023,
		"skipped": []string{"Clip 3"}, "download_url": "/v1/geometry/exports/exp_1/file"})
	scenarios := map[string]exportScenario{
		"viewer": {Replies: []exportReply{{202, exportJob("queued", nil)}, ok(exportJob("queued", nil)),
			ok(exportJob("running", nil)), ok(succeeded)}},
		"failed": {Replies: []exportReply{ok(exportJob("running", nil)), {500, refusal("database unavailable", "")},
			ok(exportJob("failed", map[string]any{"reason": "the kernel ran out of memory"}))}},
		"unreadable":  {Replies: []exportReply{{404, refusal("no variant ver_1", "")}}},
		"tooMany":     {Replies: []exportReply{{429, refusal("you have 3 export jobs waiting or running", "when one finishes, ask again")}}},
		"noBucket":    {Replies: []exportReply{{503, refusal("no blob storage is configured", "set FORGE_BLOB_BUCKET")}}},
		"removed":     {Replies: []exportReply{{202, exportJob("queued", nil)}, {403, refusal("not permitted", "")}}},
		"neverStarts": {Replies: []exportReply{{202, exportJob("queued", nil)}, ok(exportJob("queued", nil))}, Limit: 3},
		"closed":      {Replies: []exportReply{{202, exportJob("queued", nil)}, ok(exportJob("running", nil))}, StopAfter: 2},
		// As forged answers a design past the job's ceiling: the general words of the code in
		// message and remedy, the sentence about THIS refusal in details.detail.
		"tooBig": {Replies: []exportReply{{400, map[string]any{"error": map[string]any{
			"message": "One or more request fields failed validation.",
			"remedy":  "Correct the fields named in the details array and resubmit.",
			"details": map[string]any{"detail": "This design places more than 90000 parts, the most FORGE writes as STEP in an export job"}}}}}},
	}
	dir := t.TempDir()
	asset, harness, input := filepath.Join(dir, "workbench.js"), filepath.Join(dir, "run.js"), filepath.Join(dir, "in.json")
	body, _ := json.Marshal(map[string]any{"scenarios": scenarios})
	for path, data := range map[string][]byte{asset: src, harness: []byte(exportJobHarness), input: body} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(node, harness, asset, input)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the workbench could not be driven: %v %s", err, stderr.String())
	}
	var got struct {
		Missing bool
		Results struct {
			Viewer, Failed, Unreadable, TooMany, NoBucket, Removed, NeverStarts, Closed, TooBig exportRun
			Buttons                                                                             struct{ None, NoKernel string }
		}
		Interval, Limit int
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got.Missing {
		t.Fatal("workbench.js exports no ForgeExportJob: nothing in the page reaches the off-node STEP export")
	}
	r := got.Results
	post := "POST /v1/geometry/ver_1/exports?format=step"
	status := "GET /v1/geometry/exports/exp_1"

	// The whole path, as a viewer takes it: one request to the export route, then its
	// status until it settles, then nothing.
	v := r.Viewer
	if strings.Join(v.Requests, " | ") != strings.Join([]string{post, status, status, status}, " | ") {
		t.Fatalf("a STEP job asked %v; want the export route once, then its status until it settled", v.Requests)
	}
	for _, q := range append(append([]string{}, v.Requests...), r.Failed.Requests...) {
		if strings.Contains(q, "/v1/goals") {
			t.Fatalf("the export asked %q; a viewer is refused POST /v1/goals, and exporting is reading (PR #123)", q)
		}
	}
	if v.PendingLeft || len(v.Updates) != 4 || v.Updates[3].Status != "succeeded" || !v.Updates[3].Settled {
		t.Fatalf("the watcher ended with %d updates, last %+v, still polling %v", len(v.Updates), v.Updates[len(v.Updates)-1], v.PendingLeft)
	}
	for _, d := range v.Delays {
		if d != got.Interval || d < 1000 {
			t.Errorf("the status was read again after %d ms; the watcher's interval is %d", d, got.Interval)
		}
	}
	if got.Limit*got.Interval > 60*60*1000 || got.Limit < 1 {
		t.Errorf("the watcher reads for up to %d × %d ms; it must stop within an hour", got.Limit, got.Interval)
	}
	if !strings.Contains(v.Updates[0].HTML, "queued") || !strings.Contains(v.Updates[0].HTML, "worker is running") {
		t.Errorf("a queued job reads %q; it must say it waits for a worker", v.Updates[0].HTML)
	}
	if !strings.Contains(v.Updates[2].HTML, "writing the STEP file") {
		t.Errorf("a running job reads %q", v.Updates[2].HTML)
	}
	done := v.Updates[3].HTML
	link := strings.Index(done, `href="/v1/geometry/exports/exp_1/file"`)
	check := strings.Index(done, "no interference check ran for this file")
	clip := strings.Index(done, "Clip 3")
	if link < 0 || check < 0 || clip < 0 || check > link || clip > link {
		t.Fatalf("a finished job reads %q; the label (no interference check, the part left out) must come before the download link", done)
	}
	if strings.Contains(v.Updates[0].HTML, "href=") || strings.Contains(v.Updates[2].HTML, "href=") {
		t.Error("a download link was offered before the job had stored a file")
	}

	// A failure on a read is retried; a failed job is told with its reason and stops.
	f := r.Failed
	if len(f.Requests) != 3 || f.PendingLeft || f.Updates[len(f.Updates)-1].Status != "failed" ||
		!strings.Contains(f.Updates[len(f.Updates)-1].HTML, "the kernel ran out of memory") {
		t.Fatalf("a job that failed after a 500 asked %v and ended %+v (still polling %v)", f.Requests, f.Updates, f.PendingLeft)
	}

	// Refusals are shown in the server's words, with the remedy, and never retried.
	for name, run := range map[string]struct {
		run  exportRun
		want []string
	}{
		"a design the caller cannot read":        {r.Unreadable, []string{"no variant ver_1"}},
		"past the per-requester job limit":       {r.TooMany, []string{"3 export jobs", "when one finishes, ask again"}},
		"a deployment with no blob storage":      {r.NoBucket, []string{"no blob storage", "FORGE_BLOB_BUCKET"}},
		"a status the caller may no longer read": {r.Removed, []string{"not permitted"}},
	} {
		last := run.run.Updates
		if len(last) == 0 || run.run.PendingLeft {
			t.Errorf("%s: no update shown, or still polling (%v)", name, run.run.Requests)
			continue
		}
		html := last[len(last)-1].HTML
		for _, w := range run.want {
			if !strings.Contains(html, w) {
				t.Errorf("%s: the panel reads %q, without %q", name, html, w)
			}
		}
	}
	if len(r.Unreadable.Requests) != 1 || len(r.TooMany.Requests) != 1 || len(r.NoBucket.Requests) != 1 || len(r.Removed.Requests) != 2 {
		t.Errorf("a refusal was asked again: %v, %v, %v, %v", r.Unreadable.Requests, r.TooMany.Requests, r.NoBucket.Requests, r.Removed.Requests)
	}

	// A refusal with a detail written for it shows that detail, not the code's general words.
	// Seen in the browser: a million-part design read "One or more request fields failed validation".
	if u := r.TooBig.Updates; len(u) != 1 || !strings.Contains(u[0].HTML, "more than 90000 parts") ||
		strings.Contains(u[0].HTML, "Correct the fields named") || len(r.TooBig.Requests) != 1 {
		t.Errorf("a design past the job's ceiling was refused and the panel reads %+v", u)
	}

	// A job that never starts is not read forever, and closing the panel stops the reading.
	ns := r.NeverStarts
	if len(ns.Requests) != 1+3 || ns.PendingLeft || !ns.Updates[len(ns.Updates)-1].Stopped ||
		!strings.Contains(ns.Updates[len(ns.Updates)-1].HTML, "Check again") {
		t.Errorf("a job queued forever was read %d times (limit 3) and ended %+v", len(ns.Requests)-1, ns.Updates[len(ns.Updates)-1])
	}
	if len(r.Closed.Requests) != 2 || r.Closed.PendingLeft {
		t.Errorf("after stop, the watcher asked %v and is still polling %v", r.Closed.Requests, r.Closed.PendingLeft)
	}

	for name, html := range map[string]string{"no formats read": r.Buttons.None, "STEP unavailable on forged": r.Buttons.NoKernel} {
		if !strings.Contains(html, `data-export-job="ver_1"`) {
			t.Errorf("with %s, the rail offers no STEP via the worker: %q", name, html)
		}
	}
	js := codeOnly(string(src))
	if !strings.Contains(js, "throw new Error(refusalText(e, r.status, 'Export refused'));") {
		t.Error("the request-path export label does not show a refusal's own detail (refusalText)")
	}
	if !strings.Contains(js, "querySelectorAll('[data-export-job]')") || !strings.Contains(js, "toggleExportJob(b.getAttribute('data-export-job'))") {
		t.Error("the rail's STEP-via-worker button is bound to nothing")
	}
}
