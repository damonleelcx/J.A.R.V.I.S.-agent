/* FORGE — behaviour for the pre-console pages.
 *
 * Served as a file rather than written inline. The reason is not style: the
 * application's Content-Security-Policy is `script-src 'self'`, which blocks
 * inline <script> outright. An earlier version of these pages carried their
 * script inline, and the browser silently refused to run it — the verify
 * button did nothing and the reset form fell back to a native GET submit that
 * dropped the token from the URL. Nothing in the test suite could see it,
 * because the failure lives in the browser's policy engine.
 *
 * See workflow/bugfix/2026-09-02-csp-blocked-inline-page-script.md.
 *
 * No framework and no build step: these pages are opened from a mail client,
 * often on a phone, sometimes on a network that blocks third-party origins.
 */
(function () {
  'use strict';

  var root = document.querySelector('[data-page]');
  if (!root) return;

  var page = root.getAttribute('data-page');
  var token = root.getAttribute('data-token') || '';
  var note = document.getElementById('note');

  function show(kind, html) {
    if (!note) return;
    note.className = 'note ' + kind;
    note.innerHTML = html;
  }

  /* Render a server error using the API's own vocabulary. The message and the
   * remedy come from the central error registry, so the page never invents its
   * own wording for a failure the server already described. */
  function showAPIError(body) {
    var e = (body && body.error) || {};
    var detail = (e.details && e.details.detail) || '';
    show('bad',
      '<strong>' + escapeHTML(e.message || 'That did not work.') + '</strong><br>' +
      (detail ? escapeHTML(detail) + '<br>' : '') +
      escapeHTML(e.remedy || ''));
  }

  function escapeHTML(s) {
    return String(s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  function post(url, payload) {
    return fetch(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    }).then(function (r) {
      return r.json()
        .catch(function () { return {}; })
        .then(function (b) { return { ok: r.ok, body: b }; });
    });
  }

  /* ---- confirm email ---------------------------------------------------- */
  if (page === 'verify') {
    var vbtn = document.getElementById('go');
    if (!vbtn) return;

    vbtn.addEventListener('click', function () {
      vbtn.disabled = true;
      vbtn.textContent = 'Confirming…';
      post('/v1/auth/verify-email', { token: token }).then(function (res) {
        if (res.ok) {
          vbtn.classList.add('hidden');
          show('ok', 'Address confirmed. You can close this page and sign in.');
          return;
        }
        showAPIError(res.body);
        vbtn.disabled = false;
        vbtn.textContent = 'Try again';
      }).catch(function () {
        show('bad', 'Could not reach the server. Check your connection and try again.');
        vbtn.disabled = false;
        vbtn.textContent = 'Try again';
      });
    });
  }

  /* ---- set a new password ----------------------------------------------- */
  if (page === 'reset') {
    var form = document.getElementById('form');
    var pw = document.getElementById('pw');
    var pw2 = document.getElementById('pw2');
    var rbtn = document.getElementById('go');
    if (!form || !pw || !pw2 || !rbtn) return;

    form.addEventListener('submit', function (ev) {
      /* Without this the browser performs a native GET submit, which replaces
       * the query string and loses the token. */
      ev.preventDefault();

      if (pw.value !== pw2.value) {
        show('bad', 'The two passwords do not match.');
        return;
      }
      rbtn.disabled = true;
      rbtn.textContent = 'Setting…';

      post('/v1/auth/reset-password', { token: token, new_password: pw.value })
        .then(function (res) {
          if (res.ok) {
            form.classList.add('hidden');
            show('ok', 'Password updated. All devices have been signed out — ' +
                       'sign in again with your new password.');
            return;
          }
          showAPIError(res.body);
          rbtn.disabled = false;
          rbtn.textContent = 'Set password';
        }).catch(function () {
          show('bad', 'Could not reach the server. Check your connection and try again.');
          rbtn.disabled = false;
          rbtn.textContent = 'Set password';
        });
    });
  }
})();
