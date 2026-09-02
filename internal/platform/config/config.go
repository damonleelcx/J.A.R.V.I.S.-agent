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
}

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
	Converse       string
	RequestTimeout time.Duration
	MaxRetries     int
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
		BaseURL:        strings.TrimRight(l.str("FORGE_LLM_BASE_URL", "https://dashscope.aliyuncs.com/compatible-mode/v1"), "/"),
		APIKey:         l.requiredIn(SectionLLM, "FORGE_LLM_API_KEY"),
		Planner:        l.str("FORGE_LLM_PLANNER_MODEL", "qwen3.8-max"),
		Executor:       l.str("FORGE_LLM_EXECUTOR_MODEL", "qwen3.8-max"),
		Verifier:       l.str("FORGE_LLM_VERIFIER_MODEL", "deepseek-v4-pro"),
		Summarizer:     l.str("FORGE_LLM_SUMMARIZER_MODEL", "qwen3.8-flash"),
		Converse:       l.str("FORGE_LLM_CONVERSE_MODEL", "qwen-plus"),
		RequestTimeout: l.dur("FORGE_LLM_REQUEST_TIMEOUT", 3*time.Minute),
		MaxRetries:     l.intVal("FORGE_LLM_MAX_RETRIES", 3),
	}
	if set.has(SectionLLM) && modelFamily(cfg.LLM.Verifier) == modelFamily(cfg.LLM.Executor) {
		l.warnings = append(l.warnings, fmt.Sprintf(
			"verifier model %q shares a family with executor model %q: independent verification (PRD SAF-03) is weakened when one model grades its own output",
			cfg.LLM.Verifier, cfg.LLM.Executor))
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
		"worker_concurrency": c.Engine.WorkerConcurrency,
		"lease_duration":     c.Engine.LeaseDuration.String(),
		"max_attempts_task":  c.Engine.MaxAttemptsPerTask,
		"max_tasks_per_goal": c.Engine.MaxTasksPerGoal,
		"max_wallclock_goal": c.Engine.MaxWallClockPerGoal.String(),
	}
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
