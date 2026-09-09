#!/usr/bin/env node
/* Does the parts panel show how big an extrusion actually is?
 *
 * An extrusion's "size" carries ONLY its depth — the other two dimensions live
 * in its outline. dimensionsOf printed size.width and size.height straight out,
 * so every extrusion read "? × ? × 4500 mm".
 *
 * That is not merely incomplete, it is the specific reason a real failure went
 * unseen. Asked on 2026-09-09 to make a car body less boxy, the model drew the
 * car's SIDE view — 4500 mm long — and extruded it by 4500 mm, so a body that
 * should have been 1900 wide became a 4.5 m square slab. The one number on
 * screen (4500) was the one that was right, and the panel looked exactly like a
 * panel over a correct body. The person could see the render was wrong and had
 * nothing to tell them why.
 *
 * A string fence cannot catch this: the old line was well-formed JavaScript that
 * read the fields it was given. Only running the real dimensionsOf over a real
 * profile and reading the string shows it. So this drives the shipped
 * dimensionsOf against the shipped outlineExtent.
 *
 *   make test-extrusion-size   (or: node scripts/extrusion-size-check.js)
 *
 * See docs/bugfix/2026-09-09-the-body-used-its-length-twice.md
 */
const fs = require('fs');
/* Paths are overridable so the fence can be proven RED against known-bad copies
 * WITHOUT editing the tree under test — a drill that mutates the working copy
 * produces failures indistinguishable from real ones. */
const wbPath = process.argv[2] || (__dirname + '/../internal/httpapi/assets/workbench.js');
const f3Path = process.argv[3] || (__dirname + '/../internal/httpapi/assets/forge3d.js');

/* ---- the minimum browser both files reach for ------------------------- */
const listeners = {};
globalThis.window = globalThis;
globalThis.document = {
  getElementById: () => null,
  querySelector: () => null, querySelectorAll: () => [],
  createElement: () => ({ style: {}, classList: { add(){}, remove(){}, toggle(){} },
                          appendChild(){}, setAttribute(){}, getContext: () => null }),
  addEventListener: (k, f) => { listeners[k] = f; },
  body: { appendChild(){}, classList: { add(){}, remove(){} } },
  documentElement: { classList: { add(){}, remove(){}, contains: () => false },
                     setAttribute(){}, getAttribute: () => null, style: {} },
};
globalThis.localStorage = { getItem: () => null, setItem(){}, removeItem(){} };
globalThis.addEventListener = (k, f) => { listeners[k] = f; };
globalThis.requestAnimationFrame = () => 0;
globalThis.fetch = () => new Promise(() => {});
globalThis.matchMedia = () => ({ matches: false, addEventListener(){}, addListener(){} });

/* forge3d.js first: workbench.js reads window.Forge3D at call time. */
new Function(fs.readFileSync(f3Path, 'utf8'))();
if (!globalThis.Forge3D || typeof globalThis.Forge3D.outlineExtent !== 'function') {
  console.error('FAIL: Forge3D.outlineExtent is not exported, so the panel has no way to\n' +
                '      measure an outline and every extrusion will read "? × ?".');
  process.exit(1);
}

/* workbench.js keeps dimensionsOf inside its IIFE. Rather than export it purely
 * for a test — which changes the shipped surface to suit the fence — the
 * function is lifted out by source and evaluated with the two helpers it uses.
 * If it stops being called dimensionsOf, this fence fails loudly rather than
 * silently testing nothing. */
const wb = fs.readFileSync(wbPath, 'utf8');
const m = wb.match(/function dimensionsOf\(p\) \{[\s\S]*?\n  \}/);
if (!m) {
  console.error('FAIL: dimensionsOf was not found in workbench.js. This fence measures the\n' +
                '      real function; if it was renamed, point the fence at the new one\n' +
                '      rather than deleting the fence.');
  process.exit(1);
}
const dimensionsOf = new Function(
  'qty', 'esc', 'window',
  m[0] + '; return dimensionsOf;'
)(v => (v == null ? '?' : String(Math.round(v)) + ' mm'), x => x, globalThis);

/* ---- the body from the live failure ----------------------------------- */
/* Its own record, 2026-09-09: the side elevation drawn into the outline and
 * 4500 given again as the depth. */
const squareSlab = {
  id: 'chassis-body', shape: 'extrusion', size: { depth: 4500 },
  profile: [ {x:-2250,y:0}, {x:2250,y:0}, {x:2250,y:800},
             {x:1500,y:800}, {x:0,y:600}, {x:-1500,y:800}, {x:-2250,y:800} ],
};
/* What it should have been: the same silhouette, extruded by the car's WIDTH. */
const correctBody = Object.assign({}, squareSlab, { size: { depth: 1900 } });

let failed = 0;
function check(name, got, want) {
  if (got.indexOf(want) >= 0) { console.log(`  PASS  ${name}`); return; }
  console.error(`  FAIL  ${name}\n        wanted to contain: ${want}\n        got:               ${got}`);
  failed = 1;
}

const slab = dimensionsOf(squareSlab);
const good = dimensionsOf(correctBody);

/* The number that was missing is the one that would have shown the bug. */
check('the square slab shows its real 4500 mm width', slab, '4500 mm × 800 mm × 4500 mm');
check('a correct body shows its real width too',      good, '4500 mm × 800 mm × 1900 mm');

/* And the panel must not have started guessing: an outline it cannot resolve
 * still says "?", because a guess printed next to a unit reads as a measurement. */
const unresolvable = { id: 'x', shape: 'extrusion', size: { depth: 10 }, profile: [{x:0,y:0},{x:1,y:0}] };
check('an outline of two points still says "?"', dimensionsOf(unresolvable), '? × ? × 10 mm');

/* A box is untouched: it carries all three dimensions itself. */
check('a box is unaffected',
  dimensionsOf({ id: 'b', shape: 'box', size: { width: 100, height: 20, depth: 50 } }),
  '100 mm × 20 mm × 50 mm');

if (failed) {
  console.error('\nThe parts panel cannot tell a person how big an extrusion is.');
  process.exit(1);
}
console.log('\nExtrusions report the size their outline gives them.');
