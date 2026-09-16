// Local exercise proxy: forwards 127.0.0.1:<listen> to forged on <upstream> and adds the
// test user's session as a Bearer header, so the browser pane never holds a credential.
//   node proxy.js <listen> <upstream> <signin.json>
const http = require('http'), fs = require('fs');
const [listen, upstream, signin] = process.argv.slice(2);
const token = JSON.parse(fs.readFileSync(signin, 'utf8')).token;
http.createServer((req, res) => {
  const headers = Object.assign({}, req.headers, { authorization: 'Bearer ' + token, host: '127.0.0.1:' + upstream });
  delete headers.cookie;
  const up = http.request({ host: '127.0.0.1', port: +upstream, method: req.method, path: req.url, headers }, (r) => {
    res.writeHead(r.statusCode, r.headers);
    r.pipe(res);
  });
  up.on('error', (e) => { res.writeHead(502); res.end(String(e)); });
  req.pipe(up);
}).listen(+listen, '127.0.0.1', () => console.log('proxy on ' + listen + ' -> ' + upstream));
