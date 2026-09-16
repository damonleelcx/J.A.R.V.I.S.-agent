#!/usr/bin/env node
/* Does a 30,000-occurrence car load in the viewport as one draw call per definition?
 *
 * Phase 6, stage W1. Before it, every placed part was its own buffers and its own
 * drawElements, and the viewport refused anything over 4096 parts, so the car this
 * loads was not drawn at all. This loads scripts/viewport-car.js into the SHIPPED
 * Studio through scripts/webgl-stub.js — WebGL2, WebGL1 with ANGLE_instanced_arrays,
 * and WebGL1 without it — and checks what each frame sends.
 *
 *   make test-viewport   (or: node scripts/viewport-instancing-check.js)
 *
 * ‼️ What the numbers printed here are, and are not.
 *
 * The times are CPU milliseconds in node against a context that draws nothing: they
 * cover expanding the tree, grouping it into batches, building each shape's triangles
 * once, culling and writing the instance buffers. They are NOT a frame time. No GPU is
 * involved, no shader is compiled, and nothing here can say how long Chrome takes to
 * put the frame on screen. The browser run is recorded separately, in
 * docs/spikes/2026-09-15-instanced-viewport.
 */
'use strict';
const path = require('path');
const { performance } = require('perf_hooks');
const stub = require('./webgl-stub.js');
const { carDocument } = require('./viewport-car.js');

const f3Path = process.argv[2] || path.join(__dirname, '..', 'internal', 'httpapi', 'assets', 'forge3d.js');
const F = stub.loadForge3D(f3Path);

let failed = 0;
function check(name, ok, detail) {
  if (ok) { console.log('  PASS  ' + name); return; }
  console.error('  FAIL  ' + name + (detail ? '\n        ' + detail : ''));
  failed = 1;
}

const car = carDocument(30000);
const occurrences = F.partsToDraw(car).length;
check('the car places at least 30,000 parts and is not refused',
  occurrences >= 30000 && !F.drawRefusal(car), occurrences + ' parts, refusal: ' + JSON.stringify(F.drawRefusal(car)));

const rows = [];
for (const mode of ['webgl2', 'webgl1', 'webgl1-noext']) {
  const made = stub.makeCanvas(mode, 1280, 720);
  let refusal = '';
  const studio = new F.Studio(made.canvas, { onError: (m) => { refusal = m; } });

  let t0 = performance.now();
  const loaded = studio.load(car);
  const loadMs = performance.now() - t0;

  made.record.reset();
  t0 = performance.now();
  studio.draw();
  const drawMs = performance.now() - t0;
  const s = studio.stats;

  /* An orbit: ten frames turning the camera, which is what a person does first. */
  const orbit = [];
  for (let i = 0; i < 10; i++) {
    studio.camera.yaw += 0.2;
    made.record.reset();
    const t = performance.now();
    studio.draw();
    orbit.push(performance.now() - t);
  }
  const orbitMs = orbit.reduce((a, b) => a + b, 0) / orbit.length;

  console.log('\n' + mode + ' (' + studio.renderPath + ')');
  check('nothing was refused and every part was loaded', !refusal && loaded === occurrences,
    'loaded ' + loaded + ' of ' + occurrences + '; ' + refusal);
  check('the frame did nothing a browser refuses', made.record.problems.length === 0,
    made.record.problems.join('; '));
  check('every part is drawn, culled or hidden, once',
    s.instances + s.culled + s.hidden === occurrences, JSON.stringify(s));
  check('one batch per definition (' + car.definitions.length + ')', s.batches === car.definitions.length,
    s.batches + ' batches');
  if (mode === 'webgl1-noext') {
    check('without the extension, one call per copy drawn', s.drawCalls === s.instances, JSON.stringify(s));
  } else {
    const opaque = s.drawCalls - (s.translucent ? made.record.draws.filter((d) =>
      d.instances.length && d.instances[0].colour[3] < 1).length : 0);
    /* Six since Phase 6, stage W2: whole, simplified or box, each wound either way. */
    check('at most six opaque calls per definition (winding × three levels of detail)',
      opaque <= 6 * s.batches, opaque + ' opaque calls for ' + s.batches + ' batches');
  }
  rows.push({ mode: mode, loadMs: loadMs, drawMs: drawMs, orbitMs: orbitMs, stats: s,
              uploadMB: made.record.uploadedBytes / 1048576 });
}

/* One past the viewport's ceiling is refused whole, and said. */
{
  const made = stub.makeCanvas('webgl2');
  let said = '';
  const studio = new F.Studio(made.canvas, { onError: (m) => { said = m; } });
  const over = carDocument(100001);
  console.log('');
  check('a design past 100,000 parts draws nothing and says why',
    studio.load(over) === 0 && /more than 100000 parts/.test(said), 'said: ' + JSON.stringify(said));
}

console.log('\nMeasured (CPU in node against a stub context — NOT a GPU frame time):');
console.log('  mode           load ms  first draw ms  orbit draw ms  calls  instances  culled  as boxes  uploaded MB/frame');
rows.forEach((r) => {
  const s = r.stats;
  console.log('  ' + r.mode.padEnd(13) + r.loadMs.toFixed(0).padStart(8) + r.drawMs.toFixed(1).padStart(15) +
    r.orbitMs.toFixed(1).padStart(15) + String(s.drawCalls).padStart(7) + String(s.instances).padStart(11) +
    String(s.culled).padStart(8) + String(s.proxied).padStart(10) + r.uploadMB.toFixed(2).padStart(19));
});

if (failed) {
  console.error('\nThe viewport does not draw a 30k-occurrence car as one call per definition.');
  process.exit(1);
}
console.log('\nA ' + occurrences + '-occurrence car loads and draws as ' + car.definitions.length + ' batches.');
