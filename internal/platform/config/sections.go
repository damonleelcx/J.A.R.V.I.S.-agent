package config

import (
	"bufio"
	"os"
	"strings"
)

// Section names a group of configuration a command may or may not need.
//
// Why this exists: an operator running a schema migration has no reason to hold
// an LLM API key, and demanding one turns a routine database task into a
// credential request. Requiring every variable for every binary is the lazy
// default; it looks like rigour and is actually just coupling.
//
// Each command declares what it needs. Unrequested sections are still parsed —
// so a malformed value is still reported — but their required fields are not
// enforced.
type Section string

const (
	// SectionDB is Postgres connectivity. Needed by anything that touches state.
	SectionDB Section = "database"
	// SectionHTTP is the public listener and public URL.
	SectionHTTP Section = "http"
	// SectionMail is outbound transactional mail.
	SectionMail Section = "mail"
	// SectionAuth is identity and session policy.
	SectionAuth Section = "auth"
	// SectionLLM is the model portfolio.
	SectionLLM Section = "llm"
	// SectionEngine is the durable execution engine.
	SectionEngine Section = "engine"
	// SectionNone requires nothing. Used by diagnostic commands that must be
	// able to render an incomplete configuration rather than refusing to run
	// precisely when the configuration is the thing being diagnosed.
	SectionNone Section = "none"
)

// AllSections is what a full server requires.
func AllSections() []Section {
	return []Section{SectionDB, SectionHTTP, SectionMail, SectionAuth, SectionLLM, SectionEngine}
}

type sectionSet map[Section]bool

func (s sectionSet) has(sec Section) bool { return s[sec] }

// LoadDotEnv reads a .env file into the process environment if one exists,
// without overwriting variables that are already set.
//
// Why not-overwriting matters: an explicitly exported variable is a deliberate
// act (a CI secret, a one-off override), and a file silently winning over it is
// the kind of surprise that costs an afternoon. The file fills gaps; it does not
// take precedence.
//
// A missing file is not an error — production sets real environment variables
// and has no .env at all.
func LoadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		// Strip one layer of matching quotes, so both KEY=value and KEY="value"
		// work the way every other dotenv reader behaves.
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if key == "" {
			continue
		}
		if _, alreadySet := os.LookupEnv(key); alreadySet {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return sc.Err()
}
