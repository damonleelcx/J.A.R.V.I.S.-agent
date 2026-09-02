# CSP blocked the page script, so email links did nothing

**Date:** 2026-09-02 · **Phase:** 1 (identity) · **Severity:** high — both
email-driven flows were unusable in a browser · **Owner:** httpapi

## Symptom

The two pages a user reaches from email — *Confirm your email* and
*Set a new password* — rendered correctly and did nothing.

- **Confirm your email:** pressing *Confirm address* had no effect at all. No
  request, no error, no change on screen.
- **Set a new password:** submitting the form performed a **native GET submit**.
  Because the inputs carry no `name` attributes, the browser replaced the query
  string with an empty one, dropping the token. The page reloaded showing
  *"This link is missing its token."* — so the user's own attempt to reset their
  password destroyed the link they were using.

## Why nothing caught it

Every layer below the browser was correct and tested:

- `identity.Service` had passing integration tests for verification and reset.
- The HTTP handlers returned correct JSON for both endpoints.
- The templates rendered the right HTML.
- `curl` against `/v1/auth/verify-email` worked perfectly.

The failure lived entirely in the **browser's policy engine**. No Go test can
observe it. It appeared the first time the page was opened in a real browser.

## Root cause

`SecurityHeaders` sets:

```
Content-Security-Policy: ... script-src 'self' ...
```

`script-src 'self'` permits scripts **loaded from the origin** and forbids
**inline** `<script>` blocks. Both pages carried their behaviour inline, so the
browser refused to execute it:

```
Executing inline script violates the following Content Security Policy
directive 'script-src 'self''. ... The action has been blocked.
```

The two halves were written at different moments — the strict CSP in
`middleware.go`, the inline scripts in `pages.go` — and each is correct on its
own. Nothing connected them.

## Fix

The script moved out of the template into `internal/httpapi/assets/pages.js`,
served from `GET /assets/{file}` out of the embedded FS. Page-specific inputs
travel as data attributes on the container element:

```html
<main class="panel" data-page="verify" data-token="…">
```

**Rejected alternatives, and why:**

| Option | Rejected because |
|---|---|
| Add `'unsafe-inline'` to `script-src` | Removes exactly the protection the header exists for, across the whole application, to fix two pages. |
| CSP nonce per request | Correct, but requires threading a per-request value from middleware into template data. Real added machinery for no benefit over serving a file. |
| Inline the token into script source | Puts a credential inside executable text, where one escaping mistake becomes script injection. A data attribute is inert. |

## Regression fence

`TestPagesCarryNoInlineScript` (`internal/httpapi/pages_test.go`) renders every
page and fails if any contains an executable inline `<script>` block.
`TestCSPForbidsInlineScript` asserts the policy still lacks `'unsafe-inline'` in
`script-src` — so the fence stays meaningful. Together they close the loop: the
policy cannot be weakened silently, and the pages cannot drift back to inline.

Drilled both directions: re-inlining a script fails the first test; adding
`'unsafe-inline'` to the policy fails the second.

## Two measurement traps hit while verifying the fix

Recorded because they nearly produced a wrong conclusion twice:

1. **Stale console messages.** After the fix, `read_console_messages` still
   returned the CSP violation — from the *earlier* page load. Read as current, it
   said the fix had failed.
2. **Click coordinates that missed.** A ref-based click reported success while
   landing outside the button, so the page correctly did nothing and looked
   broken. Dispatching `btn.click()` directly proved the handler was attached.

Both times the code was already correct and the instrument was wrong. Ground
truth came from the database: `forge_users.email_verified_at` was set.
