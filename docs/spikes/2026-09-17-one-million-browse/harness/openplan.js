// node openplan.js <worktree> : what opening rows of the fleet and the car asks for
const path = require('path'), fs = require('fs');
const W = process.argv[2];
const stub = require(path.join(W, 'scripts', 'webgl-stub.js'));
const F = stub.loadForge3D(path.join(W, 'internal', 'httpapi', 'assets', 'forge3d.js'));
const S = path.join(__dirname, '..', 'vpcheck');
const fleet = JSON.parse(fs.readFileSync(path.join(S, 'fleet1m.json'), 'utf8'));
const car = JSON.parse(fs.readFileSync(path.join(S, 'car30k.json'), 'utf8'));
console.log('fleet root rows', F.treeChildren(fleet, fleet.root, '').map((r) => r.path + (r.slots ? 'x' + r.slots.length : '')).join(' '));
const c7 = F.treeChildren(fleet, 'car', 'car-7');
console.log('car-7 rows', c7.map((r) => r.path + (r.slots ? 'x' + r.slots.length : '')).join(' '));
for (const [name, spec, p] of [['fleet', fleet, 'car-7'], ['fleet', fleet, 'car'], ['fleet', fleet, 'car-7/seam'],
  ['fleet', fleet, 'car-7/seam-12'], ['car', car, car.root ? F.treeChildren(car, car.root, '')[0].path : '']]) {
  const t = process.hrtime.bigint();
  const plan = F.openPaths(spec, p);
  console.log(name, p, (Number(process.hrtime.bigint() - t) / 1e6).toFixed(1) + 'ms', 'drawn', plan.drawn, 'of', plan.total,
    'requests', plan.paths.length, plan.paths.slice(0, 20).join(' '));
}
console.log('car rows', F.treeChildren(car, car.root, '').map((r) => r.path + (r.slots ? 'x' + r.slots.length : '')).join(' '));
