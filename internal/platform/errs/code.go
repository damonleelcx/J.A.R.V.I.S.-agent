// Package errs defines the single central registry of FORGE error codes.
//
// Why a central registry: every operator-visible failure must carry a stable
// machine code, a human cause, and an actionable remedy. Scattering string
// literals at call sites makes it impossible to (a) enumerate the failure
// surface, (b) keep API and UI codes in sync, or (c) fence against typos.
// Code literals are therefore banned outside this file; a fence test
// (TestNoErrorCodeLiteralsOutsideRegistry) enforces it.
package errs

// Category partitions the failure surface. It exists so that operators can
// answer "whose problem is this?" without reading the message: Business is the
// user's, System is ours, External is a third party's, Data is a corrupted or
// contradictory record, Runtime is the language/stdlib layer.
type Category string

const (
	CategoryBusiness Category = "business"
	CategorySystem   Category = "system"
	CategoryExternal Category = "external"
	CategoryData     Category = "data"
	CategoryRuntime  Category = "runtime"
)

// Code is a stable UPPER_SNAKE_CASE identifier. It is part of the API contract:
// clients branch on it, so codes may be added but never renamed or repurposed.
type Code string

// Definition is the full registry entry behind a Code.
//
// Remedy is not optional decoration. A failure that cannot tell the user what
// to do next is a dead end, and dead ends are how a long-running agent strands
// a human at 3am. The registry fence asserts every entry has one.
type Definition struct {
	Code     Code
	Category Category
	// HTTPStatus is the status the API layer returns for this code. Kept here
	// rather than at the handler so one code cannot map to two statuses.
	HTTPStatus int
	// Cause explains what went wrong, in operator language.
	Cause string
	// Remedy names the next action the reader can actually take.
	Remedy string
	// Retryable marks failures where an identical retry may succeed. The job
	// queue reads this to decide backoff vs. terminal failure.
	Retryable bool
}

// ---------------------------------------------------------------------------
// Identity & authentication (business)
// ---------------------------------------------------------------------------

const (
	CodeEmailAlreadyRegistered Code = "EMAIL_ALREADY_REGISTERED"
	CodeInvalidCredentials     Code = "INVALID_CREDENTIALS"
	CodeEmailNotVerified       Code = "EMAIL_NOT_VERIFIED"
	CodeTokenInvalid           Code = "TOKEN_INVALID"
	CodeTokenExpired           Code = "TOKEN_EXPIRED"
	CodeTokenAlreadyUsed       Code = "TOKEN_ALREADY_USED"
	CodeSessionExpired         Code = "SESSION_EXPIRED"
	CodeSessionRevoked         Code = "SESSION_REVOKED"
	CodeNotAuthenticated       Code = "NOT_AUTHENTICATED"
	CodeForbidden              Code = "FORBIDDEN"
	CodePasswordTooWeak        Code = "PASSWORD_TOO_WEAK"
	CodeEmailInvalid           Code = "EMAIL_INVALID"
	CodeRateLimited            Code = "RATE_LIMITED"
	CodeAccountLocked          Code = "ACCOUNT_LOCKED"
)

// ---------------------------------------------------------------------------
// Validation & request shape (business)
// ---------------------------------------------------------------------------

const (
	CodeValidationFailed Code = "VALIDATION_FAILED"
	CodeNotFound         Code = "NOT_FOUND"
	CodeConflict         Code = "CONFLICT"
	CodePayloadTooLarge  Code = "PAYLOAD_TOO_LARGE"
	CodeUnsupportedMedia Code = "UNSUPPORTED_MEDIA_TYPE"
)

// ---------------------------------------------------------------------------
// System & storage
// ---------------------------------------------------------------------------

const (
	CodeInternal          Code = "INTERNAL"
	CodeDatabaseUnavail   Code = "DATABASE_UNAVAILABLE"
	CodeMigrationFailed   Code = "MIGRATION_FAILED"
	CodeConfigInvalid     Code = "CONFIG_INVALID"
	CodeMailDeliveryFail  Code = "MAIL_DELIVERY_FAILED"
	CodeSerializationFail Code = "SERIALIZATION_FAILED"
)

// ---------------------------------------------------------------------------
// Data integrity
// ---------------------------------------------------------------------------

const (
	CodeStateCorrupt        Code = "STATE_CORRUPT"
	CodeInvariantViolated   Code = "INVARIANT_VIOLATED"
	CodeCheckpointUnreadabl Code = "CHECKPOINT_UNREADABLE"
)

// registry is the authoritative table. Adding a Code without adding a row here
// fails TestEveryCodeHasDefinition.
var registry = map[Code]Definition{
	CodeEmailAlreadyRegistered: {CodeEmailAlreadyRegistered, CategoryBusiness, 409,
		"An account already exists for this email address.",
		"Sign in instead, or use the password-reset flow if you have forgotten the password.", false},
	CodeInvalidCredentials: {CodeInvalidCredentials, CategoryBusiness, 401,
		"The email address or password did not match an active account.",
		"Check the email and password, then try again. Use password reset if needed.", false},
	CodeEmailNotVerified: {CodeEmailNotVerified, CategoryBusiness, 403,
		"This account exists but its email address has not been verified yet.",
		"Open the verification link sent to your inbox, or request a new verification email.", false},
	CodeTokenInvalid: {CodeTokenInvalid, CategoryBusiness, 400,
		"The token does not correspond to any issued token.",
		"Request a new link. Tokens are single-use and cannot be reconstructed by hand.", false},
	CodeTokenExpired: {CodeTokenExpired, CategoryBusiness, 400,
		"The token was issued but its validity window has passed.",
		"Request a new link; the previous one is no longer accepted.", false},
	CodeTokenAlreadyUsed: {CodeTokenAlreadyUsed, CategoryBusiness, 400,
		"This token has already been redeemed once.",
		"If the action did not take effect, request a new link and retry.", false},
	CodeSessionExpired: {CodeSessionExpired, CategoryBusiness, 401,
		"The session has passed its absolute or idle expiry.",
		"Sign in again to obtain a fresh session.", false},
	CodeSessionRevoked: {CodeSessionRevoked, CategoryBusiness, 401,
		"The session was explicitly revoked (sign-out, password change, or administrative action).",
		"Sign in again. If you did not expect this, review recent account activity.", false},
	CodeNotAuthenticated: {CodeNotAuthenticated, CategoryBusiness, 401,
		"The request carried no usable session credential.",
		"Sign in and retry with the session cookie or bearer token attached.", false},
	CodeForbidden: {CodeForbidden, CategoryBusiness, 403,
		"The authenticated principal is not permitted to perform this action on this resource.",
		"Request access from the resource owner, or act on a resource within your scope.", false},
	CodePasswordTooWeak: {CodePasswordTooWeak, CategoryBusiness, 400,
		"The supplied password did not meet the minimum strength policy.",
		"Use at least 12 characters. Longer passphrases are accepted and preferred over symbol substitution.", false},
	CodeEmailInvalid: {CodeEmailInvalid, CategoryBusiness, 400,
		"The supplied string is not a syntactically valid email address.",
		"Provide an address of the form name@example.com.", false},
	CodeRateLimited: {CodeRateLimited, CategoryBusiness, 429,
		"Too many attempts from this identity or address within the limiter window.",
		"Wait for the interval named in the Retry-After header, then retry.", true},
	CodeAccountLocked: {CodeAccountLocked, CategoryBusiness, 403,
		"The account is temporarily locked after repeated failed sign-in attempts.",
		"Wait for the lockout to elapse, or reset the password to clear it immediately.", false},

	CodeValidationFailed: {CodeValidationFailed, CategoryBusiness, 400,
		"One or more request fields failed validation.",
		"Correct the fields named in the details array and resubmit.", false},
	CodeNotFound: {CodeNotFound, CategoryBusiness, 404,
		"No resource with that identifier is visible to this principal.",
		"Check the identifier. A resource you cannot see is reported the same as one that does not exist.", false},
	CodeConflict: {CodeConflict, CategoryBusiness, 409,
		"The request conflicts with the current state of the resource.",
		"Re-read the resource, reconcile against its current state, and retry.", false},
	CodePayloadTooLarge: {CodePayloadTooLarge, CategoryBusiness, 413,
		"The request body exceeded the configured maximum size.",
		"Split the payload, or upload large artifacts through the artifact endpoint instead of inline.", false},
	CodeUnsupportedMedia: {CodeUnsupportedMedia, CategoryBusiness, 415,
		"The request Content-Type is not supported by this endpoint.",
		"Send application/json unless the endpoint documents another type.", false},

	CodeInternal: {CodeInternal, CategorySystem, 500,
		"An unhandled internal failure occurred.",
		"Retry once. If it persists, quote the request_id from this response when reporting it.", true},
	CodeDatabaseUnavail: {CodeDatabaseUnavail, CategorySystem, 503,
		"The database could not be reached or refused the connection.",
		"Verify the database is running and FORGE_DATABASE_URL is correct, then retry.", true},
	CodeMigrationFailed: {CodeMigrationFailed, CategorySystem, 500,
		"A schema migration did not apply cleanly.",
		"Inspect the named migration and the server log. Do not start the service against a partially migrated schema.", false},
	CodeConfigInvalid: {CodeConfigInvalid, CategorySystem, 500,
		"Configuration is missing a required value or holds an unusable one.",
		"Correct the named environment variable. See .env.example for the expected shape.", false},
	CodeMailDeliveryFail: {CodeMailDeliveryFail, CategoryExternal, 502,
		"The mail transport rejected or failed to accept the message.",
		"Check SMTP credentials and connectivity. In development, the file transport writes to the mail outbox directory instead.", true},
	CodeSerializationFail: {CodeSerializationFail, CategorySystem, 500,
		"A value could not be encoded to or decoded from its stored representation.",
		"This indicates a schema/code mismatch. Check that the binary matches the migrated schema version.", false},

	CodeStateCorrupt: {CodeStateCorrupt, CategoryData, 500,
		"A persisted record failed its structural invariants when read back.",
		"Quarantine the named record and inspect it. Do not let the agent resume against corrupt state.", false},
	CodeInvariantViolated: {CodeInvariantViolated, CategoryData, 500,
		"An operation would have broken a domain invariant and was refused.",
		"Report this with the surrounding request_id: it indicates a logic defect, not a user error.", false},
	CodeCheckpointUnreadabl: {CodeCheckpointUnreadabl, CategoryData, 500,
		"A checkpoint exists but could not be decoded into a resumable state.",
		"Resume from the previous checkpoint, or restart the task from its last verified milestone.", false},
}

// Lookup returns the registry definition for a code.
func Lookup(c Code) (Definition, bool) {
	d, ok := registry[c]
	return d, ok
}

// All returns every registered definition. Used by the fence tests and by the
// /v1/meta/error-codes endpoint that keeps the UI dictionary in sync.
func All() []Definition {
	out := make([]Definition, 0, len(registry))
	for _, d := range registry {
		out = append(out, d)
	}
	return out
}
