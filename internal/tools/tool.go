// Package tools is FORGE's capability registry.
//
// Every tool declares a typed contract: what it takes, what it returns, what
// class of capability it exercises, what risk tier it sits at, whether it can be
// undone, and how long it may run. PRD AGT-01 requires read, write, execute,
// simulate, export, deploy, transact and control to be declared SEPARATELY —
// not collapsed into one "can act" flag — because a connector that may read a
// repository and one that may deploy from it are not the same permission, and a
// system that cannot tell them apart cannot enforce least privilege.
package tools

import (
	"context"
	"encoding/json"
	"time"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Capability is one class of thing a tool can do.
//
// Declared as a set per tool rather than a single level, because capabilities do
// not form a ladder: a tool that can transact is not "more" than one that can
// deploy, it is different, and a policy that ranks them will eventually permit
// the wrong one.
type Capability string

const (
	// CapRead observes without changing anything.
	CapRead Capability = "read"
	// CapWrite changes artifacts inside the project boundary.
	CapWrite Capability = "write"
	// CapExecute runs code or commands in a sandbox.
	CapExecute Capability = "execute"
	// CapSimulate runs a solver or model that produces evidence but changes
	// nothing outside itself.
	CapSimulate Capability = "simulate"
	// CapExport moves data across the project boundary — the capability that
	// matters for exfiltration (PRD SEC-05), which is why it is not "write".
	CapExport Capability = "export"
	// CapDeploy makes a change visible outside the workspace.
	CapDeploy Capability = "deploy"
	// CapTransact spends money or incurs an obligation.
	CapTransact Capability = "transact"
	// CapControl actuates something physical. Never granted by this codebase;
	// declared so that a connector claiming it can be refused by name rather
	// than by omission.
	CapControl Capability = "control"
)

// AllCapabilities returns every declared capability.
func AllCapabilities() []Capability {
	return []Capability{CapRead, CapWrite, CapExecute, CapSimulate,
		CapExport, CapDeploy, CapTransact, CapControl}
}

// Valid reports whether c is recognised.
func (c Capability) Valid() bool {
	for _, v := range AllCapabilities() {
		if v == c {
			return true
		}
	}
	return false
}

// Reversibility says whether a tool's effect can be taken back.
//
// This drives the approval gate more than the risk tier does: an irreversible
// action at a modest tier still deserves a human, because "we can roll that
// back" is the assumption most incident reports turn out to have made.
type Reversibility string

const (
	// ReversibleNone — the tool changes nothing. Reads and pure computation.
	ReversibleNone Reversibility = "no_effect"
	// ReversibleAutomatic — FORGE can undo it itself (a branch, a checkpointed
	// file write).
	ReversibleAutomatic Reversibility = "automatic"
	// ReversibleManual — a person can undo it, with effort.
	ReversibleManual Reversibility = "manual"
	// Irreversible — it cannot be undone. Sent mail, spent money, a published
	// release.
	Irreversible Reversibility = "irreversible"
)

// Contract is a tool's declaration.
//
// It is data rather than a set of methods so the whole registry can be rendered
// — into a model's tool list, into an operator's permission review, into the
// console — from one source. A capability surface that can only be enumerated by
// reading code is one nobody audits.
type Contract struct {
	// Name is the identifier the model calls. Stable: it appears in the
	// tool-call ledger and in idempotency keys.
	Name string
	// Description is what the model reads to decide when to call this. The
	// highest-leverage string in the contract — a vague one produces a tool
	// called at the wrong times and then blamed for the outcome.
	Description string
	// InputSchema is JSON Schema. Validated before the tool runs, so a
	// malformed call fails at the boundary with a message the model can act on
	// rather than inside the tool with a nil dereference.
	InputSchema json.RawMessage
	// Capabilities is the set this tool exercises.
	Capabilities []Capability
	// RiskTier is the tier an invocation sits at by default. A tool may raise it
	// per call — writing to a path outside the sandbox is not the same as
	// writing inside it — but never lower it.
	RiskTier engine.RiskTier
	// Reversibility of the tool's effect.
	Reversibility Reversibility
	// Timeout bounds one invocation. Required: a tool with no timeout can hold a
	// worker forever, and the lease will expire underneath it so the task runs
	// twice.
	Timeout time.Duration
	// Idempotent reports whether repeating an identical call is harmless. When
	// false, the ledger's key is checked BEFORE execution and a completed record
	// short-circuits the call.
	Idempotent bool
	// Available reports whether this tool can actually run here. A declared
	// connector with no backend returns false, and the executor refuses the call
	// with CONNECTOR_UNAVAILABLE rather than inventing a result.
	Available bool
	// UnavailableReason explains why, when Available is false. Shown to the
	// model and to the user, so "I could not do that" is specific.
	UnavailableReason string
}

// Validate checks a contract is usable before it is registered.
func (c Contract) Validate() error {
	const op = "tools.Contract.Validate"

	if c.Name == "" {
		return errs.New(op, errs.CodeInvariantViolated).WithDetail("a tool must have a name")
	}
	if c.Description == "" {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("tool %q has no description; the model has nothing to decide from", c.Name)
	}
	if len(c.InputSchema) == 0 {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("tool %q declares no input schema; arguments could not be validated", c.Name)
	}
	if len(c.Capabilities) == 0 {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("tool %q declares no capabilities; it cannot be permitted or refused by policy", c.Name)
	}
	for _, cap := range c.Capabilities {
		if !cap.Valid() {
			return errs.New(op, errs.CodeInvariantViolated).
				WithDetail("tool %q declares unknown capability %q", c.Name, cap)
		}
	}
	if !c.RiskTier.Valid() {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("tool %q declares unknown risk tier %q", c.Name, c.RiskTier)
	}
	if c.Timeout <= 0 {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("tool %q has no timeout; it could hold a worker until its lease expired, "+
				"at which point the task would be reclaimed and run a second time", c.Name)
	}
	if !c.Available && c.UnavailableReason == "" {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("tool %q is unavailable but does not say why; "+
				"\"I cannot do that\" without a reason is a dead end", c.Name)
	}
	// A tool that changes the world outside the sandbox must not claim to be a
	// read. This catches the copy-paste that makes a deploy look harmless to
	// every policy check downstream.
	if c.HasCapability(CapDeploy) || c.HasCapability(CapTransact) || c.HasCapability(CapControl) {
		if c.RiskTier.AtLeast(engine.RiskR2) == false {
			return errs.New(op, errs.CodeInvariantViolated).
				WithDetail("tool %q can deploy, transact or actuate but is declared at tier %q; "+
					"that would let it run without a human gate", c.Name, c.RiskTier)
		}
	}
	if c.Reversibility == Irreversible && !c.RiskTier.AtLeast(engine.RiskR2) {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("tool %q is irreversible but sits below R2; irreversibility is exactly "+
				"what an approval gate is for", c.Name)
	}
	return nil
}

// HasCapability reports whether the contract declares cap.
func (c Contract) HasCapability(cap Capability) bool {
	for _, v := range c.Capabilities {
		if v == cap {
			return true
		}
	}
	return false
}

// Mutating reports whether this tool changes anything at all.
func (c Contract) Mutating() bool {
	for _, cap := range c.Capabilities {
		switch cap {
		case CapWrite, CapExecute, CapExport, CapDeploy, CapTransact, CapControl:
			return true
		}
	}
	return false
}

// Invocation is one call.
type Invocation struct {
	// Tool is the contract name.
	Tool string
	// Input is the raw arguments, already validated against InputSchema.
	Input json.RawMessage
	// IdempotencyKey deduplicates this call across retries.
	IdempotencyKey string
	// TaskID and GoalID scope the call for the ledger and the audit trail.
	TaskID string
	GoalID string
	// Workspace is the directory the tool may touch. Empty means the tool does
	// not touch the filesystem.
	Workspace string
}

// Result is what a tool returns.
type Result struct {
	// Output is the structured result handed back to the model.
	Output json.RawMessage
	// Raw is the unedited output. PRD AGT-06: preserve raw outputs and keep tool
	// evidence distinguishable from model inference. The model sees Output; an
	// auditor reads Raw.
	Raw string
	// Evidence, when set, names what this result proves and how — so a
	// verification claim can point at something rather than assert itself.
	Evidence string
	// RiskTierUsed lets a tool raise its tier for a specific call. It may never
	// lower it; the executor takes the maximum of this and the contract.
	RiskTierUsed engine.RiskTier
}

// Tool is an executable capability.
type Tool interface {
	// Contract returns the declaration. Called often; must be cheap and constant.
	Contract() Contract
	// Run performs the invocation. It must respect ctx's deadline and must not
	// panic — the executor recovers, but a panicking tool has already lost its
	// own error message.
	Run(ctx context.Context, inv Invocation) (*Result, error)
}
