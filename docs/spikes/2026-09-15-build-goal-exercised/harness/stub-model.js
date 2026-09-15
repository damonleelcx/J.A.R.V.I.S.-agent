// A stand-in model for the graceful-stop drill, so the stop and the resume cost no live
// tokens. Shapes are the ones the live lamp run's calls were answered with (rec/180544-*):
//   - a look (a request carrying an image) -> {"problems": []}
//   - a build step ("Step N of M — Name: what") -> {"speech", "prototype"} holding one box
//     per step so far, 100 mm apart so the kernel's interference check has nothing to repair
// Usage is reported (synthetic: 1000 prompt + 100 completion) so the goal's charge is visible.
// HOLD_STEP's reply is held HOLD_MS, so a stop sent during it lands mid-call; whether the
// client hung up before the reply is logged.
//   node stub-model.js <listen> <log>
const http = require('http'), fs = require('fs');
const [listen, logPath] = process.argv.slice(2);
const HOLD_STEP = +(process.env.HOLD_STEP || 2), HOLD_MS = +(process.env.HOLD_MS || 45000);
let n = 0;
const log = (o) => fs.appendFileSync(logPath, JSON.stringify(Object.assign({ at: new Date().toISOString() }, o)) + '\n');
http.createServer((req, res) => {
  const chunks = [];
  req.on('data', (c) => chunks.push(c));
  req.on('end', () => {
    const id = ++n;
    let j = {};
    try { j = JSON.parse(Buffer.concat(chunks).toString('utf8')); } catch (e) {}
    const msgs = j.messages || [];
    const text = JSON.stringify(msgs);
    const isLook = text.indexOf('"image_url"') >= 0;
    const last = msgs.length ? msgs[msgs.length - 1].content : '';
    const lastText = typeof last === 'string' ? last : JSON.stringify(last);
    const m = /Step (\d+) of (\d+) \S+ ([^:]+):/.exec(lastText);
    let content, hold = 0, step = null;
    if (isLook) {
      content = '{"problems": []}';
    } else if (m) {
      step = +m[1];
      const parts = [];
      for (let k = 1; k <= step; k++) {
        parts.push({ id: 'step-' + k, name: k === step ? m[3].trim() : 'Step ' + k, shape: 'box',
          size: { width: 40, height: 20, depth: 40 }, position: [(k - 1) * 100, 10, 0], rotation: [0, 0, 0],
          color: '#8899aa', note: 'stub model: step ' + k });
      }
      content = JSON.stringify({ speech: 'Stub model: built step ' + step + '.', prototype: { name: 'Stub drill model', units: 'mm', parts } });
      if (step === HOLD_STEP) hold = HOLD_MS;
    } else {
      content = '{"speech": "stub model: nothing to do"}';
    }
    let closed = false;
    res.on('close', () => { if (!res.writableFinished) { closed = true; log({ id, event: 'client_hung_up_before_reply', step, look: isLook }); } });
    log({ id, event: 'request', model: j.model, step, look: isLook, hold_ms: hold });
    setTimeout(() => {
      if (closed) { log({ id, event: 'reply_not_sent', step }); return; }
      const body = JSON.stringify({ id: 'stub-' + id, object: 'chat.completion', model: j.model,
        choices: [{ index: 0, message: { role: 'assistant', content }, finish_reason: 'stop' }],
        usage: { prompt_tokens: 1000, completion_tokens: 100, total_tokens: 1100 } });
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(body);
      log({ id, event: 'reply', step, look: isLook });
    }, hold);
  });
}).listen(+listen, '127.0.0.1', () => console.log('stub model on ' + listen));
