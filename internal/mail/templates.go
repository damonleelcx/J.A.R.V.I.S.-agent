package mail

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// The transactional templates.
//
// House style, and the reasons for it:
//
//   - The link is stated as a full URL in the plain-text part, never hidden
//     behind "click here". A recipient must be able to see where a link goes
//     before following it, and a security-conscious one will refuse otherwise.
//   - The expiry is always stated in the body. "This link has expired" is a
//     dead end if the user was never told there was a clock.
//   - Every message says what to do if it was not expected. Unrequested
//     transactional mail is the first signal of an account takeover, and the
//     recipient is the only person positioned to notice.
//   - The tone is FORGE's: direct, unpadded, no exclamation marks, no
//     manufactured warmth. It is a tool reporting a fact.

// VerificationEmail builds the address-confirmation message.
func VerificationEmail(toEmail, displayName, verifyURL string, expiresIn time.Duration) *Message {
	name := greetingName(displayName)
	window := humanDuration(expiresIn)

	text := fmt.Sprintf(`%s

Confirm this address to finish setting up your FORGE account:

%s

This link works once and expires in %s.

If you did not create a FORGE account, you can ignore this message — no account
is active until this link is opened. If you keep receiving these, someone may be
entering your address by mistake or on purpose; replying to tell us is useful.

— FORGE
This is an automated message from an AI engineering system. Nobody is reading a reply inbox.`,
		name, verifyURL, window)

	return &Message{
		To:      toEmail,
		ToName:  displayName,
		Subject: "Confirm your email address",
		Text:    text,
		HTML: wrapHTML("Confirm your email address", fmt.Sprintf(`
<p>%s</p>
<p>Confirm this address to finish setting up your FORGE account:</p>
%s
<p class="muted">This link works once and expires in %s.</p>
<hr>
<p class="muted">If you did not create a FORGE account, you can ignore this message —
no account is active until this link is opened.</p>`,
			html.EscapeString(name), button(verifyURL, "Confirm address"), html.EscapeString(window))),
		Tag: "email_verify",
	}
}

// PasswordResetEmail builds the reset message.
func PasswordResetEmail(toEmail, displayName, resetURL string, expiresIn time.Duration) *Message {
	name := greetingName(displayName)
	window := humanDuration(expiresIn)

	text := fmt.Sprintf(`%s

Someone asked to reset the password for the FORGE account using this address.
If that was you, set a new password here:

%s

This link works once and expires in %s. Using it signs out every device
currently signed in to this account.

If you did not ask for this, do nothing. Your password has not changed and this
link will expire on its own. No action is needed to cancel it.

— FORGE
This is an automated message from an AI engineering system. Nobody is reading a reply inbox.`,
		name, resetURL, window)

	return &Message{
		To:      toEmail,
		ToName:  displayName,
		Subject: "Reset your FORGE password",
		Text:    text,
		HTML: wrapHTML("Reset your password", fmt.Sprintf(`
<p>%s</p>
<p>Someone asked to reset the password for the FORGE account using this address.
If that was you, set a new password here:</p>
%s
<p class="muted">This link works once and expires in %s. Using it signs out every
device currently signed in to this account.</p>
<hr>
<p class="muted"><strong>If you did not ask for this, do nothing.</strong> Your password has
not changed and this link will expire on its own.</p>`,
			html.EscapeString(name), button(resetURL, "Set a new password"), html.EscapeString(window))),
		Tag: "password_reset",
	}
}

// PasswordChangedEmail notifies that a password was changed.
//
// This one is not optional politeness. If an attacker resets a password, this
// message is the account holder's only signal that it happened — so it is sent
// after the change, to the address on file, whether or not the change came from
// a reset link.
func PasswordChangedEmail(toEmail, displayName string, at time.Time, sessionsRevoked int64) *Message {
	name := greetingName(displayName)
	when := at.UTC().Format("2 January 2006 at 15:04 UTC")

	sessions := "No other devices were signed in."
	if sessionsRevoked == 1 {
		sessions = "1 other signed-in device was signed out."
	} else if sessionsRevoked > 1 {
		sessions = fmt.Sprintf("%d other signed-in devices were signed out.", sessionsRevoked)
	}

	text := fmt.Sprintf(`%s

The password for your FORGE account was changed on %s.

%s

If you made this change, nothing further is needed.

If you did NOT make this change, someone else has access to your account or your
email. Reset your password immediately, and check whether your email account has
been accessed by someone else — that is usually the real entry point.

— FORGE
This is an automated message from an AI engineering system. Nobody is reading a reply inbox.`,
		name, when, sessions)

	return &Message{
		To:      toEmail,
		ToName:  displayName,
		Subject: "Your FORGE password was changed",
		Text:    text,
		HTML: wrapHTML("Your password was changed", fmt.Sprintf(`
<p>%s</p>
<p>The password for your FORGE account was changed on <strong>%s</strong>.</p>
<p class="muted">%s</p>
<hr>
<p><strong>If you did not make this change</strong>, someone else has access to your
account or your email. Reset your password immediately, and check whether your
email account has been accessed by someone else — that is usually the real entry point.</p>`,
			html.EscapeString(name), html.EscapeString(when), html.EscapeString(sessions))),
		Tag: "password_changed",
	}
}

// greetingName renders an opening line, falling back to something that does not
// read as a broken template when no display name is set.
func greetingName(displayName string) string {
	n := strings.TrimSpace(displayName)
	if n == "" {
		return "Hello,"
	}
	return "Hello " + n + ","
}

// humanDuration renders a duration the way a person would say it.
func humanDuration(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= 24*time.Hour:
		return "24 hours"
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	case d >= time.Hour:
		return "1 hour"
	case d >= 2*time.Minute:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	default:
		return "a few minutes"
	}
}

// button renders a call-to-action that degrades safely.
//
// The raw URL is printed underneath the button, always. Many clients strip
// styled anchors, some strip HTML entirely, and a recipient checking where a
// link goes before clicking it should not have to hover.
func button(url, label string) string {
	escaped := html.EscapeString(url)
	return fmt.Sprintf(`
<p><a class="btn" href="%s">%s</a></p>
<p class="muted">Or copy this address into your browser:<br><span class="url">%s</span></p>`,
		escaped, html.EscapeString(label), escaped)
}

// wrapHTML wraps body content in the shared shell.
//
// All CSS is inline or in a single <style> block, and the layout is a single
// centred column. Email clients support roughly 2003-era HTML; anything that
// depends on flexbox, grid, or an external stylesheet renders as a broken page
// in Outlook and is therefore not used.
func wrapHTML(title, body string) string {
	return fmt.Sprintf(`<!doctype html>
<html><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>%s</title>
<style>
  body { margin:0; padding:24px; background:#0b0f18; color:#eef1f6;
         font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;
         font-size:15px; line-height:1.6; }
  .card { max-width:520px; margin:0 auto; background:#141a26; border:1px solid #263041;
          border-radius:12px; padding:28px; }
  .mark { font-weight:700; letter-spacing:.22em; font-size:12px; color:#4fd8e8; margin:0 0 20px; }
  p { margin:0 0 14px; }
  .muted { color:#98a3b8; font-size:13px; }
  .url { color:#4fd8e8; word-break:break-all; font-family:ui-monospace,SFMono-Regular,Menlo,monospace; font-size:12px; }
  .btn { display:inline-block; background:#4fd8e8; color:#08202a !important; text-decoration:none;
         font-weight:650; padding:11px 20px; border-radius:7px; }
  hr { border:0; border-top:1px solid #263041; margin:20px 0; }
  a { color:#4fd8e8; }
</style></head>
<body><div class="card">
<p class="mark">FORGE</p>
%s
<hr>
<p class="muted">Automated message from an AI engineering system. Nobody is reading a reply inbox.</p>
</div></body></html>`, html.EscapeString(title), body)
}
