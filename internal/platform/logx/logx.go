package logx

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// contextKey is unexported so no other package can collide with our keys.
type contextKey int

const (
	keyRequestID contextKey = iota
	keyTraceID
	keySpanID
	keyActorID
)

// Correlation identifier names. WARN and ERROR records are required to carry
// them (see the logging convention): a warning you cannot tie back to a request
// is a warning nobody can act on.
const (
	FieldRequestID = "request_id"
	FieldTraceID   = "trace_id"
	FieldSpanID    = "span_id"
	FieldActorID   = "actor_id"
	FieldEvent     = "event"
	FieldErrorCode = "error_code"
	FieldCategory  = "error_category"
	FieldRetryable = "retryable"
	FieldRemedy    = "remedy"
)

// WithRequestID returns a context carrying the request correlation id.
func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, keyRequestID, id)
}

// WithTrace returns a context carrying trace and span identifiers.
func WithTrace(ctx context.Context, traceID, spanID string) context.Context {
	return context.WithValue(context.WithValue(ctx, keyTraceID, traceID), keySpanID, spanID)
}

// WithActor returns a context carrying the acting principal's identifier.
func WithActor(ctx context.Context, actorID string) context.Context {
	return context.WithValue(ctx, keyActorID, actorID)
}

func stringValue(ctx context.Context, k contextKey) string {
	if v, ok := ctx.Value(k).(string); ok {
		return v
	}
	return ""
}

// RequestID reads the request correlation id from ctx, or "" when absent.
func RequestID(ctx context.Context) string { return stringValue(ctx, keyRequestID) }

// TraceID reads the trace id from ctx, or "" when absent.
func TraceID(ctx context.Context) string { return stringValue(ctx, keyTraceID) }

// Logger wraps slog with FORGE's conventions: enumerated event names, automatic
// correlation-id propagation, and structured error decomposition.
//
// It deliberately offers no free-form message method. Every record is anchored
// to an Event so the log remains greppable by a stable token.
type Logger struct {
	sl *slog.Logger
}

// Options configures logger construction.
type Options struct {
	// Level is the minimum level emitted. Defaults to Info.
	Level slog.Level
	// Format is "json" (default, for machine consumption) or "text" (for a
	// human at a terminal during development).
	Format string
	// Output defaults to os.Stdout.
	Output io.Writer
	// Service is stamped on every record so multi-process logs stay separable.
	Service string
}

// New constructs a Logger.
func New(opts Options) *Logger {
	out := opts.Output
	if out == nil {
		out = os.Stdout
	}
	handlerOpts := &slog.HandlerOptions{Level: opts.Level}

	var h slog.Handler
	if strings.EqualFold(opts.Format, "text") {
		h = slog.NewTextHandler(out, handlerOpts)
	} else {
		h = slog.NewJSONHandler(out, handlerOpts)
	}
	sl := slog.New(h)
	if opts.Service != "" {
		sl = sl.With("service", opts.Service)
	}
	return &Logger{sl: sl}
}

// With returns a Logger with additional permanent fields.
func (l *Logger) With(args ...any) *Logger { return &Logger{sl: l.sl.With(args...)} }

// Enabled reports whether records at the given level would be emitted. Callers
// use it to skip building expensive fields.
func (l *Logger) Enabled(ctx context.Context, level slog.Level) bool {
	return l.sl.Enabled(ctx, level)
}

// correlate prepends the correlation fields carried on ctx.
func correlate(ctx context.Context, args []any) []any {
	out := make([]any, 0, len(args)+8)
	if v := stringValue(ctx, keyRequestID); v != "" {
		out = append(out, FieldRequestID, v)
	}
	if v := stringValue(ctx, keyTraceID); v != "" {
		out = append(out, FieldTraceID, v)
	}
	if v := stringValue(ctx, keySpanID); v != "" {
		out = append(out, FieldSpanID, v)
	}
	if v := stringValue(ctx, keyActorID); v != "" {
		out = append(out, FieldActorID, v)
	}
	return append(out, args...)
}

// Debug emits a debug-level record for the given event.
func (l *Logger) Debug(ctx context.Context, e Event, args ...any) {
	l.sl.DebugContext(ctx, string(e), correlate(ctx, args)...)
}

// Info emits an info-level record for the given event.
func (l *Logger) Info(ctx context.Context, e Event, args ...any) {
	l.sl.InfoContext(ctx, string(e), correlate(ctx, args)...)
}

// Warn emits a warning. Per the logging convention, every fallback-to-default
// path, every parse failure, and every shape mismatch must reach here rather
// than silently returning a zero value — a silent default is a defect that
// looks like health.
func (l *Logger) Warn(ctx context.Context, e Event, args ...any) {
	l.sl.WarnContext(ctx, string(e), correlate(ctx, args)...)
}

// Error emits an error-level record.
func (l *Logger) Error(ctx context.Context, e Event, args ...any) {
	l.sl.ErrorContext(ctx, string(e), correlate(ctx, args)...)
}

// ErrorWith emits an error record and decomposes err into its code, category,
// retryability, and operator remedy. This is the only place those fields are
// derived, so a log line and an API response can never disagree about a code.
func (l *Logger) ErrorWith(ctx context.Context, e Event, err error, args ...any) {
	l.sl.ErrorContext(ctx, string(e), correlate(ctx, append(errorFields(err), args...))...)
}

// WarnWith emits a warning record carrying decomposed error fields. Used for
// degraded-but-continuing paths, where the error is real but not fatal.
func (l *Logger) WarnWith(ctx context.Context, e Event, err error, args ...any) {
	l.sl.WarnContext(ctx, string(e), correlate(ctx, append(errorFields(err), args...))...)
}

func errorFields(err error) []any {
	if err == nil {
		return nil
	}
	code := errs.CodeOf(err)
	fields := []any{"error", err.Error(), FieldErrorCode, string(code)}
	if d, ok := errs.Lookup(code); ok {
		fields = append(fields, FieldCategory, string(d.Category),
			FieldRetryable, d.Retryable, FieldRemedy, d.Remedy)
	}
	var fe *errs.Error
	if errors.As(err, &fe) {
		for k, v := range fe.Fields {
			fields = append(fields, k, v)
		}
	}
	return fields
}

// Slog exposes the underlying slog.Logger for the few libraries that demand one.
func (l *Logger) Slog() *slog.Logger { return l.sl }

// Discard returns a logger that writes nowhere. For tests that assert behaviour
// rather than output.
func Discard() *Logger {
	return &Logger{sl: slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError + 1}))}
}
