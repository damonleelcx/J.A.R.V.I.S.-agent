package mail

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/id"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// FileSender writes messages to an outbox directory as .eml files.
//
// This is what makes `git clone && make run && sign up` work with no mail
// account, no API key, and no network. It is refused in production by
// config.Load, and it says so in its own logs on every send, because a
// deployment silently running on it would look healthy while delivering
// nothing.
type FileSender struct {
	dir   string
	env   Envelope
	log   *logx.Logger
	clock clock.Clock
}

// NewFileSender returns a transport writing to dir, creating it if needed.
func NewFileSender(dir string, env Envelope, log *logx.Logger, clk clock.Clock) (*FileSender, error) {
	const op = "mail.NewFileSender"

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, errs.Wrap(op, errs.CodeMailDeliveryFail, err).
			WithDetail("cannot create the mail outbox directory %q", dir)
	}
	return &FileSender{dir: dir, env: env, log: log, clock: clk}, nil
}

// Name implements Sender.
func (s *FileSender) Name() string { return "file" }

// Send writes the message to disk.
func (s *FileSender) Send(ctx context.Context, m *Message) error {
	const op = "mail.FileSender.Send"

	if err := m.Validate(); err != nil {
		return err
	}
	now := s.clock.Now()
	name := fmt.Sprintf("%s_%s_%s.eml",
		now.Format("20060102T150405"),
		sanitize(m.Tag),
		id.New(id.PrefixEvent))
	path := filepath.Join(s.dir, name)

	if err := os.WriteFile(path, renderEML(s.env, m, now), 0o600); err != nil {
		return errs.Wrap(op, errs.CodeMailDeliveryFail, err).
			WithDetail("cannot write %q", path)
	}

	// Logged at WARN, not INFO, and on every single send. This transport does
	// not deliver; an operator scanning logs must be unable to miss that.
	s.log.Warn(ctx, logx.EventMailFileDrop,
		"path", path, "to", m.To, "tag", m.Tag,
		"detail", "mail was written to the outbox, NOT delivered; this transport is for development only")
	return nil
}

// OutboxDir returns the directory being written to, so the console can tell a
// developer where to look for their verification link.
func (s *FileSender) OutboxDir() string { return s.dir }

// sanitize makes a tag safe as a filename component.
func sanitize(s string) string {
	if s == "" {
		return "untagged"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}
