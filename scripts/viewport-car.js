/* A car of at least 30,000 occurrences, written the way D1 lets one be written.
 *
 * Phase 6, stage W1's acceptance is a 30k-occurrence car loaded in the viewport with
 * its frame time recorded. The S0 fence's car (size_fence_integration_test.go) is two
 * definitions — a skin panel and a rivet — which is enough to store and count and too
 * little to draw: one batch says nothing about a draw call per definition. This one
 * has the shape of a car's tree instead: wheels with spokes and lug bolts in polar
 * patterns, corners mirrored left to right, riveted body seams in lines, spot welds and
 * battery cells in grids, a harness laid along a path, bracketed sub-assemblies, seats
 * and glazing that is see-through.
 *
 * Shared by scripts/viewport-instancing-check.js (node, stub GL) and
 * docs/spikes/2026-09-15-instanced-viewport/measure.html (a real browser), so both
 * measure the same document. Plain JavaScript with no dependencies for that reason.
 */
(function (root) {
  'use strict';

  function box(id, name, w, h, d, color, extra) {
    return Object.assign({ id: id, name: name, shape: 'box', size: { width: w, height: h, depth: d },
                           position: [0, 0, 0], rotation: [0, 0, 0], color: color, opacity: 1 }, extra || {});
  }
  function cylinder(id, name, r, h, color, extra) {
    return Object.assign({ id: id, name: name, shape: 'cylinder', size: { radius: r, height: h },
                           position: [0, 0, 0], rotation: [0, 0, 0], color: color, opacity: 1 }, extra || {});
  }

  function carDocument(target) {
    target = target || 30000;
    var definitions = [
      cylinder('rim', 'Rim', 240, 200, '#9aa0a8', { rotation: [0, 0, 90] }),
      cylinder('tyre', 'Tyre', 330, 220, '#2a2a2a', { rotation: [0, 0, 90] }),
      box('spoke', 'Spoke', 18, 200, 12, '#b8bcc4'),
      cylinder('lug', 'Lug bolt', 7, 40, '#d0d0d0', { rotation: [0, 0, 90] }),
      cylinder('disc', 'Brake disc', 160, 28, '#7a7a7a', { rotation: [0, 0, 90] }),
      box('caliper', 'Caliper', 60, 120, 80, '#c0392b'),
      cylinder('damper', 'Damper', 25, 380, '#e0b040'),
      box('panel', 'Body panel', 4400, 2, 180, '#1f5fa8'),
      cylinder('rivet', 'Rivet', 2, 6, '#c8c8c8'),
      { id: 'weld', name: 'Spot weld', shape: 'sphere', size: { radius: 3 }, position: [0, 0, 0],
        rotation: [0, 0, 0], color: '#8a8a8a', opacity: 1 },
      box('cell', 'Battery cell', 21, 70, 21, '#4caf50'),
      cylinder('harness', 'Harness segment', 6, 20, '#ff9800', { rotation: [0, 0, 90] }),
      { id: 'bracket', name: 'Bracket', shape: 'extrusion', size: { depth: 30 }, position: [0, 0, 0],
        rotation: [0, 0, 0], color: '#607d8b', opacity: 1,
        profile: [{ x: 0, y: 0 }, { x: 60, y: 0 }, { x: 60, y: 6 }, { x: 6, y: 6 }, { x: 6, y: 50 }, { x: 0, y: 50 }] },
      cylinder('bolt', 'M8 bolt', 4, 30, '#dddddd'),
      box('seat-frame', 'Seat frame', 500, 60, 520, '#333333'),
      box('cushion', 'Cushion', 480, 120, 500, '#6d4c41'),
      box('clip', 'Trim clip', 12, 8, 20, '#eeeeee'),
      box('glass', 'Window', 900, 500, 5, '#9ecfff', { opacity: 0.35 }),
      { id: 'lamp', name: 'Lamp', shape: 'sphere', size: { radius: 60 }, position: [0, 0, 0],
        rotation: [0, 0, 0], color: '#fff3b0', opacity: 1, mirrored: true },
      cylinder('exhaust', 'Exhaust', 35, 900, '#555555', { rotation: [90, 0, 0] })
    ];
    var others = {
      wheel: { id: 'wheel', children: [
        { id: 'tyre', ref: 'tyre' }, { id: 'rim', ref: 'rim' },
        { id: 'disc', ref: 'disc', position: [-60, 0, 0] },
        { id: 'caliper', ref: 'caliper', position: [-80, 150, 0] },
        { id: 'spoke', ref: 'spoke', position: [110, 110, 0], name: 'Spoke',
          pattern: { kind: 'polar', count: 20, about: 'x' } },
        { id: 'lug', ref: 'lug', position: [120, 60, 0], name: 'Lug',
          pattern: { kind: 'polar', count: 5, about: 'x' } }
      ] },
      corner: { id: 'corner', children: [
        { id: 'wheel', ref: 'wheel' },
        { id: 'damper', ref: 'damper', position: [-200, 350, 0], rotation: [0, 0, 8] }
      ] },
      seam: { id: 'seam', children: [
        { id: 'panel', ref: 'panel' },
        { id: 'rivet', ref: 'rivet', position: [-2190, 4, 0], name: 'Rivet',
          pattern: { kind: 'linear', count: 200, offset: [22, 0, 0] } }
      ] },
      mount: { id: 'mount', children: [
        { id: 'bracket', ref: 'bracket' },
        { id: 'bolt', ref: 'bolt', position: [20, 3, 8], name: 'Bolt',
          pattern: { kind: 'grid', rows: 2, columns: 2, row_offset: [0, 0, 14], column_offset: [30, 0, 0] } }
      ] },
      pan: { id: 'pan', children: [
        { id: 'weld', ref: 'weld', name: 'Weld',
          pattern: { kind: 'grid', rows: 10, columns: 50, row_offset: [0, 0, 148], column_offset: [40, 0, 0] } }
      ] },
      module: { id: 'module', children: [
        { id: 'cell', ref: 'cell', name: 'Cell',
          pattern: { kind: 'grid', rows: 5, columns: 80, row_offset: [0, 0, 48], column_offset: [25, 0, 0] } }
      ] },
      seat: { id: 'seat', children: [
        { id: 'frame', ref: 'seat-frame' },
        { id: 'cushion', ref: 'cushion', position: [0, 90, 0] },
        { id: 'clip', ref: 'clip', position: [-240, 30, -250], name: 'Clip',
          pattern: { kind: 'grid', rows: 2, columns: 20, row_offset: [0, 0, 500], column_offset: [25, 0, 0] } }
      ] }
    };
    var perWheel = 4 + 20 + 5, perCorner = perWheel + 1, perSeam = 201, perMount = 5, perSeat = 42;
    /* corners, welds (2 pans of 500), cells (5 modules of 400), harness, mounts, seats,
     * windows, lamps and the exhaust; the seams make up the rest of the target. */
    var fixed = 4 * perCorner + 2 * 500 + 5 * 400 + 400 + 200 * perMount + 4 * perSeat + 6 + 2 + 1;
    var seams = Math.max(1, Math.ceil((target - fixed) / perSeam));
    var car = { id: 'car', children: [
      { id: 'front-left', ref: 'corner', position: [1400, 330, 800] },
      { id: 'front-right', ref: 'corner', position: [1400, 330, -800], mirror: 'z' },
      { id: 'rear-left', ref: 'corner', position: [-1400, 330, 800] },
      { id: 'rear-right', ref: 'corner', position: [-1400, 330, -800], mirror: 'z' },
      { id: 'seam', ref: 'seam', name: 'Seam', position: [0, 500, -900],
        pattern: { kind: 'linear', count: seams, offset: [0, 1000 / seams, 1800 / seams] } },
      /* ‼️ A pattern places at most 512 copies (geometry/pattern.go), and a larger one
       * is REFUSED and places nothing — so a thousand welds are two pans of 500, and two
       * thousand cells are five modules of 400, the way a pack is built anyway. */
      { id: 'pan', ref: 'pan', name: 'Floor pan', position: [-2000, 250, -700],
        pattern: { kind: 'linear', count: 2, offset: [2000, 0, 0] } },
      { id: 'module', ref: 'module', name: 'Module', position: [-1000, 200, -600],
        pattern: { kind: 'linear', count: 5, offset: [0, 0, 240] } },
      { id: 'harness', ref: 'harness', name: 'Harness', position: [0, 600, 0],
        pattern: { kind: 'path', count: 400, align: true,
                   path: [{ x: 2000, y: 0, z: 600 }, { x: 0, y: 300, z: 600 }, { x: -2000, y: 300, z: -600 }] } },
      { id: 'mount', ref: 'mount', name: 'Mount', position: [-2000, 400, 700],
        pattern: { kind: 'linear', count: 200, offset: [20, 0, 0] } },
      { id: 'seat', ref: 'seat', name: 'Seat', position: [-300, 450, 400],
        pattern: { kind: 'grid', rows: 2, columns: 2, row_offset: [0, 0, -800], column_offset: [-900, 0, 0] } },
      { id: 'glass', ref: 'glass', name: 'Window', position: [0, 1300, 760],
        pattern: { kind: 'grid', rows: 2, columns: 3, row_offset: [0, 0, -1520], column_offset: [950, 0, 0] } },
      { id: 'headlamp', ref: 'lamp', name: 'Headlamp', position: [2250, 700, 550],
        pattern: { kind: 'linear', count: 2, offset: [0, 0, -1100] } },
      { id: 'exhaust', ref: 'exhaust', position: [-1900, 250, -400] }
    ] };
    return {
      name: 'Instanced viewport car', units: 'mm', root: 'car',
      definitions: definitions,
      assemblies: [car, others.corner, others.wheel, others.seam, others.mount, others.seat, others.pan, others.module]
    };
  }

  var api = { carDocument: carDocument };
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
  else root.ForgeViewportCar = api;
})(typeof window !== 'undefined' ? window : this);
