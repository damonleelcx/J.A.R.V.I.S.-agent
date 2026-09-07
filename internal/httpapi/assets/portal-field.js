/* Stream convergence — the home page's ambient field.
 *
 * # Why this is hand-written
 *
 * The effect it is modelled on ships as a licensed React package. This
 * application has no bundler, no npm step, and a `script-src 'self'` CSP, so a
 * package was never an option — and forge3d.js already establishes the house
 * answer for exactly this situation: raw WebGL, no dependency that can fail to
 * load in front of a customer.
 *
 * Three chromatically separated wavefronts cross a rotated flow field. The
 * separation is the whole look: the same curve is sampled at three phase
 * offsets and each offset drives one colour channel, so the bands read as one
 * ribbon with a violet core and blue/magenta fringes rather than three ribbons.
 *
 * # Degradation is deliberate and silent
 *
 * This is decoration. It must never be the reason the page fails to say what it
 * says. Without WebGL, without a GPU, or with prefers-reduced-motion, the canvas
 * is simply not shown and the CSS gradient beneath it stands on its own — no
 * error, no message about a decoration nobody asked for.
 */
(function (global) {
  'use strict';

  var VERT =
    'attribute vec2 p;' +
    'void main(){ gl_Position = vec4(p, 0.0, 1.0); }';

  /* The field itself.
   *
   * fbm warps the domain; the warped coordinate is then folded into a set of
   * sine streams. `band()` is a soft reciprocal falloff rather than a gaussian
   * because it keeps a long dim tail — that tail is what makes the streams look
   * like they are lit from inside rather than painted on. */
  var FRAG = [
    'precision highp float;',
    'uniform vec2  uRes;',
    'uniform float uTime;',
    'uniform float uSpeed;',
    'uniform float uScale;',
    'uniform float uBright;',
    'uniform float uScroll;',
    /* 0 = the field as designed: light on a near-black ground. 1 = the same
     * field printed as ink on paper. See the branch at the tone map. */
    'uniform float uLight;',

    'float hash(vec2 v){ return fract(sin(dot(v, vec2(127.1, 311.7))) * 43758.5453123); }',

    'float vnoise(vec2 v){',
    '  vec2 i = floor(v), f = fract(v);',
    '  vec2 u = f * f * (3.0 - 2.0 * f);',
    '  return mix(mix(hash(i + vec2(0.0,0.0)), hash(i + vec2(1.0,0.0)), u.x),',
    '             mix(hash(i + vec2(0.0,1.0)), hash(i + vec2(1.0,1.0)), u.x), u.y);',
    '}',

    'float fbm(vec2 v){',
    '  float s = 0.0, a = 0.5;',
    '  for (int i = 0; i < 5; i++){ s += a * vnoise(v); v *= 2.02; a *= 0.5; }',
    '  return s;',
    '}',

    'float band(float d, float w){ return w / (w + d * d); }',

    /* ---- the subject -----------------------------------------------------
     *
     * A white organic mass, raymarched from a smooth union of moving spheres.
     * Metaballs rather than a mesh because there is no asset pipeline here and
     * no geometry to load — and because the shape has to keep changing without
     * ever repeating a pose a viewer could recognise.
     *
     * smin() is the whole trick: a plain min() unions the spheres with visible
     * creases at every intersection, which reads as a pile of balls. Blending
     * the distance instead makes one surface that bulges. */
    'float smin(float a, float b, float k){',
    '  float h = clamp(0.5 + 0.5 * (b - a) / k, 0.0, 1.0);',
    '  return mix(b, a, h) - k * h * (1.0 - h);',
    '}',

    'float mapBlob(vec3 p, float t){',
    '  float d = 1e9;',
    '  for (int i = 0; i < 10; i++){',
    '    float fi = float(i);',
    /* Lissajous paths with incommensurate rates, so the mass never returns to
     * a pose it has held before. */
    '    vec3 c = vec3(sin(t * 0.51 + fi * 1.7),',
    '                  cos(t * 0.43 + fi * 2.3),',
    '                  sin(t * 0.37 + fi * 1.1)) * 0.40;',
    '    float rr = 0.34 - 0.15 * fract(fi * 0.37);',
    '    d = smin(d, length(p - c) - rr, 0.26);',
    '  }',
    '  return d;',
    '}',

    'vec3 blobNormal(vec3 p, float t){',
    '  vec2 e = vec2(0.0025, 0.0);',
    '  return normalize(vec3(',
    '    mapBlob(p + e.xyy, t) - mapBlob(p - e.xyy, t),',
    '    mapBlob(p + e.yxy, t) - mapBlob(p - e.yxy, t),',
    '    mapBlob(p + e.yyx, t) - mapBlob(p - e.yyx, t)));',
    '}',

    /* One wavefront, sampled at a phase offset. Returns the SIGNED DISTANCE to
     * the curve, so the caller can build a tight core and a wide halo from a
     * single evaluation rather than warping the domain twice. */
    'float stream(vec2 uv, float phase, float t){',
    '  vec2 w = uv;',
    /* Domain warp: the "fluid field". Two octaves of offset, the second slower,
     * so the ribbon breathes instead of sliding rigidly. */
    '  w.x += 0.55 * fbm(uv * 1.1 + vec2(t * 0.09, phase * 1.7));',
    '  w.y += 0.40 * fbm(uv * 0.8 - vec2(t * 0.06, phase));',
    '  float centre = 0.50 * sin(w.x * 0.95 + t * 0.45 + phase * 2.1)',
    '               + 0.22 * sin(w.x * 1.90 - t * 0.26 + phase * 1.3);',
    '  return w.y - centre;',
    '}',

    'void main(){',
    '  vec2 uv = (gl_FragCoord.xy - 0.5 * uRes) / uRes.y;',
    '  uv *= 3.4 / max(uScale, 0.001);',

    /* The field is rotated so the streams run corner to corner rather than
     * along the viewport edge — an axis-aligned ribbon reads as a UI element,
     * a rotated one reads as depth. */
    '  float a = -0.42;',
    '  uv = mat2(cos(a), -sin(a), sin(a), cos(a)) * uv;',

    '  float t = uTime * uSpeed;',

    /* Three streams, each sampled at three small phase offsets.
     *
     * The offsets are mapped onto COLOURS, not onto raw R/G/B channels. Driving
     * the channels directly is the obvious thing and it is wrong: it separates
     * into red, green and blue ribbons, and green is the one colour that must
     * not appear in a violet field. Blue leads, violet sits in the middle,
     * magenta trails — so where the three overlap they sum to violet-white and
     * where they part they fringe blue on one edge and red on the other. */
    '  vec3 cLead  = vec3(0.28, 0.34, 1.00);',
    '  vec3 cCore  = vec3(0.62, 0.22, 1.00);',
    '  vec3 cTrail = vec3(1.00, 0.20, 0.46);',

    '  vec3 col = vec3(0.0);',
    '  for (int i = 0; i < 3; i++){',
    '    float fi = float(i);',
    '    vec2 o = vec2(0.0, (fi - 1.0) * 0.95);',
    '    float d1 = stream(uv + o, fi * 2.4 + 0.030, t);',
    '    float d2 = stream(uv + o, fi * 2.4 + 0.000, t);',
    '    float d3 = stream(uv + o, fi * 2.4 - 0.030, t);',
    /* Two falloffs per wavefront. The tight one is the ribbon; the wide one is
     * the bloom around it, and the bloom is most of what the eye reads as
     * light. A single falloff gives a drawn line, not a lit one. */
    '    float k = 0.0022, h = 0.055;',
    '    col += cLead  * (band(d1, k) + 0.22 * band(d1, h));',
    '    col += cCore  * (band(d2, k) + 0.22 * band(d2, h)) * 1.25;',
    '    col += cTrail * (band(d3, k) + 0.22 * band(d3, h));',
    '  }',

    '  col *= 0.62 * uBright;',

    /* A faint haze so the black is never flat, and a vignette so the type at
     * the edges keeps its contrast. */
    '  float haze = fbm(uv * 0.5 + vec2(0.0, t * 0.02));',
    '  col += vec3(0.008, 0.008, 0.022) * haze;',
    '  float vig = 1.0 - 0.70 * dot(uv * 0.32, uv * 0.32);',
    '  col *= clamp(vig, 0.0, 1.0);',

    /* Tone map the field before the subject is composited, so the blob is lit
     * on its own terms and does not inherit the ribbons' bloom. */
    '  vec3 dcol = col / (1.0 + col);',
    '  dcol = pow(dcol, vec3(0.85));',
    '  dcol = max(dcol, vec3(0.070, 0.070, 0.082));',

    /* The light ground is not an inversion of the dark one, and it cannot be.
     *
     * Everything above accumulates ADDITIVELY, because that is what light does:
     * three overlapping wavefronts sum to violet-white. Adding light to white
     * paper produces white paper, so on a pale ground the same accumulation
     * renders as nothing at all — which is what the first attempt looked like.
     *
     * So the two quantities are separated and used differently. The MAGNITUDE
     * of the accumulation becomes coverage — how much ink is on the paper here —
     * and the HUE becomes the ink itself, normalised so a bright pixel and a dim
     * pixel of the same stream print the same colour at different strengths.
     * The result is the same composition drawn in violet ink rather than in
     * violet light, which keeps the streams' identity: blue leads, violet sits
     * in the middle, magenta trails, exactly as above.
     *
     * Inverting `dcol` was the cheaper option and it is wrong: it takes the hue
     * with it, and a blue field inverts to a gold one. */
    /* Prefixed names: main() already has an `a` (the blob's smin radius, above)
     * and GLSL ES has no block scope to hide behind, so a bare `a` here is a
     * redefinition — which fails the COMPILE, not the draw. That failure is
     * quiet by design: mount() returns null, `is-live` is never added, and the
     * CSS gradient underneath renders a perfectly plausible page with no field
     * in it at all. It cost a round trip; the names stay prefixed. */
    '  float inkMag = max(col.r, max(col.g, col.b));',
    '  vec3  inkHue = col / max(inkMag, 1e-4);',
    /* A lower exponent than the dark path's gamma, on purpose. Ink covering
     * paper is not the same curve as light adding to black: the dim tail that
     * reads as bloom on a dark ground is nearly invisible as coverage on a pale
     * one, so the midtones are lifted to give the streams the same presence in
     * both. Tuned against side-by-side screenshots, not by eye on one. */
    '  float inkA = clamp(pow(inkMag / (1.0 + inkMag), 0.72) * 0.95, 0.0, 1.0);',
    '  vec3 lcol = mix(vec3(0.957, 0.957, 0.969), inkHue * 0.56, inkA);',

    '  col = mix(dcol, lcol, uLight);',

    /* ---- composite the subject ------------------------------------------ */
    /* Screen-space ray. The subject drifts up and shrinks as the page scrolls,
     * so it hands the stage to the copy instead of following it down. */
    '  vec2 sp = (gl_FragCoord.xy - 0.5 * uRes) / uRes.y;',
    '  sp.x -= 0.46;',

    /* The subject travels UP the screen as the page scrolls, and when it leaves
     * the top it comes back from the bottom rather than simply being gone.
     *
     * It used to be a single monotonic drift — 0.6 units over the whole page —
     * which meant the subject inched upward, shrank, and then sat off to one
     * side for the last three sections doing nothing. Scrolling further did not
     * bring anything back, so the composition emptied out exactly where the copy
     * was asking to be read against something.
     *
     * `sawtooth` is written to be continuous at the wrap: it starts at 0 when
     * nothing is scrolled, climbs to +HALF, and rejoins at -HALF. The wrap is
     * only invisible because HALF exceeds half the viewport plus the subject's
     * own radius — at the moment it jumps, the subject is entirely past the top
     * edge, and it re-enters from below still rising. Shrink HALF and the shape
     * will visibly teleport.
     *
     * TRAVEL is an exact multiple of the period, so the bottom of the page
     * frames the subject exactly as the top does.
     */
    /* HALF has to exceed half the viewport plus the subject's own screen
     * radius, or the wrap is visible as a jump. Derived rather than guessed:
     *
     *   mapBlob is ten spheres with centres bounded by 0.40 per axis and radii
     *   up to 0.34, smin-blended with k=0.26. Worst case |centre| is
     *   0.40*sqrt(3)=0.693, so the world radius is under 0.693+0.34+0.07=1.10.
     *   The camera puts the object plane 6.4 away and scales by 3.20, so screen
     *   radius = 1.10 * 3.20/6.4 = 0.55 viewport heights, and `sp *= zoom`
     *   only ever divides that further.
     *
     *   Fully clear of the top edge therefore needs 0.5 + 0.02 + 0.55 = 1.07.
     *
     * 1.25 leaves a margin over a bound that already assumes all three
     * Lissajous axes peak at once, which they never do. If the shape or the
     * camera changes, redo this — a too-small HALF does not look like a bug,
     * it looks like a stutter. */
    '  const float HALF   = 1.25;',   // half a cycle, in viewport heights
    '  const float PERIOD = 2.50;',   // 2 * HALF
    '  const float TRAVEL = 7.50;',   // 3 * PERIOD — three passes over the page
    '  float march = clamp(uScroll, 0.0, 1.0) * TRAVEL;',
    '  float sawtooth = mod(march + HALF, PERIOD) - HALF;',
    '  sp.y += 0.02 - sawtooth;',
    /* A gentler recede than before. The subject now returns, so it no longer
     * has to shrink its way out of the copy's way — and at the old 1.85 the
     * later passes came back as a speck. */
    '  float zoom = mix(1.0, 1.35, clamp(uScroll, 0.0, 1.0));',
    '  sp *= zoom;',

    '  vec3 ro = vec3(0.0, 0.0, 6.4);',
    '  vec3 rd = normalize(vec3(sp, -3.20));',
    '  float bt = uTime * uSpeed * 0.85;',

    '  float dist = 0.0;',
    '  float hit = 0.0;',
    '  for (int i = 0; i < 64; i++){',
    '    vec3 pos = ro + rd * dist;',
    '    float dd = mapBlob(pos, bt);',
    '    if (dd < 0.0016){ hit = 1.0; break; }',
    '    dist += dd * 0.92;',
    '    if (dist > 8.0) break;',
    '  }',

    '  if (hit > 0.5){',
    '    vec3 pos = ro + rd * dist;',
    '    vec3 n = blobNormal(pos, bt);',
    /* A white matte subject: one key from upper left, a cool fill from below so
     * the shadow side never goes to black, and a narrow rim that separates the
     * silhouette from the field behind it. */
    '    vec3 key  = normalize(vec3(-0.55, 0.80, 0.62));',
    '    vec3 fill = normalize(vec3(0.65, -0.35, 0.40));',
    '    float kd = max(dot(n, key), 0.0);',
    '    float fd = max(dot(n, fill), 0.0);',
    '    float rim = pow(1.0 - max(dot(n, -rd), 0.0), 2.6);',
    /* The subject is white against a near-black field and mid-grey against
     * paper. It is the same material either way — what changes is that a white
     * mass on a white page has no silhouette, and the silhouette is the whole
     * of what this shape contributes. The rim is nearly withdrawn in light for
     * the same reason the ribbons are: a lift on an already-pale ground reads as
     * a smudge rather than as an edge. */
    '    vec3 mat = mix(vec3(0.97, 0.97, 0.98), vec3(0.60, 0.61, 0.69), uLight);',
    '    vec3 lit = mat * (mix(0.34, 0.30, uLight) + mix(0.86, 0.80, uLight) * kd)',
    '             + vec3(0.26, 0.28, 0.36) * fd * 0.24',
    '             + vec3(0.58, 0.62, 0.95) * rim * mix(0.26, 0.10, uLight);',
    /* Distance fade so the mass sits IN the field rather than pasted on it. */
    '    float fog = exp(-max(dist - 5.2, 0.0) * 0.50);',
    '    lit = mix(col, lit, clamp(fog, 0.0, 1.0));',
    '    col = lit;',
    '  }',

    '  gl_FragColor = vec4(col, 1.0);',
    '}'
  ].join('\n');

  function compile(gl, type, src) {
    var sh = gl.createShader(type);
    gl.shaderSource(sh, src);
    gl.compileShader(sh);
    if (!gl.getShaderParameter(sh, gl.COMPILE_STATUS)) {
      /* Logged, not shown. A shader that will not compile means the page loses
       * its decoration, which is not something to tell a visitor about. */
      if (global.console) console.warn('portal-field: shader did not compile:',
        gl.getShaderInfoLog(sh));
      return null;
    }
    return sh;
  }

  function mount(canvas, opts) {
    opts = opts || {};

    var reduced = global.matchMedia &&
      global.matchMedia('(prefers-reduced-motion: reduce)').matches;

    var gl = canvas.getContext('webgl', { antialias: false, alpha: false, depth: false })
          || canvas.getContext('experimental-webgl', { antialias: false, alpha: false, depth: false });
    if (!gl) return null;

    var vs = compile(gl, gl.VERTEX_SHADER, VERT);
    var fs = compile(gl, gl.FRAGMENT_SHADER, FRAG);
    if (!vs || !fs) return null;

    var prog = gl.createProgram();
    gl.attachShader(prog, vs);
    gl.attachShader(prog, fs);
    gl.linkProgram(prog);
    if (!gl.getProgramParameter(prog, gl.LINK_STATUS)) {
      if (global.console) console.warn('portal-field: link failed:', gl.getProgramInfoLog(prog));
      return null;
    }
    gl.useProgram(prog);

    var buf = gl.createBuffer();
    gl.bindBuffer(gl.ARRAY_BUFFER, buf);
    gl.bufferData(gl.ARRAY_BUFFER,
      new Float32Array([-1,-1, 3,-1, -1,3]), gl.STATIC_DRAW);
    var loc = gl.getAttribLocation(prog, 'p');
    gl.enableVertexAttribArray(loc);
    gl.vertexAttribPointer(loc, 2, gl.FLOAT, false, 0, 0);

    var uRes    = gl.getUniformLocation(prog, 'uRes');
    var uTime   = gl.getUniformLocation(prog, 'uTime');
    var uSpeed  = gl.getUniformLocation(prog, 'uSpeed');
    var uScale  = gl.getUniformLocation(prog, 'uScale');
    var uBright = gl.getUniformLocation(prog, 'uBright');
    var uScroll = gl.getUniformLocation(prog, 'uScroll');
    var uLight  = gl.getUniformLocation(prog, 'uLight');

    gl.uniform1f(uSpeed,  opts.speed  === undefined ? 1.0 : opts.speed);
    gl.uniform1f(uScale,  opts.scale  === undefined ? 1.0 : opts.scale);
    gl.uniform1f(uBright, opts.brightness === undefined ? 1.0 : opts.brightness);

    /* Capped device pixel ratio. This is a full-viewport fragment shader, and at
     * DPR 3 on a large display it costs several times what it does at 1.5 for a
     * difference nobody can see in a soft gradient. */
    function resize() {
      var dpr = Math.min(global.devicePixelRatio || 1, 1.5);
      var w = Math.max(1, Math.floor(canvas.clientWidth  * dpr));
      var h = Math.max(1, Math.floor(canvas.clientHeight * dpr));
      if (canvas.width !== w || canvas.height !== h) {
        canvas.width = w; canvas.height = h;
        gl.viewport(0, 0, w, h);
      }
      gl.uniform2f(uRes, canvas.width, canvas.height);
    }

    /* Scroll drives the subject. Read once per frame from a value the scroll
     * listener only stores — reading layout inside the listener would force a
     * reflow on every scroll event. */
    var scroll = 0;
    function onScroll() {
      var max = (document.documentElement.scrollHeight - global.innerHeight) || 1;
      scroll = Math.min(Math.max(global.scrollY / max, 0), 1);
    }
    global.addEventListener('scroll', onScroll, { passive: true });
    global.addEventListener('resize', onScroll);
    onScroll();

    var running = true, raf = 0, t0 = (global.performance || Date).now();

    function frame(now) {
      if (!running) return;
      resize();
      gl.uniform1f(uTime, (now - t0) / 1000);
      gl.uniform1f(uScroll, scroll);
      gl.drawArrays(gl.TRIANGLES, 0, 3);
      raf = global.requestAnimationFrame(frame);
    }

    /* The held frame, as its own function.
     *
     * Under `prefers-reduced-motion` this is the ONLY draw that ever happens —
     * there is no loop to pick a changed uniform up on its next pass. A theme
     * change would leave the field in the old palette until the page was
     * reloaded, which is exactly the reader who asked not to be surprised by
     * things moving. So setTheme calls this. */
    function paint() {
      resize();
      gl.uniform1f(uTime, 12.0);
      gl.uniform1f(uScroll, scroll);
      gl.drawArrays(gl.TRIANGLES, 0, 3);
    }

    /* Registered before the first draw so the very first frame is already in the
     * right palette; ForgeTheme calls back immediately on registration. */
    function setTheme(light) {
      gl.uniform1f(uLight, light ? 1.0 : 0.0);
      if (reduced) paint();
    }
    if (global.ForgeTheme) {
      global.ForgeTheme.onChange(setTheme);
    } else {
      /* theme.js is loaded first on every page that mounts this, so this is the
       * "somebody reused the module elsewhere" path, not a live one. Dark is the
       * design's own ground, so it is the safe assumption. */
      setTheme(false);
    }

    if (reduced) {
      /* One frame, held. The composition is the point; the motion is not, and a
       * reader who asked for less of it should still get the picture. */
      paint();
    } else {
      raf = global.requestAnimationFrame(frame);
    }

    /* Stop when the tab is hidden. A background shader burning a GPU behind
     * another window is a laptop fan for nothing. */
    document.addEventListener('visibilitychange', function () {
      if (document.hidden) {
        running = false;
        if (raf) global.cancelAnimationFrame(raf);
      } else if (!reduced && !running) {
        running = true;
        t0 = (global.performance || Date).now() - 12000;
        raf = global.requestAnimationFrame(frame);
      }
    });

    return {
      stop: function () { running = false; if (raf) global.cancelAnimationFrame(raf); },
      setTheme: setTheme
    };
  }

  global.PortalField = { mount: mount };

  /* Auto-mount so the template needs no inline script, which the CSP forbids. */
  function auto() {
    var c = document.getElementById('field');
    if (!c) return;
    var live = mount(c, { speed: 0.55, scale: 1.0, brightness: 1.0 });
    /* Only reveal the canvas once it has actually rendered. If it never does,
     * the CSS gradient underneath is the design rather than a fallback. */
    if (live) c.classList.add('is-live');
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', auto);
  } else { auto(); }

})(window);
