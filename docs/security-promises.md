# What FORGE promises about security, and what it does not

**Written:** 2026-09-03 · **Status:** live — the promises below are enforced by
code, and each names the mechanism and the fence that holds it.

This document exists because three requirements — SEC-01 (data boundary),
SEC-05 (exfiltration controls) and SEC-07 (regulated intended use) — cannot be
implemented until somebody states what the product actually promises. They are
not features with an obvious shape. Each is a claim about what happens to
customer content, and a claim nobody has written down is one that gets made
casually, in a sales conversation or a README, by somebody who assumes the code
does more than it does.

So the promises are here, with their limits stated at the same volume. A
security document that lists only what is protected is the most dangerous kind:
it is read as a complete list.

---

## The rule these three decisions share

**A promise the code cannot keep is worse than no promise.**

FORGE runs on somebody else's infrastructure, calls somebody else's model, and
executes commands on a host it does not own. There is a great deal it cannot
observe, and for each of these requirements the tempting implementation is one
that *looks* like a control and is really a comment. The pattern to avoid:

- A field nobody sets. `ShellTool.Allowed` existed for the whole life of this
  codebase, was set only by tests, and every deployment ran an unrestricted
  shell while the code read as though a control was in place.
- A column nobody reads. `forge_projects.pack` was required to be non-empty and
  then consulted by nothing, so a project could record that a medical rule set
  applied while no rule anywhere applied it.
- A default that asserts something nobody checked. A data boundary defaulting
  to "no training" would be FORGE making a claim about a contract it has never
  seen — and it would read as a promise.

Each decision below is shaped by which of those it was closest to becoming.

---

## SEC-01 — Data boundary

> *Customer content not used for training absent affirmative opt-in.*

### The promise

**FORGE will not send customer content to a model endpoint whose data-handling
terms nobody has declared.** Training on customer content requires an
affirmative, recorded opt-in. It is never inferred, never defaulted, and never
assumed from silence.

### What is NOT promised

**That the provider honours its terms.** FORGE is not the trainer. It cannot
observe what happens to a request after it leaves, and any product claiming
otherwise about a third-party endpoint is claiming to know something it cannot.
What FORGE controls is whether a request is sent at all, and under what stated
terms.

The operator holds the contract with the provider and is the only party who
knows what it says. So the operator declares it, and FORGE refuses to run
without the declaration.

### Enforced by

`FORGE_DATA_BOUNDARY`, required wherever the LLM section is required — which is
every process that sends content. Two values, `no_training` and
`training_opted_in`, and **no default**, because every possible default is a
lie: one asserts a contract term nobody checked, the other consents on the
customer's behalf. Silence is not a posture, so silence stops the process.

The declared value appears in `forgectl config` output and in the startup
config log, so the posture a deployment is running under is answerable from the
operational record rather than from memory.

Fences: `TestTheDataBoundaryHasNoDefault`, `TestAnUndeclaredBoundaryIsNotAPosture`,
`TestTheBoundaryIsRequiredOnlyWhereContentLeaves`,
`TestTheSecurityDeclarationsAreVisibleInConfigPrint`.

---

## SEC-05 — Exfiltration controls

> *DLP, export control, redaction, egress allowlists.*

This is the requirement where the honest answer is furthest from the one the
words suggest, so the limits come first.

### What is NOT promised

**FORGE does not confine network egress.** The only tool that can reach the
network is `shell_run`, which executes `sh -c`. A permitted command can reach
any host the machine can reach, and no in-process check can change that — a
program cannot allowlist destinations for a subprocess it hands to a shell.
Confining egress is the deployment's job: network policy, an egress proxy, or
no route. FORGE says so rather than implying otherwise.

**There is no content inspection.** Nothing scans outbound content for customer
data, classified material or anything else. The PRD's "DLP" is not implemented
and is not claimed.

An earlier draft of this work implemented a destination allowlist. It was
deleted before it shipped, because its only enforcement point would have been a
tool that does not exist, and a control with no call site is the third item on
the list at the top of this document.

### The promise

What FORGE does confine:

1. **Which commands may run.** `shell_run` executes only commands on the
   deployment's allowlist. An empty list **denies everything** — see below.
2. **Which credentials a tool may resolve** (SEC-03), and resolved values are
   scrubbed from tool output, raw output and error text before the model or the
   database sees them, across the encodings something in the path actually
   applies. A value that survives anyway causes the whole result to be
   discarded.
3. **What each capability class may do.** Read, write, execute, simulate,
   export, deploy, transact and control are declared separately, and the risk
   classifier floors the tier of anything that deploys, transacts or actuates.

### Why an empty allowlist denies

It used to permit, and nothing ever set it. Wiring the field to configuration
was necessary and not sufficient: configuration gets forgotten, and what
matters is the direction of that failure.

- Empty-means-permit turns a forgotten line into an unrestricted shell nobody
  notices.
- Empty-means-refuse turns the same omission into a tool that refuses its first
  call and names the variable to set.

One of those is found by an operator in a minute. The other is found by
whoever goes looking for it. Production additionally refuses to start with an
unset list; everywhere else warns at startup and the tool refuses at the call.

Fences: `TestAnUnconfiguredShellRefusesEverything`,
`TestAnUnrestrictedShellIsRefusedInProduction`,
`TestAnUnrestrictedShellWarnsOutsideProduction`.

### Open seam

`cmd/forge-worker` passing `cfg.Security.ShellAllowed` into the registry is not
covered by a test — the registry is assembled in `main`, where nothing reaches
it. The failure direction is safe: an unwired allowlist is an empty one, and an
empty one refuses every call loudly rather than permitting silently. Worth
closing by extracting the registry construction into a testable function.

---

## SEC-07 — Regulated intended use

> *Regulated deployments stay inside validated intended use.*

### The promise

**A project can only be created in a domain pack this build is validated for,
and the regulated packs are not among them.** Attempting one is refused with
the authority that would have to establish the intended use.

Available here: `software`, `general`. Refused: `medical`, `civil`,
`aerospace`, `robotics`, `electrical`, `mechanical`.

This is not a gate on regulated work. It is the statement that regulated work
is **not available here at all** — which is what the PRD's own carve-out
already said about the medical pack ("educational and device-concept scope
only. Patient-specific use requires a separately validated deployment and is
not enabled by this codebase") and what this build can honestly enforce.

### Why this is not a configuration flag

The obvious shape is `FORGE_ENABLED_PACKS`, and it is the wrong one. An
environment variable that switches on patient-specific clinical use would make
the boundary of a regulated deployment something an operator crosses by editing
a file — no validation evidence, no qualified authority, no record of who
decided. It would quietly make the PRD's carve-out false.

Reaching regulated use therefore requires changing the table in
`internal/domain/pack`, in a commit, with whatever validation evidence that
deployment stands on. That is a higher bar than a config knob on purpose, and
it is the only bar this repository is in a position to enforce.

### What is NOT promised

**That work inside an available pack is safe.** `software` and `general` carry
their own boundaries — sandbox by default, review before merge or deploy,
lower autonomy where the domain is unknown — and those are enforced by the risk
tiers and approval gates, not by the pack.

**That the packs listed as unavailable are unsupported in principle.** They are
domains this build has not implemented the qualified review, licensed authority
or validated intended use for. The refusal says which, for each.

Fences: `TestNoRegulatedPackIsAvailableInThisBuild`,
`TestTheAvailablePacksAreTheOnesThisBuildImplements`,
`TestEnsureProject_RefusesAPackThisBuildIsNotValidatedFor`,
`TestEnsureProject_RefusesAPackThatIsNotOne`,
`TestLookupIsClosedAndForgivingAboutForm`.

---

## The rest of the security surface, in one line each

Summaries, not decisions. Each was settled by the wave that built it.

| | Position |
|---|---|
| **SEC-02** Encryption, SSO, MFA, device trust, RBAC | RBAC, MFA and device trust are built and fenced. **SSO is not implemented** — the one named gap. |
| **SEC-03** Secret isolation | The model receives scoped handles, never raw values; resolved values are scrubbed from tool output before the model or the database sees them. Scoping decides who receives a credential, not what they do with it — that limit is SEC-05's, above. |
| **SEC-04** Prompt-injection defence | Exists as mitigation and detection: documents, tool output and imported results are framed as untrusted input. Detection is not prevention and is not claimed as such. |
| **SEC-06** Audio privacy | Visible recording state, retention-free mode and independent audio deletion are built, enforced server-side, and fenced. |

---

## If you are evaluating FORGE against a compliance requirement

Read the "NOT promised" sections first. They are the parts a control
questionnaire will ask about, and they are accurate. The three things most
often assumed and not true here:

1. FORGE does not restrict where a permitted command sends data.
2. FORGE does not inspect content for sensitive data on the way out.
3. FORGE cannot verify what a model provider does with a request; it can only
   refuse to send one under undeclared terms.
