// The two stored designs of the workbench check, from scripts/viewport-car.js.
//   node designs.js <out-dir>
// car30k.json: carDocument(30000), as #93 stored it, with what it does not establish
// (geometry cannot be stored without it, VIS-06).
// fleet1m.json: 34 of those cars in a row, 1,020,782 occurrences. carDocument(1000000)
// cannot be stored — its seam row is one pattern of 4,952 copies and a pattern places
// at most 512 — so the million is reached one level up, the way #89's barrel was.
const fs = require('fs'), path = require('path');
const car = require(path.join(__dirname, '..', '..', '..', '..', 'scripts', 'viewport-car.js'));
const out = process.argv[2];
const notVerified = ['a generated test design: nothing about it has been analysed or checked'];
const one = car.carDocument(30000);
one.not_verified = notVerified;
fs.writeFileSync(path.join(out, 'car30k.json'), JSON.stringify(one));
const fleet = JSON.parse(JSON.stringify(one));
fleet.name = 'Fleet of 34 cars';
fleet.root = 'fleet';
fleet.assemblies.push({ id: 'fleet', children: [{ id: 'car', ref: one.root, name: 'Car',
  pattern: { kind: 'linear', count: 34, offset: [0, 0, 2400] } }] });
fs.writeFileSync(path.join(out, 'fleet1m.json'), JSON.stringify(fleet));
