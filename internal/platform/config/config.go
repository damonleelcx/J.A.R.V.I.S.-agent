// Package config loads and validates FORGE's runtime configuration.
//
// Two rules shape this package:
//
//  1. Fail loudly at startup, never silently at 3am. A missing or malformed
//     value is refused here with the exact environment variable name and a
//     remedy, rather than defaulting to something that looks like it works.
//  2. Secrets are read from the environment only. They are never written to a
//     config file in the repository, never logged, and never rendered by the
//     String method — Redacted() exists so that startup can print the effective
//     configuration without leaking credentials.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Env names the deployment environment. It gates development-only affordances
// (the file mail transport, verbose errors) so they cannot leak into production
// by omission — production is the value that must be chosen explicitly for
// those affordances to switch off, and unknown values are rejected outright.
type Env string

const (
	EnvDevelopment Env = "development"
	EnvTest        Env = "test"
	EnvProduction  Env = "production"
)

// Config is the fully-validated runtime configuration.
type Config struct {
	Env    Env
	HTTP   HTTPConfig
	DB     DBConfig
	Log    LogConfig
	Mail   MailConfig
	Auth   AuthConfig
	LLM    LLMConfig
	Engine EngineConfig
	Media  MediaConfig
	// Security is what this deployment DECLARES about handling customer content
	// (PRD SEC-01, SEC-05). See docs/security-promises.md for what is promised
	// and, as importantly, what is not.
	Security SecurityConfig
	// CAD is the parametric kernel. Empty Python means this deployment has none.
	CAD CADConfig
	// TTS is FORGE's voice. An empty Provider means she speaks through the model
	// client, which is the default and needs no vendor.
	TTS TTSConfig
}

// TTSConfig selects the speech vendor FORGE's own voice comes from.
//
// # Why this is separate from FORGE_LLM_SPEAKER_MODEL
//
// The model client can already synthesise speech. This exists because the VOICE
// is a product decision and the model is not: the timbre has to match the other
// products in this estate, and that voice is published by a different vendor
// than the one answering questions.
//
// ‼️ A speech vendor is a SECOND EGRESS. FORGE_DATA_BOUNDARY states what the
// contract with the MODEL endpoint says; it says nothing about this one. A
// deployment can be truthfully `no_training` about its LLM while reading every
// answer aloud through a backbone that trains on it — which is why Load logs
// the backbone's training status at startup rather than leaving it implied.
type TTSConfig struct {
	// Provider is empty (the model client) or "fish".
	Provider string
	APIURL   string
	APIKey   string
	// VoiceID is the vendor's published voice id. No default: guessing one gives
	// FORGE a different voice than the rest of the estate.
	VoiceID string
	// Model is the synthesis backbone, which is separate from the voice and is
	// what decides whether requests may be trained on.
	Model string
}

// Configured reports whether a speech vendor was asked for.
func (t TTSConfig) Configured() bool { return strings.TrimSpace(t.Provider) != "" }

// DefaultTurnBudget is how long one conversational turn may take.
//
// Long enough for the longest thing a turn is allowed to do — a multi-pass build
// is twelve passes, each a model call plus repairs and a script run, and was
// measured at 25 minutes. A turn that exceeds even this is stopped, because a
// person waiting on a workbench is owed an end.
//
// Exported because a zero must never be taken literally. A Config assembled in
// code rather than loaded from the environment has this field zero, and a turn
// given a zero deadline is cancelled before its first call — every database read
// inside it then fails, and what surfaces is "the database could not be
// reached", which points at the wrong thing entirely. That is not hypothetical:
// it is what two existing tests reported the moment the handler started reading
// this field. So the reader of the field falls back to this, and there is still
// only one number.
const DefaultTurnBudget = 30 * time.Minute

// HTTPConfig covers the public API and console surface.
type HTTPConfig struct {
	// Addr is the listen address, e.g. ":8080".
	Addr string
	// PublicURL is the externally reachable origin. Email verification and
	// password-reset links are built from it, so a wrong value produces links
	// that resolve nowhere — which is why it is validated, not defaulted.
	PublicURL string
	// ReadTimeout bounds how long a client may take to send a request.
	ReadTimeout time.Duration
	// WriteTimeout bounds response writing. Kept generous because the console
	// holds long-lived SSE streams for the execution timeline.
	WriteTimeout time.Duration
	// ShutdownGrace is how long in-flight requests get during shutdown.
	ShutdownGrace time.Duration
	// MaxBodyBytes caps request bodies.
	MaxBodyBytes int64
}

// DBConfig covers Postgres connectivity.
type DBConfig struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
}

// LogConfig covers structured logging.
type LogConfig struct {
	Level  string
	Format string
}

// MailTransport selects how outbound mail leaves the process.
type MailTransport string

const (
	// MailTransportFile writes .eml files to an outbox directory. This is the
	// development default: signup must work on a laptop with no SMTP account,
	// and a developer must be able to read the verification link. It is refused
	// in production.
	MailTransportFile MailTransport = "file"
	// MailTransportResend delivers via the Resend HTTP API.
	MailTransportResend MailTransport = "resend"
	// MailTransportSMTP delivers via SMTP with STARTTLS.
	MailTransportSMTP MailTransport = "smtp"
)

// MailConfig covers outbound transactional mail (verification, reset).
type MailConfig struct {
	Transport   MailTransport
	FromName    string
	FromEmail   string
	OutboxDir   string
	ResendKey   string
	SMTPHost    string
	SMTPPort    int
	SMTPUser    string
	SMTPPass    string
	SendTimeout time.Duration
}

// AuthConfig covers identity and session policy.
type AuthConfig struct {
	// SessionSecret signs session cookies. Required, and required to be long:
	// a short secret is a forgeable session.
	SessionSecret string
	// SessionTTL is the absolute lifetime of a session regardless of activity.
	SessionTTL time.Duration
	// SessionIdleTTL ends a session that has gone quiet.
	SessionIdleTTL time.Duration
	// EmailVerifyTTL bounds an email-verification link.
	EmailVerifyTTL time.Duration
	// PasswordResetTTL bounds a password-reset link. Deliberately shorter than
	// verification: a reset link is a live credential.
	PasswordResetTTL time.Duration
	// MinPasswordLength is the floor for new passwords.
	MinPasswordLength int
	// MaxSigninAttempts before the account is temporarily locked.
	MaxSigninAttempts int
	// LockoutWindow is both the window attempts are counted in and how long a
	// lockout lasts.
	LockoutWindow time.Duration
	// CookieSecure marks session cookies Secure. Forced on in production.
	CookieSecure bool
	// CookieDomain optionally scopes the session cookie.
	CookieDomain string
}

// DataBoundary is the data-handling posture of the model endpoint this
// deployment sends customer content to (PRD SEC-01).
//
// # Why this is a declaration and not a control
//
// SEC-01 asks that customer content is not used for training absent affirmative
// opt-in. FORGE is not the trainer. It cannot observe what a provider does with
// a request after it leaves, and a product that claimed otherwise about a
// third-party endpoint would be claiming to know something it cannot.
//
// What it CAN enforce is that nothing leaves under terms nobody stated. The
// operator holds the contract with the provider and is the only party who knows
// what it says, so they declare it, and FORGE refuses to run without the
// declaration.
//
// # Why there is no default
//
// Because every possible default is a lie. Defaulting to no_training would have
// FORGE asserting a contract term nobody checked — the most dangerous shape
// available, since it reads as a promise. Defaulting to training_opted_in would
// consent on the customer's behalf. Silence is not a posture, so silence stops
// the process.
type DataBoundary string

const (
	// BoundaryNoTraining: the operator asserts this endpoint's terms forbid
	// training on submitted content.
	BoundaryNoTraining DataBoundary = "no_training"
	// BoundaryTrainingOptedIn: the customer affirmatively opted in. Set only
	// when there is a record of them doing so.
	BoundaryTrainingOptedIn DataBoundary = "training_opted_in"
)

// Valid reports whether b is a declared posture.
func (b DataBoundary) Valid() bool {
	return b == BoundaryNoTraining || b == BoundaryTrainingOptedIn
}

// SecurityConfig is what this deployment declares about content leaving it.
// CADConfig points at the interpreter the CAD kernel runs in.
//
// A path rather than a flag, because "is a kernel available" is not a policy
// somebody sets — it is whether an interpreter with build123d exists on this
// machine, and naming it is the only way to answer that without guessing which
// of several Pythons was meant.
type CADConfig struct {
	// Python is the interpreter. Empty means no kernel, which is the default
	// and is not an error.
	Python string
	// AllowScripts lets the model write build123d and have it RUN, for shapes
	// the vocabulary cannot say: an involute gear tooth, a spiral, a lattice.
	//
	// OFF by default, and that is not timidity — it is the one feature here that
	// executes text a model produced. The sandbox is described in full in
	// internal/domain/cad/script.go: an AST whitelist that refuses imports,
	// dunder attributes and every name not on a list before anything runs, and a
	// stripped short-lived process with no environment, a temporary working
	// directory and hard CPU and memory limits. It is defence in depth and not a
	// container, and a deployment that cannot accept that should leave this
	// unset — the feature then does not exist rather than half-existing.
	AllowScripts bool
}

type SecurityConfig struct {
	// DataBoundary is the declared posture of the model endpoint (PRD SEC-01).
	DataBoundary DataBoundary
	// ShellAllowed restricts which commands shell_run may execute (PRD SEC-05).
	//
	// Empty means UNRESTRICTED, which is refused in production. This is the only
	// thing standing between a model-composed command and everything the host can
	// reach: FORGE does not confine network egress, so "which commands may run"
	// is the control, and an empty list is not a permissive setting but the
	// absence of one.
	ShellAllowed []string
}

// LLMConfig covers the model portfolio.
//
// Why roles instead of one model: PRD SAF-03 requires that a high-risk
// conclusion be checked by a method independent of the path that produced it.
// Asking one model to grade its own output is not independence. Routing the
// verifier to a different model family is, so the role split is a safety
// control, not a cost optimisation.
type LLMConfig struct {
	BaseURL string
	APIKey  string
	// Planner decomposes goals and replans. Highest-capability role.
	Planner string
	// Executor drives the tool loop for a single task.
	Executor string
	// Verifier independently checks claimed results. Should be a different
	// model family from Executor; StartupWarnings reports it when it is not.
	Verifier string
	// Summarizer compresses history. Cheapest role.
	Summarizer string
	// Converse holds the workbench conversation. Chosen for LATENCY, not depth:
	// a person is waiting mid-sentence, and PRD AUD-02 asks for first audio
	// inside 700ms. Measured on this provider, the deep-reasoning model took 19s
	// to return a structured reply and the fast conversational one took 6.8s.
	// Neither meets the target — but routing conversation through the reasoning
	// model guarantees it never will.
	Converse string
	// Vision reads images attached to a turn (PRD VIS-01).
	//
	// EMPTY BY DEFAULT, and that is the design. Most text models cannot see, and
	// one sent an image does not fail — it describes its own confusion in
	// confident prose, which is the shape of answer this product exists to
	// refuse. An unset vision model means image input is unavailable and says
	// so, the same way a missing CAD kernel does.
	Vision string
	// Transcriber turns room audio into transcript text (PRD AUD-03).
	//
	// A speech model, not a chat model. It is reached through the ordinary chat
	// endpoint because this provider's OpenAI-compatible surface has no
	// /audio/transcriptions route — see internal/llm/transcribe.go.
	Transcriber string
	// Speaker turns FORGE's words into audio for a room (PRD AUD-05).
	//
	// Reached through the chat endpoint with streaming, because this provider has
	// no /audio/speech route and returns no audio at all without `stream: true`.
	Speaker string
	// Voice is which synthesised voice FORGE speaks in. One per deployment, so
	// the character sounds the same in every room.
	Voice          string
	RequestTimeout time.Duration
	MaxRetries     int
	// TurnBudget bounds ONE CONVERSATIONAL TURN, which is a different thing from
	// RequestTimeout and must never again be derived from it.
	//
	// # Why this exists
	//
	// The turn handler used `RequestTimeout + 15s`, on the reasoning that a
	// deadline shorter than the model client's own would kill a call mid-backoff
	// and blame the model for a timeout hierarchy. That reasoning is right and
	// the arithmetic was wrong: it bounds a turn by the cost of ONE model call,
	// and a turn has not been one call for some time. A turn plans a build, runs
	// a pass for each subsystem, repairs geometry that will not build, runs and
	// rewrites scripts, and looks at the render — dozens of calls, plus kernel
	// time that is not a model call at all.
	//
	// So a multi-pass build measured at 25 minutes in a live test could not
	// finish over HTTP: the handler cancelled it at 3m15s, mid-stream, and the
	// person saw a build stop moving. Nothing reported it as a timeout, because
	// from inside the turn a cancelled context looks like a model that failed.
	//
	// # Why a separate number rather than a bigger one
	//
	// RequestTimeout still bounds each individual call, so a hung provider still
	// fails in three minutes and this does not make one call wait longer. It only
	// allows MORE of them. The two are separate because they answer different
	// questions — "how long may one call take" and "how long may FORGE work" —
	// and folding them together is what produced the bug above.
	TurnBudget time.Duration
}

// MediaConfig is the realtime audio plane (PRD COL-01, AUD-03, NFR-04).
//
// # Why this is off by default
//
// Turning it on binds a UDP port range and starts accepting media. An upgrade
// that silently began doing that would be a network change nobody asked for, on
// every existing deployment at once. Rooms, presence and the live transcript all
// work without it — audio is an addition to the main path, not part of it — so
// the safe default is the one that changes nothing.
//
// A request for audio while it is off is refused with MEDIA_DISABLED, which
// names the variable to set. "Audio unavailable" with no reason is the kind of
// thing people file bugs about.
type MediaConfig struct {
	// Enabled turns the SFU on.
	Enabled bool
	// ICEServers are STUN/TURN URLs. Empty is correct for a server reachable at
	// a routable address: it then offers host candidates, which connect fastest.
	ICEServers []string
	// UDPPortMin and UDPPortMax bound the range the media plane binds.
	//
	// A bounded range rather than an ephemeral port, because the ports have to be
	// opened in a firewall by a person, and "whatever the kernel picks" cannot be.
	UDPPortMin int
	UDPPortMax int
	// PublicIP is advertised in host candidates when the server is behind a NAT
	// that it cannot discover through. Empty means advertise what is observed.
	PublicIP string
	// MaxParticipants is NFR-04's ceiling, enforced rather than assumed.
	MaxParticipants int

	// Transcribe turns spoken audio into turns in the room's record (AUD-03).
	//
	// On by default WHEN MEDIA IS ON, because a room that carries audio and
	// records nothing fails the requirement the room exists for: COL-01 asks for
	// a record of who said what, and an untranscribed meeting has none. It is
	// separable because it calls a paid provider on every utterance, and an
	// operator who wants audio without that bill must be able to say so.
	Transcribe bool
	// SilenceGap is how long a participant's packets must stop before their
	// segment is treated as a finished utterance.
	//
	// This is not voice activity detection — see internal/media/transcribe.go.
	// Too short and sentences are cut into fragments the model transcribes
	// separately and worse; too long and the transcript lags the conversation.
	SilenceGap time.Duration
	// MaxSegment caps one utterance, so a client that never stops sending still
	// produces transcript rather than one unbounded segment.
	MaxSegment time.Duration
	// TranscribeWorkers is how many segments may be in flight at the provider.
	//
	// Sized against the room, not the machine: twenty people all talking produce
	// segments faster than one request at a time can absorb, and the queue
	// behind this drops rather than blocks — dropping is lost transcript.
	TranscribeWorkers int
}

// EngineConfig bounds the durable execution engine.
//
// Every field here exists to answer one question: what stops this thing from
// running forever? PRD-adjacent bullet "prevent infinite execution" is not a
// single limit but seven, because an agent can run away along seven different
// axes and a bound on only one of them is not a bound.
type EngineConfig struct {
	// WorkerConcurrency is how many tasks one worker process runs at once.
	WorkerConcurrency int
	// LeaseDuration is how long a claimed job stays claimed without a heartbeat.
	// Too short and healthy long tasks get stolen; too long and a crashed
	// worker's tasks stall for that duration.
	LeaseDuration time.Duration
	// LeaseHeartbeat is how often a running worker extends its lease. Must be
	// meaningfully smaller than LeaseDuration; validated.
	LeaseHeartbeat time.Duration
	// PollInterval is how often an idle worker looks for work when no wake-up
	// signal has arrived.
	PollInterval time.Duration
	// MaxAttemptsPerTask before a task is failed terminally.
	MaxAttemptsPerTask int
	// BackoffBase and BackoffMax bound exponential retry backoff.
	BackoffBase time.Duration
	BackoffMax  time.Duration
	// MaxIterationsPerTask caps observe→plan→execute→verify cycles for one task.
	MaxIterationsPerTask int
	// MaxToolCallsPerIteration caps tool calls within one cycle.
	MaxToolCallsPerIteration int
	// MaxTokensPerGoal caps total model tokens spent on one goal.
	MaxTokensPerGoal int64
	// MaxCostCentsPerGoal caps spend on one goal.
	MaxCostCentsPerGoal int64
	// MaxWallClockPerGoal caps elapsed time from goal start.
	MaxWallClockPerGoal time.Duration
	// MaxTaskDepth caps recursive task creation, so a planner cannot decompose
	// forever.
	MaxTaskDepth int
	// MaxTasksPerGoal caps total task creation for one goal.
	MaxTasksPerGoal int
}

// ---------------------------------------------------------------------------
// Loading
// ---------------------------------------------------------------------------

// loader accumulates problems so that a misconfigured deployment learns about
// every bad value at once, rather than fixing them one restart at a time.
type loader struct {
	problems []string
	warnings []string
	// required names the sections whose mandatory fields are enforced on this
	// load. A field in an unrequested section is still parsed and still
	// reported if malformed — it is simply not required to be present.
	required sectionSet
}

func (l *loader) fail(key, detail string) {
	l.problems = append(l.problems, fmt.Sprintf("%s: %s", key, detail))
}

func (l *loader) str(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

// requiredIn reads a mandatory value, but only enforces its presence when sec
// is one of the sections this load was asked for.
func (l *loader) requiredIn(sec Section, key string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" && l.required.has(sec) {
		l.fail(key, "is required but was empty or unset")
	}
	return v
}

func (l *loader) intVal(key string, def int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		l.fail(key, fmt.Sprintf("must be an integer, got %q", raw))
		return def
	}
	return n
}

func (l *loader) int64Val(key string, def int64) int64 {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		l.fail(key, fmt.Sprintf("must be an integer, got %q", raw))
		return def
	}
	return n
}

func (l *loader) dur(key string, def time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		l.fail(key, fmt.Sprintf("must be a Go duration such as 30s or 5m, got %q", raw))
		return def
	}
	return d
}

// list reads a comma-separated variable, dropping blanks.
//
// Returns nil rather than a one-element slice of "" when unset, because an ICE
// server list containing an empty URL is rejected by the media stack at start-up
// with an error that points nowhere near the configuration.
func (l *loader) list(key string) []string {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func (l *loader) boolVal(key string, def bool) bool {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return def
	}
	b, err := strconv.ParseBool(raw)
	if err != nil {
		l.fail(key, fmt.Sprintf("must be true or false, got %q", raw))
		return def
	}
	return b
}

// Load reads configuration from the process environment and validates it.
//
// The variadic argument names which sections must be complete. Passing none
// means "all", which is what a full server wants. Passing SectionNone means
// "validate shapes but require nothing", which is what a diagnostic command
// wants — `forgectl config` has to be able to print a broken configuration,
// because that is exactly when somebody runs it.
//
// Problems are accumulated rather than returned at the first one, so a
// misconfigured deployment is not fixed one variable per restart.
func Load(required ...Section) (*Config, []string, error) {
	if len(required) == 0 {
		required = AllSections()
	}
	set := sectionSet{}
	for _, sec := range required {
		set[sec] = true
	}
	l := &loader{required: set}

	env := Env(strings.ToLower(l.str("FORGE_ENV", string(EnvDevelopment))))
	switch env {
	case EnvDevelopment, EnvTest, EnvProduction:
	default:
		l.fail("FORGE_ENV", fmt.Sprintf("must be one of development, test, production; got %q", env))
		env = EnvDevelopment
	}
	prod := env == EnvProduction

	cfg := &Config{Env: env}

	cfg.HTTP = HTTPConfig{
		Addr:          l.str("FORGE_HTTP_ADDR", ":8080"),
		PublicURL:     strings.TrimRight(l.str("FORGE_PUBLIC_URL", "http://localhost:8080"), "/"),
		ReadTimeout:   l.dur("FORGE_HTTP_READ_TIMEOUT", 30*time.Second),
		WriteTimeout:  l.dur("FORGE_HTTP_WRITE_TIMEOUT", 5*time.Minute),
		ShutdownGrace: l.dur("FORGE_HTTP_SHUTDOWN_GRACE", 20*time.Second),
		MaxBodyBytes:  l.int64Val("FORGE_HTTP_MAX_BODY_BYTES", 4<<20),
	}
	if !strings.HasPrefix(cfg.HTTP.PublicURL, "http://") && !strings.HasPrefix(cfg.HTTP.PublicURL, "https://") {
		l.fail("FORGE_PUBLIC_URL", "must start with http:// or https:// because verification links are built from it")
	}
	if prod && set.has(SectionHTTP) && strings.HasPrefix(cfg.HTTP.PublicURL, "http://") {
		l.fail("FORGE_PUBLIC_URL", "must use https:// in production; verification and reset links carry live credentials")
	}

	cfg.Media = MediaConfig{
		Enabled:         l.boolVal("FORGE_MEDIA_ENABLED", false),
		ICEServers:      l.list("FORGE_MEDIA_ICE_SERVERS"),
		UDPPortMin:      l.intVal("FORGE_MEDIA_UDP_PORT_MIN", 50000),
		UDPPortMax:      l.intVal("FORGE_MEDIA_UDP_PORT_MAX", 50200),
		PublicIP:        l.str("FORGE_MEDIA_PUBLIC_IP", ""),
		MaxParticipants: l.intVal("FORGE_MEDIA_MAX_PARTICIPANTS", 20),

		Transcribe:        l.boolVal("FORGE_MEDIA_TRANSCRIBE", true),
		SilenceGap:        l.dur("FORGE_MEDIA_SILENCE_GAP", 800*time.Millisecond),
		MaxSegment:        l.dur("FORGE_MEDIA_MAX_SEGMENT", 15*time.Second),
		TranscribeWorkers: l.intVal("FORGE_MEDIA_TRANSCRIBE_WORKERS", 4),
	}
	if cfg.Media.Enabled {
		if cfg.Media.UDPPortMin < 1 || cfg.Media.UDPPortMax > 65535 || cfg.Media.UDPPortMin > cfg.Media.UDPPortMax {
			l.fail("FORGE_MEDIA_UDP_PORT_MIN", fmt.Sprintf(
				"the media port range %d-%d is not a usable range within 1-65535",
				cfg.Media.UDPPortMin, cfg.Media.UDPPortMax))
		} else if got := cfg.Media.UDPPortMax - cfg.Media.UDPPortMin + 1; got < cfg.Media.MaxParticipants {
			// Each participant needs at least one port. A range narrower than the
			// room ceiling fails at the worst moment — when the room fills — and
			// looks like a media bug rather than a configuration one.
			l.fail("FORGE_MEDIA_UDP_PORT_MAX", fmt.Sprintf(
				"the media port range holds %d port(s) but a room admits %d participant(s); widen the range or lower FORGE_MEDIA_MAX_PARTICIPANTS",
				got, cfg.Media.MaxParticipants))
		}
		if cfg.Media.MaxParticipants < 1 {
			l.fail("FORGE_MEDIA_MAX_PARTICIPANTS", "must be at least 1; a room nobody may join is not a room")
		}
		if cfg.Media.Transcribe {
			if cfg.Media.SilenceGap <= 0 || cfg.Media.MaxSegment <= cfg.Media.SilenceGap {
				l.fail("FORGE_MEDIA_MAX_SEGMENT", fmt.Sprintf(
					"the maximum segment (%s) must be longer than the silence gap (%s), or every utterance is cut off before it can end",
					cfg.Media.MaxSegment, cfg.Media.SilenceGap))
			}
			if cfg.Media.TranscribeWorkers < 1 {
				l.fail("FORGE_MEDIA_TRANSCRIBE_WORKERS", "must be at least 1; zero workers means audio is segmented and never transcribed")
			}
		}
	}

	cfg.DB = DBConfig{
		URL:             l.requiredIn(SectionDB, "FORGE_DATABASE_URL"),
		MaxConns:        int32(l.intVal("FORGE_DB_MAX_CONNS", 16)),
		MinConns:        int32(l.intVal("FORGE_DB_MIN_CONNS", 2)),
		MaxConnLifetime: l.dur("FORGE_DB_CONN_LIFETIME", time.Hour),
		MaxConnIdleTime: l.dur("FORGE_DB_CONN_IDLE", 30*time.Minute),
		ConnectTimeout:  l.dur("FORGE_DB_CONNECT_TIMEOUT", 10*time.Second),
	}
	if cfg.DB.MinConns > cfg.DB.MaxConns {
		l.fail("FORGE_DB_MIN_CONNS", "must not exceed FORGE_DB_MAX_CONNS")
	}

	cfg.Log = LogConfig{
		Level:  strings.ToLower(l.str("FORGE_LOG_LEVEL", "info")),
		Format: strings.ToLower(l.str("FORGE_LOG_FORMAT", map[bool]string{true: "json", false: "text"}[prod])),
	}
	switch cfg.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		l.fail("FORGE_LOG_LEVEL", fmt.Sprintf("must be debug, info, warn or error; got %q", cfg.Log.Level))
	}
	switch cfg.Log.Format {
	case "json", "text":
	default:
		l.fail("FORGE_LOG_FORMAT", fmt.Sprintf("must be json or text; got %q", cfg.Log.Format))
	}

	cfg.Mail = MailConfig{
		Transport:   MailTransport(strings.ToLower(l.str("FORGE_MAIL_TRANSPORT", map[bool]string{true: "resend", false: "file"}[prod]))),
		FromName:    l.str("FORGE_MAIL_FROM_NAME", "FORGE"),
		FromEmail:   l.str("FORGE_MAIL_FROM_EMAIL", "forge@localhost"),
		OutboxDir:   l.str("FORGE_MAIL_OUTBOX_DIR", "./.forge/outbox"),
		ResendKey:   os.Getenv("RESEND_API_KEY"),
		SMTPHost:    os.Getenv("FORGE_SMTP_HOST"),
		SMTPPort:    l.intVal("FORGE_SMTP_PORT", 587),
		SMTPUser:    os.Getenv("FORGE_SMTP_USER"),
		SMTPPass:    os.Getenv("FORGE_SMTP_PASSWORD"),
		SendTimeout: l.dur("FORGE_MAIL_SEND_TIMEOUT", 20*time.Second),
	}
	if set.has(SectionMail) {
		switch cfg.Mail.Transport {
		case MailTransportFile:
			if prod {
				l.fail("FORGE_MAIL_TRANSPORT", "the file transport writes mail to disk instead of delivering it and is refused in production; set resend or smtp")
			}
		case MailTransportResend:
			if cfg.Mail.ResendKey == "" {
				l.fail("RESEND_API_KEY", "is required when FORGE_MAIL_TRANSPORT=resend")
			}
		case MailTransportSMTP:
			if cfg.Mail.SMTPHost == "" {
				l.fail("FORGE_SMTP_HOST", "is required when FORGE_MAIL_TRANSPORT=smtp")
			}
		default:
			l.fail("FORGE_MAIL_TRANSPORT", fmt.Sprintf("must be file, resend or smtp; got %q", cfg.Mail.Transport))
		}
	}

	cfg.Auth = AuthConfig{
		SessionSecret:     l.requiredIn(SectionAuth, "FORGE_SESSION_SECRET"),
		SessionTTL:        l.dur("FORGE_SESSION_TTL", 30*24*time.Hour),
		SessionIdleTTL:    l.dur("FORGE_SESSION_IDLE_TTL", 14*24*time.Hour),
		EmailVerifyTTL:    l.dur("FORGE_EMAIL_VERIFY_TTL", 24*time.Hour),
		PasswordResetTTL:  l.dur("FORGE_PASSWORD_RESET_TTL", time.Hour),
		MinPasswordLength: l.intVal("FORGE_MIN_PASSWORD_LENGTH", 12),
		MaxSigninAttempts: l.intVal("FORGE_MAX_SIGNIN_ATTEMPTS", 8),
		LockoutWindow:     l.dur("FORGE_LOCKOUT_WINDOW", 15*time.Minute),
		CookieSecure:      l.boolVal("FORGE_COOKIE_SECURE", prod),
		CookieDomain:      os.Getenv("FORGE_COOKIE_DOMAIN"),
	}
	if n := len(cfg.Auth.SessionSecret); n > 0 && n < 32 {
		l.fail("FORGE_SESSION_SECRET", fmt.Sprintf("must be at least 32 characters; got %d. Generate one with: openssl rand -base64 48", n))
	}
	if cfg.Auth.MinPasswordLength < 8 {
		l.fail("FORGE_MIN_PASSWORD_LENGTH", "must be at least 8")
	}
	if prod && set.has(SectionAuth) && !cfg.Auth.CookieSecure {
		l.fail("FORGE_COOKIE_SECURE", "cannot be false in production; session cookies would be sent over plaintext")
	}
	if cfg.Auth.SessionIdleTTL > cfg.Auth.SessionTTL {
		l.warnings = append(l.warnings, "FORGE_SESSION_IDLE_TTL exceeds FORGE_SESSION_TTL, so idle expiry can never trigger")
	}

	cfg.LLM = LLMConfig{
		BaseURL:    strings.TrimRight(l.str("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"), "/"),
		APIKey:     l.requiredIn(SectionLLM, "FORGE_LLM_API_KEY"),
		Planner:    l.str("FORGE_LLM_PLANNER_MODEL", "qwen3.8-max"),
		Executor:   l.str("FORGE_LLM_EXECUTOR_MODEL", "qwen3.8-max"),
		Verifier:   l.str("FORGE_LLM_VERIFIER_MODEL", "deepseek-v4-pro"),
		Summarizer: l.str("FORGE_LLM_SUMMARIZER_MODEL", "qwen3.8-flash"),
		// qwen-plus until 2026-09-06, when the provider retired it and every
		// live call to the conversation role — the whole workbench, and the
		// entire evaluation suite — failed with `Model not exist.` The rest of
		// the defaults on this block had already been moved to the 3.8
		// generation; this one was missed, so the only role a PERSON waits on
		// was the only one pointing at a model that no longer existed.
		Converse:       l.str("FORGE_LLM_CONVERSE_MODEL", "qwen3.7-plus"),
		Vision:         l.str("FORGE_LLM_VISION_MODEL", ""),
		Transcriber:    l.str("FORGE_LLM_TRANSCRIBER_MODEL", "qwen3-asr-flash-2026-02-10"),
		Speaker:        l.str("FORGE_LLM_SPEAKER_MODEL", "qwen3-omni-flash"),
		Voice:          l.str("FORGE_LLM_VOICE", "Cherry"),
		RequestTimeout: l.dur("FORGE_LLM_REQUEST_TIMEOUT", 3*time.Minute),
		TurnBudget:     l.dur("FORGE_TURN_BUDGET", DefaultTurnBudget),
		MaxRetries:     l.intVal("FORGE_LLM_MAX_RETRIES", 3),
	}
	// A turn budget below one call's timeout puts the old bug back: the turn is
	// cancelled while a single model call is still inside its own retry window,
	// and the error that surfaces names the model rather than this setting.
	// Refused rather than silently raised, because a deployment that set it low
	// on purpose is making a choice it should be told is incoherent.
	if cfg.LLM.TurnBudget < cfg.LLM.RequestTimeout {
		l.fail("FORGE_TURN_BUDGET", fmt.Sprintf(
			"is %s, shorter than FORGE_LLM_REQUEST_TIMEOUT (%s). A turn makes many model calls, "+
				"so its budget must be at least one call's timeout — otherwise a turn is killed "+
				"mid-call and the failure is reported as the model's.",
			cfg.LLM.TurnBudget, cfg.LLM.RequestTimeout))
	}
	if set.has(SectionLLM) && modelFamily(cfg.LLM.Verifier) == modelFamily(cfg.LLM.Executor) {
		l.warnings = append(l.warnings, fmt.Sprintf(
			"verifier model %q shares a family with executor model %q: independent verification (PRD SAF-03) is weakened when one model grades its own output",
			cfg.LLM.Verifier, cfg.LLM.Executor))
	}

	// PRD SEC-01 and SEC-05. Required alongside the sections whose work they
	// describe: the boundary is a property of the model endpoint, and the shell
	// allow-list is a property of the engine that runs tools.
	// PRD VIS-05. The CAD kernel is a Python interpreter with build123d in it,
	// and UNSET IS THE DEFAULT: without one, parametric export is declared and
	// refused exactly as it was before a kernel existed anywhere in this
	// product. That is the same discipline the vision model follows — an absent
	// capability that says it is absent, rather than one that quietly
	// substitutes something else.
	cfg.CAD = CADConfig{
		Python:       strings.TrimSpace(l.str("FORGE_CAD_PYTHON", "")),
		AllowScripts: l.boolVal("FORGE_ALLOW_SCRIPTS", false),
	}

	cfg.TTS = TTSConfig{
		Provider: strings.ToLower(strings.TrimSpace(l.str("FORGE_TTS_PROVIDER", ""))),
		APIURL:   strings.TrimSpace(l.str("FORGE_TTS_API_URL", "")),
		APIKey:   strings.TrimSpace(l.str("FORGE_TTS_API_KEY", "")),
		VoiceID:  strings.TrimSpace(l.str("FORGE_TTS_VOICE_ID", "")),
		Model:    strings.TrimSpace(l.str("FORGE_TTS_MODEL", "")),
	}
	if cfg.TTS.Configured() {
		if cfg.TTS.Provider != "fish" {
			l.fail("FORGE_TTS_PROVIDER", fmt.Sprintf(
				"is %q, and the only speech vendor this build implements is %q. Leave it unset "+
					"to use the model client's own voice", cfg.TTS.Provider, "fish"))
		}
		if cfg.TTS.APIKey == "" {
			l.fail("FORGE_TTS_API_KEY", "is required when FORGE_TTS_PROVIDER names a vendor")
		}
		if cfg.TTS.VoiceID == "" {
			l.fail("FORGE_TTS_VOICE_ID", "is required when FORGE_TTS_PROVIDER names a vendor. "+
				"The voice is not a default this code may pick: it is a specific published voice, "+
				"and guessing one would give FORGE a different voice than the rest of this estate")
		}
	}

	cfg.Security = SecurityConfig{
		DataBoundary: DataBoundary(strings.ToLower(strings.TrimSpace(l.str("FORGE_DATA_BOUNDARY", "")))),
		ShellAllowed: l.list("FORGE_SHELL_ALLOWED_COMMANDS"),
	}
	if set.has(SectionLLM) && !cfg.Security.DataBoundary.Valid() {
		l.fail("FORGE_DATA_BOUNDARY", fmt.Sprintf(
			"must be %q or %q, and has no default because every default would be a claim nobody "+
				"checked (got %q). This states what your contract with the model endpoint at %s says "+
				"about training on submitted content. FORGE cannot observe what a provider does with "+
				"a request; it can refuse to send one under terms nobody has stated. "+
				"See docs/security-promises.md",
			BoundaryNoTraining, BoundaryTrainingOptedIn, cfg.Security.DataBoundary, cfg.LLM.BaseURL))
	}
	if set.has(SectionEngine) && len(cfg.Security.ShellAllowed) == 0 {
		// Production refuses; everywhere else warns. An unrestricted shell on a
		// developer's laptop is how the tool is meant to be used while building;
		// an unrestricted shell in production hands a model-composed command
		// everything the host can reach, and FORGE confines no network egress.
		problem := "is empty, so shell_run may execute anything the host can run. FORGE does not " +
			"confine network egress (see docs/security-promises.md), so this list is the control, " +
			"not a refinement of one. Set it to the commands this deployment's work actually needs, " +
			"e.g. FORGE_SHELL_ALLOWED_COMMANDS=go,git,ls,cat"
		if prod {
			l.fail("FORGE_SHELL_ALLOWED_COMMANDS", problem)
		} else {
			l.warnings = append(l.warnings, "FORGE_SHELL_ALLOWED_COMMANDS "+problem)
		}
	}

	cfg.Engine = EngineConfig{
		WorkerConcurrency:        l.intVal("FORGE_WORKER_CONCURRENCY", 4),
		LeaseDuration:            l.dur("FORGE_LEASE_DURATION", 2*time.Minute),
		LeaseHeartbeat:           l.dur("FORGE_LEASE_HEARTBEAT", 20*time.Second),
		PollInterval:             l.dur("FORGE_POLL_INTERVAL", 2*time.Second),
		MaxAttemptsPerTask:       l.intVal("FORGE_MAX_ATTEMPTS_PER_TASK", 5),
		BackoffBase:              l.dur("FORGE_BACKOFF_BASE", 2*time.Second),
		BackoffMax:               l.dur("FORGE_BACKOFF_MAX", 10*time.Minute),
		MaxIterationsPerTask:     l.intVal("FORGE_MAX_ITERATIONS_PER_TASK", 25),
		MaxToolCallsPerIteration: l.intVal("FORGE_MAX_TOOL_CALLS_PER_ITERATION", 12),
		MaxTokensPerGoal:         l.int64Val("FORGE_MAX_TOKENS_PER_GOAL", 5_000_000),
		MaxCostCentsPerGoal:      l.int64Val("FORGE_MAX_COST_CENTS_PER_GOAL", 2_000),
		MaxWallClockPerGoal:      l.dur("FORGE_MAX_WALLCLOCK_PER_GOAL", 30*24*time.Hour),
		MaxTaskDepth:             l.intVal("FORGE_MAX_TASK_DEPTH", 5),
		MaxTasksPerGoal:          l.intVal("FORGE_MAX_TASKS_PER_GOAL", 500),
	}
	if cfg.Engine.LeaseHeartbeat >= cfg.Engine.LeaseDuration {
		l.fail("FORGE_LEASE_HEARTBEAT", "must be shorter than FORGE_LEASE_DURATION, otherwise a healthy worker loses its lease before it can renew it")
	}
	if cfg.Engine.LeaseHeartbeat*3 > cfg.Engine.LeaseDuration {
		l.warnings = append(l.warnings, "FORGE_LEASE_HEARTBEAT is more than a third of FORGE_LEASE_DURATION: a single missed heartbeat can lose the lease")
	}
	if cfg.Engine.WorkerConcurrency < 1 {
		l.fail("FORGE_WORKER_CONCURRENCY", "must be at least 1")
	}
	if cfg.Engine.MaxTaskDepth < 1 {
		l.fail("FORGE_MAX_TASK_DEPTH", "must be at least 1")
	}

	if len(l.problems) > 0 {
		return nil, l.warnings, errs.New("config.Load", errs.CodeConfigInvalid).
			WithDetail("%d problem(s): %s", len(l.problems), strings.Join(l.problems, "; "))
	}
	return cfg, l.warnings, nil
}

// modelFamily reduces a model id to a coarse vendor family so the verifier
// independence check can tell "qwen3.8-max vs qwen-plus" (same family, weak)
// from "qwen3.8-max vs deepseek-v4-pro" (different family, independent).
func modelFamily(model string) string {
	m := strings.ToLower(model)
	if i := strings.Index(m, "/"); i >= 0 {
		return m[:i]
	}
	for _, fam := range []string{"qwen", "deepseek", "kimi", "glm", "minimax", "step", "mimo", "gpt", "claude", "gemini", "llama", "mistral"} {
		if strings.HasPrefix(m, fam) {
			return fam
		}
	}
	return m
}

// Redacted returns a copy safe to log: every credential is replaced by a
// presence marker. Startup prints this so an operator can confirm what the
// process actually loaded without the log becoming a secret store.
func (c *Config) Redacted() map[string]any {
	return map[string]any{
		"env":                string(c.Env),
		"http_addr":          c.HTTP.Addr,
		"public_url":         c.HTTP.PublicURL,
		"db_url":             redactURL(c.DB.URL),
		"db_max_conns":       c.DB.MaxConns,
		"log_level":          c.Log.Level,
		"log_format":         c.Log.Format,
		"mail_transport":     string(c.Mail.Transport),
		"mail_from":          c.Mail.FromEmail,
		"resend_key_set":     c.Mail.ResendKey != "",
		"smtp_password_set":  c.Mail.SMTPPass != "",
		"session_secret_set": c.Auth.SessionSecret != "",
		"cookie_secure":      c.Auth.CookieSecure,
		"llm_base_url":       c.LLM.BaseURL,
		"llm_api_key_set":    c.LLM.APIKey != "",
		"llm_planner":        c.LLM.Planner,
		"llm_executor":       c.LLM.Executor,
		"llm_verifier":       c.LLM.Verifier,
		"llm_summarizer":     c.LLM.Summarizer,
		"llm_vision":         visionForPrint(c.LLM.Vision),
		// A path, not a secret, and printed so an operator can see at a glance
		// whether this deployment can write a parametric file at all.
		"cad_kernel":  cadForPrint(c.CAD.Python),
		"cad_scripts": c.CAD.AllowScripts,
		// The speech vendor and, separately, whether its backbone may be trained
		// on what FORGE says. FORGE_DATA_BOUNDARY answers that question for the
		// MODEL endpoint and not for this one, so a deployment that reads
		// answers aloud has a second egress the boundary does not describe.
		// Printed rather than implied for that reason alone.
		"tts_voice":           ttsForPrint(c.TTS),
		"tts_trains_on_input": ttsTrains(c.TTS),
		"media_enabled":       c.Media.Enabled,
		"media_udp_ports":     fmt.Sprintf("%d-%d", c.Media.UDPPortMin, c.Media.UDPPortMax),
		"media_max_parts":     c.Media.MaxParticipants,
		"media_transcribe":    c.Media.Transcribe,
		"media_silence_gap":   c.Media.SilenceGap.String(),
		"media_ice_servers":   len(c.Media.ICEServers),
		"data_boundary":       string(c.Security.DataBoundary),
		"shell_allowed":       shellAllowedForPrint(c.Security.ShellAllowed),
		"worker_concurrency":  c.Engine.WorkerConcurrency,
		"lease_duration":      c.Engine.LeaseDuration.String(),
		"max_attempts_task":   c.Engine.MaxAttemptsPerTask,
		"max_tasks_per_goal":  c.Engine.MaxTasksPerGoal,
		"max_wallclock_goal":  c.Engine.MaxWallClockPerGoal.String(),
	}
}

// visionForPrint says that image input is off rather than leaving a blank field.
// "No vision model" is a capability statement, and an empty string next to
// llm_vision reads as a value somebody forgot to fill in.
func visionForPrint(model string) string {
	if strings.TrimSpace(model) == "" {
		return "<none — image input is unavailable in this deployment>"
	}
	return model
}

// shellAllowedForPrint renders the shell allow-list so that "unrestricted" reads
// as a state rather than as an empty field somebody skims past. `config` is what
// an operator runs to answer "what is this deployment actually doing", and a
// blank line next to shell_allowed is the wrong answer to that question.
func shellAllowedForPrint(allowed []string) string {
	if len(allowed) == 0 {
		return "unrestricted: shell_run may execute anything the host can run — set FORGE_SHELL_ALLOWED_COMMANDS"
	}
	return strings.Join(allowed, ",")
}

// redactURL strips the password from a Postgres URL while keeping the host and
// database name, which are the parts an operator actually needs to see.
func redactURL(raw string) string {
	at := strings.LastIndex(raw, "@")
	if at < 0 {
		return raw
	}
	scheme := strings.Index(raw, "://")
	if scheme < 0 {
		return "<redacted>" + raw[at:]
	}
	userinfo := raw[scheme+3 : at]
	if i := strings.Index(userinfo, ":"); i >= 0 {
		userinfo = userinfo[:i] + ":<redacted>"
	}
	return raw[:scheme+3] + userinfo + raw[at:]
}

// cadForPrint says what an empty CAD interpreter MEANS.
//
// Same reason visionForPrint exists: an empty string in a config dump reads as
// something somebody forgot to fill in, and this one is a deliberate default
// that changes what the product can do.
// ttsForPrint names the vendor and backbone, never the key or the voice id.
func ttsForPrint(t TTSConfig) string {
	if !t.Configured() {
		return "<none — FORGE speaks through the model client>"
	}
	model := t.Model
	if model == "" {
		model = "<vendor default>"
	}
	return t.Provider + " (" + model + ")"
}

// ttsTrains reports whether the configured backbone may be trained on the text
// FORGE speaks.
//
// The allowlist lives in internal/tts and is deliberately not imported here:
// config must not depend on a vendor package to print a line. The names are
// duplicated, and tts_backbone_allowlist_test.go asserts the two agree —
// because a disagreement would print a reassurance that is false.
func ttsTrains(t TTSConfig) any {
	if !t.Configured() {
		return "n/a"
	}
	model := t.Model
	if model == "" {
		model = "s2.1-pro-free"
	}
	switch model {
	case "s2.1-pro", "s2-pro":
		return false
	default:
		// Unknown backbones are reported as training. Over-warning costs some
		// caution nobody needed; under-warning costs something unrecoverable.
		return true
	}
}

// TTSTrainsForTest exposes ttsTrains to the fence in internal/tts, which
// asserts this file and the provider's allowlist cannot drift apart. Exported
// for that fence alone — see backbone_allowlist_test.go for why the duplication
// is tolerated at all.
func TTSTrainsForTest(t TTSConfig) any { return ttsTrains(t) }

func cadForPrint(python string) string {
	if strings.TrimSpace(python) == "" {
		return "(none: parametric export is declared and refused)"
	}
	return python
}
