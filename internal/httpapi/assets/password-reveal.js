/* Show/hide for every password field on the page.
 *
 * # Why this is one file included by two pages
 *
 * The sign-in form lives in the workbench template and the reset form lives
 * under the shared foot template, and those load different scripts. Writing the
 * toggle twice would mean two behaviours drifting apart, so this wires itself to
 * whatever password inputs exist and is included by both.
 *
 * # Why a button and not a checkbox
 *
 * The control has to be reachable by keyboard and announced correctly, and it
 * is an action ("show this") rather than a setting that persists. aria-pressed
 * carries the state so a screen reader says "show password, pressed" instead of
 * leaving the user to guess whether the characters are currently visible.
 *
 * # What it deliberately does not do
 *
 * It never persists the revealed state — not across fields, not across page
 * loads. A password left visible because a previous session revealed it is a
 * shoulder-surfing hazard the user did not opt into. Every load starts hidden.
 */
(function () {
  'use strict';

  var EYE =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" ' +
    'stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
    '<path d="M1 12s4-8 11-8 11 8 11 8-4 8-11 8-11-8-11-8z"></path>' +
    '<circle cx="12" cy="12" r="3"></circle></svg>';

  var EYE_OFF =
    '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" ' +
    'stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">' +
    '<path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94"></path>' +
    '<path d="M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19"></path>' +
    '<path d="M14.12 14.12a3 3 0 1 1-4.24-4.24"></path>' +
    '<line x1="1" y1="1" x2="23" y2="23"></line></svg>';

  function wire(input) {
    if (input.dataset.reveal === 'on') return;   // idempotent
    input.dataset.reveal = 'on';

    var wrap = document.createElement('div');
    wrap.className = 'pw-wrap';
    input.parentNode.insertBefore(wrap, input);
    wrap.appendChild(input);

    var btn = document.createElement('button');
    btn.type = 'button';                  // never submits the form
    btn.className = 'pw-reveal';
    btn.setAttribute('aria-pressed', 'false');
    btn.setAttribute('aria-label', 'Show password');
    btn.title = 'Show password';
    btn.innerHTML = EYE;

    btn.addEventListener('click', function () {
      var shown = input.type === 'text';
      input.type = shown ? 'password' : 'text';
      btn.setAttribute('aria-pressed', shown ? 'false' : 'true');
      var label = shown ? 'Show password' : 'Hide password';
      btn.setAttribute('aria-label', label);
      btn.title = label;
      btn.innerHTML = shown ? EYE : EYE_OFF;
      /* Focus returns to the field with the caret at the end. Toggling type
       * moves the caret to position 0 in several browsers, and a user who
       * revealed the field to check the last few characters would otherwise
       * have to click back into it. */
      var end = input.value.length;
      input.focus();
      try { input.setSelectionRange(end, end); } catch (e) { /* number/email types throw */ }
    });

    wrap.appendChild(btn);
  }

  function wireAll() {
    var list = document.querySelectorAll('input[type="password"]');
    for (var i = 0; i < list.length; i++) wire(list[i]);
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', wireAll);
  } else {
    wireAll();
  }

  /* The sign-in form is inside a container that starts hidden and is revealed by
   * workbench.js, so the inputs exist at load. If a future page builds a form
   * after load, it can call this. */
  window.wirePasswordReveal = wireAll;
})();
