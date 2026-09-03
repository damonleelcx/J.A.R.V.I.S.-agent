/* FORGE 3D studio — a small WebGL renderer for engineering prototypes.
 *
 * # Why this is hand-written rather than three.js
 *
 * The application's CSP is `script-src 'self'`: no CDN, and there is no build
 * step to vendor a bundle through. More importantly, this viewport is the
 * primary surface of the product — the thing on screen when someone is deciding
 * whether a design is right — and it must render on a machine with no network
 * and no toolchain. A dependency that can fail to load is a dependency that will
 * fail to load in front of a customer.
 *
 * What it does, mapped to PRD VIS-02: orbit / pan / zoom, section cuts, exploded
 * views, assembly states, transparency, annotations, material appearance,
 * lighting, and a scale reference.
 *
 * What it deliberately does NOT do: imply that anything here is verified.
 * A render is a picture of a proposal (PRD VIS-06). The status banner is drawn
 * by the workbench, not by this file, but the renderer keeps the provenance
 * fields on every part so the banner has something true to say.
 */
(function (global) {
  'use strict';

  /* ---- linear algebra -------------------------------------------------- */
  /* Column-major 4x4, matching what WebGL expects, so nothing is transposed on
   * the way to the GPU. */

  function mat4() {
    return new Float32Array([1,0,0,0, 0,1,0,0, 0,0,1,0, 0,0,0,1]);
  }

  function multiply(a, b) {
    var out = new Float32Array(16);
    for (var c = 0; c < 4; c++) {
      for (var r = 0; r < 4; r++) {
        out[c * 4 + r] =
          a[0 * 4 + r] * b[c * 4 + 0] +
          a[1 * 4 + r] * b[c * 4 + 1] +
          a[2 * 4 + r] * b[c * 4 + 2] +
          a[3 * 4 + r] * b[c * 4 + 3];
      }
    }
    return out;
  }

  function perspective(fovyDeg, aspect, near, far) {
    var f = 1 / Math.tan((fovyDeg * Math.PI / 180) / 2);
    var out = new Float32Array(16);
    out[0] = f / aspect; out[5] = f;
    out[10] = (far + near) / (near - far); out[11] = -1;
    out[14] = (2 * far * near) / (near - far);
    return out;
  }

  function lookAt(eye, center, up) {
    var z = normalize(sub(eye, center));
    var x = normalize(cross(up, z));
    var y = cross(z, x);
    var out = mat4();
    out[0]=x[0]; out[4]=x[1]; out[8]=x[2];
    out[1]=y[0]; out[5]=y[1]; out[9]=y[2];
    out[2]=z[0]; out[6]=z[1]; out[10]=z[2];
    out[12] = -dot(x, eye); out[13] = -dot(y, eye); out[14] = -dot(z, eye);
    return out;
  }

  function translation(t) {
    var m = mat4();
    m[12] = t[0]; m[13] = t[1]; m[14] = t[2];
    return m;
  }

  function scaling(s) {
    var m = mat4();
    m[0] = s[0]; m[5] = s[1]; m[10] = s[2];
    return m;
  }

  function rotationXYZ(r) {
    var cx = Math.cos(r[0]), sx = Math.sin(r[0]);
    var cy = Math.cos(r[1]), sy = Math.sin(r[1]);
    var cz = Math.cos(r[2]), sz = Math.sin(r[2]);
    var m = mat4();
    m[0] = cy*cz;            m[4] = -cy*sz;           m[8]  = sy;
    m[1] = sx*sy*cz + cx*sz; m[5] = -sx*sy*sz + cx*cz; m[9]  = -sx*cy;
    m[2] = -cx*sy*cz + sx*sz; m[6] = cx*sy*sz + sx*cz; m[10] = cx*cy;
    return m;
  }

  function normalMatrix(m) {
    /* Inverse-transpose of the upper 3x3, so non-uniform scale does not tilt
     * the lighting — which shows up as a part that looks lit from the wrong
     * side and reads as a modelling error rather than a shading one. */
    var a00=m[0],a01=m[1],a02=m[2], a10=m[4],a11=m[5],a12=m[6], a20=m[8],a21=m[9],a22=m[10];
    var b01 =  a22*a11 - a12*a21, b11 = -a22*a10 + a12*a20, b21 =  a21*a10 - a11*a20;
    var det = a00*b01 + a01*b11 + a02*b21;
    if (!det) return new Float32Array([1,0,0, 0,1,0, 0,0,1]);
    det = 1.0 / det;
    return new Float32Array([
      b01*det, (-a22*a01 + a02*a21)*det, ( a12*a01 - a02*a11)*det,
      b11*det, ( a22*a00 - a02*a20)*det, (-a12*a00 + a02*a10)*det,
      b21*det, (-a21*a00 + a01*a20)*det, ( a11*a00 - a01*a10)*det
    ]);
  }

  function sub(a, b) { return [a[0]-b[0], a[1]-b[1], a[2]-b[2]]; }
  function add(a, b) { return [a[0]+b[0], a[1]+b[1], a[2]+b[2]]; }
  function scale3(a, s) { return [a[0]*s, a[1]*s, a[2]*s]; }
  function dot(a, b) { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2]; }
  function cross(a, b) {
    return [a[1]*b[2]-a[2]*b[1], a[2]*b[0]-a[0]*b[2], a[0]*b[1]-a[1]*b[0]];
  }
  function length3(a) { return Math.sqrt(dot(a, a)); }
  function normalize(a) {
    var l = length3(a);
    return l > 0 ? [a[0]/l, a[1]/l, a[2]/l] : [0, 0, 0];
  }

  /* ---- geometry primitives --------------------------------------------- */
  /* Each returns { positions, normals, indices } in local space, centred on the
   * origin so a part's transform means what a reader expects. */

  /* Tessellation, declared once (PRD VIS-05).
   *
   * These are not free numbers. internal/domain/geometry/mesh.go tessellates
   * exported meshes with the SAME counts, so the file somebody downloads is the
   * surface they were looking at — and the export's stated chord deviation is
   * the deviation of what is on screen. If the two drift, the export quietly
   * stops being a picture of the render and nothing says so.
   *
   * TestTessellation_GoMatchesTheRenderer parses this object out of this file
   * and fails when Go disagrees with it. Keep the shape literal and greppable. */
  var TESSELLATION = { radial: 40, sphereRadial: 32 };

  function boxGeometry(w, h, d) {
    var x = w/2, y = h/2, z = d/2;
    var p = [], n = [], i = [];
    var faces = [
      { normal: [0,0,1],  corners: [[-x,-y,z],[x,-y,z],[x,y,z],[-x,y,z]] },
      { normal: [0,0,-1], corners: [[x,-y,-z],[-x,-y,-z],[-x,y,-z],[x,y,-z]] },
      { normal: [0,1,0],  corners: [[-x,y,z],[x,y,z],[x,y,-z],[-x,y,-z]] },
      { normal: [0,-1,0], corners: [[-x,-y,-z],[x,-y,-z],[x,-y,z],[-x,-y,z]] },
      { normal: [1,0,0],  corners: [[x,-y,z],[x,-y,-z],[x,y,-z],[x,y,z]] },
      { normal: [-1,0,0], corners: [[-x,-y,-z],[-x,-y,z],[-x,y,z],[-x,y,-z]] }
    ];
    faces.forEach(function (f) {
      var base = p.length / 3;
      f.corners.forEach(function (c) {
        p.push(c[0], c[1], c[2]);
        n.push(f.normal[0], f.normal[1], f.normal[2]);
      });
      i.push(base, base+1, base+2, base, base+2, base+3);
    });
    return { positions: p, normals: n, indices: i };
  }

  function cylinderGeometry(radius, height, segments, radiusTop) {
    segments = segments || 32;
    if (radiusTop === undefined) radiusTop = radius;
    var p = [], n = [], idx = [];
    var half = height / 2;

    for (var s = 0; s <= segments; s++) {
      var theta = (s / segments) * Math.PI * 2;
      var cosT = Math.cos(theta), sinT = Math.sin(theta);
      /* The side normal accounts for taper: a cone lit as a cylinder looks
       * subtly wrong in a way people notice without being able to name. */
      var slope = (radius - radiusTop) / height;
      var nrm = normalize([cosT, slope, sinT]);
      p.push(radiusTop*cosT, half, radiusTop*sinT); n.push(nrm[0], nrm[1], nrm[2]);
      p.push(radius*cosT, -half, radius*sinT);      n.push(nrm[0], nrm[1], nrm[2]);
    }
    for (var s2 = 0; s2 < segments; s2++) {
      var a = s2*2, b = a+1, c = a+2, d = a+3;
      idx.push(a, b, d, a, d, c);
    }
    // Caps.
    [[half, radiusTop, [0,1,0], false], [-half, radius, [0,-1,0], true]].forEach(function (cap) {
      var y = cap[0], r = cap[1], nrm = cap[2], flip = cap[3];
      var centre = p.length / 3;
      p.push(0, y, 0); n.push(nrm[0], nrm[1], nrm[2]);
      for (var s3 = 0; s3 <= segments; s3++) {
        var t = (s3 / segments) * Math.PI * 2;
        p.push(r*Math.cos(t), y, r*Math.sin(t));
        n.push(nrm[0], nrm[1], nrm[2]);
      }
      for (var s4 = 0; s4 < segments; s4++) {
        if (flip) idx.push(centre, centre+s4+2, centre+s4+1);
        else idx.push(centre, centre+s4+1, centre+s4+2);
      }
    });
    return { positions: p, normals: n, indices: idx };
  }

  function sphereGeometry(radius, segments) {
    segments = segments || 24;
    var rings = Math.max(8, Math.floor(segments / 2));
    var p = [], n = [], idx = [];
    for (var y = 0; y <= rings; y++) {
      var phi = (y / rings) * Math.PI;
      for (var x = 0; x <= segments; x++) {
        var theta = (x / segments) * Math.PI * 2;
        var nx = Math.sin(phi) * Math.cos(theta);
        var ny = Math.cos(phi);
        var nz = Math.sin(phi) * Math.sin(theta);
        p.push(nx*radius, ny*radius, nz*radius);
        n.push(nx, ny, nz);
      }
    }
    for (var y2 = 0; y2 < rings; y2++) {
      for (var x2 = 0; x2 < segments; x2++) {
        var a = y2*(segments+1) + x2, b = a + segments + 1;
        idx.push(a, b, a+1, b, b+1, a+1);
      }
    }
    return { positions: p, normals: n, indices: idx };
  }

  function planeGeometry(w, d) {
    var x = w/2, z = d/2;
    return {
      positions: [-x,0,-z, x,0,-z, x,0,z, -x,0,z],
      normals: [0,1,0, 0,1,0, 0,1,0, 0,1,0],
      indices: [0,1,2, 0,2,3]
    };
  }

  var SUPPORTED = ['box', 'cylinder', 'cone', 'sphere', 'plane', 'tube'];

  /* buildGeometry returns { geo, approximated }.
   *
   * # Why substitution is reported rather than silent
   *
   * A model asked for these six shapes will sometimes name a seventh —
   * "triangle-prism", "fillet", "hex-nut". An earlier version fell through to a
   * box, and the result was a parts list that said "triangle-prism" beside a
   * render showing a rectangular block. Nobody was told. That is the same class
   * of failure as reporting an unverified task as verified: the interface
   * asserted something the system had not done.
   *
   * So an unsupported shape is still drawn — a blank viewport helps nobody — but
   * it is flagged, and the workbench puts it in the provenance banner where the
   * viewer reads what this render does NOT establish. */
  function buildGeometry(part) {
    var s = part.size || {};
    switch (part.shape) {
      case 'box':      return { geo: boxGeometry(num(s.width,1), num(s.height,1), num(s.depth,1)) };
      case 'cylinder': return { geo: cylinderGeometry(num(s.radius,0.5), num(s.height,1), TESSELLATION.radial, num(s.radius_top, num(s.radius,0.5))) };
      case 'cone':     return { geo: cylinderGeometry(num(s.radius,0.5), num(s.height,1), TESSELLATION.radial, 0) };
      case 'sphere':   return { geo: sphereGeometry(num(s.radius,0.5), TESSELLATION.sphereRadial) };
      case 'plane':    return { geo: planeGeometry(num(s.width,1), num(s.depth,1)) };
      case 'tube':
        /* A tube is drawn as its outer wall. The bore is not modelled, and that
         * is reported: an inner diameter that is not there is exactly the kind
         * of thing a render must not imply. */
        return {
          geo: cylinderGeometry(num(s.radius,0.5), num(s.height,1), TESSELLATION.radial, num(s.radius,0.5)),
          approximated: 'drawn as a solid cylinder — the bore is not modelled'
        };
      default:
        return {
          geo: boxGeometry(num(s.width,1), num(s.height,1), num(s.depth,1)),
          approximated: 'shape "' + String(part.shape) + '" is not supported by this renderer ' +
                        'and is drawn as a bounding box'
        };
    }
  }

  function num(v, d) { return (typeof v === 'number' && isFinite(v)) ? v : d; }

  /* niceStep rounds to 1, 2 or 5 times a power of ten, so grid lines land on
   * numbers a person can count in their head. */
  function niceStep(raw) {
    if (!(raw > 0) || !isFinite(raw)) return 1;
    var exp = Math.floor(Math.log(raw) / Math.LN10);
    var pow = Math.pow(10, exp);
    var frac = raw / pow;
    var mult = frac < 1.5 ? 1 : frac < 3.5 ? 2 : frac < 7.5 ? 5 : 10;
    return mult * pow;
  }

  /* ---- shaders ---------------------------------------------------------- */

  var VERT = [
    'attribute vec3 aPos;',
    'attribute vec3 aNormal;',
    'uniform mat4 uModel;',
    'uniform mat4 uView;',
    'uniform mat4 uProj;',
    'uniform mat3 uNormalMat;',
    'varying vec3 vNormal;',
    'varying vec3 vWorld;',
    'void main() {',
    '  vec4 world = uModel * vec4(aPos, 1.0);',
    '  vWorld = world.xyz;',
    '  vNormal = normalize(uNormalMat * aNormal);',
    '  gl_Position = uProj * uView * world;',
    '}'
  ].join('\n');

  /* Section cutting is done in the fragment shader by discarding anything on the
   * far side of a plane. Cheap, exact, and it needs no CSG — and a section view
   * is one of the few things that makes an assembly legible at all. */
  var FRAG = [
    'precision mediump float;',
    'varying vec3 vNormal;',
    'varying vec3 vWorld;',
    'uniform vec3 uColor;',
    'uniform float uOpacity;',
    'uniform vec3 uLightDir;',
    'uniform vec3 uCamPos;',
    'uniform int uSectionAxis;',   // 0 none, 1 x, 2 y, 3 z
    'uniform float uSectionAt;',
    'uniform float uHighlight;',
    'void main() {',
    '  if (uSectionAxis == 1 && vWorld.x > uSectionAt) discard;',
    '  if (uSectionAxis == 2 && vWorld.y > uSectionAt) discard;',
    '  if (uSectionAxis == 3 && vWorld.z > uSectionAt) discard;',
    '  vec3 N = normalize(vNormal);',
    '  vec3 L = normalize(uLightDir);',
    '  float diff = max(dot(N, L), 0.0);',
    '  vec3 V = normalize(uCamPos - vWorld);',
    '  vec3 H = normalize(L + V);',
    '  float spec = pow(max(dot(N, H), 0.0), 28.0) * 0.32;',
    // A little rim light so silhouettes read against a dark background — the
    // difference between "a shape" and "a mass" at a glance.
    '  float rim = pow(1.0 - max(dot(N, V), 0.0), 2.5) * 0.30;',
    '  vec3 ambient = uColor * 0.30;',
    '  vec3 col = ambient + uColor * diff * 0.78 + vec3(spec) + vec3(0.31, 0.85, 0.91) * rim;',
    '  col = mix(col, vec3(0.31, 0.85, 0.91), uHighlight * 0.45);',
    '  gl_FragColor = vec4(col, uOpacity);',
    '}'
  ].join('\n');

  var LINE_VERT = [
    'attribute vec3 aPos;',
    'uniform mat4 uView;',
    'uniform mat4 uProj;',
    'void main() { gl_Position = uProj * uView * vec4(aPos, 1.0); }'
  ].join('\n');

  var LINE_FRAG = [
    'precision mediump float;',
    'uniform vec3 uColor;',
    'uniform float uOpacity;',
    'void main() { gl_FragColor = vec4(uColor, uOpacity); }'
  ].join('\n');

  function compile(gl, type, src) {
    var sh = gl.createShader(type);
    gl.shaderSource(sh, src);
    gl.compileShader(sh);
    if (!gl.getShaderParameter(sh, gl.COMPILE_STATUS)) {
      throw new Error('shader: ' + gl.getShaderInfoLog(sh));
    }
    return sh;
  }

  function program(gl, vsrc, fsrc) {
    var p = gl.createProgram();
    gl.attachShader(p, compile(gl, gl.VERTEX_SHADER, vsrc));
    gl.attachShader(p, compile(gl, gl.FRAGMENT_SHADER, fsrc));
    gl.linkProgram(p);
    if (!gl.getProgramParameter(p, gl.LINK_STATUS)) {
      throw new Error('link: ' + gl.getProgramInfoLog(p));
    }
    return p;
  }

  function hexToRGB(hex) {
    if (typeof hex !== 'string') return [0.72, 0.74, 0.78];
    var h = hex.replace('#', '');
    if (h.length === 3) h = h[0]+h[0]+h[1]+h[1]+h[2]+h[2];
    var v = parseInt(h, 16);
    if (isNaN(v)) return [0.72, 0.74, 0.78];
    return [((v >> 16) & 255) / 255, ((v >> 8) & 255) / 255, (v & 255) / 255];
  }

  /* ---- the studio ------------------------------------------------------- */

  function Studio(canvas, opts) {
    opts = opts || {};
    this.canvas = canvas;
    this.onSelect = opts.onSelect || function () {};
    this.onError = opts.onError || function () {};

    var gl = canvas.getContext('webgl', { antialias: true, alpha: false })
          || canvas.getContext('experimental-webgl', { antialias: true, alpha: false });
    if (!gl) {
      // Reported, never silently blank. A viewport that renders nothing with no
      // explanation is indistinguishable from a model that produced nothing.
      this.onError('This browser did not provide a WebGL context, so the 3D view cannot render. ' +
                   'The geometry is still available in the parts list and for export.');
      return;
    }
    this.gl = gl;
    this.prog = program(gl, VERT, FRAG);
    this.lineProg = program(gl, LINE_VERT, LINE_FRAG);

    this.parts = [];
    this.spec = null;
    this.explode = 0;
    this.section = { axis: 0, at: 0 };
    this.showGrid = true;
    this.selected = null;
    this.transparency = 1.0;

    this.camera = { yaw: 0.7, pitch: 0.5, distance: 6, target: [0, 0, 0] };
    this._bindControls();
    this._resize();

    var self = this;
    window.addEventListener('resize', function () { self._resize(); self.draw(); });
  }

  Studio.prototype._resize = function () {
    var dpr = Math.min(window.devicePixelRatio || 1, 2);
    var w = this.canvas.clientWidth || 640, h = this.canvas.clientHeight || 480;
    this.canvas.width = Math.floor(w * dpr);
    this.canvas.height = Math.floor(h * dpr);
    if (this.gl) this.gl.viewport(0, 0, this.canvas.width, this.canvas.height);
  };

  Studio.prototype._bindControls = function () {
    var self = this, dragging = false, panning = false, lastX = 0, lastY = 0;

    this.canvas.addEventListener('mousedown', function (e) {
      dragging = true;
      panning = e.button === 1 || e.shiftKey;
      lastX = e.clientX; lastY = e.clientY;
      e.preventDefault();
    });
    window.addEventListener('mouseup', function () { dragging = false; });
    window.addEventListener('mousemove', function (e) {
      if (!dragging) return;
      var dx = e.clientX - lastX, dy = e.clientY - lastY;
      lastX = e.clientX; lastY = e.clientY;
      if (panning) {
        var f = self.camera.distance * 0.0016;
        var right = normalize(cross([0,1,0], self._eyeDir()));
        var up = cross(self._eyeDir(), right);
        self.camera.target = add(self.camera.target,
          add(scale3(right, -dx * f), scale3(up, dy * f)));
      } else {
        self.camera.yaw -= dx * 0.008;
        self.camera.pitch = Math.max(-1.5, Math.min(1.5, self.camera.pitch + dy * 0.008));
      }
      self.draw();
    });
    this.canvas.addEventListener('wheel', function (e) {
      e.preventDefault();
      self.camera.distance = Math.max(0.4, Math.min(400, self.camera.distance * (1 + e.deltaY * 0.0013)));
      self.draw();
    }, { passive: false });

    // Touch: one finger orbits, two pinch to zoom.
    var lastTouch = null, lastPinch = 0;
    this.canvas.addEventListener('touchstart', function (e) {
      if (e.touches.length === 1) lastTouch = [e.touches[0].clientX, e.touches[0].clientY];
      if (e.touches.length === 2) lastPinch = touchDistance(e.touches);
    }, { passive: true });
    this.canvas.addEventListener('touchmove', function (e) {
      if (e.touches.length === 1 && lastTouch) {
        self.camera.yaw -= (e.touches[0].clientX - lastTouch[0]) * 0.01;
        self.camera.pitch = Math.max(-1.5, Math.min(1.5,
          self.camera.pitch + (e.touches[0].clientY - lastTouch[1]) * 0.01));
        lastTouch = [e.touches[0].clientX, e.touches[0].clientY];
        self.draw();
      } else if (e.touches.length === 2) {
        var d = touchDistance(e.touches);
        if (lastPinch) {
          self.camera.distance = Math.max(0.4, Math.min(400, self.camera.distance * (lastPinch / d)));
          self.draw();
        }
        lastPinch = d;
      }
      e.preventDefault();
    }, { passive: false });
  };

  function touchDistance(t) {
    var dx = t[0].clientX - t[1].clientX, dy = t[0].clientY - t[1].clientY;
    return Math.sqrt(dx*dx + dy*dy) || 1;
  }

  Studio.prototype._eyeDir = function () {
    var c = this.camera;
    return normalize([
      Math.cos(c.pitch) * Math.sin(c.yaw),
      Math.sin(c.pitch),
      Math.cos(c.pitch) * Math.cos(c.yaw)
    ]);
  };

  Studio.prototype._eye = function () {
    return add(this.camera.target, scale3(this._eyeDir(), this.camera.distance));
  };

  /* load replaces the scene from a prototype spec.
   *
   * The spec is what the model produces. Everything the renderer needs is in it,
   * and everything it carries about provenance stays attached to the part so the
   * workbench can state where a shape came from rather than implying it is
   * authoritative. */
  Studio.prototype.load = function (spec) {
    if (!this.gl) return;
    var gl = this.gl, self = this;

    (this.parts || []).forEach(function (p) {
      gl.deleteBuffer(p.buffers.position);
      gl.deleteBuffer(p.buffers.normal);
      gl.deleteBuffer(p.buffers.index);
    });

    this.spec = spec || { parts: [] };
    this.approximations = [];
    this.parts = (this.spec.parts || []).map(function (part) {
      var built = buildGeometry(part);
      var geo = built.geo;
      if (built.approximated) {
        self.approximations.push((part.name || part.id) + ': ' + built.approximated);
      }
      var buffers = {
        position: makeBuffer(gl, gl.ARRAY_BUFFER, new Float32Array(geo.positions)),
        normal:   makeBuffer(gl, gl.ARRAY_BUFFER, new Float32Array(geo.normals)),
        index:    makeBuffer(gl, gl.ELEMENT_ARRAY_BUFFER, new Uint16Array(geo.indices))
      };
      return {
        spec: part,
        buffers: buffers,
        count: geo.indices.length,
        centre: part.position || [0, 0, 0]
      };
    });

    this._frameAll();
    this.draw();
    return this.parts.length;
  };

  function makeBuffer(gl, target, data) {
    var b = gl.createBuffer();
    gl.bindBuffer(target, b);
    gl.bufferData(target, data, gl.STATIC_DRAW);
    return b;
  }

  /* _frameAll fits the camera to the model. Without it, a model in millimetres
   * and a model in metres both render — one as a speck, one filling the screen —
   * and the viewer blames the geometry. */
  Studio.prototype._frameAll = function () {
    if (!this.parts.length) return;
    var min = [Infinity, Infinity, Infinity], max = [-Infinity, -Infinity, -Infinity];
    this.parts.forEach(function (p) {
      var pos = p.spec.position || [0,0,0];
      var s = p.spec.size || {};
      var r = Math.max(num(s.width,1), num(s.height,1), num(s.depth,1), num(s.radius,0.5)*2) / 2;
      for (var i = 0; i < 3; i++) {
        min[i] = Math.min(min[i], pos[i] - r);
        max[i] = Math.max(max[i], pos[i] + r);
      }
    });
    var centre = [(min[0]+max[0])/2, (min[1]+max[1])/2, (min[2]+max[2])/2];
    var span = Math.max(max[0]-min[0], max[1]-min[1], max[2]-min[2], 0.5);
    this.camera.target = centre;
    this.camera.distance = span * 2.4;
    this.bounds = { min: min, max: max, span: span, centre: centre };
  };

  Studio.prototype.setExplode = function (v) { this.explode = v; this.draw(); };
  Studio.prototype.setTransparency = function (v) { this.transparency = v; this.draw(); };
  Studio.prototype.setGrid = function (on) { this.showGrid = !!on; this.draw(); };
  Studio.prototype.select = function (id) { this.selected = id; this.draw(); };

  Studio.prototype.setSection = function (axis, t) {
    var map = { none: 0, x: 1, y: 2, z: 3 };
    this.section.axis = map[axis] || 0;
    if (this.bounds) {
      var i = Math.max(0, this.section.axis - 1);
      this.section.at = this.bounds.min[i] + (this.bounds.max[i] - this.bounds.min[i]) * t;
    }
    this.draw();
  };

  Studio.prototype.resetView = function () { this.camera.yaw = 0.7; this.camera.pitch = 0.5; this._frameAll(); this.draw(); };
  Studio.prototype.viewFrom = function (which) {
    var v = { front: [0, 0], top: [0, 1.5], side: [Math.PI/2, 0], iso: [0.7, 0.5] }[which] || [0.7, 0.5];
    this.camera.yaw = v[0]; this.camera.pitch = v[1];
    this.draw();
  };

  Studio.prototype.draw = function () {
    if (!this.gl) return;
    var gl = this.gl;
    var w = this.canvas.width, h = this.canvas.height;

    gl.viewport(0, 0, w, h);
    gl.clearColor(0.043, 0.059, 0.094, 1);
    gl.clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT);
    gl.enable(gl.DEPTH_TEST);
    gl.enable(gl.CULL_FACE);
    gl.enable(gl.BLEND);
    gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA);

    var eye = this._eye();
    var view = lookAt(eye, this.camera.target, [0, 1, 0]);
    var proj = perspective(45, w / Math.max(1, h), 0.05, Math.max(200, this.camera.distance * 8));

    if (this.showGrid) this._drawGrid(view, proj);

    gl.useProgram(this.prog);
    var P = this.prog;
    var loc = {
      pos: gl.getAttribLocation(P, 'aPos'),
      nrm: gl.getAttribLocation(P, 'aNormal'),
      model: gl.getUniformLocation(P, 'uModel'),
      view: gl.getUniformLocation(P, 'uView'),
      proj: gl.getUniformLocation(P, 'uProj'),
      nmat: gl.getUniformLocation(P, 'uNormalMat'),
      color: gl.getUniformLocation(P, 'uColor'),
      opacity: gl.getUniformLocation(P, 'uOpacity'),
      light: gl.getUniformLocation(P, 'uLightDir'),
      cam: gl.getUniformLocation(P, 'uCamPos'),
      secAxis: gl.getUniformLocation(P, 'uSectionAxis'),
      secAt: gl.getUniformLocation(P, 'uSectionAt'),
      highlight: gl.getUniformLocation(P, 'uHighlight')
    };
    gl.uniformMatrix4fv(loc.view, false, view);
    gl.uniformMatrix4fv(loc.proj, false, proj);
    gl.uniform3fv(loc.light, normalize([0.45, 0.85, 0.5]));
    gl.uniform3fv(loc.cam, eye);
    gl.uniform1i(loc.secAxis, this.section.axis);
    gl.uniform1f(loc.secAt, this.section.at);

    var self = this;
    // Opaque first, then transparent back-to-front, so a translucent housing
    // does not erase what is inside it.
    var order = this.parts.slice().sort(function (a, b) {
      var oa = num(a.spec.opacity, 1) * self.transparency;
      var ob = num(b.spec.opacity, 1) * self.transparency;
      if ((oa >= 1) !== (ob >= 1)) return oa >= 1 ? -1 : 1;
      var da = length3(sub(eye, a.spec.position || [0,0,0]));
      var db = length3(sub(eye, b.spec.position || [0,0,0]));
      return db - da;
    });

    order.forEach(function (part) {
      var s = part.spec;
      var pos = (s.position || [0, 0, 0]).slice();

      // Exploded view: parts move outward from the assembly centre, so the
      // relationship between them stays readable while the gap opens.
      if (self.explode > 0 && self.bounds) {
        var dir = sub(pos, self.bounds.centre);
        if (length3(dir) < 1e-6) dir = [0, 1, 0];
        pos = add(pos, scale3(normalize(dir), self.explode * self.bounds.span * 0.6));
      }

      var model = multiply(translation(pos),
                    multiply(rotationXYZ(s.rotation || [0,0,0]),
                             scaling(s.scale || [1,1,1])));
      gl.uniformMatrix4fv(loc.model, false, model);
      gl.uniformMatrix3fv(loc.nmat, false, normalMatrix(model));
      gl.uniform3fv(loc.color, hexToRGB(s.color || '#b8bcc4'));
      gl.uniform1f(loc.opacity, num(s.opacity, 1) * self.transparency);
      gl.uniform1f(loc.highlight, self.selected === s.id ? 1 : 0);

      gl.bindBuffer(gl.ARRAY_BUFFER, part.buffers.position);
      gl.enableVertexAttribArray(loc.pos);
      gl.vertexAttribPointer(loc.pos, 3, gl.FLOAT, false, 0, 0);
      gl.bindBuffer(gl.ARRAY_BUFFER, part.buffers.normal);
      gl.enableVertexAttribArray(loc.nrm);
      gl.vertexAttribPointer(loc.nrm, 3, gl.FLOAT, false, 0, 0);
      gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, part.buffers.index);
      gl.drawElements(gl.TRIANGLES, part.count, gl.UNSIGNED_SHORT, 0);
    });
  };

  /* The grid is a scale reference (PRD VIS-02), not decoration. Without one a
   * render has no size at all, and "how big is this?" is the first question
   * anyone asks of a prototype.
   *
   * # Two things this has to get right
   *
   * It sits BELOW the model, not at y=0. A part resting on the origin is
   * coplanar with a grid drawn there, and the two z-fight into a moiré that
   * reads as a surface defect in the part — the render inventing a texture that
   * is not in the geometry.
   *
   * Its spacing follows the model's size. A 42mm bracket against a 1-unit grid
   * is a bracket on graph paper: the reference conveys nothing because the
   * lines are too dense to count. The step is chosen as a round number near a
   * tenth of the model's span, so the lines are countable at any scale. */
  Studio.prototype._drawGrid = function (view, proj) {
    var gl = this.gl;

    var span = (this.bounds && this.bounds.span) || 10;
    var step = niceStep(span / 10);
    var floor = this.bounds ? this.bounds.min[1] - span * 0.02 : 0;

    if (!this._gridBuffer || this._gridStep !== step || this._gridFloor !== floor) {
      if (this._gridBuffer) gl.deleteBuffer(this._gridBuffer);
      var verts = [], n = 24;
      for (var i = -n; i <= n; i++) {
        verts.push(i*step, floor, -n*step, i*step, floor, n*step);
        verts.push(-n*step, floor, i*step, n*step, floor, i*step);
      }
      this._gridBuffer = makeBuffer(gl, gl.ARRAY_BUFFER, new Float32Array(verts));
      this._gridCount = verts.length / 3;
      this._gridStep = step;
      this._gridFloor = floor;
    }
    gl.useProgram(this.lineProg);
    var pos = gl.getAttribLocation(this.lineProg, 'aPos');
    gl.uniformMatrix4fv(gl.getUniformLocation(this.lineProg, 'uView'), false, view);
    gl.uniformMatrix4fv(gl.getUniformLocation(this.lineProg, 'uProj'), false, proj);
    gl.uniform3fv(gl.getUniformLocation(this.lineProg, 'uColor'), [0.16, 0.22, 0.31]);
    gl.uniform1f(gl.getUniformLocation(this.lineProg, 'uOpacity'), 0.55);
    gl.bindBuffer(gl.ARRAY_BUFFER, this._gridBuffer);
    gl.enableVertexAttribArray(pos);
    gl.vertexAttribPointer(pos, 3, gl.FLOAT, false, 0, 0);
    gl.drawArrays(gl.LINES, 0, this._gridCount);
  };

  /* snapshot returns a PNG data URL of the current view.
   *
   * preserveDrawingBuffer is off for performance, so the canvas must be redrawn
   * in the same frame as the read — otherwise the buffer has already been
   * cleared and the snapshot comes back blank. That is a classic WebGL trap and
   * the reason this is a method rather than a caller's toDataURL. */
  Studio.prototype.snapshot = function () {
    this.draw();
    return this.canvas.toDataURL('image/png');
  };

  /* approximationNotes returns what this render did NOT draw faithfully.
   * The workbench shows these in the provenance banner. */
  Studio.prototype.approximationNotes = function () {
    return (this.approximations || []).slice();
  };

  global.Forge3D = {
    supportedShapes: SUPPORTED,
    Studio: Studio,
    geometry: {
      box: boxGeometry, cylinder: cylinderGeometry,
      sphere: sphereGeometry, plane: planeGeometry
    }
  };
})(window);
