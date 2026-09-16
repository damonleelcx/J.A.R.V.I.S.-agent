// The Browser pane's origin: forwards 127.0.0.1:<listen> to forged on <upstream>, and
//   1. adds the minted session as a Bearer header, so the PAGE never holds a credential
//      and none was ever typed into a form (see harness/mintsession);
//   2. while the file <ceiling-file> exists, adds "max_tokens": <its number> to a
//      POST /v1/goals JSON body. That is how the budget-stop goal is started from the
//      real card: the card has no ceiling field (the API does), and everything else
//      about the request is the page's.
// Every /v1/goals* request is logged with its time, so polling can be seen to start and stop.
//   node pane-proxy.js <listen> <upstream> <ceiling-file> <log> <session.json>
const http = require('http'), fs = require('fs');
const [listen, upstream, ceilingFile, logPath, sessionFile] = process.argv.slice(2);
const token = JSON.parse(fs.readFileSync(sessionFile, 'utf8')).token;
const log = (s) => fs.appendFileSync(logPath, new Date().toISOString() + ' ' + s + '\n');
http.createServer((req, res) => {
  const chunks = [];
  req.on('data', (c) => chunks.push(c));
  req.on('end', () => {
    let body = Buffer.concat(chunks);
    if (req.method === 'POST' && req.url.split('?')[0] === '/v1/goals' && fs.existsSync(ceilingFile)) {
      try {
        const j = JSON.parse(body.toString('utf8'));
        j.max_tokens = +fs.readFileSync(ceilingFile, 'utf8').trim();
        body = Buffer.from(JSON.stringify(j));
        log('injected max_tokens=' + j.max_tokens);
      } catch (e) { log('could not inject: ' + e); }
    }
    const headers = Object.assign({}, req.headers, {
      host: '127.0.0.1:' + upstream,
      authorization: 'Bearer ' + token,
      'content-length': body.length
    });
    // The page is never given a cookie to hold, so it must never send one: the
    // session lives in this process and nowhere the browser can read it.
    delete headers.cookie;
    const up = http.request({ host: '127.0.0.1', port: +upstream, method: req.method, path: req.url, headers }, (r) => {
      if (req.url.startsWith('/v1/goals') || req.url.startsWith('/v1/converse')) log(req.method + ' ' + req.url + ' ' + r.statusCode);
      res.writeHead(r.statusCode, r.headers);
      r.pipe(res);
    });
    up.on('error', (e) => { res.writeHead(502); res.end(String(e)); });
    up.end(body);
  });
}).listen(+listen, '127.0.0.1', () => console.log('pane proxy on ' + listen + ' -> ' + upstream));
