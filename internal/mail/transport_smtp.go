package mail

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/smtp"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// SMTPSender delivers over SMTP with STARTTLS.
type SMTPSender struct {
	host     string
	port     int
	username string
	password string
	env      Envelope
	timeout  time.Duration
	log      *logx.Logger
	clock    clock.Clock
}

// NewSMTPSender returns an SMTP transport.
func NewSMTPSender(host string, port int, username, password string, env Envelope, timeout time.Duration, log *logx.Logger, clk clock.Clock) *SMTPSender {
	return &SMTPSender{
		host: host, port: port, username: username, password: password,
		env: env, timeout: timeout, log: log, clock: clk,
	}
}

// Name implements Sender.
func (s *SMTPSender) Name() string { return "smtp" }

// Send delivers the message.
//
// Written against net/smtp directly rather than a library because the flow is
// short and the security-relevant step — refusing to authenticate over an
// unencrypted connection — must be explicit and auditable rather than a
// library default that could change.
func (s *SMTPSender) Send(ctx context.Context, m *Message) error {
	const op = "mail.SMTPSender.Send"

	if err := m.Validate(); err != nil {
		return err
	}

	addr := net.JoinHostPort(s.host, fmt.Sprintf("%d", s.port))
	s.log.Debug(ctx, logx.EventMailSending, "transport", s.Name(), "to", m.To, "server", addr)

	dialer := &net.Dialer{Timeout: s.timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return errs.Wrap(op, errs.CodeMailDeliveryFail, err).
			WithDetail("cannot reach the SMTP server at %s", addr)
	}
	// Bound the whole conversation, not just the dial: an SMTP server that
	// accepts a connection and then stops responding would otherwise hold this
	// request open for as long as it liked.
	deadline := s.clock.Now().Add(s.timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	client, err := smtp.NewClient(conn, s.host)
	if err != nil {
		conn.Close()
		return errs.Wrap(op, errs.CodeMailDeliveryFail, err).
			WithDetail("SMTP greeting failed")
	}
	defer client.Close()

	if ok, _ := client.Extension("STARTTLS"); ok {
		if err := client.StartTLS(&tls.Config{ServerName: s.host, MinVersion: tls.VersionTLS12}); err != nil {
			return errs.Wrap(op, errs.CodeMailDeliveryFail, err).
				WithDetail("STARTTLS negotiation failed with %s", s.host)
		}
	} else if s.username != "" {
		// Refuse to send credentials in the clear. A server that cannot offer
		// STARTTLS is a misconfiguration, and silently downgrading would leak
		// the SMTP password on every send.
		return errs.New(op, errs.CodeMailDeliveryFail).
			WithDetail("%s does not advertise STARTTLS and credentials were configured; refusing to authenticate over an unencrypted connection", addr)
	}

	if s.username != "" {
		if err := client.Auth(smtp.PlainAuth("", s.username, s.password, s.host)); err != nil {
			return errs.Wrap(op, errs.CodeMailDeliveryFail, err).
				WithDetail("SMTP authentication was rejected by %s", s.host)
		}
	}

	if err := client.Mail(s.env.FromEmail); err != nil {
		return errs.Wrap(op, errs.CodeMailDeliveryFail, err).WithDetail("MAIL FROM was rejected")
	}
	if err := client.Rcpt(m.To); err != nil {
		return errs.Wrap(op, errs.CodeMailDeliveryFail, err).
			WithDetail("the server rejected the recipient %s", m.To)
	}
	w, err := client.Data()
	if err != nil {
		return errs.Wrap(op, errs.CodeMailDeliveryFail, err).WithDetail("DATA was rejected")
	}
	if _, err := w.Write(renderEML(s.env, m, s.clock.Now())); err != nil {
		return errs.Wrap(op, errs.CodeMailDeliveryFail, err).WithDetail("writing the message body failed")
	}
	if err := w.Close(); err != nil {
		// Closing the data writer is where the server actually accepts or
		// rejects the message, so this error is a delivery failure, not a
		// cleanup nuisance.
		return errs.Wrap(op, errs.CodeMailDeliveryFail, err).
			WithDetail("the server rejected the message at end-of-data")
	}
	if err := client.Quit(); err != nil {
		// The message was already accepted above; a failed QUIT is cosmetic.
		s.log.WarnWith(ctx, logx.EventMailSent, err,
			"transport", s.Name(), "detail", "message was accepted but QUIT failed")
	}

	s.log.Info(ctx, logx.EventMailSent, "transport", s.Name(), "to", m.To, "tag", m.Tag)
	return nil
}
