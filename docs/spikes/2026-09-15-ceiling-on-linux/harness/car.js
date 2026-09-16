const fs = require('fs');
const { carDocument } = require('/src/head/scripts/viewport-car.js');
for (const n of [4096, 8192, 16384, 30000]) {
  const d = carDocument(n);
  d.not_verified = (d.not_verified || []).concat(['a capacity measurement fixture (docs/spikes/2026-09-15-ceiling-on-linux), not a verified design']);
  fs.writeFileSync('/w/docs/car-' + n + '.json', JSON.stringify(d));
}
