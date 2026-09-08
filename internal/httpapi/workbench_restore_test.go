package httpapi

import (
	"strings"
	"testing"
)

// Reopening the workbench must bring back the WORK, not only the words.
//
// # What went wrong
//
// Three things were each reachable from exactly one place, and each place was
// the live turn or the one browser that happened to hold a localStorage key:
//
//   - loadPrototype() had a single caller, inside the turn stream, so the studio
//     only ever drew geometry that arrived while somebody watched it arrive. A
//     reload left "No geometry yet" on screen beside a rail that was listing the
//     very model it said did not exist.
//   - restoreVariants() read the project id out of localStorage and returned if
//     it was absent, so a browser that had never held the key showed an empty
//     rail forever.
//   - restoreConversation() painted the transcript and adopted no project, so
//     even a conversation opened from the console left every rail empty.
//
// Reported as "i don't see the 3d artifact from yesterday", from a private
// window. The variant was in the database the whole time.
//
// # What this asserts, and what it cannot
//
// That the restore path still reaches the studio and still adopts a project.
// Deliberately string matching: running workbench.js needs a DOM, a studio and
// a server, and this catches the realistic regression — one of these calls
// being dropped in a refactor, returning the code to "one caller" — at no cost.
// It cannot prove the drawing is correct; the live check for that is recorded in
// docs/bugfix/2026-09-08-history-was-unreachable.md.
func TestRestoringTheWorkbenchBringsBackTheWork(t *testing.T) {
	b, err := assetFS.ReadFile("assets/workbench.js")
	if err != nil {
		t.Fatalf("reading workbench.js: %v", err)
	}
	js := string(b)

	// 1. A stored variant is drawn, not merely listed.
	//
	// The CALL SITES, not the names. An earlier version of this test looked for
	// "drawRestoredVariant(" and for two occurrences of "loadPrototype(", and
	// both matched the function DEFINITIONS — so deleting the calls and leaving
	// two dead functions behind passed. Verified by deleting them: the test went
	// green on a workbench that could not draw anything.
	if !strings.Contains(js, "drawRestoredVariant(b.variants)") {
		t.Error("the variant listing no longer draws anything. loadPrototype's only caller is then " +
			"the live turn stream, so a reload shows 'No geometry yet' beside a rail listing the " +
			"model — the work is in the database, exportable and comparable, and cannot be SEEN.")
	}
	if !strings.Contains(js, "loadPrototype(pick.document") {
		t.Error("drawRestoredVariant no longer hands the stored document to the studio, so the " +
			"restore path reaches the rail and stops short of the stage")
	}

	// 2. Restoring a conversation adopts the project it was in, or the rails
	//    stay empty in any browser that did not already hold the key.
	if !strings.Contains(js, "if (lastProject) rememberProject(lastProject)") {
		t.Error("restoreConversation no longer adopts the conversation's project. The transcript " +
			"comes back and Parts, Variants and Industry do not, which reads as the work having " +
			"been lost when only the pointer to it was.")
	}

	// 3. Learning a project loads that project's work, not only its rules.
	if !strings.Contains(js, "if (wasNew) restoreVariants()") {
		t.Error("adopting a project no longer loads its variants. restoreVariants then runs only " +
			"at boot, off localStorage, so a browser that never held the key shows an empty rail " +
			"however many variants the project has.")
	}

	// 4. The console's link is honoured, or the listing leads nowhere.
	if !strings.Contains(js, "openConversationFromURL") {
		t.Error("the workbench no longer opens ?conversation=<id>, so the console's Conversations " +
			"panel links to a page that ignores which conversation was asked for")
	}
}
