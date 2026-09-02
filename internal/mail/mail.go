// Package mail delivers FORGE's transactional email.
//
// Three transports share one interface: a file transport for development, and
// Resend and SMTP for real delivery. The file transport exists so that sign-up
// works on a laptop with no mail account at all — a developer reads the
// verification link out of an .eml file. config.Load refuses it in production,
// because a transport that writes to disk instead of delivering is a silent
// outage.
package mail

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Message is a rendered email, ready to send.
type Message struct {
	To      string
	ToName  string
	Subject string
	// Text is the plain-text body. Always populated: some clients and most
	// security scanners never render the HTML part, and a mail whose only
	// actionable link is inside HTML is unusable for them.
	Text string
	// HTML is the rich body. Optional.
	HTML string
	// Tag groups messages for observability, e.g. "email_verify".
	Tag string
}

// Validate checks a message is sendable before a transport is bothered.
func (m *Message) Validate() error {
	const op = "mail.Message.Validate"

	if strings.TrimSpace(m.To) == "" {
		return errs.New(op, errs.CodeValidationFailed).WithDetail("message has no recipient")
	}
	if strings.TrimSpace(m.Subject) == "" {
		return errs.New(op, errs.CodeValidationFailed).WithDetail("message has no subject")
	}
	if strings.TrimSpace(m.Text) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("message has no plain-text body; clients that do not render HTML would receive an empty mail")
	}
	// Header injection: a newline in an address or subject would let a caller
	// append arbitrary headers (Bcc, Reply-To) to the outgoing message.
	for field, v := range map[string]string{"To": m.To, "ToName": m.ToName, "Subject": m.Subject} {
		if strings.ContainsAny(v, "\r\n") {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("%s contains a line break, which would allow header injection", field)
		}
	}
	return nil
}

// Sender delivers a message.
//
// Implementations must be safe for concurrent use and must respect the
// context's deadline: a mail transport that blocks forever would hold a
// sign-up request open indefinitely.
type Sender interface {
	// Send delivers m, or returns an error explaining why it could not.
	Send(ctx context.Context, m *Message) error
	// Name identifies the transport for logs and health output.
	Name() string
}

// Envelope is the From identity applied by every transport.
type Envelope struct {
	FromName  string
	FromEmail string
}

// Address renders the RFC 5322 From header value.
func (e Envelope) Address() string {
	if e.FromName == "" {
		return e.FromEmail
	}
	// Quote the display name so a comma or colon in it cannot split the header.
	return fmt.Sprintf("%q <%s>", e.FromName, e.FromEmail)
}

// renderEML renders a message as an RFC 5322 document. Shared by the file
// transport and the SMTP transport, so both produce byte-identical mail and a
// developer reading an .eml sees exactly what a recipient would.
func renderEML(env Envelope, m *Message, now time.Time) []byte {
	var b strings.Builder

	to := m.To
	if m.ToName != "" {
		to = fmt.Sprintf("%q <%s>", m.ToName, m.To)
	}

	boundary := "forge-boundary-" + fmt.Sprintf("%d", now.UnixNano())

	fmt.Fprintf(&b, "From: %s\r\n", env.Address())
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", m.Subject)
	fmt.Fprintf(&b, "Date: %s\r\n", now.Format(time.RFC1123Z))
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	if m.Tag != "" {
		fmt.Fprintf(&b, "X-Forge-Tag: %s\r\n", m.Tag)
	}

	if m.HTML == "" {
		fmt.Fprintf(&b, "Content-Type: text/plain; charset=UTF-8\r\n\r\n")
		b.WriteString(normalizeCRLF(m.Text))
		return []byte(b.String())
	}

	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=%q\r\n\r\n", boundary)
	// Plain part first: multipart/alternative is ordered least-rich to
	// most-rich, and a client picks the last part it can render.
	fmt.Fprintf(&b, "--%s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n", boundary)
	b.WriteString(normalizeCRLF(m.Text))
	fmt.Fprintf(&b, "\r\n--%s\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n", boundary)
	b.WriteString(normalizeCRLF(m.HTML))
	fmt.Fprintf(&b, "\r\n--%s--\r\n", boundary)
	return []byte(b.String())
}

// normalizeCRLF converts bare newlines to CRLF, which RFC 5322 requires. Some
// SMTP servers reject a message with bare LF; others silently truncate it.
func normalizeCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	return strings.ReplaceAll(s, "\n", "\r\n")
}
