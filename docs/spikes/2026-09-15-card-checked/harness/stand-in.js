// A local stand-in for the OpenAI-compatible model endpoint, so the proposal card can be
// watched over a real build without spending a token. Every reply is scripted:
//   - a streamed conversation turn -> a speech + proposed_goal reply (a desk lamp, 3 steps at most)
//     ... unless the person's message contains "shape check": then a reply with a proposed_goal and
//     NO speech or detail, which Reply.validate refuses ("nothing to say or show")
//   - "Plan the build of: ..." -> 3 steps (Base, Arm, Shade)
//   - "Step N of M — Name: ..." -> a whole document, one box per step so far, 100 mm apart. Step 2's
//     FIRST reply also carries a cut naming a part that does not exist, so repairIfFaulty runs once.
//     The document's name changes per step, to see which name the artifact keeps.
//   - "This document has parts that cannot be built" (a repair) -> the same document without the cut
//   - a request carrying an image (a look) -> {"problems": []}
// Usage is reported per kind so the charges can be told apart on the card and in Telemetry.
//   node stand-in.js <port> <log.jsonl>
const http = require('http'), fs = require('fs');
const [port, logPath] = process.argv.slice(2);
let n = 0;
const faultySent = {};
const log = (o) => fs.appendFileSync(logPath, JSON.stringify(Object.assign({ at: new Date().toISOString() }, o)) + '\n');

const USAGE = { converse: [3000, 200], unusable: [2800, 150], plan: [350, 50], step: [1000, 100],
  repair: [700, 80], look: [900, 20], other: [100, 10] };

const STEPS = [
  { name: 'Base', what: 'A round weighted base, 120 mm across, that everything else stands on.' },
  { name: 'Arm', what: 'A two-segment arm with a hinge, rising from the back of the base.' },
  { name: 'Shade', what: 'A conical shade on the end of the arm.' },
];
const DOC_NAMES = ['Desk lamp base', 'Desk lamp with arm', 'Desk lamp'];

function docFor(step, faulty) {
  const parts = [];
  for (let k = 1; k <= step; k++) {
    parts.push({ id: 'lamp-' + STEPS[k - 1].name.toLowerCase(), name: STEPS[k - 1].name, shape: 'box',
      size: { width: 40, height: 20, depth: 40 }, position: [(k - 1) * 100, 10, 0], rotation: [0, 0, 0],
      color: '#8899aa', note: 'stand-in model: step ' + k });
  }
  const doc = { name: DOC_NAMES[step - 1] || 'Desk lamp', units: 'mm', parts };
  if (faulty) {
    doc.features = [{ id: 'bad-cut', op: 'cut', of: 'no-such-part', with: ['lamp-base'], note: 'deliberately faulty (stand-in)' }];
  }
  return doc;
}

const PROPOSAL = {
  speech: 'I can build a small desk lamp as a model: a weighted base, a hinged arm and a conical shade. Shall I start?',
  proposed_goal: {
    title: 'Build a small desk lamp model (stand-in)',
    statement: 'Build a small desk lamp as a model in three steps at most: a round weighted base, a two-segment arm with a hinge, and a conical shade.',
    risk_tier: 'r1',
  },
};

function classify(j) {
  const msgs = j.messages || [];
  const text = JSON.stringify(msgs);
  const users = msgs.filter((m) => m.role === 'user');
  const last = users.length ? users[users.length - 1].content : '';
  const lastText = typeof last === 'string' ? last : JSON.stringify(last);
  if (text.indexOf('"image_url"') >= 0) return { kind: 'look' };
  if (j.stream) return { kind: /shape check/i.test(lastText) ? 'unusable' : 'converse' };
  if (lastText.startsWith('Plan the build of:')) return { kind: 'plan' };
  if (lastText.startsWith('This document has parts that cannot be built')) {
    const m = /"name":"(Desk lamp[^"]*)"/.exec(lastText);
    const step = m ? DOC_NAMES.indexOf(m[1]) + 1 : 2;
    return { kind: 'repair', step: step || 2 };
  }
  const m = /Step (\d+) of (\d+) \S+ ([^:]+):/.exec(lastText);
  if (m) return { kind: 'step', step: +m[1], of: +m[2], name: m[3].trim(), goalKey: lastText.slice(0, 200) };
  return { kind: 'other', head: lastText.slice(0, 160) };
}

http.createServer((req, res) => {
  const chunks = [];
  req.on('data', (c) => chunks.push(c));
  req.on('end', () => {
    const id = ++n;
    let j = {};
    try { j = JSON.parse(Buffer.concat(chunks).toString('utf8')); } catch (e) {}
    if (req.method === 'GET') { // /models, asked when an error is being explained
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ data: [{ id: 'stand-in-converse' }, { id: 'stand-in-vision' }] }));
      log({ id, event: 'get', path: req.url });
      return;
    }
    const c = classify(j);
    let content;
    if (c.kind === 'look') content = '{"problems": []}';
    else if (c.kind === 'converse') content = JSON.stringify(PROPOSAL);
    else if (c.kind === 'unusable') content = JSON.stringify({ proposed_goal: PROPOSAL.proposed_goal });
    else if (c.kind === 'plan') content = JSON.stringify({ steps: STEPS });
    else if (c.kind === 'repair') content = JSON.stringify({ prototype: docFor(c.step, false) });
    else if (c.kind === 'step') {
      // Faulty once per goal's step 2: keyed by the "Building: <asked>" line.
      const faulty = c.step === 2 && !faultySent[c.goalKey];
      if (faulty) faultySent[c.goalKey] = true;
      c.faulty = faulty;
      content = JSON.stringify({ speech: 'Stand-in: built step ' + c.step + '.', prototype: docFor(c.step, faulty) });
    } else content = '{"speech": "stand-in: unscripted request"}';
    const [p, q] = USAGE[c.kind];
    const usage = { prompt_tokens: p, completion_tokens: q, total_tokens: p + q };
    log(Object.assign({ id, event: 'request', model: j.model, stream: !!j.stream, tokens: p + q }, c, { goalKey: undefined }));
    if (j.stream) {
      res.writeHead(200, { 'Content-Type': 'text/event-stream' });
      const frame = (o) => res.write('data: ' + JSON.stringify(o) + '\n\n');
      for (let i = 0; i < content.length; i += 40) {
        frame({ model: j.model, choices: [{ index: 0, delta: { content: content.slice(i, i + 40) }, finish_reason: null }] });
      }
      frame({ model: j.model, choices: [{ index: 0, delta: {}, finish_reason: 'stop' }] });
      frame({ model: j.model, choices: [], usage });
      res.end('data: [DONE]\n\n');
    } else {
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ id: 'stand-in-' + id, object: 'chat.completion', model: j.model,
        choices: [{ index: 0, message: { role: 'assistant', content }, finish_reason: 'stop' }], usage }));
    }
  });
}).listen(+port, '127.0.0.1', () => console.log('stand-in model on ' + port));
