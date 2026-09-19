/* A WebGL context that draws nothing and remembers what it was asked to draw.
 *
 * # Why a stub, and what it can and cannot say
 *
 * The viewport's fences run in node, which has no GPU. What they need to know is
 * not what a frame LOOKS like but what the renderer SENT: how many draw calls, and
 * for every instance of every call the matrix, colour, opacity and highlight the
 * shader would read. That is fully determined by the calls made on the context,
 * so this records the calls and reads each instance's attributes back out of the
 * buffers exactly as a GPU would — by location, stride, offset and divisor.
 *
 * It cannot compile GLSL, measure time on a GPU, or see a pixel. A shader that
 * does not compile is only caught by a real browser; see
 * docs/spikes/2026-09-15-instanced-viewport for the run in one.
 *
 * # The traps it refuses
 *
 * Each is state that outlives the draw that set it, which is why each looked fine
 * in the draw that caused it:
 *
 *   - a divisor on attribute 0 (WebGL1 with ANGLE_instanced_arrays refuses the
 *     draw on some drivers);
 *   - an instance attribute left enabled, or left with a divisor, when the line
 *     program draws the grid or an overlay;
 *   - an attribute read past the end of its buffer, or an index past the end of
 *     the vertices;
 *   - a stride over 255 or an offset that is not a multiple of four bytes.
 *
 * A refused call is recorded in `problems`, never thrown, so a fence reports every
 * one it saw rather than the first.
 *
 * Used by scripts/viewport-instancing-check.js and by the node-driven fences in
 * internal/httpapi/viewport_instancing_fence_test.go.
 */
'use strict';

const fs = require('fs');
const vm = require('vm');

const C = {
  TRIANGLES: 4, LINES: 1, ARRAY_BUFFER: 34962, ELEMENT_ARRAY_BUFFER: 34963,
  STATIC_DRAW: 35044, DYNAMIC_DRAW: 35048, FLOAT: 5126, UNSIGNED_SHORT: 5123, UNSIGNED_INT: 5125,
  VERTEX_SHADER: 35633, FRAGMENT_SHADER: 35632, COMPILE_STATUS: 35713, LINK_STATUS: 35714,
  DEPTH_TEST: 2929, CULL_FACE: 2884, BLEND: 3042, SRC_ALPHA: 770, ONE_MINUS_SRC_ALPHA: 771,
  COLOR_BUFFER_BIT: 16384, DEPTH_BUFFER_BIT: 256, CW: 2304, CCW: 2305,
  // Textures, framebuffers and blending, for the presentation passes (2026-09-18).
  TEXTURE_2D: 3553, TEXTURE0: 33984, RGBA: 6408, RGBA8: 32856, UNSIGNED_BYTE: 5121,
  TEXTURE_MIN_FILTER: 10241, TEXTURE_MAG_FILTER: 10240, TEXTURE_WRAP_S: 10242, TEXTURE_WRAP_T: 10243,
  LINEAR: 9729, NEAREST: 9728, CLAMP_TO_EDGE: 33071, REPEAT: 10497,
  FRAMEBUFFER: 36160, READ_FRAMEBUFFER: 36008, DRAW_FRAMEBUFFER: 36009, RENDERBUFFER: 36161,
  COLOR_ATTACHMENT0: 36064, DEPTH_ATTACHMENT: 36096, FRAMEBUFFER_COMPLETE: 36053,
  DEPTH_COMPONENT: 6402, DEPTH_COMPONENT16: 33189, DEPTH_COMPONENT24: 33190, MAX_SAMPLES: 36183,
  ZERO: 0, ONE: 1, SRC_COLOR: 768, DST_COLOR: 774,
};

/* mode: 'webgl2', 'webgl1' (with ANGLE_instanced_arrays) or 'webgl1-noext'. */
function createContext(mode) {
  const record = { draws: [], lineDraws: 0, problems: [], uploadedBytes: 0, uploads: 0 };
  const attribs = [];
  for (let i = 0; i < 16; i++) {
    attribs.push({ enabled: false, buffer: null, size: 4, stride: 16, offset: 0, divisor: 0, constant: [0, 0, 0, 1] });
  }
  const bound = { array: null, element: null, read: null, draw: null };
  let program = null, front = C.CCW, nextID = 1;
  const problem = (msg) => { if (record.problems.indexOf(msg) < 0) record.problems.push(msg); };

  function read(loc, instance, vertex) {
    const a = attribs[loc];
    if (!a.enabled) return a.constant.slice();
    const data = a.buffer && a.buffer.data;
    if (!data) { problem('attribute ' + loc + ' is enabled with no data behind it'); return [NaN, NaN, NaN, NaN]; }
    const at = a.offset / 4 + (a.divisor ? Math.floor(instance / a.divisor) : vertex) * (a.stride / 4);
    if (at + a.size > data.length) { problem('attribute ' + loc + ' reads past the end of its buffer'); return [NaN, NaN, NaN, NaN]; }
    const out = [0, 0, 0, 1];
    for (let k = 0; k < a.size; k++) out[k] = data[at + k];
    return out;
  }

  /* A program's uniforms, from its sources: asking for one it does not declare is a
   * uniform that is never set, which a real context answers with null and silence. */
  function declared(p) {
    const names = {};
    for (const src of p.sources) {
      const re = /uniform\s+(?:(?:lowp|mediump|highp)\s+)?\w+\s+(\w+)/g;
      let m;
      while ((m = re.exec(src))) names[m[1]] = true;
    }
    return names;
  }
  /* The version rules a GLSL compiler would apply, as far as text can tell: both stages
   * the same version, 3.00 only on WebGL2, and each version's own vocabulary. It is not a
   * compiler; TestShadersCompileInARealBrowser is. */
  function lint(p) {
    const versions = p.sources.map((src) => (/^\s*#version\s+300\s+es/.test(src) ? 300 : 100));
    if (versions[0] !== versions[1]) problem('a program links a GLSL ES 1.00 stage with a 3.00 one');
    p.sources.forEach((src, k) => {
      const code = src.replace(/\/\/.*$/gm, '');
      if (versions[k] === 300) {
        if (mode !== 'webgl2') problem('a GLSL ES 3.00 shader on a WebGL1 context');
        if (/\b(attribute|varying|gl_FragColor|texture2D)\b/.test(code)) problem('GLSL ES 1.00 words in a 3.00 shader');
      } else if (/\btexture\s*\(|\btextureLod\b|^\s*(in|out)\s/m.test(code)) {
        problem('GLSL ES 3.00 words in a 1.00 shader');
      }
      if (/#version/.test(code.replace(/^\s*#version[^\n]*/, ''))) problem('#version is not the first line');
    });
  }

  /* The last value each uniform was set to, by name, and what the frame did in order: a
   * triangle draw is "opaque" or "translucent" by its first copy's alpha, a full-screen
   * pass is named by the uniform only its program declares. */
  function setUniform(l, v) { if (l) record.uniforms[l.name] = Array.prototype.slice.call(v); }
  function screenName(p) {
    const u = (p && p.uniforms) || {};
    return u.uAO ? 'occlusion' : u.uDepth ? 'ssao' : u.uInvViewProj ? 'backdrop' : u.uStep ? 'blur' :
      u.uShadow ? 'ground' : 'other';
  }

  function draw(count, type, n, instanced, mode) {
    if (!program) { problem('a draw with no program in use'); return; }
    if (attribs[0].divisor) problem('attribute 0 has a divisor, which WebGL1 with ANGLE refuses');
    const idx = bound.element && bound.element.data;
    if (!idx) { problem('a draw with no index buffer bound'); return; }
    // By element size, not instanceof: the renderer runs in a vm context, whose
    // Uint16Array is not this realm's.
    if ((type === C.UNSIGNED_SHORT ? 2 : 4) !== idx.BYTES_PER_ELEMENT) problem('an index type that is not the buffer it reads');
    if (count > idx.length) problem('a draw of more indices than its buffer holds');
    const pos = attribs[0].buffer && attribs[0].buffer.data;
    let highest = 0;
    for (let i = 0; i < count && i < idx.length; i++) if (idx[i] > highest) highest = idx[i];
    if (!pos || (highest + 1) * 3 > pos.length) problem('an index past the end of the vertices');
    const names = program.attributes;
    if (!('aModel' in names)) { problem('a triangle draw with a program that reads no instances'); return; }
    for (let k = 1; k <= 7; k++) {
      const a = attribs[k];
      if (k === 1) { if (a.divisor) problem('the normal has a divisor'); continue; }
      if (instanced && a.enabled && a.divisor !== 1) problem('instance attribute ' + k + ' is read per vertex in an instanced draw');
      if (!instanced && a.enabled) problem('instance attribute ' + k + ' is an array in a draw of one copy');
    }
    const instances = [];
    for (let j = 0; j < n; j++) {
      const model = [];
      for (let k = 0; k < 4; k++) model.push.apply(model, read(2 + k, j, 0));
      instances.push({ model: model, colour: read(6, j, 0), highlight: read(7, j, 0)[0] });
    }
    const drawn = { instanced: instanced, elements: count, indexType: type,
                    frontFace: front === C.CW ? 'cw' : 'ccw', instances: instances, mode: mode };
    /* What reaches the canvas: a draw into the default framebuffer, or into one that is
     * later resolved to it (blitFramebuffer). Lines and draws into a framebuffer that
     * never reaches the screen (the contact shadow) are recorded apart, so a fence
     * counting what a person sees counts only that. */
    if (mode === C.LINES) { record.lineInstanceDraws.push(drawn); record.sequence.push('edges'); return; }
    record.sequence.push(!program.uniforms.uMaterial ? 'shadow' : instances.length && instances[0].colour[3] < 1 ? 'translucent' : 'opaque');
    if (bound.draw) { bound.draw.pending.push(drawn); record.passes.offscreen.push(drawn); return; }
    record.draws.push(drawn);
  }

  const gl = Object.assign({}, C, {
    createShader: (type) => ({ id: nextID++, type: type, source: '' }),
    shaderSource(sh, src) { sh.source = src; }, compileShader() {},
    getShaderParameter: () => true, getShaderInfoLog: () => '',
    createProgram: () => ({ id: nextID++, attributes: {}, sources: [] }),
    attachShader(p, sh) { p.sources.push(sh.source); },
    bindAttribLocation(p, loc, name) { p.attributes[name] = loc; },
    linkProgram(p) { p.uniforms = declared(p); lint(p); record.programs.push(p.sources.slice()); },
    getProgramParameter: () => true, getProgramInfoLog: () => '',
    useProgram(p) { program = p; },
    getAttribLocation: (p, name) => (name in p.attributes ? p.attributes[name] : -1),
    getUniformLocation(p, name) {
      if (!p.uniforms || !p.uniforms[name]) { problem('uniform ' + name + ' is asked of a program that does not declare it'); return null; }
      return { name: name, program: p.id };
    },
    uniformMatrix4fv() {}, uniformMatrix3fv() {}, uniform1i() {},
    uniform1f: (l, v) => setUniform(l, [v]), uniform2f: (l, a, b) => setUniform(l, [a, b]),
    uniform3f: (l, a, b, c) => setUniform(l, [a, b, c]), uniform4f: (l, a, b, c, d) => setUniform(l, [a, b, c, d]),
    uniform3fv: (l, v) => setUniform(l, v), uniform4fv: (l, v) => setUniform(l, v), uniform2fv: (l, v) => setUniform(l, v),
    viewport() {}, clearColor() {}, enable() {}, disable() {}, blendFunc() {},
    depthMask() {}, depthFunc() {}, colorMask() {}, polygonOffset() {},
    clear(mask) { if (bound.draw && (mask & C.COLOR_BUFFER_BIT)) bound.draw.pending = []; },
    getError: () => 0,
    getParameter: (name) => (name === C.MAX_SAMPLES ? 4 : 0),
    createTexture: () => ({ id: nextID++ }), deleteTexture() {}, bindTexture() {}, activeTexture() {},
    texParameteri() {},
    texImage2D(target, level, internal, w, h, border, format, type, pixels) {
      if (internal === C.DEPTH_COMPONENT24 && mode !== 'webgl2') problem('a depth texture on WebGL1');
      if (pixels && pixels.length !== w * h * 4) problem('texImage2D with ' + pixels.length + ' bytes for ' + w + 'x' + h);
    },
    createFramebuffer: () => ({ id: nextID++, pending: [] }), deleteFramebuffer() {},
    bindFramebuffer(target, fb) {
      if (target === C.FRAMEBUFFER || target === C.DRAW_FRAMEBUFFER) bound.draw = fb;
      if (target === C.FRAMEBUFFER || target === C.READ_FRAMEBUFFER) bound.read = fb;
    },
    framebufferTexture2D() {}, framebufferRenderbuffer() {},
    checkFramebufferStatus: () => C.FRAMEBUFFER_COMPLETE,
    createRenderbuffer: () => ({ id: nextID++ }), deleteRenderbuffer() {}, bindRenderbuffer() {},
    renderbufferStorage() {},
    frontFace(f) { front = f; },
    createBuffer: () => ({ id: nextID++, data: null }),
    deleteBuffer(b) { if (b) b.deleted = true; },
    bindBuffer(target, b) { if (target === C.ARRAY_BUFFER) bound.array = b; else bound.element = b; },
    bufferData(target, data) {
      const b = target === C.ARRAY_BUFFER ? bound.array : bound.element;
      if (!b) { problem('bufferData with no buffer bound'); return; }
      b.data = data.slice();
      record.uploadedBytes += data.byteLength;
      record.uploads++;
    },
    enableVertexAttribArray(l) { attribs[l].enabled = true; },
    disableVertexAttribArray(l) { attribs[l].enabled = false; },
    vertexAttribPointer(l, size, type, normalised, stride, offset) {
      if (!bound.array) problem('vertexAttribPointer with no buffer bound');
      if (stride > 255) problem('a stride over 255 bytes');
      if (offset % 4) problem('an attribute offset that is not a multiple of four bytes');
      Object.assign(attribs[l], { buffer: bound.array, size: size, stride: stride || size * 4, offset: offset });
    },
    vertexAttrib4f(l, a, b, c, d) { attribs[l].constant = [a, b, c, d]; },
    vertexAttrib1f(l, a) { attribs[l].constant = [a, 0, 0, 1]; },
    drawElements(mode, count, type) { draw(count, type, 1, false, mode); },
    drawArrays(mode) {
      if (mode !== C.LINES) { record.screenDraws++; record.sequence.push('screen:' + screenName(program)); }
      else record.sequence.push('lines');
      for (let k = 1; k < 16; k++) {
        if (attribs[k].enabled) problem('attribute ' + k + ' is still enabled when a line is drawn');
        if (attribs[k].divisor) problem('attribute ' + k + ' still has a divisor when a line is drawn');
      }
      if (mode === C.LINES) record.lineDraws++;
    },
  });
  const setDivisor = (l, d) => { attribs[l].divisor = d; };
  if (mode === 'webgl2') {
    gl.drawElementsInstanced = (m, count, type, offset, n) => draw(count, type, n, true, m);
    gl.vertexAttribDivisor = setDivisor;
    gl.renderbufferStorageMultisample = () => {};
    /* A resolve to the canvas is when what was drawn into a framebuffer reaches the screen. */
    gl.blitFramebuffer = (x0, y0, x1, y1, a0, b0, a1, b1, mask) => {
      if (!bound.read || !(mask & C.COLOR_BUFFER_BIT)) return;
      if (bound.draw) bound.draw.pending.push.apply(bound.draw.pending, bound.read.pending);
      else { record.draws.push.apply(record.draws, bound.read.pending); record.resolves++; record.sequence.push('resolve'); }
      bound.read.pending = [];
    };
  }
  const angle = mode === 'webgl1'
    ? { drawElementsInstancedANGLE: (m, count, type, offset, n) => draw(count, type, n, true, m),
        vertexAttribDivisorANGLE: setDivisor }
    : null;
  gl.getExtension = (name) => {
    if (name === 'ANGLE_instanced_arrays') return angle;
    if (name === 'OES_element_index_uint') return mode === 'webgl2' ? null : {};
    return null;
  };
  record.reset = () => {
    record.draws = []; record.lineDraws = 0; record.uploadedBytes = 0; record.uploads = 0;
    record.lineInstanceDraws = []; record.screenDraws = 0; record.resolves = 0; record.passes = { offscreen: [] };
    record.uniforms = {}; record.sequence = [];
  };
  record.programs = [];
  record.reset();
  return { gl: gl, record: record };
}

/* A canvas that hands out the context its mode allows, as a browser would. */
function makeCanvas(mode, width, height) {
  const made = createContext(mode);
  const canvas = {
    width: 0, height: 0, clientWidth: width || 640, clientHeight: height || 480,
    getContext(type) {
      if (type === 'webgl2') return mode === 'webgl2' ? made.gl : null;
      if (type === 'webgl' || type === 'experimental-webgl') return mode === 'webgl2' ? null : made.gl;
      return null;
    },
    addEventListener() {},
    getBoundingClientRect() { return { left: 0, top: 0, width: this.clientWidth, height: this.clientHeight }; },
    toDataURL: () => '',
  };
  made.gl.canvas = canvas;
  return { canvas: canvas, gl: made.gl, record: made.record };
}

/* The shipped forge3d.js, evaluated with the little of `window` it reaches for. */
function loadForge3D(path) {
  const sandbox = { window: { addEventListener() {}, devicePixelRatio: 1 }, console: console };
  vm.createContext(sandbox);
  vm.runInContext(fs.readFileSync(path, 'utf8'), sandbox);
  return sandbox.window.Forge3D;
}

module.exports = { makeCanvas: makeCanvas, loadForge3D: loadForge3D, constants: C };
