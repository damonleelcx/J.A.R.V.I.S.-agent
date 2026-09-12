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
    /* The free RECTANGLE the mass is allowed to occupy, measured from the page's
     * own layout and handed in: xy is its centre as an offset from the centre of
     * the screen, zw its half-width and half-height, all in viewport HEIGHTS
     * because that is the unit the ray below works in.
     *
     * See measureBand(). The mass must never touch the figure, and where the
     * room is depends on the shape of the window: on a wide one it is the gap
     * beside her, on a phone it is the space above her. A tuned offset is
     * correct at one aspect ratio and wrong at the next one somebody opens.
     *
     * An axis that is not constrained is passed as a large half-extent rather
     * than as a flag, so the arithmetic below has no special case: the wide
     * layout leaves the vertical unconstrained and its orbit is unchanged. */
    'uniform vec4  uBand;',

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
    /* Left of centre, and it used to be right of centre.
     *
     * The mass was parked in the page's empty half (sp.x -= 0.46) because that
     * half WAS empty: the copy column is 560px on the left and there was
     * nothing on the right for it to collide with. The landing page now stands
     * FORGE's figure there, so that offset put a raymarched white mass directly
     * behind a white figure — two subjects in one place, each spoiling the
     * other's silhouette.
     *
     * The mass and the figure may not overlap, so the mass no longer chooses
     * where it goes: it is given the room that is left. uBand is the gap between
     * the copy column and the figure, measured from the DOM, and the two lines
     * below put the mass in the middle of it and shrink it until it fits.
     *
     * Two other arrangements were tried and are recorded here so they are not
     * tried again: staging them in depth, with the mass behind her shoulder and
     * a backlight keeping her silhouette off it — they still touch, which this
     * page does not want — and parking the mass on the left, where the section
     * scrim turns it into a grey lump under the display type. Both were
     * judgement calls about where a thing looks best. This is an arithmetic one
     * about where it FITS, which is the only kind that stays true on a window
     * nobody tested.
     *
     * When the band is too narrow to hold a mass worth drawing, it is not drawn
     * at all — see the guard below. That is deliberate: at those widths the
     * page has a copy column and a figure and no room for a third thing, and a
     * mass squeezed into the last eighty pixels reads as a rendering fault. */
    '  sp -= uBand.xy;',

    /* The subject ORBITS as the page scrolls: it rises, passes behind, comes
     * back up from below, and ends where it started. Exactly one revolution
     * over the length of the page.
     *
     * # Two things this replaces, and why the second was worse
     *
     * First it was a monotonic drift — 0.6 units up over the whole page. The
     * subject inched upward, shrank, and then sat off to one side for the last
     * three sections doing nothing, so the composition emptied out exactly
     * where the copy was asking to be read against something.
     *
     * Then it was a sawtooth: travel up, wrap, re-enter from the bottom, three
     * times over. The wrap was arranged to happen while the subject was fully
     * off screen, and that reasoning was sound as far as it went — but a wrap
     * is a TELEPORT, and putting a teleport out of frame does not make it
     * continuous, it only makes it unwitnessed. Nothing connected the exit to
     * the arrival. Three identical passes, same size and same column each time,
     * read as three copies of the shape stacked down the page rather than as
     * one shape going anywhere. Reported from a real browser; the arithmetic
     * had been checked and the arithmetic was not the thing that was wrong.
     *
     * # Why a circle fixes what a bigger margin could not
     *
     * The path is closed and every point on it is differentiable, so there is
     * no instant to catch. Height and distance run a quarter turn apart, which
     * is what makes it read as ONE object on a journey rather than a repeat:
     * the subject is never twice at the same height AND the same size, so there
     * is no frame that looks like a frame you already saw.
     *
     *   quarter turn   high, and half way out
     *   half turn      back at centre height, at its furthest — this is the
     *                  pass BEHIND, and it is what stops the return reading as
     *                  the same object simply sliding back down
     *   three quarters low, coming forward again
     *   full turn      home, and the foot of the page frames it as the head did
     *
     * RISE is deliberately under the 1.07 that would carry it off the top edge.
     * Leaving it partly in frame is the point: the previous version's gap was
     * the composition going empty, and an orbit that hides has reintroduced it.
     */
    '  const float TAU  = 6.28318530718;',
    '  const float RISE = 0.78;',   // peak height, in viewport heights
    /* The orbit is clipped to the band's height for the same reason the size is
     * clipped to its width: on a phone the mass is given the space ABOVE the
     * figure, and an orbit that ignored that would carry it down through her at
     * three quarters of a turn. On a wide screen the vertical is unconstrained,
     * uBand.w is large, and this resolves to RISE exactly as before. */
    '  float rise = min(RISE, uBand.w * 0.9);',
    '  const float AWAY = 0.62;',   // how much further it is at the back of the turn
    /* SPAN is the mass's apparent half-width at zoom 1, in viewport heights.
     * The metaballs travel within 0.40 world units of the origin and blend out
     * to about 0.75; the camera sits at z 6.4 looking down -3.20, so a world
     * radius r lands at about r/2 on screen. Rounded up, because the shape
     * breathes and a bound that is right on average is wrong half the time. */
    '  const float SPAN = 0.40;',
    /* Shrink until it fits the band's TIGHTER axis, never grow to fill it: on a
     * wide screen there is room for the mass at its natural size, and stretching
     * it to the width of the gap would make the composition depend on the
     * window. */
    '  float room = min(uBand.z, uBand.w);',
    '  float fit = max(1.0, SPAN / max(room, 0.001));',
    '  float turn = clamp(uScroll, 0.0, 1.0) * TAU;',
    '  sp.y += 0.02 - rise * sin(turn);',
    /* A quarter turn behind the height, so the extremes do not coincide. Larger
     * zoom is further away: sp is the ray's screen offset, so scaling it up
     * widens the view and the subject occupies less of it. */
    '  float zoom = fit + AWAY * 0.5 * (1.0 - cos(turn));',
    '  sp *= zoom;',

    /* No band, no mass. Below about 0.10 viewport heights of half-width the
     * mass would be a pebble wedged between the copy and the figure, and the
     * raymarch below is the expensive half of this shader — so this skips the
     * work as well as the drawing. */
    '  if (room > 0.10) {',
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
    var uBand   = gl.getUniformLocation(prog, 'uBand');

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

    /* How much room the mass is allowed, measured from the page rather than
     * assumed by the shader.
     *
     * # Why this is measured and not a constant
     *
     * The mass may not touch the figure, and the gap between the copy column
     * and the figure is not fixed: her width comes from her height, so the gap
     * depends on the window's height as much as its width — and she is not
     * drawn at all below a breakpoint, at which point the whole right side is
     * free again. Every one of those is a layout fact, and layout facts belong
     * to the layout. The shader is told the answer.
     *
     * # Why offsetLeft/offsetWidth and not getBoundingClientRect
     *
     * The figure carries a scroll-driven transform that scales her down as the
     * reader goes. getBoundingClientRect would report the SCALED box, so the
     * band measured half way down the page would be wider than the one at the
     * top — and the mass sized to it would overlap her the moment somebody
     * scrolled back up. offset* are layout boxes and ignore transforms, which
     * is exactly the guarantee this needs. A hidden figure has offsetWidth 0
     * and is treated as absent.
     *
     * # Units
     *
     * Viewport heights, and the centre as an offset from the middle of the
     * screen, because that is the frame the ray in the shader works in. Note the
     * vertical sign: the shader's y runs UP from the middle of the screen and
     * the page's runs DOWN from the top, so the two disagree and the conversion
     * below is where that is settled, once.
     */
    var GUTTER = 18;    // px of air on each side, so nothing ever looks tangent
    var UNBOUNDED = 9;  // viewport heights: "this axis constrains nothing"

    function measureBand() {
      var vw = global.innerWidth || 1, vh = global.innerHeight || 1;
      var left = 0, right = vw, top = null, bottom = null;
      if (opts.band) {
        var b = opts.band();
        if (b) {
          left = b.left + GUTTER;
          right = b.right - GUTTER;
          if (b.top !== null && b.top !== undefined) top = b.top + GUTTER;
          if (b.bottom !== null && b.bottom !== undefined) bottom = b.bottom - GUTTER;
        }
      }
      var cx = ((left + right) / 2 - vw / 2) / vh;
      var halfW = Math.max(right - left, 0) / 2 / vh;

      var cy = 0, halfH = UNBOUNDED;
      if (top !== null && bottom !== null) {
        cy = (vh / 2 - (top + bottom) / 2) / vh;
        halfH = Math.max(bottom - top, 0) / 2 / vh;
      }
      gl.uniform4f(uBand, cx, cy, halfW, halfH);
    }

    /* Scroll drives the subject. Read once per frame from a value the scroll
     * listener only stores — reading layout inside the listener would force a
     * reflow on every scroll event. */
    var lastW = 0, lastH = 0;
    function onViewport() {
      if (global.innerWidth === lastW && global.innerHeight === lastH) return;
      lastW = global.innerWidth;
      lastH = global.innerHeight;
      measureBand();
    }
    onViewport();

    var scroll = 0;
    function onScroll() {
      var max = (document.documentElement.scrollHeight - global.innerHeight) || 1;
      scroll = Math.min(Math.max(global.scrollY / max, 0), 1);
    }
    global.addEventListener('scroll', onScroll, { passive: true });
    global.addEventListener('resize', onScroll);
    global.addEventListener('resize', onViewport);
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
    var live = mount(c, {
      speed: 0.55,
      scale: 1.0,
      brightness: 1.0,
      /* The room left for the mass. Which elements matter is the PAGE's
       * knowledge, so it is stated here rather than inside the field module —
       * the module is told a pair of numbers and knows nothing about this
       * page's class names.
       *
       * # The two exclusions are not the same kind
       *
       * The FIGURE is hard: the mass may not touch her at any size of window,
       * which is the whole reason this is measured instead of tuned.
       *
       * The COPY is soft. Type over the mass is not a collision, it is what the
       * per-section scrim in home.css exists to handle, and it is what this page
       * did before the figure arrived.
       *
       * # Which way the room runs depends on the window
       *
       * On a wide window the free room is the GAP BESIDE her and the vertical is
       * unconstrained, so the orbit is untouched.
       *
       * On a narrow one there is no gap beside anything — the copy is full-bleed
       * and she stands at the foot of the first screen — so the free room is the
       * space ABOVE her, and the vertical is what constrains both her size and
       * her orbit. Treating the layout as one horizontal interval is what left a
       * phone with neither of them visible: no gap beside the copy meant the
       * mass fell back to the whole width, where the scrim hid it, and the
       * figure was simply not drawn.
       */
      band: function () {
        var i, copyRight = 0;
        var secs = document.querySelectorAll('.sec');
        for (i = 0; i < secs.length; i++) {
          copyRight = Math.max(copyRight, secs[i].offsetLeft + secs[i].offsetWidth);
        }
        var fig = document.querySelector('.home-figure');
        var drawn = !!(fig && fig.offsetWidth);

        /* How wide the gap beside the figure has to be before the mass is put in
         * it rather than sent above her.
         *
         * Well above the shader's own "not worth drawing" cutoff, and for a
         * different reason: the shader's floor is about a mass too small to be
         * anything, while this is about a mass too small to be GOOD. At 1100px
         * the gap measures a little over 200px, which passed a lower threshold
         * and rendered the mass as a dark pebble wedged between the copy and
         * her. */
        var vh = global.innerHeight || 1;
        var gap = (drawn ? fig.offsetLeft : global.innerWidth) - copyRight;

        if (gap >= 0.34 * vh) {
          /* Beside her, floor to ceiling. */
          return { left: copyRight, right: drawn ? fig.offsetLeft : global.innerWidth };
        }
        if (drawn) {
          /* Above her: the full width, from the top of the window down to the
           * top of her layout box. offsetTop rather than a rect for the same
           * reason as offsetLeft — her transform must not move the band. */
          return { left: 0, right: global.innerWidth, top: 0, bottom: fig.offsetTop };
        }
        /* She is not drawn: the mass has the window, as it did before her. */
        return { left: 0, right: global.innerWidth };
      }
    });
    /* Only reveal the canvas once it has actually rendered. If it never does,
     * the CSS gradient underneath is the design rather than a fallback. */
    if (live) c.classList.add('is-live');
  }
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', auto);
  } else { auto(); }

})(window);
