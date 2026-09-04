package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/engine"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/domain/memory"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/llm"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/persona"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/clock"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/db"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Materially different options, with tradeoffs, against stated criteria
// (PRD RSN-03).
//
// # What this is not
//
// The decision log (MEM-03) records alternatives with a required reason for
// each rejection. That is the record of a choice ALREADY MADE — written
// afterwards, by whoever made it. It cannot be what RSN-03 asks for, because by
// the time it exists the person the options were meant to help has decided.
// This is the other half: the offer, before the choice, and the choice lands in
// the decision log when it is made.
//
// # Why "materially different" is decided by the criteria and not by reading
//
// The obvious implementation is to ask a model for three options and print
// them. What comes back is usually one real option and two strawmen, or three
// phrasings of the same plan, and neither is detectable by looking at the
// prose: both read exactly like a considered comparison. "Materially different"
// as a property of wording has no test.
//
// Against stated criteria it has one. Two options that stand the same on every
// criterion ARE the same option: if the reader is supposed to be able to tell
// them apart, the criterion that tells them apart is the one that is missing.
// And an option that is at least as good as every other on everything is not
// one of a set of options — it is a recommendation with company. Both are
// arithmetic over the ratings, and neither is a judgement about writing.
//
// So the three checks below are:
//
//   - every option rates every criterion (the criterion an option skips is
//     reliably the one it loses on);
//   - no two options have the same ratings (materially different);
//   - no option beats every other on everything (there is an actual tradeoff).
//
// # Why the criteria have to exist first, in the record
//
// A model asked for criteria and options in one breath writes the criteria its
// preferred option wins on. The output is indistinguishable from an honest
// comparison, which is precisely why the ordering cannot be left to the prompt.
// Criteria are written by a separate command, stored on the goal, and
// `Offer` refuses to run without them — so "the criteria were prior" is a fact
// about the row rather than a claim about the reasoning. Restating criteria
// clears any options they had not yet judged; see StateCriteria.
//
// # Why an offer holds the goal
//
// RSN-02 taught this the expensive way: an instruction to ask is not a gate,
// and a question nobody has to answer changes nothing. The same is true of an
// offer. Once FORGE has put a choice in front of somebody, consequential work
// (r2 and above, the same boundary RSN-02 uses and for the same reason) waits
// until one of the options is chosen. Below r2 the work proceeds — exploration
// that stops dead on an open question teaches people to stop asking.
//
// Holding is an addition to what RSN-03 literally asks for. Without it the
// option set is a printout, and this codebase has now twice shipped a feature
// whose only failure was that nothing called it.

// Standing is how well one option does on one criterion.
//
// Three levels, deliberately. Two of the checks are comparisons, so they need
// an ORDER over standings and nothing finer than one. A 1-10 score would be
// more precise and less true: the distance between a 6 and a 7 is not a fact
// about the world, it is a token a model emitted, and storing it invites
// arithmetic that means nothing.
type Standing string

const (
	StandingStrong   Standing = "strong"
	StandingAdequate Standing = "adequate"
	StandingWeak     Standing = "weak"
)

// Valid reports whether s is one of the three levels.
func (s Standing) Valid() bool {
	switch s {
	case StandingStrong, StandingAdequate, StandingWeak:
		return true
	}
	return false
}

// rank orders standings so options can be compared. Higher is better.
func (s Standing) rank() int {
	switch s {
	case StandingStrong:
		return 2
	case StandingAdequate:
		return 1
	}
	return 0
}

// Criterion is one stated basis for choosing.
//
// Written before any option exists. Key is what ratings refer to, so it is
// stable and short; Statement is what it actually measures, because a criterion
// called "cost" that turns out to have meant engineering time rather than money
// changes which option should have won.
type Criterion struct {
	Key       string `json:"key"`
	Statement string `json:"statement"`
}

// Rating is where one option stands on one criterion, and why.
//
// Note is required for the same reason the decision log requires a reason per
// rejected alternative: a bare "weak" is a score, not an argument, and nobody
// can disagree with it.
type Rating struct {
	Criterion string   `json:"criterion"`
	Standing  Standing `json:"standing"`
	Note      string   `json:"note"`
}

// Option is one way the goal could be met.
type Option struct {
	Key   string `json:"key"`
	Title string `json:"title"`
	// Approach is what would actually be done — enough that somebody choosing
	// it knows what they are agreeing to, not a slogan.
	Approach string   `json:"approach"`
	Ratings  []Rating `json:"ratings"`
}

// standing returns where this option stands on a criterion, or "" if it is
// silent about it.
func (o Option) standing(key string) Standing {
	for _, r := range o.Ratings {
		if r.Criterion == key {
			return r.Standing
		}
	}
	return ""
}

// dominates reports whether o is at least as good as other on every criterion
// and strictly better on at least one.
func (o Option) dominates(other Option, criteria []Criterion) bool {
	better := false
	for _, c := range criteria {
		switch a, b := o.standing(c.Key).rank(), other.standing(c.Key).rank(); {
		case a < b:
			return false
		case a > b:
			better = true
		}
	}
	return better
}

// OptionSet is one open choice.
type OptionSet struct {
	// Question is the choice being made, in one line.
	Question string   `json:"question"`
	Options  []Option `json:"options"`
	// NoMaterialChoice, when non-empty, means there was only one sensible way to
	// do this and FORGE said so instead of manufacturing company for it.
	//
	// This exists because the checks below are strict, and a strict check with
	// no honest way to decline is a pressure to fabricate. Refusing to invent
	// two losers is the behaviour RSN-03 wants, not a failure to comply with it.
	NoMaterialChoice string `json:"no_material_choice"`
	// Refused carries what an earlier attempt was refused for, when there was
	// one. Shown rather than swallowed: a set that took two tries is a different
	// artefact from one that passed first time — either the criteria are hard to
	// argue against honestly, or the model reached for a strawman and was sent
	// back, and the person about to choose should know which.
	Refused []string  `json:"-"`
	Usage   llm.Usage `json:"-"`
	Model   string    `json:"-"`
}

// ValidateCriteria checks the basis for choosing before anything is judged
// against it.
func ValidateCriteria(criteria []Criterion) error {
	const op = "agent.ValidateCriteria"

	// One criterion cannot express a tradeoff. On a single basis for choosing
	// there is nothing to trade: either the options tie, in which case they are
	// the same option, or one of them simply wins. This is arithmetic, not
	// pedantry, and it is refused here — where somebody can still fix it — rather
	// than later as an unsatisfiable option set.
	if len(criteria) < 2 {
		return errs.New(op, errs.CodeValidationFailed).
			WithDetail("choosing needs at least two criteria, and %d were given. With a single basis "+
				"for choosing there is no tradeoff to weigh: one option is simply better. Name the "+
				"other thing you care about — what would make you pick the slower one?", len(criteria))
	}
	seen := map[string]bool{}
	for i, c := range criteria {
		if strings.TrimSpace(c.Key) == "" {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("criterion %d has no key; ratings refer to criteria by key", i+1)
		}
		if strings.TrimSpace(c.Statement) == "" {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("criterion %q says nothing about what it measures. A criterion nobody can "+
					"apply the same way twice is not a stated criterion", c.Key)
		}
		if seen[c.Key] {
			return errs.New(op, errs.CodeValidationFailed).
				WithDetail("two criteria share the key %q; a rating naming it would be ambiguous", c.Key)
		}
		seen[c.Key] = true
	}
	return nil
}

// Validate checks an offered set against the criteria it was argued on.
//
// Every refusal here is a refusal of the MODEL's output, so the code is the one
// used for a protocol failure rather than a user error: the person who asked
// did nothing wrong.
func (s *OptionSet) Validate(criteria []Criterion) error {
	const op = "agent.OptionSet.Validate"

	if s.NoMaterialChoice != "" {
		// Declining is a complete answer, and it carries no options to check.
		return nil
	}
	if err := ValidateCriteria(criteria); err != nil {
		return err
	}
	if strings.TrimSpace(s.Question) == "" {
		return errs.New(op, errs.CodeExternalProtocol).
			WithDetail("the option set does not say what is being chosen")
	}
	// A set of one is a recommendation. It may well be the right answer — which
	// is what NoMaterialChoice is for, and saying so out loud is different from
	// presenting a single option as though it were a choice.
	if len(s.Options) < 2 {
		return errs.New(op, errs.CodeExternalProtocol).
			WithDetail("an option set needs at least two options and has %d. If there is genuinely only "+
				"one sensible approach, say so with no_material_choice rather than presenting one "+
				"option as a choice", len(s.Options))
	}

	keys := map[string]bool{}
	for i, o := range s.Options {
		if strings.TrimSpace(o.Key) == "" {
			return errs.New(op, errs.CodeExternalProtocol).
				WithDetail("option %d has no key; nobody could choose it", i+1)
		}
		if keys[o.Key] {
			return errs.New(op, errs.CodeExternalProtocol).
				WithDetail("two options share the key %q; choosing it would be ambiguous", o.Key)
		}
		keys[o.Key] = true

		if strings.TrimSpace(o.Title) == "" || strings.TrimSpace(o.Approach) == "" {
			return errs.New(op, errs.CodeExternalProtocol).
				WithDetail("option %q has no title or does not say what would actually be done", o.Key)
		}
		if err := checkRatings(op, o, criteria); err != nil {
			return err
		}
	}

	// Materially different, decided by the criteria rather than by the prose.
	if a, b := identicalPair(s.Options, criteria); a != "" {
		return errs.New(op, errs.CodeExternalProtocol).
			WithDetail("options %q and %q stand identically on every criterion, so they are one option "+
				"written twice. Either they differ in something nobody stated — add that criterion — "+
				"or there is one approach here and no_material_choice is the honest answer", a, b)
	}

	// And there has to be something to trade.
	if k := dominantOption(s.Options, criteria); k != "" {
		return errs.New(op, errs.CodeExternalProtocol).
			WithDetail("option %q is at least as good as every other option on every criterion, so this "+
				"is a recommendation with company rather than a set of options. Either a criterion "+
				"the others win on is missing, or the others are strawmen — and if %q really is "+
				"better in every way, no_material_choice says that honestly", k, k)
	}
	return nil
}

// checkRatings enforces that an option answers for every criterion, exactly
// once, and says why.
func checkRatings(op string, o Option, criteria []Criterion) error {
	rated := map[string]int{}
	for _, r := range o.Ratings {
		rated[r.Criterion]++
	}
	for _, c := range criteria {
		switch rated[c.Key] {
		case 1:
			// Fine.
		case 0:
			// The most useful check in this file. An option that quietly omits a
			// criterion is not an option with a gap in its paperwork — it is an
			// option hiding the criterion it loses on, and the omission is
			// invisible in a rendered comparison unless something counts.
			return errs.New(op, errs.CodeExternalProtocol).
				WithDetail("option %q does not say how it stands on %q. Every option answers for every "+
					"criterion; a criterion an option skips is the one it loses on", o.Key, c.Key)
		default:
			return errs.New(op, errs.CodeExternalProtocol).
				WithDetail("option %q rates %q %d times", o.Key, c.Key, rated[c.Key])
		}
	}
	known := map[string]bool{}
	for _, c := range criteria {
		known[c.Key] = true
	}
	for _, r := range o.Ratings {
		if !known[r.Criterion] {
			// Not harmless. A criterion invented at rating time is a criterion
			// chosen after the options were known, which is the whole thing the
			// prior-criteria rule exists to prevent.
			return errs.New(op, errs.CodeExternalProtocol).
				WithDetail("option %q rates %q, which is not one of the stated criteria. Criteria are "+
					"stated before the options and are not added while arguing them", o.Key, r.Criterion)
		}
		if !r.Standing.Valid() {
			return errs.New(op, errs.CodeExternalProtocol).
				WithDetail("option %q stands %q on %q; expected strong, adequate or weak",
					o.Key, r.Standing, r.Criterion)
		}
		if strings.TrimSpace(r.Note) == "" {
			return errs.New(op, errs.CodeExternalProtocol).
				WithDetail("option %q says it is %q on %q and does not say why. A standing with no "+
					"reason is a score; nobody can disagree with a score", o.Key, r.Standing, r.Criterion)
		}
	}
	return nil
}

// identicalPair returns the first two options that stand the same everywhere.
func identicalPair(options []Option, criteria []Criterion) (string, string) {
	seen := map[string]string{}
	for _, o := range options {
		var sig strings.Builder
		for _, c := range criteria {
			sig.WriteString(string(o.standing(c.Key)))
			sig.WriteByte('|')
		}
		if prior, ok := seen[sig.String()]; ok {
			return prior, o.Key
		}
		seen[sig.String()] = o.Key
	}
	return "", ""
}

// dominantOption returns the key of an option that beats every other one, or "".
func dominantOption(options []Option, criteria []Criterion) string {
	for _, o := range options {
		beatsAll := true
		for _, other := range options {
			if other.Key == o.Key {
				continue
			}
			if !o.dominates(other, criteria) {
				beatsAll = false
				break
			}
		}
		if beatsAll {
			return o.Key
		}
	}
	return ""
}

// Separating reports the criteria the options do not actually differ on.
//
// Not a refusal: options can honestly be equal on something that still had to
// be weighed, and refusing would push the model to invent a difference — the
// opposite of what this file is for. It is rendered, so a comparison padded
// with criteria that decided nothing looks padded.
func (s *OptionSet) Separating(criteria []Criterion) (separates, flat []Criterion) {
	// A set with nothing in it separates nothing. Only a hand-edited row reaches
	// here empty — Validate refuses fewer than two options — but this is called
	// from the renderer, and a panic while showing a goal would take out the one
	// command somebody runs to find out what state it is in.
	if len(s.Options) == 0 {
		return nil, criteria
	}
	for _, c := range criteria {
		first := s.Options[0].standing(c.Key)
		differs := false
		for _, o := range s.Options[1:] {
			if o.standing(c.Key) != first {
				differs = true
				break
			}
		}
		if differs {
			separates = append(separates, c)
		} else {
			flat = append(flat, c)
		}
	}
	return separates, flat
}

// adviserFraming is what FORGE is told when it is offering a choice.
//
// The rules it states are the same rules Validate enforces, and that is the
// point of stating them: the check is the mechanism, and the prompt is what
// makes the check usually unnecessary. Neither replaces the other — an
// instruction to be honest that nothing verifies is a hope.
const adviserFraming = `You are OFFERING A CHOICE. You do not decide, and you have no tools.

Somebody has stated the criteria they will judge by. They are listed below. They
were written before you saw this and you may not add to them, drop them, or
reinterpret them.

Offer options for how this goal could be met. An option is a way of doing the
work, not a phrasing of it: two options that differ only in wording are one
option, and a person choosing between them is being given busywork.

Rules, each of which is CHECKED after you answer:

- Every option rates EVERY criterion, as "strong", "adequate" or "weak", with a
  short note saying why it stands there. An option that skips a criterion is
  refused — and in practice the skipped criterion is the one that option loses on.
- No two options may stand identically on every criterion. If two of your options
  do, they are the same option; either drop one or say what actually separates them.
- No option may be at least as good as all the others on everything. That is a
  recommendation with company, not a set of options, and it is refused. If your
  preferred approach really is better in every way, see the next rule.
- If there is genuinely only ONE sensible approach, say so in "no_material_choice"
  and offer nothing. Inventing two losers to surround your answer with is the
  exact failure this check exists to catch. Declining honestly is a good answer.

Be specific about what each option costs. The reader is about to commit work to
one of these, and the cost is the half they cannot get from the title.

Reply with JSON only, matching this shape exactly:

{
  "question": "the choice being made, in one line",
  "no_material_choice": "" or "why there is only one sensible approach here",
  "options": [
    {
      "key": "stable-kebab-identifier",
      "title": "short name for this approach",
      "approach": "what would actually be done, in enough detail to agree to",
      "ratings": [
        {"criterion": "criterion-key-from-above", "standing": "strong|adequate|weak",
         "note": "why it stands there"}
      ]
    }
  ]
}`

// Adviser offers choices. It never makes them.
type Adviser struct {
	client     llm.Client
	char       persona.Character
	characters *CharacterStore
}

// NewAdviser returns an adviser.
func NewAdviser(client llm.Client, char persona.Character) *Adviser {
	return &Adviser{client: client, char: char}
}

// WithCharacters makes an offer honour the project's critique intensity
// (PRD RSN-04).
func (a *Adviser) WithCharacters(s *CharacterStore) *Adviser { a.characters = s; return a }

// Model names the model that will do the arguing, so a caller can say so.
func (a *Adviser) Model() string { return a.client.ModelFor(llm.RolePlanner) }

// offered is the wire shape of a model's answer.
//
// Separate from OptionSet on purpose. OptionSet also carries the id of the
// decision the choice was recorded as, and unmarshalling model output straight
// into it would let a model name a decision that does not exist, or claim the
// choice was already recorded. Nothing the model writes can reach those fields
// if they are not in the struct it is decoded into.
type offered struct {
	Question         string   `json:"question"`
	NoMaterialChoice string   `json:"no_material_choice"`
	Options          []Option `json:"options"`
}

// Offer asks for options against criteria that already exist.
func (a *Adviser) Offer(ctx context.Context, goal *engine.Goal, criteria []Criterion) (*OptionSet, error) {
	const op = "agent.Adviser.Offer"

	if err := ValidateCriteria(criteria); err != nil {
		return nil, err
	}

	var user strings.Builder
	fmt.Fprintf(&user, "## Goal\n\n%s\n\n%s\n", goal.Title, goal.Statement)
	fmt.Fprintf(&user, "\nAutonomy ceiling: %s\nRisk ceiling: %s\n", goal.Autonomy, goal.RiskTier)
	user.WriteString("\n## The criteria, stated before you were asked\n\n")
	for _, c := range criteria {
		fmt.Fprintf(&user, "- `%s` — %s\n", c.Key, c.Statement)
	}
	if len(goal.CompletionCriteria) > 0 {
		// Different things, both worth showing: these say when the goal is DONE,
		// the criteria above say how to CHOOSE. An option that cannot satisfy the
		// completion criteria is not an option.
		user.WriteString("\nThe goal is complete when:\n")
		for _, c := range goal.CompletionCriteria {
			fmt.Fprintf(&user, "- %s\n", c.Statement)
		}
	}

	messages := []llm.Message{
		{Role: llm.System, Content: persona.SystemPrompt(
			a.characters.For(ctx, goal.ProjectID, a.char), adviserFraming)},
		{Role: llm.User, Content: user.String()},
	}

	// One retry, and the retry is told exactly what it broke.
	//
	// The checks fire on real models: the first live run of this command was
	// refused for an option that skipped the criterion it loses on, which is the
	// precise failure the rule exists to catch. Handing that straight back to a
	// person is the wrong shape twice over — they cannot fix a model's answer,
	// and re-running blind is another roll of the same dice at the same cost in
	// minutes.
	//
	// So the refusal becomes the next turn. Bounded at one retry: a loop that
	// argues until something passes eventually produces a set built to satisfy
	// the checker rather than the reader, and the honest way out
	// (no_material_choice) is already in front of it. If the second answer is
	// refused too, the person hears so, with the reason.
	var refused []string
	for attempt := 1; ; attempt++ {
		resp, err := a.client.Complete(ctx, llm.Request{
			Role:      llm.RolePlanner,
			Messages:  messages,
			JSONMode:  true,
			MaxTokens: 4096,
		})
		if err != nil {
			return nil, err
		}
		// A truncated set is missing its tail, and its tail is the options that
		// were still to come. Accepting it would present a comparison short an
		// option nobody knows about — and the checks would then pass on a set the
		// model did not finish writing.
		if resp.Truncated() {
			return nil, errs.New(op, errs.CodeExternalProtocol).
				WithDetail("the adviser was cut off at the token limit; the option set is incomplete " +
					"and was discarded rather than offered as a partial comparison")
		}

		var wire offered
		if err := json.Unmarshal([]byte(extractJSON(resp.Content)), &wire); err != nil {
			return nil, errs.Wrap(op, errs.CodeExternalProtocol, err).
				WithDetail("the adviser did not return usable JSON: %s", truncate(resp.Content, 400))
		}
		set := &OptionSet{
			Question:         wire.Question,
			NoMaterialChoice: wire.NoMaterialChoice,
			Options:          wire.Options,
			Refused:          refused,
			Usage:            resp.Usage,
			Model:            resp.Model,
		}

		err = set.Validate(criteria)
		if err == nil {
			return set, nil
		}
		if attempt >= offerAttempts {
			return nil, errs.New(op, errs.CodeExternalProtocol).
				WithDetail("FORGE could not offer an honest comparison in %d attempts. The last was "+
					"refused because: %s\n\nThat is usually the criteria rather than the model: if no "+
					"real approach can win on one of them, there is nothing to trade. Restate them "+
					"with `forgectl goal criteria`, or accept that there may be only one sensible "+
					"way to do this.", offerAttempts, refusalDetail(err))
		}
		refused = append(refused, refusalDetail(err))
		messages = append(messages,
			llm.Message{Role: llm.Assistant, Content: resp.Content},
			llm.Message{Role: llm.User, Content: "That set was refused by the check: " +
				refusalDetail(err) + "\n\nAnswer again, fixing exactly that. Do not fix it by " +
				"weakening an option you believe in — if you cannot offer materially different " +
				"options without inventing ones you do not believe in, say so in " +
				"no_material_choice instead."})
	}
}

// offerAttempts bounds how many times the adviser is asked.
//
// Two: one answer, and one chance to fix what a check named. See Offer.
const offerAttempts = 2

// refusalDetail is the human half of a refusal — what the check actually said,
// without the code and operation a model has no use for.
func refusalDetail(err error) string {
	var e *errs.Error
	if errors.As(err, &e) && e.Detail != "" {
		return e.Detail
	}
	return err.Error()
}

// ---------------------------------------------------------------------------
// What is stored, and what reads it
// ---------------------------------------------------------------------------

// storedSet is the option set as it sits on the goal.
//
// DecisionID and SupersedesDecision are written by ChooseOption and by
// StoreOptions respectively, never by a model. They are what keeps the decision
// log to one current answer per question when somebody re-opens a choice: the
// second decision supersedes the first rather than sitting beside it,
// contradicting it, with nothing to tell a reader which one is live.
type storedSet struct {
	Question         string   `json:"question"`
	NoMaterialChoice string   `json:"no_material_choice,omitempty"`
	Options          []Option `json:"options"`
	// DecisionID is the decision the choice was recorded as, once one was made.
	DecisionID string `json:"decision_id,omitempty"`
	// SupersedesDecision is the decision a previous, now-replaced choice was
	// recorded as. Carried forward when a new set replaces a chosen one.
	SupersedesDecision string `json:"supersedes_decision,omitempty"`
}

// optionHold is what the gate and the renderer read.
type optionHold struct {
	Set      *storedSet
	Criteria []Criterion
	// Chosen is the key somebody picked, or "" while the choice is open.
	Chosen string
}

// Open reports whether a choice is outstanding.
func (h *optionHold) Open() bool {
	return h != nil && h.Set != nil && h.Set.NoMaterialChoice == "" && h.Chosen == ""
}

// StateCriteria records the basis for choosing, before anything is judged.
//
// Restating clears any options already offered. Options were argued against the
// criteria that stood when they were written, and leaving them in place while
// the criteria changed underneath would produce a comparison whose ratings refer
// to something nobody can read any more — and would let somebody keep the
// options they liked and change the basis they were judged on, which is the
// laundering the prior-criteria rule exists to prevent, one step later.
func StateCriteria(ctx context.Context, pool *db.Pool, goalID string, criteria []Criterion) error {
	const op = "agent.StateCriteria"

	if err := ValidateCriteria(criteria); err != nil {
		return err
	}
	raw, err := json.Marshal(criteria)
	if err != nil {
		return errs.Wrap(op, errs.CodeSerializationFail, err)
	}
	tag, err := pool.Exec(ctx, `
		update forge_goals
		   set option_criteria = $2, options = null, chosen_option = null, updated_at = now()
		 where id = $1`, goalID, raw)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if tag.RowsAffected() == 0 {
		return errs.New(op, errs.CodeNotFound).WithDetail("no goal %s", goalID)
	}
	return nil
}

// CriteriaFor reads the stated criteria, which may be none.
func CriteriaFor(ctx context.Context, q db.Querier, goalID string) ([]Criterion, error) {
	const op = "agent.CriteriaFor"

	var raw []byte
	if err := q.QueryRow(ctx,
		`select option_criteria from forge_goals where id = $1`, goalID).Scan(&raw); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err).WithDetail("no goal %s", goalID)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	var out []Criterion
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errs.Wrap(op, errs.CodeStateCorrupt, err).
			WithDetail("goal %s has criteria that cannot be decoded; the row and this build disagree "+
				"about their shape", goalID)
	}
	return out, nil
}

// optionsFor reads the open choice, if there is one.
func optionsFor(ctx context.Context, q db.Querier, goalID string) (*optionHold, error) {
	const op = "agent.optionsFor"

	var rawOptions, rawCriteria []byte
	var chosen *string
	if err := q.QueryRow(ctx,
		`select options, option_criteria, chosen_option from forge_goals where id = $1`, goalID).
		Scan(&rawOptions, &rawCriteria, &chosen); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err).WithDetail("no goal %s", goalID)
	}
	if len(rawOptions) == 0 {
		return nil, nil
	}
	hold := &optionHold{Set: &storedSet{}}
	if err := json.Unmarshal(rawOptions, hold.Set); err != nil {
		return nil, errs.Wrap(op, errs.CodeStateCorrupt, err).
			WithDetail("goal %s has options that cannot be decoded", goalID)
	}
	if len(rawCriteria) > 0 {
		if err := json.Unmarshal(rawCriteria, &hold.Criteria); err != nil {
			return nil, errs.Wrap(op, errs.CodeStateCorrupt, err).
				WithDetail("goal %s has criteria that cannot be decoded", goalID)
		}
	}
	if chosen != nil {
		hold.Chosen = *chosen
	}
	return hold, nil
}

// StoreOptions writes an offered set onto the goal, replacing any previous one.
//
// The previous set's decision id is carried forward so that a choice made on
// this set supersedes the one made on the last, rather than joining it.
func StoreOptions(ctx context.Context, pool *db.Pool, goalID string, set *OptionSet) error {
	const op = "agent.StoreOptions"

	prior, err := optionsFor(ctx, pool, goalID)
	if err != nil {
		return err
	}
	stored := storedSet{
		Question:         set.Question,
		NoMaterialChoice: set.NoMaterialChoice,
		Options:          set.Options,
	}
	if prior != nil && prior.Set != nil {
		// Whichever decision is currently live for this question — the one this
		// set's choice will replace.
		stored.SupersedesDecision = prior.Set.DecisionID
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		return errs.Wrap(op, errs.CodeSerializationFail, err)
	}
	tag, err := pool.Exec(ctx, `
		update forge_goals
		   set options = $2, chosen_option = null, updated_at = now()
		 where id = $1 and option_criteria is not null`, goalID, raw)
	if err != nil {
		return errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	if tag.RowsAffected() == 0 {
		// The criteria are gone, or the goal is. Either way this set was argued
		// against something that is no longer on the row, and writing it would
		// produce options nobody can read the ratings of.
		return errs.New(op, errs.CodeConflict).
			WithDetail("goal %s has no stated criteria to have argued these options against; state them "+
				"first with `forgectl goal criteria %s ...`", goalID, goalID)
	}
	return nil
}

// gateOnOptions decides whether a goal with an open choice may start.
//
// The r2 boundary is RSN-02's, for RSN-02's reason: r2 is consequential digital
// change, and a system that refused to explore anything while a choice was open
// would teach people not to ask for options.
//
// Unlike the clarification gate there is no assumption to label below r2. A
// question that goes unanswered leaves nothing behind, which is why RSN-02
// writes a node; an offer that goes unchosen leaves the entire set on the goal,
// where `goal show` renders it. Nothing was discarded, so there is nothing to
// reconstruct.
func gateOnOptions(hold *optionHold, goal *engine.Goal) error {
	if !hold.Open() || !goal.RiskTier.AtLeast(engine.RiskR2) {
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "this goal is %s — consequential work — and FORGE has put a choice in front of "+
		"you that nobody has made:\n\n  %s\n\n", goal.RiskTier, hold.Set.Question)
	for _, o := range hold.Set.Options {
		fmt.Fprintf(&b, "  %-20s %s\n", o.Key, o.Title)
	}
	fmt.Fprintf(&b, "\nChoose one with `forgectl goal choose %s <option> --as <your-email>`, then "+
		"replan so the plan is built on it. Below r2 the same open choice would not hold the "+
		"work; at r2 and above it does, because starting one of these approaches is most of "+
		"the way to having chosen it.", goal.ID)

	return errs.New("agent.PlanApplier.Activate", errs.CodeValidationFailed).WithDetail("%s", b.String())
}

// ChooseRequest is somebody picking one of the options.
type ChooseRequest struct {
	GoalID    string
	OptionKey string
	// ByUserID is required. PRD SAF-05's principle applied to a choice rather
	// than an approval: an unattributed decision cannot be questioned, and the
	// decision log refuses to store one.
	ByUserID string
	// Why is the reason in the chooser's own words. Optional in general, and
	// REQUIRED when the chosen option is not better than a rejected one on any
	// stated criterion — see ChooseOption.
	Why string
}

// ChoiceResult is what was chosen and what it was recorded as.
type ChoiceResult struct {
	Option     Option
	DecisionID string
	// Superseded names the decision this choice replaced, when the choice was
	// re-opened and made again.
	Superseded string
}

// ChooseOption records a choice and writes it to the decision log.
//
// # Why the decision log and not just the column
//
// The column answers "is this goal held". The decision log answers "why is it
// built this way", six months later, when the goal is long gone — and it is
// already the place this product keeps that. Recording it here rather than in
// each caller is the same rule the activation gate follows: two implementations
// of one durable fact eventually disagree, and the one that is out of date is
// always the one somebody reads.
//
// # Why a losing choice must be explained in words
//
// Every rejected option gets a reason, because MEM-03 requires one and because a
// list of rejections with no reasons looks like the alternatives were weighed
// when nothing records that they were. Most of those reasons can be derived: the
// criteria the rejected option stands worse on ARE why it was not chosen.
//
// When there are none — when somebody picks an option that no stated criterion
// prefers — the derivation has nothing to say, and that is not a gap to paper
// over with a sentence like "not selected". It means the real reason is not in
// the criteria at all. So it is asked for, and the choice is refused without it.
// This is the one place where choosing costs the chooser a sentence, and it is
// exactly the place where the record would otherwise be silently untrue.
func ChooseOption(ctx context.Context, pool *db.Pool, clk clock.Clock, log *logx.Logger,
	req ChooseRequest) (*ChoiceResult, error) {
	const op = "agent.ChooseOption"

	if strings.TrimSpace(req.ByUserID) == "" {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("choosing names the person who chose; an unattributed decision cannot be questioned")
	}
	hold, err := optionsFor(ctx, pool, req.GoalID)
	if err != nil {
		return nil, err
	}
	if hold == nil || hold.Set == nil {
		return nil, errs.New(op, errs.CodeConflict).
			WithDetail("goal %s has no options to choose from. Offer some with "+
				"`forgectl goal options %s`", req.GoalID, req.GoalID)
	}
	if hold.Set.NoMaterialChoice != "" {
		return nil, errs.New(op, errs.CodeConflict).
			WithDetail("there is no choice open on goal %s: FORGE found only one sensible approach — %s",
				req.GoalID, hold.Set.NoMaterialChoice)
	}
	if hold.Chosen != "" {
		return nil, errs.New(op, errs.CodeConflict).
			WithDetail("goal %s already chose %q, and that choice is in the decision log. Changing it is "+
				"a new decision rather than an edit to that one: re-open the choice with "+
				"`forgectl goal options %s`, and the next choice will supersede it",
				req.GoalID, hold.Chosen, req.GoalID)
	}

	var chosen *Option
	for i := range hold.Set.Options {
		if hold.Set.Options[i].Key == req.OptionKey {
			chosen = &hold.Set.Options[i]
			break
		}
	}
	if chosen == nil {
		keys := make([]string, 0, len(hold.Set.Options))
		for _, o := range hold.Set.Options {
			keys = append(keys, o.Key)
		}
		return nil, errs.New(op, errs.CodeNotFound).
			WithDetail("goal %s has no option %q; it offers %s",
				req.GoalID, req.OptionKey, strings.Join(keys, ", "))
	}

	alternatives, needsWhy := rejections(*chosen, hold.Set.Options, hold.Criteria, req.Why)
	if needsWhy && strings.TrimSpace(req.Why) == "" {
		return nil, errs.New(op, errs.CodeValidationFailed).
			WithDetail("no stated criterion prefers %q to every option you are rejecting, so the record "+
				"cannot say why it was chosen — and a rejection with no reason reads as though the "+
				"alternatives were weighed when nothing shows that they were. Say why in your own "+
				"words: `--why \"...\"`. That reason is worth more than the criteria here, because it "+
				"is the part nobody wrote down.", chosen.Key)
	}

	var projectID string
	if err := pool.QueryRow(ctx,
		`select project_id from forge_goals where id = $1`, req.GoalID).Scan(&projectID); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err).WithDetail("no goal %s", req.GoalID)
	}

	goalID := req.GoalID
	decision := &memory.Decision{
		ProjectID:    projectID,
		GoalID:       &goalID,
		AuthorID:     req.ByUserID,
		Title:        hold.Set.Question,
		Decision:     fmt.Sprintf("%s — %s", chosen.Title, chosen.Approach),
		Rationale:    rationale(*chosen, hold.Criteria, req.Why),
		Alternatives: alternatives,
		DecidedAt:    clk.Now(),
	}
	if hold.Set.SupersedesDecision != "" {
		decision.SupersedesID = &hold.Set.SupersedesDecision
	}
	recorded, err := memory.NewService(pool, clk, log).RecordDecision(ctx, decision)
	if err != nil {
		return nil, err
	}

	// The column is written after the log, not before. If recording fails the
	// goal is still held, which is the safe direction: the alternative leaves a
	// released goal whose choice exists nowhere anybody can read it.
	hold.Set.DecisionID = recorded.ID
	raw, err := json.Marshal(hold.Set)
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeSerializationFail, err)
	}
	if _, err := pool.Exec(ctx, `
		update forge_goals set options = $2, chosen_option = $3, updated_at = now()
		 where id = $1`, req.GoalID, raw, chosen.Key); err != nil {
		return nil, errs.Wrap(op, errs.CodeDatabaseUnavail, err)
	}
	return &ChoiceResult{
		Option:     *chosen,
		DecisionID: recorded.ID,
		Superseded: hold.Set.SupersedesDecision,
	}, nil
}

// rejections turns the options nobody chose into decision-log alternatives.
//
// needsWhy reports that the derivation came up empty for at least one rejected
// option — that the chosen option is not preferred by any stated criterion over
// something it beat anyway.
func rejections(chosen Option, all []Option, criteria []Criterion, why string) ([]memory.Alternative, bool) {
	out := make([]memory.Alternative, 0, len(all)-1)
	needsWhy := false

	for _, o := range all {
		if o.Key == chosen.Key {
			continue
		}
		var beats []string
		for _, c := range criteria {
			if chosen.standing(c.Key).rank() > o.standing(c.Key).rank() {
				beats = append(beats, fmt.Sprintf("%s (%s: %s)",
					c.Key, o.standing(c.Key), noteFor(o, c.Key)))
			}
		}
		reason := ""
		switch {
		case len(beats) > 0:
			reason = fmt.Sprintf("%s was preferred on %s.", chosen.Title, strings.Join(beats, "; "))
		default:
			// Nothing in the criteria says why this lost. The chooser's own words
			// are the only true answer, and ChooseOption refuses without them.
			needsWhy = true
			reason = fmt.Sprintf("No stated criterion preferred %s to this.", chosen.Title)
		}
		if strings.TrimSpace(why) != "" {
			reason += " " + strings.TrimSpace(why)
		}
		out = append(out, memory.Alternative{
			Option: fmt.Sprintf("%s — %s", o.Title, o.Approach),
			WhyNot: reason,
		})
	}
	return out, needsWhy
}

// noteFor returns why an option stands where it does on a criterion.
func noteFor(o Option, key string) string {
	for _, r := range o.Ratings {
		if r.Criterion == key {
			return r.Note
		}
	}
	return ""
}

// rationale writes the reasoning into the decision log.
//
// The derived half is marked as derived. A sentence assembled from ratings and a
// sentence a person wrote are different kinds of thing, and a reader six months
// from now has to be able to tell which one they are arguing with.
func rationale(chosen Option, criteria []Criterion, why string) string {
	var strong, weak []string
	for _, c := range criteria {
		switch chosen.standing(c.Key) {
		case StandingStrong:
			strong = append(strong, c.Key)
		case StandingWeak:
			weak = append(weak, c.Key)
		}
	}
	var b strings.Builder
	if s := strings.TrimSpace(why); s != "" {
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	b.WriteString("Derived from the ratings this option was offered with: ")
	if len(strong) > 0 {
		fmt.Fprintf(&b, "strong on %s", strings.Join(strong, ", "))
	} else {
		b.WriteString("strong on nothing")
	}
	if len(weak) > 0 {
		fmt.Fprintf(&b, "; weak on %s, which is the cost that was accepted", strings.Join(weak, ", "))
	}
	b.WriteString(".")
	return b.String()
}

// ---------------------------------------------------------------------------
// What the planner is told once somebody has chosen
// ---------------------------------------------------------------------------

// ChoiceStore reads a goal's settled choice for the planner.
//
// Nil is a legal value, like CharacterStore: a deployment that never offers
// options never has one to read, and planning proceeds unchanged.
type ChoiceStore struct{ pool *db.Pool }

// NewChoiceStore returns a store reading from forge_goals.
func NewChoiceStore(pool *db.Pool) *ChoiceStore {
	if pool == nil {
		return nil
	}
	return &ChoiceStore{pool: pool}
}

// For returns the goal's choice, or nil when there is none.
func (s *ChoiceStore) For(ctx context.Context, goalID string) (*optionHold, error) {
	if s == nil {
		return nil, nil
	}
	return optionsFor(ctx, s.pool, goalID)
}

// settledChoiceBrief is what a plan is told about a choice that was made.
//
// # Why this exists at all
//
// A choice nothing acts on is a survey. If the planner does not see which
// approach was chosen, it plans whichever one the model likes on this roll —
// and the person who chose is worse off than if they had never been asked,
// because the record now says they picked something the work then ignored.
//
// # Why the weaknesses are included
//
// The chosen option was chosen WITH its costs; those costs are the part the plan
// has to survive. A plan that is told "we chose the fast one" writes a fast
// plan. A plan told "we chose the fast one, and it is weak on reversibility
// because it writes to the shared instance" can put the backup step in.
// unreadable reports that the row names a choice the set does not contain, so
// the caller can say so rather than let it pass as "nobody chose".
func settledChoiceBrief(hold *optionHold) (brief string, unreadable bool) {
	if hold == nil || hold.Set == nil || hold.Chosen == "" {
		return "", false
	}
	var chosen *Option
	for i := range hold.Set.Options {
		if hold.Set.Options[i].Key == hold.Chosen {
			chosen = &hold.Set.Options[i]
			break
		}
	}
	if chosen == nil {
		// The row says a key that is not in the set. Reported as nothing rather
		// than guessed at: planning around the wrong option is worse than
		// planning around none. Not silently, though — the caller logs it, because
		// a plan that ignores a choice somebody made must not be the quiet path.
		return "", true
	}

	var b strings.Builder
	b.WriteString("\n## The approach was already chosen\n\n")
	fmt.Fprintf(&b, "Question: %s\n", hold.Set.Question)
	fmt.Fprintf(&b, "Chosen: %s — %s\n", chosen.Title, chosen.Approach)
	b.WriteString("\nThis is settled. Plan this approach; do not re-open it or substitute another.\n")

	var costs []string
	for _, c := range hold.Criteria {
		if chosen.standing(c.Key) == StandingWeak {
			costs = append(costs, fmt.Sprintf("- %s: %s", c.Statement, noteFor(*chosen, c.Key)))
		}
	}
	if len(costs) > 0 {
		b.WriteString("\nIt was chosen knowing it is weak on the following. These are the costs the " +
			"plan has to account for, not reasons to pick something else:\n")
		b.WriteString(strings.Join(costs, "\n"))
		b.WriteString("\n")
	}
	return b.String(), false
}

// Render writes an option set for a terminal.
func (h *optionHold) Render(goalID string) string {
	var b strings.Builder
	if h == nil || h.Set == nil {
		return ""
	}
	if h.Set.NoMaterialChoice != "" {
		fmt.Fprintf(&b, "\nFORGE found no material choice here:\n\n  %s\n\n", h.Set.NoMaterialChoice)
		b.WriteString("Nothing is held. Offering two approaches it did not believe in would have " +
			"looked like more consideration and been less.\n")
		return b.String()
	}

	fmt.Fprintf(&b, "\n%s\n\n", h.Set.Question)

	separates, flat := (&OptionSet{Options: h.Set.Options}).Separating(h.Criteria)
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprint(w, "OPTION\t")
	for _, c := range separates {
		fmt.Fprintf(w, "%s\t", c.Key)
	}
	fmt.Fprintln(w)
	for _, o := range h.Set.Options {
		marker := "  "
		if o.Key == h.Chosen {
			marker = "→ "
		}
		fmt.Fprintf(w, "%s%s\t", marker, o.Key)
		for _, c := range separates {
			fmt.Fprintf(w, "%s\t", o.standing(c.Key))
		}
		fmt.Fprintln(w)
	}
	w.Flush()

	for _, o := range h.Set.Options {
		fmt.Fprintf(&b, "\n%s — %s\n", o.Key, o.Title)
		fmt.Fprintf(&b, "  %s\n", o.Approach)
		for _, c := range h.Criteria {
			fmt.Fprintf(&b, "  · %-15s %-9s %s\n", c.Key, o.standing(c.Key), noteFor(o, c.Key))
		}
	}

	if len(flat) > 0 {
		b.WriteString("\nThese criteria did not separate the options — every option stands the same " +
			"on them:\n")
		for _, c := range flat {
			fmt.Fprintf(&b, "  · %s — %s\n", c.Key, c.Statement)
		}
	}

	// PRD RSN-05. Options are reasoning, not measurement, and a comparison table
	// laid out this neatly reads like data unless it says otherwise.
	b.WriteString("\nEvery standing above is PROPOSED — FORGE's argument, not a measurement.\n")
	if h.Chosen == "" {
		fmt.Fprintf(&b, "\nChoose one: forgectl goal choose %s <option> --as <your-email>\n", goalID)
	} else {
		fmt.Fprintf(&b, "\nChosen: %s. Replan so the plan is built on it: forgectl goal replan %s\n",
			h.Chosen, goalID)
	}
	return b.String()
}

// RenderOptions writes a goal's choice — open or settled — for a terminal.
//
// Exported so the CLI does not need the shape of what is stored. One renderer
// rather than one per surface: a comparison whose columns depend on which
// command printed it is a comparison somebody will read twice and remember
// differently.
func RenderOptions(ctx context.Context, q db.Querier, goalID string) (string, error) {
	hold, err := optionsFor(ctx, q, goalID)
	if err != nil {
		return "", err
	}
	return hold.Render(goalID), nil
}
