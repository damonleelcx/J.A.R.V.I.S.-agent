package persona

import (
	"fmt"
	"html/template"
	"strings"
)

// PortraitURL is the path a portrait is served from.
func PortraitURL(e Expression) string { return "/assets/portrait/" + string(e) + ".png" }

// PresenceHTML renders FORGE: the portrait with the state sigil badged on it.
//
// # Why both, together
//
// They answer different questions and neither answers both.
//
//   - The portrait answers "who am I working with". It carries the product's
//     character, and at 72px it is recognisable.
//   - The sigil answers "what is happening right now". It is the thing a reader
//     scans for, and it stays readable at 22px where a face becomes a smudge.
//
// Putting the state only in the expression would be a mistake: the difference
// between "thinking" and "waiting for you" is a hand near a chin, which nobody
// reads at a glance and which a screen reader cannot read at all. So the
// expression carries mood and the badged sigil carries state — and the sigil is
// the one with the aria-label.
//
// The portrait is decoration in the strict sense: if the image fails to load,
// the sigil is still there and the state is still legible. A missing asset must
// never be able to take out a status indicator.
// AvatarHTML renders FORGE's portrait alone, at a small size, for a header.
//
// # Why the portrait rather than the sigil
//
// The sigil is a state indicator: it changes with what FORGE is doing, and it
// earns its place beside a portrait that carries mood. On its own, in a header,
// it is a small abstract mark that says nothing a wordmark next to it does not
// already say — and it is not what people recognise FORGE as. The portrait is.
//
// No badge here. A header is not a status surface: the room's own status word
// and the workbench's state word already say what is happening, and a second
// indicator repeating it in a form nobody can read is noise.
//
// The accessible name lives on this element, because unlike PresenceHTML there
// is no sigil beside it to carry one.
func AvatarHTML(size int) template.HTML {
	if size <= 0 {
		size = 32
	}
	return template.HTML(fmt.Sprintf(
		`<span class="forge-avatar" style="width:%dpx;height:%dpx">`+
			`<img src="%s" alt="FORGE" width="%d" height="%d" decoding="async"></span>`,
		size, size, PortraitURL(ExpressionFor(StateIdle)), size, size))
}

func PresenceHTML(state AvatarState, size int) template.HTML {
	if !state.Valid() {
		state = StateIdle
	}
	if size <= 0 {
		size = 72
	}
	expr := ExpressionFor(state)
	badge := size / 3
	if badge < 20 {
		badge = 20
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<span class="forge-portrait" style="width:%dpx;height:%dpx">`, size, size)
	// alt is empty and aria-hidden is set: the portrait is decorative, and the
	// accessible name lives on the sigil. Two elements both announcing "FORGE"
	// makes a screen reader repeat itself for no gain.
	fmt.Fprintf(&b,
		`<img src="%s" alt="" aria-hidden="true" width="%d" height="%d" loading="lazy" decoding="async">`,
		PortraitURL(expr), size, size)
	fmt.Fprintf(&b, `<span class="forge-portrait__badge" style="width:%dpx;height:%dpx">%s</span>`,
		badge, badge, AvatarSVG(state, badge-4))
	b.WriteString(`</span>`)

	return template.HTML(b.String())
}
