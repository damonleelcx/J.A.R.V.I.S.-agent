package persona

import (
	"fmt"
	"strings"
)

// FORGE's visual identity has two forms, and they exist for different jobs.
//
// # The sigil
//
// A winged core: three swept blades around a glowing centre, taken from the
// character's hair ornament. This is the working mark — it appears in table
// rows, tabs, headers, and anywhere a status must be legible at 24px.
//
// A character portrait cannot do that job. At 26px a detailed illustration is
// mush, and the thing a reader most needs to distinguish at a glance is whether
// FORGE is working, waiting for them, or stopped. So the sigil carries state and
// the portrait carries presence.
//
// # The portrait
//
// The full character, shown where there is room for her: the goal page, the
// console header, the sign-in screen. Per-state expressions come from the
// character sheet — see PortraitManifest for exactly which files are expected
// and what each one is for.
//
// The portrait is optional at runtime. When an expression asset is missing the
// surface falls back to the sigil rather than rendering a broken image, because
// a missing decorative asset must never take out a status indicator.

// Palette is the character's colour scheme, kept here as the single source so
// the stylesheet, the SVG, and any future asset generation agree.
const (
	// ColourShell is the white of the uniform.
	ColourShell = "#f7f8fa"
	// ColourGold is the trim.
	ColourGold = "#d9b25c"
	// ColourGoldDeep is the shadowed edge of the trim.
	ColourGoldDeep = "#a8823a"
	// ColourCore is the cyan of the ornament, the collar gem, and the wrist
	// display. This is the colour that means "active" throughout the interface.
	ColourCore = "#4fd8e8"
	// ColourCoreDeep is the core's shadowed edge.
	ColourCoreDeep = "#1c7f8e"
)

// AvatarState is what FORGE is currently doing.
//
// The avatar is a status indicator first. A mark that looks identical whether
// FORGE is working, waiting for a human, or dead teaches people to stop looking
// at it — and the most common failure of a long-running agent is that nobody
// notices it stopped.
type AvatarState string

const (
	// StateIdle — nothing to do. The core breathes slowly.
	StateIdle AvatarState = "idle"
	// StateThinking — a model call is in flight. The core pulses faster.
	StateThinking AvatarState = "thinking"
	// StateWorking — a tool is running. The wings turn: something is happening
	// outside this process and it is not instantaneous.
	StateWorking AvatarState = "working"
	// StateBlocked — waiting for a human. The most visually distinct state on
	// purpose: it stays true until somebody acts, and an agent waiting unnoticed
	// looks exactly like one that died.
	StateBlocked AvatarState = "blocked"
	// StateFailed — the goal ended badly. The core goes dark and still.
	StateFailed AvatarState = "failed"
	// StateDone — the goal completed. Settled, no motion.
	StateDone AvatarState = "done"
)

// AllAvatarStates returns every state.
func AllAvatarStates() []AvatarState {
	return []AvatarState{StateIdle, StateThinking, StateWorking, StateBlocked, StateFailed, StateDone}
}

// Valid reports whether s is recognised.
func (s AvatarState) Valid() bool {
	for _, v := range AllAvatarStates() {
		if v == s {
			return true
		}
	}
	return false
}

// Label is the short name shown beside the avatar.
//
// Written as a statement of fact rather than a status word: "Waiting for you"
// tells a reader what to do, where "BLOCKED" invites the reading that something
// is broken when in fact FORGE is fine and the human is the missing input.
func (s AvatarState) Label() string {
	switch s {
	case StateIdle:
		return "Idle"
	case StateThinking:
		return "Thinking"
	case StateWorking:
		return "Working"
	case StateBlocked:
		return "Waiting for you"
	case StateFailed:
		return "Stopped"
	case StateDone:
		return "Done"
	default:
		return "Unknown"
	}
}

// Expression is the character's face for a state, as drawn on the character
// sheet. Named separately from the state because several states share one
// expression, and because the sheet is the authority on what exists.
type Expression string

const (
	// ExpressionCalm — the neutral, level look. Idle.
	ExpressionCalm Expression = "calm"
	// ExpressionThoughtful — hand to chin, considering. Thinking.
	ExpressionThoughtful Expression = "thoughtful"
	// ExpressionFocused — narrowed, determined. Working.
	ExpressionFocused Expression = "focused"
	// ExpressionBright — the open smile. Done.
	ExpressionBright Expression = "bright"
)

// ExpressionFor maps a state onto the character's face.
//
// Blocked reuses Thoughtful and Failed reuses Calm, deliberately. The character
// sheet defines four expressions; inventing two more would mean either
// commissioning art that does not exist or picking one that misreads the moment.
// A level, waiting look for "waiting for you" is right; a distressed one would
// dramatise a state that is entirely normal.
func ExpressionFor(s AvatarState) Expression {
	switch s {
	case StateThinking, StateBlocked:
		return ExpressionThoughtful
	case StateWorking:
		return ExpressionFocused
	case StateDone:
		return ExpressionBright
	default:
		return ExpressionCalm
	}
}

// AllExpressions returns every expression the character sheet defines.
func AllExpressions() []Expression {
	return []Expression{ExpressionCalm, ExpressionThoughtful, ExpressionFocused, ExpressionBright}
}

// PortraitAsset is the file an expression expects.
type PortraitAsset struct {
	Expression Expression
	// File is the path under internal/httpapi/assets/portrait/.
	File string
	// Purpose says what the crop should show, so the asset can be produced from
	// the character sheet without guessing.
	Purpose string
	// UsedFor lists the avatar states that resolve to this expression.
	UsedFor []AvatarState
}

// PortraitManifest is the complete list of character art the interface can use.
//
// It exists so that "which files do I need and what should each contain?" has a
// written answer rather than being discoverable only by reading template code.
// Every file is OPTIONAL at runtime: a missing portrait falls back to the sigil,
// because a decorative asset must never be able to take out a status indicator.
func PortraitManifest() []PortraitAsset {
	return []PortraitAsset{
		{
			Expression: ExpressionCalm,
			File:       "portrait/calm.png",
			Purpose:    "Head and shoulders, level and unhurried. The default presence.",
			UsedFor:    []AvatarState{StateIdle, StateFailed},
		},
		{
			Expression: ExpressionThoughtful,
			File:       "portrait/thoughtful.png",
			Purpose:    "Considering — hand near the chin. Shown while reasoning, and while waiting on a human decision.",
			UsedFor:    []AvatarState{StateThinking, StateBlocked},
		},
		{
			Expression: ExpressionFocused,
			File:       "portrait/focused.png",
			Purpose:    "Narrowed and deliberate. Shown while a tool is running.",
			UsedFor:    []AvatarState{StateWorking},
		},
		{
			Expression: ExpressionBright,
			File:       "portrait/bright.png",
			Purpose:    "Open smile. Shown when a goal completes.",
			UsedFor:    []AvatarState{StateDone},
		},
	}
}

// AvatarSVG renders the sigil at the given state and pixel size.
//
// # The design
//
// Three swept blades around a glowing core, from the character's hair ornament.
// The blades are the boundary; the core is the work. The blades are present in
// every state — including failure and completion — because the boundary does not
// disappear when the work does.
//
// Inline SVG rather than a raster asset: it has to be legible at 24px in a table
// row and at 160px on a goal page, in light and dark, and it has to animate per
// state. It also means the status indicator needs no network request, which
// matters most when other things are failing.
func AvatarSVG(state AvatarState, size int) string {
	if !state.Valid() {
		state = StateIdle
	}
	if size <= 0 {
		size = 48
	}
	// Unique gradient ids per render: two avatars on one page sharing an id makes
	// the second adopt the first's colours, which reads as a bug in state
	// reporting rather than in markup.
	uid := fmt.Sprintf("%s-%d", state, size)

	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="forge-avatar forge-avatar--%s" viewBox="0 0 100 100" `+
		`width="%d" height="%d" role="img" aria-label="FORGE: %s" xmlns="http://www.w3.org/2000/svg">`,
		state, size, size, state.Label())

	fmt.Fprintf(&b, `<defs>`+
		`<radialGradient id="core-%s" cx="50%%" cy="46%%" r="58%%">`+
		`<stop offset="0%%" class="fa-core-hot"/><stop offset="55%%" class="fa-core-mid"/>`+
		`<stop offset="100%%" class="fa-core-edge"/></radialGradient>`+
		`<linearGradient id="wing-%s" x1="0%%" y1="0%%" x2="100%%" y2="100%%">`+
		`<stop offset="0%%" class="fa-wing-lit"/><stop offset="100%%" class="fa-wing-shade"/>`+
		`</linearGradient></defs>`, uid, uid)

	// The three swept blades. Drawn as one group so a single rotation animates
	// the whole wing rather than each blade independently.
	fmt.Fprintf(&b, `<g class="fa-wings" fill="url(#wing-%s)">`, uid)
	// Long leading blade, then two shorter ones fanning behind it — the shape of
	// the ornament on the character sheet.
	b.WriteString(`<path class="fa-blade fa-blade--1" d="M50 6 L56 26 L50 36 L44 26 Z"/>`)
	b.WriteString(`<path class="fa-blade fa-blade--2" d="M80 20 L70 40 L56 42 L64 26 Z"/>`)
	b.WriteString(`<path class="fa-blade fa-blade--3" d="M20 20 L36 26 L44 42 L30 40 Z"/>`)
	b.WriteString(`</g>`)

	// The containing ring — the boundary the work may not leave.
	b.WriteString(`<circle class="fa-ring" cx="50" cy="60" r="30" fill="none" stroke-width="3"/>`)

	// Working: an arc travels the ring. The motion is on the BOUNDARY, because a
	// tool call is something happening outside this process.
	if state == StateWorking {
		b.WriteString(`<circle class="fa-arc" cx="50" cy="60" r="30" fill="none" stroke-width="4"` +
			` stroke-linecap="round" stroke-dasharray="34 155"/>`)
	}

	// Blocked: the ring becomes a dashed gate. The work is stopped AT the
	// boundary rather than proceeding inside it.
	if state == StateBlocked {
		b.WriteString(`<circle class="fa-gate" cx="50" cy="60" r="30" fill="none" stroke-width="3"` +
			` stroke-dasharray="6 8"/>`)
	}

	fmt.Fprintf(&b, `<circle class="fa-core" cx="50" cy="60" r="20" fill="url(#core-%s)"/>`, uid)

	// Terminal states carry a glyph as well as a colour. Colour alone fails for a
	// colour-blind reader and on a monochrome screen.
	if state == StateFailed {
		b.WriteString(`<path class="fa-mark" d="M42 52 L58 68 M58 52 L42 68" stroke-width="3.5" stroke-linecap="round"/>`)
	}
	if state == StateDone {
		b.WriteString(`<path class="fa-mark" d="M42 60 L48 67 L59 53" fill="none" stroke-width="4" stroke-linecap="round" stroke-linejoin="round"/>`)
	}

	b.WriteString(`</svg>`)
	return b.String()
}

// AvatarStateForGoal maps a goal's situation onto a state.
//
// A terminal goal outranks everything, and blocked is checked before running: a
// goal with work in flight and an open approval must show "waiting for you",
// because that is the state a human can act on. Showing "working" would be true
// and useless.
func AvatarStateForGoal(goalStatus string, hasPendingApproval, hasRunningTask, modelCallInFlight bool) AvatarState {
	switch goalStatus {
	case "succeeded":
		return StateDone
	case "failed", "cancelled":
		return StateFailed
	}
	if hasPendingApproval {
		return StateBlocked
	}
	if hasRunningTask {
		return StateWorking
	}
	if modelCallInFlight {
		return StateThinking
	}
	return StateIdle
}
