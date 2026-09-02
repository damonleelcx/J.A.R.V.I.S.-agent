package mail

import (
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// NewSender builds the transport named by configuration.
//
// The switch is exhaustive with a failing default rather than a silent fallback
// to the file transport: a typo in FORGE_MAIL_TRANSPORT must stop the process,
// not quietly redirect every password-reset email to a directory nobody reads.
func NewSender(cfg config.MailConfig, log *logx.Logger, clk clock.Clock) (Sender, error) {
	const op = "mail.NewSender"

	env := Envelope{FromName: cfg.FromName, FromEmail: cfg.FromEmail}

	switch cfg.Transport {
	case config.MailTransportFile:
		return NewFileSender(cfg.OutboxDir, env, log, clk)
	case config.MailTransportResend:
		return NewResendSender(cfg.ResendKey, env, cfg.SendTimeout, log), nil
	case config.MailTransportSMTP:
		return NewSMTPSender(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser, cfg.SMTPPass, env, cfg.SendTimeout, log, clk), nil
	default:
		return nil, errs.New(op, errs.CodeConfigInvalid).
			WithDetail("mail transport %q is not implemented; expected file, resend or smtp", cfg.Transport)
	}
}

// Compile-time proof that all three transports satisfy Sender, so a signature
// change cannot leave one of them silently unbuildable behind a config branch
// that only production takes.
var (
	_ Sender = (*FileSender)(nil)
	_ Sender = (*ResendSender)(nil)
	_ Sender = (*SMTPSender)(nil)
)
