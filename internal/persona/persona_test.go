package persona

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImmutableCommitmentsAreAllPresent is a fence on the parts of FORGE's
// character that exist precisely because they are inconvenient at the moment
// they matter.
//
// Each of these is the kind of rule someone deletes while shipping something
// else — "the model already knows not to lie", "we can re-add the approval gate
// later". They are named individually so that removing one produces a test
// failure that says what it was for, rather than a diff nobody questions.
func TestImmutableCommitmentsAreAllPresent(t *testing.T) {
	required := map[string]string{
		"truthful-state": "Without it the agent can claim a tool ran, a check passed, or a human " +
			"approved something that never happened — the one failure that makes every other " +
			"guarantee unverifiable.",
		"no-fabrication": "Without it, invented citations and measurements get built on. " +
			"An admitted gap gets filled; a fabrication does not.",
		"evidence-over-fluency": "Without it, a convincing answer about something unchecked reads " +
			"identically to a verified one.",
		"human-authority": "Without it the agent can widen its own permissions, which means it has " +
			"no permission limit at all.",
		"safety-dissent": "Without it, 'be brief' or 'stop critiquing' silences a safety objection. " +
			"A concern that can be switched off is not a safeguard.",
	}

	found := map[string]bool{}
	for _, c := range Soul {
		found[c.ID] = true
		if c.Text == "" {
			t.Errorf("commitment %q has no text", c.ID)
		}
		if c.Why == "" {
			t.Errorf("commitment %q has no Why; a rule whose reason is lost gets deleted by the "+
				"next person who finds it inconvenient", c.ID)
		}
	}
	for id, why := range required {
		if !found[id] {
			t.Errorf("the immutable commitment %q has been removed.\n  %s", id, why)
		}
	}

	// And they must still be MARKED immutable, not merely present — an
	// immutable commitment silently downgraded to a tunable one is the same
	// removal wearing a disguise.
	immutable := map[string]bool{}
	for _, c := range ImmutableCommitments() {
		immutable[c.ID] = true
	}
	for id := range required {
		if found[id] && !immutable[id] {
			t.Errorf("commitment %q is present but no longer marked Immutable; "+
				"configuration could now relax it", id)
		}
	}
}

func TestNoDuplicateCommitmentIDs(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range Soul {
		if seen[c.ID] {
			t.Errorf("commitment id %q appears twice; tests and timeline entries reference these", c.ID)
		}
		seen[c.ID] = true
	}
	if len(Soul) < 8 {
		t.Errorf("the soul has only %d commitments; this fence would be weak", len(Soul))
	}
}

// TestSystemPromptCarriesEveryCommitment — a commitment that exists in the
// package but never reaches the model is decoration.
func TestSystemPromptCarriesEveryCommitment(t *testing.T) {
	prompt := SystemPrompt(DefaultCharacter(), "")
	for _, c := range Soul {
		// Compare on a distinctive opening fragment rather than the whole text,
		// so reformatting does not fail the test but omission does.
		fragment := firstWords(c.Text, 6)
		if !strings.Contains(prompt, fragment) {
			t.Errorf("commitment %q never reaches the model (looked for %q)", c.ID, fragment)
		}
	}
	if !strings.Contains(prompt, Name) {
		t.Error("the prompt does not name the agent")
	}
	// The durability framing is the thing that makes FORGE behave like a
	// resumable worker rather than a chat turn.
	for _, phrase := range []string{"reconstruct", "interrupted", "bounded amount of work"} {
		if !strings.Contains(prompt, phrase) {
			t.Errorf("the prompt is missing the durability framing %q", phrase)
		}
	}
}

// TestCritiqueIntensityNeverDisablesSafetyDissent is the specific loophole worth
// fencing: PRD RSN-04 allows critique intensity to be tuned but forbids
// safety-critical dissent from being switched off. A "low" setting must narrow
// style commentary, not silence a hazard.
func TestCritiqueIntensityNeverDisablesSafetyDissent(t *testing.T) {
	for _, intensity := range []string{"low", "normal", "high"} {
		ch := DefaultCharacter()
		ch.CritiqueIntensity = intensity
		prompt := SystemPrompt(ch, "")

		if !strings.Contains(prompt, firstWords(commitmentByID(t, "safety-dissent").Text, 6)) {
			t.Errorf("critique intensity %q dropped the safety-dissent commitment", intensity)
		}
		if intensity == "low" {
			if !strings.Contains(prompt, "This does not apply to safety") {
				t.Error("the 'low' setting must explicitly exempt safety objections, " +
					"or it reads as permission to stay quiet about a hazard")
			}
		}
	}
}

func TestVerbosityChangesToneNotSubstance(t *testing.T) {
	terse := SystemPrompt(Character{Verbosity: "terse"}, "")
	explanatory := SystemPrompt(Character{Verbosity: "explanatory"}, "")

	if !strings.Contains(terse, "Keep responses short") {
		t.Error("terse verbosity had no effect")
	}
	if !strings.Contains(explanatory, "Explain your reasoning") {
		t.Error("explanatory verbosity had no effect")
	}
	// Substance is unchanged either way.
	for _, c := range ImmutableCommitments() {
		frag := firstWords(c.Text, 6)
		if !strings.Contains(terse, frag) {
			t.Errorf("terse mode dropped %q", c.ID)
		}
		if !strings.Contains(explanatory, frag) {
			t.Errorf("explanatory mode dropped %q", c.ID)
		}
	}
}

func TestRoleFramingIsAppended(t *testing.T) {
	const framing = "You are planning. Produce a task list; do not call tools."
	prompt := SystemPrompt(DefaultCharacter(), framing)
	if !strings.Contains(prompt, framing) {
		t.Fatal("role framing did not reach the prompt")
	}
	// Framing comes AFTER the identity, so a role instruction cannot be read as
	// preceding — and thus overriding — the commitments.
	if strings.Index(prompt, framing) < strings.Index(prompt, "What I will and will not do") {
		t.Error("role framing precedes the commitments; it would read as overriding them")
	}
}

// ---------------------------------------------------------------------------
// avatar
// ---------------------------------------------------------------------------

// TestEveryAvatarStateIsStyled keeps the Go states and the stylesheet in step.
// A state with no CSS renders as a static blob identical to idle — which is
// exactly the failure the avatar exists to prevent, since the state that matters
// most ("waiting for you") stays true until a human acts.
func TestEveryAvatarStateIsStyled(t *testing.T) {
	css := readAsset(t, "avatar.css")
	for _, s := range AllAvatarStates() {
		selector := ".forge-avatar--" + string(s)
		if !strings.Contains(css, selector) {
			t.Errorf("avatar state %q has no rule (%s) and would render identically to idle", s, selector)
		}
	}
}

// TestAvatarStatesAreDistinguishableWithoutColour — colour alone fails for a
// colour-blind reader and on a monochrome screen. Every terminal state carries a
// glyph, and blocked carries a dash pattern.
func TestAvatarStatesAreDistinguishableWithoutColour(t *testing.T) {
	failed := AvatarSVG(StateFailed, 48)
	if !strings.Contains(failed, "fa-mark") {
		t.Error("the failed state relies on colour alone; it needs a glyph")
	}
	done := AvatarSVG(StateDone, 48)
	if !strings.Contains(done, "fa-mark") {
		t.Error("the done state relies on colour alone; it needs a glyph")
	}
	blocked := AvatarSVG(StateBlocked, 48)
	if !strings.Contains(blocked, "stroke-dasharray") {
		t.Error("the blocked state relies on colour alone; it needs a distinct outline")
	}
	// Working must show motion on the boundary, not the core.
	if !strings.Contains(AvatarSVG(StateWorking, 48), "fa-arc") {
		t.Error("the working state has no arc")
	}
}

// TestSigilAlwaysDrawsItsParts — the blades and ring are the boundary the work
// may not leave. They stay in every state, including failure and completion,
// because the boundary does not disappear when the work does.
func TestSigilAlwaysDrawsItsParts(t *testing.T) {
	for _, s := range AllAvatarStates() {
		svg := AvatarSVG(s, 48)
		for _, part := range []string{"fa-wings", "fa-blade", "fa-ring", "fa-core"} {
			if !strings.Contains(svg, part) {
				t.Errorf("state %q renders no %s", s, part)
			}
		}
		// Three blades, as on the character's ornament.
		if n := strings.Count(svg, "fa-blade fa-blade--"); n != 3 {
			t.Errorf("state %q draws %d blades, want 3", s, n)
		}
	}
}

// TestWorkingDoesNotSpinTheWings pins a correction made after looking at it.
// An earlier version rotated the whole wing assembly on the working state and it
// read as "broken and tumbling" rather than "working" — a hair ornament that
// revolves is a malfunction, not activity. The motion belongs on the boundary
// arc, which is also the more accurate signal: a tool call happens outside this
// process.
func TestWorkingDoesNotSpinTheWings(t *testing.T) {
	css := readAsset(t, "avatar.css")
	i := strings.Index(css, ".forge-avatar--working .fa-wings")
	if i < 0 {
		t.Skip("the working state no longer styles the wings at all, which is also fine")
	}
	rule := css[i:]
	if end := strings.Index(rule, "}"); end > 0 {
		rule = rule[:end]
	}
	if strings.Contains(rule, "fa-turn") || strings.Contains(rule, "rotate") {
		t.Error("the working state rotates the wing assembly; it reads as tumbling rather than working. " +
			"Put the motion on the boundary arc instead.")
	}
	if !strings.Contains(css, ".forge-avatar--working .fa-arc") {
		t.Error("the working state has no boundary arc, so nothing carries the motion")
	}
}

// TestEveryExpressionHasAPortraitEntry keeps the character-art manifest honest:
// an expression the code can ask for but that names no file would fall back to
// the sigil forever, silently.
func TestEveryExpressionHasAPortraitEntry(t *testing.T) {
	manifest := map[Expression]PortraitAsset{}
	for _, a := range PortraitManifest() {
		if a.File == "" {
			t.Errorf("expression %q names no file", a.Expression)
		}
		if a.Purpose == "" {
			t.Errorf("expression %q says nothing about what the crop should show; "+
				"the art cannot be produced from the character sheet without guessing", a.Expression)
		}
		manifest[a.Expression] = a
	}
	for _, e := range AllExpressions() {
		if _, ok := manifest[e]; !ok {
			t.Errorf("expression %q has no manifest entry", e)
		}
	}
	// Every avatar state must resolve to an expression that exists.
	covered := map[AvatarState]bool{}
	for _, a := range PortraitManifest() {
		for _, s := range a.UsedFor {
			covered[s] = true
		}
	}
	for _, s := range AllAvatarStates() {
		if _, ok := manifest[ExpressionFor(s)]; !ok {
			t.Errorf("state %q maps to expression %q, which has no manifest entry", s, ExpressionFor(s))
		}
		if !covered[s] {
			t.Errorf("state %q is not listed under any manifest entry's UsedFor", s)
		}
	}
}

// TestAvatarGradientIDsAreUnique — two avatars on one page sharing a gradient id
// makes the second adopt the first's colours, which reads as a bug in state
// reporting rather than in markup.
func TestAvatarGradientIDsAreUnique(t *testing.T) {
	a := AvatarSVG(StateIdle, 24)
	b := AvatarSVG(StateBlocked, 24)
	if idOf(a) == idOf(b) {
		t.Error("two different states share a gradient id")
	}
	if idOf(AvatarSVG(StateIdle, 24)) == idOf(AvatarSVG(StateIdle, 96)) {
		t.Error("the same state at two sizes shares a gradient id")
	}
}

func TestAvatarIsAccessible(t *testing.T) {
	for _, s := range AllAvatarStates() {
		svg := AvatarSVG(s, 48)
		if !strings.Contains(svg, `role="img"`) {
			t.Errorf("state %q has no img role", s)
		}
		if !strings.Contains(svg, `aria-label="FORGE: `+s.Label()) {
			t.Errorf("state %q has no descriptive aria-label; a screen reader would announce nothing", s)
		}
	}
}

// TestBlockedOutranksWorking — a goal with work in flight AND an open approval
// must show "waiting for you". Showing "working" would be true and useless: it
// is the blocked state a human can act on.
func TestBlockedOutranksWorking(t *testing.T) {
	got := AvatarStateForGoal("active", true, true, true)
	if got != StateBlocked {
		t.Errorf("state = %q, want blocked; a pending approval is the state a human can act on", got)
	}
	if AvatarStateForGoal("active", false, true, false) != StateWorking {
		t.Error("a running task should show working")
	}
	if AvatarStateForGoal("active", false, false, true) != StateThinking {
		t.Error("an in-flight model call should show thinking")
	}
	if AvatarStateForGoal("active", false, false, false) != StateIdle {
		t.Error("an active goal with nothing happening should show idle")
	}
	// A terminal goal outranks everything: a completed goal with a stale
	// in-flight flag must not animate as if it were still working.
	if AvatarStateForGoal("succeeded", true, true, true) != StateDone {
		t.Error("a succeeded goal must show done regardless of stale in-flight state")
	}
	if AvatarStateForGoal("failed", true, true, true) != StateFailed {
		t.Error("a failed goal must show failed")
	}
}

func TestAvatarLabelsAreReadable(t *testing.T) {
	// "Waiting for you" rather than "BLOCKED": a status word invites the reading
	// that something is broken, when in fact the agent is fine and the human is
	// the missing input.
	if StateBlocked.Label() != "Waiting for you" {
		t.Errorf("blocked label = %q", StateBlocked.Label())
	}
	for _, s := range AllAvatarStates() {
		if s.Label() == "" || s.Label() == "Unknown" {
			t.Errorf("state %q has no label", s)
		}
	}
}

func TestUnknownAvatarStateFallsBackVisibly(t *testing.T) {
	svg := AvatarSVG("nonsense", 40)
	if !strings.Contains(svg, "forge-avatar--idle") {
		t.Error("an unrecognised state should fall back to idle rather than rendering nothing")
	}
}

// --- helpers ---------------------------------------------------------------

func commitmentByID(t *testing.T, id string) Commitment {
	t.Helper()
	for _, c := range Soul {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("no commitment %q", id)
	return Commitment{}
}

func firstWords(s string, n int) string {
	fields := strings.Fields(s)
	if len(fields) > n {
		fields = fields[:n]
	}
	return strings.Join(fields, " ")
}

func idOf(svg string) string {
	const marker = `id="core-`
	i := strings.Index(svg, marker)
	if i < 0 {
		return ""
	}
	rest := svg[i+len(marker):]
	j := strings.Index(rest, `"`)
	return rest[:j]
}

func readAsset(t *testing.T, name string) string {
	t.Helper()
	// Walk up to the repo root so the test does not depend on where it is run.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		p := filepath.Join(dir, "internal", "httpapi", "assets", name)
		if b, err := os.ReadFile(p); err == nil {
			if len(b) == 0 {
				t.Fatalf("%s is empty; this fence would pass vacuously", name)
			}
			return string(b)
		}
		dir = filepath.Dir(dir)
	}
	t.Fatalf("could not find internal/httpapi/assets/%s", name)
	return ""
}
