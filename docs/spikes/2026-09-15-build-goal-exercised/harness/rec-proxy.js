// Recording proxy between forged and the model endpoint, to see what a workbench turn
// was actually sent and answered. Logs request shape and response BODIES to rec/, never
// any header (the Authorization header is forwarded untouched and never written).
//
// ‼️ forged sends enable_thinking=false only to hosts under aliyuncs.com (llm/deliberation.go),
// so pointed at 127.0.0.1 it would let the conversation model deliberate. To keep the
// request what forged sends the real host, this adds enable_thinking=false to requests for
// the latency-bound models (converse qwen3.7-plus; vision qwen3.8-max is not used here)
// when the field is absent, and records that it did.
//   node rec-proxy.js <listen> <outdir>
const http = require('http'), https = require('https'), fs = require('fs'), path = require('path'), zlib = require('zlib');
const [listen, outdir] = process.argv.slice(2);
const UP = 'token-plan.cn-beijing.maas.aliyuncs.com';
fs.mkdirSync(outdir, { recursive: true });
let n = 0;
// Files are prefixed by this process's start time, so a restarted proxy never overwrites a record.
const RUN = new Date().toISOString().replace(/[-:.TZ]/g, '').slice(8, 14);
http.createServer((req, res) => {
  const id = RUN + '-' + String(++n).padStart(3, '0');
  const chunks = [];
  req.on('data', (c) => chunks.push(c));
  req.on('end', () => {
    let body = Buffer.concat(chunks);
    let meta = { id, method: req.method, url: req.url, at: new Date().toISOString() };
    try {
      const j = JSON.parse(body.toString('utf8'));
      // Latency-bound roles only: converse (qwen3.7-plus) and vision (qwen3.8-max WITH an image).
      // The planner is qwen3.8-max without images and must keep deliberating.
      const hasImage = JSON.stringify(j.messages || []).indexOf('"image_url"') >= 0;
      if ((j.model === 'qwen3.7-plus' || (j.model === 'qwen3.8-max' && hasImage)) && !('enable_thinking' in j)) {
        j.enable_thinking = false; meta.injected_enable_thinking = false;
      }
      meta.has_image = hasImage;
      meta.model = j.model; meta.stream = j.stream; meta.max_tokens = j.max_tokens;
      meta.response_format = j.response_format; meta.messages = (j.messages || []).length;
      meta.request_bytes = body.length;
      fs.writeFileSync(path.join(outdir, id + '-request.json'), JSON.stringify(j, null, 1));
      body = Buffer.from(JSON.stringify(j));
    } catch (e) { meta.unparsed_request = true; }
    const headers = Object.assign({}, req.headers, { host: UP, 'content-length': body.length });
    const t0 = Date.now();
    const up = https.request({ host: UP, port: 443, method: req.method, path: req.url, headers }, (r) => {
      const out = [];
      res.writeHead(r.statusCode, r.headers);
      r.on('data', (c) => { out.push(c); res.write(c); });
      r.on('end', () => {
        res.end();
        // The client asked for compression (Go sends Accept-Encoding: gzip), so the recorded copy is
        // decompressed here; what was forwarded is untouched.
        let buf = Buffer.concat(out);
        const enc = (r.headers['content-encoding'] || '').toLowerCase();
        try { if (enc === 'gzip') buf = zlib.gunzipSync(buf); else if (enc === 'br') buf = zlib.brotliDecompressSync(buf); else if (enc === 'deflate') buf = zlib.inflateSync(buf); } catch (e) { meta.decompress_error = String(e); }
        meta.encoding = enc || null;
        const raw = buf.toString('utf8');
        meta.status = r.statusCode; meta.ms = Date.now() - t0;
        // Assemble a streamed reply's content and usage.
        let content = '', reasoning = 0, usage = null, finish = null;
        raw.split('\n').forEach((line) => {
          if (!line.startsWith('data: ') || line === 'data: [DONE]') return;
          try {
            const f = JSON.parse(line.slice(6));
            if (f.usage) usage = f.usage;
            (f.choices || []).forEach((ch) => {
              const d = ch.delta || {};
              if (d.content) content += d.content;
              if (d.reasoning_content) reasoning += d.reasoning_content.length;
              if (ch.finish_reason) finish = ch.finish_reason;
            });
          } catch (e) {}
        });
        if (!meta.stream) { try { const j = JSON.parse(raw); usage = j.usage; content = ((j.choices || [])[0] || {}).message ? j.choices[0].message.content : ''; finish = (j.choices || [{}])[0].finish_reason; } catch (e) {} }
        meta.usage = usage; meta.finish = finish; meta.reasoning_chars = reasoning; meta.content_chars = content.length;
        fs.writeFileSync(path.join(outdir, id + '-content.txt'), content);
        fs.appendFileSync(path.join(outdir, 'calls.jsonl'), JSON.stringify(meta) + '\n');
      });
    });
    up.on('error', (e) => { meta.error = String(e); fs.appendFileSync(path.join(outdir, 'calls.jsonl'), JSON.stringify(meta) + '\n'); res.writeHead(502); res.end(String(e)); });
    up.end(body);
  });
}).listen(+listen, '127.0.0.1', () => console.log('recording proxy on ' + listen));
