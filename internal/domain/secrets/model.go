// Package secrets brokers credentials between a deployment's environment and the
// tools that need them, without the model ever holding one (PRD SEC-03).
//
// # The shape of the requirement
//
// "Model receives scoped handles, not raw secrets." Read literally: the model is
// told that `secret://github_token` exists and what it is for, it may pass that
// string into a tool call, and the executor swaps it for the real value at the
// tool boundary — if and only if that tool is granted that secret.
//
// # The half that is easy to forget
//
// Substituting on the way in is the obvious half and it is worth nothing on its
// own, because the tool's OUTPUT goes back to the model. A shell command that
// echoes its own environment, an HTTP client that logs the request it sent, an
// error message that quotes the header it failed on — each of them round-trips
// the value into the context window, and from there into the next tool call, the
// transcript, and the ledger.
//
// So redaction is not a hardening measure here, it is the other half of the
// mechanism. Every value this package resolves is scrubbed from the tool's
// output and raw output before either reaches the model or the database.
//
// # What is NOT claimed
//
//   - A tool that was legitimately given a value can still send it somewhere.
//     Scoping decides who receives a credential, not what they do with it. That
//     is SEC-05's problem (egress control) and is not implemented.
//   - FORGE does not hold the values. It reads one environment variable at the
//     moment a granted tool needs it. An attacker who can read the process
//     environment has the credentials regardless of anything here.
package secrets

import (
	"strings"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// HandlePrefix is what marks a handle in a tool argument.
//
// A URL-ish scheme rather than a bare name or `${...}`: it cannot be produced by
// accident, it survives JSON encoding unchanged, and a value that leaks into a
// log is instantly recognisable as a handle rather than as a credential.
const HandlePrefix = "secret://"

// Source is where a value comes from.
//
// One value today. It is an enum rather than a bare bool so that a deployment
// which wants FORGE to hold values has somewhere to add it — alongside SEC-02's
// key management, not before it.
type Source string

// SourceEnv reads the value from the process environment at the moment of use.
const SourceEnv Source = "env"

// Valid reports whether s is a recognised source.
func (s Source) Valid() bool { return s == SourceEnv }

// Secret is a declaration: a name, where its value comes from, and who may have it.
//
// There is no Value field, and that is the design rather than an omission. A
// struct with a value field is a struct that gets logged, serialised into an
// error, and put in a test fixture.
type Secret struct {
	ID        string
	ProjectID string
	Name      string
	Source    Source
	EnvVar    string
	// Description is shown to the model beside the handle so it knows when to
	// reach for this. It must not contain the value; nothing enforces that but
	// the person writing it, so DescribeForModel says so at the point of use.
	Description string

	// Tools that may receive this. Empty means nothing may: a secret somebody
	// declared and has not yet thought about is readable by nothing.
	Tools []string

	RevokedAt     *time.Time
	RevokedBy     *string
	RevokedReason string

	CreatedBy string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Revoked reports whether this secret has been withdrawn.
func (s *Secret) Revoked() bool { return s.RevokedAt != nil }

// Handle is the string the model uses to refer to this secret.
func (s *Secret) Handle() string { return HandlePrefix + s.Name }

// GrantedTo reports whether a named tool may receive this secret.
//
// Deny by default, and an exact match rather than a prefix or a pattern. A
// pattern is a rule somebody has to re-read to evaluate, and the reading always
// happens at the wrong moment.
func (s *Secret) GrantedTo(tool string) bool {
	if s.Revoked() {
		return false
	}
	for _, t := range s.Tools {
		if t == tool {
			return true
		}
	}
	return false
}

// ValidateName checks a handle name is usable.
//
// Lowercase and narrow, matching forge_secrets_name_shape. A name that differs
// only by case would be two handles the model cannot tell apart and one secret
// somebody thinks they revoked.
func ValidateName(name string) error {
	const op = "secrets.ValidateName"

	if name == "" {
		return errs.New(op, errs.CodeValidationFailed).WithDetail("a secret needs a name")
	}
	if len(name) > 63 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("the name %q is %d characters; the maximum is 63", name, len(name))
	}
	if name != strings.ToLower(name) {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("secret names are lowercase; %q would be a second handle the model cannot tell from %q",
				name, strings.ToLower(name))
	}
	for i, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r == '_' && i > 0)
		if !ok {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("secret names hold lowercase letters, digits and underscores, and start with a letter or digit; %q does not", name)
		}
	}
	return nil
}

// Validate checks a declaration before it is written.
func (s *Secret) Validate() error {
	const op = "secrets.Secret.Validate"

	if err := ValidateName(s.Name); err != nil {
		return err
	}
	if !s.Source.Valid() {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("source %q is not recognised; this build reads values from the process environment only", s.Source)
	}
	if strings.TrimSpace(s.EnvVar) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("secret %q must name the environment variable its value is read from", s.Name)
	}
	if strings.TrimSpace(s.ProjectID) == "" || strings.TrimSpace(s.CreatedBy) == "" {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("a secret must name its project and who declared it")
	}
	return nil
}

// FindHandles returns every handle referenced in a string, in order and without
// duplicates.
//
// Scanning rather than parsing, because a handle can appear anywhere a tool
// takes a string: inside a header value, inside a URL, inside a command line. A
// parser would have to understand every argument shape a tool might invent.
//
// The name is taken greedily over the permitted alphabet, so `secret://tok/x`
// yields `tok` and leaves `/x` in place — which is what a caller composing a URL
// from a handle would mean.
func FindHandles(s string) []string {
	var out []string
	seen := map[string]bool{}

	for i := 0; ; {
		j := strings.Index(s[i:], HandlePrefix)
		if j < 0 {
			return out
		}
		start := i + j + len(HandlePrefix)
		end := start
		for end < len(s) {
			r := s[end]
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
				end++
				continue
			}
			break
		}
		if name := s[start:end]; name != "" && !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
		i = start
		if end > start {
			i = end
		}
	}
}
