package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// verifierFraming frames the verifier adversarially on purpose.
//
// A verifier asked "is this right?" agrees. A verifier asked to find the reason
// it is wrong, and to treat an unevidenced claim as unsupported by default, is
// doing something different from the executor rather than the same thing twice.
const verifierFraming = `You are VERIFYING work that something else claims to have completed. You did not
do this work and you have no stake in it being right.

Your job is to find the reason it is NOT done. Look for:

- Claims with no evidence behind them. "The tests pass" with no command output is
  an assertion, not a result.
- Evidence that does not actually support the claim it is attached to. A file
  that exists does not show that its contents are correct. A build succeeding
  does not show that a feature works.
- The task's stated expected output, compared against what was actually produced.
- Work that was skipped and reported as unnecessary.
- Side effects the summary does not mention.

Default to NOT verified. Verification is a positive finding that requires
evidence; the absence of a reason to doubt is not the same as a reason to accept.

Reply with JSON only:

{
  "verified": true | false,
  "confidence": "high" | "medium" | "low",
  "reasoning": "what you checked and what you concluded",
  "unsupported_claims": ["claims made without evidence"],
  "missing_checks": ["what would need to be run or observed to settle this"],
  "recommendation": "accept" | "recheck" | "reject"
}`

// Verifier independently checks a completed task.
//
// # Independence is the whole point
//
// PRD SAF-03 requires a high-risk conclusion to be checked by a method
// independent of the one that produced it. This runs on llm.RoleVerifier, which
// is configured to a DIFFERENT model family from the executor — and
// config.Load warns at startup when they share one, because a model grading its
// own output is not an independent check while looking exactly like one.
//
// It is also given no tools. A verifier that can re-run the work can convince
// itself; one that can only inspect what was recorded has to judge the evidence
// that actually exists, which is the question anyone reading the audit trail
// later will be asking too.
type Verifier struct {
	client llm.Client
	char   persona.Character
	// characters resolves the project's character (PRD RSN-04). Optional; nil
	// verifies every project with the constructed character.
	characters *CharacterStore
}

// NewVerifier returns a verifier.
func NewVerifier(client llm.Client, char persona.Character) *Verifier {
	return &Verifier{client: client, char: char}
}

// WithCharacters makes verification honour the project's critique intensity.
func (v *Verifier) WithCharacters(s *CharacterStore) *Verifier { v.characters = s; return v }

// Verdict is a verification result.
type Verdict struct {
	Verified          bool     `json:"verified"`
	Confidence        string   `json:"confidence"`
	Reasoning         string   `json:"reasoning"`
	UnsupportedClaims []string `json:"unsupported_claims"`
	MissingChecks     []string `json:"missing_checks"`
	Recommendation    string   `json:"recommendation"`

	// Model records which model produced this verdict, so an auditor can confirm
	// the independence claim rather than take it on trust.
	Model string    `json:"model"`
	Usage llm.Usage `json:"-"`
}

// Verify checks a task's claimed outcome.
func (v *Verifier) Verify(ctx context.Context, tc *TaskContext, outcome *Outcome, toolEvidence []string) (*Verdict, error) {
	const op = "agent.Verifier.Verify"

	var b strings.Builder
	fmt.Fprintf(&b, "## The task\n\n%s\n\n%s\n", tc.Task.Title, tc.Task.Instruction)
	if len(tc.Task.ExpectedOutput) > 0 && string(tc.Task.ExpectedOutput) != "{}" {
		fmt.Fprintf(&b, "\nExpected output:\n```json\n%s\n```\n", indentJSON(tc.Task.ExpectedOutput))
	}

	fmt.Fprintf(&b, "\n## What the executor claims\n\nStatus: %s\n\n%s\n", outcome.Status, outcome.Summary)
	if len(outcome.Result) > 0 && string(outcome.Result) != "null" {
		fmt.Fprintf(&b, "\nResult:\n```json\n%s\n```\n", indentJSON(outcome.Result))
	}

	if len(outcome.Evidence) > 0 {
		b.WriteString("\n### Evidence it offers\n\n")
		for _, e := range outcome.Evidence {
			fmt.Fprintf(&b, "- %s\n", e)
		}
	} else {
		b.WriteString("\n### Evidence it offers\n\nNone was offered.\n")
	}

	if len(outcome.Assumptions) > 0 {
		b.WriteString("\n### What it assumed rather than checked\n\n")
		for _, a := range outcome.Assumptions {
			fmt.Fprintf(&b, "- %s\n", a)
		}
	}

	// Raw tool output is presented separately from the executor's account of it.
	// This is the point of keeping raw output (PRD AGT-06): the verifier compares
	// what the tools actually returned against what the executor said they
	// returned, which is a check the executor cannot perform on itself.
	if len(toolEvidence) > 0 {
		b.WriteString("\n## What the tools actually returned\n\n" +
			"This is raw tool output, not the executor's description of it. " +
			"Where the two disagree, this is what happened.\n\n")
		for _, t := range toolEvidence {
			fmt.Fprintf(&b, "```\n%s\n```\n\n", truncate(t, 4000))
		}
	} else {
		b.WriteString("\n## What the tools actually returned\n\nNo tools were run.\n")
	}

	resp, err := v.client.Complete(ctx, llm.Request{
		Role: llm.RoleVerifier,
		Messages: []llm.Message{
			{Role: llm.System, Content: persona.SystemPrompt(
				v.characters.For(ctx, tc.Goal.ProjectID, v.char), verifierFraming)},
			{Role: llm.User, Content: b.String()},
		},
		JSONMode:  true,
		MaxTokens: 4096,
	})
	if err != nil {
		return nil, err
	}

	var verdict Verdict
	if err := json.Unmarshal([]byte(extractJSON(resp.Content)), &verdict); err != nil {
		// A verdict that cannot be parsed is NOT a pass. Defaulting to verified
		// on a parse failure would turn every verifier hiccup into an
		// unnoticed acceptance.
		return nil, errs.Wrap(op, errs.CodeExternalProtocol, err).
			WithDetail("the verifier did not return usable JSON, so nothing was verified: %s",
				truncate(resp.Content, 400))
	}
	verdict.Model = resp.Model
	verdict.Usage = resp.Usage

	if err := verdict.validate(); err != nil {
		return nil, err
	}
	return &verdict, nil
}

// validate enforces internal consistency of a verdict.
func (v *Verdict) validate() error {
	const op = "agent.Verdict.validate"

	switch v.Recommendation {
	case "accept", "recheck", "reject":
	default:
		return errs.New(op, errs.CodeExternalProtocol).
			WithDetail("the verifier returned recommendation %q; expected accept, recheck or reject",
				v.Recommendation)
	}
	if strings.TrimSpace(v.Reasoning) == "" {
		return errs.New(op, errs.CodeExternalProtocol).
			WithDetail("the verifier gave a verdict with no reasoning, which cannot be reviewed")
	}
	// A verdict that says "verified" while also listing claims it could not
	// support is contradicting itself. Rather than pick a side, refuse it — a
	// self-inconsistent verdict is not evidence of anything.
	if v.Verified && len(v.UnsupportedClaims) > 0 && v.Recommendation == "accept" {
		return errs.New(op, errs.CodeExternalProtocol).
			WithDetail("the verifier marked the work verified while listing %d unsupported claim(s): %s. "+
				"A verdict that contradicts itself is not evidence",
				len(v.UnsupportedClaims), strings.Join(v.UnsupportedClaims, "; "))
	}
	return nil
}

// Passed reports whether the work may be recorded as verified.
//
// Both the boolean and the recommendation must agree. Requiring both is
// deliberate: it is the disagreement between them — "verified: true,
// recommendation: recheck" — that most often marks a verifier that was unsure
// and rounded up.
func (v *Verdict) Passed() bool { return v.Verified && v.Recommendation == "accept" }

// RequiresRework reports whether the task should be retried rather than failed.
func (v *Verdict) RequiresRework() bool { return v.Recommendation == "recheck" }

// VerificationRequired reports whether a task at this tier must be verified
// before it may be recorded as succeeded.
//
// R0 and R1 are exempt: reversible sandbox work verified by a second model call
// doubles the cost of every trivial step and trains nobody to read the result.
// The threshold is where consequence begins.
func VerificationRequired(tier engine.RiskTier) bool { return tier.AtLeast(engine.RiskR2) }
