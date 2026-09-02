// Package logx provides FORGE's structured logging and the central registry of
// event names.
//
// Why event names are enumerated: an operator asking "did the reporter ever
// run?" needs to grep one stable token, not guess at a sentence someone typed.
// Free-form log messages drift; enumerated event names do not. The naming rule
// is <service>.<area>.<state>, lowercase, dot-separated.
package logx

// Event is a stable, greppable log event name. Like error codes, events may be
// added but never renamed: dashboards and alert rules bind to these strings.
type Event string

const (
	// Process lifecycle
	EventServerStarting  Event = "forge.server.starting"
	EventServerReady     Event = "forge.server.ready"
	EventServerStopping  Event = "forge.server.stopping"
	EventServerStopped   Event = "forge.server.stopped"
	EventWorkerStarting  Event = "forge.worker.starting"
	EventWorkerReady     Event = "forge.worker.ready"
	EventWorkerStopping  Event = "forge.worker.stopping"
	EventWorkerStopped   Event = "forge.worker.stopped"
	EventShutdownTimeout Event = "forge.shutdown.timeout"

	// Storage
	EventDBConnecting        Event = "forge.db.connecting"
	EventDBConnected         Event = "forge.db.connected"
	EventDBConnectFailed     Event = "forge.db.connect_failed"
	EventMigrationApplying   Event = "forge.migration.applying"
	EventMigrationApplied    Event = "forge.migration.applied"
	EventMigrationSkipped    Event = "forge.migration.skipped"
	EventMigrationFailed     Event = "forge.migration.failed"
	EventMigrationAdvisoryOK Event = "forge.migration.lock_acquired"

	// HTTP
	EventHTTPRequest   Event = "forge.http.request"
	EventHTTPRejected  Event = "forge.http.rejected"
	EventHTTPPanic     Event = "forge.http.panic"
	EventHTTPRateLimit Event = "forge.http.rate_limited"

	// Identity
	EventAuthSignupStarted     Event = "forge.auth.signup_started"
	EventAuthSignupCompleted   Event = "forge.auth.signup_completed"
	EventAuthSignupRejected    Event = "forge.auth.signup_rejected"
	EventAuthSigninSucceeded   Event = "forge.auth.signin_succeeded"
	EventAuthSigninFailed      Event = "forge.auth.signin_failed"
	EventAuthSignedOut         Event = "forge.auth.signed_out"
	EventAuthEmailVerifySent   Event = "forge.auth.email_verify_sent"
	EventAuthEmailVerified     Event = "forge.auth.email_verified"
	EventAuthEmailVerifyFailed Event = "forge.auth.email_verify_failed"
	EventAuthResetRequested    Event = "forge.auth.reset_requested"
	EventAuthResetCompleted    Event = "forge.auth.reset_completed"
	EventAuthResetFailed       Event = "forge.auth.reset_failed"
	EventAuthSessionsRevoked   Event = "forge.auth.sessions_revoked"
	EventAuthLockoutEngaged    Event = "forge.auth.lockout_engaged"

	// Mail
	EventMailSending  Event = "forge.mail.sending"
	EventMailSent     Event = "forge.mail.sent"
	EventMailFailed   Event = "forge.mail.failed"
	EventMailFileDrop Event = "forge.mail.written_to_outbox"

	// Configuration
	EventConfigLoaded  Event = "forge.config.loaded"
	EventConfigInvalid Event = "forge.config.invalid"
	EventConfigDefault Event = "forge.config.default_applied"
)

// allEvents backs the fence test that asserts naming discipline. Adding an
// Event constant without listing it here fails TestEveryEventIsRegistered.
var allEvents = []Event{
	EventServerStarting, EventServerReady, EventServerStopping, EventServerStopped,
	EventWorkerStarting, EventWorkerReady, EventWorkerStopping, EventWorkerStopped,
	EventShutdownTimeout,
	EventDBConnecting, EventDBConnected, EventDBConnectFailed,
	EventMigrationApplying, EventMigrationApplied, EventMigrationSkipped,
	EventMigrationFailed, EventMigrationAdvisoryOK,
	EventHTTPRequest, EventHTTPRejected, EventHTTPPanic, EventHTTPRateLimit,
	EventAuthSignupStarted, EventAuthSignupCompleted, EventAuthSignupRejected,
	EventAuthSigninSucceeded, EventAuthSigninFailed, EventAuthSignedOut,
	EventAuthEmailVerifySent, EventAuthEmailVerified, EventAuthEmailVerifyFailed,
	EventAuthResetRequested, EventAuthResetCompleted, EventAuthResetFailed,
	EventAuthSessionsRevoked, EventAuthLockoutEngaged,
	EventMailSending, EventMailSent, EventMailFailed, EventMailFileDrop,
	EventConfigLoaded, EventConfigInvalid, EventConfigDefault,
}

// AllEvents returns every registered event name.
func AllEvents() []Event { return append([]Event(nil), allEvents...) }
