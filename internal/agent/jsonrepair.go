package agent

import "strings"

// repairJSON escapes raw control characters that appear inside JSON string
// literals.
//
// # Why this is necessary rather than defensive
//
// Several models emit a literal newline inside a JSON string value — most often
// in a long prose field like "rationale" or "instruction". RFC 8259 forbids it,
// and Go's encoding/json rejects it with "invalid character '\n' in string
// literal", so a complete and otherwise correct plan is thrown away over a line
// break.
//
// Measured on the models this deployment is configured for: of four candidates
// asked for a JSON object with prose fields, three produced raw newlines inside
// strings. Treating that as the model's fault does not make the plan parse.
//
// The repair walks the text tracking whether it is inside a string, and escapes
// only control characters found there. Structural whitespace between tokens is
// left exactly as it is, so a document that was already valid is returned
// unchanged — which is what makes this safe to run unconditionally.
func repairJSON(s string) string {
	// Fast path: nothing to do unless a control character is present at all.
	needsWork := false
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 {
			needsWork = true
			break
		}
	}
	if !needsWork {
		return s
	}

	var b strings.Builder
	b.Grow(len(s) + 16)

	inString := false
	escaped := false

	for i := 0; i < len(s); i++ {
		c := s[i]

		if escaped {
			// The previous byte was a backslash, so this one is part of an
			// escape sequence and is copied verbatim whatever it is.
			b.WriteByte(c)
			escaped = false
			continue
		}

		switch {
		case c == '\\' && inString:
			b.WriteByte(c)
			escaped = true

		case c == '"':
			inString = !inString
			b.WriteByte(c)

		case inString && c < 0x20:
			// A raw control character inside a string. Escape it rather than
			// dropping it: a newline inside a "rationale" is content, and
			// deleting it silently corrupts the text the model wrote.
			switch c {
			case '\n':
				b.WriteString(`\n`)
			case '\r':
				b.WriteString(`\r`)
			case '\t':
				b.WriteString(`\t`)
			case '\b':
				b.WriteString(`\b`)
			case '\f':
				b.WriteString(`\f`)
			default:
				b.WriteString(`\u00`)
				const hex = "0123456789abcdef"
				b.WriteByte(hex[c>>4])
				b.WriteByte(hex[c&0x0f])
			}

		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}
