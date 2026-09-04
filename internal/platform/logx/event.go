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

// Model portfolio events. Appended after the initial registry; AllEvents below
// is extended to match (the fence parses this file and would catch an omission).
const (
	EventLLMCompleted     Event = "forge.llm.completed"
	EventLLMRetrying      Event = "forge.llm.retrying"
	EventLLMTruncated     Event = "forge.llm.truncated"
	EventLLMEmptyResponse Event = "forge.llm.empty_response"
	EventLLMUsageMissing  Event = "forge.llm.usage_missing"
	EventLLMRefused       Event = "forge.llm.refused"
)

func init() {
	allEvents = append(allEvents,
		EventLLMCompleted, EventLLMRetrying, EventLLMTruncated,
		EventLLMEmptyResponse, EventLLMUsageMissing, EventLLMRefused,
	)
}

// Agent runtime events.
const (
	EventTaskResumeDegraded Event = "forge.task.resume_degraded"
	EventCheckpointFailed   Event = "forge.checkpoint.write_failed"
	EventToolLedgerFailed   Event = "forge.tool.ledger_write_failed"
	EventBudgetRecordFailed Event = "forge.budget.record_failed"
	EventToolSucceeded      Event = "forge.tool.succeeded"
	EventToolFailed         Event = "forge.tool.failed"
	EventToolDeduplicated   Event = "forge.tool.deduplicated"
	// EventToolRefusedSchema is a call whose arguments did not match the tool's
	// declared schema. Logged separately from a policy refusal: one is the
	// model getting the shape wrong and the other is the system saying no, and
	// an operator asking "is the model struggling with this tool's contract?"
	// needs to count the first without the second.
	EventToolRefusedSchema Event = "forge.tool.refused_schema"
	// EventInjectionSuspected is untrusted content matching a known
	// prompt-injection shape (PRD SEC-04). A WARN because somebody tried, not
	// because anything failed: the content was framed and passed through
	// unaltered, and the record is the artefact.
	EventInjectionSuspected Event = "forge.security.injection_suspected"
	EventPlanCreated        Event = "forge.plan.created"
	EventTaskCycleStarted   Event = "forge.task.cycle_started"
	EventTaskCycleEnded     Event = "forge.task.cycle_ended"
	EventVerificationRan    Event = "forge.verification.ran"
	EventApprovalOpened     Event = "forge.approval.opened"
	EventWorkerIdle         Event = "forge.worker.idle"
	EventWorkerReaped       Event = "forge.worker.reaped"
)

func init() {
	allEvents = append(allEvents,
		EventTaskResumeDegraded, EventCheckpointFailed, EventToolLedgerFailed,
		EventBudgetRecordFailed, EventToolSucceeded, EventToolFailed,
		EventToolDeduplicated, EventToolRefusedSchema, EventInjectionSuspected,
		EventPlanCreated, EventTaskCycleStarted,
		EventTaskCycleEnded, EventVerificationRan, EventApprovalOpened,
		EventWorkerIdle, EventWorkerReaped,
	)
}

// Worker outcome events.
const (
	EventBudgetExceededLog Event = "forge.budget.exceeded"
	EventTaskRetryingLog   Event = "forge.task.retrying"
	EventTaskSkippedLog    Event = "forge.task.skipped"
	EventTaskLeaseExpired  Event = "forge.task.lease_expired"
)

func init() {
	allEvents = append(allEvents,
		EventBudgetExceededLog, EventTaskRetryingLog, EventTaskSkippedLog, EventTaskLeaseExpired,
	)
}

// Goal settlement events.
const (
	EventGoalSettled      Event = "forge.goal.settled"
	EventGoalSettleFailed Event = "forge.goal.settle_failed"
)

func init() { allEvents = append(allEvents, EventGoalSettled, EventGoalSettleFailed) }

// Workbench events.
const (
	EventConverseTurn Event = "forge.converse.turn"
)

func init() { allEvents = append(allEvents, EventConverseTurn) }

// Goal intake events. These are the web surface's half of what `forgectl goal
// new` prints to a terminal: an operator asking "did anyone start a goal from
// the workbench, and did planning ever succeed?" greps these.
const (
	EventGoalDrafted    Event = "forge.goal.drafted"
	EventGoalPlanFailed Event = "forge.goal.plan_failed"
	EventGoalStarted    Event = "forge.goal.started"
)

func init() { allEvents = append(allEvents, EventGoalDrafted, EventGoalPlanFailed, EventGoalStarted) }

// Geometry events (PRD VIS-04, VIS-05).
//
// Saving is logged because a variant is the first thing the workbench writes
// that OUTLIVES the conversation, and "where did this render come from?" has to
// be answerable from the log alone. Export is logged because it is the moment
// geometry LEAVES the system, after which the conversion label travels only if
// the file carries it.
const (
	EventGeometrySaved    Event = "forge.geometry.saved"
	EventGeometryExported Event = "forge.geometry.exported"
	EventGeometryRefused  Event = "forge.geometry.export_refused"
	EventGeometryCompared Event = "forge.geometry.compared"
	// EventGeometryAdopted is an earlier variant brought forward so a person can
	// rule on it. Logged because "we went back to v1" is a decision, and a
	// decision that leaves no trace is one nobody can ask about later.
	EventGeometryAdopted Event = "forge.geometry.adopted"
)

func init() {
	allEvents = append(allEvents,
		EventGeometrySaved, EventGeometryExported, EventGeometryRefused, EventGeometryCompared,
		EventGeometryAdopted,
	)
}

// Streaming events.
const (
	EventLLMStreamFrame Event = "forge.llm.stream_frame_skipped"
)

func init() { allEvents = append(allEvents, EventLLMStreamFrame) }

// Memory and decision-log events (PRD MEM-01..03).
//
// Forgetting and purging are logged because they are the two acts that REMOVE
// something at a person's request: "why does FORGE no longer know that?" has to
// be answerable, and the row itself is gone or blank by the time anyone asks.
// Purge is a warning rather than info — it is the one operation that undoes a
// user's deletion record, and it should be visible without going looking.
const (
	EventMemoryWritten      Event = "forge.memory.written"
	EventMemoryForgotten    Event = "forge.memory.forgotten"
	EventMemoryPurged       Event = "forge.memory.purged"
	EventMemorySwept        Event = "forge.memory.swept"
	EventDecisionMade       Event = "forge.decision.made"
	EventDecisionSuperseded Event = "forge.decision.superseded"
)

func init() {
	allEvents = append(allEvents,
		EventMemoryWritten, EventMemoryForgotten, EventMemoryPurged, EventMemorySwept,
		EventDecisionMade, EventDecisionSuperseded,
	)
}

// Workspace-model events (PRD RSN-01, WRK-03, WRK-04).
//
// Promotion is logged because it is the one operation that creates a node from
// another one: "where did this requirement come from?" is answerable from the
// graph, and from here when somebody is reading logs instead.
const (
	EventNodeAdded             Event = "forge.node.added"
	EventNodePromoted          Event = "forge.node.promoted"
	EventArtifactVersioned     Event = "forge.artifact.versioned"
	EventArtifactVerified      Event = "forge.artifact.verified"
	EventArtifactDispositioned Event = "forge.artifact.dispositioned"
)

func init() {
	allEvents = append(allEvents,
		EventNodeAdded, EventNodePromoted, EventArtifactVersioned,
		EventArtifactVerified, EventArtifactDispositioned,
	)
}

// Containment events (PRD SEC-03, SAF-07).
//
// Revocation and unredactable values are warnings rather than info: the first is
// somebody withdrawing a credential, usually during an incident, and the second
// means a value will reach the model. Both should be visible without going
// looking for them.
const (
	EventSecretDeclared     Event = "forge.secret.declared"
	EventSecretGranted      Event = "forge.secret.granted"
	EventSecretRevoked      Event = "forge.secret.revoked"
	EventSecretResolved     Event = "forge.secret.resolved"
	EventSecretRefused      Event = "forge.secret.refused"
	EventSecretUnredactable Event = "forge.secret.unredactable"
	EventSecretLeakBlocked  Event = "forge.secret.leak_blocked"
	EventIncidentOpened     Event = "forge.incident.opened"
	EventIncidentAction     Event = "forge.incident.action"
	EventIncidentClosed     Event = "forge.incident.closed"
)

func init() {
	allEvents = append(allEvents,
		EventSecretDeclared, EventSecretGranted, EventSecretRevoked,
		EventSecretResolved, EventSecretRefused, EventSecretUnredactable,
		EventSecretLeakBlocked,
		EventIncidentOpened, EventIncidentAction, EventIncidentClosed,
	)
}

// Access, second factors and rooms (PRD SEC-02, COL-01).
//
// Grants and revocations are logged because they change who can do what, and
// "when did they get access" is the first question after anything goes wrong.
const (
	EventAccessGranted   Event = "forge.access.granted"
	EventAccessRevoked   Event = "forge.access.revoked"
	EventAccessRefused   Event = "forge.access.refused"
	EventMFAEnrolled     Event = "forge.mfa.enrolled"
	EventMFAActivated    Event = "forge.mfa.activated"
	EventMFAChallenged   Event = "forge.mfa.challenged"
	EventMFAAccepted     Event = "forge.mfa.accepted"
	EventMFARejected     Event = "forge.mfa.rejected"
	EventMFARecoveryUsed Event = "forge.mfa.recovery_used"
	EventDeviceTrusted   Event = "forge.device.trusted"
	EventDeviceRevoked   Event = "forge.device.revoked"
	EventRoomOpened      Event = "forge.room.opened"
	EventRoomTurn        Event = "forge.room.turn"
	EventRoomClosed      Event = "forge.room.closed"
	EventHandoffTaken    Event = "forge.handoff.taken"

	// Presence. Separate from opened/closed because "who was in the room when
	// that was approved" is answered by arrivals and departures, not by the
	// session's own lifetime.
	EventRoomJoined Event = "forge.room.joined"
	EventRoomLeft   Event = "forge.room.left"

	// The live stream (collab/hub.go). Opening one is routine; falling behind
	// on one is a real degradation of somebody's session and is logged at WARN,
	// because a subscriber that silently misses events renders an incomplete
	// transcript and cannot tell.
	// Privacy controls (PRD SEC-06, AUD-07). Both are consequential and both are
	// logged: one changes whether a conversation is written down, the other
	// erases part of one that was.
	EventRoomTranscribing  Event = "forge.room.transcribing"
	EventRoomVoiceRedacted Event = "forge.room.voice_redacted"

	EventRoomStreamOpened Event = "forge.room.stream_opened"
	EventRoomStreamLagged Event = "forge.room.stream_lagged"

	// The media plane (internal/media). Peers arriving and leaving are ordinary;
	// a forward or a renegotiation that fails is not, and both silence somebody
	// for everybody else — which is invisible from inside the room, so it is
	// logged rather than left to be noticed.
	EventMediaPeerJoined        Event = "forge.media.peer_joined"
	EventMediaPeerLeft          Event = "forge.media.peer_left"
	EventMediaRenegotiated      Event = "forge.media.renegotiated"
	EventMediaRenegotiateFailed Event = "forge.media.renegotiate_failed"
	EventMediaForwardFailed     Event = "forge.media.forward_failed"
	EventMediaRefused           Event = "forge.media.refused"
	// EventMediaStateChanged is somebody muting, pausing or resuming (AUD-07).
	EventMediaStateChanged Event = "forge.media.state_changed"

	// Transcription (PRD AUD-03). A segment that produced no text is ordinary —
	// silence and coughs are — but a response that could not be read at all is
	// not, and the two must never look the same in the transcript.
	EventASRTranscribed   Event = "forge.asr.transcribed"
	EventASRFailed        Event = "forge.asr.failed"
	EventASREmptyResponse Event = "forge.asr.empty_response"
	EventASRDropped       Event = "forge.asr.dropped"

	// FORGE's own voice in a room (PRD AUD-01, AUD-05, AUD-07). An interruption
	// is ordinary and is logged at INFO because "did it stop when I spoke" is the
	// first thing anybody asks of a barge-in that felt wrong.
	EventTTSSpoke       Event = "forge.tts.spoke"
	EventTTSInterrupted Event = "forge.tts.interrupted"
	EventTTSFailed      Event = "forge.tts.failed"
	EventTTSEmpty       Event = "forge.tts.empty_response"

	// A project's character could not be read, so FORGE argued and explained at
	// the default intensity (PRD RSN-04). WARN rather than silence: a deployment
	// where every call quietly reverts to the default looks exactly like one
	// where nobody has changed the setting.
	EventCharacterFallback Event = "forge.character.fallback"

	// A tool raised its own tier for a call, above the ceiling it was offered
	// under (PRD SAF-01). The effect has already happened; what is wrong is the
	// classification that admitted it.
	EventToolExceededTier Event = "forge.tool.exceeded_tier"

	// Hazards were loaded into a plan because the goal is r3 or above
	// (PRD SAF-02). Logged at INFO including when the count is zero: "this
	// project has no recorded hazards" and "nobody looked" are different facts.
	EventPlanHazardsLoaded Event = "forge.plan.hazards_loaded"

	// A goal started with an unanswered question and the labelled assumption
	// could not be filed (PRD RSN-02). The work proceeds; what it rests on was
	// not written down, which is exactly the state this requirement exists to
	// prevent, so it is said loudly.
	EventAssumptionUnfiled Event = "forge.goal.assumption_unfiled"

	// A goal names a chosen option that is not in its option set (PRD RSN-03).
	// The planner is told nothing rather than told the wrong approach, so the
	// work proceeds as though no choice had been made — which is the one outcome
	// somebody who chose would never expect. Only a hand-edited row can produce
	// it, and a silent recovery would leave nothing to find afterwards.
	EventChoiceUnreadable Event = "forge.goal.choice_unreadable"

	// A turn named recorded requirements to build from and the project graph
	// could not be read (PRD VIS-01). The turn proceeds as an ordinary message;
	// said loudly because from the outside it looks identical to a workbench
	// where "model this requirement" simply does nothing.
	EventWorkspaceUnreadable Event = "forge.workspace.unreadable"
)

func init() {
	allEvents = append(allEvents,
		EventAccessGranted, EventAccessRevoked, EventAccessRefused,
		EventMFAEnrolled, EventMFAActivated, EventMFAChallenged,
		EventMFAAccepted, EventMFARejected, EventMFARecoveryUsed,
		EventDeviceTrusted, EventDeviceRevoked,
		EventRoomOpened, EventRoomTurn, EventRoomClosed, EventHandoffTaken,
		EventRoomJoined, EventRoomLeft,
		EventRoomStreamOpened, EventRoomStreamLagged,
		EventRoomTranscribing, EventRoomVoiceRedacted,
		EventMediaPeerJoined, EventMediaPeerLeft,
		EventMediaRenegotiated, EventMediaRenegotiateFailed,
		EventMediaForwardFailed, EventMediaRefused, EventMediaStateChanged,
		EventASRTranscribed, EventASRFailed, EventASREmptyResponse, EventASRDropped,
		EventTTSSpoke, EventTTSInterrupted, EventTTSFailed, EventTTSEmpty,
		EventCharacterFallback, EventToolExceededTier, EventPlanHazardsLoaded,
		EventChoiceUnreadable, EventWorkspaceUnreadable,
		EventAssumptionUnfiled,
	)
}
