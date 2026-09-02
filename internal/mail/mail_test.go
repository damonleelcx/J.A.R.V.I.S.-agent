package mail

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// TestHeaderInjectionIsRefused is the security fence of this package.
//
// Addresses and subjects are built from user-supplied values. A newline in one
// of them lets the sender append arbitrary headers to the outgoing message —
// a Bcc to themselves, a forged Reply-To — because SMTP separates headers by
// CRLF and a header value that contains one simply becomes two headers.
func TestHeaderInjectionIsRefused(t *testing.T) {
	base := func() *Message {
		return &Message{To: "user@example.com", Subject: "Hello", Text: "body"}
	}

	cases := map[string]func(*Message){
		"newline in To":      func(m *Message) { m.To = "user@example.com\nBcc: attacker@evil.test" },
		"CRLF in To":         func(m *Message) { m.To = "user@example.com\r\nBcc: attacker@evil.test" },
		"newline in Subject": func(m *Message) { m.Subject = "Hi\nBcc: attacker@evil.test" },
		"CR in Subject":      func(m *Message) { m.Subject = "Hi\rX-Evil: 1" },
		"newline in ToName":  func(m *Message) { m.ToName = "Ada\r\nBcc: attacker@evil.test" },
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := base()
			mutate(m)
			if err := m.Validate(); err == nil {
				t.Fatalf("a message with %s was accepted; headers could be injected", name)
			}
		})
	}

	if err := base().Validate(); err != nil {
		t.Fatalf("a clean message was refused: %v", err)
	}
}

func TestValidateRequiresAUsableMessage(t *testing.T) {
	cases := map[string]*Message{
		"no recipient": {Subject: "s", Text: "t"},
		"no subject":   {To: "a@b.test", Text: "t"},
		// A message whose only body is HTML is unreadable to clients and
		// security scanners that never render it — so the actionable link
		// vanishes for exactly the readers most likely to be cautious.
		"no text part": {To: "a@b.test", Subject: "s", HTML: "<p>hi</p>"},
	}
	for name, m := range cases {
		if err := m.Validate(); err == nil {
			t.Errorf("%s: was accepted", name)
		}
	}
}

// TestTemplatesStateTheLinkAndTheClock covers the house rules that make these
// messages usable: the URL is visible as text, and the expiry is stated.
// "This link has expired" is a dead end if nobody mentioned there was a clock.
func TestTemplatesStateTheLinkAndTheClock(t *testing.T) {
	const url = "https://forge.test/auth/verify-email?token=abc123"

	for name, m := range map[string]*Message{
		"verification": VerificationEmail("ada@example.com", "Ada", url, 24*time.Hour),
		"reset":        PasswordResetEmail("ada@example.com", "Ada", url, time.Hour),
	} {
		t.Run(name, func(t *testing.T) {
			if err := m.Validate(); err != nil {
				t.Fatalf("template produced an invalid message: %v", err)
			}
			// The full URL must appear in the PLAIN text part. A reader who
			// wants to see where a link goes before following it should not
			// have to hover over styled HTML.
			if !strings.Contains(m.Text, url) {
				t.Errorf("the plain-text part does not contain the URL:\n%s", m.Text)
			}
			if !strings.Contains(m.HTML, url) {
				t.Error("the HTML part does not contain the URL")
			}
			// Expiry stated.
			if !strings.Contains(m.Text, "expires in") {
				t.Error("the message does not say when the link expires")
			}
			// What to do if this was not expected — the recipient is the only
			// person positioned to notice an account takeover in progress.
			if !strings.Contains(strings.ToLower(m.Text), "if you did not") {
				t.Error("the message does not tell the reader what to do if they did not request it")
			}
			// AUD-05: identify as automated.
			if !strings.Contains(m.Text, "automated message") {
				t.Error("the message does not identify itself as automated")
			}
		})
	}
}

func TestPasswordChangedNoticeReportsTheBlastRadius(t *testing.T) {
	at := time.Date(2026, 9, 2, 14, 30, 0, 0, time.UTC)

	for n, want := range map[int64]string{
		0: "No other devices",
		1: "1 other signed-in device was signed out",
		5: "5 other signed-in devices were signed out",
	} {
		m := PasswordChangedEmail("ada@example.com", "Ada", at, n)
		if !strings.Contains(m.Text, want) {
			t.Errorf("with %d revoked sessions, the notice should say %q:\n%s", n, want, m.Text)
		}
	}
	m := PasswordChangedEmail("ada@example.com", "Ada", at, 0)
	if !strings.Contains(m.Text, "did NOT make this change") {
		t.Error("the notice must tell an account holder what to do if the change was not theirs; " +
			"for a hostile reset this message is their only signal")
	}
}

func TestGreetingSurvivesAnEmptyDisplayName(t *testing.T) {
	// An account created without a name must not produce "Hello ,".
	m := VerificationEmail("a@b.test", "", "https://x.test/?token=t", time.Hour)
	if strings.Contains(m.Text, "Hello ,") || strings.Contains(m.Text, "Hello  ") {
		t.Errorf("greeting reads as a broken template:\n%.60s", m.Text)
	}
	if !strings.HasPrefix(m.Text, "Hello,") {
		t.Errorf("expected a nameless greeting, got: %.30s", m.Text)
	}
}

func TestEnvelopeQuotesTheDisplayName(t *testing.T) {
	// An unquoted comma in a display name splits the From header into two
	// addresses.
	e := Envelope{FromName: "FORGE, Engineering", FromEmail: "forge@example.com"}
	got := e.Address()
	if !strings.HasPrefix(got, `"`) {
		t.Errorf("display name is not quoted: %s", got)
	}
	if !strings.Contains(got, "<forge@example.com>") {
		t.Errorf("address is malformed: %s", got)
	}
	bare := Envelope{FromEmail: "forge@example.com"}.Address()
	if bare != "forge@example.com" {
		t.Errorf("with no display name, expected a bare address, got %q", bare)
	}
}

// TestRenderedMessageUsesCRLF — RFC 5322 requires CRLF line endings. Some SMTP
// servers reject a message with bare LF; others silently truncate it, which is
// worse because it looks like delivery succeeded.
func TestRenderedMessageUsesCRLF(t *testing.T) {
	m := &Message{To: "a@b.test", Subject: "s", Text: "line one\nline two\nline three"}
	out := string(renderEML(Envelope{FromEmail: "f@b.test"}, m, time.Now()))

	if strings.Contains(strings.ReplaceAll(out, "\r\n", ""), "\n") {
		t.Error("the rendered message contains a bare LF")
	}
	if !strings.Contains(out, "line one\r\nline two") {
		t.Error("body line endings were not normalised to CRLF")
	}
}

func TestMultipartPutsPlainTextFirst(t *testing.T) {
	// multipart/alternative is ordered least-rich to most-rich, and a client
	// renders the LAST part it understands. Reversing them means every client
	// shows the plain text.
	m := &Message{To: "a@b.test", Subject: "s", Text: "plain body", HTML: "<p>rich body</p>"}
	out := string(renderEML(Envelope{FromEmail: "f@b.test"}, m, time.Now()))

	plainAt := strings.Index(out, "text/plain")
	htmlAt := strings.Index(out, "text/html")
	if plainAt < 0 || htmlAt < 0 {
		t.Fatalf("expected both parts:\n%s", out)
	}
	if plainAt > htmlAt {
		t.Error("the HTML part precedes the plain part; clients would prefer the wrong one")
	}
	if !strings.Contains(out, "multipart/alternative") {
		t.Error("a message with both parts should be multipart/alternative")
	}
}

// TestFileSenderWritesAndWarns covers the development transport. The warning is
// the point: a deployment silently running on it looks healthy while delivering
// nothing at all.
func TestFileSenderWritesAndWarns(t *testing.T) {
	dir := t.TempDir()
	var buf strings.Builder
	log := logx.New(logx.Options{Output: &buf, Format: "json"})

	s, err := NewFileSender(dir, Envelope{FromName: "FORGE", FromEmail: "f@b.test"}, log,
		clock.NewFake(time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	msg := VerificationEmail("ada@example.com", "Ada", "https://forge.test/?token=abc", time.Hour)
	if err := s.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("wrote %d files, want 1", len(entries))
	}
	body, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "token=abc") {
		t.Error("the written .eml does not contain the link a developer needs to read")
	}
	if !strings.Contains(string(body), "Subject: Confirm your email address") {
		t.Error("the written .eml is missing its Subject header")
	}

	logged := buf.String()
	if !strings.Contains(logged, `"level":"WARN"`) {
		t.Error("the file transport must log at WARN on every send; an operator must not be able to miss that mail is not being delivered")
	}
	if !strings.Contains(logged, "NOT delivered") {
		t.Errorf("the warning does not say that nothing was delivered: %s", logged)
	}
}

func TestFileSenderRefusesAnInvalidMessage(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileSender(dir, Envelope{FromEmail: "f@b.test"}, logx.Discard(),
		clock.NewFake(time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Send(context.Background(), &Message{To: "a@b.test\r\nBcc: evil@x.test", Subject: "s", Text: "t"}); err == nil {
		t.Fatal("the transport wrote a message that fails validation")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Error("an invalid message was written to disk anyway")
	}
}
