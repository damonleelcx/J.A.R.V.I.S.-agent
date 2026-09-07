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
  if (page === 'signup') {
    var sform = document.getElementById('form');
    var sname = document.getElementById('name');
    var semail = document.getElementById('email');
    var spw = document.getElementById('pw');
    var sbtn = document.getElementById('go');
    if (!sform || !sname || !semail || !spw || !sbtn) return;

    sform.addEventListener('submit', function (ev) {
      ev.preventDefault();
      sbtn.disabled = true;
      sbtn.textContent = 'Creating…';

      post('/v1/auth/sign-up', {
        email: semail.value.trim(),
        password: spw.value,
        display_name: sname.value.trim()
      }).then(function (res) {
        if (res.ok) {
          /* The form goes away on success. Leaving it would invite a second
           * submit, which fails as a duplicate and reads as though the first
           * one had not worked. */
          sform.classList.add('hidden');
          show('ok', 'Account created. Check your email for a link to confirm ' +
                     'the address — the account is not active until you open it.');
          return;
        }
        showAPIError(res.body);
        sbtn.disabled = false;
        sbtn.textContent = 'Create account';
      }).catch(function () {
        show('bad', 'Could not reach the server. Check your connection and try again.');
        sbtn.disabled = false;
        sbtn.textContent = 'Create account';
      });
    });
  }

  if (page === 'forgot') {
    var fform = document.getElementById('form');
    var femail = document.getElementById('email');
    var fbtn = document.getElementById('go');
    if (!fform || !femail || !fbtn) return;

    fform.addEventListener('submit', function (ev) {
      ev.preventDefault();
      fbtn.disabled = true;
      fbtn.textContent = 'Sending…';

      post('/v1/auth/forgot-password', { email: femail.value.trim() })
        .then(function (res) {
          if (res.ok) {
            /* The wording matches what the endpoint actually guarantees. It
             * answers identically whether or not the address has an account, so
             * claiming "we sent you a link" would both overstate the result and
             * turn this form into a way to test which addresses are registered. */
            fform.classList.add('hidden');
            show('ok', 'If that address has an account, a link to set a new ' +
                       'password is on its way. It works once and expires.');
            return;
          }
          showAPIError(res.body);
          fbtn.disabled = false;
          fbtn.textContent = 'Send the link';
        }).catch(function () {
          show('bad', 'Could not reach the server. Check your connection and try again.');
          fbtn.disabled = false;
          fbtn.textContent = 'Send the link';
        });
    });
  }

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
