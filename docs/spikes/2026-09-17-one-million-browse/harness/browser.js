// What was run in the Browser pane (Chrome), pasted into each tab's page: the base tab on
// the pane proxy 18361 -> forged at origin/main (18360), the new tab on 18363 -> this
// branch (18362). Both show the fleet project; the pane was HIDDEN throughout.

// 1. Keep the page's Studio (the workbench holds it in a closure) and time each search.
window.__cap = null;
const P = window.Forge3D.Studio.prototype;
for (const k of ['findOccurrences', 'openRow', 'requestSubtree', 'draw']) {
  const f = P[k];
  if (f) P[k] = function (...a) { window.__cap = this; return f.apply(this, a); };
}

// 2. Typing "rivet" into the tree's search box, one input event per character.
async function typeRivet() {
  const s = document.getElementById('tree-search');
  const t = performance.now();
  for (const v of ['r', 'ri', 'riv', 'rive', 'rivet']) { s.value = v; s.dispatchEvent(new Event('input')); }
  return performance.now() - t;
}

// 3. The same five queries, three rounds, through the page's own findOccurrences.
function searchRounds() {
  const st = window.__cap, out = [];
  for (let round = 0; round < 3; round++) {
    for (const q of ['rivet', 'car-7/seam-12/rivet-1', 'seam 3', 'wheel', 'zzz']) {
      const t = performance.now(), r = st.findOccurrences(q, 50);
      out.push([round, q, +(performance.now() - t).toFixed(1), r.total].join(' '));
    }
  }
  return out;
}

// 4. Opening a car's row by clicking its toggle: time to its first rows, to all asked for.
window.__open = async (p) => {
  const tog = (x) => document.querySelector('#tree [data-toggle="' + x + '"]');
  const st = window.__cap;
  performance.clearResourceTimings();
  const t0 = performance.now();
  tog(p).click();
  let first = 0, done = 0;
  for (let i = 0; i < 400; i++) {
    const ls = st.lazyState();
    const mine = ls.loaded.filter((k) => k === p || k.startsWith(p + '/')).length;
    if (!first && mine) first = performance.now() - t0;
    if (mine && !ls.pending.length) { done = performance.now() - t0; break; }
    await new Promise((r) => setTimeout(r, 25));
  }
  const res = performance.getEntriesByType('resource').filter((e) => e.name.includes('subtree='));
  return { p, firstMs: Math.round(first), doneMs: Math.round(done), requests: res.length,
           bytes: res.reduce((a, e) => a + e.encodedBodySize, 0), drawn: st.lazyState().drawn };
};
