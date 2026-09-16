// Injected into the shipped workbench page (javascript_tool) for the hidden-pane measurement.
// The Studio is a closure variable of workbench.js, so it is caught by wrapping
// Forge3D.Studio.prototype.draw and provoking one draw with a window resize (forge3d.js
// listens for it). Everything else reads the page as it is.
window.__m = window.__m || (function () {
  var M = { studio: null, draws: 0 };
  var P = Forge3D.Studio.prototype, orig = P.draw;
  P.draw = function () { M.draws++; if (this.lazy) M.studio = this; return orig.apply(this, arguments); };
  M.catchStudio = function () { window.dispatchEvent(new Event('resize')); return !!M.studio; };
  // Loader and page state.
  M.probe = function () {
    var s = M.studio, ls = s && s.lazyState ? s.lazyState() : null;
    var res = performance.getEntriesByType('resource').filter(function (e) { return e.name.indexOf('subtree=') >= 0; });
    return { t: Math.round(performance.now()), vis: document.visibilityState, focus: document.hasFocus(),
      loaded: ls && ls.loaded.length, pending: ls && ls.pending.length, failed: ls && ls.failed.length,
      requested: ls && ls.requested.length, placeholders: ls && ls.placeholders,
      subtreeRequests: res.length, subtreeDone: res.filter(function (e) { return e.responseEnd > 0; }).length,
      lastResponseEnd: res.length ? Math.round(Math.max.apply(null, res.map(function (e) { return e.responseEnd; }))) : null,
      draws: M.draws, batches: s && s.batches.length,
      canvas: s ? [s.canvas.width, s.canvas.height, s.canvas.clientWidth, s.canvas.clientHeight] : null };
  };
  function stats(iv) {
    iv = iv.slice().sort(function (a, b) { return a - b; });
    return iv.length ? { n: iv.length, median: +iv[iv.length >> 1].toFixed(1), p95: +iv[Math.floor(iv.length * 0.95)].toFixed(1),
      max: +iv[iv.length - 1].toFixed(1) } : { n: 0 };
  }
  // Presented frames: n requestAnimationFrame callbacks, each orbiting 0.05 rad and drawing
  // when draw is true. Gives up after timeout ms (a hidden document may never call back).
  M.raf = function (n, timeout, draw) {
    return new Promise(function (resolve) {
      var ts = [], t0 = performance.now(), done = false, s = M.studio;
      function finish(why) {
        if (done) return; done = true;
        var iv = []; for (var i = 1; i < ts.length; i++) iv.push(ts[i] - ts[i - 1]);
        resolve({ vis: document.visibilityState, why: why, callbacks: ts.length, waitedMs: Math.round(performance.now() - t0),
          interval: stats(iv) });
      }
      function frame(t) {
        if (done) return;
        ts.push(t);
        if (draw && s) { s.camera.yaw += 0.05; s.draw(); }
        if (ts.length > n) finish('frames'); else requestAnimationFrame(frame);
      }
      requestAnimationFrame(frame);
      setTimeout(function () { finish('timeout'); }, timeout);
    });
  };
  // Timer throttling: a 100 ms interval counted over ms.
  M.timers = function (ms) {
    return new Promise(function (resolve) {
      var n = 0, t0 = performance.now(), id = setInterval(function () { n++; }, 100);
      setTimeout(function () { clearInterval(id); resolve({ vis: document.visibilityState, ticks: n,
        expected: Math.round(ms / 100), waitedMs: Math.round(performance.now() - t0) }); }, ms);
    });
  };
  // Synced draw cost, as #93 measured it: 5 warm-up, then n draws each followed by a 1-px readPixels.
  M.drawTimes = function (n) {
    var s = M.studio, gl = s.gl, px = new Uint8Array(4), ts = [], i, t;
    for (i = 0; i < 5; i++) { s.draw(); gl.readPixels(0, 0, 1, 1, gl.RGBA, gl.UNSIGNED_BYTE, px); }
    for (i = 0; i < n; i++) {
      s.camera.yaw += 0.05; t = performance.now();
      s.draw(); gl.readPixels(0, 0, 1, 1, gl.RGBA, gl.UNSIGNED_BYTE, px);
      ts.push(performance.now() - t);
    }
    return { vis: document.visibilityState, draw: stats(ts), contextLost: gl.isContextLost() };
  };
  return M;
})();
