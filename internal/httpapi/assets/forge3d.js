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

  /* Column-major, matching multiply() below and what WebGL expects: element
   * [c*4+r] is column c, row r. Getting this backwards produces a projection
   * that looks almost right until the camera moves off-axis. */
  function mulMat4Vec4(m, v) {
    var out = [0, 0, 0, 0];
    for (var r = 0; r < 4; r++) {
      out[r] = m[0*4+r]*v[0] + m[1*4+r]*v[1] + m[2*4+r]*v[2] + m[3*4+r]*v[3];
    }
    return out;
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
  /* ---- extrusions -------------------------------------------------------
   *
   * A closed outline in the part's own XY plane, swept along local Z.
   *
   * # Why ear clipping and not a triangle fan
   *
   * A fan from the first vertex is four lines and is WRONG for any concave
   * outline, which is most of the interesting ones — an L-bracket is concave by
   * definition, and a fan across its inner corner draws triangles outside the
   * part. The first shape anybody makes with this feature would be drawn wrong.
   *
   * # Why this is a second implementation
   *
   * internal/domain/geometry/triangulate.go does the same thing for the mesh
   * exporters, which cannot run in a browser. The duplication is real and this
   * codebase has recorded what two copies of one rule cost — so what is shared
   * is the PROPERTY rather than the code: any correct triangulation of an
   * outline covers exactly the outline's area, so the two agree about the SHAPE
   * however they each cut it up. That is not true of curve tessellation, which
   * is why the segment counts are fenced across the boundary and this is not.
   */
  function signedArea2D(pts) {
    var a = 0;
    for (var i = 0; i < pts.length; i++) {
      var j = (i + 1) % pts.length;
      a += pts[i][0] * pts[j][1] - pts[j][0] * pts[i][1];
    }
    return a / 2;
  }

  function cross2D(a, b, c) {
    return (b[0] - a[0]) * (c[1] - b[1]) - (b[1] - a[1]) * (c[0] - b[0]);
  }

  function pointInTriangle2D(p, a, b, c) {
    var d1 = cross2D(a, b, p), d2 = cross2D(b, c, p), d3 = cross2D(c, a, p);
    var neg = d1 < 0 || d2 < 0 || d3 < 0;
    var pos = d1 > 0 || d2 > 0 || d3 > 0;
    return !(neg && pos);
  }

  /* Returns { pts, tris } — the points in the order the triangles index into,
   * which may be reversed. Returning them is not a convenience: keeping a
   * separate copy is how the caps come out normalised and the side walls do
   * not, which draws a clockwise outline inside out. */
  function earClip(input) {
    if (input.length < 3) return { pts: input, tris: [] };
    var pts = input;
    if (signedArea2D(pts) < 0) pts = input.slice().reverse();

    var idx = [], i;
    for (i = 0; i < pts.length; i++) idx.push(i);
    var tris = [], guard = 0;

    while (idx.length > 3) {
      var clipped = false;
      for (i = 0; i < idx.length; i++) {
        var prev = idx[(i - 1 + idx.length) % idx.length];
        var cur = idx[i];
        var next = idx[(i + 1) % idx.length];
        if (cross2D(pts[prev], pts[cur], pts[next]) <= 0) continue;
        var clear = true;
        for (var k = 0; k < idx.length && clear; k++) {
          var o = idx[k];
          if (o === prev || o === cur || o === next) continue;
          if (pointInTriangle2D(pts[o], pts[prev], pts[cur], pts[next])) clear = false;
        }
        if (!clear) continue;
        tris.push([prev, cur, next]);
        idx.splice(i, 1);
        clipped = true;
        break;
      }
      /* A pass that removed nothing means the outline crosses itself. Stopping
       * matters more here than anywhere else in this file: this runs in the
       * browser's main thread, and a loop that never ends is a tab that never
       * responds again. */
      if (!clipped) { guard++; if (guard > 1) return { pts: pts, tris: tris }; }
    }
    if (idx.length === 3) tris.push([idx[0], idx[1], idx[2]]);
    return { pts: pts, tris: tris };
  }

  function extrusionGeometry(profile, depth) {
    var raw = (profile || []).map(function (p) { return [num(p.x, 0), num(p.y, 0)]; });
    if (raw.length < 3) {
      return {
        geo: boxGeometry(1, 1, num(depth, 1)),
        approximated: 'this outline has fewer than three points and encloses nothing, ' +
                      'so it is drawn as a unit box'
      };
    }
    var clipped = earClip(raw);
    var pts = clipped.pts, tris = clipped.tris;
    if (!tris.length) {
      return {
        geo: boxGeometry(1, 1, num(depth, 1)),
        approximated: 'this outline could not be closed into a surface — it crosses itself ' +
                      'or repeats a point — so it is drawn as a unit box'
      };
    }

    var half = num(depth, 1) / 2;
    var positions = [], normals = [], indices = [], n = 0;
    function vert(x, y, z, nx, ny, nz) {
      positions.push(x, y, z); normals.push(nx, ny, nz); indices.push(n++);
    }

    tris.forEach(function (t) {
      vert(pts[t[0]][0], pts[t[0]][1], half, 0, 0, 1);
      vert(pts[t[1]][0], pts[t[1]][1], half, 0, 0, 1);
      vert(pts[t[2]][0], pts[t[2]][1], half, 0, 0, 1);
      // Reversed, so the bottom cap faces away from the solid too.
      vert(pts[t[2]][0], pts[t[2]][1], -half, 0, 0, -1);
      vert(pts[t[1]][0], pts[t[1]][1], -half, 0, 0, -1);
      vert(pts[t[0]][0], pts[t[0]][1], -half, 0, 0, -1);
    });

    for (var i = 0; i < pts.length; i++) {
      var j = (i + 1) % pts.length;
      var dx = pts[j][0] - pts[i][0], dy = pts[j][1] - pts[i][1];
      var len = Math.sqrt(dx * dx + dy * dy) || 1;
      // Outward for a counter-clockwise outline: on a square wound
      // counter-clockwise the bottom edge runs +x and the outside is -y.
      var nx = dy / len, ny = -dx / len;
      vert(pts[i][0], pts[i][1], -half, nx, ny, 0);
      vert(pts[j][0], pts[j][1], -half, nx, ny, 0);
      vert(pts[j][0], pts[j][1], half, nx, ny, 0);
      vert(pts[i][0], pts[i][1], -half, nx, ny, 0);
      vert(pts[j][0], pts[j][1], half, nx, ny, 0);
      vert(pts[i][0], pts[i][1], half, nx, ny, 0);
    }
    return { geo: { positions: positions, normals: normals, indices: indices } };
  }

  function buildGeometry(part) {
    var s = part.size || {};
    switch (part.shape) {
      case 'box':      return { geo: boxGeometry(num(s.width,1), num(s.height,1), num(s.depth,1)) };
      case 'cylinder': return { geo: cylinderGeometry(num(s.radius,0.5), num(s.height,1), TESSELLATION.radial, num(s.radius_top, num(s.radius,0.5))) };
      case 'cone':     return { geo: cylinderGeometry(num(s.radius,0.5), num(s.height,1), TESSELLATION.radial, 0) };
      case 'sphere':   return { geo: sphereGeometry(num(s.radius,0.5), TESSELLATION.sphereRadial) };
      case 'plane':    return { geo: planeGeometry(num(s.width,1), num(s.depth,1)) };
      case 'extrusion': return extrusionGeometry(part.profile || [], num(s.depth, 1));
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
    'uniform float uSpecPower;',
    'uniform float uSpecGloss;',
    'void main() {',
    '  if (uSectionAxis == 1 && vWorld.x > uSectionAt) discard;',
    '  if (uSectionAxis == 2 && vWorld.y > uSectionAt) discard;',
    '  if (uSectionAxis == 3 && vWorld.z > uSectionAt) discard;',
    '  vec3 N = normalize(vNormal);',
    '  vec3 L = normalize(uLightDir);',
    '  float diff = max(dot(N, L), 0.0);',
    '  vec3 V = normalize(uCamPos - vWorld);',
    '  vec3 H = normalize(L + V);',
    '  float spec = pow(max(dot(N, H), 0.0), uSpecPower) * uSpecGloss;',
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

  /* How material being removed is drawn.
   *
   * Faint enough to read as absence rather than as a translucent SOLID — a
   * housing somebody made see-through is a real part and must not look like
   * this — and visible enough that a person can tell where the hole will be. The
   * colour is the warning gold this interface already uses for "quoted from
   * memory, not checked", because both mean the same thing to a reader: what you
   * are looking at is not the whole story. */
  var REMOVED_ALPHA = 0.22;
  var REMOVED_COLOUR = '#e6cd8f';

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
    this.overlays = [];
    this.showOverlays = false;
    this.state = null;
    /* The layer DOM labels are placed into, when the host gives us one. Without
     * it the lines still draw and the numbers do not — which is the safe half to
     * lose, since a line with no number claims nothing. */
    this.labelLayer = (opts && opts.labels) || null;
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

    /* Parts that are material being REMOVED, not material that is there.
     *
     * A cut feature names a part as the tool that makes a hole, and the CAD
     * kernel consumes it: the exported solid has a void where it was. This
     * renderer has no boolean operations and cannot make that void, so without
     * this the four bolt holes of a bracket are drawn as four solid posts
     * standing on the plate — the exact opposite of what they are.
     *
     * It cannot be fixed by drawing the hole. It CAN be stopped from reading as
     * a post: a tool is drawn as a ghost, and the provenance banner says which
     * shape the exported file has. Same stance as "Drawn approximately" — say
     * what was done instead of hiding it. */
    var removed = {};
    (this.spec.features || []).forEach(function (f) {
      if (!f || String(f.op).toLowerCase() !== 'cut') return;
      (f.with || []).forEach(function (id) { removed[id] = true; });
    });

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
        centre: part.position || [0, 0, 0],
        // Held on the WRAPPER and never written into spec: the document on
        // screen has to stay the document that was stored, so a presentation
        // decision must not become a value the model appears to have stated.
        removed: !!removed[part.id]
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

  /* Engineering overlays (PRD VIS-03).
   *
   * Two lists, kept apart all the way to the screen. `authored` is what somebody
   * put on the model — a dimension off a drawing, a tolerance, a datum.
   * `measured` is what FORGE derived from the parts. They are drawn
   * DIFFERENTLY on purpose, and that is the whole requirement: "engineering
   * overlays without confusing appearance with validated data".
   *
   * A dimension line with a number on it is the most authoritative mark you can
   * put on a picture. It is what a drawing IS. So the encoding borrows the
   * convention an engineer already reads: a value that came from outside FORGE
   * is drawn SOLID, and a value FORGE worked out itself is drawn DASHED, the way
   * a reference dimension is. Nobody has to learn a legend to be warned. */
  Studio.prototype.setOverlays = function (authored, measured) {
    this.overlays = (authored || []).concat(measured || []);
    this.draw();
  };

  Studio.prototype.setOverlaysVisible = function (on) {
    this.showOverlays = !!on;
    this.draw();
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
      highlight: gl.getUniformLocation(P, 'uHighlight'),
      specPower: gl.getUniformLocation(P, 'uSpecPower'),
      specGloss: gl.getUniformLocation(P, 'uSpecGloss')
    };
    gl.uniformMatrix4fv(loc.view, false, view);
    gl.uniformMatrix4fv(loc.proj, false, proj);
    gl.uniform3fv(loc.light, normalize([0.45, 0.85, 0.5]));
    gl.uniform3fv(loc.cam, eye);
    gl.uniform1i(loc.secAxis, this.section.axis);
    gl.uniform1f(loc.secAt, this.section.at);

    var self = this;
    /* One function, read by both the sort and the draw. Two copies would
     * eventually disagree, and a part sorted as opaque and drawn translucent is
     * a part that erases whatever is behind it. */
    var alphaOf = function (p) {
      return p.removed ? REMOVED_ALPHA : num(p.spec.opacity, 1) * self.transparency;
    };
    // Opaque first, then transparent back-to-front, so a translucent housing
    // does not erase what is inside it.
    var order = this.parts.slice().sort(function (a, b) {
      var oa = alphaOf(a);
      var ob = alphaOf(b);
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

      /* PRD VIS-02. The active assembly state moves parts and hides them. It is
       * applied here rather than baked into the loaded geometry so that
       * switching states costs a redraw instead of a rebuild, and so the
       * document on screen stays the document that was stored. */
      var st = self._stateFor(s.id);
      if (st.hidden) return;
      if (st.offset) pos = add(pos, st.offset);

      var model = multiply(translation(pos),
                    multiply(rotationXYZ(s.rotation || [0,0,0]),
                             scaling(s.scale || [1,1,1])));
      gl.uniformMatrix4fv(loc.model, false, model);
      gl.uniformMatrix3fv(loc.nmat, false, normalMatrix(model));
      gl.uniform3fv(loc.color, hexToRGB(part.removed ? REMOVED_COLOUR : (s.color || '#b8bcc4')));
      gl.uniform1f(loc.opacity, alphaOf(part));
      gl.uniform1f(loc.highlight, self.selected === s.id ? 1 : 0);
      /* The finish, as the document declared it. Not looked up from the material
       * NAME: that table would have to exist here and in Go, and this codebase
       * has already recorded what two copies of one rule cost. */
      var sh = shadingFor(s.material);
      gl.uniform1f(loc.specPower, sh[0]);
      gl.uniform1f(loc.specGloss, sh[1]);

      gl.bindBuffer(gl.ARRAY_BUFFER, part.buffers.position);
      gl.enableVertexAttribArray(loc.pos);
      gl.vertexAttribPointer(loc.pos, 3, gl.FLOAT, false, 0, 0);
      gl.bindBuffer(gl.ARRAY_BUFFER, part.buffers.normal);
      gl.enableVertexAttribArray(loc.nrm);
      gl.vertexAttribPointer(loc.nrm, 3, gl.FLOAT, false, 0, 0);
      gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, part.buffers.index);
      gl.drawElements(gl.TRIANGLES, part.count, gl.UNSIGNED_SHORT, 0);
    });

    /* PRD VIS-03, drawn last so the marks sit over the model rather than
     * inside it, and placed last so the numbers follow the same camera the
     * lines were drawn with — computing them from a stale matrix is how a
     * label ends up beside the wrong feature. */
    if (this.showOverlays) this._drawOverlays(view, proj);
    this._placeLabels(view, proj);
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

  /* What each finish looks like (PRD VIS-02).
   *
   * The renderer owns this and the server does not. Go holds the closed set of
   * finish NAMES, because it validates them and describes them to the model;
   * these two numbers are the specular exponent and its strength, which nothing
   * outside this file has any use for. Splitting the table by who needs it is
   * what stops the two halves drifting — neither side holds the other's.
   *
   * An unknown finish falls back rather than failing: the material's NAME is the
   * claim, and losing the look is a much smaller harm than losing the part. */
  var FINISHES = {
    metal:      [42, 0.55],
    painted:    [26, 0.30],
    plastic:    [18, 0.22],
    glass:      [60, 0.70],
    rubber:     [6,  0.05],
    unfinished: [20, 0.18]
  };

  function shadingFor(material) {
    var f = material && material.finish;
    return FINISHES[f] || FINISHES.unfinished;
  }

  /* _stateFor resolves what the active assembly state does to one part. */
  Studio.prototype._stateFor = function (id) {
    var st = this.state;
    if (!st) return {};
    if (st.hidden && st.hidden.indexOf(id) >= 0) return { hidden: true };
    var off = st.offsets && st.offsets[id];
    return off && off.length === 3 ? { offset: off } : {};
  };

  /* setState selects a named assembly state, or null for the assembly as
   * modelled. Redraws rather than reloads: switching states must not rebuild
   * geometry, and the document on screen stays the document that was stored. */
  Studio.prototype.setState = function (state) {
    this.state = state || null;
    this.draw();
  };

  /* fromOutside reports whether an overlay's value came from beyond FORGE.
   *
   * The same two labels the server enforces for tolerances (geometry/overlay.go).
   * Kept as one predicate rather than scattered comparisons so the line style,
   * the colour and the chip text can never disagree about which side a value is
   * on — three places deciding independently is how a dashed line ends up with
   * a "from a drawing" chip. */
  function fromOutside(o) {
    return o && (o.how === 'observed' || o.how === 'retrieved');
  }

  /* _overlaySegments turns one dimension into line segments in model space.
   *
   * A dimension is the span plus a tick at each end, perpendicular to it, so the
   * mark reads as a measurement rather than as an edge of the model. Derived
   * dimensions are broken into dashes here rather than with a line style,
   * because WebGL has no dashed lines — the dashes ARE separate segments. */
  function overlaySegments(o, tick) {
    var a = o.from, b = o.to;
    if (!a || !b || a.length !== 3 || b.length !== 3) return [];

    var dir = normalize(sub(b, a));
    /* Any vector not parallel to the span gives a usable tick direction. */
    var ref = Math.abs(dir[1]) > 0.9 ? [1, 0, 0] : [0, 1, 0];
    var side = scale3(normalize(cross(dir, ref)), tick);

    var out = [];
    if (fromOutside(o)) {
      out.push(a[0], a[1], a[2], b[0], b[1], b[2]);
    } else {
      /* Eight dashes along the span. A count rather than a fixed length, so the
       * mark reads the same on a 4 mm pin and a 4 m beam. */
      var n = 8;
      for (var i = 0; i < n; i++) {
        var t0 = i / n, t1 = t0 + 0.5 / n;
        out.push(a[0] + (b[0]-a[0])*t0, a[1] + (b[1]-a[1])*t0, a[2] + (b[2]-a[2])*t0,
                 a[0] + (b[0]-a[0])*t1, a[1] + (b[1]-a[1])*t1, a[2] + (b[2]-a[2])*t1);
      }
    }
    /* Ticks are solid either way: they mark where the measurement was taken,
     * which is not in question. */
    out.push(a[0]-side[0], a[1]-side[1], a[2]-side[2], a[0]+side[0], a[1]+side[1], a[2]+side[2]);
    out.push(b[0]-side[0], b[1]-side[1], b[2]-side[2], b[0]+side[0], b[1]+side[1], b[2]+side[2]);
    return out;
  }

  /* A datum is drawn as a short cross at its position — a mark saying "measure
   * from here", not a length. */
  function datumSegments(o, tick) {
    var p = o.from;
    if (!p || p.length !== 3) return [];
    var out = [];
    for (var i = 0; i < 3; i++) {
      var d = [0, 0, 0];
      d[i] = tick;
      out.push(p[0]-d[0], p[1]-d[1], p[2]-d[2], p[0]+d[0], p[1]+d[1], p[2]+d[2]);
    }
    return out;
  }

  Studio.prototype._drawOverlays = function (view, proj) {
    var gl = this.gl;
    if (!this.overlays.length) return;

    var span = (this.bounds && this.bounds.span) || 10;
    var tick = span * 0.02;

    var stated = [], derived = [];
    this.overlays.forEach(function (o) {
      /* Anything that is not a span gets the anchor cross: a datum marks where
       * measurements are taken from, a note marks what it is about. */
      var segs = o.kind === 'dimension' ? overlaySegments(o, tick) : datumSegments(o, tick);
      (fromOutside(o) ? stated : derived).push.apply(fromOutside(o) ? stated : derived, segs);
    });

    /* Depth testing OFF for overlays. A dimension is an annotation ON the
     * drawing, not an object in the scene: one that vanishes behind the part it
     * measures is worse than useless, because the reader sees a number with no
     * visible extent and cannot tell which feature it belongs to. */
    gl.disable(gl.DEPTH_TEST);
    this._drawSegments(stated,  view, proj, [0.55, 0.85, 0.95], 0.95);
    this._drawSegments(derived, view, proj, [0.52, 0.60, 0.72], 0.85);
    gl.enable(gl.DEPTH_TEST);
  };

  Studio.prototype._drawSegments = function (verts, view, proj, colour, opacity) {
    if (!verts.length) return;
    var gl = this.gl;
    if (this._olBuffer) gl.deleteBuffer(this._olBuffer);
    this._olBuffer = makeBuffer(gl, gl.ARRAY_BUFFER, new Float32Array(verts));

    gl.useProgram(this.lineProg);
    var pos = gl.getAttribLocation(this.lineProg, 'aPos');
    gl.uniformMatrix4fv(gl.getUniformLocation(this.lineProg, 'uView'), false, view);
    gl.uniformMatrix4fv(gl.getUniformLocation(this.lineProg, 'uProj'), false, proj);
    gl.uniform3fv(gl.getUniformLocation(this.lineProg, 'uColor'), colour);
    gl.uniform1f(gl.getUniformLocation(this.lineProg, 'uOpacity'), opacity);
    gl.bindBuffer(gl.ARRAY_BUFFER, this._olBuffer);
    gl.enableVertexAttribArray(pos);
    gl.vertexAttribPointer(pos, 3, gl.FLOAT, false, 0, 0);
    gl.drawArrays(gl.LINES, 0, verts.length / 3);
  };

  /* project maps a model-space point to canvas pixels, or null if it is behind
   * the camera. Behind-the-camera points project to a mathematically valid
   * position on the wrong side of the screen, so they are dropped rather than
   * drawn somewhere misleading. */
  function project(p, view, proj, w, h) {
    var v = mulMat4Vec4(view, [p[0], p[1], p[2], 1]);
    var c = mulMat4Vec4(proj, v);
    if (c[3] <= 0.0001) return null;
    return {
      x: (c[0] / c[3] * 0.5 + 0.5) * w,
      y: (1 - (c[1] / c[3] * 0.5 + 0.5)) * h
    };
  }

  /* _placeLabels positions the numbers as HTML over the canvas.
   *
   * DOM rather than glyphs in GL. A dimension whose text is a blurry texture is
   * a dimension somebody misreads, and this viewer has no font atlas — building
   * one to draw eight numbers would be a lot of machinery to make the type
   * worse. The cost is that labels only appear where a host gave us a layer. */
  Studio.prototype._placeLabels = function (view, proj) {
    var layer = this.labelLayer;
    if (!layer) return;
    if (!this.showOverlays || !this.overlays.length) { layer.innerHTML = ''; return; }

    var rect = this.canvas.getBoundingClientRect();
    var w = rect.width, h = rect.height;
    var html = [];

    this.overlays.forEach(function (o) {
      var anchor = o.kind !== 'dimension'
        ? o.from
        : (o.from && o.to ? [(o.from[0]+o.to[0])/2, (o.from[1]+o.to[1])/2, (o.from[2]+o.to[2])/2] : null);
      if (!anchor || anchor.length !== 3) return;
      var at = project(anchor, view, proj, w, h);
      if (!at) return;

      var outside = fromOutside(o);
      /* A datum marks a reference and carries no magnitude, so it has nothing to
       * put here — its name is already in the label. Repeating it produced
       * "A A", which reads as a second mark rather than as one. */
      var text = '';
      if (o.kind === 'dimension') {
        text = esc(String(o.value)) + ' ' + esc(o.unit || '');
        if (o.tolerance) text += ' <b>' + esc(o.tolerance) + '</b>';
      } else if (o.kind === 'note') {
        /* An annotation IS its text. The label names what it is about and the
         * note says the thing, which is the opposite way round from a
         * dimension, where the number is the point. */
        text = esc(o.note || '');
      }
      /* The chip is not decoration and is not optional. VIS-03's whole clause is
       * "without confusing appearance with validated data" — a mark floating
       * over a render, with no statement of where it came from, is exactly that
       * confusion.
       *
       * "from the model" is the plain-language form of `calculated`, and it is
       * only true of something measured off the geometry. Every other FORGE-side
       * label keeps its own word: a datum FORGE picked is `proposed`, and
       * calling that "from the model" would claim it was derived from the shape
       * when it is a guess at somebody's intent. */
      var chipText = outside || o.how !== 'calculated' ? o.how : 'from the model';
      var chip = '<i class="' + (outside ? 'dim-src' : 'dim-model') + '">' +
                 esc(chipText) + '</i>';

      html.push(
        '<div class="dim' + (outside ? ' dim-stated' : '') + '"' +
        ' style="left:' + at.x.toFixed(1) + 'px;top:' + at.y.toFixed(1) + 'px"' +
        ' title="' + esc(o.note || o.source || '') + '">' +
        '<span class="dim-label">' + esc(o.label) + '</span>' +
        '<span class="dim-value">' + text + '</span>' + chip + '</div>');
    });

    layer.innerHTML = html.join('');
    this._spreadLabels(layer);
  };

  /* _spreadLabels pushes overlapping labels apart, downward.
   *
   * Dimensions cluster: the extents of a model all anchor near its middle, and
   * from most camera angles several midpoints project within a few pixels of
   * each other. Left alone they stack into an unreadable pile — which on a
   * drawing is not a cosmetic problem. VIS-03 is "overlays WITHOUT confusing
   * appearance with validated data", and two numbers overlapping so that one
   * reads as part of the other is that confusion in its most literal form.
   *
   * Measured rather than estimated. Guessing a label's width from its character
   * count is wrong the moment the font loads differently or a tolerance is long,
   * and the failure mode is silent. One forced layout per draw for a handful of
   * elements is cheap next to the frame that was just rendered.
   *
   * Downward only, and never upward: a label that moves has to stay BELOW its
   * anchor so the eye still travels from the mark to the number in one
   * direction. */
  Studio.prototype._spreadLabels = function (layer) {
    var nodes = layer.children;
    if (nodes.length < 2) return;

    var boxes = [];
    for (var i = 0; i < nodes.length; i++) {
      var r = nodes[i].getBoundingClientRect();
      boxes.push({ node: nodes[i], top: r.top, left: r.left, w: r.width, h: r.height, shift: 0 });
    }
    boxes.sort(function (a, b) { return a.top - b.top; });

    var gap = 3;
    for (var j = 1; j < boxes.length; j++) {
      for (var k = 0; k < j; k++) {
        var a = boxes[k], b = boxes[j];
        var aTop = a.top + a.shift, bTop = b.top + b.shift;
        /* Only a real overlap moves anything: two labels far apart horizontally
         * are both readable however close their vertical positions are. */
        var overlapX = a.left < b.left + b.w && b.left < a.left + a.w;
        var overlapY = aTop < bTop + b.h && bTop < aTop + a.h;
        if (overlapX && overlapY) {
          b.shift += (aTop + a.h + gap) - bTop;
        }
      }
    }
    boxes.forEach(function (box) {
      if (box.shift) {
        box.node.style.marginTop = box.shift.toFixed(1) + 'px';
      }
    });
  };

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

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
