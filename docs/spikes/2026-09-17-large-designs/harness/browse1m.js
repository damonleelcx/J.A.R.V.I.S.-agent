// node --expose-gc browse1m.js <worktree> : browse readers on the 1M fleet, CPU ms in node
const path = require('path'), fs = require('fs');
const W = process.argv[2];
const stub = require(path.join(W, 'scripts', 'webgl-stub.js'));
const F = stub.loadForge3D(path.join(W, 'internal', 'httpapi', 'assets', 'forge3d.js'));
const fleet = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'vpcheck', 'fleet1m.json'), 'utf8'));
const car = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'vpcheck', 'car30k.json'), 'utf8'));
function time(label, fn) { const t = process.hrtime.bigint(); const r = fn(); console.log(label, Number(process.hrtime.bigint() - t) / 1e6 + 'ms', typeof r === 'object' ? JSON.stringify(r).slice(0, 300) : r); return r; }
console.log('root rows', JSON.stringify(F.treeChildren(fleet, fleet.root, '')).slice(0, 200));
time('occurrencesUnder car', () => F.occurrencesUnder(fleet, 'car'));
time('occurrencesUnder car-3', () => F.occurrencesUnder(fleet, 'car-3'));
const rows = F.treeChildren(fleet, 'car', 'car-3');
console.log('car-3 rows', rows.map(r => r.path + (r.slots ? 'x' + r.slots.length : '')).join(' '));
time('occurrencesUnder car-3/seam-12', () => F.occurrencesUnder(fleet, 'car-3/seam-12'));
const pu = time('partsUnder car-3/seam-12', () => F.partsUnder(fleet, 'car-3/seam-12').length);
time('partsUnder car-3', () => F.partsUnder(fleet, 'car-3').length);
time('searchTree rivet', () => F.searchTree(fleet, 'rivet', 50).total);
time('searchTree car-3/seam-12/rivet-1', () => F.searchTree(fleet, 'car-3/seam-12/rivet-1', 50));
// parity on the 30k car against the drawn scan
const st = { parts: F.partsToDraw(car).map(d => ({ id: d.spec.id, spec: d.spec, repeatOf: d.repeatOf })) };
const S = F.Studio.prototype;
for (const q of ['rivet', 'seam-12/rivet-1', 'Seam 3', 'wheel', 'x']) {
  const a = S.scanOccurrences.call(st, q, 50), b = F.searchTree(car, q, 50);
  console.log('parity', q, a.total, b.total, JSON.stringify(a.found) === JSON.stringify(b.found));
}
for (const p of ['seam', 'seam-12', 'seam-12/rivet-1', 'front-left']) {
  const n = F.partsToDraw(car).filter(d => F.occurrenceMatcher(p)(d.spec.id, d.repeatOf)).length;
  console.log('count parity', p, n, F.occurrencesUnder(car, p), F.partsUnder(car, p).length);
}
