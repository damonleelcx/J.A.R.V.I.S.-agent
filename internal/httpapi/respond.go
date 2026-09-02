// Package httpapi is FORGE's HTTP surface: routing, middleware, and handlers.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// ErrorBody is the wire shape of every failure this API returns.
//
// The fields are not decoration. A client needs `code` to branch on, a human
// needs `message` and `remedy` to act, and support needs `request_id` to find
// the corresponding log line. An API that returns only a message forces every
// consumer to string-match, which then makes the message unchangeable.
type ErrorBody struct {
	Code      string         `json:"code"`
	Category  string         `json:"category"`
	Message   string         `json:"message"`
	Remedy    string         `json:"remedy"`
	Retryable bool           `json:"retryable"`
	RequestID string         `json:"request_id,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

// unknownFieldPrefix is how encoding/json reports a field the target struct
// does not declare. Matched as a string because the standard library does not
// expose a typed error for it.
const unknownFieldPrefix = `json: unknown field `

type errorEnvelope struct {
	Error ErrorBody `json:"error"`
}

// WriteJSON writes a JSON response.
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if payload == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		// The status line is already sent, so nothing can be corrected here.
		// Encoding failures are a programming error (an unserialisable field),
		// and the connection is simply closed.
		return
	}
}

// WriteError renders an error using the central registry.
//
// Status, category, message and remedy all come from the registry rather than
// from the call site, so a code cannot mean one thing in the log and another on
// the wire.
func WriteError(w http.ResponseWriter, r *http.Request, log *logx.Logger, err error) {
	ctx := r.Context()
	code := errs.CodeOf(err)
	def := lookupOrInternal(code)

	body := ErrorBody{
		Code:      string(def.Code),
		Category:  string(def.Category),
		Message:   def.Cause,
		Remedy:    def.Remedy,
		Retryable: def.Retryable,
		RequestID: logx.RequestID(ctx),
	}

	// The `detail` field carries case-specific context — which field failed,
	// when a link expired. It is included for client errors, where it helps the
	// caller fix their request, and withheld for server errors, where it can
	// carry internal structure a caller has no business seeing.
	var fe *errs.Error
	if errors.As(err, &fe) && fe.Detail != "" && def.HTTPStatus < 500 {
		body.Details = map[string]any{"detail": fe.Detail}
	}

	// 5xx is ours, 4xx is theirs. Logging every 4xx at error level trains
	// everyone to ignore the error log.
	if def.HTTPStatus >= 500 {
		log.ErrorWith(ctx, logx.EventHTTPRejected, err,
			"method", r.Method, "path", r.URL.Path, "status", def.HTTPStatus)
	} else {
		log.Info(ctx, logx.EventHTTPRejected,
			"method", r.Method, "path", r.URL.Path, "status", def.HTTPStatus,
			logx.FieldErrorCode, string(def.Code))
	}

	if def.Code == errs.CodeRateLimited {
		w.Header().Set("Retry-After", "60")
	}
	WriteJSON(w, def.HTTPStatus, errorEnvelope{Error: body})
}

func lookupOrInternal(c errs.Code) errs.Definition {
	if d, ok := errs.Lookup(c); ok {
		return d
	}
	d, _ := errs.Lookup(errs.CodeInternal)
	return d
}

// DecodeJSON reads and validates a JSON request body.
//
// DisallowUnknownFields is deliberate: a client sending `{"emial": ...}` gets a
// clear rejection instead of an account created with an empty address. Silently
// ignoring unknown fields turns a typo into a mystery.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	const op = "httpapi.DecodeJSON"

	if ct := r.Header.Get("Content-Type"); ct != "" &&
		!hasPrefixFold(ct, "application/json") {
		return errs.New(op, errs.CodeUnsupportedMedia).
			WithDetail("Content-Type was %q; this endpoint accepts application/json", ct)
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var syntaxErr *json.SyntaxError
		var typeErr *json.UnmarshalTypeError
		switch {
		case errors.As(err, &syntaxErr):
			return errs.Wrap(op, errs.CodeValidationFailed, err).
				WithDetail("request body is not valid JSON (at byte %d)", syntaxErr.Offset)
		case errors.As(err, &typeErr):
			return errs.Wrap(op, errs.CodeValidationFailed, err).
				WithDetail("field %q expects a %s", typeErr.Field, typeErr.Type)
		case err.Error() == "http: request body too large":
			return errs.Wrap(op, errs.CodePayloadTooLarge, err)
		case strings.HasPrefix(err.Error(), unknownFieldPrefix):
			// encoding/json names the offending field but only in the message
			// text. Surfacing it turns "request body could not be decoded" into
			// something the caller can actually act on — a typo like "emial"
			// otherwise looks like a server problem.
			field := strings.Trim(strings.TrimPrefix(err.Error(), unknownFieldPrefix), `"`)
			return errs.Wrap(op, errs.CodeValidationFailed, err).
				WithDetail("unknown field %q; check the spelling, or consult the endpoint's accepted fields", field)
		case errors.Is(err, io.EOF):
			return errs.Wrap(op, errs.CodeValidationFailed, err).
				WithDetail("request body was empty; this endpoint expects a JSON object")
		default:
			return errs.Wrap(op, errs.CodeValidationFailed, err).
				WithDetail("request body could not be decoded: %s", err.Error())
		}
	}
	// Exactly one JSON value per request: trailing content usually means a
	// client concatenated two payloads and only the first would be honoured.
	if dec.More() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("request body contains more than one JSON value")
	}
	return nil
}

func hasPrefixFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		a, b := s[i], prefix[i]
		if 'A' <= a && a <= 'Z' {
			a += 'a' - 'A'
		}
		if 'A' <= b && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}
