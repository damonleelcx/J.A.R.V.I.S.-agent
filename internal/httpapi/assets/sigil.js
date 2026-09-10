/* FORGE's mark, placed into the page as real SVG.
 *
 * # Why this exists instead of <img src="/v1/meta/sigil">
 *
 * persona.AvatarSVG deliberately carries no paint of its own. Every colour is a
 * class that resolves against avatar.css — .fa-wing-lit is var(--wing-lit),
 * .fa-ring is var(--gold) — and every one of the six states is an animation in
 * the same stylesheet. That is the right shape: it is how the mark follows the
 * theme without the server needing to know which theme is on screen.
 *
 * But an SVG loaded through <img> is an isolated document. It cannot see the
 * host page's stylesheet, so none of those classes resolve: the gradient stops
 * fell back to black, .fa-ring lost its stroke entirely (stroke has no initial
 * colour, so the ring was not drawn at all), and all six state animations were
 * dead. On the dark workbench header the result was a black silhouette on a
 * near-black ground — the mark had effectively vanished, and the state it is
 * there to report was not being reported in any theme.
 *
 * Inlining the same markup into the document fixes both at once, because the
 * page's stylesheet then applies to it. The server stays the single
 * implementation of the mark and of the state rules; this file only decides
 * where the markup lands.
 *
 * See docs/bugfix/2026-09-09-the-sigil-was-black-behind-an-img.md
 * Fence: TestSigilIsNeverConsumedAsAnImage in sigil_paint_fence_test.go — if a
 * caller goes back to <img>, the mark silently turns black again.
 */
window.ForgeSigil = (function () {
  'use strict';

  /* One in-flight request per distinct mark, keyed by what the URL varies on.
   * A page that cycles idle → thinking → idle fetches two marks, ever, and the
   * responses are cached for five minutes by the server on top of that. */
  var pending = {};

  function url(state, size) {
    return '/v1/meta/sigil?state=' + encodeURIComponent(state) + '&size=' + encodeURIComponent(size);
  }

  function markup(state, size) {
    var key = state + ':' + size;
    if (!pending[key]) {
      pending[key] = fetch(url(state, size)).then(function (r) {
        if (!r.ok) throw new Error('sigil ' + r.status);
        return r.text();
      }).catch(function (err) {
        /* Dropped rather than retried forever, and re-requestable: a mark that
         * failed to load must never be able to wedge the surface it sits on. */
        delete pending[key];
        throw err;
      });
    }
    return pending[key];
  }

  /* Fill one element with the mark. The element keeps its own classes, so
   * whatever sized and positioned it before still does. */
  function place(el, state, size) {
    if (!el) return Promise.resolve();
    return markup(state, size).then(function (svg) {
      el.innerHTML = svg;
    }).catch(function (err) {
      /* Left empty on failure. The alternative — a broken-image glyph — is
       * worse, and every surface using the mark states FORGE's name in text
       * beside it, so nothing becomes unreadable. */
      if (window.console && console.warn) console.warn('sigil failed to load', err);
    });
  }

  /* The placeholder, for callers that build a row as an HTML string and assign
   * it in one go. Hydrate the container afterwards.
   *
   * cls lands on the placeholder rather than on the SVG so that the caller's
   * existing layout rules (.goal .sig, and so on) keep applying to the same
   * element they always did. */
  function slot(state, size, cls) {
    /* The box is reserved at its final size so the row does not reflow when the
     * mark arrives — the fetch is fast, but a list that twitches once per row is
     * a worse result than the <img> this replaced. */
    return '<span class="' + (cls || '') + '" data-sigil="' + state +
           '" data-sigil-size="' + size + '" aria-hidden="true"' +
           ' style="display:inline-block;line-height:0;width:' + size + 'px;height:' + size + 'px"></span>';
  }

  function hydrate(root) {
    var slots = (root || document).querySelectorAll('[data-sigil]');
    for (var i = 0; i < slots.length; i++) {
      var el = slots[i];
      place(el, el.getAttribute('data-sigil'), el.getAttribute('data-sigil-size') || 24);
    }
  }

  return { place: place, slot: slot, hydrate: hydrate };
})();
