package mail

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// ResendSender delivers through the Resend HTTP API.
type ResendSender struct {
	apiKey  string
	env     Envelope
	client  *http.Client
	log     *logx.Logger
	baseURL string
}

// NewResendSender returns a Resend transport.
func NewResendSender(apiKey string, env Envelope, timeout time.Duration, log *logx.Logger) *ResendSender {
	return &ResendSender{
		apiKey:  apiKey,
		env:     env,
		client:  &http.Client{Timeout: timeout},
		log:     log,
		baseURL: "https://api.resend.com",
	}
}

// Name implements Sender.
func (s *ResendSender) Name() string { return "resend" }

type resendRequest struct {
	From    string   `json:"from"`
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Text    string   `json:"text"`
	HTML    string   `json:"html,omitempty"`
	Tags    []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"tags,omitempty"`
}

// Send delivers the message.
func (s *ResendSender) Send(ctx context.Context, m *Message) error {
	const op = "mail.ResendSender.Send"

	if err := m.Validate(); err != nil {
		return err
	}

	payload := resendRequest{
		From:    s.env.Address(),
		To:      []string{m.To},
		Subject: m.Subject,
		Text:    m.Text,
		HTML:    m.HTML,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return errs.Wrap(op, errs.CodeSerializationFail, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.baseURL+"/emails", bytes.NewReader(body))
	if err != nil {
		return errs.Wrap(op, errs.CodeInternal, err)
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")

	s.log.Debug(ctx, logx.EventMailSending, "transport", s.Name(), "to", m.To, "tag", m.Tag)

	resp, err := s.client.Do(req)
	if err != nil {
		return errs.Wrap(op, errs.CodeMailDeliveryFail, err).
			WithDetail("the Resend API could not be reached")
	}
	defer resp.Body.Close()

	// Read a bounded amount: an error body is small, and an unbounded read of a
	// misbehaving endpoint is a memory exhaustion vector.
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var parsed struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(respBody, &parsed); err != nil || parsed.ID == "" {
			// Accepted, but not in the shape we expect. The mail probably went;
			// warn rather than fail so a response-format change does not break
			// sign-up, but never claim more than we know.
			s.log.Warn(ctx, logx.EventMailSent,
				"transport", s.Name(), "to", m.To, "status", resp.StatusCode,
				"detail", "Resend accepted the message but the response did not contain an id; delivery is probable but unconfirmed")
			return nil
		}
		s.log.Info(ctx, logx.EventMailSent,
			"transport", s.Name(), "to", m.To, "tag", m.Tag, "provider_id", parsed.ID)
		return nil
	}

	// Classify: a 4xx will fail identically on retry, a 5xx or 429 may not.
	// The queue reads Retryable, so getting this wrong either spins forever or
	// drops a recoverable failure.
	code := errs.CodeMailDeliveryFail
	if resp.StatusCode == http.StatusTooManyRequests {
		code = errs.CodeRateLimited
	} else if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		code = errs.CodeValidationFailed
	}
	err = errs.New(op, code).WithDetail("Resend returned %d: %s",
		resp.StatusCode, truncate(string(respBody), 400))
	s.log.WarnWith(ctx, logx.EventMailFailed, err, "transport", s.Name(), "to", m.To)
	return err
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("… (%d bytes total)", len(s))
}
