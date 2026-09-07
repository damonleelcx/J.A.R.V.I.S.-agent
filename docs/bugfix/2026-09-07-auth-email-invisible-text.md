# Auth email arrived with its text invisible

**Status:** fixed
**Found:** 2026-09-07, verifying auth mail on the first `forge.heros-agent.space` deploy.

## Symptom

The verification email was delivered — the relay logged
`status=sent (250 …)` and it reached the inbox — but the two most important
lines in it were unreadable:

- `Hello Damon,`
- `Confirm this address to finish setting up your FORGE account:`

Everything else rendered correctly: the FORGE wordmark, the **Confirm address**
button, the fallback URL, the expiry note and the footer.

## Why this was easy to miss

Delivery and readability are different facts, and every signal available said
delivery. `signup_completed` logged `verification_sent: true`, `forge.mail.sent`
logged the recipient, and postfix logged `dsn=2.0.0, status=sent`. All three
were true. None of them says anything about whether a human can read the result.

## Root cause

The template is dark-themed: a dark card with light text. The text colour was
declared once, on `body`:

    body { …; color:#eef1f6; }
    p    { margin:0 0 14px; }        <-- no colour of its own

**Gmail rewrites `<body>` into a `<div>`**, so the `body` rule never reaches the
content. Elements carrying their own class colour were unaffected — `.mark`,
`.btn`, `.url` and `.muted` all declare one — but a plain `<p>` had nothing to
fall back to except Gmail's default text colour, which is dark, on a dark card.

That is precisely the split seen in the screenshot: every visible line had a
class with a colour, and both invisible lines were the only unclassed `<p>`
tags in the message.

## Fix

Declare the colour on selectors that survive the rewrite — `.card` and `p` —
and keep the `body` rule for clients that do honour it.

Affects all three mail templates, because they share `wrapHTML`.

## Regression test

`TestPlainParagraphsCarryTheirOwnColour` in `internal/mail/mail_test.go`.

Verified to bite: reintroducing exactly the original defect (removing `color:`
from the `p` rule) fails the test with the reason stated; restoring it passes.

    GOWORK=off go test ./internal/mail/ -run TestPlainParagraphsCarryTheirOwnColour -count=1

Note `GOWORK=off` — this module is not in the parent directory's `go.work`, and
without it the test reports a setup failure rather than a result.

## What this does not cover

The test asserts the stylesheet declares a colour where it must. It cannot
assert that a given client renders it: that needs a real send. The check that
actually caught this was looking at the delivered mail, and nothing cheaper
would have.
