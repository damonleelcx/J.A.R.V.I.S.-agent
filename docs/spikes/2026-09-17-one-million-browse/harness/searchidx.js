// node --expose-gc searchidx.js <worktree> : the browsed search, walk vs index, on the 1M fleet and the 30k car
const path = require('path'), fs = require('fs');
const W = process.argv[2];
const stub = require(path.join(W, 'scripts', 'webgl-stub.js'));
const F = stub.loadForge3D(path.join(W, 'internal', 'httpapi', 'assets', 'forge3d.js'));
const S = path.join(__dirname, '..', 'vpcheck');
const fleet = JSON.parse(fs.readFileSync(path.join(S, 'fleet1m.json'), 'utf8'));
const car = JSON.parse(fs.readFileSync(path.join(S, 'car30k.json'), 'utf8'));
const ms = (t) => (Number(process.hrtime.bigint() - t) / 1e6).toFixed(1);
for (const [name, spec] of [['car30k', car], ['fleet1m', fleet]]) {
  global.gc(); const h0 = process.memoryUsage().heapUsed;
  let t = process.hrtime.bigint();
  const ix = F.treeSearchIndex(spec);
  const build = ms(t);
  global.gc(); const h1 = process.memoryUsage().heapUsed;
  console.log(name, 'index lines', ix.lines, 'chars', ix.text.length, 'build', build + 'ms', 'heap +' + ((h1 - h0) / 1e6).toFixed(1) + 'MB');
  for (const q of ['rivet', 'car-7/seam-12/rivet-1', 'seam 3', 'wheel', 'x', 'r', 'zzz', 'Car 7 / Seam']) {
    t = process.hrtime.bigint(); const a = F.searchTree(spec, q, 50); const tw = ms(t);
    t = process.hrtime.bigint(); const b = F.searchTreeIndexed(ix, q, 50); const ti = ms(t);
    console.log(' ', JSON.stringify(q), 'walk', tw + 'ms', 'index', ti + 'ms', 'total', a.total, b.total,
      'same', JSON.stringify(a) === JSON.stringify(b));
  }
}
