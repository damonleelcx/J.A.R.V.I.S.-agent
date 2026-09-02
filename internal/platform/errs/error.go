package errs

import (
	"errors"
	"fmt"
)

// Error is the single error type crossing FORGE package boundaries.
//
// Why one type: the API layer, the job queue, and the audit log all need the
// same three answers from any failure — what code, is it worth retrying, and
// what does the human do now. Bare `errors.New` cannot answer those, so it is
// wrapped at the boundary rather than propagated.
type Error struct {
	Code Code
	// Op names the operation that failed, in `package.Function` form. Chained
	// through wrapping so a message reads as a call path, not a single frame.
	Op string
	// Detail adds case-specific context beyond the registry Cause. It must never
	// contain secrets, password material, or raw token values.
	Detail string
	// Fields carries structured context onto the log record.
	Fields map[string]any
	// wrapped is the underlying cause, preserved for errors.Is/As.
	wrapped error
}

func (e *Error) Error() string {
	d, ok := Lookup(e.Code)
	base := string(e.Code)
	if ok {
		base = fmt.Sprintf("%s: %s", e.Code, d.Cause)
	}
	if e.Op != "" {
		base = e.Op + ": " + base
	}
	if e.Detail != "" {
		base += " (" + e.Detail + ")"
	}
	if e.wrapped != nil {
		base += ": " + e.wrapped.Error()
	}
	return base
}

func (e *Error) Unwrap() error { return e.wrapped }

// Definition resolves the registry entry, falling back to CodeInternal so that
// an unregistered code still produces a usable HTTP status rather than a zero.
func (e *Error) Definition() Definition {
	if d, ok := Lookup(e.Code); ok {
		return d
	}
	d, _ := Lookup(CodeInternal)
	return d
}

// Retryable reports whether an identical retry may succeed. The durable queue
// branches on this to choose backoff-and-retry over terminal failure, so a
// miscategorised code silently changes the engine's behaviour.
func (e *Error) Retryable() bool { return e.Definition().Retryable }

// New builds an Error with no underlying cause.
func New(op string, code Code) *Error { return &Error{Code: code, Op: op} }

// Wrap attaches a code and operation to an existing error. Returns nil when err
// is nil so it can be used directly in a return without a preceding check.
func Wrap(op string, code Code, err error) *Error {
	if err == nil {
		return nil
	}
	return &Error{Code: code, Op: op, wrapped: err}
}

// WithDetail returns a copy carrying case-specific context.
func (e *Error) WithDetail(format string, args ...any) *Error {
	c := *e
	c.Detail = fmt.Sprintf(format, args...)
	return &c
}

// WithField returns a copy carrying one structured field.
func (e *Error) WithField(k string, v any) *Error {
	c := *e
	c.Fields = make(map[string]any, len(e.Fields)+1)
	for kk, vv := range e.Fields {
		c.Fields[kk] = vv
	}
	c.Fields[k] = v
	return &c
}

// CodeOf extracts the code from any error, defaulting to CodeInternal. This is
// what lets a handler answer "which status?" without type-switching everywhere.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}

// Is reports whether err carries the given code anywhere in its chain.
func Is(err error, code Code) bool {
	var e *Error
	for errors.As(err, &e) {
		if e.Code == code {
			return true
		}
		if e.wrapped == nil {
			return false
		}
		err = e.wrapped
	}
	return false
}

// IsRetryable reports whether err is worth retrying. Unknown errors are treated
// as retryable: an unclassified transient fault that we drop permanently is a
// worse outcome than one wasted retry.
func IsRetryable(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Retryable()
	}
	return true
}
