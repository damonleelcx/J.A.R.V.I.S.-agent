/* One turn against Zoo's ML copilot websocket.
 *
 * This is the only route that generates geometry: the REST generation endpoint
 * was removed (see README), and POST /ml/kcl/completions returns an empty
 * completion list on this account for every prompt shape tried.
 *
 * Usage:
 *   npm i ws
 *   ZOO_API_KEY=... node zoo-copilot.mjs --prompt "..." [--out DIR] [--dry-run]
 *                                        [--timeout MS] [--conversation UUID]
 *
 * --conversation resumes an existing thread, so a follow-up ("export it as
 * STEP") does not pay for the model to be rebuilt from scratch.
 *
 * Prints every server message it receives, because the point of a spike is that
 * the conclusion can be re-derived from the transcript rather than taken on
 * trust. The raw transcript is written next to any artefacts.
 */
import fs from "node:fs";
import path from "node:path";

const args = process.argv.slice(2);
const arg = (n, d) => { const i = args.indexOf(n); return i >= 0 ? args[i + 1] : d; };
const has = (n) => args.includes(n);

const KEY = process.env.ZOO_API_KEY;
const PROMPT = arg("--prompt", "A motor mount bracket for a NEMA 17 stepper: flat base plate, cylindrical boss, two stiffening ribs.");
const OUT = arg("--out", "./out");
const TIMEOUT_MS = Number(arg("--timeout", "180000"));
const CONVERSATION = arg("--conversation", "");
const URL = "wss://api.zoo.dev/ws/ml/copilot" +
  (CONVERSATION ? `?conversation_id=${encodeURIComponent(CONVERSATION)}` : "");

if (has("--dry-run")) {
  console.log("DRY RUN — nothing sent, nothing billed");
  console.log("  url    :", URL);
  console.log("  auth   :", KEY ? `Bearer <${KEY.length} chars>` : "MISSING ZOO_API_KEY");
  console.log("  prompt :", PROMPT);
  console.log("  convo  :", CONVERSATION || "(new)");
  console.log("  out    :", path.resolve(OUT));
  console.log("  would send: {type:'user', content:<prompt>, source_ranges:[]}");
  process.exit(0);
}
if (!KEY) { console.error("ZOO_API_KEY is not set"); process.exit(2); }

/* `ws`, not the global WebSocket. Auth is an Authorization HEADER and the
 * WebSocket API has no header hook — see the note on the constructor below.
 * Imported after --dry-run so the dry run works with nothing installed. */
let WebSocketImpl;
try {
  ({ default: WebSocketImpl } = await import("ws"));
} catch {
  console.error("this needs the `ws` package:  npm i ws");
  console.error("(Node's global WebSocket cannot set an Authorization header, and Zoo requires one)");
  process.exit(2);
}

fs.mkdirSync(OUT, { recursive: true });
const transcript = [];
const record = (dir, msg) => {
  transcript.push({ dir, at: Date.now(), msg });
  const t = typeof msg === "string" ? msg : JSON.stringify(msg);
  console.log(`${dir === "in" ? "<--" : "-->"} ${t.slice(0, 400)}${t.length > 400 ? " …" : ""}`);
};

/* Auth is an Authorization HEADER, which is why this needs the `ws` package
 * (npm i ws) rather than Node's global WebSocket.
 *
 * Three forms were probed against the live endpoint (all returned 101, so the
 * socket opens either way and a failure here is silent unless you read the
 * frames):
 *
 *   Authorization header          works, but the browser WebSocket API has no
 *                                 header hook, so a web client cannot use it
 *   ?token= query parameter       refused
 *   Sec-WebSocket-Protocol        refused
 *
 * The refusals say, verbatim: "Please send `{ headers: { Authorization:
 * "Bearer <token>" } }` over this websocket." That matches the spec's
 * MlCopilotClientMessage `headers` variant, which requires both `type` and
 * `headers`. */
const ws = new WebSocketImpl(URL, { headers: { Authorization: `Bearer ${KEY}` } });
const started = Date.now();
let done = false;

const finish = (why, code) => {
  if (done) return;
  done = true;
  fs.writeFileSync(path.join(OUT, "transcript.json"),
    JSON.stringify({ url: URL, prompt: PROMPT, started, ended: Date.now(), why, transcript }, null, 1));
  console.log(`\n[${why}] ${Date.now() - started}ms · transcript -> ${path.join(OUT, "transcript.json")}`);
  try { ws.close(); } catch {}
  process.exit(code);
};

setTimeout(() => finish("timeout", 3), TIMEOUT_MS);

let authed = false;

ws.on("open", () => {
  console.log("connected");
  // The prompt is sent on `session_data`, not here: the server has to finish
  // establishing the session first.
});

function sendPrompt() {
  if (authed) return;
  authed = true;
  const msg = { type: "user", content: PROMPT, source_ranges: [] };
  record("out", msg);
  ws.send(JSON.stringify(msg));
}

ws.on("message", (raw) => {
  let msg;
  try { msg = JSON.parse(raw.toString()); } catch { msg = raw.toString(); }
  record("in", msg);

  if (msg?.session_data || msg?.conversation_id) sendPrompt();

  // Anything that looks like a file lands on disk, whatever the wrapper is
  // called: the point of the run is the artefact.
  /* Artefacts arrive in TWO different shapes and neither is obvious.
   *
   *   tool_output.result.outputs   { "main.kcl": "<text>" } — source, plain text
   *   files                        [ { name, mimetype, data: [byte, …] } ] —
   *                                renders, as a JSON ARRAY OF BYTES, not base64
   *
   * The first live run read `tool_output.outputs` (one level too shallow) and
   * saved nothing at all while the KCL sat in the frame; the renders were missed
   * entirely because a byte array is not a string. Both are handled explicitly
   * rather than guessed at. */
  const save = (name, buf) => {
    const p = path.join(OUT, path.basename(name).replace(/[^\w.\- ]/g, "_"));
    fs.writeFileSync(p, buf);
    console.log(`    saved ${p} (${buf.length} bytes)`);
  };
  const outputs = msg?.tool_output?.result?.outputs || msg?.outputs;
  if (outputs && typeof outputs === "object" && !Array.isArray(outputs)) {
    for (const [name, val] of Object.entries(outputs)) {
      if (typeof val !== "string") continue;
      // Source files are plain text; anything binary would be base64.
      save(name, /\.(kcl|txt|json|md)$/i.test(name) ? Buffer.from(val, "utf8") : Buffer.from(val, "base64"));
    }
  }
  const fileList = msg?.files;
  if (Array.isArray(fileList)) {
    for (const f of fileList) {
      if (!f?.name) continue;
      if (Array.isArray(f.data)) save(f.name, Buffer.from(f.data));
      else if (typeof f.data === "string") save(f.name, Buffer.from(f.data, "base64"));
    }
  }
  if (msg?.error || msg?.access_denied) finish("refused", 4);
  // The server signals the end of a turn by closing the delta stream; several
  // shapes are accepted because the exact terminator is what this run is here
  // to discover, and guessing one and hanging would waste the billed call.
  if (msg?.end_of_stream || msg?.tool_output?.result === "done" ||
      msg?.delta?.end_of_stream || msg?.info?.text === "done") {
    finish("complete", 0);
  }
});

ws.on("error", (e) => { console.error("socket error:", e.message || e); finish("socket-error", 5); });
ws.on("close", (code, reason) => {
  console.log(`closed code=${code} reason=${reason?.toString() || "-"}`);
  finish("closed", code === 1000 ? 0 : 6);
});
