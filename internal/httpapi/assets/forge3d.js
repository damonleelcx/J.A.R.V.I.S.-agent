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

  /* A document's rotation is in DEGREES; this matrix wants radians.
   *
   * Nothing used to convert, and nothing said which unit the document was in.
   * A model asked for a wheel writes 90 for a quarter turn — every non-zero
   * rotation ever stored in this deployment was [0, 0, 90] on a wheel, and read
   * as radians that is 116.8 degrees, so the car's wheels sat tilted 27 degrees
   * off vertical and looked like a modelling mistake.
   *
   * Kept in step with geometry.Part.RotationRadians by
   * TestTheRendererTurnsDegreesLikeTheBuilderDoes: the stage and the exported
   * file must not disagree about where a part is pointing. */
  function rotationRadians(r) {
    var out = [0, 0, 0];
    for (var i = 0; i < 3; i++) out[i] = num(r && r[i], 0) * Math.PI / 180;
    return out;
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

  /* How many times finer than TESSELLATION a curve is drawn right now (A5, 2026-09-18).
   *
   * 1 everywhere except inside geometryAtDetail, which draws a curved primitive that
   * covers a lot of the screen with 2, 4 or 8 times the segments (see "Curves as fine
   * as the screen needs"). The counts above stay what the exporter uses: the level at
   * DETAIL 1 is the surface the file carries, and a finer level only lies closer to the
   * true curve than the file does — the export's stated chord deviation is still an
   * upper bound on what is on screen. flattenDrawing's arithmetic, which Go's parity
   * fences compare, is only ever read at 1. */
  var DETAIL = 1;
  function radialSegments() { return TESSELLATION.radial * DETAIL; }

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
      idx.push(a, d, b, a, c, d);
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
        if (flip) idx.push(centre, centre+s4+1, centre+s4+2);
        else idx.push(centre, centre+s4+2, centre+s4+1);
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
        idx.push(a, a+1, b, b, a+1, b+1);
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

  /* Every shape buildGeometry has a case for. It went stale the moment outlines
   * arrived — 'extrusion' and 'revolve' were drawn correctly and named nowhere,
   * which makes a list called "supported shapes" say the opposite of the truth
   * about three of them. */
  var SUPPORTED = ['box', 'cylinder', 'cone', 'sphere', 'plane',
                   'extrusion', 'revolve', 'sweep', 'section', 'gear', 'standard'];

  /* ---- standard parts ----------------------------------------------------
   *
   * The browser's copy of internal/domain/geometry/standard.go (Phase 2, stage A3):
   * a designation — "ISO 4762 M8x30", "ISO 15 608" — written out as the revolve or
   * extrusion the exporter builds, at the published figures in the document's units.
   * Every row is the Go table's row, and every expression is written in the order Go
   * writes it, so the same doubles come out. A copy for the reason the gear has one:
   * the browser cannot call Go, and a copy that drifted would draw a screw the STEP
   * file does not hold. TestRendererDrawsTheSameStandardPartAsTheExporter holds every
   * designation in the catalogue to Go's answer. */
  var ISO4762_LENGTHS = [5, 6, 8, 10, 12, 16, 20, 25, 30, 35, 40, 45, 50, 55, 60, 65, 70, 80, 90, 100, 110, 120];
  /* size, d, head diameter dk, head height k, shortest and longest length */
  var ISO4762 = [['M3', 3, 5.5, 3, 5, 30], ['M4', 4, 7, 4, 6, 40], ['M5', 5, 8.5, 5, 8, 50],
                 ['M6', 6, 10, 6, 10, 60], ['M8', 8, 13, 8, 12, 80], ['M10', 10, 16, 10, 16, 100],
                 ['M12', 12, 18, 12, 20, 120]];
  /* size, d, across flats s, height m */
  var ISO4032 = [['M3', 3, 5.5, 2.4], ['M4', 4, 7, 3.2], ['M5', 5, 8, 4.7], ['M6', 6, 10, 5.2],
                 ['M8', 8, 13, 6.8], ['M10', 10, 16, 8.4], ['M12', 12, 18, 10.8]];
  /* size, d1, d2, h */
  var ISO7089 = [['M3', 3.2, 7, 0.5], ['M4', 4.3, 9, 0.8], ['M5', 5.3, 10, 1], ['M6', 6.4, 12, 1.6],
                 ['M8', 8.4, 16, 1.6], ['M10', 10.5, 20, 2], ['M12', 13, 24, 2.5]];
  /* series, bore, outside diameter, width: ISO/R 15/1-1968 Table 3, dimension series 10
   * (standard.go names the source, and that ISO 15:2017's own table went unread) */
  var ISO15 = [['608', 8, 22, 7], ['6000', 10, 26, 8], ['6001', 12, 28, 8], ['6002', 15, 32, 9],
               ['6003', 17, 35, 10], ['6004', 20, 42, 12], ['6005', 25, 47, 12]];
  /* kind, B, H, t */
  var EN10219 = [['SHS', 20, 20, 2], ['SHS', 30, 30, 3], ['SHS', 40, 40, 3], ['SHS', 40, 40, 4],
                 ['SHS', 50, 50, 5], ['RHS', 40, 20, 2], ['RHS', 60, 40, 3]];
  /* leg a, t, root radius r1, toe radius r2 — half the root radius, as EN 10056-1:1998
   * Note 1 states; the 2017 edition prints the same r1 (standard.go names the sources;
   * the L20's was 2 until 2026-09-15) */
  var EN10056 = [[20, 3, 3.5, 1.75], [30, 3, 5, 2.5], [40, 4, 6, 3], [50, 5, 7, 3.5]];
  /* geometry/units.go unitTable: the factor to millimetres and every alias. */
  var UNIT_TABLE = [[1, ['mm', 'millimetre', 'millimeter', 'millimetres', 'millimeters']],
                    [10, ['cm', 'centimetre', 'centimeter', 'centimetres', 'centimeters']],
                    [1000, ['m', 'metre', 'meter', 'metres', 'meters']],
                    [25.4, ['in', 'inch', 'inches', '"']]];

  function unitToMM(units) {
    var norm = String(units == null ? '' : units).trim().toLowerCase();
    for (var i = 0; i < UNIT_TABLE.length; i++) {
      if (norm && UNIT_TABLE[i][1].indexOf(norm) >= 0) return UNIT_TABLE[i][0];
    }
    return 0;
  }

  function circleLoop(r) {
    return [{ x: r, y: 0, via: { x: 0, y: -r } }, { x: -r, y: 0, via: { x: 0, y: r } }];
  }

  function ringSection(inner, outer, width) {
    return [{ x: inner, y: -width / 2 }, { x: outer, y: -width / 2 }, { x: outer, y: width / 2 }, { x: inner, y: width / 2 }];
  }

  var STANDARD_CATALOG = (function () {
    var out = [];
    ISO4762.forEach(function (s) {
      ISO4762_LENGTHS.forEach(function (l) {
        if (l < s[4] || l > s[5]) return;
        out.push({ designation: 'ISO 4762 ' + s[0] + 'x' + l, draw: function (k) {
          var r = s[1] / 2 * k, head = s[2] / 2 * k, length = l * k, height = s[3] * k;
          return { shape: 'revolve', profile: [{ x: 0, y: -length }, { x: r, y: -length }, { x: r, y: 0 },
                   { x: head, y: 0 }, { x: head, y: height }, { x: 0, y: height }] };
        } });
      });
    });
    ISO4032.forEach(function (n) {
      out.push({ designation: 'ISO 4032 ' + n[0], draw: function (k) {
        var corner = n[2] / Math.sqrt(3) * k, half = n[2] * k / 2;
        return { shape: 'extrusion', turned: true, depth: n[3] * k,
                 profile: [{ x: corner, y: 0 }, { x: corner / 2, y: half }, { x: -corner / 2, y: half },
                           { x: -corner, y: 0 }, { x: -corner / 2, y: -half }, { x: corner / 2, y: -half }],
                 holes: [circleLoop(n[1] / 2 * k)] };
      } });
    });
    ISO7089.forEach(function (w) {
      out.push({ designation: 'ISO 7089 ' + w[0], draw: function (k) {
        return { shape: 'revolve', profile: ringSection(w[1] / 2 * k, w[2] / 2 * k, w[3] * k) };
      } });
    });
    ISO15.forEach(function (b) {
      out.push({ designation: 'ISO 15 ' + b[0], draw: function (k) {
        return { shape: 'revolve', profile: ringSection(b[1] / 2 * k, b[2] / 2 * k, b[3] * k) };
      } });
    });
    EN10219.forEach(function (h) {
      out.push({ designation: 'EN 10219 ' + h[0] + ' ' + h[1] + 'x' + h[2] + 'x' + h[3], needsLength: true,
        draw: function (k, length) {
          var x = h[1] / 2 * k, y = h[2] / 2 * k, t = h[3] * k;
          var outside = 2 * h[3] * k, inside = h[3] * k;
          return { shape: 'extrusion', depth: length,
                   profile: [{ x: -x, y: -y, radius: outside }, { x: x, y: -y, radius: outside },
                             { x: x, y: y, radius: outside }, { x: -x, y: y, radius: outside }],
                   holes: [[{ x: -(x - t), y: -(y - t), radius: inside }, { x: x - t, y: -(y - t), radius: inside },
                            { x: x - t, y: y - t, radius: inside }, { x: -(x - t), y: y - t, radius: inside }]] };
        } });
    });
    EN10056.forEach(function (a) {
      out.push({ designation: 'EN 10056 L' + a[0] + 'x' + a[0] + 'x' + a[1], needsLength: true,
        draw: function (k, length) {
          var leg = a[0] * k, t = a[1] * k, root = a[2] * k, toe = a[3] * k;
          return { shape: 'extrusion', depth: length, profile: [{ x: 0, y: 0 }, { x: leg, y: 0 },
                   { x: leg, y: t, radius: toe }, { x: t, y: t, radius: root }, { x: t, y: leg, radius: toe }, { x: 0, y: leg }] };
        } });
    });
    return out;
  })();

  /* geometry normaliseDesignation: ASCII letters upper-cased, the multiplication
   * sign read as x, runs of spaces as one. No more forgiving than Go, or the browser
   * would draw a bearing the exporter refuses. */
  function normaliseDesignation(s) {
    s = String(s == null ? '' : s).replace(/\u00d7/g, 'x');
    var t = '';
    for (var i = 0; i < s.length; i++) {
      var ch = s.charAt(i);
      t += (ch >= 'a' && ch <= 'z') ? ch.toUpperCase() : ch;
    }
    return t.split(/\s+/).filter(function (w) { return w.length > 0; }).join(' ');
  }

  var STANDARD_INDEX = {};
  STANDARD_CATALOG.forEach(function (s, i) { STANDARD_INDEX[normaliseDesignation(s.designation)] = i; });

  function isStandardShape(p) {
    return !!p && String(p.shape || '').trim().toLowerCase() === 'standard';
  }

  /* standardPart is the part written out as geometry/standard.go writes it, or null
   * where Go refuses it: no designation, one not in the catalogue, no unit it can be
   * sized in, or a section with no length. */
  function standardPart(part, units) {
    part = part || {};
    if (!String(part.standard || '').trim()) return null;
    var i = STANDARD_INDEX[normaliseDesignation(part.standard)];
    if (i === undefined) return null;
    var toMM = unitToMM(units);
    if (!toMM) return null;
    var spec = STANDARD_CATALOG[i], length = 0;
    if (spec.needsLength) {
      var l = (part.size || {}).length;
      if (!(typeof l === 'number' && isFinite(l) && l > 0)) return null;
      length = l;
    }
    var drawn = spec.draw(1 / toMM, length);
    var q = shallowCopy(part);
    q.shape = drawn.shape;
    q.standard = spec.designation;
    q.profile = drawn.profile;
    if (drawn.holes) q.holes = drawn.holes; else delete q.holes;
    delete q.path; delete q.path_closed; delete q.script; delete q.size_from; delete q.axis;
    q.size = {};
    if (drawn.shape === 'revolve') q.axis = 'y'; else q.size.depth = drawn.depth;
    if (!q.name) q.name = spec.designation;
    if (drawn.turned) {
      /* Local Z up along +Y, inside the part's own placement (standard.go). */
      var st = storedPlacement(thenPlacement(placementOf(part.position, part.rotation, !!part.mirrored),
                                             placementOf(null, [-90, 0, 0], false)));
      q.position = st.position;
      q.rotation = st.rotation;
      q.mirrored = st.mirrored;
    }
    return q;
  }

  /* geometry expandStandards. A designation Go refuses is left for buildResolved,
   * which draws it as a labelled unit box — the bargain an unreadable gear has. */
  function expandStandards(parts, units) {
    return (parts || []).map(function (p) {
      if (!isStandardShape(p)) return p;
      return standardPart(p, units) || p;
    });
  }

  /* ---- gears ------------------------------------------------------------
   *
   * The browser's copy of internal/domain/geometry/gear.go: a spur gear's
   * numbers turned into an extrusion's outline. Mirrored step for step, down to
   * the order of the arithmetic, because the exported file is built from Go's
   * copy and this one is what the person looks at.
   *
   * # Why a copy rather than an outline sent from the server
   *
   * A turn's repairs install a replacement document without binding it, so an
   * outline written onto the stored part would be missing on exactly the turns
   * that were repaired — and those gears would be drawn as unit boxes.
   *
   * Fence: TestRendererDrawsTheSameGearAsTheExporter */
  var GEAR = { flankSegments: 8, addendum: 1, dedendum: 1.25, pressureAngle: 20, maxTeeth: 512 };
  /* sizeSynonyms["gear"] in geometry/mesh.go, in the sorted order Go reads them. */
  var GEAR_SYNONYMS = { depth: ['face_width', 'thickness'] };

  function gearSize(s, key) {
    if (typeof s[key] === 'number') return s[key];
    var aliases = GEAR_SYNONYMS[key] || [];
    for (var i = 0; i < aliases.length; i++) {
      if (typeof s[aliases[i]] === 'number') return s[aliases[i]];
    }
    return undefined;
  }

  /* gearOutline returns { profile, holes, depth, outsideDiameter }, or null when
   * the numbers do not describe a gear — the same gears readGear refuses, whose
   * reasons the document's own notes carry. */
  function gearOutline(size) {
    var s = size || {};
    var module = gearSize(s, 'module'), teethRaw = gearSize(s, 'teeth');
    if (!(typeof module === 'number' && isFinite(module) && module > 0)) return null;
    if (!(typeof teethRaw === 'number' && isFinite(teethRaw)) ||
        Math.abs(teethRaw - Math.round(teethRaw)) > 1e-9 ||
        Math.round(teethRaw) > GEAR.maxTeeth) return null;
    var teeth = Math.round(teethRaw);
    var pa = gearSize(s, 'pressure_angle');
    if (pa === undefined) pa = GEAR.pressureAngle;
    if (!(isFinite(pa) && pa > 0 && pa < 45)) return null;
    var depth = gearSize(s, 'depth');
    if (depth === undefined) depth = 1;
    if (!(isFinite(depth) && depth > 0)) return null;
    var bore = gearSize(s, 'bore_radius');
    if (bore === undefined) bore = 0;

    var a = pa * Math.PI / 180;
    var pitchR = module * teeth / 2;
    var tip = pitchR + GEAR.addendum * module;
    var root = pitchR - GEAR.dedendum * module;
    var base = pitchR * Math.cos(a);
    var halfBase = Math.PI / (2 * teeth) + (Math.tan(a) - a);
    function roll(r) { return r <= base ? 0 : Math.sqrt((r / base) * (r / base) - 1); }
    function turn(t) { return t - Math.atan(t); }
    var tStart = roll(Math.max(root, base)), tTip = roll(tip);
    function flankRoll(i) { return tStart + (tTip - tStart) * i / GEAR.flankSegments; }

    if (root <= 0) return null;
    var tipPressure = Math.acos(base / tip);
    if (halfBase - (Math.tan(tipPressure) - tipPressure) <= 0) return null;
    if (Math.PI / teeth - (halfBase - turn(flankRoll(0))) <= 0) return null;
    if (!isFinite(bore) || bore < 0 || bore >= root) return null;

    function at(r, angle) { return { x: r * Math.cos(angle), y: r * Math.sin(angle) }; }
    var radial = root < base, pitch = 2 * Math.PI / teeth, profile = [];
    for (var k = 0; k < teeth; k++) {
      var centre = Math.PI / 2 + pitch * k, first = profile.length, i, t, pt;
      if (radial) profile.push(at(root, centre - halfBase));
      for (i = 0; i <= GEAR.flankSegments; i++) {
        t = flankRoll(i);
        profile.push(at(base * Math.sqrt(1 + t * t), centre - (halfBase - turn(t))));
      }
      for (i = GEAR.flankSegments; i >= 0; i--) {
        t = flankRoll(i);
        pt = at(base * Math.sqrt(1 + t * t), centre + (halfBase - turn(t)));
        if (i === GEAR.flankSegments) pt.via = at(tip, centre);
        profile.push(pt);
      }
      if (radial) profile.push(at(root, centre + halfBase));
      profile[first].via = at(root, centre - pitch / 2);
    }
    var holes = [];
    if (bore > 0) {
      holes = [[{ x: bore, y: 0, via: { x: 0, y: -bore } },
                { x: -bore, y: 0, via: { x: 0, y: bore } }]];
    }
    return { profile: profile, holes: holes, depth: depth, outsideDiameter: 2 * tip };
  }

  /* Shape words the document vocabulary no longer offers, and what a document
   * that already uses one is read as. The browser's copy of the table in
   * internal/domain/geometry/retired.go — kept in step by a fence, because a
   * word that resolves one way here and another in the exported file is the
   * defect the tessellation fences exist to prevent.
   *
   * `tube` never modelled a bore and never could: its size keys are radius and
   * height, so the document had nowhere to say a wall thickness. It resolves to
   * what it always was, and says so. See retired.go for the full reasoning. */
  var RETIRED = {
    tube: {
      as: 'cylinder',
      because: 'drawn as a solid cylinder, which is what a "tube" has always been here — ' +
               'it has no inner dimension, so no bore was ever stated'
    }
  };

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

  function samePoint2D(a, b) {
    return Math.abs(a[0]-b[0]) < 1e-12 && Math.abs(a[1]-b[1]) < 1e-12;
  }

  /* ---- holes in an outline ----------------------------------------------
   *
   * Ear clipping walks ONE ring of vertices and cannot see a loop inside
   * another, so each hole is spliced into the outer loop by a BRIDGE: a segment
   * to a visible outer vertex, traversed out and back, which turns a
   * ring-with-holes into one ring that touches itself along the bridge. Exact:
   * the bridge is walked both ways and encloses nothing.
   *
   * The same algorithm as internal/domain/geometry/triangulate.go, and the same
   * winding rule — the outline counter-clockwise, every hole the other way —
   * which is what makes one wall-normal formula point out of the material on
   * both. TestRendererSweepsTheSameSolidAsTheExporter holds the two together.
   */
  function insideLoop2D(p, loop) {
    var inside = false;
    for (var i = 0; i < loop.length; i++) {
      var a = loop[i], b = loop[(i + 1) % loop.length];
      if ((a[1] > p[1]) !== (b[1] > p[1])) {
        var x = a[0] + (p[1] - a[1]) / (b[1] - a[1]) * (b[0] - a[0]);
        if (x > p[0]) inside = !inside;
      }
    }
    return inside;
  }

  function onSegment2D(a, b, p) {
    return Math.min(a[0], b[0]) <= p[0] && p[0] <= Math.max(a[0], b[0]) &&
           Math.min(a[1], b[1]) <= p[1] && p[1] <= Math.max(a[1], b[1]);
  }

  function strictlyBetween2D(a, b, p) {
    if (samePoint2D(p, a) || samePoint2D(p, b)) return false;
    return onSegment2D(a, b, p);
  }

  function properlyCross2D(p1, p2, p3, p4) {
    var d1 = cross2D(p3, p4, p1), d2 = cross2D(p3, p4, p2);
    var d3 = cross2D(p1, p2, p3), d4 = cross2D(p1, p2, p4);
    if (((d1 > 0 && d2 < 0) || (d1 < 0 && d2 > 0)) &&
        ((d3 > 0 && d4 < 0) || (d3 < 0 && d4 > 0))) return true;
    return (d1 === 0 && strictlyBetween2D(p3, p4, p1)) ||
           (d2 === 0 && strictlyBetween2D(p3, p4, p2)) ||
           (d3 === 0 && strictlyBetween2D(p1, p2, p3)) ||
           (d4 === 0 && strictlyBetween2D(p1, p2, p4));
  }

  function blocksBridge2D(a, b, loop) {
    for (var i = 0; i < loop.length; i++) {
      var p = loop[i], q = loop[(i + 1) % loop.length];
      if (samePoint2D(p, a) || samePoint2D(p, b) || samePoint2D(q, a) || samePoint2D(q, b)) {
        /* Touching AT a shared endpoint is how a bridge always meets its own
         * loops. An edge lying ALONG the bridge is not. */
        if (cross2D(a, b, p) === 0 && cross2D(a, b, q) === 0) return true;
        continue;
      }
      if (properlyCross2D(a, b, p, q)) return true;
    }
    return false;
  }

  function bridgeInto(merged, hole, pending) {
    var candidates = [], i, j;
    for (i = 0; i < merged.length; i++) {
      for (j = 0; j < hole.length; j++) {
        var dx = hole[j][0] - merged[i][0], dy = hole[j][1] - merged[i][1];
        candidates.push({ o: i, h: j, d: dx*dx + dy*dy });
      }
    }
    candidates.sort(function (x, y) { return x.d - y.d; });

    for (var c = 0; c < candidates.length; c++) {
      var a = merged[candidates[c].o], b = hole[candidates[c].h];
      if (samePoint2D(a, b)) continue;
      if (blocksBridge2D(a, b, merged) || blocksBridge2D(a, b, hole)) continue;
      var blocked = false, k;
      for (k = 0; k < pending.length && !blocked; k++) {
        if (blocksBridge2D(a, b, pending[k])) blocked = true;
      }
      if (blocked) continue;
      var mid = [(a[0] + b[0]) / 2, (a[1] + b[1]) / 2];
      if (!insideLoop2D(mid, merged) || insideLoop2D(mid, hole)) continue;
      for (k = 0; k < pending.length && !blocked; k++) {
        if (insideLoop2D(mid, pending[k])) blocked = true;
      }
      if (blocked) continue;

      /* Out along the bridge, round the hole, and back. Both bridge vertices
       * appear twice, which is the trick: the ring touches itself along a
       * segment of zero width and encloses exactly what it did before. */
      return merged.slice(0, candidates[c].o + 1)
        .concat(hole.slice(candidates[c].h))
        .concat(hole.slice(0, candidates[c].h + 1))
        .concat(merged.slice(candidates[c].o));
    }
    return null;
  }

  function rightmostX2D(loop) {
    var x = -Infinity;
    for (var i = 0; i < loop.length; i++) x = Math.max(x, loop[i][0]);
    return x;
  }

  /* The outline first, counter-clockwise, then every hole the other way. */
  function sectionLoops2D(outer, holes) {
    var loops = [signedArea2D(outer) >= 0 ? outer : outer.slice().reverse()];
    (holes || []).forEach(function (h) {
      loops.push(signedArea2D(h) <= 0 ? h : h.slice().reverse());
    });
    return loops;
  }

  /* Which loop directly contains each one, and how deep it sits.
   *
   * A loop inside a hole is an ISLAND: solid material standing in a void — the
   * post in an annular slot, the bar of a letter A, a lug in the bottom of a
   * pocket. A loop contained in an odd number of others is a void; in an even
   * number, solid. Go reads it the same way in nestLoops, and sends the same
   * tree to the CAD kernel, which cannot work it out from curves.
   *
   * Ordinarily every hole is directly inside the outline and this is the answer
   * it always was. */
  function nest2D(loops) {
    var depth = [], parent = [], i, j;
    for (i = 0; i < loops.length; i++) {
      depth[i] = 0;
      for (j = 0; j < loops.length; j++) {
        if (i !== j && loops[j].length && loops[i].length &&
            insideLoop2D(loops[i][0], loops[j])) depth[i]++;
      }
    }
    for (i = 0; i < loops.length; i++) {
      var best = -1;
      for (j = 0; j < loops.length; j++) {
        if (i === j || !loops[j].length || !loops[i].length) continue;
        if (!insideLoop2D(loops[i][0], loops[j])) continue;
        if (best < 0 || depth[j] > depth[best]) best = j;
      }
      parent[i] = best;
    }
    return { depth: depth, parent: parent };
  }

  /* { merged, tris, loops } — the caps index into merged, the walls walk loops.
   *
   * merged is the CONCATENATION of one bridged ring per solid area, and the
   * triangles are offset into it, so a section with an island still hands the
   * extrusion, the revolve and the sweep one point list and one triangle list.
   * Go does the same, for the same reason: those three are fenced against this
   * file and neither should have to learn about nesting. */
  function triangulateSection(outer, holes) {
    holes = holes || [];
    if (!holes.length) {
      var clipped = earClip(signedArea2D(outer) >= 0 ? outer : outer.slice().reverse());
      return { merged: clipped.pts, tris: clipped.tris, loops: [clipped.pts] };
    }
    var raw = [outer].concat(holes);
    var tree = nest2D(raw);
    /* Wound by PARITY, not by position: an island's wall must face out of the
     * material like the outline's, and winding everything after the first one
     * clockwise would point it into the solid. */
    var wound = raw.map(function (loop, i) {
      var ccw = signedArea2D(loop) >= 0;
      var wantCCW = tree.depth[i] % 2 === 0;
      return ccw === wantCCW ? loop : loop.slice().reverse();
    });

    var merged = [], tris = [], i;
    for (i = 0; i < wound.length; i++) {
      if (tree.depth[i] % 2 !== 0) continue;          /* a void, not an area */
      var inner = [];
      for (var j = 0; j < wound.length; j++) {
        if (tree.depth[j] % 2 === 1 && tree.parent[j] === i) inner.push(wound[j]);
      }
      var ring = wound[i].slice(), remaining = inner.slice();
      var failed = false;
      while (remaining.length) {
        var best = 0, k;
        for (k = 1; k < remaining.length; k++) {
          if (rightmostX2D(remaining[k]) > rightmostX2D(remaining[best])) best = k;
        }
        var hole = remaining[best];
        remaining = remaining.slice(0, best).concat(remaining.slice(best + 1));
        var spliced = bridgeInto(ring, hole, remaining);
        if (!spliced) { failed = true; break; }
        ring = spliced;
      }
      if (failed) return { merged: wound[0], tris: [], loops: wound };
      /* earClip returns the points its triangles index INTO — it may reorder
       * them — so the ring comes back from it rather than being kept
       * separately. That is the same trap the caps-and-walls bug came from. */
      var done = earClip(ring);
      var base = merged.length;
      merged = merged.concat(done.pts);
      for (k = 0; k < done.tris.length; k++) {
        tris.push([done.tris[k][0] + base, done.tris[k][1] + base, done.tris[k][2] + base]);
      }
    }
    return { merged: merged, tris: tris, loops: wound };
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
          /* A bridge puts TWO vertices at the same coordinates, and the boundary
           * counts as inside — so without this, the duplicate of a bridge
           * endpoint blocks every ear that touches it and clipping stalls on any
           * outline with a hole in it. Skipped by POSITION, because which index
           * is the duplicate is not knowable from here. */
          if (samePoint2D(pts[o], pts[prev]) || samePoint2D(pts[o], pts[cur]) ||
              samePoint2D(pts[o], pts[next])) continue;
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

  /* ---- corner radii ------------------------------------------------------
   *
   * A point may carry a `radius`, which rounds the corner there: an arc of that
   * radius, tangent to both edges meeting at it. The same field on an outline
   * point and on a path point, because it is the same idea — and on a path it is
   * the BEND RADIUS, the number a tube bender is set to.
   *
   * # Why this is flattened here and not sent as a curve
   *
   * internal/domain/geometry/curve.go works out the same corners and hands the
   * CAD kernel the TRUE arcs, so an exported bend is a real cylindrical surface.
   * This is a renderer: it draws triangles, so it turns each arc into chords at
   * the same count a cylinder gets, and the workbench reports the deviation the
   * Go side computed. Same bargain every curved shape here already makes.
   *
   * Returns null when the corners cannot be resolved — radii that overlap each
   * other, a radius where there is no corner. Go refuses those documents before
   * they are ever drawn, so reaching null means the two disagree, and drawing a
   * labelled box beats drawing a lie.
   */
  function flattenDrawing(points, closed) {
    /* A loop that closes itself by repeating its first point is READ, not
     * refused: every polygon format a model has read closes a ring that way, and
     * a final edge of zero length is never a shape, so the repeated point is
     * redundant. The RADIUS moves with it — the repeated point often carries the
     * corner radius while the original does not, and leaving it behind would
     * mitre a corner somebody asked to be bent.
     *
     * internal/domain/geometry/curve.go does the same on the way to the kernel.
     * The two must agree or the picture and the file are different shapes. */
    points = (points || []).slice();
    if (closed && points.length > 1) {
      var a0 = points[0], z0 = points[points.length - 1];
      if (Math.abs(num(a0.x,0)-num(z0.x,0)) < 1e-12 && Math.abs(num(a0.y,0)-num(z0.y,0)) < 1e-12 &&
          Math.abs(num(a0.z,0)-num(z0.z,0)) < 1e-12) {
        points.pop();
        /* The VIA moves with it too, and with a cleaner argument than the
         * radius: a via on the repeated point describes the edge ARRIVING at
         * it, which once the duplicate is gone is exactly the closing edge —
         * entry 0. Same edge, renumbered. Leaving it behind draws a bowed edge
         * straight, which is a different outline of the same overall size. */
        var keepR = num(a0.radius, 0) || num(z0.radius, 0);
        var keepV = z0.via || a0.via;
        points[0] = { x: num(a0.x,0), y: num(a0.y,0), z: num(a0.z,0),
                      radius: keepR, via: keepV };
      }
    }
    var n = points.length;
    if (n < 2) return null;
    function at(i) {
      var p = points[((i % n) + n) % n];
      return [num(p.x, 0), num(p.y, 0), num(p.z, 0)];
    }
    function sub(a, b) { return [a[0]-b[0], a[1]-b[1], a[2]-b[2]]; }
    function add(a, b) { return [a[0]+b[0], a[1]+b[1], a[2]+b[2]]; }
    function mul(a, s) { return [a[0]*s, a[1]*s, a[2]*s]; }
    function dot(a, b) { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2]; }
    function crs(a, b) {
      return [a[1]*b[2]-a[2]*b[1], a[2]*b[0]-a[0]*b[2], a[0]*b[1]-a[1]*b[0]];
    }
    function len(a) { return Math.sqrt(dot(a, a)); }
    function unit(a) { var l = len(a); return l ? mul(a, 1/l) : [0,0,0]; }

    var TOL = 1e-9, corners = [], i;

    /* ---- bowed edges -----------------------------------------------------
     *
     * A point may carry a `via`: one more point, and the edge ARRIVING at it is
     * the circular arc through it. Three points fix a circle completely, which
     * a radius and two endpoints do not — see internal/domain/geometry/curve.go
     * for the measurement that settled that, and for why an arc edge always has
     * SHARP ends. Entry 0 is the closing edge of a closed run. */
    function arcAt(i2) {
      var p = points[((i2 % n) + n) % n];
      if (!p.via) return null;
      if (!closed && i2 === 0) return null;
      var from = at(i2 - 1), to = at(i2);
      var via = [num(p.via.x, 0), num(p.via.y, 0), num(p.via.z, 0)];
      var u = sub(via, from), v = sub(to, from), nrm = crs(u, v);
      var nn = dot(nrm, nrm);
      if (nn < TOL * TOL) return null;               /* in line: no circle */
      var centre = add(from, mul(add(mul(crs(v, nrm), dot(u, u)),
                                     mul(crs(nrm, u), dot(v, v))), 1 / (2 * nn)));
      var a = sub(from, centre), rad = len(a);
      if (rad < TOL) return null;
      var axis = unit(nrm);
      function sweepTo(p2) {
        var d = sub(p2, centre);
        var t = Math.atan2(dot(axis, crs(a, d)), dot(a, d));
        return t < 0 ? t + 2 * Math.PI : t;
      }
      var toA = sweepTo(to), viaA = sweepTo(via);
      if (toA < TOL || viaA < TOL || Math.abs(toA - viaA) < TOL) return null;
      /* Which way round: the arc is the one that PASSES THROUGH the via. */
      if (viaA < toA) return { centre: centre, axis: axis, angle: toA, from: from };
      return { centre: centre, axis: mul(axis, -1), angle: 2 * Math.PI - toA, from: from };
    }
    var arcs = [];
    for (i = 0; i < n; i++) arcs.push(arcAt(i));

    for (i = 0; i < n; i++) {
      var r = num(points[i].radius, 0);
      var interior = closed || (i > 0 && i < n - 1);
      if (!(r > 0) || !interior) { corners.push(null); continue; }
      /* A vertex with an arc on either side is SHARP. Rounding an arc into an
       * arc is a fillet between two curves, and the construction below — which
       * walks back r·tan(θ/2) along a straight — has no answer for it. Go takes
       * the same reading and reports it as an ignored radius. */
      if (arcs[i] || arcs[(i + 1) % n]) { corners.push(null); continue; }
      var dIn = sub(at(i), at(i-1)), dOut = sub(at(i+1), at(i));
      if (len(dIn) < TOL || len(dOut) < TOL) return null;
      dIn = unit(dIn); dOut = unit(dOut);
      var axis = crs(dIn, dOut), sin = len(axis), cos = dot(dIn, dOut);
      if (sin < TOL) {
        /* In line: there is no corner here, so the radius names nothing and is
         * IGNORED — the same reading Go takes, which reports it as a warning and
         * still builds the part. A reversal is a different matter: no radius can
         * round it, and there is nothing honest to draw. */
        if (cos > 0) { corners.push(null); continue; }
        return null;
      }
      var angle = Math.atan2(sin, cos);
      var cut = r * Math.tan(angle / 2);
      var from = add(at(i), mul(dIn, -cut));
      var centre = add(at(i), mul(unit(sub(dOut, dIn)), r / Math.cos(angle / 2)));
      corners.push({ cut: cut, from: from, centre: centre, angle: angle,
                     axis: mul(axis, 1/sin) });
    }
    /* Two corners on one edge must leave room for each other. */
    var last = closed ? n - 1 : n - 2;
    for (i = 0; i <= last; i++) {
      var j = (i + 1) % n;
      var need = (corners[i] ? corners[i].cut : 0) + (corners[j] ? corners[j].cut : 0);
      if (need > len(sub(at(j), at(i))) + TOL) return null;
    }

    var out = [];
    function push(p) {
      var back = out[out.length - 1];
      if (back && Math.abs(back[0]-p[0]) < 1e-12 && Math.abs(back[1]-p[1]) < 1e-12 &&
          Math.abs(back[2]-p[2]) < 1e-12) return;
      out.push(p);
    }
    /* One bowed edge, stepped at the same fineness a corner arc gets. Safe to
     * emit whole, with no trimming, because an arc edge always has sharp ends. */
    function bow(i2) {
      var a2 = arcs[i2];
      if (!a2) return;
      var steps2 = Math.max(1, Math.ceil(radialSegments() * a2.angle / (2 * Math.PI)));
      var spoke2 = sub(a2.from, a2.centre);
      for (var k3 = 0; k3 <= steps2; k3++) {
        var t2 = a2.angle * k3 / steps2, c2 = Math.cos(t2), s2 = Math.sin(t2);
        push(add(a2.centre, add(add(mul(spoke2, c2), mul(crs(a2.axis, spoke2), s2)),
                                mul(a2.axis, dot(a2.axis, spoke2) * (1 - c2)))));
      }
    }
    var seam = 0;   /* how many points the first corner contributed */
    for (i = 0; i < n; i++) {
      /* The edge ARRIVING here, before the corner itself. Index 0's arriving
       * edge is the closing one and is emitted at the end, where a closed run
       * actually reaches it. */
      if (i > 0) bow(i);
      var c = corners[i];
      if (!c) {
        push(at(i));
        if (i === 0) seam = out.length;
        continue;
      }
      var steps = Math.max(1, Math.ceil(radialSegments() * c.angle / (2 * Math.PI)));
      var spoke = sub(c.from, c.centre);
      for (var k = 0; k <= steps; k++) {
        var t = c.angle * k / steps;
        var ct = Math.cos(t), st = Math.sin(t);
        /* Rodrigues about the corner's own axis. */
        push(add(c.centre, add(add(mul(spoke, ct), mul(crs(c.axis, spoke), st)),
                               mul(c.axis, dot(c.axis, spoke) * (1 - ct)))));
      }
      if (i === 0) seam = out.length;
    }
    if (closed) bow(0);
    if (closed && out.length > 1) {
      var a = out[0], b = out[out.length - 1];
      if (Math.abs(a[0]-b[0]) < 1e-12 && Math.abs(a[1]-b[1]) < 1e-12 &&
          Math.abs(a[2]-b[2]) < 1e-12) out.pop();
    }
    /* A CLOSED run starts where its first corner ENDS. Beginning part-way along
     * the seam's arc puts the first direction on a CHORD of that arc, while the
     * kernel's curve has the true tangent there — 4.5° apart at this fineness
     * for a right angle, which tilts the whole solid. Rotating costs nothing: a
     * closed run has no first point, only a place we chose to start writing it
     * down. See internal/domain/geometry/curve.go. */
    if (closed && seam > 1 && out.length) {
      var k2 = (seam - 1) % out.length;
      out = out.slice(k2).concat(out.slice(0, k2));
    }
    return out;
  }

  /* The outline, flattened, in its own two dimensions. */
  function outlinePoints(profile) {
    var flat = flattenDrawing(profile || [], true);
    if (!flat) return null;
    return flat.map(function (p) { return [p[0], p[1]]; });
  }

  /* An outline's own bounding rectangle, in the part's local X and Y.
   *
   * # Why the panel needs this
   *
   * An extrusion's "size" carries only its depth: the other two dimensions are
   * in the drawing. The parts panel used to print those as "? × ? × 4500 mm",
   * so a body that had quietly become 4500 mm wide looked exactly like one that
   * had not. A model asked to make a car body less boxy drew the car's SIDE
   * view (4500 long) and extruded it by 4500, giving a square slab — and the
   * only number on screen was the one that was right.
   *
   * Measured from the FLATTENED outline for the same reason profileExtent does
   * in geometry/overlay.go: a rounded corner sits inside the corner it replaced,
   * so the drawn vertices would report a part bigger than the part.
   *
   * null when the outline cannot be resolved — the panel then says "?" as
   * before, which is honest. A guess here would be a measurement. */
  function outlineExtent(profile) {
    var pts = outlinePoints(profile);
    if (!pts || pts.length < 3) return null;
    var lo = [Infinity, Infinity], hi = [-Infinity, -Infinity];
    for (var i = 0; i < pts.length; i++) {
      lo[0] = Math.min(lo[0], pts[i][0]); hi[0] = Math.max(hi[0], pts[i][0]);
      lo[1] = Math.min(lo[1], pts[i][1]); hi[1] = Math.max(hi[1], pts[i][1]);
    }
    if (!isFinite(lo[0]) || !isFinite(lo[1])) return null;
    return { width: hi[0] - lo[0], height: hi[1] - lo[1] };
  }

  function extrusionGeometry(profile, depth, holes) {
    var raw = outlinePoints(profile);
    var bores = holeOutlines(holes);
    if (!raw || !bores) {
      return {
        geo: boxGeometry(1, 1, num(depth, 1)),
        approximated: 'the corner radii on this outline could not be resolved, so it is ' +
                      'drawn as a unit box'
      };
    }
    if (raw.length < 3) {
      return {
        geo: boxGeometry(1, 1, num(depth, 1)),
        approximated: 'this outline has fewer than three points and encloses nothing, ' +
                      'so it is drawn as a unit box'
      };
    }
    var sec = triangulateSection(raw, bores);
    if (!sec.tris.length) {
      return {
        geo: boxGeometry(1, 1, num(depth, 1)),
        approximated: 'this outline could not be closed into a surface — it crosses itself, ' +
                      'repeats a point, or has a hole that will not fit inside it — so it ' +
                      'is drawn as a unit box'
      };
    }

    var pts = sec.merged, half = num(depth, 1) / 2;
    var positions = [], normals = [], indices = [], n = 0;
    function vert(x, y, z, nx, ny, nz) {
      positions.push(x, y, z); normals.push(nx, ny, nz); indices.push(n++);
    }

    sec.tris.forEach(function (t) {
      vert(pts[t[0]][0], pts[t[0]][1], half, 0, 0, 1);
      vert(pts[t[1]][0], pts[t[1]][1], half, 0, 0, 1);
      vert(pts[t[2]][0], pts[t[2]][1], half, 0, 0, 1);
      // Reversed, so the bottom cap faces away from the solid too.
      vert(pts[t[2]][0], pts[t[2]][1], -half, 0, 0, -1);
      vert(pts[t[1]][0], pts[t[1]][1], -half, 0, 0, -1);
      vert(pts[t[0]][0], pts[t[0]][1], -half, 0, 0, -1);
    });

    /* The walls, loop by loop rather than over the merged ring: a wall along a
     * bridge would be a quad of zero width, drawn twice and facing both ways. */
    sec.loops.forEach(function (loop) {
      for (var i = 0; i < loop.length; i++) {
        var j = (i + 1) % loop.length;
        var dx = loop[j][0] - loop[i][0], dy = loop[j][1] - loop[i][1];
        var len = Math.sqrt(dx * dx + dy * dy) || 1;
        /* Outward for a counter-clockwise outline: on a square wound
         * counter-clockwise the bottom edge runs +x and the outside is -y. A
         * HOLE runs the other way, so the same formula points into the hole,
         * which is out of the material. */
        var nx = dy / len, ny = -dx / len;
        vert(loop[i][0], loop[i][1], -half, nx, ny, 0);
        vert(loop[j][0], loop[j][1], -half, nx, ny, 0);
        vert(loop[j][0], loop[j][1], half, nx, ny, 0);
        vert(loop[i][0], loop[i][1], -half, nx, ny, 0);
        vert(loop[j][0], loop[j][1], half, nx, ny, 0);
        vert(loop[i][0], loop[i][1], half, nx, ny, 0);
      }
    });
    return { geo: { positions: positions, normals: normals, indices: indices } };
  }

  /* Every hole of an outline, flattened. null when any of them cannot be. */
  function holeOutlines(holes) {
    var out = [];
    for (var i = 0; i < (holes || []).length; i++) {
      var flat = outlinePoints(holes[i]);
      if (!flat) return null;
      out.push(flat);
    }
    return out;
  }

  /* An outline turned a full circle about its own axis.
   *
   * # Why there is no triangulation here
   *
   * An extrusion needs its caps triangulated. A full revolve has none — the
   * surface closes on itself — so every facet is a quad between two adjacent
   * outline points at two adjacent angles. The winding still matters, because it
   * decides which way those quads face.
   *
   * TESSELLATION.radial is the same count a cylinder uses, so a revolved boss
   * and a cylinder beside it are drawn to the same fineness, and the exported
   * mesh is the surface that was on screen.
   */
  function revolveGeometry(profile, axis, holes) {
    var raw = outlinePoints(profile);
    var bores = holeOutlines(holes);
    if (!raw || !bores) {
      return {
        geo: boxGeometry(1, 1, 1),
        approximated: 'the corner radii on this outline could not be resolved, so it is ' +
                      'drawn as a unit box'
      };
    }
    if (raw.length < 3) {
      return {
        geo: boxGeometry(1, 1, 1),
        approximated: 'this outline has fewer than three points and encloses nothing, ' +
                      'so it is drawn as a unit box'
      };
    }
    /* The same normalisation the extrusion does, for the same reason: the facet
     * winding below is only outward for a counter-clockwise outline — and a
     * hole, wound the other way, turns into a surface facing into the void. */
    var loops = sectionLoops2D(raw, bores);
    var aboutX = String(axis || '').toLowerCase() === 'x';
    var seg = radialSegments();

    function at(pts, i, t) {
      if (aboutX) {
        var rx = pts[i][1];
        return [pts[i][0], rx * Math.cos(t), rx * Math.sin(t)];
      }
      var r = pts[i][0];
      return [r * Math.cos(t), pts[i][1], r * Math.sin(t)];
    }
    /* The normal comes from the facet itself rather than from a formula per
     * axis: the two axes have opposite handedness, and a formula written for one
     * is silently inverted for the other. */
    function normalOf(a, b, c) {
      var ux = b[0]-a[0], uy = b[1]-a[1], uz = b[2]-a[2];
      var vx = c[0]-a[0], vy = c[1]-a[1], vz = c[2]-a[2];
      var nx = uy*vz - uz*vy, ny = uz*vx - ux*vz, nz = ux*vy - uy*vx;
      var len = Math.sqrt(nx*nx + ny*ny + nz*nz) || 1;
      return [nx/len, ny/len, nz/len];
    }

    var positions = [], normals = [], indices = [], n = 0;
    function tri(a, b, c) {
      var nn = normalOf(a, b, c);
      [a, b, c].forEach(function (v) {
        positions.push(v[0], v[1], v[2]);
        normals.push(nn[0], nn[1], nn[2]);
        indices.push(n++);
      });
    }
    for (var k = 0; k < seg; k++) {
      var t0 = k / seg * 2 * Math.PI, t1 = (k + 1) / seg * 2 * Math.PI;
      for (var l = 0; l < loops.length; l++) {
        var pts = loops[l];
        for (var i = 0; i < pts.length; i++) {
          var j = (i + 1) % pts.length;
          var a = at(pts, i, t0), b = at(pts, j, t0), c = at(pts, j, t1), d = at(pts, i, t1);
          tri(a, b, c);
          tri(a, c, d);
        }
      }
    }
    return { geo: { positions: positions, normals: normals, indices: indices } };
  }

  /* An outline carried along a PATH — the shape everything that bends is made
   * of: a pipe run, a handrail, a cable tray, a wire form.
   *
   * # Why this is a third implementation and what keeps it honest
   *
   * internal/domain/geometry/sweep.go decides where every ring of points goes,
   * and the CAD kernel builds a real B-Rep from the same numbers. This draws the
   * same rings for the viewport, which cannot call either.
   *
   * What is shared is the PROPERTY, as with ear clipping: a swept polygon is a
   * polyhedron with no curved surface anywhere on it, so all three produce the
   * SAME solid rather than three approximations of one — and its volume is the
   * outline's area times the path's length whenever the outline is centred on
   * the path, which is what the fences on both sides assert.
   *
   * Corners are mitred: each ring sits in the plane bisecting the two segments,
   * which is what a fabricated bend is, and the section is carried between
   * segments by the smallest rotation that takes one direction to the next, so
   * it does not twist as the path bends.
   */
  function sweepSections(loops, path, closed) {
    function sub(a, b) { return [a[0]-b[0], a[1]-b[1], a[2]-b[2]]; }
    function add(a, b) { return [a[0]+b[0], a[1]+b[1], a[2]+b[2]]; }
    function mul(a, s) { return [a[0]*s, a[1]*s, a[2]*s]; }
    function dot(a, b) { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2]; }
    function crs(a, b) {
      return [a[1]*b[2]-a[2]*b[1], a[2]*b[0]-a[0]*b[2], a[0]*b[1]-a[1]*b[0]];
    }
    function len(a) { return Math.sqrt(dot(a, a)); }
    function unit(a) { var l = len(a); return l ? mul(a, 1/l) : [0,0,0]; }
    /* The smallest rotation taking unit a to unit b — Rodrigues, angle in
     * [0, pi]. Antiparallel has no such axis, so a fixed perpendicular is used
     * and the answer is at least deterministic; the only path that reaches it
     * sets off straight down. */
    function rotation(a, b) {
      var axis = crs(a, b), sin = len(axis), cos = dot(a, b);
      if (sin < 1e-12) {
        if (cos > 0) return function (v) { return v; };
        var lean = Math.abs(a[0]) > Math.abs(a[1]) ? [0,1,0] : [1,0,0];
        var k0 = unit(crs(a, lean));
        return function (v) { return sub(mul(k0, 2*dot(v, k0)), v); };
      }
      var k = mul(axis, 1/sin);
      return function (v) {
        return add(add(mul(v, cos), mul(crs(k, v), sin)), mul(k, dot(k, v)*(1-cos)));
      };
    }

    if (!loops.length || loops[0].length < 3 || path.length < 2) return null;
    var n = path.length;
    var i, j, k, segments = closed ? n : n - 1, tangent = [];
    for (i = 0; i < segments; i++) {
      var d = sub(path[(i+1) % n], path[i]);
      if (len(d) < 1e-12) return null;   /* a zero-length segment */
      tangent.push(unit(d));
    }
    /* A CLOSED path has a bisector at every vertex, the seam included: it is a
     * corner like any other, joining the last segment to the first. An open one
     * has ends, where the section sits square to the path. */
    var bisector = [];
    for (j = 0; j < n; j++) {
      if (!closed && j === 0) { bisector.push(tangent[0]); continue; }
      if (!closed && j === n - 1) { bisector.push(tangent[segments-1]); continue; }
      var sum = add(tangent[(j-1+segments) % segments], tangent[j % segments]);
      if (len(sum) < 1e-9) return null;  /* the path doubles back */
      bisector.push(unit(sum));
    }

    /* The frame at the first segment is the smallest rotation from +Z, so a
     * path straight up local Z leaves the outline exactly as drawn and the
     * sweep IS the extrusion. */
    var start = rotation([0,0,1], tangent[0]);
    var axisX = [start([1,0,0])], axisY = [start([0,1,0])];
    for (i = 1; i < segments; i++) {
      var turn = rotation(tangent[i-1], tangent[i]);
      axisX.push(turn(axisX[i-1]));
      axisY.push(turn(axisY[i-1]));
    }
    /* Round a loop the carried frame must come back to ITSELF, and generally it
     * does not: carrying a frame round a closed curve rotates it by the area its
     * tangents enclose on the sphere. Go refuses those documents and names the
     * angle; here there is nothing honest to draw. */
    if (closed) {
      var backTo = rotation(tangent[segments-1], tangent[0])(axisX[segments-1]);
      if (Math.abs(Math.atan2(dot(backTo, axisY[0]), dot(backTo, axisX[0]))) > 1e-6) return null;
    }

    /* EVERY loop is carried by the same frames — the outline and the holes in
     * it — because they are one section. A bore carried by frames of its own
     * would drift out of the wall around it as the path bends. */
    var all = [];
    for (var l = 0; l < loops.length; l++) {
      var rings = [];
      for (j = 0; j < n; j++) {
        var into = j > 0 ? j - 1 : (closed ? segments - 1 : 0);
        var t = tangent[into], m = bisector[j];
        var denom = dot(t, m), ring = [];
        for (k = 0; k < loops[l].length; k++) {
          /* On the perpendicular section at the vertex, then slid ALONG the
           * segment onto the bisector plane. Sliding rather than projecting is
           * what makes it a mitre: each point stays on the line the sweep
           * carries it along, so the two faces meet edge to edge. */
          var base = add(path[j], add(mul(axisX[into], loops[l][k][0]),
                                      mul(axisY[into], loops[l][k][1])));
          ring.push(add(base, mul(t, dot(sub(path[j], base), m) / denom)));
        }
        rings.push(ring);
      }
      all.push(rings);
    }
    return all;
  }

  function sweepGeometry(profile, path, holes, closed) {
    var raw = outlinePoints(profile);
    var bores = holeOutlines(holes);
    var way = flattenDrawing(path || [], !!closed);
    if (!raw || !bores || !way) {
      return {
        geo: boxGeometry(1, 1, 1),
        approximated: 'the corner radii on this outline or the bend radii on its path could ' +
                      'not be resolved, so it is drawn as a unit box'
      };
    }
    if (raw.length < 3 || way.length < 2) {
      return {
        geo: boxGeometry(1, 1, 1),
        approximated: 'a sweep needs an outline of at least three points and a path of at ' +
                      'least two, so it is drawn as a unit box'
      };
    }
    var sec = triangulateSection(raw, bores);
    /* The merged ring is carried along the path as loop ZERO, so the caps come
     * from the rings like everything else rather than from a second
     * transformation that could disagree with them. */
    var rings = sec.tris.length ? sweepSections([sec.merged].concat(sec.loops), way, !!closed) : null;
    if (!rings) {
      return {
        geo: boxGeometry(1, 1, 1),
        approximated: 'this outline could not be closed into a surface, or this path ' +
                      'repeats a point or doubles back, so it is drawn as a unit box'
      };
    }

    var positions = [], normals = [], indices = [], n = 0;
    function normalOf(a, b, c) {
      var ux = b[0]-a[0], uy = b[1]-a[1], uz = b[2]-a[2];
      var vx = c[0]-a[0], vy = c[1]-a[1], vz = c[2]-a[2];
      var nx = uy*vz - uz*vy, ny = uz*vx - ux*vz, nz = ux*vy - uy*vx;
      var l = Math.sqrt(nx*nx + ny*ny + nz*nz) || 1;
      return [nx/l, ny/l, nz/l];
    }
    function tri(a, b, c, nn) {
      var norm = nn || normalOf(a, b, c);
      [a, b, c].forEach(function (v) {
        positions.push(v[0], v[1], v[2]);
        normals.push(norm[0], norm[1], norm[2]);
        indices.push(n++);
      });
    }

    var caps = rings[0], walls = rings.slice(1);
    var vertices = caps.length, last = vertices - 1;
    /* The two ends, each facing away from the material between them. Taken from
     * where one outline point MOVED between the first two rings, which is
     * parallel to the segment however the mitre tilted the ring.
     *
     * A CLOSED path has no ends: the surface closes on itself at the seam, and a
     * cap there would be a disc standing in the middle of the material. */
    function direction(from, to) {
      var v = [to[0]-from[0], to[1]-from[1], to[2]-from[2]];
      var l = Math.sqrt(v[0]*v[0] + v[1]*v[1] + v[2]*v[2]) || 1;
      return [v[0]/l, v[1]/l, v[2]/l];
    }
    if (!closed) {
      var startN = direction(caps[1][0], caps[0][0]);
      var endN = direction(caps[last-1][0], caps[last][0]);
      sec.tris.forEach(function (t) {
        tri(caps[0][t[2]], caps[0][t[1]], caps[0][t[0]], startN);
        tri(caps[last][t[0]], caps[last][t[1]], caps[last][t[2]], endN);
      });
    }
    var segments = closed ? vertices : last;
    for (var l = 0; l < sec.loops.length; l++) {
      var loop = sec.loops[l], ring = walls[l];
      for (var i = 0; i < segments; i++) {
        var onward = (i + 1) % vertices;
        for (var k = 0; k < loop.length; k++) {
          var j2 = (k + 1) % loop.length;
          var a = ring[i][k], b = ring[i][j2], c = ring[onward][j2], d = ring[onward][k];
          tri(a, b, c);
          tri(a, c, d);
        }
      }
    }
    return { geo: { positions: positions, normals: normals, indices: indices } };
  }

  /* The surface the CAD kernel actually built, when the deployment has one.
   *
   * # Why this is not "another shape case"
   *
   * Every case below draws a PRIMITIVE, and this renderer has no boolean
   * operations: a bolt hole is a cylinder standing in a plate rather than a void
   * through it, a fillet is invisible, and a fuse of two bodies is two bodies.
   * The exported STEP file has always been correct — the divergence was only on
   * screen, which is the one place a person judges the result.
   *
   * So this is not a new shape. It is the same solid the exporter writes,
   * tessellated by OpenCASCADE and handed here, and it takes precedence over
   * every case below because it is the thing those cases were approximating.
   *
   * # Why the normals are accumulated rather than sent
   *
   * OCCT tessellates per FACE, so a box arrives as 24 vertices and not 8 — each
   * face owns its corners. Accumulating a normal per vertex therefore yields
   * FLAT shading for free, and a crease stays a crease. Averaging across a
   * shared vertex would round every edge of every machined part, which is the
   * one thing a CAD viewport must not do.
   *
   * See docs/plan-2026-09-08-solids-the-viewport-can-show.md */
  function kernelGeometry(mesh, scale) {
    var positions = mesh.vertices || [];
    /* A mesh reply is in MILLIMETRES and the stage is in the document's unit; scale
     * is the factor between them (drawBatches), and 1 or absent for a design in mm. */
    if (scale && scale !== 1) positions = Array.prototype.map.call(positions, function (v) { return v * scale; });
    var indices = mesh.triangles || [];
    var normals = new Array(positions.length);
    for (var n = 0; n < normals.length; n++) normals[n] = 0;

    for (var t = 0; t + 2 < indices.length; t += 3) {
      var a = indices[t] * 3, b = indices[t + 1] * 3, c = indices[t + 2] * 3;
      var abx = positions[b] - positions[a],
          aby = positions[b + 1] - positions[a + 1],
          abz = positions[b + 2] - positions[a + 2];
      var acx = positions[c] - positions[a],
          acy = positions[c + 1] - positions[a + 1],
          acz = positions[c + 2] - positions[a + 2];
      /* Not normalised per triangle on purpose: the cross product's LENGTH is
       * twice the triangle's area, so accumulating raw lets big triangles
       * outweigh slivers. A sliver at the edge of a fillet would otherwise tilt
       * the shading as much as the face it sits on. */
      var nx = aby * acz - abz * acy,
          ny = abz * acx - abx * acz,
          nz = abx * acy - aby * acx;
      for (var k = 0; k < 3; k++) {
        var at = indices[t + k] * 3;
        normals[at] += nx; normals[at + 1] += ny; normals[at + 2] += nz;
      }
    }
    for (var v = 0; v < normals.length; v += 3) {
      var len = Math.sqrt(normals[v] * normals[v] + normals[v + 1] * normals[v + 1] +
                          normals[v + 2] * normals[v + 2]);
      if (len > 0) { normals[v] /= len; normals[v + 1] /= len; normals[v + 2] /= len; }
      else { normals[v + 1] = 1; }   // a degenerate triangle: point it up rather than at nothing
    }
    return { geo: { positions: positions, normals: normals, indices: indices }, fromKernel: true };
  }

  /* ---- Repeats, mirroring geometry/repeat.go step for step -----------------
   *
   * Until 2026-09-13 this file had no repeat expansion at all. A part carrying
   * "repeat" was drawn ONCE, at its authored position, while the exporter, the
   * kernel and the contact sheet all built every copy — a sixty-spoke wheel on
   * screen had one spoke. The browser cannot call Go, so like the gear it holds a
   * copy of the rule, and TestRendererExpandsARepeatLikeTheExporter holds that
   * copy to Go's answer: ids, names, positions, rotations, dropped patterns and
   * which copies are tools being removed.
   * docs/bugfix/2026-09-13-repeat-copies-were-invisible-to-most-readers.md */
  var MAX_REPEAT = 512;   // geometry/repeat.go maxRepeat

  function repeatSweep(r) {
    var angle = r.angle || 0;
    if (angle === 0 || Math.abs(angle) >= 360) return 2 * Math.PI / r.count;
    return (angle * Math.PI / 180) / (r.count - 1);
  }

  /* geometry.RotationMatrix: row-major, RADIANS, term for term. The one copy of
   * the formula in this file; rotating a point and composing frames both read it. */
  function rowMajor(r) {
    var cx = Math.cos(r[0]), sx = Math.sin(r[0]);
    var cy = Math.cos(r[1]), sy = Math.sin(r[1]);
    var cz = Math.cos(r[2]), sz = Math.sin(r[2]);
    return [cy * cz, -cy * sz, sy,
            sx * sy * cz + cx * sz, -sx * sy * sz + cx * cz, -sx * cy,
            -cx * sy * cz + sx * sz, cx * sy * sz + sx * cz, cx * cy];
  }

  /* geometry rotate: a point turned by RotationMatrix. */
  function rotateLikeTheExporter(v, r) {
    var m = rowMajor(r);
    return [m[0] * v[0] + m[1] * v[1] + m[2] * v[2],
            m[3] * v[0] + m[4] * v[1] + m[5] * v[2],
            m[6] * v[0] + m[7] * v[1] + m[8] * v[2]];
  }

  function pad3(v) {
    return [(v && v[0]) || 0, (v && v[1]) || 0, (v && v[2]) || 0];
  }

  /* placeCopy */
  function repeatPosition(p, r, k) {
    var pos = pad3(p.position);
    if (!r.about) {
      var step = pad3(r.offset);
      return [pos[0] + step[0] * k, pos[1] + step[1] * k, pos[2] + step[2] * k];
    }
    var a = repeatSweep(r) * k;
    var rot = r.about === 'x' ? [a, 0, 0] : r.about === 'y' ? [0, a, 0] : [0, 0, a];
    return rotateLikeTheExporter(pos, rot);
  }

  /* turnCopy — a straight pattern keeps the part's own rotation untouched. */
  function repeatRotation(p, r, k) {
    if (!r.about) return p.rotation;
    var base = pad3(p.rotation);
    var a = repeatSweep(r) * k * 180 / Math.PI;   // rotation is in DEGREES
    if (r.about === 'x') return [base[0] + a, base[1], base[2]];
    if (r.about === 'y') return [base[0], base[1] + a, base[2]];
    return [base[0], base[1], base[2] + a];
  }

  function shallowCopy(o) {
    var out = {};
    for (var key in o) if (Object.prototype.hasOwnProperty.call(o, key)) out[key] = o[key];
    return out;
  }

  /* expandRepeats: every repeated part written out as its copies, features
   * retargeted to them. copyOf maps each copy's id to the part it came from. */
  function expandRepeats(parts, features) {
    var out = [], copies = {}, copyOf = {};
    (parts || []).forEach(function (p) {
      if (!p) return;
      var r = p.repeat;
      if (!r) { out.push(p); return; }
      if (!(r.count >= 2)) {
        var once = shallowCopy(p);
        delete once.repeat;
        out.push(once);
        return;
      }
      if (r.count > MAX_REPEAT) return;   // refused, as the exporter refuses it
      var label = p.name || p.id, made = [];
      for (var k = 0; k < r.count; k++) {
        var q = shallowCopy(p);
        delete q.repeat;
        q.id = p.id + '-' + (k + 1);
        q.name = label + ' ' + (k + 1);
        q.position = repeatPosition(p, r, k);
        q.rotation = repeatRotation(p, r, k);
        out.push(q);
        made.push(q.id);
        copyOf[q.id] = p.id;
      }
      copies[p.id] = made;
    });
    var retargeted = (features || []).map(function (f) {
      if (!f) return f;
      var g = shallowCopy(f);
      if (copies[f.of] && copies[f.of].length) g.of = copies[f.of][0];
      var withIDs = [];
      (f.with || []).forEach(function (id) {
        if (copies[id]) withIDs.push.apply(withIDs, copies[id]);
        else withIDs.push(id);
      });
      g.with = withIDs;
      return g;
    });
    return { parts: out, features: retargeted, copyOf: copyOf };
  }

  /* ---- Designs placed inside assemblies, mirroring geometry/tree.go + frame.go ---
   *
   * Phase 1, stage D1b of docs/plan-2026-09-13-millions-of-parts.md. A document may
   * place definitions through assemblies from a root; the exporter flattens that tree
   * into ordinary parts whose ids are the path of child ids ("front-left/damper"),
   * and this does the same, term for term, so the browser draws what the file holds.
   * TestRendererFlattensATreeLikeTheExporter holds it to Go's answer. */
  var MAX_TREE_DEPTH = 16;     // geometry/tree.go maxTreeDepth
  /* The most the VIEWPORT draws (geometry/limits.go maxViewportParts). Until Phase 6,
   * stage W1 this was 4096 and was the kernel's ceiling too; instanced drawing moved
   * the browser's own to what storage accepts by default, and the kernel builds 8192
   * for a view (Document.BuildRefusal in Go; docs/spikes/2026-09-15-ceiling-on-linux).
   * Kept at 100,000 by decision (limits.go says why): measured at 99,971, and a design
   * that large first uploads at most FIRST_VIEW_OCCURRENCES.
   * Fence: TestViewportLimitIsTheMeasuredOneAndAFirstViewStaysSmall. */
  var MAX_VIEWPORT_PARTS = 100000;
  var PATH_SEPARATOR = '/';
  // A tree part's display name: every child above it, then its own name (tree.go,
  // NameSeparator; docs/bugfix/2026-09-14-tree-copies-shared-display-names.md).
  var NAME_SEPARATOR = ' / ';

  function degreesToRadians3(r) {
    var p = pad3(r);
    return [p[0] * Math.PI / 180, p[1] * Math.PI / 180, p[2] * Math.PI / 180];
  }

  function mulMat3(a, b) {
    var out = new Array(9);
    for (var r = 0; r < 3; r++) {
      for (var c = 0; c < 3; c++) {
        out[r * 3 + c] = a[r * 3] * b[c] + a[r * 3 + 1] * b[3 + c] + a[r * 3 + 2] * b[6 + c];
      }
    }
    return out;
  }

  /* geometry.EulerDegreesFromMatrix: the inverse of RotationMatrix, in degrees. */
  function eulerDegreesFromMatrix(m) {
    var sy = Math.max(-1, Math.min(1, m[2]));
    var y = Math.asin(sy), cy = Math.cos(y), x, z;
    if (cy > 1e-9) {
      x = Math.atan2(-m[5], m[8]);
      z = Math.atan2(-m[1], m[0]);
    } else {
      z = 0;
      x = Math.atan2(m[3] * sy, m[4]);
    }
    var deg = 180 / Math.PI;
    return [x * deg, y * deg, z * deg];
  }

  /* ---- Placements with reflection, mirroring geometry/frame.go ------------------
   *
   * A placement is a position and a 3x3 matrix that may include one reflection. A
   * part STORES it as a rotation plus one flag, mirrored: "negate local x, then
   * rotate" (Phase 1, stage D1c). */
  var MIRROR_X = [-1, 0, 0, 0, 1, 0, 0, 0, 1];

  function reflectionAcross(axis) {
    if (axis === '') return [1, 0, 0, 0, 1, 0, 0, 0, 1];
    if (axis === 'x') return MIRROR_X;
    if (axis === 'y') return [1, 0, 0, 0, -1, 0, 0, 0, 1];
    if (axis === 'z') return [1, 0, 0, 0, 1, 0, 0, 0, -1];
    return null;
  }

  function placementOf(pos, rotDeg, mirrored) {
    var m = rowMajor(degreesToRadians3(rotDeg));
    if (mirrored) m = mulMat3(m, MIRROR_X);
    return { pos: pad3(pos), m: m };
  }

  function applyPlacement(p, v) {
    var m = p.m;
    return [m[0] * v[0] + m[1] * v[1] + m[2] * v[2] + p.pos[0],
            m[3] * v[0] + m[4] * v[1] + m[5] * v[2] + p.pos[1],
            m[6] * v[0] + m[7] * v[1] + m[8] * v[2] + p.pos[2]];
  }

  function thenPlacement(p, child) {
    return { pos: applyPlacement(p, child.pos), m: mulMat3(p.m, child.m) };
  }

  function det3(m) {
    return m[0] * (m[4] * m[8] - m[5] * m[7]) - m[1] * (m[3] * m[8] - m[5] * m[6]) +
           m[2] * (m[3] * m[7] - m[4] * m[6]);
  }

  /* geometry placement.stored */
  function storedPlacement(p) {
    var m = p.m, mirrored = false;
    if (det3(m) < 0) {
      m = mulMat3(m, MIRROR_X);
      mirrored = true;
    }
    return { position: p.pos.slice(), rotation: eulerDegreesFromMatrix(m), mirrored: mirrored };
  }

  /* ---- Patterns on a placed child, mirroring geometry/pattern.go ---------------
   *
   * Phase 1, stage D1c-2 of docs/plan-2026-09-13-millions-of-parts.md. A child may
   * carry "pattern" (linear, polar, grid, path); each copy is the child's own
   * placement carried by the pattern's transform in the parent's frame. patternCopies
   * answers null where Go refuses the pattern (an Error), and one unnamed slot where
   * Go draws it once with a warning. TestRendererFlattensATreeLikeTheExporter holds
   * this to Go's answer. */
  function patternSlot(n, at) {
    return { suffix: '-' + n, number: String(n), at: at };
  }

  function movedBy(v) {
    var at = placementOf(null, null, false);
    at.pos = v;
    return at;
  }

  function usableVector(v) {
    if (!v || !v.length) return null;
    var p = pad3(v);
    for (var i = 0; i < 3; i++) if (!isFinite(p[i])) return null;
    return p;
  }

  /* geometry pathStations */
  function pathStations(pts, count) {
    var segs = [], total = 0;
    for (var i = 0; i + 1 < pts.length; i++) {
      var d = [pts[i + 1][0] - pts[i][0], pts[i + 1][1] - pts[i][1], pts[i + 1][2] - pts[i][2]];
      var l = Math.sqrt(d[0] * d[0] + d[1] * d[1] + d[2] * d[2]);
      if (l === 0) continue;
      segs.push({ from: pts[i], dir: [d[0] / l, d[1] / l, d[2] / l], start: total, length: l });
      total += l;
    }
    if (!segs.length) return null;
    if (count < 2) return [{ point: segs[0].from, direction: segs[0].dir }];
    var out = [];
    for (var n = 0; n < count; n++) {
      var s = total * n / (count - 1), k = segs.length - 1;
      for (var j = 0; j < segs.length; j++) {
        if (s < segs[j].start + segs[j].length) { k = j; break; }
      }
      var sg = segs[k], t = Math.min(s - sg.start, sg.length);
      out.push({ point: [sg.from[0] + sg.dir[0] * t, sg.from[1] + sg.dir[1] * t, sg.from[2] + sg.dir[2] * t],
                 direction: sg.dir });
    }
    return out;
  }

  /* geometry rotationTaking: the smallest turn taking +X onto d (Rodrigues). */
  function rotationTaking(d) {
    var ay = -d[2], az = d[1];
    var s = Math.sqrt(ay * ay + az * az), c = d[0];
    if (s < 1e-12) return c > 0 ? [1, 0, 0, 0, 1, 0, 0, 0, 1] : [-1, 0, 0, 0, -1, 0, 0, 0, 1];
    var kx = 0, ky = ay / s, kz = az / s, v = 1 - c;
    return [c + kx * kx * v, kx * ky * v - kz * s, kx * kz * v + ky * s,
            ky * kx * v + kz * s, c + ky * ky * v, ky * kz * v - kx * s,
            kz * kx * v - ky * s, kz * ky * v + kx * s, c + kz * kz * v];
  }

  /* geometry Pattern.copies */
  function patternCopies(p) {
    var once = [{ suffix: '', number: '', at: placementOf(null, null, false) }];
    if (!p) return once;
    var out = [], n, count = p.count || 0;
    switch (String(p.kind || '').trim().toLowerCase()) {
      case 'linear': {
        var step = usableVector(p.offset);
        if (!step || count > MAX_REPEAT) return null;
        if (count < 2) return once;
        for (n = 1; n <= count; n++) {
          out.push(patternSlot(n, movedBy([step[0] * (n - 1), step[1] * (n - 1), step[2] * (n - 1)])));
        }
        return out;
      }
      case 'polar': {
        if (p.about !== 'x' && p.about !== 'y' && p.about !== 'z') return null;
        if (count > MAX_REPEAT) return null;
        if (count < 2) return once;
        var between = repeatSweep({ count: count, angle: p.angle });
        for (n = 1; n <= count; n++) {
          var a = between * (n - 1), at = placementOf(null, null, false);
          at.m = rowMajor(p.about === 'x' ? [a, 0, 0] : p.about === 'y' ? [0, a, 0] : [0, 0, a]);
          out.push(patternSlot(n, at));
        }
        return out;
      }
      case 'grid': {
        var rows = p.rows || 0, columns = p.columns || 0;
        if (rows < 1 || columns < 1 || rows * columns > MAX_REPEAT) return null;
        var row = usableVector(p.row_offset), col = usableVector(p.column_offset);
        if ((rows > 1 && !row) || (columns > 1 && !col)) return null;
        if (rows * columns < 2) return once;
        row = row || [0, 0, 0];
        col = col || [0, 0, 0];
        for (n = 0; n < rows * columns; n++) {
          var r = Math.floor(n / columns), cc = n % columns;
          out.push(patternSlot(n + 1, movedBy([row[0] * r + col[0] * cc, row[1] * r + col[1] * cc,
                                               row[2] * r + col[2] * cc])));
        }
        return out;
      }
      case 'path': {
        var path = p.path || [];
        if (path.length < 2) return null;
        var pts = [];
        for (var i = 0; i < path.length; i++) {
          var q = path[i] || {};
          if (q.radius || q.radius_from || q.via || q.x_from || q.y_from || q.z_from) return null;
          pts.push([q.x || 0, q.y || 0, q.z || 0]);
        }
        if (count > MAX_REPEAT) return null;
        var stations = pathStations(pts, count);
        if (!stations) return null;
        if (count < 2) return once;
        stations.forEach(function (st, k) {
          var at = movedBy(st.point);
          if (p.align) at.m = rotationTaking(st.direction);
          out.push(patternSlot(k + 1, at));
        });
        return out;
      }
    }
    return null;
  }

  /* ---- Interfaces, mirroring geometry/interface.go --------------------------
   *
   * Phase 1, stage D1d. A child attached `at` an interface is measured in that
   * interface's frame. The path names the parent's own interface ("mount") or one
   * on a sibling's placement ("front-left/hub", "bolt-3/seat"). reference answers
   * null where Go refuses the attachment, so the child is left out here too.
   * TestRendererFlattensATreeLikeTheExporter holds this to Go's answer. */
  function makeAttachments(asms, rootId) {
    var resolving = {};
    /* Whether a places a child, or one copy of a patterned child, under this id
     * (interface.go, places). */
    function places(a, id) {
      var children = a.children || [];
      for (var i = 0; i < children.length; i++) {
        var c = children[i] || {}, cid = String(c.id || '');
        if (cid === id) return true;
        var slots = patternCopies(c.pattern) || [];
        for (var s = 0; s < slots.length; s++) if (cid + slots[s].suffix === id) return true;
      }
      return false;
    }
    function interfaceIn(a, at) {
      var segs = String(at).split(PATH_SEPARATOR);
      for (var i = 0; i < segs.length; i++) if (!segs[i].trim()) return null;
      /* ‼️ A path resolved FROM THE ROOT may begin with the root's own id and means
       * the same without it (interface.go, interfaceIn). Go places that child, so the
       * browser must place it too — a copy that refuses what Go accepts draws a model
       * the exporter does not.
       * Fence: TestRendererFlattensATreeLikeTheExporter, "a placement from the root
       * whose path names the root". */
      if (segs.length > 1 && a.id === rootId && segs[0] === rootId && !places(a, segs[0])) {
        segs = segs.slice(1);
        at = segs.join(PATH_SEPARATOR);
      }
      if (segs.length === 1) {
        var faces = a.interfaces || [];
        for (var k = 0; k < faces.length; k++) {
          var f = faces[k] || {};
          if (f.id === at) return placementOf(f.position, f.rotation, false);
        }
        return null;
      }
      var hit = childFrameIn(a, segs[0]);
      if (!hit) return null;
      var rest = interfaceIn(hit.sub, segs.slice(1).join(PATH_SEPARATOR));
      return rest ? thenPlacement(hit.frame, rest) : null;
    }
    /* A patterned sibling named without a copy id falls through to null, as an
     * unknown child does: Go words the two refusals differently, the outcome is
     * the same, so there is nothing for the browser to tell apart. */
    function childFrameIn(a, seg) {
      var children = a.children || [];
      for (var i = 0; i < children.length; i++) {
        var c = children[i] || {};
        var slots = patternCopies(c.pattern);
        if (!slots) continue;
        for (var s = 0; s < slots.length; s++) {
          if (String(c.id || '') + slots[s].suffix !== seg) continue;
          var sub = asms[c.ref];
          if (!sub) return null;
          var reflect = reflectionAcross(c.mirror || '');
          if (!reflect) return null;
          var ref = reference(a, c);
          if (!ref) return null;
          var local = placementOf(c.position, c.rotation, false);
          local.m = mulMat3(local.m, reflect);
          return { frame: thenPlacement(ref, thenPlacement(slots[s].at, local)), sub: sub };
        }
      }
      return null;
    }
    function reference(a, c) {
      if (!c.at) return placementOf(null, null, false);
      var key = a.id + '\u0000' + c.id;
      if (resolving[key]) return null;   // attachments that lead back to themselves
      resolving[key] = true;
      try { return interfaceIn(a, c.at); } finally { delete resolving[key]; }
    }
    return { reference: reference };
  }

  /* geometry expandAssemblies: top-level parts, then every part the tree places.
   * definitionOf maps each placed part's id to the definition it came from. A
   * placement Go refuses is left out here too; Go says why, in the export notes. */
  function expandAssemblies(spec, within) {
    var definitionOf = {};
    var hasTree = !!(spec.root || (spec.assemblies && spec.assemblies.length) ||
                     (spec.definitions && spec.definitions.length));
    if (!hasTree) return { parts: spec.parts || [], definitionOf: definitionOf, features: [] };
    var parts = (spec.parts || []).slice(), features = [];
    if (!spec.root) return { parts: parts, definitionOf: definitionOf, features: features };

    var defs = {}, asms = {};
    (spec.definitions || []).forEach(function (p) {
      if (!p || !String(p.id || '').trim() || defs[p.id]) return;
      defs[p.id] = p;
    });
    (spec.assemblies || []).forEach(function (a) {
      if (!a || !String(a.id || '').trim() || asms[a.id] || defs[a.id]) return;
      asms[a.id] = a;
    });
    var root = asms[spec.root];
    if (!root) return { parts: parts, definitionOf: definitionOf, features: features };

    var attach = makeAttachments(asms, spec.root);
    /* geometry occurrenceFeatures (tree_features.go): an assembly's features in one
     * occurrence, naming the parts its placements wrote out. `of` takes a group's
     * first part, `with` all of them; a path that places nothing is left out. */
    function occurrenceFeatures(a, path, index) {
      var prefix = path.join(PATH_SEPARATOR);
      var placedIDs = function (p) {
        var r = index[p];
        if (!r || r[1] <= r[0]) return [];
        return parts.slice(r[0], r[1]).map(function (q) { return q.id; });
      };
      (a.features || []).forEach(function (f, n) {
        if (!f) return;
        var id = String(f.id || '').trim() || ('feature-' + (n + 1));
        var of = placedIDs(f.of);
        if (!of.length) return;   // refused by the exporter
        var tools = [], refused = false;
        (f.with || []).forEach(function (w) {
          var got = placedIDs(w);
          if (!got.length) refused = true;
          tools = tools.concat(got);
        });
        if (refused) return;
        var q = shallowCopy(f);
        q.id = prefix ? prefix + PATH_SEPARATOR + id : id;
        q.of = of[0];
        q.with = tools;
        features.push(q);
      });
    }
    function walk(a, path, names, onPath, frame, index) {
      if (path.length >= MAX_TREE_DEPTH) return true;
      var ids = {}, children = a.children || [];
      for (var i = 0; i < children.length; i++) {
        var c = children[i] || {};
        var cid = String(c.id || '');
        if (!cid.trim() || cid.indexOf(PATH_SEPARATOR) >= 0 || ids[cid]) continue;
        ids[cid] = true;
        var reflect = reflectionAcross(c.mirror || '');
        if (!reflect) continue;   // refused by the exporter, left out here too
        var local = placementOf(c.position, c.rotation, false);
        local.m = mulMat3(local.m, reflect);
        var sub = asms[c.ref], def = defs[c.ref];
        if (sub && onPath[sub.id]) continue;
        if (!sub && !def) continue;
        var slots = patternCopies(c.pattern);
        if (!slots) continue;
        // Measured in the interface's frame when attached (interface.go).
        var reference = attach.reference(a, c);
        if (!reference) continue;   // refused by the exporter, left out here too
        // The definition's own repeat, once, in the DEFINITION's frame (see tree.go).
        var defCopies = sub ? [] : expandRepeats([def], []).parts;
        var childStart = parts.length;
        for (var s = 0; s < slots.length; s++) {
          var slot = slots[s], slotStart = parts.length;
          var childPath = path.concat([cid + slot.suffix]);
          /* Pruned to one occurrence path when asked (occurrencePrefix): a slot that can
           * lead to nothing under that path is not walked, so one car of a million-part
           * fleet is placed without placing the other 33. */
          if (within && !within(childPath)) continue;
          var slotName = childPath.join(PATH_SEPARATOR);
          // The occurrence's display path, a label per level (tree.go, NameSeparator).
          var childNames = names.concat([(c.name || cid) + (slot.number ? ' ' + slot.number : '')]);
          var childFrame = thenPlacement(frame, thenPlacement(reference, thenPlacement(slot.at, local)));
          if (sub) {
            onPath[sub.id] = true;
            var subIndex = {};
            var stop = walk(sub, childPath, childNames, onPath, childFrame, subIndex);
            delete onPath[sub.id];
            for (var rel in subIndex) index[cid + slot.suffix + PATH_SEPARATOR + rel] = subIndex[rel];
            index[cid + slot.suffix] = [slotStart, parts.length];
            if (stop) return true;
            continue;
          }
          for (var j = 0; j < defCopies.length; j++) {
            var lp = defCopies[j], q = shallowCopy(lp), partStart = parts.length;
            var suffix = lp.id.indexOf(def.id) === 0 ? lp.id.slice(def.id.length) : lp.id;
            q.id = slotName + suffix;
            q.name = childNames.concat([lp.name || lp.id]).join(NAME_SEPARATOR);
            /* The occurrence's OWN name, without the path above it: the label the tree
             * gives this row ("Rivet 1"), and the definition part's label after it when
             * one definition placed several parts ("Ell 1" tells those copies apart).
             *
             * q.name is the whole occurrence path since #70 (NAME_SEPARATOR), which is
             * what a name must be where it travels alone — the Parts panel, a STEP file,
             * a message. A row that shows the path in a column of its own would then say
             * where twice and what never; this is the "what". Browser-only: Go's
             * geometry.Part has no such field and the exporter does not need one.
             * Fence: TestWorkbenchSearchRowsSayWhereEachOccurrenceIs. */
            q.occurrenceName = defCopies.length > 1
              ? childNames[childNames.length - 1] + NAME_SEPARATOR + (lp.name || lp.id)
              : childNames[childNames.length - 1];
            var st = storedPlacement(thenPlacement(childFrame, placementOf(lp.position, lp.rotation, !!lp.mirrored)));
            q.position = st.position;
            q.rotation = st.rotation;
            q.mirrored = st.mirrored;
            parts.push(q);
            definitionOf[q.id] = def.id;
            if (suffix) index[cid + slot.suffix + suffix] = [partStart, parts.length];
          }
          index[cid + slot.suffix] = [slotStart, parts.length];
        }
        if (slots.length > 1) index[cid] = [childStart, parts.length];
      }
      occurrenceFeatures(a, path, index);
      return false;
    }
    var onPath = {};
    onPath[root.id] = true;
    walk(root, [], [], onPath, placementOf(null, null, false), {});
    return { parts: parts, definitionOf: definitionOf, features: features };
  }

  /* partsToDraw is the list Studio.load draws: every part as the exporter builds
   * it, each marked with whether it is material being removed and, for a copy,
   * which part it is a copy of — so a state or a selection that names the part
   * applies to every copy. A kernel mesh is found by the DRAWN id on the part it
   * came from, and travels on the wrapper, never written into the document. */
  /* geometry occurrences (limits.go): how many parts a design places, counted
   * without placing them and saturating past `limit`, so a runaway description
   * costs nothing to refuse. The expansions' own rules: a refused pattern or repeat
   * places nothing, one of one places one, and a cycle counts nothing. */
  function repeatCount(p) {
    var r = p && p.repeat;
    if (!r || !(r.count >= 2)) return 1;
    return r.count > MAX_REPEAT ? 0 : r.count;
  }
  function occurrences(spec, limit) {
    var sat = function (n) { return n > limit ? limit + 1 : n; };
    var total = 0;
    (spec.parts || []).forEach(function (p) { if (p) total = sat(total + repeatCount(p)); });
    if (!spec.root) return total;
    var defs = {}, asms = {}, memo = {}, onPath = {};
    (spec.definitions || []).forEach(function (p) {
      if (p && String(p.id || '').trim() && !defs[p.id]) defs[p.id] = p;
    });
    (spec.assemblies || []).forEach(function (a) {
      if (a && String(a.id || '').trim() && !asms[a.id] && !defs[a.id]) asms[a.id] = a;
    });
    function count(id) {
      if (defs[id]) return repeatCount(defs[id]);
      var a = asms[id];
      if (!a || onPath[id]) return 0;
      if (memo[id] !== undefined) return memo[id];
      onPath[id] = true;
      var n = 0;
      (a.children || []).forEach(function (c) {
        c = c || {};
        var slots = patternCopies(c.pattern);
        n = sat(n + sat((slots ? slots.length : 0) * count(c.ref)));
      });
      delete onPath[id];
      memo[id] = n;
      return n;
    }
    return sat(total + count(spec.root));
  }

  /* geometry Document.ViewportRefusal: why a design is not drawn, in Go's words, or ''.
   * A design over the ceiling draws NOTHING — the first hundred thousand parts of a
   * design would pass for the design. TestRendererFlattensATreeLikeTheExporter holds
   * the text. */
  function drawRefusal(spec) {
    if (occurrences(spec || {}, MAX_VIEWPORT_PARTS) <= MAX_VIEWPORT_PARTS) return '';
    return 'This design places more than ' + MAX_VIEWPORT_PARTS + ' parts, which is the most the FORGE ' +
      'viewport draws at once. It is stored as it is; ' +
      'nothing was drawn.';
  }

  function partsToDraw(spec, under) {
    spec = spec || {};
    /* `under`, an occurrence path, draws only what that path places (partsUnder). Its
     * caller bounds it, having counted it first (occurrencesUnder). */
    if (!under && drawRefusal(spec)) return [];
    // The tree first, then repeats — the exporter's order (geometry.Expanded).
    var tree = expandAssemblies(spec, under ? occurrencePrefix(under) : null);
    // Top-level features first, then the tree's, as the exporter orders them.
    // Standard parts after the tree and before the repeats, as geometry.Expanded does.
    var expanded = expandRepeats(expandStandards(tree.parts, spec.units), (spec.features || []).concat(tree.features));
    var removed = {};
    expanded.features.forEach(function (f) {
      if (!f) return;
      var op = String(f.op).toLowerCase();
      /* A cut's tool is material being removed. A loft's stations are consumed
       * too — the kernel blends them into one body and they cease to exist as
       * parts — so they are ghosted for the same reason: drawn solid, two
       * stations read as two flat plates somebody meant to keep. */
      if (op !== 'cut' && op !== 'loft') return;
      (f.with || []).forEach(function (id) { removed[id] = true; });
    });
    var authored = {}, definitions = {};
    (spec.parts || []).forEach(function (p) { if (p) authored[p.id] = p; });
    (spec.definitions || []).forEach(function (p) { if (p && !definitions[p.id]) definitions[p.id] = p; });
    return expanded.parts.map(function (part) {
      var repeatOf = expanded.copyOf[part.id] || '';
      // The object this drawn part came from: a top-level part, the part a copy
      // was written out from, or the definition a placement names. Its kernel mesh
      // is kept there, keyed by the DRAWN id, so every placement keeps its own.
      var placedID = repeatOf || part.id;
      var definitionID = tree.definitionOf[placedID] || '';
      var source = (definitionID ? definitions[definitionID] : authored[placedID]) || part;
      var mesh = (source.meshes && source.meshes[part.id]) || part.mesh || null;
      return {
        spec: part,
        source: source,
        removed: !!removed[part.id],
        repeatOf: repeatOf,
        mesh: mesh,
        // Whether this part will be drawn from the kernel's mesh, which is already
        // in assembly coordinates — see modelMatrix. Load clears it when it falls
        // back to the primitive.
        fromKernel: !!(mesh && mesh.triangles && mesh.triangles.length)
      };
    }).filter(underFilter(under));
  }

  /* ---- Instanced drawing: what is drawn with what (Phase 6, stage W1) -----------
   *
   * # The problem this solves
   *
   * Until W1 every placed part was its own set of buffers and its own drawElements,
   * with where it goes, its colour and whether it is selected set as uniforms before
   * each call. A 30,000-occurrence car was 30,000 draw calls and 30,000 copies of a
   * few rivets' triangles on the GPU, which is why the viewport refused anything over
   * 4096 parts (Phase 3, stage S0).
   *
   * drawBatches groups the parts partsToDraw returns by the triangles they share —
   * one batch per distinct shape and finish — and gives every part a 4×4
   * column-major matrix. A batch is uploaded once and drawn with one instanced call;
   * the matrix, colour, opacity and highlight travel as per-instance attributes. It
   * is pure (no GL), so the fences run it in node and hold every instance to the
   * exporter's placement.
   *
   * # Three sources of a batch
   *
   *   - a kernel DEFINITION from the mesh reply (stage K4): its triangles in its own
   *     frame, and the reply's matrix for each occurrence of it;
   *   - a kernel mesh already in assembly coordinates (a part a feature changed, or
   *     the per-part channel partsToDraw still carries): its own batch, drawn with the
   *     identity — see modelMatrix for what placing one twice did;
   *   - a primitive: grouped by the fields buildGeometry reads, and placed by
   *     placementMatrix, which is the exporter's rotation term for term.
   *
   * ‼️ The finish is part of the key. Specular power and gloss are still uniforms, so
   * two parts of one shape in different finishes cannot share a call. Every part a
   * definition places shares its material, so a definition is still one call. */
  var IDENTITY = [1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1];

  /* Where a primitive goes: T · R · S, column-major, in float64. rowMajor is
   * geometry.RotationMatrix; a mirrored part negates its own x first (Part.Mirrored),
   * the rule the kernel and the Go mesh follow. */
  function placementMatrix(spec) {
    var m = rowMajor(degreesToRadians3(spec.rotation));
    var p = pad3(spec.position), sc = spec.scale || [1, 1, 1];
    var sx = num(sc[0], 1) * (spec.mirrored ? -1 : 1), sy = num(sc[1], 1), sz = num(sc[2], 1);
    return [m[0] * sx, m[3] * sx, m[6] * sx, 0,
            m[1] * sy, m[4] * sy, m[7] * sy, 0,
            m[2] * sz, m[5] * sz, m[8] * sz, 0,
            p[0], p[1], p[2], 1];
  }

  /* The fields buildGeometry reads, and nothing else: two parts with one key are the
   * same triangles. A field buildGeometry starts reading has to be added here, or two
   * different shapes are drawn as whichever of them came first. */
  function primitiveKey(part) {
    return JSON.stringify([part.shape, part.size || null, part.profile || null, part.holes || null,
                           part.path || null, !!part.path_closed, part.axis || null]);
  }

  /* A batch's own box and the sphere around it, in the batch's frame. Culling moves
   * the sphere by each instance's matrix; the level of detail draws the box. */
  function geometryBounds(positions) {
    var min = [Infinity, Infinity, Infinity], max = [-Infinity, -Infinity, -Infinity];
    for (var i = 0; i + 2 < positions.length; i += 3) {
      for (var k = 0; k < 3; k++) {
        var v = positions[i + k];
        if (v < min[k]) min[k] = v;
        if (v > max[k]) max[k] = v;
      }
    }
    if (!(min[0] <= max[0])) { min = [0, 0, 0]; max = [0, 0, 0]; }
    var half = [(max[0] - min[0]) / 2, (max[1] - min[1]) / 2, (max[2] - min[2]) / 2];
    return { min: min, max: max, half: half, radius: length3(half),
             centre: [(min[0] + max[0]) / 2, (min[1] + max[1]) / 2, (min[2] + max[2]) / 2] };
  }

  /* drawn is partsToDraw's list; built is the mesh reply (GET /v1/geometry/{id}/mesh)
   * or null; opts.wide says whether this browser indexes past 65,535 vertices; opts.toMM
   * is unitToMM of the document's units.
   *
   * # Why a reply is scaled here (2026-09-15)
   *
   * Every mesh reply — the kernel's, the Go tessellator's, the whole design's and a
   * subtree's — is in MILLIMETRES (cad.Kernel, geometry.TessellateInstances), and
   * everything else on the stage is in the DOCUMENT's unit: the primitives, the
   * dimension overlays, and the boxes a design loaded a subtree at a time draws until
   * its geometry arrives. They were drawn as they arrived, so an inch design's built
   * surface was 25.4 times its own boxes and dimensions, and a metre design's a
   * thousand. The reply is put in the document's unit, once, where both the whole
   * load and addSubtree read it.
   * docs/bugfix/2026-09-15-mesh-replies-were-drawn-in-millimetres-on-a-stage-in-the-documents-units.md
   * Fence: TestMeshSubtree_ADesignInInchesOrMetresIsDrawnWhereItsPrimitivesAre. */
  function drawBatches(drawn, built, opts) {
    opts = opts || {};
    var wide = opts.wide !== false;
    var fromMM = opts.toMM > 0 ? 1 / opts.toMM : 1;
    var definitions = (built && built.definitions) || [];
    var placedMesh = {}, instanceOf = {};
    ((built && built.parts) || []).forEach(function (m) {
      if (m && m.id && m.triangles && m.triangles.length) placedMesh[m.id] = m;
    });
    ((built && built.instances) || []).forEach(function (inst) {
      var d = inst && definitions[inst.definition];
      if (d && d.triangles && d.triangles.length && inst.matrix && inst.matrix.length === 16) {
        instanceOf[inst.id] = inst;
      }
    });
    var byKey = {}, batches = [], approximations = [];
    (drawn || []).forEach(function (d) {
      var part = d.spec, shading = shadingFor(part.material);
      var inst = instanceOf[part.id];
      var mesh = inst ? definitions[inst.definition] : (placedMesh[part.id] || (d.fromKernel ? d.mesh : null));
      /* A tessellation this browser cannot index is drawn as its primitive instead,
       * and named — truncating to 65,535 vertices would draw a shape nobody built,
       * which is worse than the approximation everybody has been looking at. */
      var narrow = !!(mesh && !wide && mesh.vertices.length / 3 > 65535);
      var key, matrix, make, drawnShape = null;
      if (mesh && !narrow) {
        key = inst ? 'definition:' + inst.definition : 'placed:' + part.id;
        matrix = inst ? Array.prototype.slice.call(inst.matrix) : IDENTITY.slice();
        /* The copy's translation and its definition's vertices, from millimetres. A
         * placed part's vertices are already where it is, so only they are scaled. */
        if (inst) { matrix[12] *= fromMM; matrix[13] *= fromMM; matrix[14] *= fromMM; }
        make = function () { return kernelGeometry(mesh, fromMM); };
      } else {
        var shape = { shape: part.shape, size: part.size, profile: part.profile, holes: part.holes,
                      path: part.path, path_closed: part.path_closed, axis: part.axis };
        key = 'shape:' + primitiveKey(part) + (narrow ? '|narrow' : '');
        matrix = placementMatrix(part);
        drawnShape = narrow ? null : shape;
        make = function () {
          var built2 = buildGeometry(shape);
          if (narrow) {
            built2.approximated = 'the built solid needs ' + Math.round(mesh.vertices.length / 3) +
              ' vertices and this browser indexes 65,535, so the primitive is drawn instead';
          }
          return built2;
        };
      }
      key += '|' + shading.join(',');
      var b = byKey[key];
      if (!b) {
        var geo = make();
        b = byKey[key] = {
          key: key, fromKernel: !!geo.fromKernel, definition: inst ? inst.definition : -1,
          shading: shading, geo: geo.geo, approximated: geo.approximated || '',
          bounds: geometryBounds(geo.geo.positions), instances: [],
          /* A primitive that is curved and drawn as itself can be drawn finer (A5). */
          shape: drawnShape && !geo.approximated ? drawnShape : null,
          curve: drawnShape && !geo.approximated ? curveOf(drawnShape) : null
        };
        batches.push(b);
      }
      if (b.approximated) approximations.push((part.name || part.id) + ': ' + b.approximated);
      b.instances.push({ id: part.id, matrix: matrix, spec: part, removed: !!d.removed,
                         repeatOf: d.repeatOf || '' });
    });
    return { batches: batches, approximations: approximations };
  }

  /* ---- The assembly tree as a browser lists it (Phase 6, stage W2) ---------------
   *
   * treeChildren is the rows under ONE assembly, computed when somebody opens it and
   * not before: a car's tree is a few hundred rows written once, and listing its
   * 30,000 occurrences up front would put a list nobody can read into the page. A
   * child placed more than once by a pattern is one row whose slots are its copies.
   * A child the exporter refuses is left out here too, by expandAssemblies' rules. */
  function treeChildren(spec, ref, path) {
    spec = spec || {};
    var defs = {}, asms = {};
    (spec.definitions || []).forEach(function (p) {
      if (p && String(p.id || '').trim() && !defs[p.id]) defs[p.id] = p;
    });
    (spec.assemblies || []).forEach(function (a) {
      if (a && String(a.id || '').trim() && !asms[a.id] && !defs[a.id]) asms[a.id] = a;
    });
    var a = asms[ref];
    if (!a) return [];
    var attach = makeAttachments(asms), ids = {}, rows = [];
    (a.children || []).forEach(function (c) {
      c = c || {};
      var cid = String(c.id || '');
      if (!cid.trim() || cid.indexOf(PATH_SEPARATOR) >= 0 || ids[cid]) return;
      ids[cid] = true;
      if (!reflectionAcross(c.mirror || '')) return;
      if (!asms[c.ref] && !defs[c.ref]) return;
      var slots = patternCopies(c.pattern);
      if (!slots || !attach.reference(a, c)) return;
      var childPath = path ? path + PATH_SEPARATOR + cid : cid;
      rows.push({
        path: childPath, id: cid, label: c.name || cid, ref: c.ref, assembly: !!asms[c.ref],
        slots: slots.length > 1 ? slots.map(function (s) { return childPath + s.suffix; }) : null
      });
    });
    return rows;
  }

  /* Whether a drawn part is under a tree row or IS the part a row names.
   *
   * A path names its own occurrence ("front-left/damper"), everything under it
   * ("front-left/damper/…"), and every copy a pattern or repeat wrote out along the
   * way: the exporter names a copy by adding "-n" to what it was copied from, at ANY
   * level, so the row "front-left/pin" reaches "front-left-2/pin-3-1" — the second
   * copy of the corner, the third pin of its polar pattern, the first of the pin's own
   * repeat. Each segment of the path may therefore carry copy numbers.
   *
   * ‼️ That naming is also why a sibling literally CALLED "front-left-2" is matched by
   * "front-left" — Go allows the id, and the two cannot be told apart from the drawn
   * ids alone. TestRendererFlattensATreeLikeTheExporter has a document that does it. */
  function occurrenceMatcher(path) {
    var p = String(path || '');
    if (!p) return null;
    var pattern = new RegExp('^' + p.split(PATH_SEPARATOR).map(function (seg) {
      return seg.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '(-\\d+)*';
    }).join(PATH_SEPARATOR) + '(/|$)');
    return function (id, repeatOf) {
      return (!!repeatOf && repeatOf === p) || pattern.test(String(id || ''));
    };
  }

  function underFilter(path) {
    if (!path) return function () { return true; };
    var m = occurrenceMatcher(path);
    return function (d) { return m(d.spec.id, d.repeatOf); };
  }

  /* ---- Browsing a design past the viewport's limit (2026-09-17) ------------------
   *
   * # The decision
   *
   * MAX_VIEWPORT_PARTS bounds what is DRAWN at once, not what may be browsed (taken by
   * the coordinator under damon's delegation, after the workbench check found a stored
   * 1,020,782-part design listed in the tree with nothing searchable and no row that
   * would load). A design past it is still refused WHOLE (drawRefusal, in Go's words),
   * and is then browsed a subtree at a time: a row loads when it is opened, selected or
   * isolated, as long as what is drawn stays within the limit; a subtree that alone
   * places more is refused by name, with its count; and search reads every occurrence
   * the tree lists, not only what is drawn.
   *
   * # Why nothing here places the whole design
   *
   * Placing a million occurrences in the browser is ~10 s and over a gigabyte (the
   * 30,023-part car alone: 302 ms and 35 MB in node). So each reader below walks only
   * what it needs: occurrencePrefix prunes expandAssemblies to one path, and
   * occurrencesUnder and searchTree read the tree's definitions and patterns without
   * composing a frame or making a part. */

  /* Whether a slot's path (an array of segments) can lead to an occurrence at or under
   * `path`, by occurrenceMatcher's rule segment for segment: each segment it has so far
   * matches the path's segment there, copy numbers allowed. */
  function occurrencePrefix(path) {
    var segs = String(path || '').split(PATH_SEPARATOR).map(function (seg) {
      return new RegExp('^' + seg.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '(-\\d+)*$');
    });
    return function (childPath) {
      var n = Math.min(childPath.length, segs.length);
      for (var i = 0; i < n; i++) if (!segs[i].test(childPath[i])) return false;
      return true;
    };
  }

  /* The occurrences one path places, as the list Studio draws them. */
  function partsUnder(spec, path) {
    return partsToDraw(spec || {}, String(path || ''));
  }

  /* The tree prepared once per call: each assembly's children, with what the exporter
   * refuses left out as expandAssemblies leaves it out, and each definition's copies. */
  function preparedTree(spec) {
    var defs = {}, asms = {};
    (spec.definitions || []).forEach(function (p) {
      if (!p || !String(p.id || '').trim() || defs[p.id]) return;
      defs[p.id] = p;
    });
    (spec.assemblies || []).forEach(function (a) {
      if (!a || !String(a.id || '').trim() || asms[a.id] || defs[a.id]) return;
      asms[a.id] = a;
    });
    var root = asms[spec.root] || null;
    var attach = root ? makeAttachments(asms, spec.root) : null;
    var children = {}, copies = {};
    function childrenOf(a) {
      if (children[a.id]) return children[a.id];
      var ids = {}, out = [];
      (a.children || []).forEach(function (c) {
        c = c || {};
        var cid = String(c.id || '');
        if (!cid.trim() || cid.indexOf(PATH_SEPARATOR) >= 0 || ids[cid]) return;
        ids[cid] = true;
        if (!reflectionAcross(c.mirror || '')) return;
        var sub = asms[c.ref], def = defs[c.ref];
        if (!sub && !def) return;
        var slots = patternCopies(c.pattern);
        if (!slots || !attach.reference(a, c)) return;
        out.push({ id: cid, label: c.name || cid, sub: sub || null, def: def || null, slots: slots });
      });
      return (children[a.id] = out);
    }
    function copiesOf(def) {
      if (copies[def.id]) return copies[def.id];
      return (copies[def.id] = expandRepeats([def], []).parts.map(function (lp) {
        return { suffix: lp.id.indexOf(def.id) === 0 ? lp.id.slice(def.id.length) : lp.id, name: lp.name || lp.id };
      }));
    }
    return { root: root, childrenOf: childrenOf, copiesOf: copiesOf };
  }

  /* How many parts `path` places, counted without placing them, so a refusal can say how
   * many. The expansion's rules: what expandAssemblies leaves out is not counted. */
  function occurrencesUnder(spec, path) {
    spec = spec || {};
    var within = occurrencePrefix(path), matcher = occurrenceMatcher(path), total = 0;
    var top = expandRepeats(spec.parts || [], []);
    top.parts.forEach(function (p) { if (matcher(p.id, top.copyOf[p.id] || '')) total++; });
    if (!spec.root) return total;
    var t = preparedTree(spec);
    if (!t.root) return total;
    var memo = {};
    function all(a, onPath, depth) {   // everything one assembly places
      if (depth >= MAX_TREE_DEPTH) return 0;
      if (memo[a.id] !== undefined) return memo[a.id];
      var n = 0;
      t.childrenOf(a).forEach(function (c) {
        if (c.sub && onPath[c.sub.id]) return;
        if (!c.sub) { n += c.slots.length * t.copiesOf(c.def).length; return; }
        onPath[c.sub.id] = true;
        n += c.slots.length * all(c.sub, onPath, depth + 1);
        delete onPath[c.sub.id];
      });
      return (memo[a.id] = n);
    }
    function walk(a, path, onPath) {
      if (path.length >= MAX_TREE_DEPTH) return 0;
      var n = 0;
      t.childrenOf(a).forEach(function (c) {
        if (c.sub && onPath[c.sub.id]) return;
        c.slots.forEach(function (slot) {
          var childPath = path.concat([c.id + slot.suffix]);
          if (!within(childPath)) return;
          var slotName = childPath.join(PATH_SEPARATOR);
          if (!c.sub) {
            t.copiesOf(c.def).forEach(function (lp) { if (matcher(slotName + lp.suffix, '')) n++; });
            return;
          }
          onPath[c.sub.id] = true;
          n += matcher(slotName, '') ? all(c.sub, onPath, childPath.length) : walk(c.sub, childPath, onPath);
          delete onPath[c.sub.id];
        });
      });
      return n;
    }
    var onPath = {};
    onPath[t.root.id] = true;
    return total + walk(t.root, [], onPath);
  }

  /* Every occurrence the tree lists, in the exporter's order, named as findOccurrences
   * matches it and labelled as occurrenceLabel labels it, without making a part:
   * visit(lowercased path, lowercased display path, path, label). A visit answering true
   * stops the walk. searchTree and treeSearchIndex both read this, so the index cannot
   * name an occurrence differently from the walk it replaced. */
  function eachTreeOccurrence(spec, visit) {
    spec = spec || {};
    var stopped = false;
    expandRepeats(expandStandards(spec.parts || [], spec.units), []).parts.forEach(function (p) {
      var name = String(p.name || '');
      if (!stopped && visit(String(p.id).toLowerCase(), name.toLowerCase(), p.id, name || p.id)) stopped = true;
    });
    if (stopped || !spec.root) return;
    var t = preparedTree(spec);
    if (!t.root) return;
    function walk(a, path, lowPath, names, depth, onPath) {
      // expandAssemblies stops the whole walk at the depth limit, so this does too.
      if (depth >= MAX_TREE_DEPTH) { stopped = true; return; }
      var kids = t.childrenOf(a);
      for (var i = 0; i < kids.length && !stopped; i++) {
        var c = kids[i];
        if (c.sub && onPath[c.sub.id]) continue;
        var lowID = c.id.toLowerCase(), copies = c.sub ? null : t.copiesOf(c.def);
        for (var s = 0; s < c.slots.length && !stopped; s++) {
          var slot = c.slots[s];
          var slotName = (path ? path + PATH_SEPARATOR : '') + c.id + slot.suffix;
          var lowSlot = (path ? lowPath + PATH_SEPARATOR : '') + lowID + slot.suffix;
          var childName = c.label + (slot.number ? ' ' + slot.number : '');
          var childNames = names ? names + NAME_SEPARATOR + childName : childName;
          if (c.sub) {
            onPath[c.sub.id] = true;
            walk(c.sub, slotName, lowSlot, childNames, depth + 1, onPath);
            delete onPath[c.sub.id];
            continue;
          }
          for (var j = 0; j < copies.length && !stopped; j++) {
            var lp = copies[j];
            if (visit(lowSlot + lp.suffix.toLowerCase(), (childNames + NAME_SEPARATOR + lp.name).toLowerCase(),
                      slotName + lp.suffix, copies.length > 1 ? childName + NAME_SEPARATOR + lp.name : childName)) {
              stopped = true;
            }
          }
        }
      }
    }
    var onPath = {};
    onPath[t.root.id] = true;
    walk(t.root, '', '', '', 0, onPath);
  }

  /* Studio.findOccurrences' answer for a design that is not placed in the browser: every
   * occurrence the tree lists (eachTreeOccurrence), matched on its path and name as
   * findOccurrences matches them. Fence: TestRendererBrowsesADesignPastTheViewportLimit.
   * A browsed design is searched through treeSearchIndex; this walk is what that index
   * answers like, and what answers when it cannot. */
  function searchTree(spec, query, limit) {
    var q = String(query || '').trim().toLowerCase(), found = [], total = 0;
    limit = limit || 50;
    if (!q) return { found: found, total: 0 };
    eachTreeOccurrence(spec, function (lowID, lowName, id, label) {
      if (lowID.indexOf(q) < 0 && lowName.indexOf(q) < 0) return false;
      total++;
      if (found.length < limit) found.push({ id: id, label: label });
      return false;
    });
    return { found: found, total: total };
  }

  /* ---- A browsed design's search, through an index built once (2026-09-17) --------
   *
   * searchTree walks the whole tree on every keystroke: 0.4-1.0 s per character typed
   * over the 1,020,782-part fleet in the browser, most of it building each occurrence's
   * path and name again. So the walk is done ONCE per design, on its first search (as
   * W2 meant the drawn design's index to be built), and writes one line per occurrence
   * into a single string, in the walk's order:
   *
   *     lowercased path TAB lowercased display path TAB path, if its case differs TAB label LF
   *
   * from exactly the strings searchTree matches and answers with (eachTreeOccurrence).
   * A query is then a run of String.indexOf over that one string: the first place q is
   * found in a line is in its first two fields exactly when searchTree matches that
   * occurrence (q holds no TAB or LF, so no match straddles a field), and the next
   * search starts at the following line, so each occurrence is counted once. Same
   * answers, same order, same total.
   * Fence: TestRendererSearchesABrowsedDesignThroughAnIndexLikeTheWalk.
   *
   * ‼️ Memory, stated: one string of about 72 characters an occurrence (71 MB of heap
   * for the fleet, measured in node), held while the design is open and only once
   * something was searched. A query with a TAB or LF in it, or a design whose ids or
   * names hold one, is answered by the walk (treeSearchIndex answers null for it). */
  function treeSearchIndex(spec) {
    var lines = [], bad = false, separators = /[\t\n]/;
    eachTreeOccurrence(spec, function (lowID, lowName, id, label) {
      id = String(id);
      label = String(label);
      if (separators.test(id) || separators.test(lowName) || separators.test(label)) return (bad = true);
      lines.push(lowID + '\t' + lowName + '\t' + (id === lowID ? '' : id) + '\t' + label + '\n');
      return false;
    });
    return bad ? null : { text: lines.join(''), lines: lines.length };
  }

  /* searchTree's answer, read from treeSearchIndex's string. */
  function searchTreeIndexed(index, query, limit) {
    var q = String(query || '').trim().toLowerCase(), found = [], total = 0;
    limit = limit || 50;
    if (!q) return { found: found, total: 0 };
    var s = index.text, from = 0;
    for (;;) {
      var at = s.indexOf(q, from);
      if (at < 0) break;
      var start = s.lastIndexOf('\n', at) + 1, end = s.indexOf('\n', at);
      var t1 = s.indexOf('\t', start), t2 = s.indexOf('\t', t1 + 1);
      from = end + 1;
      /* The first match in a line is in its first two fields, or searchTree does not
       * match that occurrence: the rest of the line is only what to answer with. */
      if (at >= t2) continue;
      total++;
      if (found.length < limit) {
        var t3 = s.indexOf('\t', t2 + 1);
        found.push({ id: t3 > t2 + 1 ? s.slice(t2 + 1, t3) : s.slice(start, t1), label: s.slice(t3 + 1, end) });
      }
    }
    return { found: found, total: total };
  }

  function buildGeometry(part) {
    /* The built solid wins over the primitive that approximated it. */
    if (part.mesh && part.mesh.triangles && part.mesh.triangles.length) {
      return kernelGeometry(part.mesh);
    }
    /* A retired word is resolved before anything is drawn, and the note travels
     * on `approximated` — the same channel every other substitution uses, which
     * is what puts it in the provenance banner rather than nowhere. */
    var retired = RETIRED[part.shape];
    var shape = retired ? retired.as : part.shape;
    var built = buildResolved(shape, part);
    if (retired) built.approximated = retired.because;
    /* Shaded smooth where the surface is curved (A5, 2026-09-18): these builders give
     * every facet its own flat normal, so a revolved boss or a rounded corner showed each
     * of its 40 steps. Positions and triangles are untouched — only how light reads them. */
    if (SMOOTHED_SHAPES[shape] && !built.approximated) built.geo = smoothNormals(built.geo, CREASE_DEGREES);
    return built;
  }

  /* The builders whose facets approximate a curve with flat normals. A cylinder, cone and
   * sphere are built with the true normal already, and a box has no curve. */
  var SMOOTHED_SHAPES = { extrusion: true, revolve: true, sweep: true, section: true, gear: true };

  /* Two facets meeting at more than this are an EDGE: shaded as a crease, and drawn as a
   * feature line when those are on. Under it they are one curved surface. 35° keeps every
   * right angle and chamfer sharp and joins a 40-step circle (9° a step) into one surface;
   * a gear's flank steps join and its tip corners stay sharp. */
  var CREASE_DEGREES = 35;

  /* Normals averaged over the facets that meet at a point within the crease angle,
   * weighted by the angle each facet makes at that point — so a quad counts the same
   * however it was split into triangles, and a smooth surface's normal points where the
   * surface does (an area weighting leans towards whichever side has more triangles
   * there: 1.5° on a revolved tube). Vertices are matched by POSITION, because these builders give each
   * facet its own corners; a facet's own corners keep their own answer, so a vertex
   * shared by facets on both sides of a crease is split the way the facets are. */
  function smoothNormals(geo, creaseDeg) {
    var pos = geo.positions, idx = geo.indices, tris = idx.length / 3;
    if (!tris) return geo;
    var limit = Math.cos(creaseDeg * Math.PI / 180);
    var raw = new Float64Array(tris * 3), unit = new Float64Array(tris * 3), t, k;
    for (t = 0; t < tris; t++) {
      var a = idx[t * 3] * 3, b = idx[t * 3 + 1] * 3, c = idx[t * 3 + 2] * 3;
      var ux = pos[b] - pos[a], uy = pos[b + 1] - pos[a + 1], uz = pos[b + 2] - pos[a + 2];
      var vx = pos[c] - pos[a], vy = pos[c + 1] - pos[a + 1], vz = pos[c + 2] - pos[a + 2];
      var nx = uy * vz - uz * vy, ny = uz * vx - ux * vz, nz = ux * vy - uy * vx;
      /* Faced the way the builder said, whatever the winding: the builder's normal is the
       * statement of which side is outside. */
      var gn = geo.normals;
      if (nx * gn[a] + ny * gn[a + 1] + nz * gn[a + 2] < 0) { nx = -nx; ny = -ny; nz = -nz; }
      var l = Math.sqrt(nx * nx + ny * ny + nz * nz);
      raw[t * 3] = nx; raw[t * 3 + 1] = ny; raw[t * 3 + 2] = nz;
      if (l > 0) { unit[t * 3] = nx / l; unit[t * 3 + 1] = ny / l; unit[t * 3 + 2] = nz / l; }
    }
    var bd = geometryBounds(pos), q = Math.max(bd.half[0], bd.half[1], bd.half[2], 1e-9) * 1e-7;
    var byPoint = {}, keyOf = new Array(idx.length), corner = new Float64Array(idx.length);
    for (k = 0; k < idx.length; k++) {
      var v = idx[k] * 3;
      var key = Math.round(pos[v] / q) + ',' + Math.round(pos[v + 1] / q) + ',' + Math.round(pos[v + 2] / q);
      keyOf[k] = key;
      (byPoint[key] || (byPoint[key] = [])).push(k);
      /* The facet's angle at this corner. */
      var base = k - k % 3, p1 = idx[base + (k % 3 + 1) % 3] * 3, p2 = idx[base + (k % 3 + 2) % 3] * 3;
      var e1 = [pos[p1] - pos[v], pos[p1 + 1] - pos[v + 1], pos[p1 + 2] - pos[v + 2]];
      var e2 = [pos[p2] - pos[v], pos[p2 + 1] - pos[v + 1], pos[p2 + 2] - pos[v + 2]];
      var l12 = length3(e1) * length3(e2);
      corner[k] = l12 > 0 ? Math.acos(Math.max(-1, Math.min(1, dot(e1, e2) / l12))) : 0;
    }
    var normals = geo.normals.slice();
    for (k = 0; k < idx.length; k++) {
      var own = Math.floor(k / 3), sx = 0, sy = 0, sz = 0, around = byPoint[keyOf[k]];
      if (!(unit[own * 3] || unit[own * 3 + 1] || unit[own * 3 + 2])) continue;
      for (var j = 0; j < around.length; j++) {
        var o = Math.floor(around[j] / 3), w = corner[around[j]];
        if (unit[own * 3] * unit[o * 3] + unit[own * 3 + 1] * unit[o * 3 + 1] +
            unit[own * 3 + 2] * unit[o * 3 + 2] < limit) continue;
        sx += unit[o * 3] * w; sy += unit[o * 3 + 1] * w; sz += unit[o * 3 + 2] * w;
      }
      var len = Math.sqrt(sx * sx + sy * sy + sz * sz);
      if (!(len > 0)) continue;
      var at = idx[k] * 3;
      normals[at] = sx / len; normals[at + 1] = sy / len; normals[at + 2] = sz / len;
    }
    return { positions: pos, normals: normals, indices: idx };
  }

  /* The shape actually drawn, given the word after retirement is applied. Split
   * out so there is ONE switch: a second one for retired words would be a second
   * place to add a case to, and the case somebody forgot would draw a bounding
   * box with no note. */
  function buildResolved(shape, part) {
    var s = part.size || {};
    switch (shape) {
      case 'box':      return { geo: boxGeometry(num(s.width,1), num(s.height,1), num(s.depth,1)) };
      case 'cylinder': return { geo: cylinderGeometry(num(s.radius,0.5), cylinderLength(s), radialSegments(), num(s.radius_top, num(s.radius,0.5))) };
      case 'cone':     return { geo: cylinderGeometry(num(s.radius,0.5), cylinderLength(s), radialSegments(), 0) };
      case 'sphere':   return { geo: sphereGeometry(num(s.radius,0.5), TESSELLATION.sphereRadial * DETAIL) };
      case 'plane':    return { geo: planeGeometry(num(s.width,1), num(s.depth,1)) };
      case 'extrusion': return extrusionGeometry(part.profile || [], num(s.depth, 1), part.holes);
      case 'revolve':   return revolveGeometry(part.profile || [], part.axis, part.holes);
      case 'sweep':     return sweepGeometry(part.profile || [], part.path || [], part.holes, part.path_closed);
      /* A drawing with no thickness — a loft's station.
       *
       * Drawn as a sheet, because that is what it is: a section has no depth to
       * give it, and drawing it with one would show a slab where somebody wrote
       * an outline. Where the deployment has a CAD kernel this is replaced by
       * the blended solid before anybody sees it (workbench.js), and where it
       * does not, the document's own feature notes say the body between the
       * stations is not on screen. */
      case 'section':   return extrusionGeometry(part.profile || [], 0, part.holes);
      /* A spur gear: its numbers become an extrusion's outline, the way the
       * exporter builds it (gear.go). Any outline the part carries is ignored,
       * as it is there. */
      /* A standard part reaches here only when expandStandards refused it. */
      case 'standard':
        return {
          geo: boxGeometry(1, 1, 1),
          approximated: 'this standard part names a designation FORGE does not have, or the document ' +
                        'states no unit to size it in — the notes say which — so it is drawn as a unit box'
        };
      case 'gear': {
        var gear = gearOutline(s);
        if (!gear) {
          return {
            geo: boxGeometry(1, 1, 1),
            approximated: 'the numbers on this gear do not describe one that can be drawn — ' +
                          'the notes say which — so it is drawn as a unit box'
          };
        }
        return extrusionGeometry(gear.profile, gear.depth, gear.holes);
      }
      default:
        return {
          geo: boxGeometry(num(s.width,1), num(s.height,1), num(s.depth,1)),
          approximated: 'shape "' + String(part.shape) + '" is not supported by this renderer ' +
                        'and is drawn as a bounding box'
        };
    }
  }

  /* A cylinder's length, reading "depth" when "height" is absent.
   *
   * Kept in step with sizeSynonyms in internal/domain/geometry/mesh.go — a
   * reading that happened in the exporter and not here would draw a different
   * solid from the one in the file, which is the single worst thing this
   * viewport can do.
   *
   * A model asked for a wheel wrote {"depth": 250, "radius": 350} on a cylinder.
   * A cylinder has no "depth" in this vocabulary, so 250 can only be its length;
   * before this it was discarded and the height defaulted to 1, which is why the
   * sports car's wheels were ⌀700 discs a millimetre wide.
   * Fence: TestCylinderDepthIsReadTheSameWayInBothPlaces */
  function cylinderLength(s) {
    if (typeof s.height === 'number' && isFinite(s.height)) return s.height;
    if (typeof s.depth === 'number' && isFinite(s.depth)) return s.depth;
    return 1;
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

  /* Where each attribute lives, bound before linking (Phase 6, stage W1).
   *
   * Everything a draw used to set per part as a uniform — where it goes, its colour
   * and opacity, whether it is selected — is an ATTRIBUTE with one value per
   * instance, so one call draws every copy of a batch.
   *
   * ‼️ Two rules the numbers follow, neither visible from the shader:
   *
   *   - aPos is location 0 in BOTH programs and is never instanced. WebGL1 with
   *     ANGLE_instanced_arrays refuses a draw whose attribute 0 has a divisor on
   *     some drivers, and the line program shares location 0 with the part program.
   *   - Eight locations in all (a mat4 takes four). Eight is the fewest WebGL
   *     guarantees, so there is no room for a ninth per-instance value: add one by
   *     packing it, not by adding an attribute.
   *
   * The normal is turned by the cofactor of the model matrix, which is the
   * inverse-transpose times the determinant: GLSL ES 1.00 has no inverse(), and the
   * determinant's SIGN is put back so a mirrored copy is lit from the side it faces. */
  var ATTRIB = { pos: 0, normal: 1, model: 2, colour: 6, highlight: 7 };
  var PART_ATTRIBUTES = { aPos: ATTRIB.pos, aNormal: ATTRIB.normal, aModel: ATTRIB.model,
                          aColour: ATTRIB.colour, aHighlight: ATTRIB.highlight };
  /* One instance: 16 matrix, 4 colour and opacity, 1 highlight. */
  var INSTANCE_FLOATS = 21;

  /* ---- Looking designed (2026-09-18) --------------------------------------------
   *
   * damon, 2026-09-18: "looks designed" is a FORGE goal — the output should read as a
   * designed product, not boxes and sticks. This renderer's half of that is PRESENTATION
   * ONLY: light, materials, grounding, a camera. Nothing here changes a shape, a
   * placement or what is exported, and the defect check (internal/agent look.go,
   * sketch.go) stays closed to styling: looks are judged by a separate gate.
   *
   * Until this stage a part was lit by one directional light, Blinn-Phong and a rim,
   * written straight to the screen in whatever space the colours happened to be: no
   * reflections of anything, no tone mapping, no shadow, no occlusion, so every
   * material looked like matte plastic floating over a grid.
   *
   * Now, on every path:
   *   - image-based light from a small studio (two soft boxes, strip lights, a cove)
   *     generated here, pre-filtered for four roughnesses into one texture, plus its
   *     diffuse irradiance as nine spherical-harmonic coefficients (no fetch: the page's
   *     CSP allows only same-origin, and this needs no file at all);
   *   - a metal/roughness material read from the document's FINISH (MATERIALS below;
   *     the names are Go's closed set, unchanged);
   *   - colours taken from sRGB to linear, lit in linear, ACES-filmic tone mapped and
   *     written back as sRGB;
   *   - a studio backdrop (a cove: floor to wall to ceiling) in each theme, a soft
   *     contact shadow under the model, and a three-quarter hero camera.
   * WebGL2 only (it needs a depth texture and a multisampled target to resolve from):
   *   - screen-space ambient occlusion, applied before anything translucent is drawn.
   * WebGL1 draws everything else identically and has no occlusion.
   *
   * ‼️ The part program is still GLSL ES 1.00 on every path — "one pair of shaders, no
   * second place for the lighting to drift" (W1) holds. Only the occlusion passes, which
   * exist on WebGL2 alone, are GLSL ES 3.00. */

  /* ACES filmic, Narkowicz's fit (2015): x(ax+b) / (x(cx+d)+e). Written into the shader
   * from these numbers, and computed by acesFilm from the same ones, so a fence can read
   * the curve the GPU applies. */
  var ACES = { a: 2.51, b: 0.03, c: 2.43, d: 0.59, e: 0.14 };
  function acesFilm(x) {
    var v = (x * (ACES.a * x + ACES.b)) / (x * (ACES.c * x + ACES.d) + ACES.e);
    return Math.max(0, Math.min(1, v));
  }
  /* The exact sRGB transfer functions (IEC 61966-2-1), not a 2.2 power. */
  function linearToSrgb(c) {
    return c <= 0.0031308 ? 12.92 * c : 1.055 * Math.pow(c, 1 / 2.4) - 0.055;
  }
  function srgbToLinear(c) {
    return c <= 0.04045 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4);
  }
  function glslFloat(v) { var s = String(v); return /[.e]/.test(s) ? s : s + '.0'; }

  /* The environment's layout in its texture: ENV.levels rows of ENV.width × ENV.height,
   * row k pre-filtered for roughness k / (levels − 1), each an equirectangular map
   * (u = azimuth, v = polar angle from +Y), RGBM-encoded with ENV.range. Both sides
   * powers of two, so WebGL1 can repeat it round the seam. */
  var ENV = { width: 128, height: 64, levels: 4, range: 16 };
  /* Real spherical harmonics to order 2, as (constant, which product of x, y, z). */
  var SH_BASIS = [0.282095, 0.488603, 0.488603, 0.488603, 1.092548, 1.092548, 0.315392, 1.092548, 0.546274];
  function shBasis(n) {
    var x = n[0], y = n[1], z = n[2], k = SH_BASIS;
    return [k[0], k[1] * y, k[2] * z, k[3] * x, k[4] * x * y, k[5] * y * z,
            k[6] * (3 * z * z - 1), k[7] * x * z, k[8] * (x * x - y * y)];
  }

  var PART_VERT = [
    'attribute vec3 aPos;',
    'attribute vec3 aNormal;',
    'attribute mat4 aModel;',
    'attribute vec4 aColour;',
    'attribute float aHighlight;',
    'uniform mat4 uView;',
    'uniform mat4 uProj;',
    'varying vec3 vNormal;',
    'varying vec3 vWorld;',
    'varying vec4 vColour;',
    'varying float vHighlight;',
    /* A document's colour is sRGB (what a person means by #b3122e); light adds up in
     * linear. Converted here, per vertex, so the instance data stays the document's
     * colour — what the placement fences read back. */
    'vec3 toLinear(vec3 c) {',
    '  return mix(c / 12.92, pow((c + 0.055) / 1.055, vec3(2.4)), step(0.04045, c));',
    '}',
    'void main() {',
    '  vec4 world = aModel * vec4(aPos, 1.0);',
    '  vec3 c0 = aModel[0].xyz;',
    '  vec3 c1 = aModel[1].xyz;',
    '  vec3 c2 = aModel[2].xyz;',
    '  vec3 n = mat3(cross(c1, c2), cross(c2, c0), cross(c0, c1)) * aNormal;',
    '  vNormal = n * sign(dot(c0, cross(c1, c2)));',
    '  vWorld = world.xyz;',
    '  vColour = vec4(toLinear(aColour.rgb), aColour.a);',
    '  vHighlight = aHighlight;',
    '  gl_Position = uProj * uView * world;',
    '}'
  ].join('\n');
  var VERT = PART_VERT;

  var HIGHP = [
    '#ifdef GL_FRAGMENT_PRECISION_HIGH',
    'precision highp float;',
    '#else',
    'precision mediump float;',
    '#endif'
  ].join('\n');

  /* Section cutting is done in the fragment shader by discarding anything on the
   * far side of a plane. Cheap, exact, and it needs no CSG — and a section view
   * is one of the few things that makes an assembly legible at all. */
  var FRAG = [
    HIGHP,
    'varying vec3 vNormal;',
    'varying vec3 vWorld;',
    'varying vec4 vColour;',
    'varying float vHighlight;',
    'uniform vec3 uCamPos;',
    'uniform int uSectionAxis;',   // 0 none, 1 x, 2 y, 3 z
    'uniform float uSectionAt;',
    'uniform float uLight;',
    'uniform vec3 uMaterial;',     // metallic, roughness, clear coat
    'uniform sampler2D uEnv;',
    'uniform vec3 uSH[9];',
    'uniform vec3 uKeyDir;',
    'uniform vec3 uKeyColour;',
    'uniform float uExposure;',
    'const float PI = 3.14159265;',
    'const float LEVELS = ' + glslFloat(ENV.levels) + ';',
    'const float HALF_TEXEL = ' + glslFloat(0.5 / ENV.height) + ';',
    'vec3 irradiance(vec3 n) {',
    '  return uSH[0] * ' + glslFloat(SH_BASIS[0]) +
         ' + uSH[1] * (' + glslFloat(SH_BASIS[1]) + ' * n.y)' +
         ' + uSH[2] * (' + glslFloat(SH_BASIS[2]) + ' * n.z)' +
         ' + uSH[3] * (' + glslFloat(SH_BASIS[3]) + ' * n.x)' +
         ' + uSH[4] * (' + glslFloat(SH_BASIS[4]) + ' * n.x * n.y)' +
         ' + uSH[5] * (' + glslFloat(SH_BASIS[5]) + ' * n.y * n.z)' +
         ' + uSH[6] * (' + glslFloat(SH_BASIS[6]) + ' * (3.0 * n.z * n.z - 1.0))' +
         ' + uSH[7] * (' + glslFloat(SH_BASIS[7]) + ' * n.x * n.z)' +
         ' + uSH[8] * (' + glslFloat(SH_BASIS[8]) + ' * (n.x * n.x - n.y * n.y));',
    '}',
    'vec2 envUV(vec3 d, float level) {',
    '  float u = atan(d.z, d.x) * (0.5 / PI) + 0.5;',
    '  float v = clamp(acos(clamp(d.y, -1.0, 1.0)) / PI, HALF_TEXEL, 1.0 - HALF_TEXEL);',
    '  return vec2(u, (level + v) / LEVELS);',
    '}',
    'vec3 rgbm(vec4 c) { return c.rgb * c.a * ' + glslFloat(ENV.range) + '; }',
    'vec3 envAt(vec3 d, float rough) {',
    '  float lv = clamp(rough, 0.0, 1.0) * (LEVELS - 1.0);',
    '  float l0 = floor(lv);',
    '  float l1 = min(l0 + 1.0, LEVELS - 1.0);',
    '  return mix(rgbm(texture2D(uEnv, envUV(d, l0))), rgbm(texture2D(uEnv, envUV(d, l1))), lv - l0);',
    '}',
    /* Karis's analytic fit of the split-sum BRDF term: no lookup table to ship. */
    'vec2 envBRDF(float NoV, float r) {',
    '  vec4 r4 = r * vec4(-1.0, -0.0275, -0.572, 0.022) + vec4(1.0, 0.0425, 1.04, -0.04);',
    '  float a004 = min(r4.x * r4.x, exp2(-9.28 * NoV)) * r4.x + r4.y;',
    '  return vec2(-1.04, 1.04) * a004 + r4.zw;',
    '}',
    'vec3 aces(vec3 x) {',
    '  return clamp((x * (' + glslFloat(ACES.a) + ' * x + ' + glslFloat(ACES.b) + ')) / (x * (' +
         glslFloat(ACES.c) + ' * x + ' + glslFloat(ACES.d) + ') + ' + glslFloat(ACES.e) + '), 0.0, 1.0);',
    '}',
    'vec3 toSrgb(vec3 c) {',
    '  return mix(c * 12.92, 1.055 * pow(c, vec3(1.0 / 2.4)) - 0.055, step(0.0031308, c));',
    '}',
    'void main() {',
    '  if (uSectionAxis == 1 && vWorld.x > uSectionAt) discard;',
    '  if (uSectionAxis == 2 && vWorld.y > uSectionAt) discard;',
    '  if (uSectionAxis == 3 && vWorld.z > uSectionAt) discard;',
    '  vec3 N = normalize(vNormal);',
    '  vec3 V = normalize(uCamPos - vWorld);',
    '  float NoV = max(dot(N, V), 1e-4);',
    '  float metal = clamp(uMaterial.x, 0.0, 1.0);',
    '  float rough = clamp(uMaterial.y, 0.03, 1.0);',
    '  vec3 base = vColour.rgb;',
    '  vec3 F0 = mix(vec3(0.04), base, metal);',
    '  vec3 R = reflect(-V, N);',
    '  vec2 ab = envBRDF(NoV, rough);',
    '  vec3 spec = envAt(R, rough) * (F0 * ab.x + ab.y);',
    '  vec3 diff = base * (1.0 - metal) * max(irradiance(N), vec3(0.0));',
    // One key light on top of the environment, for a highlight with a direction.
    '  vec3 L = normalize(uKeyDir);',
    '  float NoL = max(dot(N, L), 0.0);',
    '  vec3 H = normalize(L + V);',
    '  float NoH = max(dot(N, H), 0.0);',
    '  float a2 = max(rough * rough * rough * rough, 1e-5);',
    '  float dd = NoH * NoH * (a2 - 1.0) + 1.0;',
    '  float D = min(a2 / (PI * dd * dd), 400.0);',
    '  float LoH = max(dot(L, H), 0.05);',
    '  vec3 Fk = F0 + (1.0 - F0) * pow(1.0 - LoH, 5.0);',
    '  spec += uKeyColour * NoL * D * Fk * (0.25 / (LoH * LoH));',
    '  diff += base * (1.0 - metal) * uKeyColour * NoL / PI;',
    '  vec3 col = diff + spec;',
    // A clear coat over the base: a second, sharp reflection (painted finishes).
    '  if (uMaterial.z > 0.0) {',
    '    vec2 cab = envBRDF(NoV, 0.05);',
    '    float Fc = (0.04 * cab.x + cab.y) * uMaterial.z;',
    '    col = col * (1.0 - Fc) + envAt(R, 0.05) * Fc;',
    '  }',
    '  col = toSrgb(aces(col * uExposure));',
    // The edge treatment is the one thing that cannot be shared between the two
    // grounds. Against near-black a silhouette is invisible until something lifts it,
    // so a faint cyan rim is added; against paper the mass is already darker than the
    // ground and the same edge is carved instead. One term, only its sign changes.
    '  float rim = pow(1.0 - NoV, 2.5);',
    '  col += vec3(0.31, 0.85, 0.91) * rim * 0.12 * (1.0 - uLight);',
    '  col *= 1.0 - rim * 0.12 * uLight;',
    // Selection. Bright cyan reads as "lit" on black and as "washed out" on
    // paper, so the light ground gets the same hue at a legible depth.
    '  col = mix(col, mix(vec3(0.31, 0.85, 0.91), vec3(0.10, 0.40, 0.50), uLight), vHighlight * 0.45);',
    '  gl_FragColor = vec4(col, vColour.a);',
    '}'
  ].join('\n');

  /* A full-screen triangle's vertex shader, shared by every screen pass written in
   * GLSL ES 1.00 (backdrop, blur). */
  var SCREEN_VERT = [
    'attribute vec3 aPos;',
    'varying vec2 vUV;',
    'varying vec2 vNdc;',
    'void main() { vNdc = aPos.xy; vUV = aPos.xy * 0.5 + 0.5; gl_Position = vec4(aPos.xy, 0.0, 1.0); }'
  ].join('\n');

  /* The studio backdrop: an infinite cove, floor curving up into wall and on into a
   * lighter ceiling, read from each pixel's view direction so it turns with the camera
   * like a room rather than sitting on the screen like wallpaper. Written in sRGB — it is
   * a colour on the page, not a lit surface. */
  var BACKDROP_FRAG = [
    HIGHP,
    'varying vec2 vUV;',
    'varying vec2 vNdc;',
    'uniform mat4 uInvViewProj;',
    'uniform vec3 uFloor;',
    'uniform vec3 uWall;',
    'uniform vec3 uTop;',
    'void main() {',
    '  vec4 p0 = uInvViewProj * vec4(vNdc, -1.0, 1.0);',
    '  vec4 p1 = uInvViewProj * vec4(vNdc, 1.0, 1.0);',
    '  vec3 d = normalize(p1.xyz / p1.w - p0.xyz / p0.w);',
    '  vec3 c = mix(uFloor, uWall, smoothstep(-0.35, 0.30, d.y));',
    '  c = mix(c, uTop, smoothstep(0.30, 1.0, d.y));',
    '  c *= 1.0 - 0.10 * dot(vNdc, vNdc);',
    '  gl_FragColor = vec4(c, 1.0);',
    '}'
  ].join('\n');

  /* The contact shadow, captured from BELOW: the depth test keeps each column's lowest
   * surface, and how close that surface is to the floor is how dark the column is. */
  var SHADOW_VERT = [
    'attribute vec3 aPos;',
    'attribute mat4 aModel;',
    'attribute vec3 aNormal;',
    'attribute vec4 aColour;',
    'attribute float aHighlight;',
    'uniform mat4 uView;',
    'uniform mat4 uProj;',
    'uniform float uFloor;',
    'uniform float uFalloff;',
    'varying float vOcc;',
    'void main() {',
    '  vec4 world = aModel * vec4(aPos, 1.0);',
    '  vOcc = exp(-max(world.y - uFloor, 0.0) / uFalloff) * step(0.5, aColour.a);',
    '  gl_Position = uProj * uView * world;',
    '}'
  ].join('\n');
  var SHADOW_FRAG = [
    'precision mediump float;',
    'varying float vOcc;',
    'void main() { gl_FragColor = vec4(vOcc, vOcc, vOcc, 1.0); }'
  ].join('\n');

  /* One direction of a separable Gaussian over the captured shadow. */
  var BLUR_FRAG = [
    'precision mediump float;',
    'varying vec2 vUV;',
    'varying vec2 vNdc;',
    'uniform sampler2D uTex;',
    'uniform vec2 uStep;',
    'void main() {',
    '  float s = texture2D(uTex, vUV).r * 0.2270270;',
    '  s += (texture2D(uTex, vUV + uStep * 1.3846154).r + texture2D(uTex, vUV - uStep * 1.3846154).r) * 0.3162162;',
    '  s += (texture2D(uTex, vUV + uStep * 3.2307692).r + texture2D(uTex, vUV - uStep * 3.2307692).r) * 0.0702703;',
    '  gl_FragColor = vec4(s, s, s, 1.0);',
    '}'
  ].join('\n');

  /* The floor the shadow is laid on: transparent except where the shadow is. */
  var GROUND_VERT = [
    'attribute vec3 aPos;',
    'uniform mat4 uView;',
    'uniform mat4 uProj;',
    'uniform vec4 uRect;',
    'varying vec2 vUV;',
    'void main() {',
    '  vUV = (aPos.xz - uRect.xy) / uRect.zw;',
    '  gl_Position = uProj * uView * vec4(aPos, 1.0);',
    '}'
  ].join('\n');
  var GROUND_FRAG = [
    'precision mediump float;',
    'varying vec2 vUV;',
    'uniform sampler2D uShadow;',
    'uniform vec3 uTint;',
    'uniform float uStrength;',
    'void main() {',
    '  vec2 e = min(vUV, 1.0 - vUV);',
    '  float fade = smoothstep(0.0, 0.12, min(e.x, e.y));',
    '  float s = texture2D(uShadow, vUV).r;',
    '  gl_FragColor = vec4(uTint, clamp(s * uStrength * fade, 0.0, 1.0));',
    '}'
  ].join('\n');

  /* Feature lines: a batch's sharp edges, drawn per instance like its triangles. */
  var EDGE_VERT = [
    'attribute vec3 aPos;',
    'attribute vec3 aNormal;',
    'attribute mat4 aModel;',
    'attribute vec4 aColour;',
    'attribute float aHighlight;',
    'uniform mat4 uView;',
    'uniform mat4 uProj;',
    'varying float vAlpha;',
    'void main() {',
    '  vAlpha = aColour.a;',
    '  gl_Position = uProj * uView * (aModel * vec4(aPos, 1.0));',
    '}'
  ].join('\n');
  var EDGE_FRAG = [
    'precision mediump float;',
    'varying float vAlpha;',
    'uniform vec4 uEdge;',
    'void main() { gl_FragColor = vec4(uEdge.rgb, uEdge.a * vAlpha); }'
  ].join('\n');

  /* ---- Screen-space ambient occlusion (WebGL2 only; GLSL ES 3.00) ---------------
   *
   * Read from the depth of what was drawn opaque, at half resolution: for each pixel,
   * AO_SAMPLES points in the hemisphere over its surface (the normal is rebuilt from the
   * neighbouring depths), and the share of them that something nearer the camera hides
   * darkens it. A range check stops a far background from occluding a near edge. It
   * costs a fixed number of full-screen passes whatever the model — 30,000 copies cost
   * it nothing more than one. */
  var AO_SAMPLES = 8;
  var AO_KERNEL = (function () {
    var out = [];
    for (var i = 0; i < AO_SAMPLES; i++) {
      /* A spiral over the hemisphere, denser near the surface. */
      var t = (i + 0.5) / AO_SAMPLES, phi = i * 2.399963;
      var z = 0.15 + 0.85 * Math.sqrt(1 - t), r = Math.sqrt(1 - z * z), s = 0.2 + 0.8 * t * t;
      out.push([r * Math.cos(phi) * s, r * Math.sin(phi) * s, z * s]);
    }
    return out;
  })();
  var AO_VERT = [
    '#version 300 es',
    'in vec3 aPos;',
    'out vec2 vUV;',
    'void main() { vUV = aPos.xy * 0.5 + 0.5; gl_Position = vec4(aPos.xy, 0.0, 1.0); }'
  ].join('\n');
  var AO_FRAG = [
    '#version 300 es',
    'precision highp float;',
    'in vec2 vUV;',
    'out vec4 outColour;',
    'uniform sampler2D uDepth;',
    'uniform mat4 uInvProj;',
    'uniform mat4 uProj;',
    'uniform vec2 uTexel;',
    'uniform float uRadius;',
    'uniform float uStrength;',
    'const vec3 K[' + AO_SAMPLES + '] = vec3[' + AO_SAMPLES + '](' + AO_KERNEL.map(function (k) {
      return 'vec3(' + k.map(function (v) { return v.toFixed(5); }).join(', ') + ')';
    }).join(', ') + ');',
    'vec3 viewAt(vec2 uv) {',
    '  float z = texture(uDepth, uv).r;',
    '  vec4 p = uInvProj * vec4(uv * 2.0 - 1.0, z * 2.0 - 1.0, 1.0);',
    '  return p.xyz / p.w;',
    '}',
    'void main() {',
    '  float d = texture(uDepth, vUV).r;',
    '  if (d >= 1.0) { outColour = vec4(1.0); return; }',
    '  vec3 P = viewAt(vUV);',
    '  vec3 r = viewAt(vUV + vec2(uTexel.x, 0.0)) - P, l = P - viewAt(vUV - vec2(uTexel.x, 0.0));',
    '  vec3 u = viewAt(vUV + vec2(0.0, uTexel.y)) - P, b = P - viewAt(vUV - vec2(0.0, uTexel.y));',
    '  vec3 N = normalize(cross(abs(r.z) < abs(l.z) ? r : l, abs(u.z) < abs(b.z) ? u : b));',
    '  if (dot(N, P) > 0.0) N = -N;',
    /* The kernel turned by one of sixteen angles laid out on a 4 × 4 tile, which the
     * composite's 4 × 4 box averages away exactly: no grain left behind. */
    '  vec2 cell = mod(floor(gl_FragCoord.xy), 4.0);',
    '  float a = 6.2831853 * (cell.x * 4.0 + cell.y + 0.5) / 16.0;',
    '  vec3 rv = vec3(cos(a), sin(a), 0.0);',
    '  vec3 T = normalize(rv - N * dot(rv, N));',
    '  if (length(rv - N * dot(rv, N)) < 1e-3) T = normalize(cross(N, vec3(0.0, 1.0, 0.0)));',
    '  vec3 B = cross(N, T);',
    '  float occ = 0.0;',
    '  for (int i = 0; i < ' + AO_SAMPLES + '; i++) {',
    '    vec3 S = P + (T * K[i].x + B * K[i].y + N * K[i].z) * uRadius;',
    '    vec4 c = uProj * vec4(S, 1.0);',
    '    vec2 suv = c.xy / c.w * 0.5 + 0.5;',
    '    if (suv.x < 0.0 || suv.y < 0.0 || suv.x > 1.0 || suv.y > 1.0) continue;',
    '    float sz = viewAt(suv).z;',
    '    float range = smoothstep(0.0, 1.0, uRadius / max(abs(P.z - sz), 1e-6));',
    '    occ += (sz >= S.z + uRadius * 0.03 ? 1.0 : 0.0) * range;',
    '  }',
    '  outColour = vec4(vec3(clamp(1.0 - occ / ' + glslFloat(AO_SAMPLES) + ' * uStrength, 0.0, 1.0)), 1.0);',
    '}'
  ].join('\n');
  /* Laid over the opaque picture by multiplying, through a 4×4 blur of the half-size
   * occlusion that hides its sampling pattern (read as four bilinear taps). */
  var AO_COMPOSITE_FRAG = [
    '#version 300 es',
    'precision highp float;',
    'in vec2 vUV;',
    'out vec4 outColour;',
    'uniform sampler2D uAO;',
    'uniform vec2 uTexel;',
    'void main() {',
    /* Four bilinear taps on texel corners: the 4 × 4 box for a quarter of the reads. */
    '  float s = texture(uAO, vUV + vec2(-1.0, -1.0) * uTexel).r + texture(uAO, vUV + vec2(1.0, -1.0) * uTexel).r +',
    '            texture(uAO, vUV + vec2(-1.0, 1.0) * uTexel).r + texture(uAO, vUV + vec2(1.0, 1.0) * uTexel).r;',
    '  outColour = vec4(vec3(s * 0.25), 1.0);',
    '}'
  ].join('\n');

  var LINE_VERT = [
    'attribute vec3 aPos;',
    'uniform mat4 uView;',
    'uniform mat4 uProj;',
    'varying vec3 vWorld;',
    'void main() { vWorld = aPos; gl_Position = uProj * uView * vec4(aPos, 1.0); }'
  ].join('\n');

  /* uFade (2026-09-18): the grid fades out between two distances from the model's centre
   * (x, z, start, end), so it reads as a floor under the model rather than a sheet of
   * lines aliasing to the horizon. An end of 0 draws the line whole — every overlay. */
  var LINE_FRAG = [
    HIGHP,
    'uniform vec3 uColor;',
    'uniform float uOpacity;',
    'uniform vec4 uFade;',
    'varying vec3 vWorld;',
    'void main() {',
    '  float f = uFade.w > 0.0 ? 1.0 - smoothstep(uFade.z, uFade.w, length(vWorld.xz - uFade.xy)) : 1.0;',
    '  gl_FragColor = vec4(uColor, uOpacity * f);',
    '}'
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

  function program(gl, vsrc, fsrc, attributes) {
    var p = gl.createProgram();
    gl.attachShader(p, compile(gl, gl.VERTEX_SHADER, vsrc));
    gl.attachShader(p, compile(gl, gl.FRAGMENT_SHADER, fsrc));
    for (var name in (attributes || {})) gl.bindAttribLocation(p, attributes[name], name);
    gl.linkProgram(p);
    if (!gl.getProgramParameter(p, gl.LINK_STATUS)) {
      throw new Error('link: ' + gl.getProgramInfoLog(p));
    }
    return p;
  }

  function hexToRGB(hex) {
    if (typeof hex !== 'string') return ground().partFallback;
    var h = hex.replace('#', '');
    if (h.length === 3) h = h[0]+h[0]+h[1]+h[1]+h[2]+h[2];
    var v = parseInt(h, 16);
    if (isNaN(v)) return ground().partFallback;
    return [((v >> 16) & 255) / 255, ((v >> 8) & 255) / 255, (v & 255) / 255];
  }

  /* ---- The studio the parts are lit by (2026-09-18) ------------------------------
   *
   * A photographic studio, written as a function of direction rather than shipped as a
   * picture: a large soft box overhead, a key strip to one side, a dimmer fill strip
   * opposite, a rim panel behind, and a cove that is dark at the floor and lifts towards
   * the ceiling. Linear radiance; a soft box is several times brighter than the walls,
   * which is what gives a metal part its long bright reflections. The edges are smooth
   * so the pre-filtered maps converge with few samples. */
  function smoothstep(a, b, x) {
    var t = (x - a) / (b - a);
    t = t < 0 ? 0 : t > 1 ? 1 : t;
    return t * t * (3 - 2 * t);
  }
  /* 1 within `half` of centre, falling to 0 over `soft` beyond it. */
  function band(x, centre, half, soft) {
    return 1 - smoothstep(half, half + soft, Math.abs(x - centre));
  }
  /* A light spread round the horizon: centred at azimuth `az`, `half` radians either
   * side, fading over `soft` — as a smoothstep on the cosine of the angle away from it,
   * so no inverse trigonometry per direction. */
  function strip(az, half, soft) {
    return { cx: Math.cos(az), sz: Math.sin(az), lo: Math.cos(half + soft), hi: Math.cos(half) };
  }
  var KEY_STRIP = strip(0.95, 0.10, 0.08), FILL_STRIP = strip(-2.25, 0.16, 0.12), RIM_PANEL = strip(2.55, 0.28, 0.15);

  function studioRadiance(d, out) {
    out = out || [0, 0, 0];
    var x = d[0], y = d[1], z = d[2];
    var up = smoothstep(-0.25, 0.6, y);
    var flat = Math.sqrt(x * x + z * z), light = 0;
    /* Overhead soft box: a rectangle seen through the ceiling. */
    if (y > 0.2) light += 4.0 * band(x / y, 0.0, 0.35, 0.18) * band(z / y, 0.10, 0.22, 0.15);
    /* Key strip, tall and narrow, front right; fill strip, back left; rim panel, behind. */
    var key = 0, fill = 0, rim = 0;
    if (flat > 1e-6) {
      var cx = x / flat, sz = z / flat;
      key = 4.2 * smoothstep(KEY_STRIP.lo, KEY_STRIP.hi, cx * KEY_STRIP.cx + sz * KEY_STRIP.sz) * band(y, 0.35, 0.30, 0.12);
      fill = 1.4 * smoothstep(FILL_STRIP.lo, FILL_STRIP.hi, cx * FILL_STRIP.cx + sz * FILL_STRIP.sz) * band(y, 0.30, 0.28, 0.15);
      rim = 2.2 * smoothstep(RIM_PANEL.lo, RIM_PANEL.hi, cx * RIM_PANEL.cx + sz * RIM_PANEL.sz) * band(y, 0.28, 0.16, 0.10);
    }
    light += key + fill + rim;
    /* The key is a touch warm, the fill a touch cool: enough to separate the planes of
     * a part, not enough to tint it. */
    out[0] = 0.15 + 0.22 * up + light + key * 0.06 - fill * 0.05;
    out[1] = 0.155 + 0.23 * up + light;
    out[2] = 0.165 + 0.26 * up + light - key * 0.05 + fill * 0.08;
    return out;
  }

  function envDirection(u, v) {
    var phi = (u - 0.5) * 2 * Math.PI, theta = v * Math.PI;
    return [Math.sin(theta) * Math.cos(phi), Math.cos(theta), Math.sin(theta) * Math.sin(phi)];
  }

  /* GGX half-vectors for `samples` Hammersley points at one roughness, in the frame of a
   * normal along +Z (Karis 2013) — computed once per roughness, turned per texel. */
  function radicalInverse(i) {
    var b = 0, f = 0.5;
    while (i) { if (i & 1) b += f; i >>>= 1; f *= 0.5; }
    return b;
  }
  function ggxSamples(rough, samples) {
    var a = rough * rough, out = [];
    for (var i = 0; i < samples; i++) {
      var e1 = (i + 0.5) / samples, e2 = radicalInverse(i), phi = 2 * Math.PI * e1;
      var cosT = Math.sqrt((1 - e2) / (1 + (a * a - 1) * e2)), sinT = Math.sqrt(1 - cosT * cosT);
      out.push(sinT * Math.cos(phi), sinT * Math.sin(phi), cosT);
    }
    return out;
  }
  /* The environment seen by a mirror-like view along n through a GGX lobe (N = V = R). */
  function prefiltered(n, table) {
    var up = Math.abs(n[1]) < 0.999 ? [0, 1, 0] : [1, 0, 0];
    var tx = normalize(cross(up, n)), ty = cross(n, tx), c = [0, 0, 0], l = [0, 0, 0];
    var r = 0, g = 0, bl = 0, w = 0;
    for (var i = 0; i < table.length; i += 3) {
      var hx = table[i], hy = table[i + 1], hz = table[i + 2];
      var h0 = tx[0] * hx + ty[0] * hy + n[0] * hz, h1 = tx[1] * hx + ty[1] * hy + n[1] * hz,
          h2 = tx[2] * hx + ty[2] * hy + n[2] * hz;
      var vh = 2 * hz;   /* dot(n, h) = hz, and l = 2(n·h)h − n */
      l[0] = vh * h0 - n[0]; l[1] = vh * h1 - n[1]; l[2] = vh * h2 - n[2];
      var nl = vh * hz - 1;
      if (nl <= 0) continue;
      studioRadiance(l, c);
      r += c[0] * nl; g += c[1] * nl; bl += c[2] * nl; w += nl;
    }
    return w > 0 ? [r / w, g / w, bl / w] : studioRadiance(n);
  }

  /* The diffuse light arriving at a surface facing n, over π (so an albedo of 1 reflects
   * exactly this), as nine SH coefficients per channel: k_l · L_lm, with k = 1, 2/3, 1/4
   * (Ramamoorthi & Hanrahan 2001). The shader sums them against SH_BASIS. */
  function environmentSH() {
    var coeff = new Float64Array(27), W = 64, H = 32;
    var band2 = [1, 2 / 3, 2 / 3, 2 / 3, 0.25, 0.25, 0.25, 0.25, 0.25];
    for (var j = 0; j < H; j++) {
      var v = (j + 0.5) / H, dOmega = (2 * Math.PI / W) * (Math.PI / H) * Math.sin(v * Math.PI);
      for (var i = 0; i < W; i++) {
        var d = envDirection((i + 0.5) / W, v), c = studioRadiance(d), y = shBasis(d);
        for (var k = 0; k < 9; k++) {
          coeff[k * 3] += c[0] * y[k] * dOmega;
          coeff[k * 3 + 1] += c[1] * y[k] * dOmega;
          coeff[k * 3 + 2] += c[2] * y[k] * dOmega;
        }
      }
    }
    var out = new Float32Array(27);
    for (var m = 0; m < 27; m++) out[m] = coeff[m] * band2[Math.floor(m / 3)];
    return out;
  }
  function shIrradiance(sh, n) {
    var y = shBasis(n), out = [0, 0, 0];
    for (var k = 0; k < 9; k++) {
      out[0] += sh[k * 3] * y[k]; out[1] += sh[k * 3 + 1] * y[k]; out[2] += sh[k * 3 + 2] * y[k];
    }
    return out;
  }

  /* The texture: ENV.levels rows, RGBM. Built once per page and shared by every Studio.
   * A rough row is filtered at a quarter or an eighth of the size and stretched: it has
   * nothing sharp to lose. */
  var ENVIRONMENT = null;
  function environment() {
    if (ENVIRONMENT) return ENVIRONMENT;
    var W = ENV.width, H = ENV.height, px = new Uint8Array(W * H * ENV.levels * 4);
    for (var level = 0; level < ENV.levels; level++) {
      var rough = level / (ENV.levels - 1), shrink = [1, 4, 8, 8][level] || 8;
      var w = W / shrink, h = H / shrink, small = [], table = level ? ggxSamples(rough, 64) : null;
      for (var j = 0; j < h; j++) {
        for (var i = 0; i < w; i++) {
          var dir = envDirection((i + 0.5) / w, (j + 0.5) / h);
          small.push(table ? prefiltered(dir, table) : studioRadiance(dir));
        }
      }
      for (var y = 0; y < H; y++) {
        for (var x = 0; x < W; x++) {
          /* Bilinear from the small map, wrapping round in u. */
          var fx = (x + 0.5) / shrink - 0.5, fy = Math.max(0, Math.min(h - 1, (y + 0.5) / shrink - 0.5));
          var x0 = Math.floor(fx), y0 = Math.floor(fy), ax = fx - x0, ay = fy - y0;
          var y1 = Math.min(h - 1, y0 + 1), c = [0, 0, 0];
          var xa = ((x0 % w) + w) % w, xb = (xa + 1) % w;
          for (var ch = 0; ch < 3; ch++) {
            var top = small[y0 * w + xa][ch] * (1 - ax) + small[y0 * w + xb][ch] * ax;
            var bottom = small[y1 * w + xa][ch] * (1 - ax) + small[y1 * w + xb][ch] * ax;
            c[ch] = top * (1 - ay) + bottom * ay;
          }
          var m = Math.max(c[0], c[1], c[2]) / ENV.range;
          m = Math.min(1, Math.max(1 / 255, Math.ceil(m * 255) / 255));
          var o = ((level * H + y) * W + x) * 4;
          for (ch = 0; ch < 3; ch++) px[o + ch] = Math.max(0, Math.min(255, Math.round(c[ch] / (m * ENV.range) * 255)));
          px[o + 3] = Math.round(m * 255);
        }
      }
    }
    ENVIRONMENT = { width: W, height: H * ENV.levels, pixels: px, sh: environmentSH() };
    return ENVIRONMENT;
  }

  /* What each finish looks like (PRD VIS-02), as a metal/roughness material.
   *
   * The renderer owns this and the server does not. Go holds the closed set of finish
   * NAMES (geometry.FinishNames), because it validates them and describes them to the
   * model; these numbers are how each one catches light, which nothing outside this file
   * has any use for. Splitting the table by who needs it is what stops the two halves
   * drifting — neither side holds the other's. ONE table: a finish is looked up here and
   * nowhere else, and TestRendererHasAMaterialForEveryFinishGoAccepts holds its names to
   * Go's and its roughness to the order the contract describes (glass sharpest, then
   * metal's hard tight highlight, painted's soft sheen, plastic's broad dull one, rubber
   * almost none).
   *
   *   metallic   0 a dielectric (its colour is diffuse), 1 a conductor (its colour tints
   *              the reflection and there is no diffuse)
   *   roughness  0 a mirror … 1 no highlight at all
   *   coat       a clear coat's strength: a second, sharp reflection over the base —
   *              what makes paint read as paint
   *
   * An unknown finish falls back rather than failing: the material's NAME is the claim,
   * and losing the look is a much smaller harm than losing the part. */
  var MATERIALS = {
    metal:      { metallic: 1.0, roughness: 0.28, coat: 0.0 },
    painted:    { metallic: 0.0, roughness: 0.45, coat: 0.8 },
    plastic:    { metallic: 0.0, roughness: 0.55, coat: 0.0 },
    glass:      { metallic: 0.0, roughness: 0.05, coat: 0.0 },
    rubber:     { metallic: 0.0, roughness: 0.90, coat: 0.0 },
    unfinished: { metallic: 0.0, roughness: 0.62, coat: 0.0 }
  };

  /* A batch's material as the three numbers the shader reads. */
  function shadingFor(material) {
    var f = material && material.finish;
    var m = (Object.prototype.hasOwnProperty.call(MATERIALS, f) && MATERIALS[f]) || MATERIALS.unfinished;
    return [m.metallic, m.roughness, m.coat];
  }

  /* ---- Where the camera may look from, and how deep it sees (A4, 2026-09-18) ----
   *
   * # What was wrong
   *
   * The near plane was 0.05 whatever the model, and the far plane at least 200. The
   * workbench draws a design in its own unit, so a car in millimetres framed whole put
   * the camera about 11,000 units away with a near plane of 0.05: a far/near ratio of
   * 1.75 million, and a 24-bit depth buffer that can no longer tell apart two surfaces
   * closer than ~140 mm at that distance. A rim 2.5 mm proud of its tyre, or a cut
   * tool's ghost beside the wheel it clears, fought the tyre for every pixel — the
   * streaks on the wheel faces. Nothing about the geometry was wrong.
   * docs/bugfix/2026-09-18-wheel-faces-streaked-because-the-near-plane-ignored-the-models-size.md
   *
   * # The rule
   *
   * Both planes follow the model: `reach` is how far from the model's centre anything
   * drawn can be (its box, an exploded view's spread, the dimension overlays), and the
   * near plane is as far out as the eye can be without cutting into that — never nearer
   * than 1% of the eye's distance to its target, so a camera zoomed right into a part
   * keeps a usable ratio too. The far plane takes the whole reach and the floor grid.
   * The wheel/pinch zoom is limited in the same terms (zoomLimits): it was 0.4 to 400
   * units, so the first wheel tick on a millimetre car jumped the camera from 11,000 to
   * 400 — inside the body.
   * Fences: TestRendererDepthResolvesTheModelAtAnyScale, TestRendererZoomsInTheModelsOwnUnits. */
  function clipPlanes(eye, target, bounds, explode) {
    var span = bounds && bounds.span > 0 ? bounds.span : 10;
    var centre = bounds ? bounds.centre : [0, 0, 0];
    var reach = span * (0.9 + (explode || 0) * 0.6) + span * 0.1;
    var toCentre = length3(sub(eye, centre)), toTarget = Math.max(length3(sub(eye, target)), span * 1e-4);
    var near = Math.max(toCentre - reach, toTarget * 0.01);
    /* The grid is centred on the origin and reaches 24 steps of about a tenth of the span. */
    var grid = length3(sub(eye, [0, centre[1], 0])) + span * 2.5 * Math.SQRT2;
    var far = Math.max(toCentre + reach, grid, near * 2);
    return { near: near, far: far };
  }
  function zoomLimits(span) {
    var s = span > 0 ? span : 10;
    return { min: s * 0.02, max: s * 100 };
  }

  /* ---- Curves as fine as the screen needs (A5, 2026-09-18) ----------------------
   *
   * A cylinder's 40 sides are right for the export and for a part a few hundred pixels
   * across; a wheel filling the viewport showed its facets along the silhouette. So a
   * curved primitive batch is drawn, frame by frame, with the fewest of 1, 2, 4 or 8
   * times its segments that keeps the chord's sag under DETAIL_TOLERANCE_PX pixels on its
   * largest copy on screen. The sag of a circle of r pixels cut into n chords is
   * r(1 − cos(π/n)), so the count needed is π / acos(1 − tol/r). Small copies never go
   * below the export's count — the level of detail (W2) already draws those simplified
   * or as a box — and each finer level is built once, when first needed, and kept. */
  var DETAIL_TOLERANCE_PX = 0.5;
  var DETAIL_MAX = 8;
  function detailFor(radiusPx, baseSegments) {
    if (!(radiusPx > DETAIL_TOLERANCE_PX) || !(baseSegments > 0)) return 1;
    var need = Math.PI / Math.acos(1 - DETAIL_TOLERANCE_PX / radiusPx), f = 1;
    while (f < DETAIL_MAX && baseSegments * f < need) f *= 2;
    return f;
  }
  function geometryAtDetail(shape, factor) {
    var saved = DETAIL;
    DETAIL = factor;
    try { return buildGeometry(shape); } finally { DETAIL = saved; }
  }
  /* The radius of the curve a primitive is drawn round, in its own frame, and how many
   * segments its export count gives that curve; null for a shape with no curve to refine. */
  function curveOf(shape) {
    var s = shape.size || {};
    switch (shape.shape) {
      case 'cylinder': return { radius: Math.max(num(s.radius, 0.5), num(s.radius_top, 0)), segments: TESSELLATION.radial };
      case 'cone': return { radius: num(s.radius, 0.5), segments: TESSELLATION.radial };
      case 'sphere': return { radius: num(s.radius, 0.5), segments: TESSELLATION.sphereRadial };
    }
    /* The outline shapes curve wherever their outline or path does: refined by their box. */
    return SMOOTHED_SHAPES[shape.shape] ? { radius: 0, segments: TESSELLATION.radial } : null;
  }

  /* ---- Feature lines (A6, 2026-09-18) ---------------------------------------------
   *
   * Every edge of a batch's surface where the two facets meeting there turn by more than
   * CREASE_DEGREES, and every open edge, as a line list in the batch's frame. Computed
   * once per batch the first time lines are asked for; off by default (setFeatureLines). */
  function featureEdges(geo, creaseDeg) {
    var pos = geo.positions, idx = geo.indices, tris = idx.length / 3;
    var limit = Math.cos((creaseDeg || CREASE_DEGREES) * Math.PI / 180);
    var bd = geometryBounds(pos), q = Math.max(bd.half[0], bd.half[1], bd.half[2], 1e-9) * 1e-6;
    var ids = {}, points = [], weld = new Int32Array(pos.length / 3), v;
    for (v = 0; v < weld.length; v++) {
      var key = Math.round(pos[v * 3] / q) + ',' + Math.round(pos[v * 3 + 1] / q) + ',' + Math.round(pos[v * 3 + 2] / q);
      if (ids[key] === undefined) { ids[key] = points.length / 3; points.push(pos[v * 3], pos[v * 3 + 1], pos[v * 3 + 2]); }
      weld[v] = ids[key];
    }
    var edges = {};
    for (var t = 0; t < tris; t++) {
      var a = weld[idx[t * 3]], b = weld[idx[t * 3 + 1]], c = weld[idx[t * 3 + 2]];
      if (a === b || b === c || a === c) continue;
      var ux = points[b * 3] - points[a * 3], uy = points[b * 3 + 1] - points[a * 3 + 1], uz = points[b * 3 + 2] - points[a * 3 + 2];
      var wx = points[c * 3] - points[a * 3], wy = points[c * 3 + 1] - points[a * 3 + 1], wz = points[c * 3 + 2] - points[a * 3 + 2];
      var n = normalize([uy * wz - uz * wy, uz * wx - ux * wz, ux * wy - uy * wx]);
      [[a, b], [b, c], [c, a]].forEach(function (e) {
        var k = e[0] < e[1] ? e[0] + ':' + e[1] : e[1] + ':' + e[0];
        (edges[k] || (edges[k] = [])).push(n);
      });
    }
    var lines = [];
    Object.keys(edges).forEach(function (k) {
      var ns = edges[k], sharp = ns.length === 1;
      for (var i = 1; i < ns.length && !sharp; i++) if (dot(ns[0], ns[i]) < limit) sharp = true;
      if (!sharp) return;
      var ends = k.split(':');
      lines.push(+ends[0], +ends[1]);
    });
    return { positions: points, indices: lines };
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

  /* ---- the two grounds ---------------------------------------------------
   *
   * Every colour the viewport chooses for itself, in one table rather than at
   * nine call sites. The viewport is not chrome — it is a picture with its own
   * light — so it cannot inherit the page's custom properties, and before this
   * table each of these was a literal buried in a draw call.
   *
   * The light ground is NOT the dark one lightened. A part at #b8bcc4 is a
   * pale grey that reads as a solid object on black and as a smudge on paper,
   * so the light ground gets its own mid grey. The same is true of the grid and
   * the dimension overlays: on paper they have to be darker than the ground,
   * not brighter. */
  var GROUNDS = {
    dark: {
      clear: [0.043, 0.059, 0.094],
      grid: [0.16, 0.22, 0.31],
      gridOpacity: 0.55,
      part: '#b8bcc4',
      partFallback: [0.72, 0.74, 0.78],
      /* The warning gold this interface already uses for "quoted, not checked".
       * Both mean the same thing to a reader: what you are looking at is not the
       * whole story. Matches --warn-ink in shell.css, in each theme. */
      removed: '#e6cd8f',
      stated: [0.55, 0.85, 0.95],
      derived: [0.52, 0.60, 0.72],
      /* The studio backdrop (2026-09-18): the cove's floor, its wall at the horizon and
       * the ceiling above, in sRGB; the contact shadow's colour and depth; exposure. */
      backdrop: { floor: [0.085, 0.095, 0.120], wall: [0.120, 0.135, 0.170], top: [0.165, 0.180, 0.225] },
      shadow: { tint: [0.0, 0.0, 0.0], strength: 1.0 },
      edge: [0.02, 0.03, 0.05, 0.55],
      exposure: 0.85
    },
    light: {
      clear: [0.949, 0.953, 0.965],
      grid: [0.72, 0.75, 0.80],
      gridOpacity: 0.90,
      part: '#8d929c',
      partFallback: [0.55, 0.57, 0.61],
      removed: '#8a6520',
      stated: [0.08, 0.38, 0.52],
      derived: [0.34, 0.38, 0.48],
      backdrop: { floor: [0.830, 0.838, 0.855], wall: [0.935, 0.940, 0.952], top: [0.975, 0.977, 0.982] },
      shadow: { tint: [0.10, 0.11, 0.14], strength: 0.85 },
      edge: [0.10, 0.11, 0.14, 0.60],
      exposure: 0.85
    }
  };

  /* Which ground is in force. Read at draw time rather than cached into each
   * Studio: this is a draw-on-demand renderer with no loop, so there is no
   * per-frame cost to reading it, and no second copy to leave stale. */
  var LIGHT = false;
  function ground() { return LIGHT ? GROUNDS.light : GROUNDS.dark; }

  function Studio(canvas, opts) {
    opts = opts || {};
    this.canvas = canvas;
    this.onSelect = opts.onSelect || function () {};
    this.onError = opts.onError || function () {};
    /* What the stage says about a design it browses rather than draws whole, and why a
     * row did not load; '' when there is nothing to say. Falls back to onError. */
    this.onNotice = opts.onNotice || null;

    /* WebGL2 first, WebGL1 with ANGLE_instanced_arrays second (Phase 6, stage W1;
     * decided 2026-09-15 to keep the WebGL1 path).
     *
     * Both draw a batch with ONE call however many copies it has. A WebGL1 context
     * without the extension still draws — each copy's attributes are set as constant
     * values and drawn with its own drawElements — so an old browser is slow rather
     * than blank. All three paths use the same shaders: GLSL ES 1.00 is valid in both
     * versions, and a second pair would be a second place for the lighting to drift.
     * TestRendererUploadsTheInstancesTheExporterPlaces draws through all three. */
    var attrs = { antialias: true, alpha: false };
    /* WebGL2 draws into its own multisampled target and resolves that to the canvas
     * (2026-09-18: the occlusion pass needs the depth of what was drawn, and a canvas's
     * depth cannot be read), so its canvas needs no multisampling of its own. */
    var gl = canvas.getContext('webgl2', { antialias: false, alpha: false }), webgl2 = !!gl, instancing = null;
    if (gl) {
      instancing = {
        draw: function (count, type, n, mode) { gl.drawElementsInstanced(mode || gl.TRIANGLES, count, type, 0, n); },
        divisor: function (loc, d) { gl.vertexAttribDivisor(loc, d); }
      };
    } else {
      gl = canvas.getContext('webgl', attrs) || canvas.getContext('experimental-webgl', attrs);
      var angle = gl && gl.getExtension ? gl.getExtension('ANGLE_instanced_arrays') : null;
      if (angle) {
        instancing = {
          draw: function (count, type, n, mode) { angle.drawElementsInstancedANGLE(mode || gl.TRIANGLES, count, type, 0, n); },
          divisor: function (loc, d) { angle.vertexAttribDivisorANGLE(loc, d); }
        };
      }
    }
    /* 32-bit indices, so a tessellated solid is not capped at 65,535 vertices.
     *
     * WebGL 1 indexes with an unsigned short unless this extension is present,
     * and a real tessellation passes that in one body: the primitives never
     * came close, so the ceiling was invisible until the kernel started drawing.
     * Where the extension is missing the part falls back to its primitive and
     * SAYS so — a mesh silently truncated to 65k vertices would draw a shape
     * nobody built. WebGL2 has them without asking. */
    if (gl && !webgl2 && gl.getExtension) gl.getExtension('OES_element_index_uint');
    if (!gl) {
      // Reported, never silently blank. A viewport that renders nothing with no
      // explanation is indistinguishable from a model that produced nothing.
      this.onError('This browser did not provide a WebGL context, so the 3D view cannot render. ' +
                   'The geometry is still available in the parts list and for export.');
      return;
    }
    this.gl = gl;
    this.webgl2 = webgl2;
    this.instancing = instancing;
    this.renderPath = webgl2 ? 'webgl2' : instancing ? 'webgl1-instanced' : 'webgl1-per-copy';
    this.prog = program(gl, VERT, FRAG, PART_ATTRIBUTES);
    this.lineProg = program(gl, LINE_VERT, LINE_FRAG, { aPos: ATTRIB.pos });
    /* The presentation's programs (2026-09-18): every one but the occlusion pair is GLSL
     * ES 1.00 and so the same on every path. */
    this.backdropProg = program(gl, SCREEN_VERT, BACKDROP_FRAG, { aPos: ATTRIB.pos });
    this.shadowProg = program(gl, SHADOW_VERT, SHADOW_FRAG, PART_ATTRIBUTES);
    this.blurProg = program(gl, SCREEN_VERT, BLUR_FRAG, { aPos: ATTRIB.pos });
    this.groundProg = program(gl, GROUND_VERT, GROUND_FRAG, { aPos: ATTRIB.pos });
    this.edgeProg = program(gl, EDGE_VERT, EDGE_FRAG, PART_ATTRIBUTES);
    if (webgl2) {
      this.aoProg = program(gl, AO_VERT, AO_FRAG, { aPos: ATTRIB.pos });
      this.aoCompositeProg = program(gl, AO_VERT, AO_COMPOSITE_FRAG, { aPos: ATTRIB.pos });
    }
    this._uniforms = {};
    this._screen = makeBuffer(gl, gl.ARRAY_BUFFER, new Float32Array([-1, -1, 0, 3, -1, 0, -1, 3, 0]));
    this._envTexture = this._makeEnvironment();
    /* What the presentation draws; each can be turned off (the measurement page does, to
     * time what each costs). Feature lines are off unless asked for. */
    this.contactShadow = true;
    this.ambientOcclusion = webgl2;
    this.featureLines = false;
    this.adaptiveDetail = true;
    this._shadowDirty = true;

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
    this.batches = [];
    this.isolated = null;
    /* Pixels of radius below which a copy is drawn as its box; 0 draws every copy whole
     * (and turns the simplified level off with it). */
    this.lod = LOD_PIXELS;
    /* Pixels of radius below which a copy is drawn as its batch's simplified mesh (W2). */
    this.lodSimple = LOD_SIMPLE_PIXELS;
    /* Cull through each batch's hierarchy of copies (W2); false tests every copy. */
    this.hierarchy = true;
    /* A design loaded a subtree at a time (loadLazy), or null. */
    this.lazy = null;
    this.stats = null;

    this.camera = { yaw: HERO.yaw, pitch: HERO.pitch, distance: 6, target: [0, 0, 0] };
    this._bindControls();
    this._resize();

    var self = this;
    window.addEventListener('resize', function () { self._resize(); self.draw(); });
    /* Hidden and shown again (2026-09-17). This is draw-on-demand, so nothing redraws a
     * viewport whose pane comes back unless something asks: a pane re-shown at a new
     * size without a window resize (a tab, a split, the desktop app's Browser pane)
     * kept the buffer it had while hidden. Re-sized and redrawn when the page is
     * visible again. Fence: TestRendererRedrawsWhenItsPaneIsShownAgain. */
    if (typeof document !== 'undefined' && document && document.addEventListener) {
      document.addEventListener('visibilitychange', function () {
        if (document.visibilityState === 'hidden') return;
        self._resize();
        self.draw();
      });
    }

    /* Registered so a theme change repaints this viewport.
     *
     * This is draw-on-demand: there is no animation loop that would pick the new
     * ground up on its next pass, so without this the model stays on the old
     * background until something else happens to redraw it — and on a settled
     * screen nothing does.
     *
     * The list is never pruned, and that is safe rather than sloppy: Studios are
     * created once and deliberately outlive the DOM they are shown in. The
     * compare view keeps a POOL for exactly that reason (see workbench.js), so
     * there is no destruction path to hook and nothing accumulates. */
    STUDIOS.push(this);

    /* Paint once, immediately, before there is anything to look at.
     *
     * This is a draw-on-demand renderer and nothing demanded a draw until
     * geometry arrived, so a freshly mounted viewport was never cleared at all.
     * A WebGL canvas created with `alpha: false` and never drawn composites as
     * opaque BLACK — which was indistinguishable from this viewport's intended
     * near-black ground, so for as long as there was only one ground the defect
     * could not be seen. On a light page it is a black rectangle in the middle
     * of the screen, sitting where the model will be.
     *
     * It also only reproduced on a settled window: any resize calls draw(), so
     * anyone who dragged a window edge — or ran a test that emulated a
     * viewport — saw the correct ground and could not reproduce it.
     *
     * docs/bugfix/2026-09-07-the-workbench-hid-half-itself-on-a-phone.md */
    this.draw();
  }

  var STUDIOS = [];
  if (window.ForgeTheme) {
    /* Called back immediately on registration, which is what sets LIGHT before
     * the first draw. */
    window.ForgeTheme.onChange(function (light) {
      LIGHT = !!light;
      for (var i = 0; i < STUDIOS.length; i++) { STUDIOS[i].draw(); }
    });
  }

  Studio.prototype._resize = function () {
    var dpr = Math.min(window.devicePixelRatio || 1, 2);
    /* ‼️ A canvas with no size is HIDDEN (a collapsed pane, display: none), not small.
     * Its buffer is kept rather than set to the 640×480 a canvas never sized starts
     * with, so a resize while hidden does not leave the viewport drawing at the wrong
     * size once shown; showing it sizes it again (visibilitychange, resize). */
    if ((!this.canvas.clientWidth || !this.canvas.clientHeight) && this._sized) return;
    this._sized = !!(this.canvas.clientWidth && this.canvas.clientHeight);
    var w = this.canvas.clientWidth || 640, h = this.canvas.clientHeight || 480;
    this.canvas.width = Math.floor(w * dpr);
    this.canvas.height = Math.floor(h * dpr);
    if (this.gl) this.gl.viewport(0, 0, this.canvas.width, this.canvas.height);
  };

  Studio.prototype._bindControls = function () {
    var self = this, dragging = false, panning = false, lastX = 0, lastY = 0, downX = 0, downY = 0;

    this.canvas.addEventListener('mousedown', function (e) {
      dragging = true;
      panning = e.button === 1 || e.shiftKey;
      lastX = e.clientX; lastY = e.clientY;
      downX = e.clientX; downY = e.clientY;
      e.preventDefault();
    });
    /* A press that did not move is a click, and a click picks (Phase 6, stage W2).
     * Five pixels of slack, because a hand on a trackpad does not hold still. */
    window.addEventListener('mouseup', function (e) {
      var still = dragging && !panning && Math.abs(e.clientX - downX) + Math.abs(e.clientY - downY) < 5;
      dragging = false;
      if (!still) return;
      var hit = self.pick(e.clientX, e.clientY);
      self.onSelect(hit ? hit.id : null, hit);
    });
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
      self.zoomBy(1 + e.deltaY * 0.0013);
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
          self.zoomBy(lastPinch / d);
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

  /* Zoom by a factor of the camera's distance, within zoomLimits of the model's size. */
  Studio.prototype.zoomBy = function (factor) {
    var lim = zoomLimits(this.bounds && this.bounds.span);
    this.camera.distance = Math.max(lim.min, Math.min(lim.max, this.camera.distance * factor));
  };

  /* The presentation's switches (2026-09-18). Each redraws. */
  Studio.prototype.setFeatureLines = function (on) { this.featureLines = !!on; this.draw(); };
  Studio.prototype.setAmbientOcclusion = function (on) { this.ambientOcclusion = !!on && this.webgl2; this.draw(); };
  Studio.prototype.setContactShadow = function (on) { this.contactShadow = !!on; this._shadowDirty = true; this.draw(); };

  /* A uniform's location in a program, looked up once. */
  Studio.prototype._u = function (prog, name) {
    var key = (prog.__forgeID || (prog.__forgeID = ++PROGRAM_IDS)) + ':' + name;
    var memo = this._uniforms;
    if (!(key in memo)) memo[key] = this.gl.getUniformLocation(prog, name);
    return memo[key];
  };
  var PROGRAM_IDS = 0;

  /* The studio's environment as a texture: built once per page, uploaded once per Studio. */
  Studio.prototype._makeEnvironment = function () {
    var gl = this.gl, env = environment(), tex = gl.createTexture();
    gl.bindTexture(gl.TEXTURE_2D, tex);
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, env.width, env.height, 0, gl.RGBA, gl.UNSIGNED_BYTE, env.pixels);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.REPEAT);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    gl.bindTexture(gl.TEXTURE_2D, null);
    return tex;
  };

  /* A colour texture of w × h, and a framebuffer drawing into it (with a depth buffer when
   * asked). RGBA8 with linear filtering: renderable and filterable on every WebGL1. */
  function colourTarget(gl, w, h, depth) {
    var tex = gl.createTexture();
    gl.bindTexture(gl.TEXTURE_2D, tex);
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.RGBA, w, h, 0, gl.RGBA, gl.UNSIGNED_BYTE, null);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.LINEAR);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    var fb = gl.createFramebuffer(), rb = null;
    gl.bindFramebuffer(gl.FRAMEBUFFER, fb);
    gl.framebufferTexture2D(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.TEXTURE_2D, tex, 0);
    if (depth) {
      rb = gl.createRenderbuffer();
      gl.bindRenderbuffer(gl.RENDERBUFFER, rb);
      gl.renderbufferStorage(gl.RENDERBUFFER, gl.DEPTH_COMPONENT16, w, h);
      gl.framebufferRenderbuffer(gl.FRAMEBUFFER, gl.DEPTH_ATTACHMENT, gl.RENDERBUFFER, rb);
    }
    var ok = gl.checkFramebufferStatus(gl.FRAMEBUFFER) === gl.FRAMEBUFFER_COMPLETE;
    gl.bindFramebuffer(gl.FRAMEBUFFER, null);
    gl.bindTexture(gl.TEXTURE_2D, null);
    return ok ? { fb: fb, tex: tex, rb: rb, w: w, h: h } : null;
  }

  /* One full-screen triangle through the bound program. */
  Studio.prototype._fullScreen = function () {
    var gl = this.gl;
    gl.bindBuffer(gl.ARRAY_BUFFER, this._screen);
    gl.enableVertexAttribArray(ATTRIB.pos);
    gl.vertexAttribPointer(ATTRIB.pos, 3, gl.FLOAT, false, 0, 0);
    gl.drawArrays(gl.TRIANGLES, 0, 3);
  };

  /* ---- The contact shadow (A3, 2026-09-18) ---------------------------------------
   *
   * The model seen from under the floor, straight up, into a small texture: each texel
   * is how close to the floor the lowest surface above it is. Blurred, and laid on the
   * floor under the model. It does not depend on the camera, so it is captured only when
   * what is drawn changes — a load, an explode, an assembly state, an isolation — and a
   * frame that only turns the camera costs one textured quad. */
  var SHADOW_SIZE = 256;
  Studio.prototype._shadowRect = function () {
    var bd = this.bounds;
    if (!bd) return null;
    var grow = bd.span * (0.35 + (this.explode || 0) * 0.6);
    return { x0: bd.min[0] - grow, z0: bd.min[2] - grow, x1: bd.max[0] + grow, z1: bd.max[2] + grow,
             floor: bd.min[1] - bd.span * 0.002, span: bd.span };
  };

  Studio.prototype._captureShadow = function () {
    var gl = this.gl, r = this._shadowRect();
    this._shadowDirty = false;
    if (!r || !this.batches || !this.batches.length) { this._shadow = null; return; }
    if (!this._shadowTargets) {
      var a = colourTarget(gl, SHADOW_SIZE, SHADOW_SIZE, true), b = colourTarget(gl, SHADOW_SIZE, SHADOW_SIZE, false);
      var c = colourTarget(gl, SHADOW_SIZE, SHADOW_SIZE, false);
      this._shadowTargets = a && b && c ? [a, b, c] : false;
    }
    if (!this._shadowTargets) { this._shadow = null; return; }
    var t = this._shadowTargets, cx = (r.x0 + r.x1) / 2, cz = (r.z0 + r.z1) / 2;
    var hx = (r.x1 - r.x0) / 2, hz = (r.z1 - r.z0) / 2, depth = r.span * 4;
    /* From below, looking up, with +Z up the texture: texture u is world x, v world z. */
    var eye = [cx, r.floor - r.span, cz];
    var view = lookAt(eye, [cx, r.floor, cz], [0, 0, 1]);
    var proj = orthographic(-hx, hx, -hz, hz, 0, depth);
    gl.bindFramebuffer(gl.FRAMEBUFFER, t[0].fb);
    gl.viewport(0, 0, SHADOW_SIZE, SHADOW_SIZE);
    gl.clearColor(0, 0, 0, 1);
    gl.clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT);
    gl.enable(gl.DEPTH_TEST);
    gl.disable(gl.CULL_FACE);
    gl.disable(gl.BLEND);
    gl.useProgram(this.shadowProg);
    gl.uniformMatrix4fv(this._u(this.shadowProg, 'uView'), false, view);
    gl.uniformMatrix4fv(this._u(this.shadowProg, 'uProj'), false, proj);
    gl.uniform1f(this._u(this.shadowProg, 'uFloor'), r.floor);
    gl.uniform1f(this._u(this.shadowProg, 'uFalloff'), r.span * 0.12);
    /* Through the same batches, culling and level of detail as a frame, at the texture's
     * own scale — its own frame number, so pick() still reads the last frame on screen. */
    var frame = { number: -1, view: view, proj: proj, eye: eye, planes: frustumPlanes(multiply(proj, view)),
                  pixels: SHADOW_SIZE / (2 * Math.max(hx, hz)) * r.span, pass: 'shadow' };
    var stats = { drawCalls: 0, instances: 0, culled: 0, proxied: 0, simplified: 0, hidden: 0, translucent: 0,
                  placeholders: 0, visited: 0, uploadedBytes: 0 };
    this._pass = 'shadow';
    for (var n = 0; n < this.batches.length; n++) {
      if (this.batches[n].placeholder) continue;
      this._drawBatch(this.batches[n], frame, stats, []);
    }
    this._pass = null;
    this._resetInstanceAttributes();   // before the blur's full-screen passes
    /* Two rounds of a separable blur, ping-ponging through the other two targets. */
    gl.disable(gl.DEPTH_TEST);
    gl.useProgram(this.blurProg);
    gl.uniform1i(this._u(this.blurProg, 'uTex'), 0);
    gl.activeTexture(gl.TEXTURE0);
    var src = t[0], steps = [[1, 0], [0, 1], [1, 0], [0, 1]], spread = 3.0 / SHADOW_SIZE;
    for (var s = 0; s < steps.length; s++) {
      var dst = s % 2 === 0 ? t[1] : t[2];
      gl.bindFramebuffer(gl.FRAMEBUFFER, dst.fb);
      gl.viewport(0, 0, SHADOW_SIZE, SHADOW_SIZE);
      gl.bindTexture(gl.TEXTURE_2D, src.tex);
      gl.uniform2f(this._u(this.blurProg, 'uStep'), steps[s][0] * spread, steps[s][1] * spread);
      this._fullScreen();
      src = dst;
    }
    gl.bindTexture(gl.TEXTURE_2D, null);
    gl.bindFramebuffer(gl.FRAMEBUFFER, null);
    gl.enable(gl.CULL_FACE);
    this._shadow = { tex: src.tex, rect: r, drawCalls: stats.drawCalls, instances: stats.instances };
  };

  function orthographic(l, r, b, t, n, f) {
    var out = new Float32Array(16);
    out[0] = 2 / (r - l); out[5] = 2 / (t - b); out[10] = -2 / (f - n);
    out[12] = -(r + l) / (r - l); out[13] = -(t + b) / (t - b); out[14] = -(f + n) / (f - n); out[15] = 1;
    return out;
  }

  /* The captured shadow, laid on the floor under the model. */
  Studio.prototype._drawGround = function (view, proj) {
    var gl = this.gl, sh = this._shadow;
    if (!sh) return;
    var r = sh.rect, g = ground().shadow;
    if (!this._groundBuffer || this._groundKey !== [r.x0, r.z0, r.x1, r.z1, r.floor].join()) {
      if (this._groundBuffer) gl.deleteBuffer(this._groundBuffer);
      this._groundBuffer = makeBuffer(gl, gl.ARRAY_BUFFER, new Float32Array([
        r.x0, r.floor, r.z0, r.x1, r.floor, r.z1, r.x1, r.floor, r.z0,
        r.x0, r.floor, r.z0, r.x0, r.floor, r.z1, r.x1, r.floor, r.z1]));
      this._groundKey = [r.x0, r.z0, r.x1, r.z1, r.floor].join();
    }
    var P = this.groundProg;
    gl.useProgram(P);
    gl.uniformMatrix4fv(this._u(P, 'uView'), false, view);
    gl.uniformMatrix4fv(this._u(P, 'uProj'), false, proj);
    gl.uniform4f(this._u(P, 'uRect'), r.x0, r.z0, r.x1 - r.x0, r.z1 - r.z0);
    gl.uniform3fv(this._u(P, 'uTint'), g.tint);
    gl.uniform1f(this._u(P, 'uStrength'), g.strength);
    gl.uniform1i(this._u(P, 'uShadow'), 0);
    gl.activeTexture(gl.TEXTURE0);
    gl.bindTexture(gl.TEXTURE_2D, sh.tex);
    gl.disable(gl.CULL_FACE);
    gl.depthMask(false);
    gl.bindBuffer(gl.ARRAY_BUFFER, this._groundBuffer);
    gl.enableVertexAttribArray(ATTRIB.pos);
    gl.vertexAttribPointer(ATTRIB.pos, 3, gl.FLOAT, false, 0, 0);
    gl.drawArrays(gl.TRIANGLES, 0, 6);
    gl.depthMask(true);
    gl.enable(gl.CULL_FACE);
    gl.bindTexture(gl.TEXTURE_2D, null);
  };

  /* The studio backdrop, first, behind everything and writing no depth. */
  Studio.prototype._drawBackdrop = function (view, proj) {
    var gl = this.gl, P = this.backdropProg, b = ground().backdrop;
    var inv = invert4(multiply(proj, view));
    if (!inv) return;
    gl.useProgram(P);
    gl.uniformMatrix4fv(this._u(P, 'uInvViewProj'), false, new Float32Array(inv));
    gl.uniform3fv(this._u(P, 'uFloor'), b.floor);
    gl.uniform3fv(this._u(P, 'uWall'), b.wall);
    gl.uniform3fv(this._u(P, 'uTop'), b.top);
    gl.disable(gl.DEPTH_TEST);
    gl.depthMask(false);
    this._fullScreen();
    gl.depthMask(true);
    gl.enable(gl.DEPTH_TEST);
  };

  /* ---- WebGL2's own target, and the occlusion read from it (A3, 2026-09-18) --------
   *
   * A multisampled colour and depth target the size of the canvas: the frame is drawn
   * there, its depth resolved into a texture the occlusion pass reads, the occlusion
   * multiplied back over the opaque picture, translucent parts and overlays drawn on top,
   * and the whole resolved to the canvas. null — and the frame drawn straight to the
   * canvas as on WebGL1 — when occlusion is off or a target cannot be made. */
  Studio.prototype._postTargets = function (w, h) {
    var gl = this.gl;
    if (!this.webgl2 || !this.ambientOcclusion) return null;
    var p = this._post;
    if (p === false) return null;
    if (p && p.w === w && p.h === h) return p;
    if (p) releasePost(gl, p);
    var samples = Math.min(4, gl.getParameter(gl.MAX_SAMPLES) || 0);
    p = { w: w, h: h, samples: samples };
    p.ms = gl.createFramebuffer();
    p.colour = gl.createRenderbuffer();
    gl.bindRenderbuffer(gl.RENDERBUFFER, p.colour);
    gl.renderbufferStorageMultisample(gl.RENDERBUFFER, samples, gl.RGBA8, w, h);
    p.depthRB = gl.createRenderbuffer();
    gl.bindRenderbuffer(gl.RENDERBUFFER, p.depthRB);
    gl.renderbufferStorageMultisample(gl.RENDERBUFFER, samples, gl.DEPTH_COMPONENT24, w, h);
    gl.bindFramebuffer(gl.FRAMEBUFFER, p.ms);
    gl.framebufferRenderbuffer(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.RENDERBUFFER, p.colour);
    gl.framebufferRenderbuffer(gl.FRAMEBUFFER, gl.DEPTH_ATTACHMENT, gl.RENDERBUFFER, p.depthRB);
    var ok = gl.checkFramebufferStatus(gl.FRAMEBUFFER) === gl.FRAMEBUFFER_COMPLETE;
    /* The resolved depth, as a texture. */
    p.depthTex = gl.createTexture();
    gl.bindTexture(gl.TEXTURE_2D, p.depthTex);
    gl.texImage2D(gl.TEXTURE_2D, 0, gl.DEPTH_COMPONENT24, w, h, 0, gl.DEPTH_COMPONENT, gl.UNSIGNED_INT, null);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_S, gl.CLAMP_TO_EDGE);
    gl.texParameteri(gl.TEXTURE_2D, gl.TEXTURE_WRAP_T, gl.CLAMP_TO_EDGE);
    p.resolve = gl.createFramebuffer();
    gl.bindFramebuffer(gl.FRAMEBUFFER, p.resolve);
    gl.framebufferTexture2D(gl.FRAMEBUFFER, gl.DEPTH_ATTACHMENT, gl.TEXTURE_2D, p.depthTex, 0);
    ok = ok && gl.checkFramebufferStatus(gl.FRAMEBUFFER) === gl.FRAMEBUFFER_COMPLETE;
    /* The multisampled colour is resolved into this, same format, and copied from here to
     * the canvas: a canvas without alpha is RGB8, and a multisample resolve must not
     * change format (a single-sample copy may). */
    p.flat = gl.createFramebuffer();
    p.flatRB = gl.createRenderbuffer();
    gl.bindRenderbuffer(gl.RENDERBUFFER, p.flatRB);
    gl.renderbufferStorage(gl.RENDERBUFFER, gl.RGBA8, w, h);
    gl.bindFramebuffer(gl.FRAMEBUFFER, p.flat);
    gl.framebufferRenderbuffer(gl.FRAMEBUFFER, gl.COLOR_ATTACHMENT0, gl.RENDERBUFFER, p.flatRB);
    ok = ok && gl.checkFramebufferStatus(gl.FRAMEBUFFER) === gl.FRAMEBUFFER_COMPLETE;
    p.ao = colourTarget(gl, Math.max(1, w >> 1), Math.max(1, h >> 1), false);
    gl.bindFramebuffer(gl.FRAMEBUFFER, null);
    gl.bindTexture(gl.TEXTURE_2D, null);
    if (!ok || !p.ao) { releasePost(gl, p); this._post = false; return null; }
    this._post = p;
    return p;
  };

  function releasePost(gl, p) {
    ['ms', 'resolve', 'flat'].forEach(function (k) { if (p[k]) gl.deleteFramebuffer(p[k]); });
    ['colour', 'depthRB', 'flatRB'].forEach(function (k) { if (p[k]) gl.deleteRenderbuffer(p[k]); });
    if (p.depthTex) gl.deleteTexture(p.depthTex);
    if (p.ao) { gl.deleteFramebuffer(p.ao.fb); gl.deleteTexture(p.ao.tex); }
  }

  /* The occlusion over what is opaque so far: resolve depth, compute at half size, and
   * multiply it into the multisampled picture. */
  Studio.prototype._applyOcclusion = function (p, proj, span) {
    var gl = this.gl;
    gl.bindFramebuffer(gl.READ_FRAMEBUFFER, p.ms);
    gl.bindFramebuffer(gl.DRAW_FRAMEBUFFER, p.resolve);
    gl.blitFramebuffer(0, 0, p.w, p.h, 0, 0, p.w, p.h, gl.DEPTH_BUFFER_BIT, gl.NEAREST);
    gl.bindFramebuffer(gl.FRAMEBUFFER, p.ao.fb);
    gl.viewport(0, 0, p.ao.w, p.ao.h);
    gl.disable(gl.DEPTH_TEST);
    gl.disable(gl.BLEND);
    var P = this.aoProg, inv = invert4(proj);
    gl.useProgram(P);
    gl.activeTexture(gl.TEXTURE0);
    gl.bindTexture(gl.TEXTURE_2D, p.depthTex);
    gl.uniform1i(this._u(P, 'uDepth'), 0);
    gl.uniformMatrix4fv(this._u(P, 'uInvProj'), false, new Float32Array(inv || IDENTITY));
    gl.uniformMatrix4fv(this._u(P, 'uProj'), false, proj);
    gl.uniform2f(this._u(P, 'uTexel'), 1 / p.w, 1 / p.h);
    gl.uniform1f(this._u(P, 'uRadius'), Math.min(span * 0.05, this.camera.distance * 0.08));
    gl.uniform1f(this._u(P, 'uStrength'), 1.1);
    this._fullScreen();
    /* Back over the picture: destination × occlusion. */
    gl.bindFramebuffer(gl.FRAMEBUFFER, p.ms);
    gl.viewport(0, 0, p.w, p.h);
    var C = this.aoCompositeProg;
    gl.useProgram(C);
    gl.bindTexture(gl.TEXTURE_2D, p.ao.tex);
    gl.uniform1i(this._u(C, 'uAO'), 0);
    gl.uniform2f(this._u(C, 'uTexel'), 1 / p.ao.w, 1 / p.ao.h);
    gl.enable(gl.BLEND);
    gl.blendFunc(gl.ZERO, gl.SRC_COLOR);
    gl.depthMask(false);
    this._fullScreen();
    gl.depthMask(true);
    gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA);
    gl.enable(gl.DEPTH_TEST);
    gl.bindTexture(gl.TEXTURE_2D, null);
  };

  /* load replaces the scene from a prototype spec.
   *
   * The spec is what the model produces. Everything the renderer needs is in it,
   * and everything it carries about provenance stays attached to the part so the
   * workbench can state where a shape came from rather than implying it is
   * authoritative. */
  Studio.prototype.load = function (spec, built) {
    if (!this.gl) return;
    var gl = this.gl, self = this;

    (this.batches || []).forEach(function (b) { releaseBatch(gl, b); });

    this.spec = spec || { parts: [] };
    this.approximations = [];
    this.isolated = null;
    this.lazy = null;
    this._searchIndex = null;
    this._treeIndex = undefined;   // a browsed design's search index: built on its first search
    /* Refused whole and said out loud (Phase 3, stage S0): partsToDraw draws nothing
     * for a design over the ceiling, and an empty stage must not read as an empty design. */
    var refusal = drawRefusal(this.spec);
    if (refusal) this.onError(refusal);

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
    /* Which parts are removed material, and every copy of every repeat, are
     * decided in ONE place — partsToDraw — so what the parity fence checks is
     * what is drawn. What is drawn WITH what is decided in one more — drawBatches
     * — and `built`, the mesh reply when the kernel answered, is how a definition's
     * triangles and its copies' matrices reach it (Phase 6, stage W1). */
    var wide = this.webgl2 || !!gl.getExtension('OES_element_index_uint');
    this._wide = wide;
    var plan = drawBatches(partsToDraw(this.spec), built || null, { wide: wide, toMM: unitToMM(this.spec.units) });
    this.approximations = plan.approximations;
    this.batches = plan.batches.map(function (b) { return self._upload(b, wide); });

    /* One entry per drawn part, for the readers that want parts rather than batches:
     * framing, search, and the count load returns. Held on the WRAPPER and never
     * written into spec: the document on screen has to stay the document stored. */
    this.parts = [];
    for (var n = 0; n < this.batches.length; n++) {
      var b = this.batches[n];
      for (var i = 0; i < b.n; i++) {
        this.parts.push({ id: b.ids[i], spec: b.specs[i], removed: !!b.removed[i],
                          repeatOf: b.repeatOf[i], fromKernel: b.fromKernel });
      }
    }

    this._frameAll();
    this.draw();
    return this.parts.length;
  };

  /* _upload puts one batch on the GPU: its triangles once, the box its level of
   * detail draws, and every instance's matrix plus what culling needs to decide
   * about it without touching the matrix again — the centre and radius of its
   * bounding sphere, and its half-extent along each axis for framing. */
  Studio.prototype._upload = function (plan, wide) {
    var gl = this.gl, geo = plan.geo, n = plan.instances.length, bd = plan.bounds;
    var vertexCount = geo.positions.length / 3;
    var wideHere = wide && vertexCount > 65535;
    var b = {
      key: plan.key, fromKernel: plan.fromKernel, definition: plan.definition, shading: plan.shading,
      geo: geo, bounds: bd, triangles: geo.indices.length / 3, n: n,
      buffers: {
        position: makeBuffer(gl, gl.ARRAY_BUFFER, new Float32Array(geo.positions)),
        normal:   makeBuffer(gl, gl.ARRAY_BUFFER, new Float32Array(geo.normals)),
        index:    makeBuffer(gl, gl.ELEMENT_ARRAY_BUFFER,
                    wideHere ? new Uint32Array(geo.indices) : new Uint16Array(geo.indices))
      },
      count: geo.indices.length,
      // Held per batch: one body may need 32-bit indices while its neighbour
      // does not, and drawing with the wrong width renders noise.
      indexType: wideHere ? gl.UNSIGNED_INT : gl.UNSIGNED_SHORT,
      proxy: proxyBuffers(gl, bd),
      ids: new Array(n), repeatOf: new Array(n), specs: new Array(n),
      removed: new Uint8Array(n), mirrored: new Uint8Array(n), opacity: new Float32Array(n),
      model: new Float32Array(n * 16), centre: new Float64Array(n * 3), radius: new Float32Array(n),
      extent: new Float32Array(n * 3), anchor: new Float64Array(n * 3),
      disp: new Float32Array(n * 3),
      /* The frame each copy was last on screen in, so nothing is cleared per copy per
       * frame; and this frame's drawn copies with their run, in the order they were found. */
      seen: new Uint32Array(n), drawn: new Int32Array(n), drawnGroup: new Uint8Array(n),
      placeholder: !!plan.placeholder, simple: null, tree: null,
      shape: plan.shape || null, curve: plan.shape ? plan.curve || null : null, details: {}, draw: null, detail: 1,
      scratch: new Float32Array(n * INSTANCE_FLOATS), instanceBuffer: gl.createBuffer()
    };
    var c = bd.centre, h = bd.half;
    for (var i = 0; i < n; i++) {
      var inst = plan.instances[i], m = inst.matrix, s = inst.spec, o = i * 3;
      b.ids[i] = inst.id;
      b.repeatOf[i] = inst.repeatOf;
      b.specs[i] = s;
      b.removed[i] = inst.removed ? 1 : 0;
      b.opacity[i] = num(s.opacity, 1);
      for (var k = 0; k < 16; k++) b.model[i * 16 + k] = m[k];
      b.centre[o]     = m[0] * c[0] + m[4] * c[1] + m[8] * c[2] + m[12];
      b.centre[o + 1] = m[1] * c[0] + m[5] * c[1] + m[9] * c[2] + m[13];
      b.centre[o + 2] = m[2] * c[0] + m[6] * c[1] + m[10] * c[2] + m[14];
      /* The sphere grows by the longest column: a scaled copy's sphere must still
       * hold it, or culling drops a part that is on screen. */
      var s0 = Math.sqrt(m[0] * m[0] + m[1] * m[1] + m[2] * m[2]);
      var s1 = Math.sqrt(m[4] * m[4] + m[5] * m[5] + m[6] * m[6]);
      var s2 = Math.sqrt(m[8] * m[8] + m[9] * m[9] + m[10] * m[10]);
      b.radius[i] = bd.radius * Math.max(s0, s1, s2);
      for (k = 0; k < 3; k++) {
        b.extent[o + k] = Math.abs(m[k]) * h[0] + Math.abs(m[4 + k]) * h[1] + Math.abs(m[8 + k]) * h[2];
      }
      var det = m[0] * (m[5] * m[10] - m[6] * m[9]) - m[4] * (m[1] * m[10] - m[2] * m[9]) +
                m[8] * (m[1] * m[6] - m[2] * m[5]);
      b.mirrored[i] = det < 0 ? 1 : 0;
      var at = s.position || [0, 0, 0];
      b.anchor[o] = num(at[0], 0); b.anchor[o + 1] = num(at[1], 0); b.anchor[o + 2] = num(at[2], 0);
    }
    b.tree = instanceTree(b);
    b.simple = simpleBuffers(gl, geo, bd);
    return b;
  };

  /* The box a far-away copy is drawn as: the batch's own bounds, as twelve triangles. */
  function proxyBuffers(gl, bd) {
    var box = boxGeometry(bd.half[0] * 2, bd.half[1] * 2, bd.half[2] * 2);
    for (var i = 0; i < box.positions.length; i += 3) {
      box.positions[i] += bd.centre[0];
      box.positions[i + 1] += bd.centre[1];
      box.positions[i + 2] += bd.centre[2];
    }
    return {
      position: makeBuffer(gl, gl.ARRAY_BUFFER, new Float32Array(box.positions)),
      normal:   makeBuffer(gl, gl.ARRAY_BUFFER, new Float32Array(box.normals)),
      index:    makeBuffer(gl, gl.ELEMENT_ARRAY_BUFFER, new Uint16Array(box.indices)),
      count:    box.indices.length
    };
  }

  function releaseBatch(gl, b) {
    gl.deleteBuffer(b.buffers.position);
    gl.deleteBuffer(b.buffers.normal);
    gl.deleteBuffer(b.buffers.index);
    gl.deleteBuffer(b.proxy.position);
    gl.deleteBuffer(b.proxy.normal);
    gl.deleteBuffer(b.proxy.index);
    if (b.simple) {
      gl.deleteBuffer(b.simple.position);
      gl.deleteBuffer(b.simple.normal);
      gl.deleteBuffer(b.simple.index);
    }
    Object.keys(b.details || {}).forEach(function (k) {
      var d = b.details[k];
      if (d.position) { gl.deleteBuffer(d.position); gl.deleteBuffer(d.normal); gl.deleteBuffer(d.index); }
    });
    if (b.edges && b.edges.position) { gl.deleteBuffer(b.edges.position); gl.deleteBuffer(b.edges.index); }
    gl.deleteBuffer(b.instanceBuffer);
  }

  /* modelMatrix is where one drawn part goes on screen, given the displacement the
   * view adds to it (an exploded view's gap, an assembly state's offset).
   *
   * A primitive is built in its own frame, so it is turned by its rotation and
   * moved to its position. A KERNEL mesh is not: the sidecar tessellates each solid
   * after placing it, so the vertices arrive already in assembly coordinates — a
   * 20 mm box at x=100 turned 30 degrees arrives spanning x 86.34 to 113.66. Until
   * 2026-09-13 it was placed and turned AGAIN, so every kernel-built part away from
   * the origin was drawn somewhere it is not: that box at about (187, 50), turned
   * 60 degrees, outside the frame. The contact sheet the vision check reads was
   * right all along; only what a person saw was wrong.
   * docs/bugfix/2026-09-13-kernel-built-parts-were-placed-twice.md
   * Fence: TestRendererDoesNotPlaceAKernelMeshTwice.
   *
   * Since Phase 6, stage W1 the matrix itself comes from placementMatrix, the one
   * copy of the rule the instanced draw reads too. */
  function modelMatrix(part, displacement) {
    var d = displacement || [0, 0, 0];
    /* A mirrored primitive reflects its own x first (Part.Mirrored) — the same
     * rule the kernel and the Go mesh follow. The draw flips its front face,
     * because a reflection turns every triangle inside out. */
    var m = part.fromKernel ? IDENTITY.slice() : placementMatrix(part.spec);
    m[12] += d[0]; m[13] += d[1]; m[14] += d[2];
    return new Float32Array(m);
  }

  function makeBuffer(gl, target, data) {
    var b = gl.createBuffer();
    gl.bindBuffer(target, b);
    gl.bufferData(target, data, gl.STATIC_DRAW);
    return b;
  }

  /* _frameAll fits the camera to the model. Without it, a model in millimetres
   * and a model in metres both render — one as a speck, one filling the screen —
   * and the viewer blames the geometry.
   *
   * From what is DRAWN (each instance's box in the world), not from the document's
   * positions and sizes: a kernel mesh, an extrusion whose outline sits away from its
   * origin and a copy placed by a matrix all have a position that is not their middle. */
  Studio.prototype._frameAll = function () {
    if (!this.parts.length) return;
    var min = [Infinity, Infinity, Infinity], max = [-Infinity, -Infinity, -Infinity];
    this.batches.forEach(function (b) {
      for (var i = 0; i < b.n; i++) {
        for (var k = 0; k < 3; k++) {
          var c = b.centre[i * 3 + k], e = b.extent[i * 3 + k];
          if (c - e < min[k]) min[k] = c - e;
          if (c + e > max[k]) max[k] = c + e;
        }
      }
    });
    var centre = [(min[0]+max[0])/2, (min[1]+max[1])/2, (min[2]+max[2])/2];
    var span = Math.max(max[0]-min[0], max[1]-min[1], max[2]-min[2], 0.5);
    this.camera.target = centre;
    /* Framed on the sphere round the box, so the whole model fits the 45° view from any
     * side with a little room (2026-09-18: span × 2.4 left a long car a third of the
     * frame). Never nearer than the old framing's half, for a model that is one long bar. */
    var half = length3([(max[0] - min[0]) / 2, (max[1] - min[1]) / 2, (max[2] - min[2]) / 2]);
    this.camera.distance = Math.max(half * 1.08 / Math.sin(FOV_DEGREES * Math.PI / 360), span * 1.2);
    /* A design browsed a row at a time is framed on what has arrived so far, and the rows
     * after it arrive round it: framed as loosely as before, so they arrive in view. */
    if (this.lazy && this.lazy.browse) this.camera.distance = span * 2.4;
    this.bounds = { min: min, max: max, span: span, centre: centre };
    this._shadowDirty = true;
  };

  /* The default view: a three-quarter hero angle, looking a little down across the model
   * from front-left — the angle a product is photographed from (A7, 2026-09-18). It was
   * yaw 0.7, pitch 0.5: nearly the "iso" preset, high enough to flatten a car's sides. */
  var HERO = { yaw: 0.62, pitch: 0.34 };

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

  Studio.prototype.setExplode = function (v) { this.explode = v; this._shadowDirty = true; this.draw(); };
  Studio.prototype.setTransparency = function (v) { this.transparency = v; this._shadowDirty = true; this.draw(); };
  Studio.prototype.setGrid = function (on) { this.showGrid = !!on; this.draw(); };
  /* select highlights what a person picked: a part as written ("spoke" lights every
   * copy), a path (its whole subtree, Phase 6 stage W2), or a matcher from
   * occurrenceMatcher, which decides for itself. null clears it. */
  Studio.prototype.select = function (id) { this.selected = id || null; this.draw(); };

  /* isolate draws only what a matcher accepts, or everything again for null. A
   * redraw, not a reload: isolating a subtree of a car must not rebuild the car. */
  Studio.prototype.isolate = function (matcher) {
    this.isolated = typeof matcher === 'function' ? matcher : null;
    this._shadowDirty = true;
    this.draw();
  };

  /* Every drawn part's id, which for a tree is its occurrence path. */
  Studio.prototype.occurrenceIds = function () {
    return this.parts.map(function (p) { return p.id; });
  };

  /* Drawn parts whose path or name contains the query, case-insensitively, up to
   * limit, and how many there were in all — a search over a car must say it found
   * 500 rivets rather than list them.
   *
   * # Through an index built once (Phase 6, stage W2)
   *
   * Until W2 every keystroke read every occurrence's path and name: 30,000 string
   * searches per character typed into the tree's search box. The index maps each
   * three-letter run of every lowercased path and name to the occurrences that contain
   * it, built on the first search after a load. A query of three letters or more reads
   * only the occurrences in its rarest run's list, and still checks each of them for
   * the whole query — so the answer is the scan's, in the scan's order, and
   * TestRendererFindsOccurrencesThroughAnIndexLikeTheScan holds it to scanOccurrences.
   *
   * ‼️ A query of one or two letters has no run of three and is answered by the scan.
   * It matches most of a car anyway, and the reply is fifty rows and a count. */
  Studio.prototype.findOccurrences = function (query, limit) {
    var q = String(query || '').trim().toLowerCase(), found = [], total = 0;
    if (!q) return { found: found, total: 0 };
    /* A browsed design lists every occurrence of its tree, drawn or not, and is not placed
     * in the browser to be searched: the tree is walked instead (searchTree). */
    if (this.lazy && this.lazy.browse) {
      /* Through the index treeSearchIndex writes on the first search, not a walk of the
       * whole tree per keystroke; the walk answers what the index cannot hold. */
      if (!/[\t\n]/.test(q)) {
        if (this._treeIndex === undefined) this._treeIndex = treeSearchIndex(this.spec);
        if (this._treeIndex) {
          this.searchStats = { rows: this._treeIndex.lines, examined: 0, indexed: true, tree: true };
          return searchTreeIndexed(this._treeIndex, q, limit || 50);
        }
      }
      this.searchStats = { rows: 0, examined: 0, indexed: false, tree: true };
      return searchTree(this.spec, q, limit || 50);
    }
    var index = this._searchIndex || (this._searchIndex = searchIndex(this.parts));
    var candidates = null, k;
    if (q.length >= SEARCH_RUN) {
      for (k = 0; k + SEARCH_RUN <= q.length; k++) {
        var list = index.runs.get(q.substr(k, SEARCH_RUN));
        if (!list) { candidates = []; break; }
        if (!candidates || list.length < candidates.length) candidates = list;
      }
    }
    var n = candidates ? candidates.length : this.parts.length;
    this.searchStats = { rows: this.parts.length, examined: n, indexed: !!candidates };
    for (k = 0; k < n; k++) {
      var i = candidates ? candidates[k] : k, text = index.text[i];
      if (text[0].indexOf(q) < 0 && text[1].indexOf(q) < 0) continue;
      total++;
      if (found.length < (limit || 50)) {
        var p = this.parts[i];
        found.push({ id: p.id, label: occurrenceLabel(p) });
      }
    }
    return { found: found, total: total };
  };

  /* The scan the index replaced, kept as the statement of what a search answers. */
  Studio.prototype.scanOccurrences = function (query, limit) {
    var q = String(query || '').trim().toLowerCase(), found = [], total = 0;
    if (!q) return { found: found, total: 0 };
    for (var i = 0; i < this.parts.length; i++) {
      var p = this.parts[i], name = String(p.spec.name || '');
      if (p.id.toLowerCase().indexOf(q) < 0 && name.toLowerCase().indexOf(q) < 0) continue;
      total++;
      if (found.length < (limit || 50)) found.push({ id: p.id, label: occurrenceLabel(p) });
    }
    return { found: found, total: total };
  };

  /* What to CALL one occurrence in a row that shows its path separately: its own name
   * (occurrenceName, set by the tree flattener), falling back to the whole display name
   * for a part the document lists at the top level, which has no path above it. */
  function occurrenceLabel(p) {
    return String(p.spec.occurrenceName || p.spec.name || '') || p.id;
  }

  var SEARCH_RUN = 3;

  /* Every three-letter run of each occurrence's lowercased path and name, to the
   * occurrences holding it in ascending order, each listed once. */
  function searchIndex(parts) {
    var runs = new Map(), text = new Array(parts.length);
    function add(s, i) {
      for (var k = 0; k + SEARCH_RUN <= s.length; k++) {
        var key = s.substr(k, SEARCH_RUN), list = runs.get(key);
        if (!list) runs.set(key, list = []);
        if (list[list.length - 1] !== i) list.push(i);
      }
    }
    for (var i = 0; i < parts.length; i++) {
      text[i] = [String(parts[i].id).toLowerCase(), String(parts[i].spec.name || '').toLowerCase()];
      add(text[i][0], i);
      add(text[i][1], i);
    }
    return { runs: runs, text: text };
  }

  /* ---- Loading a large tree a subtree at a time (Phase 6, stage W2) -------------
   *
   * # The problem this solves
   *
   * load() uploads every occurrence of a design before the first frame: a 30,000-part
   * car is 30,000 instances on the GPU, and a person who came to look at one wheel paid
   * for the seams too. The tree already LISTED lazily (W2, #88); the geometry did not.
   *
   * # The policy
   *
   * loadLazy lists every occurrence at once (search, the tree and a click all read the
   * list, and listing is cheap), but uploads none of them. Each top-level slot of the
   * tree is drawn as one translucent box around where its parts are, until geometry
   * arrives for it. Then:
   *
   *   - FIRST VIEW: every top-level row that places no more than the kernel builds at
   *     once (LAZY_OCCURRENCES) is requested, in the tree's order, until
   *     FIRST_VIEW_OCCURRENCES have been asked for. A car's corners, pack and seats
   *     arrive; its 26,000 riveted seams stay boxes.
   *   - OPENING, SELECTING or ISOLATING a row requests exactly that row's path
   *     (requestSubtree), unless a path already loaded or on its way covers it.
   *
   * request(path) is the host's: the workbench fetches
   * GET /v1/geometry/{id}/mesh?subtree=<path> and hands the reply to addSubtree, which
   * draws the row's parts from it exactly as load draws a whole reply (drawBatches).
   * Fence: TestRendererLoadsASubtreeWhenItIsAskedForAndDrawsWhatGoPlacesThere.
   *
   * ‼️ A design is loaded this way only when it places more than LAZY_OCCURRENCES
   * (loadsLazily) — past the kernel's ceiling, where the whole design has no built
   * surface to fetch anyway. Everything smaller is loaded whole, as before. */
  var LAZY_OCCURRENCES = 8192;           // geometry/limits.go maxBuiltParts
  var FIRST_VIEW_OCCURRENCES = 8192;
  var PLACEHOLDER_COLOUR = '#8a94a6', PLACEHOLDER_OPACITY = 0.18;

  /* ‼️ A design past MAX_VIEWPORT_PARTS loads this way too, BROWSED (see "Browsing a
   * design past the viewport's limit"): refused whole in Go's words, then drawn a subtree
   * at a time within the limit. Until 2026-09-17 it was refused and nothing more. */
  function loadsLazily(spec) {
    return !!(spec && spec.root) && occurrences(spec, MAX_VIEWPORT_PARTS) > LAZY_OCCURRENCES;
  }

  /* geometry Subtree.Refusal, in Go's words: one subtree that alone places more than the
   * viewport draws at once. Fence: TestRendererBrowsesADesignPastTheViewportLimit. */
  function subtreeRefusal(path, n) {
    return 'The subtree ' + path + ' places ' + n + ' parts, more than ' + MAX_VIEWPORT_PARTS +
      ', which is the most the FORGE viewport draws at once. Open a row beneath it instead; nothing was sent.';
  }

  /* What a first view asks for, by the policy above. */
  function firstViewPaths(spec) {
    spec = spec || {};
    var out = [], total = 0;
    function take(path, n) {
      if (n > 0 && n <= LAZY_OCCURRENCES && total + n <= FIRST_VIEW_OCCURRENCES) {
        out.push(path);
        total += n;
      }
    }
    (spec.parts || []).forEach(function (p) { if (p && String(p.id || '').trim()) take(String(p.id), repeatCount(p)); });
    treeChildren(spec, spec.root, '').forEach(function (row) {
      var one = occurrences({ definitions: spec.definitions, assemblies: spec.assemblies, root: row.ref }, MAX_VIEWPORT_PARTS);
      take(row.path, one * (row.slots ? row.slots.length : 1));
    });
    return out;
  }

  Studio.prototype.loadLazy = function (spec, request) {
    if (!this.gl) return 0;
    var gl = this.gl, self = this;
    (this.batches || []).forEach(function (b) { releaseBatch(gl, b); });
    this.batches = [];
    this.spec = spec || { parts: [] };
    this.approximations = [];
    this.isolated = null;
    this._searchIndex = null;
    this._treeIndex = undefined;   // a browsed design's search index: built on its first search
    var refusal = drawRefusal(this.spec);
    /* Past the limit the design is BROWSED: nothing is listed as parts up front (placing
     * a million in the browser is seconds and a gigabyte), each row is placed when it is
     * asked for, and the stage says so beside Go's refusal. */
    var browse = !!refusal;
    if (browse) {
      this._notice(refusal + ' Open, select or isolate a row of the tree to draw that part of it; up to ' +
        MAX_VIEWPORT_PARTS + ' parts are drawn at once.');
    }

    var drawn = browse ? [] : partsToDraw(this.spec);
    this.parts = drawn.map(function (d) {
      return { id: d.spec.id, spec: d.spec, removed: !!d.removed, repeatOf: d.repeatOf, fromKernel: false };
    });
    var wide = this.webgl2 || !!gl.getExtension('OES_element_index_uint');
    this._wide = wide;
    /* Where each top-level slot's parts are, from the boxes of the shapes that will be
     * drawn there: computed on the CPU and never uploaded. */
    var slots = {}, order = [];
    drawBatches(drawn, null, { wide: wide }).batches.forEach(function (p) {
      var h = p.bounds.half, c = p.bounds.centre;
      p.instances.forEach(function (inst) {
        var m = inst.matrix, key = String(inst.id).split(PATH_SEPARATOR)[0], s = slots[key];
        if (!s) {
          s = slots[key] = { min: [Infinity, Infinity, Infinity], max: [-Infinity, -Infinity, -Infinity] };
          order.push(key);
        }
        for (var k = 0; k < 3; k++) {
          var at = m[k] * c[0] + m[4 + k] * c[1] + m[8 + k] * c[2] + m[12 + k];
          var e = Math.abs(m[k]) * h[0] + Math.abs(m[4 + k]) * h[1] + Math.abs(m[8 + k]) * h[2];
          if (at - e < s.min[k]) s.min[k] = at - e;
          if (at + e > s.max[k]) s.max[k] = at + e;
        }
      });
    });
    this.lazy = { request: request || function () {}, drawn: drawn, wide: wide, loaded: {}, pending: {},
                  failed: {}, requested: [], matchers: {}, slots: slots, slotOrder: order, placeholder: null,
                  browse: browse, counts: {}, used: 0, recent: [], opened: {} };
    this._placeholders();
    this._frameAll();
    firstViewPaths(this.spec).forEach(function (path) { self.requestSubtree(path); });
    this.draw();
    return this.parts.length;
  };

  /* ---- Opening a row: its first rows, and the rest when asked (2026-09-17) ---------
   *
   * # The problem this solves
   *
   * Opening a row asked for the whole row: opening one car of the fleet fetched and
   * placed all 30,023 of its parts, and in a browsed design put back up to a third of
   * what the person had drawn to make room for them.
   *
   * # The decision, and why
   *
   * Opening a row is how a person walks DOWN the tree — car, then seam, then rivet — as
   * much as how they look at a thing. So opening draws what the opened row lists first,
   * by the first view's own policy (firstViewPaths) applied beneath the row: its child
   * rows in the order the tree lists them, each taken whole when it fits and a pattern
   * taken a copy at a time when it does not, until OPEN_OCCURRENCES parts or
   * OPEN_REQUESTS requests. Nearest first, because those are the rows on screen right
   * under the one opened; bounded by count, because what a person meant by "open" is
   * "show me what is in here", which is the first few thousand parts and not the
   * thirty thousandth rivet. A row it did not finish says so and offers the rest
   * ("Draw all", the row's own path, which replaces what the open drew); Select and
   * Isolate still draw the whole row at once, since they mean "this thing".
   *
   * The alternative — stream every child in turn until the row is whole — was rejected:
   * it costs the same parts and the same room in the end, and a person walking through
   * a car to one seam would pay for the car anyway.
   * Fence: TestRendererOpensARowItsFirstRowsFirstAndTheRestWhenAsked. */
  var OPEN_OCCURRENCES = FIRST_VIEW_OCCURRENCES;
  var OPEN_REQUESTS = 24;

  /* The tree row a path names, found the way treeRows lists it: { row, slot } where slot
   * is the path when it names one copy of a patterned row. Null for no such row. */
  function treeRowAt(spec, path) {
    var segs = String(path || '').split(PATH_SEPARATOR), ref = spec.root, parent = '';
    for (var i = 0; i < segs.length; i++) {
      var here = (parent ? parent + PATH_SEPARATOR : '') + segs[i], hit = null;
      treeChildren(spec, ref, parent).forEach(function (row) {
        if (hit) return;
        if (row.path === here) hit = { row: row, slot: null };
        else if (row.slots && row.slots.indexOf(here) >= 0) hit = { row: row, slot: here };
      });
      if (!hit) return null;
      if (i === segs.length - 1) return hit;
      if (!hit.row.assembly) return null;
      ref = hit.row.ref;
      parent = here;
    }
    return null;
  }

  /* What opening `path` asks for: { paths, drawn, total }. A row within OPEN_OCCURRENCES
   * is asked for whole, as before. */
  function openPaths(spec, path) {
    spec = spec || {};
    path = String(path || '');
    var total = occurrencesUnder(spec, path);
    if (total <= OPEN_OCCURRENCES) return { paths: total ? [path] : [], drawn: total, total: total };
    var at = treeRowAt(spec, path), rows = [];
    if (at && at.row.slots && !at.slot) {
      /* A patterned row opened lists its copies. */
      rows = at.row.slots.map(function (slot) { return { path: slot, slots: null }; });
    } else if (at && at.row.assembly) {
      rows = treeChildren(spec, at.row.ref, path);
    }
    var paths = [], drawn = 0;
    function take(p) {
      if (paths.length >= OPEN_REQUESTS) return false;
      var n = occurrencesUnder(spec, p);
      if (!n || drawn + n > OPEN_OCCURRENCES) return false;
      paths.push(p);
      drawn += n;
      return true;
    }
    /* Every row that fits whole first, so each kind of thing the row holds is on screen;
     * then what is left of the budget on the copies of the patterns that did not fit. */
    var whole = rows.map(function (row) { return take(row.path); });
    rows.forEach(function (row, i) {
      if (whole[i] || !row.slots) return;
      for (var s = 0; s < row.slots.length && take(row.slots[s]); s++) { /* one copy at a time */ }
    });
    return { paths: paths, drawn: drawn, total: total };
  }

  /* Open a row: ask for what openPaths says, and remember a row left unfinished so the
   * tree can offer the rest. Returns the plan, or null for a design loaded whole. */
  Studio.prototype.openRow = function (path) {
    var lazy = this.lazy, self = this;
    path = String(path || '');
    if (!lazy || !path || this._covered(path)) return null;
    var plan = openPaths(this.spec, path);
    if (!plan.paths.length) {
      /* Nothing beneath it fits (every child row alone is past the budget): the row is
       * asked for whole, as before, which draws it or refuses it by name with its count. */
      this.requestSubtree(path);
      return plan;
    }
    plan.paths.forEach(function (p) { self.requestSubtree(p); });
    if (plan.drawn < plan.total) lazy.opened[path] = { drawn: plan.drawn, total: plan.total };
    else delete lazy.opened[path];
    return plan;
  };

  /* { drawn, total } for an opened row not yet drawn whole, else null. */
  Studio.prototype.openedPartly = function (path) {
    var lazy = this.lazy, o = lazy && lazy.opened[path];
    if (!o) return null;
    if (this._covered(path)) { delete lazy.opened[path]; return null; }
    return o;
  };

  Studio.prototype._matcher = function (key) {
    var m = this.lazy.matchers;
    return m[key] || (m[key] = occurrenceMatcher(key));
  };

  /* Whether a path is loaded — or, unless loadedOnly, on its way — itself or under a
   * path that is. */
  Studio.prototype._covered = function (path, loadedOnly) {
    var lazy = this.lazy, sets = loadedOnly ? [lazy.loaded] : [lazy.loaded, lazy.pending];
    for (var s = 0; s < sets.length; s++) {
      for (var key in sets[s]) {
        if (key === path || this._matcher(key)(path, '')) return true;
      }
    }
    return false;
  };

  /* Ask the host for one path's geometry, unless it is already covered. True when
   * asked. A no-op for a design loaded whole. */
  Studio.prototype.requestSubtree = function (path) {
    var lazy = this.lazy;
    path = String(path || '');
    if (!lazy || !path) return false;
    if (this._covered(path)) return false;
    if (lazy.browse) {
      /* Counted before anything is asked for or placed: a row that alone is past the
       * limit is refused by name with its count, and room is made for one that is not
       * by putting back the rows drawn longest ago. */
      var n = occurrencesUnder(this.spec, path);
      if (!n) return false;
      if (n > MAX_VIEWPORT_PARTS) {
        lazy.failed[path] = subtreeRefusal(path, n);
        this._notice(lazy.failed[path]);
        return false;
      }
      if (!this._makeRoom(path, n)) {
        lazy.failed[path] = 'Drawing ' + path + ' (' + n + ' parts) as well as the rows still on their way would ' +
          'draw more than ' + MAX_VIEWPORT_PARTS + ' parts at once, the most the FORGE viewport draws; ask again ' +
          'when they have arrived. Nothing was sent.';
        this._notice(lazy.failed[path]);
        return false;
      }
      lazy.counts[path] = n;
      lazy.used += n;
    }
    lazy.pending[path] = true;
    delete lazy.failed[path];
    lazy.requested.push(path);
    lazy.request(path);
    return true;
  };

  /* Draw one requested path's parts from its mesh reply (or as primitives, for null),
   * replacing any path beneath it that arrived earlier. Returns how many parts it drew. */
  Studio.prototype.addSubtree = function (path, reply) {
    var lazy = this.lazy, gl = this.gl, self = this;
    if (!lazy || !lazy.pending[path]) return 0;
    delete lazy.pending[path];
    if (this._covered(path, true)) {
      /* Arrived under a row drawn meanwhile (an open's first rows, then "Draw all"): not
       * drawn, so the room it was counted for is given back. */
      if (lazy.browse) {
        lazy.used -= lazy.counts[path] || 0;
        delete lazy.counts[path];
      }
      return 0;
    }
    var under = this._matcher(path);
    Object.keys(lazy.loaded).forEach(function (key) {
      if (!under(key, '')) return;
      if (lazy.browse) { self._putBack(key); return; }
      var gone = lazy.loaded[key];
      gone.forEach(function (b) { releaseBatch(gl, b); });
      self.batches = self.batches.filter(function (b) { return gone.indexOf(b) < 0; });
      delete lazy.loaded[key];
    });
    var drawn = lazy.browse ? partsUnder(this.spec, path)
      : lazy.drawn.filter(function (d) { return under(d.spec.id, d.repeatOf); });
    var framed = !lazy.browse || this.parts.length > 0;
    if (lazy.browse) {
      lazy.used += drawn.length - (lazy.counts[path] || 0);
      lazy.counts[path] = drawn.length;
      lazy.recent.push(path);
      this.parts = this.parts.concat(drawn.map(function (d) {
        return { id: d.spec.id, spec: d.spec, removed: !!d.removed, repeatOf: d.repeatOf, fromKernel: false };
      }));
      this._searchIndex = null;
    }
    var plan = drawBatches(drawn, reply || null, { wide: lazy.wide, toMM: unitToMM(this.spec.units) });
    var batches = plan.batches.map(function (p) {
      p.key = path + '|' + p.key;
      return self._upload(p, lazy.wide);
    });
    lazy.loaded[path] = batches;
    this.batches = this.batches.concat(batches);
    this.approximations = this.approximations.concat(plan.approximations);
    this._placeholders();
    if (lazy.browse) {
      // A browsed design has no boxes to frame before its first row arrives.
      if (!framed) this._frameAll();
      this._notice('');
    }
    this._shadowDirty = true;
    this.draw();
    return drawn.length;
  };

  /* Say something about the stage, or clear it with ''. */
  Studio.prototype._notice = function (msg) {
    if (this.onNotice) this.onNotice(msg || '');
    else if (msg) this.onError(msg);
  };

  /* A browsed row put back: its batches released and its parts no longer listed. */
  Studio.prototype._putBack = function (key) {
    var lazy = this.lazy, gl = this.gl, gone = lazy.loaded[key] || [], under = this._matcher(key);
    gone.forEach(function (b) { releaseBatch(gl, b); });
    this.batches = this.batches.filter(function (b) { return gone.indexOf(b) < 0; });
    delete lazy.loaded[key];
    lazy.used -= lazy.counts[key] || 0;
    delete lazy.counts[key];
    lazy.recent = lazy.recent.filter(function (k) { return k !== key; });
    this.parts = this.parts.filter(function (p) { return !under(p.id, p.repeatOf); });
    this._searchIndex = null;
  };

  /* Room for n more parts within MAX_VIEWPORT_PARTS: rows under `path` will be replaced
   * by it, and the rows drawn longest ago are put back until it fits. False when even
   * that is not enough, because rows still on their way hold the room. */
  Studio.prototype._makeRoom = function (path, n) {
    var lazy = this.lazy, under = this._matcher(path), self = this;
    var replaced = 0;
    /* Loaded or still on its way: either is replaced by `path` (addSubtree). */
    Object.keys(lazy.counts).forEach(function (key) { if (under(key, '')) replaced += lazy.counts[key] || 0; });
    var others = lazy.recent.filter(function (key) { return !under(key, ''); });
    while (lazy.used - replaced + n > MAX_VIEWPORT_PARTS && others.length) self._putBack(others.shift());
    return lazy.used - replaced + n <= MAX_VIEWPORT_PARTS;
  };

  /* A request the host could not answer: the path stays a box, and may be asked again. */
  Studio.prototype.failSubtree = function (path, why) {
    if (!this.lazy || !this.lazy.pending[path]) return;
    delete this.lazy.pending[path];
    if (this.lazy.browse) {
      this.lazy.used -= this.lazy.counts[path] || 0;
      delete this.lazy.counts[path];
    }
    this.lazy.failed[path] = why || true;
  };

  Studio.prototype.lazyState = function () {
    var lazy = this.lazy;
    if (!lazy) return null;
    return { loaded: Object.keys(lazy.loaded), pending: Object.keys(lazy.pending),
             failed: Object.keys(lazy.failed), requested: lazy.requested.slice(),
             placeholders: lazy.placeholder ? lazy.placeholder.n : 0,
             browse: !!lazy.browse, drawn: lazy.browse ? lazy.used : this.parts.length,
             refusals: Object.keys(lazy.failed).map(function (k) { return lazy.failed[k]; })
               .filter(function (w) { return typeof w === 'string'; }) };
  };

  /* One box per top-level slot that no loaded path covers yet. */
  Studio.prototype._placeholders = function () {
    var lazy = this.lazy, gl = this.gl, self = this, old = lazy.placeholder;
    if (old) {
      releaseBatch(gl, old);
      this.batches = this.batches.filter(function (b) { return b !== old; });
      lazy.placeholder = null;
    }
    var instances = [];
    lazy.slotOrder.forEach(function (key) {
      if (self._covered(key, true)) return;
      var s = lazy.slots[key], size = [], centre = [];
      for (var k = 0; k < 3; k++) {
        size[k] = Math.max(s.max[k] - s.min[k], 1e-3);
        centre[k] = (s.max[k] + s.min[k]) / 2;
      }
      instances.push({ id: key, repeatOf: '', removed: false,
        spec: { id: key, name: key, color: PLACEHOLDER_COLOUR, opacity: PLACEHOLDER_OPACITY, position: centre },
        matrix: [size[0], 0, 0, 0, 0, size[1], 0, 0, 0, 0, size[2], 0, centre[0], centre[1], centre[2], 1] });
    });
    if (!instances.length) return;
    var geo = boxGeometry(1, 1, 1);
    lazy.placeholder = this._upload({ key: 'placeholder', fromKernel: false, definition: -1, shading: shadingFor(null),
      geo: geo, bounds: geometryBounds(geo.positions), instances: instances, placeholder: true }, lazy.wide);
    this.batches.unshift(lazy.placeholder);
  };

  Studio.prototype.setSection = function (axis, t) {
    var map = { none: 0, x: 1, y: 2, z: 3 };
    this.section.axis = map[axis] || 0;
    if (this.bounds) {
      var i = Math.max(0, this.section.axis - 1);
      this.section.at = this.bounds.min[i] + (this.bounds.max[i] - this.bounds.min[i]) * t;
    }
    this.draw();
  };

  Studio.prototype.resetView = function () { this.camera.yaw = HERO.yaw; this.camera.pitch = HERO.pitch; this._frameAll(); this.draw(); };
  Studio.prototype.viewFrom = function (which) {
    var v = { front: [0, 0], top: [0, 1.5], side: [Math.PI/2, 0], iso: [0.7, 0.5], hero: [HERO.yaw, HERO.pitch] }[which] || [HERO.yaw, HERO.pitch];
    this.camera.yaw = v[0]; this.camera.pitch = v[1];
    this.draw();
  };

  /* ---- Culling and level of detail (Phase 6, stage W2) --------------------------
   *
   * A copy whose bounding sphere is wholly outside the view is not sent at all, and a
   * copy that would cover fewer than `lod` pixels of radius is drawn as its batch's
   * box — twelve triangles in place of however many the shape has. Both are decided
   * per instance, on the CPU, from what _upload worked out once.
   *
   * ‼️ Conservative on purpose: the section plane culls nothing (a copy cut in half
   * is still on screen), and a camera inside a sphere never draws its box. What is
   * culled only has to be invisible; what is kept may be. */
  var FOV_DEGREES = 45;

  /* One function, read by both the sort and the draw. Two copies would eventually
   * disagree, and a part sorted as opaque and drawn translucent is a part that
   * erases whatever is behind it. */
  function alphaOf(b, i, transparency) {
    return b.removed[i] ? REMOVED_ALPHA : b.opacity[i] * transparency;
  }
  var LOD_PIXELS = 3;

  /* ---- A level between the box and the whole mesh (Phase 6, stage W2) ----------
   *
   * # The problem this solves
   *
   * Until W2 a copy was either its box (under three pixels of radius) or every triangle
   * of its shape. A lamp twenty pixels across on a car seen whole was drawn with the
   * sphere's 960 triangles, and a kernel definition of a cast housing with all of its
   * thousands.
   *
   * Each batch computes, once at upload, a SIMPLIFIED mesh of its shape by clustering
   * vertices on a grid of SIMPLE_CELLS across its longest side: every vertex in a cell
   * becomes the cell's average, and triangles that collapse are dropped. A copy under
   * LOD_SIMPLE_PIXELS of radius is drawn with it. A shape that does not at least halve
   * has no simplified level and goes straight from whole to box.
   *
   * Picking never reads it: a click is tested against the real triangles (pick).
   * Fence: TestRendererDrawsACopyAtTheLevelItsProjectedSizeCalls. */
  var LOD_SIMPLE_PIXELS = 24;
  var SIMPLE_CELLS = 8;
  var SIMPLE_MIN_TRIANGLES = 48;

  function simplifyGeometry(geo, bd) {
    var pos = geo.positions, idx = geo.indices, triangles = idx.length / 3;
    if (triangles <= SIMPLE_MIN_TRIANGLES) return null;
    var side = Math.max(bd.max[0] - bd.min[0], bd.max[1] - bd.min[1], bd.max[2] - bd.min[2]);
    if (!(side > 0)) return null;
    var cell = side / SIMPLE_CELLS, span = SIMPLE_CELLS + 1;
    var clusters = {}, sums = [], remap = new Int32Array(pos.length / 3), v, k;
    for (v = 0; v < remap.length; v++) {
      var key = 0;
      for (k = 0; k < 3; k++) {
        key = key * span + Math.max(0, Math.min(SIMPLE_CELLS, Math.floor((pos[v * 3 + k] - bd.min[k]) / cell)));
      }
      var c = clusters[key];
      if (c === undefined) { c = clusters[key] = sums.length / 4; sums.push(0, 0, 0, 0); }
      sums[c * 4] += pos[v * 3]; sums[c * 4 + 1] += pos[v * 3 + 1]; sums[c * 4 + 2] += pos[v * 3 + 2];
      sums[c * 4 + 3]++;
      remap[v] = c;
    }
    var vertices = new Array(sums.length / 4 * 3);
    for (c = 0; c < sums.length / 4; c++) {
      for (k = 0; k < 3; k++) vertices[c * 3 + k] = sums[c * 4 + k] / sums[c * 4 + 3];
    }
    var out = [], seen = {};
    for (var t = 0; t + 2 < idx.length; t += 3) {
      var a = remap[idx[t]], b = remap[idx[t + 1]], d = remap[idx[t + 2]];
      if (a === b || b === d || a === d) continue;
      /* One key per triangle AND winding: two faces back to back are both kept. */
      var id = a < b && a < d ? a + ',' + b + ',' + d : b < d ? b + ',' + d + ',' + a : d + ',' + a + ',' + b;
      if (seen[id]) continue;
      seen[id] = true;
      out.push(a, b, d);
    }
    if (!out.length || out.length / 3 > triangles / 2) return null;
    return kernelGeometry({ vertices: vertices, triangles: out }).geo;
  }

  function simpleBuffers(gl, geo, bd) {
    var s = simplifyGeometry(geo, bd);
    if (!s) return null;
    return {
      position: makeBuffer(gl, gl.ARRAY_BUFFER, new Float32Array(s.positions)),
      normal:   makeBuffer(gl, gl.ARRAY_BUFFER, new Float32Array(s.normals)),
      index:    makeBuffer(gl, gl.ELEMENT_ARRAY_BUFFER, new Uint16Array(s.indices)),
      count:    s.indices.length,
      geo:      s
    };
  }

  /* Which level a copy of radius r at dist is drawn at: 2 its box, 1 its simplified
   * mesh, 0 whole. One function, read per copy and for a whole node of the hierarchy. */
  function levelFor(b, r, dist, pixels, lod, simple) {
    if (lod > 0 && dist > r) {
      var px = r * pixels / dist;
      if (b.triangles > 12 && px < lod) return 2;
      if (b.simple && px < simple) return 1;
    }
    return 0;
  }

  /* ---- Culling through a hierarchy of copies (Phase 6, stage W2) ----------------
   *
   * # The problem this solves
   *
   * Until W2 every frame tested every copy's sphere against the view: 100,000 sphere
   * tests to draw a car seen from across the room, and 100,000 to draw nothing when the
   * camera looked away from it.
   *
   * Each batch builds, once at upload, a binary tree over its copies: split at the
   * median along the longest side of their centres, TREE_LEAF copies to a leaf. A node
   * holds the box around its copies' spheres, the box of their centres and their largest
   * radius. A frame walks it:
   *
   *   - a node wholly outside one plane culls every copy under it, unvisited;
   *   - a node wholly inside every plane keeps every copy under it without testing
   *     one, and when even its nearest copy is too small, draws them all as boxes;
   *   - a node that straddles a plane is opened, and a leaf tests its copies one by
   *     one exactly as before.
   *
   * # Why the answer is the per-copy answer
   *
   * A copy is culled by the flat test when its centre is more than its radius behind a
   * plane. A node's box holds every copy's centre ± radius, so a box wholly behind the
   * plane holds only such copies, and a box wholly in front none — both with a margin
   * for rounding, and anything within it is opened and tested per copy. An exploded view
   * moves each copy by at most the explode distance, so the boxes grow by that. An
   * assembly state moves copies by amounts of its own, so a frame with one tests every
   * copy. TestRendererCullsAHierarchyExactlyAsItCullsEachCopy holds it on randomized
   * cameras, and that a far or turned-away car visits a handful of nodes.
   *
   * ‼️ What is counted changes in one way: a copy under a culled node is counted
   * culled even when an isolation would also have hidden it. Every copy is still
   * counted once. */
  var TREE_LEAF = 8;

  function instanceTree(b) {
    var n = b.n;
    if (n <= TREE_LEAF) return null;
    var order = new Int32Array(n), scale = 0, count = 0, i;
    for (i = 0; i < n; i++) order[i] = i;
    function build(start, end) {
      var node = { start: start, end: end, lo: [Infinity, Infinity, Infinity], hi: [-Infinity, -Infinity, -Infinity],
                   clo: [Infinity, Infinity, Infinity], chi: [-Infinity, -Infinity, -Infinity], rmax: 0,
                   left: null, right: null };
      count++;
      for (var k = start; k < end; k++) {
        var j = order[k], r = b.radius[j];
        if (r > node.rmax) node.rmax = r;
        for (var a = 0; a < 3; a++) {
          var c = b.centre[j * 3 + a];
          if (c - r < node.lo[a]) node.lo[a] = c - r;
          if (c + r > node.hi[a]) node.hi[a] = c + r;
          if (c < node.clo[a]) node.clo[a] = c;
          if (c > node.chi[a]) node.chi[a] = c;
        }
      }
      if (end - start <= TREE_LEAF) return node;
      var axis = 0;
      for (var x = 1; x < 3; x++) {
        if (node.chi[x] - node.clo[x] > node.chi[axis] - node.clo[axis]) axis = x;
      }
      if (!(node.chi[axis] - node.clo[axis] > 0)) return node;
      var sorted = Array.prototype.slice.call(order, start, end).sort(function (p, q) {
        return b.centre[p * 3 + axis] - b.centre[q * 3 + axis];
      });
      order.set(sorted, start);
      var mid = (start + end) >> 1;
      node.left = build(start, mid);
      node.right = build(mid, end);
      return node;
    }
    var root = build(0, n);
    for (i = 0; i < 3; i++) scale = Math.max(scale, Math.abs(root.lo[i]), Math.abs(root.hi[i]));
    return { root: root, order: order, nodes: count, scale: scale };
  }

  /* -1 when a box grown by `grow` is wholly behind a plane, 1 when it is wholly in front
   * of every plane, 0 when it straddles one — with a rounding margin that only ever
   * turns an answer into 0. */
  function boxAgainstFrustum(planes, lo, hi, grow, scale) {
    var inside = 1;
    for (var i = 0; i < 6; i++) {
      var p = planes[i], eps = 1e-9 * (scale + grow + Math.abs(p[3]) + 1);
      var most = p[0] * (p[0] > 0 ? hi[0] + grow : lo[0] - grow) + p[1] * (p[1] > 0 ? hi[1] + grow : lo[1] - grow) +
                 p[2] * (p[2] > 0 ? hi[2] + grow : lo[2] - grow) + p[3];
      if (most < -eps) return -1;
      var least = p[0] * (p[0] > 0 ? lo[0] - grow : hi[0] + grow) + p[1] * (p[1] > 0 ? lo[1] - grow : hi[1] + grow) +
                  p[2] * (p[2] > 0 ? lo[2] - grow : hi[2] + grow) + p[3];
      if (least < eps) inside = 0;
    }
    return inside;
  }

  /* The six planes of the view, from clip = projection · view, each normalised so a
   * sphere's distance can be compared with its radius (Gribb and Hartmann). */
  function frustumPlanes(m) {
    var rows = [[m[0], m[4], m[8], m[12]], [m[1], m[5], m[9], m[13]],
                [m[2], m[6], m[10], m[14]], [m[3], m[7], m[11], m[15]]];
    var out = [];
    [[0, 1], [0, -1], [1, 1], [1, -1], [2, 1], [2, -1]].forEach(function (p) {
      var a = rows[3], r = rows[p[0]], s = p[1];
      var pl = [a[0] + s * r[0], a[1] + s * r[1], a[2] + s * r[2], a[3] + s * r[3]];
      var l = Math.sqrt(pl[0] * pl[0] + pl[1] * pl[1] + pl[2] * pl[2]) || 1;
      out.push([pl[0] / l, pl[1] / l, pl[2] / l, pl[3] / l]);
    });
    return out;
  }

  function sphereInFrustum(planes, x, y, z, r) {
    for (var i = 0; i < 6; i++) {
      var p = planes[i];
      if (p[0] * x + p[1] * y + p[2] * z + p[3] < -r) return false;
    }
    return true;
  }

  function invert4(m) {
    var a00 = m[0], a01 = m[1], a02 = m[2], a03 = m[3], a10 = m[4], a11 = m[5], a12 = m[6], a13 = m[7],
        a20 = m[8], a21 = m[9], a22 = m[10], a23 = m[11], a30 = m[12], a31 = m[13], a32 = m[14], a33 = m[15];
    var b00 = a00 * a11 - a01 * a10, b01 = a00 * a12 - a02 * a10, b02 = a00 * a13 - a03 * a10,
        b03 = a01 * a12 - a02 * a11, b04 = a01 * a13 - a03 * a11, b05 = a02 * a13 - a03 * a12,
        b06 = a20 * a31 - a21 * a30, b07 = a20 * a32 - a22 * a30, b08 = a20 * a33 - a23 * a30,
        b09 = a21 * a32 - a22 * a31, b10 = a21 * a33 - a23 * a31, b11 = a22 * a33 - a23 * a32;
    var det = b00 * b11 - b01 * b10 + b02 * b09 + b03 * b08 - b04 * b07 + b05 * b06;
    if (!det) return null;
    det = 1 / det;
    return [(a11 * b11 - a12 * b10 + a13 * b09) * det, (a02 * b10 - a01 * b11 - a03 * b09) * det,
            (a31 * b05 - a32 * b04 + a33 * b03) * det, (a22 * b04 - a21 * b05 - a23 * b03) * det,
            (a12 * b08 - a10 * b11 - a13 * b07) * det, (a00 * b11 - a02 * b08 + a03 * b07) * det,
            (a32 * b02 - a30 * b05 - a33 * b01) * det, (a20 * b05 - a22 * b02 + a23 * b01) * det,
            (a10 * b10 - a11 * b08 + a13 * b06) * det, (a01 * b08 - a00 * b10 - a03 * b06) * det,
            (a30 * b04 - a31 * b02 + a33 * b00) * det, (a21 * b02 - a20 * b04 - a23 * b00) * det,
            (a11 * b07 - a10 * b09 - a12 * b06) * det, (a00 * b09 - a01 * b07 + a02 * b06) * det,
            (a31 * b01 - a30 * b03 - a32 * b00) * det, (a20 * b03 - a21 * b01 + a22 * b00) * det];
  }

  function transformPoint(m, v, w) {
    var x = m[0] * v[0] + m[4] * v[1] + m[8] * v[2] + m[12] * w;
    var y = m[1] * v[0] + m[5] * v[1] + m[9] * v[2] + m[13] * w;
    var z = m[2] * v[0] + m[6] * v[1] + m[10] * v[2] + m[14] * w;
    var q = m[3] * v[0] + m[7] * v[1] + m[11] * v[2] + m[15] * w;
    return w && q ? [x / q, y / q, z / q] : [x, y, z];
  }

  /* The nearest triangle a ray crosses, from either side, as its distance along the
   * ray (Möller and Trumbore), or null. */
  function nearestTriangle(pos, idx, o, d) {
    var best = null;
    for (var t = 0; t + 2 < idx.length; t += 3) {
      var a = idx[t] * 3, b = idx[t + 1] * 3, c = idx[t + 2] * 3;
      var e1x = pos[b] - pos[a], e1y = pos[b + 1] - pos[a + 1], e1z = pos[b + 2] - pos[a + 2];
      var e2x = pos[c] - pos[a], e2y = pos[c + 1] - pos[a + 1], e2z = pos[c + 2] - pos[a + 2];
      var px = d[1] * e2z - d[2] * e2y, py = d[2] * e2x - d[0] * e2z, pz = d[0] * e2y - d[1] * e2x;
      var det = e1x * px + e1y * py + e1z * pz;
      if (Math.abs(det) < 1e-12) continue;
      var inv = 1 / det;
      var sx = o[0] - pos[a], sy = o[1] - pos[a + 1], sz = o[2] - pos[a + 2];
      var u = (sx * px + sy * py + sz * pz) * inv;
      if (u < 0 || u > 1) continue;
      var qx = sy * e1z - sz * e1y, qy = sz * e1x - sx * e1z, qz = sx * e1y - sy * e1x;
      var v = (d[0] * qx + d[1] * qy + d[2] * qz) * inv;
      if (v < 0 || u + v > 1) continue;
      var dist = (e2x * qx + e2y * qy + e2z * qz) * inv;
      if (dist >= 0 && (best === null || dist < best)) best = dist;
    }
    return best;
  }

  Studio.prototype.draw = function () {
    if (!this.gl) return;
    var gl = this.gl;
    var w = this.canvas.width, h = this.canvas.height;

    /* The contact shadow first, when what is drawn has changed: it draws into its own
     * target and leaves the canvas alone. */
    var captured = 0;
    if (this._shadowDirty) {
      if (this.contactShadow && this.bounds) {
        this._captureShadow();
        captured = this._shadow ? this._shadow.instances : 0;
      } else { this._shadow = null; this._shadowDirty = false; }
    }

    var post = this._postTargets(w, h);
    gl.bindFramebuffer(gl.FRAMEBUFFER, post ? post.ms : null);
    gl.viewport(0, 0, w, h);
    var g = ground();
    gl.clearColor(g.clear[0], g.clear[1], g.clear[2], 1);
    gl.clear(gl.COLOR_BUFFER_BIT | gl.DEPTH_BUFFER_BIT);
    gl.enable(gl.DEPTH_TEST);
    gl.enable(gl.CULL_FACE);
    gl.enable(gl.BLEND);
    gl.blendFunc(gl.SRC_ALPHA, gl.ONE_MINUS_SRC_ALPHA);

    var eye = this._eye();
    var view = lookAt(eye, this.camera.target, [0, 1, 0]);
    /* Near and far from the model's size, not fixed numbers (A4, 2026-09-18). */
    var clip = clipPlanes(eye, this.camera.target, this.bounds, this.explode);
    var proj = perspective(FOV_DEGREES, w / Math.max(1, h), clip.near, clip.far);

    this._drawBackdrop(view, proj);
    if (this.showGrid) this._drawGrid(view, proj);
    this._drawGround(view, proj);

    gl.useProgram(this.prog);
    var P = this.prog;
    var loc = this._loc || (this._loc = {
      view: gl.getUniformLocation(P, 'uView'),
      proj: gl.getUniformLocation(P, 'uProj'),
      cam: gl.getUniformLocation(P, 'uCamPos'),
      secAxis: gl.getUniformLocation(P, 'uSectionAxis'),
      secAt: gl.getUniformLocation(P, 'uSectionAt'),
      light2: gl.getUniformLocation(P, 'uLight'),
      material: gl.getUniformLocation(P, 'uMaterial'),
      env: gl.getUniformLocation(P, 'uEnv'),
      sh: gl.getUniformLocation(P, 'uSH'),
      keyDir: gl.getUniformLocation(P, 'uKeyDir'),
      keyColour: gl.getUniformLocation(P, 'uKeyColour'),
      exposure: gl.getUniformLocation(P, 'uExposure')
    });
    gl.uniformMatrix4fv(loc.view, false, view);
    gl.uniformMatrix4fv(loc.proj, false, proj);
    gl.uniform3fv(loc.cam, eye);
    gl.uniform1f(loc.light2, LIGHT ? 1 : 0);
    gl.uniform1i(loc.secAxis, this.section.axis);
    gl.uniform1f(loc.secAt, this.section.at);
    gl.activeTexture(gl.TEXTURE0);
    gl.bindTexture(gl.TEXTURE_2D, this._envTexture);
    gl.uniform1i(loc.env, 0);
    gl.uniform3fv(loc.sh, environment().sh);
    gl.uniform3fv(loc.keyDir, KEY_LIGHT.direction);
    gl.uniform3fv(loc.keyColour, KEY_LIGHT.colour);
    gl.uniform1f(loc.exposure, g.exposure);

    /* What the frame was drawn with, kept for pick(): a click is resolved against the
     * camera and the displacements that were on screen, not whatever changed since. */
    var frame = this._frame = {
      number: (this._frameNumber = (this._frameNumber || 0) + 1),
      view: view, proj: proj, eye: eye, planes: frustumPlanes(multiply(proj, view)),
      pixels: h / (2 * Math.tan(FOV_DEGREES * Math.PI / 360)), near: clip.near, far: clip.far
    };
    /* Counted every frame and kept, because "how many draw calls" is the question a
     * slow viewport is asked first and the answer must not need a debugger. */
    var stats = this.stats = { path: this.renderPath, batches: (this.batches || []).length, drawCalls: 0,
      instances: 0, culled: 0, proxied: 0, simplified: 0, hidden: 0, translucent: 0, placeholders: 0,
      visited: 0, uploadedBytes: 0, edgeDraws: 0, finer: 0,
      post: post ? 'msaa' + post.samples + '+ssao' : 'none', shadow: !!this._shadow,
      /* Copies drawn into the contact shadow THIS frame: 0 unless what is drawn changed. */
      shadowPass: captured };
    var translucent = [];
    for (var n = 0; n < (this.batches || []).length; n++) {
      this._drawBatch(this.batches[n], frame, stats, translucent);
    }
    /* Occlusion darkens what is opaque, before anything see-through is laid over it. */
    if (post) {
      this._resetInstanceAttributes();   // before the occlusion's full-screen passes
      this._applyOcclusion(post, proj, (this.bounds && this.bounds.span) || 10);
      gl.useProgram(this.prog);
      gl.activeTexture(gl.TEXTURE0);
      gl.bindTexture(gl.TEXTURE_2D, this._envTexture);
    }
    if (translucent.length) this._drawTranslucent(translucent, stats);
    this._resetInstanceAttributes();
    gl.frontFace(gl.CCW);
    gl.bindTexture(gl.TEXTURE_2D, null);

    /* PRD VIS-03, drawn last so the marks sit over the model rather than
     * inside it, and placed last so the numbers follow the same camera the
     * lines were drawn with — computing them from a stale matrix is how a
     * label ends up beside the wrong feature. */
    if (this.showOverlays) this._drawOverlays(view, proj);
    if (post) {
      gl.bindFramebuffer(gl.READ_FRAMEBUFFER, post.ms);
      gl.bindFramebuffer(gl.DRAW_FRAMEBUFFER, post.flat);
      gl.blitFramebuffer(0, 0, w, h, 0, 0, w, h, gl.COLOR_BUFFER_BIT, gl.NEAREST);
      gl.bindFramebuffer(gl.READ_FRAMEBUFFER, post.flat);
      gl.bindFramebuffer(gl.DRAW_FRAMEBUFFER, null);
      gl.blitFramebuffer(0, 0, w, h, 0, 0, w, h, gl.COLOR_BUFFER_BIT, gl.NEAREST);
      gl.bindFramebuffer(gl.FRAMEBUFFER, null);
    }
    this._placeLabels(view, proj);
  };

  /* The key light: from above, front-right of the hero view, slightly warm. The
   * environment carries the rest of the light; this gives the highlight a direction. */
  var KEY_LIGHT = { direction: normalize([0.45, 0.85, 0.5]), colour: [2.2, 2.1, 1.95] };

  /* One batch: decide each copy — through the batch's hierarchy, or one by one — lay
   * the survivors out in six runs (whole, simplified or box, each wound either way),
   * upload them once, and draw each run with one call. */
  Studio.prototype._drawBatch = function (b, f, stats, translucent) {
    var gl = this.gl, g = ground();
    var ctx = { b: b, f: f, stats: stats, translucent: translucent,
      explode: this.explode > 0 && this.bounds ? this.explode * this.bounds.span * 0.6 : 0,
      isolated: this.isolated, state: this.state, lod: this.lod, simple: this.lodSimple,
      count: 0, counts: [0, 0, 0, 0, 0, 0], widest: 0 };
    var k;
    if (this.hierarchy && b.tree && !this.state) {
      this._visitTree(ctx);
    } else {
      for (k = 0; k < b.n; k++) this._classify(ctx, k, false, -1);
    }
    var counts = ctx.counts, total = ctx.count;
    /* How finely a curved primitive is drawn this frame, from its largest copy on screen
     * (A5). Decided in the frame a person sees, never in the shadow's. */
    if (f.pass !== 'shadow') this._chooseDetail(b, ctx.widest, stats);
    if (!total) return;
    var starts = [0];
    for (k = 1; k < 6; k++) starts[k] = starts[k - 1] + counts[k - 1];
    var at = starts.slice();
    for (k = 0; k < total; k++) this._writeInstance(b.scratch, at[b.drawnGroup[k]]++, b, b.drawn[k], 1, g);
    if (this.instancing) {
      var data = b.scratch.subarray(0, total * INSTANCE_FLOATS);
      gl.bindBuffer(gl.ARRAY_BUFFER, b.instanceBuffer);
      gl.bufferData(gl.ARRAY_BUFFER, data, gl.DYNAMIC_DRAW);
      stats.uploadedBytes += data.byteLength;
    }
    for (k = 0; k < 6; k++) {
      if (counts[k]) this._drawRun(b, k >> 1, k % 2 === 1, b.instanceBuffer, b.scratch, starts[k], counts[k], stats);
    }
    /* Feature lines over the copies drawn whole (A6). Instanced paths only: a browser
     * drawing one call per copy is already slow, and lines would double it. */
    if (this.featureLines && f.pass !== 'shadow' && this.instancing && counts[0] + counts[1]) {
      this._drawEdges(b, starts[0], counts[0] + counts[1], stats);
    }
  };

  /* Pick the level of a curved primitive batch for this frame and put its buffers in
   * b.draw, building a finer level the first time it is needed. `widest` is the largest
   * bounding radius, in pixels, of a copy drawn whole. */
  Studio.prototype._chooseDetail = function (b, widest, stats) {
    var factor = 1;
    if (this.adaptiveDetail && b.curve && widest > 0) {
      /* The curve's own radius when the shape names one (a cylinder's), else the box's. */
      var r = b.curve.radius > 0 ? widest * b.curve.radius / Math.max(b.bounds.radius, 1e-12) : widest;
      factor = detailFor(r, b.curve.segments);
    }
    if (factor === 1) { b.draw = b.buffers; b.drawCount = b.count; b.drawType = b.indexType; b.detail = 1; return; }
    var level = b.details[factor];
    if (!level) {
      var gl = this.gl, built = geometryAtDetail(b.shape, factor), geo = built.geo;
      var wide = geo.positions.length / 3 > 65535;
      if (wide && !this._wide) { level = b.details[factor] = { fallback: true }; }
      else {
        level = b.details[factor] = {
          position: makeBuffer(gl, gl.ARRAY_BUFFER, new Float32Array(geo.positions)),
          normal: makeBuffer(gl, gl.ARRAY_BUFFER, new Float32Array(geo.normals)),
          index: makeBuffer(gl, gl.ELEMENT_ARRAY_BUFFER, wide ? new Uint32Array(geo.indices) : new Uint16Array(geo.indices)),
          count: geo.indices.length, type: wide ? gl.UNSIGNED_INT : gl.UNSIGNED_SHORT
        };
      }
    }
    if (level.fallback) { b.draw = b.buffers; b.drawCount = b.count; b.drawType = b.indexType; b.detail = 1; return; }
    b.draw = level; b.drawCount = level.count; b.drawType = level.type; b.detail = factor;
    stats.finer++;
  };

  /* One batch's feature lines, for `count` copies from `start` of its instance buffer. */
  Studio.prototype._drawEdges = function (b, start, count, stats) {
    var gl = this.gl, E = this.edgeProg, f = this._frame;
    if (!b.edges) {
      var fe = featureEdges(b.geo, CREASE_DEGREES);
      b.edges = fe.indices.length ? {
        position: makeBuffer(gl, gl.ARRAY_BUFFER, new Float32Array(fe.positions)),
        index: makeBuffer(gl, gl.ELEMENT_ARRAY_BUFFER, fe.positions.length / 3 > 65535 ? new Uint32Array(fe.indices) : new Uint16Array(fe.indices)),
        count: fe.indices.length, type: fe.positions.length / 3 > 65535 ? gl.UNSIGNED_INT : gl.UNSIGNED_SHORT
      } : { count: 0 };
    }
    if (!b.edges.count || (b.edges.type === gl.UNSIGNED_INT && !this._wide)) return;
    gl.useProgram(E);
    gl.uniformMatrix4fv(this._u(E, 'uView'), false, f.view);
    gl.uniformMatrix4fv(this._u(E, 'uProj'), false, f.proj);
    gl.uniform4fv(this._u(E, 'uEdge'), ground().edge);
    gl.bindBuffer(gl.ARRAY_BUFFER, b.edges.position);
    gl.enableVertexAttribArray(ATTRIB.pos);
    gl.vertexAttribPointer(ATTRIB.pos, 3, gl.FLOAT, false, 0, 0);
    /* The normal is not read, and its array holds the TRIANGLES' vertices — fewer than
     * the lines may index — so it is switched off for this draw (_drawRun turns it on). */
    gl.disableVertexAttribArray(ATTRIB.normal);
    gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, b.edges.index);
    var stride = INSTANCE_FLOATS * 4, off = start * stride, inst = this.instancing, k;
    gl.bindBuffer(gl.ARRAY_BUFFER, b.instanceBuffer);
    for (k = 0; k < 4; k++) {
      gl.enableVertexAttribArray(ATTRIB.model + k);
      gl.vertexAttribPointer(ATTRIB.model + k, 4, gl.FLOAT, false, stride, off + k * 16);
      inst.divisor(ATTRIB.model + k, 1);
    }
    gl.enableVertexAttribArray(ATTRIB.colour);
    gl.vertexAttribPointer(ATTRIB.colour, 4, gl.FLOAT, false, stride, off + 16 * 4);
    inst.divisor(ATTRIB.colour, 1);
    gl.enableVertexAttribArray(ATTRIB.highlight);
    gl.vertexAttribPointer(ATTRIB.highlight, 1, gl.FLOAT, false, stride, off + 20 * 4);
    inst.divisor(ATTRIB.highlight, 1);
    inst.draw(b.edges.count, b.edges.type, count, gl.LINES);
    stats.edgeDraws++;
    gl.useProgram(this.prog);
  };

  /* The batch's hierarchy, walked with the rules above; a leaf and a node wholly in view
   * hand their copies to _classify. */
  Studio.prototype._visitTree = function (ctx) {
    var b = ctx.b, t = b.tree, f = ctx.f, stats = ctx.stats;
    var stack = this._stack || (this._stack = []);
    stack.length = 0;
    stack.push(t.root);
    while (stack.length) {
      var node = stack.pop();
      stats.visited++;
      var rel = boxAgainstFrustum(f.planes, node.lo, node.hi, ctx.explode, t.scale);
      if (rel < 0) { stats.culled += node.end - node.start; continue; }
      if (rel > 0 || !node.left) {
        var level = -1;
        if (rel > 0) {
          /* Nearest any copy's centre can be, after the explode moves it. */
          var d2 = 0;
          for (var a = 0; a < 3; a++) {
            var e = f.eye[a], gap = e < node.clo[a] ? node.clo[a] - e : e > node.chi[a] ? e - node.chi[a] : 0;
            d2 += gap * gap;
          }
          var near = Math.sqrt(d2) - ctx.explode;
          if (near > 0 && levelFor(b, node.rmax, near, f.pixels, ctx.lod, 0) === 2) level = 2;
        }
        for (var k = node.start; k < node.end; k++) this._classify(ctx, t.order[k], rel > 0, level);
        continue;
      }
      stack.push(node.right, node.left);
    }
  };

  /* One copy: hidden, culled, or drawn at a level — in a run, or queued as translucent.
   * `whole` says a node already put it in view; a level of 2 says a node already made it
   * a box, and -1 leaves the level to this copy's own distance. */
  Studio.prototype._classify = function (ctx, i, whole, level) {
    var b = ctx.b, f = ctx.f, stats = ctx.stats, o = i * 3;
    var id = b.ids[i], rep = b.repeatOf[i];
    if (ctx.isolated && !ctx.isolated(id, rep)) { stats.hidden++; return; }
    var dx = 0, dy = 0, dz = 0;
    /* Exploded view: parts move outward from the assembly centre, so the
     * relationship between them stays readable while the gap opens. */
    if (ctx.explode) {
      var ax = b.anchor[o] - this.bounds.centre[0], ay = b.anchor[o + 1] - this.bounds.centre[1],
          az = b.anchor[o + 2] - this.bounds.centre[2];
      var len = Math.sqrt(ax * ax + ay * ay + az * az);
      if (len < 1e-6) { ax = 0; ay = 1; az = 0; len = 1; }
      dx = ax / len * ctx.explode; dy = ay / len * ctx.explode; dz = az / len * ctx.explode;
    }
    /* PRD VIS-02. The active assembly state moves parts and hides them. It is
     * applied here rather than baked into the loaded geometry so that
     * switching states costs a redraw instead of a rebuild, and so the
     * document on screen stays the document that was stored. */
    if (ctx.state) {
      var st = this._stateFor(id, rep);
      if (st.hidden) { stats.hidden++; return; }
      if (st.offset) { dx += st.offset[0]; dy += st.offset[1]; dz += st.offset[2]; }
    }
    b.disp[o] = dx; b.disp[o + 1] = dy; b.disp[o + 2] = dz;
    var cx = b.centre[o] + dx, cy = b.centre[o + 1] + dy, cz = b.centre[o + 2] + dz, r = b.radius[i];
    if (!whole && !sphereInFrustum(f.planes, cx, cy, cz, r)) { stats.culled++; return; }
    b.seen[i] = f.number;
    var ex = cx - f.eye[0], ey = cy - f.eye[1], ez = cz - f.eye[2];
    var dist = Math.sqrt(ex * ex + ey * ey + ez * ez);
    if (level < 0) level = levelFor(b, r, dist, f.pixels, ctx.lod, ctx.simple);
    if (level === 0) {
      /* The camera may be inside a big copy's sphere: its surface is then nearer than its
       * centre, and the curve needs more than the centre's distance says. */
      var px = r * f.pixels / Math.max(dist, r * 0.02);
      if (px > ctx.widest) ctx.widest = px;
    }
    if (alphaOf(b, i, this.transparency) < 1) {
      ctx.translucent.push({ b: b, i: i, level: level, depth: dist });
      return;
    }
    var group = level * 2 + b.mirrored[i];
    b.drawn[ctx.count] = i;
    b.drawnGroup[ctx.count++] = group;
    ctx.counts[group]++;
  };

  /* One instance's 21 floats: its matrix moved by what the view added, its colour and
   * opacity, and whether it is selected. */
  Studio.prototype._writeInstance = function (out, slot, b, i, alpha, g) {
    var o = slot * INSTANCE_FLOATS, m = i * 16;
    for (var k = 0; k < 16; k++) out[o + k] = b.model[m + k];
    out[o + 12] += b.disp[i * 3];
    out[o + 13] += b.disp[i * 3 + 1];
    out[o + 14] += b.disp[i * 3 + 2];
    var rgb = this._colour(b.removed[i] ? g.removed : (b.specs[i].color || g.part));
    out[o + 16] = rgb[0]; out[o + 17] = rgb[1]; out[o + 18] = rgb[2]; out[o + 19] = alpha;
    out[o + 20] = this._highlighted(b.ids[i], b.repeatOf[i]) ? 1 : 0;
  };

  /* hexToRGB once per colour per ground, not once per copy per frame. */
  Studio.prototype._colour = function (hex) {
    var key = (LIGHT ? 'l' : 'd') + hex, memo = this._colours || (this._colours = {});
    return memo[key] || (memo[key] = hexToRGB(hex));
  };

  Studio.prototype._highlighted = function (id, repeatOf) {
    var sel = this.selected;
    if (!sel) return false;
    if (typeof sel === 'function') return !!sel(id, repeatOf);
    return id === sel || (!!repeatOf && repeatOf === sel) || id.indexOf(sel + PATH_SEPARATOR) === 0;
  };

  /* Opaque first, then transparent back-to-front, so a translucent housing does not
   * erase what is inside it. Sorted by COPY, not by batch — a batch's copies can be
   * in front of and behind another's — and drawn in runs of neighbours that share a
   * batch, so a cut's tools in a row are still one call. */
  Studio.prototype._drawTranslucent = function (list, stats) {
    var gl = this.gl, g = ground();
    list.sort(function (a, b) { return b.depth - a.depth; });
    var need = list.length * INSTANCE_FLOATS;
    if (!this._translucentData || this._translucentData.length < need) {
      this._translucentData = new Float32Array(need);
    }
    var data = this._translucentData, j;
    for (j = 0; j < list.length; j++) {
      this._writeInstance(data, j, list[j].b, list[j].i, alphaOf(list[j].b, list[j].i, this.transparency), g);
    }
    if (this.instancing) {
      if (!this._translucentBuffer) this._translucentBuffer = gl.createBuffer();
      gl.bindBuffer(gl.ARRAY_BUFFER, this._translucentBuffer);
      gl.bufferData(gl.ARRAY_BUFFER, data.subarray(0, need), gl.DYNAMIC_DRAW);
      stats.uploadedBytes += need * 4;
    }
    stats.translucent = list.length;
    var start = 0;
    for (j = 1; j <= list.length; j++) {
      var a = list[start], e = list[j];
      if (e && e.b === a.b && e.level === a.level && e.b.mirrored[e.i] === a.b.mirrored[a.i]) continue;
      this._drawRun(a.b, a.level, !!a.b.mirrored[a.i], this._translucentBuffer, data, start, j - start, stats);
      start = j;
    }
  };

  /* Draw `count` instances starting at `start` of `data` (uploaded to `buffer`). */
  Studio.prototype._drawRun = function (b, level, mirrored, buffer, data, start, count, stats) {
    var gl = this.gl, loc = this._loc;
    /* Whole copies are drawn at the level _chooseDetail picked for this frame (A5). */
    var whole = b.draw || b.buffers;
    var geo = level === 2 ? b.proxy : level === 1 ? b.simple : whole;
    var elements = level ? geo.count : (b.draw ? b.drawCount : b.count);
    var type = level ? gl.UNSIGNED_SHORT : (b.draw ? b.drawType : b.indexType);
    /* The finish, as the document declared it, as MATERIALS maps it. Not set in the
     * shadow's pass, whose program has no material. */
    if (this._pass !== 'shadow') gl.uniform3fv(loc.material, b.shading);
    gl.bindBuffer(gl.ARRAY_BUFFER, geo.position);
    gl.enableVertexAttribArray(ATTRIB.pos);
    gl.vertexAttribPointer(ATTRIB.pos, 3, gl.FLOAT, false, 0, 0);
    gl.bindBuffer(gl.ARRAY_BUFFER, geo.normal);
    gl.enableVertexAttribArray(ATTRIB.normal);
    gl.vertexAttribPointer(ATTRIB.normal, 3, gl.FLOAT, false, 0, 0);
    gl.bindBuffer(gl.ELEMENT_ARRAY_BUFFER, geo.index);
    /* A reflected copy's triangles wind the other way, so with back faces culled it
     * would be drawn inside out. Decided by the sign of the matrix it is drawn with,
     * which is a mirrored primitive's reflection and nothing for a kernel mesh that
     * arrived already reflected with its winding intact. */
    gl.frontFace(mirrored ? gl.CW : gl.CCW);

    var inst = this.instancing, k;
    if (inst) {
      var stride = INSTANCE_FLOATS * 4, off = start * stride;
      gl.bindBuffer(gl.ARRAY_BUFFER, buffer);
      for (k = 0; k < 4; k++) {
        gl.enableVertexAttribArray(ATTRIB.model + k);
        gl.vertexAttribPointer(ATTRIB.model + k, 4, gl.FLOAT, false, stride, off + k * 16);
        inst.divisor(ATTRIB.model + k, 1);
      }
      gl.enableVertexAttribArray(ATTRIB.colour);
      gl.vertexAttribPointer(ATTRIB.colour, 4, gl.FLOAT, false, stride, off + 64);
      inst.divisor(ATTRIB.colour, 1);
      gl.enableVertexAttribArray(ATTRIB.highlight);
      gl.vertexAttribPointer(ATTRIB.highlight, 1, gl.FLOAT, false, stride, off + 80);
      inst.divisor(ATTRIB.highlight, 1);
      inst.draw(elements, type, count);
      stats.drawCalls++;
    } else {
      for (k = ATTRIB.model; k <= ATTRIB.highlight; k++) gl.disableVertexAttribArray(k);
      for (var j = 0; j < count; j++) {
        var o = (start + j) * INSTANCE_FLOATS;
        for (k = 0; k < 4; k++) {
          gl.vertexAttrib4f(ATTRIB.model + k, data[o + k * 4], data[o + k * 4 + 1], data[o + k * 4 + 2], data[o + k * 4 + 3]);
        }
        gl.vertexAttrib4f(ATTRIB.colour, data[o + 16], data[o + 17], data[o + 18], data[o + 19]);
        gl.vertexAttrib1f(ATTRIB.highlight, data[o + 20]);
        gl.drawElements(gl.TRIANGLES, elements, type, 0);
        stats.drawCalls++;
      }
    }
    stats.instances += count;
    if (level === 2) stats.proxied += count;
    if (level === 1) stats.simplified += count;
    if (b.placeholder) stats.placeholders += count;
  };

  /* ‼️ Divisors and enabled arrays are CONTEXT state, not program state. Left set, the
   * next grid or overlay drawArrays runs with attribute locations 1–7 still reading
   * per-instance buffers: WebGL1 with ANGLE refuses the draw, and WebGL2 reads past the
   * end of a buffer sized for this batch. So every frame puts them back. */
  Studio.prototype._resetInstanceAttributes = function () {
    var gl = this.gl;
    for (var k = ATTRIB.normal; k <= ATTRIB.highlight; k++) {
      if (this.instancing && k >= ATTRIB.model) this.instancing.divisor(k, 0);
      gl.disableVertexAttribArray(k);
    }
  };

  /* pick is the drawn part under a point on the canvas, as {id, distance}, or null.
   *
   * On the CPU and exact: the ray is carried into each candidate's own frame by the
   * inverse of the matrix it was drawn with, and tested against its batch's real
   * triangles — never the box a far copy is drawn as, and never a copy that was
   * hidden, isolated away or culled in the frame the click landed on. The id is the
   * occurrence path, so a click in a car names "seam-12/rivet-340".
   * Fence: TestRendererPicksAnInstanceBackToItsOccurrencePath. */
  Studio.prototype.pick = function (clientX, clientY) {
    var f = this._frame;
    if (!f || !this.batches || !this.batches.length) return null;
    var rect = this.canvas.getBoundingClientRect();
    var x = (clientX - rect.left) / Math.max(1, rect.width) * 2 - 1;
    var y = 1 - (clientY - rect.top) / Math.max(1, rect.height) * 2;
    var inv = invert4(multiply(f.proj, f.view));
    if (!inv) return null;
    var near = transformPoint(inv, [x, y, -1], 1), far = transformPoint(inv, [x, y, 1], 1);
    return this.pickRay(near, normalize(sub(far, near)));
  };

  Studio.prototype.pickRay = function (origin, dir) {
    var best = null, f = this._frame;
    if (!f) return null;
    for (var n = 0; n < this.batches.length; n++) {
      var b = this.batches[n];
      for (var i = 0; i < b.n; i++) {
        if (b.seen[i] !== f.number) continue;
        var o = i * 3, r = b.radius[i];
        var cx = b.centre[o] + b.disp[o] - origin[0], cy = b.centre[o + 1] + b.disp[o + 1] - origin[1],
            cz = b.centre[o + 2] + b.disp[o + 2] - origin[2];
        var along = cx * dir[0] + cy * dir[1] + cz * dir[2];
        if (along + r < 0 || cx * cx + cy * cy + cz * cz - along * along > r * r) continue;
        if (best && along - r > best.distance) continue;
        var m = Array.prototype.slice.call(b.model, i * 16, i * 16 + 16);
        m[12] += b.disp[o]; m[13] += b.disp[o + 1]; m[14] += b.disp[o + 2];
        var inv = invert4(m);
        if (!inv) continue;
        /* The same parameter measures distance in both frames: the map is affine, and
         * the world direction is a unit vector. */
        var t = nearestTriangle(b.geo.positions, b.geo.indices, transformPoint(inv, origin, 1),
                                transformPoint(inv, dir, 0));
        if (t !== null && (!best || t < best.distance)) {
          best = { id: b.ids[i], repeatOf: b.repeatOf[i], distance: t };
        }
      }
    }
    return best;
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
    var gg = ground();
    gl.uniform3fv(gl.getUniformLocation(this.lineProg, 'uColor'), gg.grid);
    gl.uniform1f(gl.getUniformLocation(this.lineProg, 'uOpacity'), gg.gridOpacity);
    var c = this.bounds ? this.bounds.centre : [0, 0, 0];
    gl.uniform4f(gl.getUniformLocation(this.lineProg, 'uFade'), c[0], c[2], span * 0.45, span * 1.1);
    gl.bindBuffer(gl.ARRAY_BUFFER, this._gridBuffer);
    gl.enableVertexAttribArray(pos);
    gl.vertexAttribPointer(pos, 3, gl.FLOAT, false, 0, 0);
    /* Writing no depth (2026-09-18): the grid is a mark on the floor, not a surface, and
     * its faded far lines left depth the occlusion pass read as geometry — a band of
     * speckle at the horizon. Parts still hide it: they are drawn after. */
    gl.depthMask(false);
    gl.drawArrays(gl.LINES, 0, this._gridCount);
    gl.depthMask(true);
  };

  /* _stateFor resolves what the active assembly state does to one part. */
  Studio.prototype._stateFor = function (id, repeatOf) {
    var st = this.state;
    if (!st) return {};
    /* A state names a part as written ("spoke": every copy) or one copy
     * ("spoke-3"), exactly the ids geometry.ValidateStates accepts. */
    var names = function (list) {
      return list && (list.indexOf(id) >= 0 || (repeatOf && list.indexOf(repeatOf) >= 0));
    };
    if (names(st.hidden)) return { hidden: true };
    var off = st.offsets && (st.offsets[id] || (repeatOf && st.offsets[repeatOf]));
    return off && off.length === 3 ? { offset: off } : {};
  };

  /* setState selects a named assembly state, or null for the assembly as
   * modelled. Redraws rather than reloads: switching states must not rebuild
   * geometry, and the document on screen stays the document that was stored. */
  Studio.prototype.setState = function (state) {
    this.state = state || null;
    this._shadowDirty = true;
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
    var go = ground();
    this._drawSegments(stated,  view, proj, go.stated, 0.95);
    this._drawSegments(derived, view, proj, go.derived, 0.85);
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
    gl.uniform4f(gl.getUniformLocation(this.lineProg, 'uFade'), 0, 0, 0, 0);
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

  /* Every part's kernel surface in assembly coordinates, from a mesh reply.
   *
   * Since Phase 4, stage K4 the reply sends each distinct shape's triangles ONCE
   * ("definitions") and each placed copy as the 4×4 column-major matrix that
   * places them ("instances"); only a part a feature changed arrives placed, in
   * "parts". The renderer draws placed parts, so this moves each copy into place —
   * exactly as cad.Build.WorldMeshes does in Go, which
   * TestRendererExpandsMeshInstancesLikeTheBuild holds it to. Since Phase 6, stage W1
   * the workbench does not call it: the reply is drawn instanced (drawBatches), each
   * definition once. It stays as the browser's statement of the rule for readers
   * that want placed triangles. */
  function expandMeshInstances(reply) {
    var out = (reply && reply.parts ? reply.parts : []).slice();
    var defs = (reply && reply.definitions) || [];
    ((reply && reply.instances) || []).forEach(function (inst) {
      var d = defs[inst.definition];
      if (!d) return;
      var m = inst.matrix, src = d.vertices, v = new Array(src.length);
      for (var i = 0; i + 2 < src.length; i += 3) {
        var x = src[i], y = src[i + 1], z = src[i + 2];
        v[i] = m[0] * x + m[4] * y + m[8] * z + m[12];
        v[i + 1] = m[1] * x + m[5] * y + m[9] * z + m[13];
        v[i + 2] = m[2] * x + m[6] * y + m[10] * z + m[14];
      }
      out.push({ id: inst.id, label: inst.label, vertices: v, triangles: d.triangles });
    });
    return out;
  }

  global.Forge3D = {
    supportedShapes: SUPPORTED,
    expandMeshInstances: expandMeshInstances,
    /* What Studio.load draws with what, exported so the fences hold every instance's
     * matrix to the exporter's placement without a GPU (Phase 6, stages W1 and W3). */
    drawBatches: drawBatches,
    /* The tree browser's rows and what a row reaches, exported for the workbench and
     * for the fence that holds a row's reach to what Go places under it (W2, W3). */
    treeChildren: treeChildren,
    occurrenceMatcher: occurrenceMatcher,
    /* Browsing a design past the viewport's limit (2026-09-17): one path's parts, how
     * many a path places, and a search over what the tree lists — none places the whole. */
    partsUnder: partsUnder, occurrencesUnder: occurrencesUnder, searchTree: searchTree,
    treeSearchIndex: treeSearchIndex, searchTreeIndexed: searchTreeIndexed,
    /* Whether a design is loaded a subtree at a time, and what its first view asks for
     * (W2): exported for the workbench, and for the fence that holds the policy. */
    loadsLazily: loadsLazily,
    firstViewPaths: firstViewPaths,
    /* What opening a row asks for (2026-09-17), exported for the fence that holds it. */
    openPaths: openPaths,
    /* A cylinder's length, reading "depth" when "height" is absent.
     *
     * Exported so the Parts panel reads it the same way the stage draws it and
     * the exporter builds it. Before this the panel had its OWN reading and
     * showed "⌀700 mm" with no length for a wheel the other two agreed was
     * 250 mm long — three consumers, three answers, from one document. */
    cylinderLength: cylinderLength,
    outlineExtent: outlineExtent,
    /* Exported for the Parts panel, which reads a gear's face width and outside
     * diameter the way the stage draws it, and for the fence that holds this copy
     * of gear.go to Go's answer point for point. */
    gearOutline: gearOutline,
    /* Exported for the fence that holds this copy of standard.go to Go's answer,
     * designation by designation. */
    standardPart: standardPart,
    standardDesignations: function () { return STANDARD_CATALOG.map(function (s) { return s.designation; }); },
    /* The list Studio.load draws, exported so TestRendererExpandsARepeatLikeTheExporter
     * holds the browser's copies to the exporter's, and so the workbench attaches a
     * kernel mesh to the copy it belongs to. */
    partsToDraw: partsToDraw, drawRefusal: drawRefusal,
    /* Where a drawn part goes, exported so TestRendererDoesNotPlaceAKernelMeshTwice
     * can hold a kernel mesh to the position the exporter gives it. */
    modelMatrix: modelMatrix,
    rotationRadians: rotationRadians,
    /* Exported so a Go fence can read it. The browser and the exporter each
     * hold a copy of the retirement table, and the failure they guard against
     * is the two disagreeing about what a retired word means — which a test
     * cannot see unless it can read both. */
    retiredShapes: RETIRED,
    /* Exported for the same reason: the fence drives the real dispatch rather
     * than a re-implementation of it, so a retired word that stopped resolving
     * would be caught where it actually happens. */
    buildGeometry: buildGeometry,
    /* The corner and arc arithmetic, which is the one thing in this file whose
     * ANSWER has to be identical to Go's rather than merely equivalent.
     *
     * The triangulation does not: two ear-clippings of the same polygon are the
     * same planar surface, and a fence comparing facets over a strongly concave
     * cap asserts an implementation detail that has no observable consequence.
     * Measured 2026-09-06 — the two implementations agree on every outline
     * tried and disagree on a crescent, with identical outlines and identical
     * solids. The DRAWING is what must match, so the drawing is what is
     * exported for comparison. */
    flattenDrawing: flattenDrawing,
    /* The presentation's rules (2026-09-18), exported so node fences can hold them
     * without a GPU: the finish table, the tone curve the shader is written from, the
     * clip planes and zoom limits, how fine a curve is drawn, the studio's light and its
     * irradiance, smoothing and feature edges, the hero view, and every shader source. */
    presentation: {
      materials: MATERIALS, shadingFor: shadingFor, aces: ACES, acesFilm: acesFilm,
      linearToSrgb: linearToSrgb, srgbToLinear: srgbToLinear,
      clipPlanes: clipPlanes, zoomLimits: zoomLimits, perspective: perspective,
      detailFor: detailFor, detailTolerancePx: DETAIL_TOLERANCE_PX, curveOf: curveOf,
      geometryAtDetail: geometryAtDetail,
      studioRadiance: studioRadiance, environmentSH: environmentSH, shIrradiance: shIrradiance,
      environment: environment, envLayout: ENV, shBasis: SH_BASIS,
      smoothNormals: smoothNormals, featureEdges: featureEdges, creaseDegrees: CREASE_DEGREES,
      hero: HERO, aoKernel: AO_KERNEL,
      shaders: { part: [VERT, FRAG], line: [LINE_VERT, LINE_FRAG], backdrop: [SCREEN_VERT, BACKDROP_FRAG],
                 shadow: [SHADOW_VERT, SHADOW_FRAG], blur: [SCREEN_VERT, BLUR_FRAG],
                 ground: [GROUND_VERT, GROUND_FRAG], edge: [EDGE_VERT, EDGE_FRAG],
                 ao: [AO_VERT, AO_FRAG], aoComposite: [AO_VERT, AO_COMPOSITE_FRAG] }
    },
    Studio: Studio,
    geometry: {
      box: boxGeometry, cylinder: cylinderGeometry,
      sphere: sphereGeometry, plane: planeGeometry,
      /* Exported because a sweep is the one shape here whose CONVENTIONS have to
       * agree with two other implementations — which way up the section starts,
       * how it is carried round a bend, and that corners are mitred. Volume
       * would not catch a section that came round a bend rolled, so the fence
       * compares this builder's facets against the Go one's, facet for facet
       * (TestRendererSweepsTheSameSolidAsTheExporter). It cannot do that unless
       * it can call this. */
      sweep: sweepGeometry
    }
  };
})(window);
