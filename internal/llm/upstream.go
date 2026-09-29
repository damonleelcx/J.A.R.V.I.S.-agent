package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Upstream is what the PROVIDER said about a failed model call.
//
// # What was wrong (2026-09-29)
//
// A workbench turn failed with EXTERNAL_UNAVAILABLE and the record kept
// FORGE's own summary — "model %q (role %s) failed after %d attempts" — and
// nothing else. The provider's status, its error code and its message were
// formatted into a wrapped error's text and then never read again: DetailOf
// takes the OUTERMOST detail, which is the summary, and that outer detail is
// what reaches the turn row, the browser and the event.
//
// So a 429 quota exhaustion, a 503 outage, a model that no longer exists and a
// TLS failure all read identically afterwards. By the time anybody looks the
// endpoint is healthy again and the original cause is unrecoverable — which is
// exactly what happened to the turn this type was written for.
//
// ‼️ This is a RECORD, not a decision. Nothing here changes what is retried,
// how long anything waits, or which codes are retryable. It only keeps what
// arrived, so that the same failure a week later can be told apart from this
// one.
//
// # Every field is redacted and bounded before it is stored
//
// A provider error body is untrusted input that travels further than most: into
// a log, into an error detail, into a Postgres row that survives the turn, and
// into a browser. Two things therefore happen to it on the way in, in
// newUpstream and nowhere else:
//
//   - redaction (redactSecrets): a gateway that echoes the request — and some
//     do — must not be able to put our Authorization header, our API key or a
//     bearer token anywhere on that list;
//   - bounding (upstreamMessageLimit): a provider that answers with a 200 KiB
//     HTML error page must not be able to fill a log record or a row with it.
type Upstream struct {
	// Status is the HTTP status the provider answered with, and 0 when no
	// response arrived at all (a refused connection, a TLS failure, a timeout).
	Status int
	// Code and Type are the provider's own classification, from an
	// OpenAI-compatible `{"error":{"message","type","code"}}` body or from the
	// top-level `{"code","message"}` some providers use instead. Empty when the
	// body carried neither.
	Code string
	Type string
	// Message is what the provider said: its error message when the body was
	// JSON this build could read, otherwise a snippet of the body as it
	// arrived. Redacted and bounded.
	Message string
	// Transport is the transport-level failure, for the case where Status is 0
	// and there is no body to quote. Redacted and bounded like Message.
	Transport string
	// Attempts labels what EVERY attempt of a retried call returned, oldest
	// first, and is set only on the error a retry loop gives up with.
	//
	// ‼️ Three 429s and a 500 followed by two 429s are different stories — the
	// first is a quota that has not moved, the second is an endpoint that broke
	// and then throttled. Keeping only the last attempt loses the difference,
	// and the difference is what decides whether anybody needs to act.
	Attempts []string

	// wrapped keeps the cause this answer was built from, so that attaching an
	// Upstream to an error never removes anything from its chain.
	wrapped error
}

// upstreamMessageLimit bounds what is kept of a provider's answer, in
// characters. Generous enough for a real provider message (the longest observed
// on this deployment is DashScope's regional-key sentence at ~120), small enough
// that an HTML error page cannot fill a log record or a durable row.
const upstreamMessageLimit = 400

// newUpstream reads what a provider answered on one failed call.
//
// ‼️ The ONE door: every Upstream is built here, so redaction and bounding
// cannot be forgotten at a call site. secrets are the values that must never
// come back out — the endpoint's own keys — and are removed by literal match as
// well as by shape, because a key that does not look like a key is still a key.
func newUpstream(status int, body []byte, transportErr error, secrets ...string) *Upstream {
	u := &Upstream{Status: status}
	if transportErr != nil {
		u.wrapped = transportErr
		u.Transport = clean(transportErr.Error(), secrets)
	}

	raw := strings.TrimSpace(string(body))
	if raw == "" {
		return u
	}

	// Both shapes this repository has met. The nested one is the
	// OpenAI-compatible contract; the flat one is what DashScope's image and
	// speech endpoints answer with (see decodeDrawing in illustrate.go).
	var parsed struct {
		Error *struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
		Code    any    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
		switch {
		case parsed.Error != nil:
			u.Code = scalar(parsed.Error.Code)
			u.Type = parsed.Error.Type
			u.Message = clean(parsed.Error.Message, secrets)
		case parsed.Message != "" || scalar(parsed.Code) != "":
			u.Code = scalar(parsed.Code)
			u.Message = clean(parsed.Message, secrets)
		}
		if u.Message != "" || u.Code != "" || u.Type != "" {
			return u
		}
	}
	// Not JSON, or JSON with no error in it. Kept as a snippet: an HTML 502
	// page from a load balancer in front of the provider says WHICH hop failed,
	// which is the one thing that distinguishes it from the provider failing.
	u.Message = clean(raw, secrets)
	return u
}

// scalar renders a provider's `code`, which arrives as a string on some
// endpoints and a number on others, without inventing a value for a missing one.
func scalar(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case float64:
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// clean is redaction, whitespace collapse and bounding, in that order.
//
// Collapse before bounding, because an HTML page is mostly indentation and a
// snippet of 400 characters of newlines says nothing. Redaction before both,
// because a truncation that cut a key in half would still be a key on the page.
func clean(s string, secrets []string) string {
	return truncate(strings.Join(strings.Fields(redactSecrets(s, secrets...)), " "), upstreamMessageLimit)
}

// redactionMarker is what replaces anything that might be a credential. Spelled
// out rather than blanked so that a reader can tell "there was a key here" from
// "the provider said nothing".
const redactionMarker = "[redacted]"

var (
	// A labelled credential: `Authorization: Bearer x`, `"api_key":"x"`,
	// `x-api-key=x`.
	//
	// ‼️ The value is a quoted string, or a scheme word and its token, or one
	// token — and NOT "the rest of the line". It was the rest of the line
	// first, and a provider message reading `Rejected: Authorization: Bearer
	// sk-… . The key is not enabled for this model.` came out as the first six
	// words and nothing else: the redaction ate the sentence that said what to
	// do. The scheme alternative is what keeps `Basic dXNlcjpwYXNz` — whose
	// token matches no key shape — from surviving on its own.
	redactLabelled = regexp.MustCompile(
		`(?i)\b(authorization|api[-_]?key|apikey|x-api-key|access[-_]?token|bearer[-_]?token|secret[-_]?key|api[-_]?secret)\b("?\s*[:=]\s*)("[^"]*"|(basic|bearer|token)\s+\S+|\S+)`)
	// A bearer token with no label in front of it.
	redactBearer = regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._\-+/=]{4,}`)
	// The shape every OpenAI-compatible provider issues keys in, which is also
	// the shape a key pasted into a prompt has.
	redactKeyShaped = regexp.MustCompile(`\bsk-[A-Za-z0-9._\-]{3,}`)
)

// redactSecrets removes credential material from text on its way to a log, an
// error detail, a database row or a browser.
//
// # Why this is not optional and not a caller's job
//
// A provider error body is the one place a credential can come BACK from: a
// gateway that echoes the failing request, a proxy that quotes its own
// configuration, a validation error that repeats the header it rejected. Every
// one of those has been seen in the wild, and the body is on its way to four
// places that outlive the turn.
//
// secrets are removed by literal match first — this endpoint's own keys, which
// are the ones we would be leaking, whatever shape they take — and the shapes
// above catch the rest.
func redactSecrets(s string, secrets ...string) string {
	for _, secret := range secrets {
		// Short values are not keys and would redact ordinary prose.
		if len(secret) < 6 {
			continue
		}
		s = strings.ReplaceAll(s, secret, redactionMarker)
	}
	s = redactLabelled.ReplaceAllString(s, "${1}${2}"+redactionMarker)
	s = redactBearer.ReplaceAllString(s, "Bearer "+redactionMarker)
	s = redactKeyShaped.ReplaceAllString(s, redactionMarker)
	return s
}

// Error makes an Upstream part of an error chain, so that attaching one neither
// hides the cause it was built from nor needs a parallel channel to travel on.
func (u *Upstream) Error() string {
	if u == nil {
		return ""
	}
	s := u.Sentence()
	if u.wrapped != nil {
		if s == "" {
			return u.wrapped.Error()
		}
		return s + ": " + u.wrapped.Error()
	}
	return s
}

// Unwrap keeps the cause reachable by errors.Is and errors.As.
func (u *Upstream) Unwrap() error { return u.wrapped }

// Label is how one attempt is named in a summary of several.
func (u *Upstream) Label() string {
	if u == nil {
		return "unclassified"
	}
	if u.Status == 0 {
		return "no response"
	}
	return strconv.Itoa(u.Status)
}

// Sentence is what the provider said, in one line a person can read.
//
// Lower-cased at the front so it composes into a longer detail; callers that
// start a sentence with it capitalise it themselves.
func (u *Upstream) Sentence() string {
	if u == nil {
		return ""
	}
	if u.Status == 0 {
		if u.Transport == "" {
			return ""
		}
		return "the provider did not answer: " + u.Transport
	}
	s := fmt.Sprintf("the provider answered %d", u.Status)
	if id := u.identifier(); id != "" {
		s += " (" + id + ")"
	}
	if u.Message == "" {
		if u.Transport != "" {
			// A status arrived and then the body did not. Distinct from both a
			// refused connection and a provider that answered with an empty
			// body, and the status is the part that says which.
			return s + " and then the connection failed: " + u.Transport
		}
		return s + " and said nothing"
	}
	return s + ": " + u.Message
}

// identifier is the provider's own name for this failure: its code, or its type
// when it sent no code.
func (u *Upstream) identifier() string {
	if u.Code != "" {
		return u.Code
	}
	return u.Type
}

// AttemptSummary is what each attempt returned, oldest first, with repeats
// counted: "500, 429 ×2". Empty for a call that made one attempt.
//
// Counted rather than listed in full because the difference that matters is
// "the same thing every time" against "it changed", and a run of nine identical
// statuses should not push the interesting one off the end of a log line.
func (u *Upstream) AttemptSummary() string {
	if u == nil || len(u.Attempts) < 2 {
		return ""
	}
	var parts []string
	for i := 0; i < len(u.Attempts); {
		j := i
		for j < len(u.Attempts) && u.Attempts[j] == u.Attempts[i] {
			j++
		}
		if n := j - i; n > 1 {
			parts = append(parts, fmt.Sprintf("%s ×%d", u.Attempts[i], n))
		} else {
			parts = append(parts, u.Attempts[i])
		}
		i = j
	}
	return strings.Join(parts, ", ")
}

// withAttempts copies this answer with the whole call's attempt trail on it, and
// with cause in its chain so nothing the loop collected is dropped.
func (u *Upstream) withAttempts(attempts []string, cause error) *Upstream {
	out := &Upstream{Attempts: attempts, wrapped: cause}
	if u != nil {
		out.Status, out.Code, out.Type = u.Status, u.Code, u.Type
		out.Message, out.Transport = u.Message, u.Transport
	}
	return out
}

// annotate puts the provider's answer onto an error's structured fields.
//
// This is how it reaches the LOG without any log site having to know about it:
// logx.errorFields copies the outermost *errs.Error's Fields onto the record.
// So one call here at the place the error is built puts the status, the
// provider's code and its message on every line that ever reports it.
func (u *Upstream) annotate(e *errs.Error) *errs.Error {
	if u == nil || e == nil {
		return e
	}
	if u.Status != 0 {
		e = e.WithField("upstream_status", u.Status)
	}
	if u.Code != "" {
		e = e.WithField("upstream_code", u.Code)
	}
	if u.Type != "" {
		e = e.WithField("upstream_type", u.Type)
	}
	if u.Message != "" {
		e = e.WithField("upstream_message", u.Message)
	}
	if u.Transport != "" {
		e = e.WithField("upstream_transport", u.Transport)
	}
	if s := u.AttemptSummary(); s != "" {
		e = e.WithField("upstream_attempts", s)
	}
	return e
}

// UpstreamOf digs the provider's answer out of an error, or returns nil.
//
// Exported because the surfaces a person looks at are in other packages: the
// failed-turn record and the workbench's transcript are written by
// internal/httpapi, and a turn whose detail says only FORGE's general sentence
// is the defect this whole file exists for.
func UpstreamOf(err error) *Upstream {
	var u *Upstream
	if errors.As(err, &u) {
		return u
	}
	return nil
}

// UpstreamSentence is UpstreamOf's answer as one readable line, or "".
func UpstreamSentence(err error) string {
	return UpstreamOf(err).Sentence()
}
