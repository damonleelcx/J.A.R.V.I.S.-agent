# A build that renamed the model split its history in two

*2026-09-15. Found by #119 driving a build from the proposal card in a real browser against a
stand-in model; fixed on `agent/build-goal-one-artifact`.*

## What a person saw

They asked for a desk lamp and watched the build run. Every step succeeded, the goal card said
"2 versions kept", and the Files panel showed **two files**:

    geometry/desk-lamp-base.forge.json     1 version · model
    geometry/desk-lamp.forge.json          1 version · model

One build, one lamp, two histories with one version each. Opening either showed half the work, and
neither was the list of what the build had kept. Nothing failed, nothing was logged, and every row
written was correct in isolation — which is why no fence caught it.

## Why

A geometry version is stored against the artifact whose path is derived from the DOCUMENT'S NAME
(`geometry.artifactPath`). Step 1 called the model "desk lamp base". Step 2 added the stem and
called the same model "desk lamp" — a build step is free to rename as it learns what it is making,
and this one did. `workspace.Repository.FindOrCreateArtifact` looks an artifact up BY PATH, so the
second name found no artifact and created one.

The name rule is not a mistake. It is what makes "make it taller" version 2 of the bracket in a
conversation, with no state for a client to keep and nothing for it to assert. The cost was known
and written down in `artifactPath`: a model that renames slightly starts a second history, which is
"visible and harmless" — true of a TURN, and not true of a BUILD.

A build's steps are one piece of work. Splitting them splits:

  - the Files panel, which lists one entry per artifact anchor and reads each one's own history
    (`stage.js`), so a build appears as two files with half a history each;
  - "the versions this build kept", which stops being one list;
  - comparison, which still works — it takes arbitrary version ids — but no longer has one place to
    read the build's versions FROM.

## The fix

A build goal PINS the artifact its first kept step created, and every later step appends there
however the model has renamed the document since. `workspace.Change.ArtifactID` carries the pin,
`workspace.Service.artifactFor` resolves it (refusing an artifact of another project or another
kind), `geometry.NewVariant.ArtifactID` passes it through `Save`, and `agent.BuildSteps` reads it
from the previous step's own version — which the step already loads to get the model so far, so
nothing new is stored and no caller asserts anything.

    the artifact wins for a build, because a build IS one artifact's history
    the name wins for a turn, because a turn belongs to whatever it is named after

‼️ The rename is not swallowed by the fix. The document's new name is still stored on the version
that renamed it; what does not move is the artifact's path. A reader sees the rename where it
happened, in one history, instead of seeing two files and having to guess they are the same lamp.

## What this did NOT change

A conversational save after a rename still starts a second artifact — unchanged, and now fenced
(`TestSave_AConversationalSaveAfterARenameStillStartsASecondArtifact`), because a turn has no
history of its own to belong to and the name is the only thing that says which one it revises.

## The defect this nearly created next door

`Adopt` and `Respec` reached the source's artifact by keeping the source document's NAME. That is
the same answer only while a name and an artifact are the same fact — true before this change, and
false the moment an artifact can be pinned. Adopting a renamed build step would have appended the
copy to a DIFFERENT artifact from the one whose "is this already current?" check had just run, so
"we went back to v2" would have appeared in neither list. Both now name `source.ArtifactID`
outright, in the same commit, with fences of their own.

## Fences

  - `TestBuildGoal_ABuildThatRenamesTheModelKeepsOneArtifactWithAVersionPerStep`
  - `TestBuildGoal_AStepWhosePredecessorKeptNothingStillCreatesTheArtifact`
  - `TestBuildGoal_AnotherGoalsArtifactIsNeverAppendedTo`
  - `TestSave_APinnedSaveAppendsToThatArtifactAndStillStoresTheNewName`
  - `TestSave_AConversationalSaveAfterARenameStillStartsASecondArtifact`
  - `TestSave_APinnedArtifactFromAnotherProjectIsRefused`
  - `TestAdopt_AppendsToTheArtifactTheSourceIsOnAfterARename`
  - `TestRespec_AppendsToTheArtifactTheSourceIsOnAfterARename`

Seven drills in `scripts/drill-fences.sh`, each run and each red.

‼️ The adopt fence had to be strengthened before its drill could redden it. As first written it
adopted v1 — whose own name still resolves to the artifact v1 is on, so the name rule and the
artifact rule agree there and removing the pin changed nothing. It now adopts the RENAMED version,
which is where the two rules disagree. The same trap as the sweep-twist fence: a test written where
the right implementation and the plausible wrong one produce the same answer.
