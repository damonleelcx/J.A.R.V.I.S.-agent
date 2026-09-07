/* FORGE — which theme is in force, and how a reader changes it.
 *
 * # What this file is NOT responsible for
 *
 * It does not apply the theme on load. The server does that: it reads the
 * `forge_theme` cookie and renders `data-theme` onto <html> before the bytes
 * leave. That is not a stylistic choice — the application's CSP is
 * `script-src 'self'`, so the usual trick of a blocking inline script that
 * stamps the attribute before first paint is unavailable, and a theme applied
 * by a deferred external script arrives AFTER the page has already painted in
 * the wrong one. Every navigation would flash.
 *
 * So this file only handles the change: the click, the cookie that makes the
 * change survive the next navigation, and telling the parts of the interface
 * that are not CSS.
 *
 * # Why anything needs telling
 *
 * Three surfaces draw themselves and cannot inherit a custom property: the
 * landing page's WebGL field, the workbench's 3D viewport, and the voice orb.
 * They ask `ForgeTheme.isLight()` rather than each working it out from
 * `matchMedia` and the attribute separately — with four copies of that rule,
 * the first one to be edited becomes the odd one out and a page renders half in
 * each theme.
 *
 * # The three states
 *
 * `data-theme` absent is not "dark". It means "no explicit choice", and the
 * operating system decides — which is why `isLight()` falls through to
 * `matchMedia` rather than defaulting, and why a system change is announced
 * while no choice is stored.
 */
(function (global, doc) {
  'use strict';

  var COOKIE = 'forge_theme';
  var root = doc.documentElement;
  var listeners = [];

  /* The stored choice, or '' when there is none. Anything else on the attribute
   * is treated as no choice rather than trusted — it arrives from a cookie. */
  function chosen() {
    var v = root.getAttribute('data-theme');
    return (v === 'light' || v === 'dark') ? v : '';
  }

  function systemIsLight() {
    return !!(global.matchMedia && global.matchMedia('(prefers-color-scheme: light)').matches);
  }

  function isLight() {
    var c = chosen();
    return c ? c === 'light' : systemIsLight();
  }

  function announce() {
    var light = isLight();
    for (var i = 0; i < listeners.length; i++) {
      /* One renderer throwing must not leave the others in the old theme. */
      try { listeners[i](light); } catch (e) {
        if (global.console && console.warn) { console.warn('forge.theme.listener_failed', e); }
      }
    }
  }

  /* Registered listeners are called immediately with the current theme, so a
   * renderer that mounts late does not have to ask separately. */
  function onChange(fn) {
    if (typeof fn !== 'function') { return; }
    listeners.push(fn);
    try { fn(isLight()); } catch (e) {
      if (global.console && console.warn) { console.warn('forge.theme.listener_failed', e); }
    }
  }

  function remember(v) {
    /* Not HttpOnly — this line is the only writer, and a display preference is
     * not a credential. Lax rather than Strict so following a link from an email
     * into a page still arrives in the theme the reader chose. */
    var secure = global.location.protocol === 'https:' ? '; Secure' : '';
    doc.cookie = COOKIE + '=' + v + '; Path=/; Max-Age=31536000; SameSite=Lax' + secure;
  }

  function set(v) {
    root.setAttribute('data-theme', v);
    remember(v);
    label();
    announce();
  }

  function toggle() { set(isLight() ? 'dark' : 'light'); }

  /* The button's accessible name has to say what pressing it DOES, not what is
   * currently true — "Light theme" on a button is ambiguous about which of the
   * two it means. The glyph swap itself is CSS; see shell.css. */
  function label() {
    var next = isLight() ? 'dark' : 'light';
    var text = 'Switch to the ' + next + ' theme';
    var btns = doc.querySelectorAll('[data-theme-toggle]');
    for (var i = 0; i < btns.length; i++) {
      btns[i].setAttribute('aria-label', text);
      btns[i].setAttribute('title', text);
    }
  }

  function wire() {
    var btns = doc.querySelectorAll('[data-theme-toggle]');
    for (var i = 0; i < btns.length; i++) {
      btns[i].addEventListener('click', toggle);
    }
    label();
  }

  if (doc.readyState === 'loading') {
    doc.addEventListener('DOMContentLoaded', wire);
  } else {
    wire();
  }

  /* Follow the system while — and only while — nothing has been chosen. CSS
   * already re-resolves on its own; this exists for the three canvases, which
   * would otherwise stay in the old palette until the page was reloaded. */
  if (global.matchMedia) {
    var mq = global.matchMedia('(prefers-color-scheme: light)');
    var onSystem = function () { if (!chosen()) { label(); announce(); } };
    if (mq.addEventListener) { mq.addEventListener('change', onSystem); }
    else if (mq.addListener) { mq.addListener(onSystem); }
  }

  global.ForgeTheme = {
    isLight: isLight,
    onChange: onChange,
    toggle: toggle,
    set: set
  };
})(window, document);
