#!/usr/bin/env bash
#
# Fence drills: break one thing, and check that the test that claims to hold it
# actually goes red.
#
# # Why this exists
#
# A test that has never failed is a claim, not a fence. Two of the fences in this
# repository were written, reviewed, and could not fail:
#
#   - the shape DISPATCH was untested for extrusions and revolves both. The unit
#     tests called the builders directly, so removing the case from partTriangles
#     left them green while every outline was silently drawn and exported as a
#     bounding box (wave 18).
#   - the sweep TWIST test bent its path once, in one plane — where the right
#     implementation and the plausible wrong one agree. Re-framing each segment
#     from scratch left every test in the package green (wave 19).
#
# Neither was found by reading. Both were found by breaking the code on purpose
# and watching what stayed green. That is what this does, one mutation at a time.
#
# # How it is safe to run on a working tree
#
# Each mutation is applied to the real source — there is no other way to know
# whether the real test sees it — and reverted from a checksummed backup. The
# restore runs from a trap, so an interrupt, a failure or a kill puts the tree
# back. The last thing it does is verify every file is byte-identical to how it
# started, and say so.
#
# It is still a bad idea to run this concurrently with anything else that reads
# these files: for the seconds a mutation is applied, the tree on disk is the
# mutated one. A second `go test` in another terminal would see it and produce a
# failure indistinguishable from a real defect.
#
# Usage:
#   scripts/drill-fences.sh              run every drill
#   scripts/drill-fences.sh --list       name them without touching anything
#   scripts/drill-fences.sh --dry-run    apply nothing; check every anchor still
#                                        matches the source
#
# Exit status is non-zero if any drill stayed green, or if any anchor has moved.

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT" || exit 1

# This repository is often checked out inside a parent Go workspace, and the
# Makefile sets the same thing for the same reason. Set here as well so the
# script behaves identically when run directly.
export GOWORK=off

# The kernel drills need a real build123d and the browser drill needs node.
# Neither is required to develop here, and a drill that cannot run is reported
# as UNPROVEN rather than passed — a skipped test is green, and green is the
# answer this script exists to distrust.
if [ -z "${FORGE_CAD_PYTHON:-}" ] && [ -x "$ROOT/.cadvenv/bin/python" ]; then
  export FORGE_CAD_PYTHON="$ROOT/.cadvenv/bin/python"
fi

MODE="run"
case "${1:-}" in
  --list)    MODE="list" ;;
  --dry-run) MODE="dry" ;;
  "")        ;;
  *) echo "unknown argument: $1" >&2; exit 2 ;;
esac

# Every file any drill below touches. A drill naming a file that is NOT here
# would mutate it and never put it back — the restore and the final checksum both
# work from this list. It has happened: on 2026-09-05 a drill against profile.go
# was added without adding the file, and it left the working tree with the
# mutation still in it while the script reported the tree byte-identical, which
# it truthfully was about everything it knew about. The guard in drill() now
# refuses rather than trusting this list to be kept up to date by hand.
FILES=(
  internal/domain/geometry/curve.go
  internal/domain/geometry/triangulate.go
  internal/domain/geometry/sweep.go
  internal/domain/geometry/mesh.go
  internal/domain/geometry/overlay.go
  internal/domain/geometry/profile.go
  internal/domain/geometry/solid.go
  internal/domain/geometry/retired.go
  internal/domain/geometry/gear.go
  internal/domain/geometry/faults.go
  internal/httpapi/assets/forge3d.js
  internal/domain/cad/sidecar.py
  internal/domain/cad/cad.go
  internal/llm/deliberation.go
  internal/llm/stream.go
  internal/llm/openai_compatible.go
  internal/agent/scriptrepair.go
  internal/agent/converse.go
  internal/agent/converse_stream.go
  internal/agent/assemble.go
  internal/agent/georepair.go
  internal/agent/settledoc.go
  internal/httpapi/converse.go
  internal/agent/look.go
  internal/domain/cad/script.py
  internal/domain/cad/script_test.go
  internal/domain/cad/script.go
  internal/domain/geometry/expression.go
  internal/domain/geometry/parameters.go
  internal/agent/sketch.go
  internal/llm/illustrate.go
  internal/agent/render.go
  internal/domain/geometry/mesh.go
  internal/httpapi/assets/voice.js
  internal/httpapi/assets/workbench.js
  internal/httpapi/transcribe.go
  internal/httpapi/router.go
  internal/llm/transcribe.go
  internal/domain/geometry/interference.go
  internal/agent/interference.go
  internal/httpapi/goals_start.go
  internal/agent/worker.go
  internal/domain/engine/repository.go
  internal/domain/engine/queue.go
  internal/agent/executor.go
  internal/agent/settle.go
)

BACKUP=""

# saved is where a file's backup lives, mirroring its path under $BACKUP.
#
# ‼️ It used to be the BASENAME, and two files that share one — this repository
# now has internal/agent/converse.go and internal/httpapi/converse.go — collided
# silently. The second overwrote the first in the backup, and "restore" then
# wrote the httpapi file over the agent one. The tree was left holding a Go file
# whose package did not match its directory, and the run reported it as
# "THE TREE WAS NOT RESTORED" rather than as the corruption it had just caused.
# The whole safety argument for this script rests on the restore being exact.
saved() { printf '%s/%s' "$BACKUP" "$1"; }

restore() {
  [ -n "$BACKUP" ] || return 0
  local f
  for f in "${FILES[@]}"; do
    [ -f "$(saved "$f")" ] && cp "$(saved "$f")" "$f"
  done
}
cleanup() {
  local status=$?
  restore
  [ -n "$BACKUP" ] && rm -rf "$BACKUP"
  exit $status
}

RED=0 GREEN=0 UNPROVEN=0 MOVED=0

# drill <name> <file> <python mutation over `s`> <package> <test regex>
drill() {
  local name=$1 file=$2 mutation=$3 pkg=$4 regex=$5

  if [ "$MODE" = "list" ]; then
    printf '  %-52s %s\n' "$name" "$regex"
    return
  fi

  # A file with no backup cannot be put back, so it is never touched. This is
  # the structural half of the note on FILES above: the list being wrong is now a
  # refusal to run rather than a permanent edit.
  if [ "$MODE" != "list" ] && [ ! -f "$(saved "$file")" ]; then
    echo "  ⛔ NOT BACKED UP — $name"
    echo "        $file is not in FILES, so this drill would edit it and leave it edited."
    MOVED=$((MOVED + 1))
    return
  fi

  restore
  if ! python3 -c "
p = '$file'
s = open(p).read()
$mutation
open(p, 'w').write(s)
"; then
    echo "  ⚠️  the mutation for \"$name\" could not be applied at all"
    MOVED=$((MOVED + 1))
    restore
    return
  fi

  if cmp -s "$file" "$(saved "$file")"; then
    # The code this drill points at has been edited and the anchor no longer
    # matches. Reported loudly: a drill that changes nothing reports the fence
    # as red-worthy forever without ever testing it, which is the same failure
    # this script exists to catch, one level up.
    echo "  ⚠️  ANCHOR MOVED  — $name"
    echo "        the mutation changed nothing, so this drill tests nothing"
    MOVED=$((MOVED + 1))
    restore
    return
  fi

  if [ "$MODE" = "dry" ]; then
    printf '  ✎  would mutate    — %s\n' "$name"
    restore
    return
  fi

  local out
  # -v is not for the reading. Without it a SKIPPED test prints "ok" and is
  # indistinguishable from a passing one — so a drill against the kernel fences
  # on a machine with no build123d would report "the fence held" about a fence
  # that never ran. Verified 2026-09-05: `go test` and `go test -v` on the same
  # skipped test differ only in that -v says "--- SKIP".
  out=$(go test -v -count=1 "$pkg" -run "$regex" 2>&1)
  restore

  # FAIL is checked BEFORE skip: a run where one subtest skipped and another
  # went red is red. The other order would report the fence as unproven and
  # hide the thing it caught.
  if echo "$out" | grep -q "^FAIL\|--- FAIL"; then
    echo "  ✅ went red      — $name"
    echo "$out" | grep -E "^ *--- FAIL|^    [a-z_]+\.go:" | head -2 | sed 's/^/       /'
    RED=$((RED + 1))
    return
  fi
  if echo "$out" | grep -q -- "--- SKIP\|no tests to run"; then
    echo "  ⏭️  UNPROVEN      — $name"
    echo "        the fence skipped, so it did not judge this. $(missing_tool "$pkg")"
    UNPROVEN=$((UNPROVEN + 1))
    return
  fi
  echo "  ❌ STAYED GREEN  — $name"
  echo "        $regex does not hold this. Either the fence asserts something that"
  echo "        cannot fail, or it is asserting it somewhere the mutation misses."
  GREEN=$((GREEN + 1))
}

missing_tool() {
  case "$1" in
    ./internal/domain/cad) echo "Set FORGE_CAD_PYTHON — see \`make cad-venv\`." ;;
    ./internal/httpapi)    echo "Install node." ;;
    *)                     echo "" ;;
  esac
}

if [ "$MODE" != "list" ]; then
  BACKUP=$(mktemp -d)
  trap cleanup EXIT INT TERM
  for f in "${FILES[@]}"; do mkdir -p "$(dirname "$(saved "$f")")" && cp "$f" "$(saved "$f")"; done
  shasum "${FILES[@]}" > "$BACKUP/before.sha"
fi

# ---------------------------------------------------------------------------
# The sweep and corner-radius fences.
#
# There are no drills here for the extrusion or the revolve themselves. That is a
# GAP and not a judgement that they are safe: waves 17 and 18 shipped without
# drills, and the dispatch fence they both rely on was found vacuous afterwards.
# ---------------------------------------------------------------------------

echo "Corner radii"
drill "corner radii are ignored entirely" internal/domain/geometry/curve.go \
  's = s.replace("\t\tif r == 0 {\n\t\t\tcontinue\n\t\t}", "\t\tif true {\n\t\t\tcontinue\n\t\t}", 1)' \
  ./internal/domain/geometry 'TestFlattenCurve|TestMeasureFindsTheMaterialAndNotTheRoundedOffCorner'

drill "the arc starts the wrong distance back along its edges" internal/domain/geometry/curve.go \
  's = s.replace("cut := r * math.Tan(angle/2)", "cut := r * math.Sin(angle/2)", 1)' \
  ./internal/domain/geometry 'TestFlattenCurve_ARoundedCornerRemovesWhatArithmeticSays'

drill "the arc centre is set back the wrong distance" internal/domain/geometry/curve.go \
  's = s.replace("scale3(bisector, r/math.Cos(angle/2))", "scale3(bisector, r/math.Sin(angle/2))", 1)' \
  ./internal/domain/geometry 'TestFlattenCurve'

drill "two radii on one edge are no longer checked against each other" internal/domain/geometry/curve.go \
  's = s.replace("if need > span+arcTolerance {", "if false {", 1)' \
  ./internal/domain/geometry 'TestRoundedCorners_RefusesWhatCannotBeRounded'

drill "flattening does not report what it cost" internal/domain/geometry/curve.go \
  's = s.replace("\tif d <= 0 {\n\t\treturn nil\n\t}", "\tif true {\n\t\treturn nil\n\t}", 1)' \
  ./internal/domain/cad 'TestKernel_AroundABendTheViewportIsInsideTheSolidBySomethingWeCanState'

drill "the kernel is sent chords instead of arcs" internal/domain/geometry/curve.go \
  's = s.replace("return CurveEdge{To: corners[i].to, Via: &via}", "_ = via\n\t\treturn CurveEdge{To: corners[i].to}", 1)' \
  ./internal/domain/cad 'TestKernel_ARoundedCornerIsARealArc'

drill "the measurement path measures the drawn vertex" internal/domain/geometry/overlay.go \
  's = s.replace("flat, _, err := partOutline(p).flatten(\"outline\", Millimetre)\n\tif err != nil {\n\t\treturn min, max, false\n\t}", "flat := partOutline(p).Points\n\tif false {\n\t\treturn min, max, false\n\t}", 1)' \
  ./internal/domain/geometry 'TestMeasureFindsTheMaterialAndNotTheRoundedOffCorner'

drill "the renderer steps its arcs at a different fineness" internal/httpapi/assets/forge3d.js \
  "s = s.replace('Math.ceil(TESSELLATION.radial * c.angle / (2 * Math.PI))', 'Math.ceil(TESSELLATION.radial * c.angle / (3 * Math.PI))', 1)" \
  ./internal/httpapi 'TestRendererSweepsTheSameSolidAsTheExporter'

drill "the kernel builds a straight line where an arc was sent" internal/domain/cad/sidecar.py \
  "s = s.replace('edges.append(ThreePointArc(at, Vector(*via), to))', 'edges.append(Line(at, to))', 1)" \
  ./internal/domain/cad 'TestKernel_ARoundedCornerIsARealArc'

echo
echo "Arcs that are not tangent to their neighbours"
drill "the arc goes the other way round" internal/domain/geometry/curve.go \
  's = s.replace("\tif viaAngle < toAngle {\n\t\treturn centre, unit, radius, toAngle, true\n\t}", "\tif viaAngle >= toAngle {\n\t\treturn centre, unit, radius, toAngle, true\n\t}", 1)' \
  ./internal/domain/geometry 'TestArcThrough_ResolvesACircleFromThreePoints|TestABowedEdgeLeavesItsChord'

drill "a via is ignored entirely" internal/domain/geometry/curve.go \
  's = s.replace("\t\tif via == nil {\n\t\t\tcontinue\n\t\t}", "\t\tif true {\n\t\t\tcontinue\n\t\t}", 1)' \
  ./internal/domain/geometry 'TestABowedEdgeLeavesItsChord|TestACrescentIsExpressible'

drill "the kernel is sent a chord where an arc was drawn" internal/domain/geometry/curve.go \
  's = s.replace("\t\tif a := arcs[i]; a != nil {", "\t\tif a := arcs[i]; false {", 1)' \
  ./internal/domain/cad 'TestKernel_ABowedEdgeIsARealArc'

drill "a corner beside an arc is rounded after all" internal/domain/geometry/curve.go \
  's = s.replace("\t\tif bowed(arcs, i) || bowed(arcs, (i+1)%n) {", "\t\tif false {", 1)' \
  ./internal/domain/geometry 'TestARadiusBesideAnArcIsIgnoredRatherThanFatal'

drill "the closing duplicate drops its via" internal/domain/geometry/curve.go \
  's = s.replace("\tif n-1 < len(vias) && vias[n-1] != nil && len(outVias) > 0 {", "\tif false {", 1)' \
  ./internal/domain/geometry 'TestAClosingDuplicateCarriesItsVia'

drill "a via is left in the document unit" internal/domain/geometry/curve.go \
  's = s.replace("\t\tscaled := scale3(*v, toMM)", "\t\tscaled := *v", 1)' \
  ./internal/domain/geometry 'TestAViaIsConvertedWithItsDrawing'

drill "an arc that crosses another edge is not noticed" internal/domain/geometry/profile.go \
  's = s.replace("\t\tif outer.bows() {", "\t\tif false {", 1)' \
  ./internal/domain/geometry 'TestAnArcThatCrossesAnotherEdgeIsRefused'

drill "the renderer steps its bows at a different fineness" internal/httpapi/assets/forge3d.js \
  "s = s.replace('var steps2 = Math.max(1, Math.ceil(TESSELLATION.radial * a2.angle / (2 * Math.PI)));', 'var steps2 = Math.max(1, Math.ceil(TESSELLATION.radial * a2.angle / (3 * Math.PI)));', 1)" \
  ./internal/httpapi 'TestRendererBowsTheSameOutlineAsTheExporter'

drill "the renderer bows the other way round" internal/httpapi/assets/forge3d.js \
  "s = s.replace('if (viaA < toA) return { centre: centre, axis: axis, angle: toA, from: from };', 'if (viaA >= toA) return { centre: centre, axis: axis, angle: toA, from: from };', 1)" \
  ./internal/httpapi 'TestRendererBowsTheSameOutlineAsTheExporter'

echo
echo "Retired shape words"
drill "a retired word resolves to nothing" internal/domain/geometry/retired.go \
  's = s.replace("\tr, ok := retiredShapes[shape]", "\tr, ok := retirement{}, false\n\t_ = retiredShapes", 1)' \
  ./internal/domain/geometry 'TestARetiredShapeStillDraws|TestTheKernelIsSentTheResolvedWord'

drill "a retired word resolves silently" internal/domain/geometry/retired.go \
  's = s.replace("\treturn r.As, fmt.Sprintf(r.Because, label, shape)", "\treturn r.As, \"\"", 1)' \
  ./internal/domain/geometry 'TestARetiredShapeSaysWhatItWasReadAs'

drill "the renderer does not retire what Go retires" internal/httpapi/assets/forge3d.js \
  "s = s.replace('var RETIRED = {', 'var RETIRED = {}; var UNUSED_RETIRED = {', 1)" \
  ./internal/httpapi 'TestTheRendererRetiresTheSameShapeWords'

echo
echo "Every document a turn installs is settled"
# Added 2026-09-11. validate() was the only place a document was bound, defaulted
# and noted, and it ran once, before the three other producers of a turn's
# document: resolveEdit, repairGeometry (all four repairs) and a build pass. Each
# installed what the model typed. See
# docs/bugfix/2026-09-11-edited-and-repaired-documents-were-never-settled.md.
drill "a repair installs an unsettled document" internal/agent/georepair.go \
  's = s.replace("\tout.Prototype = settleDocument(out.Prototype)\n", "\n", 1)' \
  ./internal/agent 'TestRepair_TheRepairedDocumentIsBound'

drill "an edit installs an unsettled document" internal/agent/converse.go \
  's = s.replace("\tr.Prototype = settleDocument(&applied)\n", "\tr.Prototype = &applied\n", 1)' \
  ./internal/agent 'TestEdit_TheEditedDocumentIsBound'

drill "a build pass installs an unsettled document" internal/agent/assemble.go \
  's = s.replace("\treply.Prototype = settleDocument(reply.Prototype)\n", "\n", 1)' \
  ./internal/agent 'TestAssemble_APassIsSettled'

drill "settling a document twice repeats its notes" internal/agent/settledoc.go \
  's = s.replace("\td.NotVerified = distinctNotes(d.NotVerified)\n", "\n", 1)' \
  ./internal/agent 'TestSettle_IsIdempotent'

drill "a second settle rewords the unit note" internal/agent/settledoc.go \
  's = s.replace("\t\tif !saysUnitless(d.NotVerified) {\n", "\t\tif true {\n", 1)' \
  ./internal/agent 'TestSettle_IsIdempotent'

echo
echo "The gear shape"
# Added 2026-09-10 with the shape. The involute is worked out in gear.go and
# mirrored in forge3d.js, and every reader of a document has to expand a gear
# before it reads the part — a reader that forgot would see a word it has no case
# for, which the exporter skips and the viewport draws as a box.
drill "a gear reaches the kernel as the word" internal/domain/geometry/solid.go \
  's = s.replace("\td, gearProblems := expandGears(d)", "\tgearProblems := []Problem(nil)", 1)' \
  ./internal/domain/geometry 'TestTheKernelIsSentAGearAsAnExtrusion'

drill "the mesh does not expand a gear" internal/domain/geometry/mesh.go \
  's = s.replace("\tdoc, gearProblems := expandGears(doc)", "\tgearProblems := []Problem(nil)", 1)' \
  ./internal/domain/geometry 'TestAGearIsDrawnAndMeasuredAtItsOwnSize'

drill "a gear that cannot exist never reaches the repair loop" internal/domain/geometry/faults.go \
  's = s.replace("\texpanded, gearProblems := expandGears(*d)\n\texpanded, repeatProblems := expandRepeats(expanded)", "\tgearProblems := []Problem(nil)\n\texpanded, repeatProblems := expandRepeats(*d)", 1)' \
  ./internal/domain/geometry 'TestAGearThatCannotExistIsAFaultNotABox'

drill "a gear flank is not an involute" internal/domain/geometry/gear.go \
  's = s.replace("func flankTurn(t float64) float64 { return t - math.Atan(t) }", "func flankTurn(t float64) float64 { return t }", 1)' \
  ./internal/domain/geometry 'TestAGearHasInvoluteTeeth'

drill "the renderer draws a different tooth" internal/httpapi/assets/forge3d.js \
  "s = s.replace('function turn(t) { return t - Math.atan(t); }', 'function turn(t) { return t - Math.atan(t) * 0.99; }', 1)" \
  ./internal/httpapi 'TestRendererDrawsTheSameGearAsTheExporter'

drill "the renderer has no gear" internal/httpapi/assets/forge3d.js \
  "s = s.replace(\"case 'gear': {\", \"case 'gear-unused': {\", 1)" \
  ./internal/httpapi 'TestRendererDrawsTheSameGearAsTheExporter'

drill "the contract still sends a spur gear to a script" internal/agent/converse.go \
  's = s.replace("above — a spiral, a lattice, a helical gear, a \"", "above — an involute gear tooth, a spiral, a \"", 1)' \
  ./internal/agent 'TestTheContractNoLongerSendsASpurGearToAScript'

echo
echo "Interference"
# Added 2026-09-12 with the check. Found by measuring a live car build that came
# back with document faults ZERO, a clean kernel build and a passing visual
# check, with the master cylinder entirely inside the engine block:
# docs/spikes/2026-09-12-car-ceiling/README.md
#
# The first two are the DESIGN, not the arithmetic. The whole reason the numbers
# come from the kernel on the solids that survive the features is that anything
# computed earlier reports every bolt hole as an interference.
drill "nothing is ever reported as interfering" internal/domain/cad/sidecar.py \
  "s = s.replace('    found.sort(key=lambda f: f[\"fraction\"], reverse=True)', '    found = []', 1)" \
  ./internal/domain/cad 'TestKernel_TwoSolidsInTheSameSpaceAreReported'

# ‼️ The first version of this drill substituted `shapes` at the call site and
# STAYED GREEN, which looked like a vacuous fence and was a wrong mutation: by
# then `shapes` holds the solids AFTER the booleans, so the plate already has its
# hole and the drill sits in the void — no overlap, nothing to report, fence
# correctly silent. Reproducing "before the tools are consumed" means stashing
# the solids as they are BEFORE the feature loop, which is what this does.
drill "interference is measured BEFORE the tools are consumed" internal/domain/cad/sidecar.py \
  's = s.replace("    shapes = dict(zip(ids, built))", "    shapes = dict(zip(ids, built))\n    _pre = (list(built), list(ids), list(names))", 1)
s = s.replace("clashes, clash_truncated = _interferences(built, ids, names)", "clashes, clash_truncated = _interferences(*_pre)", 1)' \
  ./internal/domain/cad 'TestKernel_ACutToolIsNotAnInterference'

drill "the pair is reported in build order, not smaller first" internal/domain/cad/sidecar.py \
  "s = s.replace('            lo, hi = (i, j) if volumes[i] <= volumes[j] else (j, i)', '            lo, hi = i, j', 1)" \
  ./internal/domain/cad 'TestKernel_ASwallowedPartIsReportedAsBuried'

drill "every graze counts as buried, so a weld drives a rewrite" internal/domain/geometry/interference.go \
  's = s.replace("func (i Interference) Buried() bool { return i.Fraction >= BuriedFraction }", "func (i Interference) Buried() bool { return i.Fraction > 0 }", 1)' \
  ./internal/agent 'TestInterference_AGrazeIsReportedAndNotRepaired'

drill "a described render reports interference it cannot know about" internal/agent/interference.go \
  's = s.replace("sheet == nil || !sheet.FromKernel {", "sheet == nil {", 1)' \
  ./internal/agent 'TestInterference_ADescribedRenderClaimsNothing'

drill "a repair is accepted without rebuilding" internal/agent/interference.go \
  's = s.replace("\t\tif after.FromKernel &&\n\t\t\tlen(fixed.Faults()) <= len(reply.Prototype.Faults()) &&\n\t\t\tlen(geometry.InterferenceProblems(after.Interferences)) < len(problems) {", "\t\tif true {", 1)' \
  ./internal/agent 'TestInterference_ARepairThatDoesNotHelpIsRefused'

echo
echo "Islands"
drill "an island is cut away with its hole" internal/domain/geometry/triangulate.go \
  's = s.replace("\t\tif depth[i]%2 != 0 {\n\t\t\tcontinue // a void, and it belongs to whatever contains it\n\t\t}", "\t\tif i != 0 {\n\t\t\tcontinue\n\t\t}", 1)' \
  ./internal/domain/geometry 'TestAnIslandIsSolidInTheMesh|TestNestingKeepsGoing'

drill "the nesting parity is ignored, so every loop after the first is a hole" internal/domain/geometry/triangulate.go \
  's = s.replace("\t\tif depth[i]%2 == 0 {\n\t\t\tout[i] = counterClockwise(loop)", "\t\tif i == 0 {\n\t\t\tout[i] = counterClockwise(loop)", 1)' \
  ./internal/domain/geometry 'TestAnIslandsWallFacesOutOfTheMaterial|TestAnIslandIsSolidInTheMesh'

drill "the kernel is not told which loop is an island" internal/domain/geometry/solid.go \
  's = s.replace("\t\t\t\t\t\t\tholeParents = parents", "\t\t\t\t\t\t\t_ = parents", 1)' \
  ./internal/domain/geometry 'TestTheKernelIsToldWhichLoopIsAnIsland'

drill "the kernel treats an island as a second hole" internal/domain/cad/sidecar.py \
  "s = s.replace('        if depth(i) % 2 == 0:', '        if False:', 1)" \
  ./internal/domain/cad 'TestKernel_AnIslandInAHoleIsSolid'

drill "an island is read but never mentioned" internal/domain/geometry/profile.go \
  's = s.replace("\t\tnote(label, islandNotes(flatHoles)...)", "", 1)' \
  ./internal/domain/geometry 'TestAnIslandIsReportedAsOne'

drill "the renderer draws an island as a hole" internal/httpapi/assets/forge3d.js \
  "s = s.replace('      if (tree.depth[i] % 2 !== 0) continue;          /* a void, not an area */', '      if (i !== 0) continue;', 1)" \
  ./internal/httpapi 'TestRendererNestsTheSameSectionAsTheExporter'

drill "the mesh export says nothing about the features it did not perform" internal/domain/geometry/mesh.go \
  's = s.replace("\tfor _, f := range doc.Features {", "\tfor _, f := range []Feature(nil) {\n\t\t_ = f\n\t}\n\tfor _, f := range []Feature(nil) {", 1)' \
  ./internal/domain/geometry 'TestAMeshSaysWhichFeaturesItDidNotPerform'

echo
echo "The Go tessellator"
drill "the sweep case is gone from partTriangles" internal/domain/geometry/mesh.go \
  's = s.replace("\tcase \"sweep\":\n", "\tcase \"sweep-disabled\":\n", 1)' \
  ./internal/domain/geometry 'TestTessellate_ReachesTheOutlineShapes'

drill "corners are not mitred (the section stays perpendicular)" internal/domain/geometry/sweep.go \
  's = s.replace("bisector[j] = normalise(add3(tangent[(j-1+segments)%segments], tangent[j%segments]))", "bisector[j] = tangent[(j-1+segments)%segments]", 1)' \
  ./internal/domain/geometry 'TestSwept_EnclosesAreaTimesPathLength'

drill "the section is re-framed each segment instead of carried" internal/domain/geometry/sweep.go \
  's = s.replace("\t\taxisX[i], axisY[i] = turn(axisX[i-1]), turn(axisY[i-1])", "\t\t_ = turn\n\t\taxisX[i], axisY[i] = initialSectionFrame(tangent[i])", 1)' \
  ./internal/domain/geometry 'TestSectionFrames_CarryTheSectionWithoutRollingIt'

drill "the section is ROLLED as it is carried" internal/domain/geometry/sweep.go \
  's = s.replace("\t\taxisX[i], axisY[i] = turn(axisX[i-1]), turn(axisY[i-1])", "\t\troll := smallestRotation(normalise(add3(tangent[i], scale3(axisX[i-1], 0.3))), tangent[i])\n\t\taxisX[i], axisY[i] = roll(turn(axisX[i-1])), roll(turn(axisY[i-1]))", 1)' \
  ./internal/domain/geometry 'TestSectionFrames_CarryTheSectionWithoutRollingIt|TestSwept_KeepsTheSectionSquareThroughABend'

drill "the fold check is gone" internal/domain/geometry/sweep.go \
  's = s.replace("if dot3(sub3(rings[l][(i+1)%n][k], rings[l][i][k]), tangent[i]) <= 1e-9 {", "if false {", 1)' \
  ./internal/domain/geometry 'TestSweptSections_RefusesThePathsThatAreNotSolids|TestProfileProblems_RefusesWhatASweepCannotBe'

drill "a reversal is no longer refused" internal/domain/geometry/sweep.go \
  's = s.replace("if length3(add3(tangent[(i-1+segments)%segments], tangent[i])) < 1e-9 {", "if false {", 1)' \
  ./internal/domain/geometry 'TestSweptSections_RefusesThePathsThatAreNotSolids'

echo
echo "Holes in an outline"
drill "holes are dropped from the section entirely" internal/domain/geometry/curve.go \
  's = s.replace("\tfor i, hole := range holes {", "\tfor i, hole := range holes[:0] {", 1)' \
  ./internal/domain/geometry 'TestExtrusion_AHoleTakesMaterialOut|TestSwept_ABentTubeIsHollowAllTheWayRound'

drill "a hole is wound the same way as the outline" internal/domain/geometry/triangulate.go \
  's = s.replace("\t\t\tout[i] = clockwise(loop)", "\t\t\tout[i] = counterClockwise(loop)", 1)' \
  ./internal/domain/geometry 'TestExtrusion_AHoleTakesMaterialOut'

drill "the walls are built over the merged ring, bridges and all" internal/domain/geometry/mesh.go \
  's = s.replace("\tfor _, loop := range sec.Loops {\n\t\tfor i := range loop {", "\tfor _, loop := range [][][2]float64{sec.Merged} {\n\t\tfor i := range loop {", 1)' \
  ./internal/domain/geometry 'TestExtrusion_AHoleTakesMaterialOut'

drill "a hole is not checked against the outline it sits in" internal/domain/geometry/profile.go \
  's = s.replace("if problem := holesFit(flatOuter, flatHoles); problem != \"\" {", "if problem := \"\"; problem != \"\" {", 1)' \
  ./internal/domain/geometry 'TestProfileProblems_RefusesHolesThatAreNotHoles|TestProfileProblems_AHoleMustFitTheROUNDEDOutline'

drill "the kernel builds the outline and ignores its holes" internal/domain/cad/sidecar.py \
  "s = s.replace('    return Face(outer_wire, [_wire(h) if isinstance(h, dict) else h for h in inner_loops])', '    return make_face(outer_wire)', 1)" \
  ./internal/domain/cad 'TestKernel_ABentTubeIsHollowRoundTheCorner'

drill "the renderer draws the outline and ignores its holes" internal/httpapi/assets/forge3d.js \
  "s = s.replace('var bores = holeOutlines(holes);\n    var way = flattenDrawing', 'var bores = [];\n    var way = flattenDrawing', 1)" \
  ./internal/httpapi 'TestRendererSweepsTheSameSolidAsTheExporter'

drill "an inert radius on a run's END is fatal again" internal/domain/geometry/curve.go \
  's = s.replace("inert(i, \"which is where the run starts or ends rather than a corner\")\n\t\t\tcontinue", "return nil, ignored, fmt.Errorf(\"no corner at an end\")", 1)' \
  ./internal/domain/geometry 'TestSwept_ARadiusOnAnEndIsIgnoredRatherThanFatal|TestRoundedCorners_IgnoresARadiusThatNamesNoCorner'

drill "the export calls a part absent when it is present" internal/domain/geometry/solid.go \
  's = s.replace("\t\tif problem.Severity == Error {", "\t\tif true {", 1)' \
  ./internal/domain/geometry 'TestSwept_ARadiusOnAnEndIsIgnoredRatherThanFatal'

drill "a loop's repeated closing point is fatal again" internal/domain/geometry/curve.go \
  's = s.replace("\tif n < 2 || !same(pts[0], pts[n-1]) {", "\tif true {", 1)' \
  ./internal/domain/geometry 'TestProfileProblems_ALoopMayCloseItselfTheWayEveryPolygonFormatDoes'

drill "the closing point's radius is dropped with the point" internal/domain/geometry/curve.go \
  's = s.replace("\t\toutRadii[0] = closing", "\t\t_ = closing", 1)' \
  ./internal/domain/geometry 'TestSolids_TheClosingPointsRadiusSurvivesIntoTheBuild|TestWithoutClosingDuplicate'

drill "two different radii on one corner are merged silently" internal/domain/geometry/curve.go \
  's = s.replace("\tcase outRadii[0] != closing:\n\t\treturn pts, radii, vias, false, true", "\tcase false:\n\t\treturn pts, radii, vias, false, true", 1)' \
  ./internal/domain/geometry 'TestWithoutClosingDuplicate'

drill "a duplicate ANYWHERE is read as a closing convention" internal/domain/geometry/curve.go \
  's = s.replace("!same(pts[0], pts[n-1])", "false", 1)' \
  ./internal/domain/geometry 'TestProfileProblems_ALoopMayCloseItselfTheWayEveryPolygonFormatDoes'

drill "an inert radius is fatal again" internal/domain/geometry/curve.go \
  's = s.replace("\t\t\t\tinert(i, \"where the edges either side of it are in line\")\n\t\t\t\tcontinue", "\t\t\t\treturn nil, ignored, fmt.Errorf(\"in line\")", 1)' \
  ./internal/domain/geometry 'TestRoundedCorners_IgnoresARadiusThatNamesNoCorner'

drill "an ignored radius is ignored SILENTLY" internal/domain/geometry/curve.go \
  's = s.replace("\t\tignored = append(ignored, fmt.Sprintf(", "\t\t_ = fmt.Sprintf(", 1)' \
  ./internal/domain/geometry 'TestRoundedCorners_IgnoresARadiusThatNamesNoCorner|TestSwept_ARadiusOnAnEndIsIgnoredRatherThanFatal'

drill "the renderer keeps a loop's repeated closing point" internal/httpapi/assets/forge3d.js \
  "s = s.replace('        points.pop();', '        void 0;', 1)" \
  ./internal/httpapi 'TestRendererSweepsTheSameSolidAsTheExporter'

drill "the renderer drops the closing point's radius" internal/httpapi/assets/forge3d.js \
  "s = s.replace('var keepR = num(a0.radius, 0) || num(z0.radius, 0);', 'var keepR = num(a0.radius, 0);', 1)" \
  ./internal/httpapi 'TestRendererSweepsTheSameSolidAsTheExporter'

echo
echo "Closed paths"
drill "a closed path is swept as an open one" internal/domain/geometry/curve.go \
  's = s.replace("return readLoop(p.Path, p.PathClosed)", "return readLoop(p.Path, false)", 1)' \
  ./internal/domain/geometry 'TestSwept_AClosedPathEnclosesAreaTimesItsPerimeter'

drill "the seam gets caps like an open path" internal/domain/geometry/mesh.go \
  's = s.replace("\tif !p.PathClosed {\n\t\tstartNormal", "\tif true {\n\t\tstartNormal", 1)' \
  ./internal/domain/geometry 'TestSwept_AClosedPathEnclosesAreaTimesItsPerimeter'

drill "the walls stop at the last vertex instead of wrapping" internal/domain/geometry/mesh.go \
  's = s.replace("\tsegments := last\n\tif p.PathClosed {\n\t\tsegments = vertices\n\t}", "\tsegments := last", 1)' \
  ./internal/domain/geometry 'TestSwept_AClosedPathEnclosesAreaTimesItsPerimeter'

drill "the frame is not checked for closing round the loop" internal/domain/geometry/sweep.go \
  's = s.replace("\t\tif err := framesClose(tangent, axisX, axisY); err != nil {", "\t\tif err := error(nil); err != nil {", 1)' \
  ./internal/domain/geometry 'TestSwept_AClosedPathMustBringItsSectionBack'

drill "a closed run starts where its seam arc begins" internal/domain/geometry/curve.go \
  's = s.replace("\tcurve := Curve{Start: exit(0), Closed: closed}", "\tcurve := Curve{Start: entry(0), Closed: closed}", 1)' \
  ./internal/domain/cad 'TestKernel_AClosedPathSweepsARing'

drill "the renderer keeps its own seam" internal/httpapi/assets/forge3d.js \
  "s = s.replace('if (closed && seam > 1 && out.length) {', 'if (false) {', 1)" \
  ./internal/httpapi 'TestRendererSweepsTheSameSolidAsTheExporter'

echo
echo "The measurement path"
drill "the sweep case is gone from localBox" internal/domain/geometry/overlay.go \
  's = s.replace("\tcase \"sweep\":\n", "\tcase \"sweep-disabled\":\n", 1)' \
  ./internal/domain/geometry 'TestMeasureFollowsASweptPathAndItsMitre'

echo
echo "The export path"
drill "the sweep case is gone from Solids" internal/domain/geometry/solid.go \
  's = s.replace("\t\tcase \"sweep\":\n", "\t\tcase \"sweep-disabled\":\n", 1)' \
  ./internal/domain/geometry 'TestSolids_ConvertsASweepsPathAndFramesItsSection'

drill "the drawing is left in the document's own unit" internal/domain/geometry/curve.go \
  's = s.replace("out.Points[i] = scale3(pt, toMM)", "out.Points[i] = pt", 1)' \
  ./internal/domain/geometry 'TestSolids_Converts'

drill "a corner radius is left in the document's own unit" internal/domain/geometry/curve.go \
  's = s.replace("out.Radii[i] = r * toMM", "out.Radii[i] = r", 1)' \
  ./internal/domain/geometry 'TestSolids_ConvertsACornerRadiusToo'

drill "the section frame does not travel to the kernel" internal/domain/geometry/solid.go \
  's = s.replace("\t\t\t\tframe = &f", "\t\t\t\t_ = f", 1)' \
  ./internal/domain/geometry 'TestSolids_ConvertsASweepsPathAndFramesItsSection'

echo
echo "The browser copy"
drill "the renderer frames the section differently" internal/httpapi/assets/forge3d.js \
  "s = s.replace('var axisX = [start([1,0,0])], axisY = [start([0,1,0])];', 'var axisX = [start([0,1,0])], axisY = [start([1,0,0])];', 1)" \
  ./internal/httpapi 'TestRendererSweepsTheSameSolidAsTheExporter'

drill "the renderer does not mitre its corners" internal/httpapi/assets/forge3d.js \
  "s = s.replace('bisector.push(unit(sum));', 'bisector.push(tangent[j-1]);', 1)" \
  ./internal/httpapi 'TestRendererSweepsTheSameSolidAsTheExporter'

drill "the renderer lights its facets from the wrong side" internal/httpapi/assets/forge3d.js \
  "s = s.replace('var norm = nn || normalOf(a, b, c);', 'var norm = nn || normalOf(a, c, b);', 1)" \
  ./internal/httpapi 'TestRendererSweepsTheSameSolidAsTheExporter'

drill "the renderer has no sweep case" internal/httpapi/assets/forge3d.js \
  "s = s.replace(\"case 'sweep':\", \"case 'sweep-disabled':\", 1)" \
  ./internal/httpapi 'TestRendererDrawsOutlineShapes'

echo
echo "Latency and the model catalogue"
# Anchored on the ENTRY, not on the whole map literal. It was written as the
# literal and went stale the moment RoleVision joined the table (#42): the drill
# then found nothing to replace and this fence went unproven for five commits
# while the run still printed a summary. An anchor that names one line survives
# the next role being added.
drill "the conversation role is allowed to deliberate" internal/llm/deliberation.go \
  's = s.replace("\tRoleConverse: true,\n", "", 1)' \
  ./internal/llm 'TestTheConversationRoleIsToldNotToDeliberate|TestTheStreamingPathIsToldToo'

drill "the STREAMING path forgets to say it" internal/llm/stream.go \
  's = s.replace("\tc.applyDeliberation(ctx, req.Role, body)\n", "", 1)' \
  ./internal/llm 'TestTheStreamingPathIsToldToo'

echo
echo "Trigonometry in expressions"
# Added 2026-09-10 because its absence had a measured cost: a gear's base_radius
# could not resolve, so it was not in scope for the part's script, so the script
# naming it was refused. The naming is the whole safety argument, and the unit
# rule is what makes it usable at all.
drill "the degree-named trigonometry is gone again" internal/domain/geometry/expression.go \
  's = s.replace(chr(34) + "cos_deg" + chr(34) + ": {1,", chr(34) + "cos_deg_disabled" + chr(34) + ": {1,", 1)' \
  ./internal/domain/geometry 'TestExpression_TrigonometryInDegrees'

drill "the ambiguous spelling is offered too" internal/domain/geometry/expression.go \
  's = s.replace(chr(34) + "sin_deg" + chr(34) + ": {1,", chr(34) + "cos" + chr(34) + ": {1,", 1)' \
  ./internal/domain/geometry 'TestExpression_BareTrigonometryIsStillAbsent|TestExpression_UnstatedConventionTrigonometryIsRefusedOnPurpose'

drill "a right angle's tangent is a very large number" internal/domain/geometry/expression.go \
  's = s.replace("if math.Mod(math.Abs(a[0])-90, 180) == 0 {", "if false {", 1)' \
  ./internal/domain/geometry 'TestExpression_TrigonometryRefusesTheUndefined'

# ‼️ Without this, adding cos_deg MOVES the failure rather than removing it:
# `pitch_radius * cos_deg(pressure_angle)` reads mm and deg, and the value is
# refused for mixing units. The fence caught that on the first run.
drill "a trig function lends its argument's unit to the result" internal/domain/geometry/parameters.go \
  's = s.replace("inheritedUnit(p.node.UnitReferences(), res.Values)", "inheritedUnit(p.refs, res.Values)", 1)' \
  ./internal/domain/geometry 'TestExpression_TrigonometryInDegrees'

drill "the contract stops naming the real function list" internal/agent/converse.go \
  's = s.replace("strings.Join(geometry.ExpressionFunctions(), \", \")", chr(34) + "sqrt, abs" + chr(34), 1)' \
  ./internal/agent 'TestExpressionFunctionsAreNamedInTheContract'

echo
echo "Running the scripts the model wrote"
# Every fence below drives repairIfScriptsFail directly, so the WIRING drills are
# the ones that matter most: the first run of them found both reply paths red and
# the multi-pass build loop green, which is a twelve-pass build shipping scripts
# nobody ran.
drill "a verified fix is not kept" internal/agent/scriptrepair.go \
  's = s.replace("\t\t\t\t\tpart.Script = source\n", "", 1)' \
  ./internal/agent 'TestScripts_RewrittenUntilTheyBuild'

drill "the builder's words never reach the model" internal/agent/scriptrepair.go \
  's = s.replace(chr(43) + " refusal " + chr(43), chr(43) + " \"\" " + chr(43), 1)' \
  ./internal/agent 'TestScripts_TheBuildersWordsReachTheModel'

drill "a stuck model is asked the same thing forever" internal/agent/scriptrepair.go \
  's = s.replace("\t\t\tif refusal == last {", "\t\t\tif false {", 1)' \
  ./internal/agent 'TestScripts_StopsWhenTheModelIsNotMoving'

drill "an unverified rewrite replaces the original" internal/agent/scriptrepair.go \
  's = s.replace("\tfor _, o := range broken {", "\tfor _, o := range broken[:0] {", 1)' \
  ./internal/agent 'TestScripts_UnfixableKeepsTheOriginalAndIsSaidOutLoud'

drill "every pass re-runs every script it ever wrote" internal/agent/scriptrepair.go \
  's = s.replace("\tparts := unverified(scriptedParts(reply.Prototype), previous)", "\tparts := scriptedParts(reply.Prototype)\n\t_ = previous", 1)' \
  ./internal/agent 'TestScripts_UnchangedScriptsAreNotRerun'

drill "a part is built and never verified" internal/agent/scriptrepair.go \
  's = s.replace("strings.ToLower(strings.TrimSpace(p.Shape)) != \"script\"", "p.Shape != \"script\"", 1)' \
  ./internal/agent 'TestScripts_SelectionAgreesWithTheBuilder'

drill "the STREAMED turn never runs its scripts" internal/agent/converse_stream.go \
  's = s.replace("\t\tc.repairIfScriptsFail(ctx, &reply, current, func(line string) {\n\t\t\t_ = emit(StreamEvent{Kind: \"notice\", Text: line})\n\t\t})\n", "", 1)' \
  ./internal/agent 'TestScripts_TheTurnActuallyRunsThem'

drill "the BUFFERED turn never runs its scripts" internal/agent/converse.go \
  's = s.replace("\tc.repairIfScriptsFail(ctx, &reply, current, nil)\n", "", 1)' \
  ./internal/agent 'TestScripts_TheTurnActuallyRunsThem'

drill "a multi-pass build never runs its scripts" internal/agent/assemble.go \
  's = s.replace("\tc.repairIfScriptsFail(ctx, &reply, doc, nil)\n", "", 1)' \
  ./internal/agent 'TestScripts_MultiPassBuildRunsThemToo'

drill "the contract offers scripts nothing can run" internal/agent/converse.go \
  's = s.replace("return c != nil && c.runner != nil", "return true", 1)' \
  ./internal/agent 'TestScripts_NoRunnerIsNotAQuietPass'

# Puts the call back where it FIRST was — beside repairIfFaulty, before the two
# repairs that hand back a whole new document. Deleting it is a different drill
# (above); this one proves the ORDER is load-bearing, which is what the live run
# on 2026-09-09 showed and what reading the code did not.
drill "a later repair can undo the verification" internal/agent/converse.go \
  's = s.replace("\tc.repairIfScriptsFail(ctx, &reply, current, nil)\n", "", 1); s = s.replace("\tc.repairIfFaulty(ctx, &reply)\n", "\tc.repairIfFaulty(ctx, &reply)\n\tc.repairIfScriptsFail(ctx, &reply, current, nil)\n", 1)' \
  ./internal/agent 'TestScripts_TheScriptCheckHasTheLastWord'

# The blind spots are now REMOVED by rendering the kernel's surface, and the
# apology survives only for the fallback picture. Both halves are drilled: that
# the kernel render is used at all, and that its picture carries no apology —
# telling a checker "a solid where a hole should be is correct here" about a
# picture in which holes are real teaches it to ignore a hole that failed to cut.
drill "the kernel surface is never rendered" internal/agent/render.go \
  's = s.replace("if c != nil && c.solids != nil {", "if false {", 1)' \
  ./internal/agent 'TestRender_TheKernelPictureCarriesNoApology'

drill "the kernel picture carries the apology anyway" internal/agent/render.go \
  's = s.replace("if sheet.FromKernel || doc == nil {", "if doc == nil {", 1)' \
  ./internal/agent 'TestRender_TheKernelPictureCarriesNoApology'

drill "the described picture carries no apology" internal/agent/render.go \
  's = s.replace("if tools := cuttingTools(doc); len(tools) > 0 {", "if false {", 1)' \
  ./internal/agent 'TestRender_TheKernelPictureCarriesNoApology|TestLook_IsToldWhichPartsAreCuttingTools'

# ‼️ This mutation must delete the phrase the fence looks for, not reword around
# it. The first version replaced "DO still say if " and left "floating clear"
# standing three words later, so the drill passed and proved nothing.
drill "look is told to ignore cutting tools entirely" internal/agent/render.go \
  's = s.replace("one is floating clear of the part it is meant to cut", "nothing", 1)' \
  ./internal/agent 'TestLook_IsToldWhichPartsAreCuttingTools'

drill "the surface is rebuilt for every check" internal/agent/sketch.go \
  's = s.replace("seen := c.matchesSketch(ctx, reply.Prototype, s, *sheet)", "seen := c.matchesSketch(ctx, reply.Prototype, s, c.render(ctx, reply.Prototype))", 1)' \
  ./internal/agent 'TestRender_TheKernelIsAskedOncePerTurn'

drill "a bad mesh index is trusted" internal/domain/geometry/mesh.go \
  's = s.replace("if o < 0 || o+2 >= len(verts) {", "if false {", 1)' \
  ./internal/domain/geometry 'TestTrianglesFrom'

drill "the vision check is not told what it cannot see" internal/agent/render.go \
  's = s.replace("if scripted := scriptedLabels(doc); len(scripted) > 0 {", "if false {", 1)' \
  ./internal/agent 'TestScripts_TheVisionCheckIsToldItCannotSeeThem'

echo
echo "The sandbox, after lambda was allowed"
# Widening the AST whitelist is the one change in this repository that can only
# make the sandbox weaker. These two drills are the price: one says the escape is
# still refused inside a lambda, the other says a lambda's parameter is bound —
# the half that is a SEPARATE rule from allowing the node, and whose absence
# refuses a correct script for using its own argument.
drill "the dunder rule does not reach inside a lambda" internal/domain/cad/script.py \
  's = s.replace("if isinstance(node, ast.Attribute) and node.attr.startswith(", "if False and node.attr.startswith(", 1)' \
  ./internal/domain/cad 'TestScript_RefusesTheWayOut'

drill "a lambda's parameters are not bound" internal/domain/cad/script.py \
  's = s.replace("        elif isinstance(node, ast.Lambda):\n            # Parameters only", "        elif False:\n            # Parameters only", 1)' \
  ./internal/domain/cad 'TestScript_ALambdaRunsAndItsParameterResolves'

# ‼️ No cutoff separates the good suggestions from the bad: Rotate -> Rotation
# scores 0.714 while module -> Mode scores 0.800. The shared-prefix requirement
# is what does it, and both directions are drilled — dropping it lets `module`
# suggest `Mode` again, and tightening it drops the suggestion this exists for.
drill "a suggestion need not share the start of the word" internal/domain/cad/script.py \
  's = s.replace("if shared_prefix(typed, candidate) >= 0.70 * len(typed):", "if True:", 1)' \
  ./internal/domain/cad 'TestScript_AnUnavailableNameSuggestsTheCloseOnes'

drill "the prefix requirement is tight enough to lose Rotation" internal/domain/cad/script.py \
  's = s.replace("0.70 * len(typed)", "1.00 * len(typed)", 1)' \
  ./internal/domain/cad 'TestScript_AnUnavailableNameSuggestsTheCloseOnes'

drill "the suggestion is case-sensitive again" internal/domain/cad/script.py \
  's = s.replace("difflib.get_close_matches(typed, sorted(folded)", "difflib.get_close_matches(name, sorted(folded)", 1)' \
  ./internal/domain/cad 'TestScript_AnUnavailableNameSuggestsTheCloseOnes'

drill "an unavailable name suggests nothing" internal/domain/cad/script.py \
  's = s.replace("_did_you_mean(node.id, known)", "\"\"", 1)' \
  ./internal/domain/cad 'TestScript_AnUnavailableNameSuggestsTheCloseOnes'

# The other direction, and the one that actually caught something: at difflib's
# default cutoff the refusal for `urlopen` came back "Did you mean len?".
#
# ‼️ Mutates BOTH the cutoff and the shared-prefix rule. Two rules now stop this
# independently, so breaking one leaves the other standing and the drill reports
# "not a fence" about a property that is doubly held — which is how defence in
# depth reads to a single-point mutation.
# Once the names resolved, what was left across four live gear requests was the
# model guessing at the API behind a name that EXISTS — BuildSketch(local_mode=…),
# Standard_TypeMismatch. The signature comes from the library that is installed,
# at the moment of failure, which is the only place it is guaranteed right.
# A scripted part could not see the document's own parameters — the numbers it is
# made of. Measured live: a model wrote `m = module` and every name was refused.
# The safety half matters as much: these names come from a model and go straight
# into the namespace a script executes in.
drill "the document's parameters are not in scope" internal/domain/cad/script.py \
  's = s.replace("            ns.setdefault(name, value)", "            pass", 1)' \
  ./internal/domain/cad 'TestScript_ReadsTheDocumentsParameters'

# ‼️ These two mutate BOTH rules that hold the property, because each is held
# twice: usable_parameters refuses the name, AND the injection is a setdefault
# that will not overwrite what the namespace already has. Breaking one leaves the
# other standing and the drill reports "not a fence" about something doubly
# protected — the third time in this file that defence in depth has read that way
# to a single-point mutation.
drill "a parameter can shadow a builder" internal/domain/cad/script.py \
  's = s.replace("        if name in taken:", "        if False:", 1); s = s.replace("ns.setdefault(name, value)", "ns[name] = value", 1)' \
  ./internal/domain/cad 'TestScript_AParameterCannotHijackTheNamespace'

drill "a parameter may begin with an underscore" internal/domain/cad/script.py \
  'i = s.index("names may not begin with an underscore"); j = s.rindex("if name.startswith(", 0, i); s = s[:j] + "if False: #" + s[j + len("if name.startswith("):]' \
  ./internal/domain/cad 'TestScript_AParameterCannotHijackTheNamespace'

# The regression this feature caused before it worked: every parameter handed
# over as a float, so range(teeth_count) raised in 6 of 9 live runs — worse than
# having no parameters at all.
drill "a whole-numbered parameter arrives as a float" internal/domain/cad/script.py \
  's = s.replace("usable[name] = int(number) if number.is_integer() else number", "usable[name] = number", 1)' \
  ./internal/domain/cad 'TestScript_AWholeNumberedParameterIsAnInt'

drill "a length reaches the script in its authored unit" internal/domain/cad/script.go \
  's = s.replace("if mm, converted := q.In(geometry.Millimetre); converted {", "if mm, converted := q.In(geometry.Millimetre); false {", 1)' \
  ./internal/domain/cad 'TestScriptParameters_LengthsInMillimetresAndNothingElseTouched'

# What was left after the names and signatures were answered: build123d USAGE.
# Both of these are computed from the installed library at the moment of failure,
# for the reason the signatures are — a list maintained here would go stale
# against the build123d that is actually installed.
drill "a misused with is not told what it can use" internal/domain/cad/script.py \
  's = s.replace("if \"context manager protocol\" in str(exc):", "if False:", 1)' \
  ./internal/domain/cad 'TestScript_AMisusedWithIsToldWhatItCanUse'

drill "a method is offered a spelling correction instead" internal/domain/cad/script.py \
  's = s.replace("hint = _method_hint(ns, _refused_name(detail))", "hint = \"\"", 1)' \
  ./internal/domain/cad 'TestScript_AMethodIsNotASpellingMistake'

drill "the method hint names the inherited leaves" internal/domain/cad/script.py \
  's = s.replace("defined = qual.split(\".\")[0] if \".\" in qual else owner", "defined = owner", 1)' \
  ./internal/domain/cad 'TestScript_AMethodIsNotASpellingMistake'

drill "a failure does not say how the builder is called" internal/domain/cad/script.py \
  's = s.replace("signature_help(ns, source, exc)", "\"\"", 1)' \
  ./internal/domain/cad 'TestScript_AFailureSaysHowTheBuilderIsCalled'

drill "the failing LINE is not consulted" internal/domain/cad/script.py \
  's = s.replace("_names_in_message(str(exc)) + _names_on_line(source, _failing_line(exc))", "_names_in_message(str(exc))", 1)' \
  ./internal/domain/cad 'TestScript_AFailureSaysHowTheBuilderIsCalled'

drill "a suggested name comes without its signature" internal/domain/cad/script.py \
  's = s.replace("detail += _suggestion_signatures(ns, detail)", "pass", 1)' \
  ./internal/domain/cad 'TestScript_ASuggestedNameComesWithItsSignature'

# ‼️ And the other direction: the signature help must not turn a refusal about
# reaching OUTSIDE the sandbox into an API tutorial.
#
# Mutating the "Did you mean" GUARD does not work — it makes the parse raise, the
# runner dies, and a crashed run contains no signature either, so the fence stays
# green while measuring nothing. The real lever is the cutoff: loosen it and
# `urlopen` suggests a name again, and the suggestion then drags a signature in
# behind it. Both tests are named because the two effects arrive together.
drill "a security refusal gains a signature lesson" internal/domain/cad/script.py \
  's = s.replace("cutoff=0.70", "cutoff=0.30", 1); s = s.replace("if shared_prefix(typed, candidate) >= 0.70 * len(typed):", "if True:", 1)' \
  ./internal/domain/cad 'TestScript_ReachingOutsideStillSaysNothingHelpful|TestScript_AnUnavailableNameSuggestsTheCloseOnes'

# `@` was allowed on 2026-09-10 — every other binary operator on that line
# dispatches to a dunder method and `%`, its sibling in build123d, was already
# there. Two drills, as with lambda: that it runs, and that the escape is still
# refused when spelled through it.
drill "the @ operator is refused again" internal/domain/cad/script.py \
  's = s.replace("    ast.MatMult,", "", 1)' \
  ./internal/domain/cad 'TestScript_TheAtOperatorRunsAndComputesTheRightPoint'

drill "the dunder rule does not reach through an @ expression" internal/domain/cad/script.py \
  's = s.replace("if isinstance(node, ast.Attribute) and node.attr.startswith(", "if False and node.attr.startswith(", 1)' \
  ./internal/domain/cad 'TestScript_RefusesTheWayOut'

drill "a refusal names the parser's word, not the author's" internal/domain/cad/script.py \
  's = s.replace("_syntax_name(node)", "type(node).__name__", 1)' \
  ./internal/domain/cad 'TestScript_ARefusalNamesWhatWasWritten'

drill "the suggestion cutoff is difflib's loose default" internal/domain/cad/script.py \
  's = s.replace("cutoff=0.70", "cutoff=0.60", 1); s = s.replace("if shared_prefix(typed, candidate) >= 0.70 * len(typed):", "if True:", 1)' \
  ./internal/domain/cad 'TestScript_AnUnavailableNameSuggestsTheCloseOnes'

echo
echo "Drawing the thing before building it"
drill "the drawing is made but never reaches the prompt" internal/agent/converse.go \
  's = s.replace("history, prompt, workspaceNote, current, images)", "history, message, workspaceNote, current, images)", 1)' \
  ./internal/agent 'TestSketch_DrawnFirstAndItsFormReachesTheGeometryPrompt'

# The safety property. A generated picture has no dimensions and gets counts
# wrong (28-30 teeth for 20, measured), so it must never be the reason a number
# changed. Both halves are drilled: the prompt that forbids it, and the
# acceptance rule that refuses a resize whatever the complaint claimed.
drill "the comparison may report counts and sizes" internal/agent/sketch.go \
  's = s.replace("- Do NOT count anything", "- You may count things", 1)' \
  ./internal/agent 'TestSketch_APictureCannotChangeANumber'

drill "a resizing correction is accepted" internal/agent/sketch.go \
  's = s.replace("len(turnedOnItsSide(before, fixed)) != 0", "false", 1)' \
  ./internal/agent 'TestSketch_APictureCannotChangeANumber'

drill "every turn pays for a drawing" internal/agent/sketch.go \
  's = s.replace("if prompt == \"\" {\n\t\treturn nil\n\t}", "if false {\n\t\treturn nil\n\t}\n\tprompt = prompt + \" a shape\"", 1)' \
  ./internal/agent 'TestSketch_NotEveryTurnIsDrawn'

drill "a drawing is made with nothing able to read it" internal/agent/sketch.go \
  's = s.replace("c.client.ModelFor(llm.RoleVision) == \"\"", "false", 1)' \
  ./internal/agent 'TestSketch_NoVisionMeansNoDrawing'

drill "the reference is not sent first" internal/agent/sketch.go \
  's = s.replace("[]string{s.Image, sheet.Image}", "[]string{sheet.Image, s.Image}", 1)' \
  ./internal/agent 'TestSketch_BothPicturesAreSentInOrder'

# Three deterministic guards, each standing where a PROMPT rule was measured
# being ignored: the reading said "Two circular holes", the comparison reported
# pegs on a part whose holes are cut, and it reported "three bolt holes ... only
# two" as a defect. A prompt states an intention; these enforce it.
drill "numbers from the drawing reach the geometry prompt" internal/agent/sketch.go \
  's = s.replace("func withoutNumbers(s string) string {", "func withoutNumbers(s string) string {\n\treturn s", 1)' \
  ./internal/agent 'TestSketch_NumbersAreStrippedFromWhatTheDrawingSays'

# Two drills, because the unit fence calls formOnly DIRECTLY and stays green if
# the call site is deleted — which the first run of this drill proved. One breaks
# the filter, the other unhooks it.
drill "the count filter does nothing" internal/agent/sketch.go \
  's = s.replace("func formOnly(problems []geometry.Problem) []geometry.Problem {", "func formOnly(problems []geometry.Problem) []geometry.Problem {\n\treturn problems", 1)' \
  ./internal/agent 'TestSketch_ComplaintsAboutCountsAreDropped'

drill "the count filter is not wired in" internal/agent/sketch.go \
  's = s.replace("return formOnly(parseLook(resp.Content))", "return parseLook(resp.Content)", 1)' \
  ./internal/agent 'TestSketch_TheComparisonActuallyFiltersCounts'

drill "the comparison is not told what the picture is lying about" internal/agent/sketch.go \
  's = s.replace("describedRenderNote(doc, sheet)", "\"\"", 1)' \
  ./internal/agent 'TestSketch_TheComparisonIsToldACutIsDrawnAsASolid'

drill "a chat-shaped reply is read as a drawing" internal/llm/illustrate.go \
  's = s.replace("part.Type == \"image\" && strings.HasPrefix(part.Image, \"https://\")", "true", 1)' \
  ./internal/llm 'TestDecodeDrawing'

echo
echo "How long a turn may take"
drill "a turn is bounded by one model call's timeout" internal/httpapi/converse.go \
  's = s.replace("budget := h.deps.Config.LLM.TurnBudget", "budget := h.deps.Config.LLM.RequestTimeout + 15*time.Second", 1)' \
  ./internal/httpapi 'TestConverse_TurnBudgetBoundsTheTurnNotOneCall'

drill "an unset budget is read as no time at all" internal/httpapi/converse.go \
  's = s.replace("\tif budget <= 0 {", "\tif false {", 1)' \
  ./internal/httpapi 'TestConverse_AnUnsetTurnBudgetIsNotAnExpiredOne'

drill "the connection closes under a working turn" internal/httpapi/converse.go \
  's = s.replace("http.NewResponseController(w).SetWriteDeadline(time.Time{})", "error(nil)", 1)' \
  ./internal/httpapi 'TestConverse_AStreamOutlivesTheServerWriteTimeout'

drill "the provider extension is sent to every endpoint" internal/llm/deliberation.go \
  's = s.replace("\tfor domain, field := range deliberationField {", "\treturn \"enable_thinking\", true\n\tfor domain, field := range deliberationField {", 1)' \
  ./internal/llm 'TestAnUnknownEndpointIsNotSentAProviderExtension|TestWhichEndpointsUnderstandTheField'

drill "the host match is a bare suffix, not a domain one" internal/llm/deliberation.go \
  's = s.replace("if host == domain || strings.HasSuffix(host, \".\"+domain) {", "if strings.HasSuffix(host, domain) {", 1)' \
  ./internal/llm 'TestWhichEndpointsUnderstandTheField'

drill "a retired model is reported without naming the survivors" internal/llm/openai_compatible.go \
  's = s.replace("\tserved, err := c.servedModels(ctx)", "\tserved, err := []string(nil), error(nil)\n\t_ = c.servedModels", 1)' \
  ./internal/llm 'TestAMissingModelNamesWhatTheEndpointDoesServe'

echo
echo "A worker carries a plan to its end"
# Added 2026-09-15. Three engine defects found building stage A1 (#85): a finished
# task released nothing, so a plan stopped after its first layer; a budget refusal
# could not fail a task that was only claimed, so a spent goal never stopped; and
# events were hashed at nanoseconds but stored at microseconds, so every event the
# real clock wrote failed the audit chain. Needs FORGE_TEST_DATABASE_URL.
drill "a finished task releases nothing" internal/agent/worker.go \
  's = s.replace("\tw.releaseWaiting(book, goalID)\n", "", 1)' \
  ./internal/agent 'TestWorker_AFinishedTaskReleasesTheTasksWaitingOnIt'

drill "the idle poll releases nothing" internal/agent/worker.go \
  's = s.replace("\t\t\tw.releaseWaitingGoals(ctx)\n", "", 1)' \
  ./internal/agent 'TestWorker_ATaskLeftWaitingByACrashIsReleasedOnTheIdlePoll'

drill "a budget refusal fails a task that is only claimed" internal/agent/worker.go \
  's = s.replace("\t\tif err := w.transition(ctx, task, engine.StatusRunning, engine.TaskMutation{}); err != nil {\n\t\t\treturn\n\t\t}\n", "", 1)' \
  ./internal/agent 'TestWorker_ABudgetRefusalStopsTheGoal'

drill "an event is hashed at a precision it is not stored at" internal/domain/engine/repository.go \
  's = s.replace("\tnow = now.Truncate(time.Microsecond)\n", "", 1)' \
  ./internal/domain/engine 'TestAuditChain_AnEventStampedAtNanosecondsVerifies'

echo
echo "A stopping worker's bookkeeping"
# Added 2026-09-15, found exercising a live build goal (#104). A graceful stop cancels
# the worker's context mid-task, and what the task's end sets moving (releasing waiting
# tasks, settling the goal) must run on a context of its own, or each fails on the
# cancelled one and is logged as the database being unavailable. See
# docs/bugfix/2026-09-15-a-stopping-worker-reported-its-own-stop-as-a-database-outage.md.
# Needs FORGE_TEST_DATABASE_URL.
drill "a stopping worker's bookkeeping runs on the cancelled context" internal/agent/worker.go \
  's = s.replace("context.WithTimeout(context.WithoutCancel(ctx), afterTaskTimeout)", "context.WithTimeout(ctx, afterTaskTimeout)", 1)' \
  ./internal/agent 'TestWorker_AWorkerStoppedMidTaskDoesNotReportItsBookkeepingAsADatabaseFailure|TestWorker_ATaskFinishedAsTheStopArrivesStillReleasesItsDependentsAndSettlesItsGoal'

echo
echo "A stopped worker hands its task back"
# Added 2026-09-15 (a stopped worker hands its task back). A graceful stop cancels the
# worker's context mid-task; the task must go back to the queue at once, its stopped
# attempt not counted, instead of staying leased until the reaper finds it, and a real
# failure must still be retried and failed. See
# docs/bugfix/2026-09-15-a-stopped-worker-left-its-task-to-run-out-its-lease.md.
# Needs FORGE_TEST_DATABASE_URL.
drill "a stopped worker leaves its task to its lease" internal/agent/worker.go \
  's = s.replace("\t\tif ctx.Err() != nil {\n\t\t\tw.handBack(ctx, task)\n\t\t}\n", "", 1)' \
  ./internal/agent 'TestWorker_AWorkerStoppedInsideAModelCallHandsItsTaskBackAtOnce|TestWorker_AWorkerStoppedBeforeItsTaskStartsHandsItBackUnstarted|TestWorker_AWorkerStoppedAtTheApprovalGateHandsItsTaskBackAndTheGateIsOpenedOnce'

drill "a stop still counts as an attempt" internal/domain/engine/queue.go \
  's = s.replace("not_before = $3,\n\t\t       attempt_count = greatest(attempt_count - 1, 0)\n", "not_before = $3\n", 1)' \
  ./internal/domain/engine 'TestQueue_AReleasedTaskIsClaimableAtOnceAndItsAttemptIsNotCounted'

drill "a stop still counts as an attempt, through the worker" internal/domain/engine/queue.go \
  's = s.replace("not_before = $3,\n\t\t       attempt_count = greatest(attempt_count - 1, 0)\n", "not_before = $3\n", 1)' \
  ./internal/agent 'TestWorker_AWorkerStoppedInsideAModelCallHandsItsTaskBackAtOnce'

drill "a task stopped in verification cannot be released" internal/domain/engine/queue.go \
  's = s.replace("where id = $1 and lease_owner = $2 and status in (\x27claimed\x27,\x27running\x27,\x27verifying\x27)", "where id = $1 and lease_owner = $2 and status in (\x27claimed\x27,\x27running\x27)", 1)' \
  ./internal/domain/engine 'TestQueue_ATaskStoppedDuringVerificationCanBeReleased'

drill "a stop is recorded as a failed attempt" internal/agent/worker.go \
  's = s.replace("\tif ctx.Err() != nil {\n\t\treturn\n\t}\n\tif breach := w.budget.CheckAttempts(task)", "\tif breach := w.budget.CheckAttempts(task)", 1)' \
  ./internal/agent 'TestWorker_AWorkerStoppedInsideAModelCallHandsItsTaskBackAtOnce'

drill "a genuine failure is dropped as if it were a stop" internal/agent/worker.go \
  's = s.replace("\tif ctx.Err() != nil {\n\t\treturn\n\t}\n\tif breach := w.budget.CheckAttempts(task)", "\tif ctx.Err() != nil || true {\n\t\treturn\n\t}\n\tif breach := w.budget.CheckAttempts(task)", 1)' \
  ./internal/agent 'TestWorker_ATaskThatFailsWhileItsWorkerRunsIsStillRetriedAndThenFailed'

echo
echo "A stopped worker keeps what it did"
# Added 2026-09-15 (stop leftovers). A stop must leave an approval request and its
# approval.requested event both or neither; must still record what had happened before
# it (an event, spent tokens, a tool call that ran); must skip, not fail, what it cut
# short (a decision, a tool call, a checkpoint); and must log no DATABASE_UNAVAILABLE.
# Mutations keep the code compiling, so a red here is the fence and not the build. See
# docs/bugfix/2026-09-15-a-stopped-worker-lost-what-it-had-done-and-blamed-the-database.md.
# Needs FORGE_TEST_DATABASE_URL.
drill "the approval request and its event are written apart" internal/agent/worker.go \
  's = s.replace("if _, err := tx.Exec(ctx, `\n\t\t\tinsert into forge_approvals", "if _, err := w.pool.Exec(ctx, `\n\t\t\tinsert into forge_approvals", 1)' \
  ./internal/agent 'TestWorker_AStopBetweenOpeningAnApprovalRequestAndRecordingItLeavesTheTimelineAndTheApprovalsAgreeing'

drill "a failure reached while stopping is still written" internal/agent/worker.go \
  's = s.replace("\t// database being unavailable. Run hands the task back instead.\n\tif ctx.Err() != nil {\n\t\treturn\n\t}\n", "\t// database being unavailable. Run hands the task back instead.\n", 1)' \
  ./internal/agent 'TestWorker_AStopBetweenOpeningAnApprovalRequestAndRecordingItLeavesTheTimelineAndTheApprovalsAgreeing'

drill "a decision the stop refused is logged as a database failure" internal/agent/worker.go \
  's = s.replace("if err != nil && ctx.Err() == nil {\n\t\tw.log.WarnWith(ctx, logx.EventTaskCycleEnded, err,", "if err != nil {\n\t\tw.log.WarnWith(ctx, logx.EventTaskCycleEnded, err,", 1)' \
  ./internal/agent 'TestWorker_AWorkerStoppedAnywhereInATaskKeepsWhatItDidAndLogsNoDatabaseFailure/^at_the_approval_gate$'

drill "a heartbeat the stop cancelled reports a lost lease" internal/agent/worker.go \
  's = s.replace("\t\t\t\tif ctx.Err() != nil {\n\t\t\t\t\treturn\n\t\t\t\t}\n\t\t\t\t// Losing the lease", "\t\t\t\t// Losing the lease", 1)' \
  ./internal/agent 'TestWorker_AWorkerStoppedAnywhereInATaskKeepsWhatItDidAndLogsNoDatabaseFailure'

drill "a blocked answer that arrives with the stop is recorded as a failure" internal/agent/worker.go \
  's = s.replace("\t\tif ctx.Err() != nil {\n\t\t\treturn\n\t\t}\n\t\tw.appendEvent(ctx, goal.ID, &task.ID, engine.EventTaskFailed", "\t\tw.appendEvent(ctx, goal.ID, &task.ID, engine.EventTaskFailed", 1)' \
  ./internal/agent 'TestWorker_AWorkerStoppedAnywhereInATaskKeepsWhatItDidAndLogsNoDatabaseFailure/^as_a_model_call_answers_that_the_task_is_blocked$'

drill "a success the stop refused is recorded" internal/agent/worker.go \
  's = s.replace("if err := w.transition(ctx, task, engine.StatusSucceeded, engine.TaskMutation{Result: resultJSON}); err != nil {\n\t\t\treturn\n\t\t}\n", "w.transition(ctx, task, engine.StatusSucceeded, engine.TaskMutation{Result: resultJSON})\n", 1)' \
  ./internal/agent 'TestWorker_AWorkerStoppedAnywhereInATaskKeepsWhatItDidAndLogsNoDatabaseFailure/^as_a_model_call_answers_that_the_task_is_done$'

drill "an event is written on the context the stop cancelled" internal/agent/worker.go \
  's = s.replace("\trec, cancel := outliving(ctx)\n\tdefer cancel()\n\tif err := w.repo.AppendEvent(rec", "\trec, cancel := context.WithCancel(ctx)\n\tdefer cancel()\n\tif err := w.repo.AppendEvent(rec", 1)' \
  ./internal/agent 'TestWorker_AWorkerStoppedAsItRecoversACrashedWorkersTaskRecordsTheRecoveryAndLogsNoDatabaseFailure'

drill "a claim the stop refused is logged as a database failure" internal/agent/worker.go \
  's = s.replace("\t\t\tif ctx.Err() != nil {\n\t\t\t\tcontinue // stopped while claiming", "\t\t\tif false {\n\t\t\t\tcontinue // stopped while claiming", 1)' \
  ./internal/agent 'TestWorker_AWorkerStoppedAsItRecoversACrashedWorkersTaskRecordsTheRecoveryAndLogsNoDatabaseFailure'

drill "the tokens a stopped call spent go uncounted" internal/agent/executor.go \
  's = s.replace("rec, cancelRec := outliving(ctx)", "rec, cancelRec := context.WithCancel(ctx)", 1)' \
  ./internal/agent 'TestWorker_AWorkerStoppedAnywhereInATaskKeepsWhatItDidAndLogsNoDatabaseFailure/^as_a_model_call_answers_that_the_task_is_done$'

drill "a stopped worker runs its next tool call" internal/agent/executor.go \
  's = s.replace("\t\t\tif ctx.Err() != nil {\n\t\t\t\treturn nil, ctx.Err()\n\t\t\t}\n\t\t\ttotalCalls++", "\t\t\ttotalCalls++", 1)' \
  ./internal/agent 'TestWorker_AWorkerStoppedAnywhereInATaskKeepsWhatItDidAndLogsNoDatabaseFailure/^as_a_tool_call_finishes$'

drill "a tool call that ran is lost from the ledger" internal/agent/executor.go \
  's = s.replace("rec, cancel := outliving(ctx)\n\tdefer cancel()\n\terr := db.InTx(rec", "rec, cancel := context.WithCancel(ctx)\n\tdefer cancel()\n\terr := db.InTx(rec", 1)' \
  ./internal/agent 'TestWorker_AWorkerStoppedAnywhereInATaskKeepsWhatItDidAndLogsNoDatabaseFailure/^as_a_tool_call_finishes$'

drill "a tool call the stop cut short is recorded as failed" internal/agent/executor.go \
  's = s.replace("\t\tif ctx.Err() != nil {\n\t\t\treturn toolError(", "\t\tif false {\n\t\t\treturn toolError(", 1)' \
  ./internal/agent 'TestWorker_AWorkerStoppedAnywhereInATaskKeepsWhatItDidAndLogsNoDatabaseFailure/^inside_a_tool_call$'

drill "a checkpoint is saved from an iteration the stop cut short" internal/agent/executor.go \
  's = s.replace("\t\tif ctx.Err() != nil {\n\t\t\treturn nil, ctx.Err()\n\t\t}\n\t\tstate, _ := json.Marshal(", "\t\tstate, _ := json.Marshal(", 1)' \
  ./internal/agent 'TestWorker_AWorkerStoppedAnywhereInATaskKeepsWhatItDidAndLogsNoDatabaseFailure/^inside_a_tool_call$'

echo
echo "The last four things a stop got wrong"
# Added 2026-09-15 (last stop items). The four things #109's doc left open. A statement
# the stop cancelled AFTER Postgres committed it must be read back, not guessed at, and
# not guessed at in the other direction either. A stop during verification must not warn
# that the verifier failed. A suspected injection found as the worker stops must survive
# on the timeline. And the polling path's three reconciliations must be skipped quietly
# when a stop cancels them, without becoming sweeps that never run. Mutations keep the
# code compiling, so a red here is the fence and not the build. See
# docs/bugfix/2026-09-15-a-stop-still-guessed-at-a-committed-write-and-lost-a-security-record.md.
# Needs FORGE_TEST_DATABASE_URL.
drill "a transition the stop cancelled in flight is assumed refused" internal/agent/worker.go \
  's = s.replace("if err != nil && ctx.Err() != nil && w.stopLanded(ctx, task, to) {\n\t\treturn nil\n\t}\n", "", 1)' \
  ./internal/agent 'TestWorker_ASuccessTheStopCancelledAfterPostgresHadCommittedItIsStillOnTheTimeline'

drill "a transition the stop cancelled in flight is assumed to have landed" internal/agent/worker.go \
  's = s.replace("if current.Status != to {", "if false \x26\x26 current.Status != to {", 1)' \
  ./internal/agent 'TestWorker_AWorkerStoppedAnywhereInATaskKeepsWhatItDidAndLogsNoDatabaseFailure/^as_a_model_call_answers_that_the_task_is_done$'

drill "a stop during verification is logged as a verifier failure" internal/agent/worker.go \
  's = s.replace("\t\tif ctx.Err() == nil {\n\t\t\tw.log.WarnWith(ctx, logx.EventVerificationRan, err, \x22task_id\x22, task.ID)", "\t\tif true {\n\t\t\tw.log.WarnWith(ctx, logx.EventVerificationRan, err, \x22task_id\x22, task.ID)", 1)' \
  ./internal/agent 'TestWorker_AStopDuringVerificationDoesNotWarnThatTheVerifierFailed'

drill "a suspected injection is recorded on the context the stop cancelled" internal/agent/executor.go \
  's = s.replace("\trec, cancel := outliving(ctx)\n\tdefer cancel()\n\tif err := e.repo.AppendEvent(rec", "\trec, cancel := context.WithCancel(ctx)\n\tdefer cancel()\n\tif err := e.repo.AppendEvent(rec", 1)' \
  ./internal/agent 'TestWorker_ASuspectedInjectionFoundAsTheWorkerStopsIsStillRecordedOnTheTimeline'

drill "the lease reaper runs on the context the stop cancelled" internal/agent/worker.go \
  's = s.replace("\tif ctx.Err() != nil {\n\t\treturn\n\t}\n\treaped, err := w.queue.ReapExpiredLeases", "\tif false {\n\t\treturn\n\t}\n\treaped, err := w.queue.ReapExpiredLeases", 1); s = s.replace("\t\tif ctx.Err() == nil {\n\t\t\tw.log.WarnWith(ctx, logx.EventWorkerReaped", "\t\tif true {\n\t\t\tw.log.WarnWith(ctx, logx.EventWorkerReaped", 1)' \
  ./internal/agent 'TestWorker_ThePollSweepsAStopCancelledAreSkippedQuietlyAndStillRunWhenNothingStoppedThem'

drill "the release sweep runs on the context the stop cancelled" internal/agent/settle.go \
  's = s.replace("\tif ctx.Err() != nil {\n\t\treturn\n\t}\n\trows, err := w.pool.Query(ctx, `\n\t\tselect distinct t.goal_id", "\tif false {\n\t\treturn\n\t}\n\trows, err := w.pool.Query(ctx, `\n\t\tselect distinct t.goal_id", 1); s = s.replace("\t\tif ctx.Err() == nil {\n\t\t\tw.log.WarnWith(ctx, logx.EventTaskReleaseFailed", "\t\tif true {\n\t\t\tw.log.WarnWith(ctx, logx.EventTaskReleaseFailed", 1)' \
  ./internal/agent 'TestWorker_ThePollSweepsAStopCancelledAreSkippedQuietlyAndStillRunWhenNothingStoppedThem'

drill "the settle sweep runs on the context the stop cancelled" internal/agent/settle.go \
  's = s.replace("\tif ctx.Err() != nil {\n\t\treturn\n\t}\n\trows, err := w.pool.Query(ctx, `\n\t\tselect g.id", "\tif false {\n\t\treturn\n\t}\n\trows, err := w.pool.Query(ctx, `\n\t\tselect g.id", 1); s = s.replace("if ctx.Err() == nil {\n\t\t\tw.log.WarnWith(ctx, logx.EventGoalSettleFailed, err,\n", "if true {\n\t\t\tw.log.WarnWith(ctx, logx.EventGoalSettleFailed, err,\n", 1)' \
  ./internal/agent 'TestWorker_ThePollSweepsAStopCancelledAreSkippedQuietlyAndStillRunWhenNothingStoppedThem'

# The other direction: a guard that skips the sweep whether or not anything stopped it is
# a reconciliation that never reconciles, which is what these two sweeps exist to be.
drill "the release sweep is skipped even when nothing stopped it" internal/agent/settle.go \
  's = s.replace("\tif ctx.Err() != nil {\n\t\treturn\n\t}\n\trows, err := w.pool.Query(ctx, `\n\t\tselect distinct t.goal_id", "\tif true {\n\t\treturn\n\t}\n\trows, err := w.pool.Query(ctx, `\n\t\tselect distinct t.goal_id", 1)' \
  ./internal/agent 'TestWorker_ThePollSweepsAStopCancelledAreSkippedQuietlyAndStillRunWhenNothingStoppedThem'

drill "the settle sweep is skipped even when nothing stopped it" internal/agent/settle.go \
  's = s.replace("\tif ctx.Err() != nil {\n\t\treturn\n\t}\n\trows, err := w.pool.Query(ctx, `\n\t\tselect g.id", "\tif true {\n\t\treturn\n\t}\n\trows, err := w.pool.Query(ctx, `\n\t\tselect g.id", 1)' \
  ./internal/agent 'TestReconciliationSweepSettlesWhatTheEventMissed'

echo
echo "The kernel"
drill "the kernel uses OCCT's default transition" internal/domain/cad/sidecar.py \
  "s = s.replace('transition=Transition.RIGHT', 'transition=Transition.TRANSFORMED', 1)" \
  ./internal/domain/cad 'TestKernel_SweepsAnOutlineAlongAPath'

drill "the kernel ignores the section frame it was sent" internal/domain/cad/sidecar.py \
  "s = s.replace('x_dir=Vector(m[0], m[3], m[6]),\n                      z_dir=Vector(m[2], m[5], m[8]))', 'x_dir=Vector(1, 0, 0),\n                      z_dir=Vector(0, 0, 1))', 1)" \
  ./internal/domain/cad 'TestKernel_ASweptSolidIsTheOneTheRendererDrew'

# Added 2026-09-10. The STEP import that makes a script's solid a part was first
# written into _apply, after a return, and every scripted part was left out of
# every export and built mesh for a whole release — while the turn said it built.
# Every script test stopped at RunScript. See
# docs/bugfix/2026-09-10-scripted-parts-never-exported.md.
drill "a scripted part is unreachable in the shape dispatch" internal/domain/cad/sidecar.py \
  's = s.replace("    if kind == \"step\":\n        # A solid that was built somewhere else", "    if kind == \"step-unreachable\":\n        # A solid that was built somewhere else", 1)' \
  ./internal/domain/cad 'TestKernel_AScriptedPartIsExportedAndMeshed'

drill "a refused assembly hides which part it refused" internal/domain/cad/cad.go \
  's = s.replace("\t\tif len(res.Skipped) > 0 {\n\t\t\tdetail +=", "\t\tif false {\n\t\t\tdetail +=", 1)' \
  ./internal/domain/cad 'TestKernel_ARefusedAssemblyNamesWhatItRefused'

# ---------------------------------------------------------------------------
# Added 2026-09-15 (workbench voice input).
#
# The owner held the workbench microphone from mainland China and nothing
# reached FORGE. Push-to-talk used only the browser's recogniser, which Chrome
# runs on Google's servers; a transcript that arrived during a turn was dropped
# by send(); the hold ended on mouseleave; a quick second press was swallowed;
# and several failures said nothing. The voice fences run the real voice.js in
# node against a stubbed browser, so each of these is a behaviour, not a
# string. See docs/bugfix/2026-09-15-the-microphone-sent-nothing.md.
# ---------------------------------------------------------------------------

echo
echo "The workbench microphone"
drill "push-to-talk goes back to the browser recogniser" internal/httpapi/assets/voice.js \
  's = s.replace("if (this.serverASR !== null && this._canRecord()) return \x27server\x27;", "if (false) return \x27server\x27;", 1)' \
  ./internal/httpapi 'TestVoiceInput_PushToTalkRecordsAndUploadsToTheServer'

drill "the upload drops the recording content type" internal/httpapi/assets/voice.js \
  's = s.replace("headers: { \x27Content-Type\x27: blob.type || \x27audio/webm\x27 },", "headers: {},", 1)' \
  ./internal/httpapi 'TestVoiceInput_PushToTalkRecordsAndUploadsToTheServer'

drill "a transcript during a turn is sent, and dropped" internal/httpapi/assets/voice.js \
  's = s.replace("    if (!ctx.busy) {", "    if (true) {", 1)' \
  ./internal/httpapi 'TestVoiceInput_WhatWasSaidDuringATurnIsKeptInTheTextBox'

drill "what was said overwrites what was typed" internal/httpapi/assets/voice.js \
  's = s.replace("ctx.input.value = typed ? typed + \x27 \x27 + text : text;", "ctx.input.value = text;", 1)' \
  ./internal/httpapi 'TestVoiceInput_WhatWasSaidDuringATurnIsKeptInTheTextBox'

drill "the hold captures no pointer" internal/httpapi/assets/voice.js \
  's = s.replace("      if (button.setPointerCapture && pointer != null) {", "      if (false) {", 1)' \
  ./internal/httpapi 'TestVoiceInput_TheHoldSurvivesTheCursorLeavingTheButton'

drill "a click is treated as a hold" internal/httpapi/assets/voice.js \
  's = s.replace("        if (took < min) {", "        if (false) {", 1)' \
  ./internal/httpapi 'TestVoiceInput_AQuickPressSaysHoldToTalk'

drill "a press before the session ended is swallowed again" internal/httpapi/assets/voice.js \
  's = s.replace("(self._restartWhenEnded || self.mode === \x27hands-free\x27)", "(self.mode === \x27hands-free\x27)", 1)' \
  ./internal/httpapi 'TestVoiceInput_PressingAgainBeforeTheLastSessionEndedStillListens'

drill "no-speech during a hold is silent again" internal/httpapi/assets/voice.js \
  's = s.replace("        if (self.mode === \x27push\x27) {", "        if (false) {", 1)' \
  ./internal/httpapi 'TestVoiceInput_EveryFailureReachesTheNote/no_speech_while_holding'

drill "a recogniser that cannot reach Google is still offered" internal/httpapi/assets/voice.js \
  's = s.replace("        self.browserBroken = \"The browser", "        self.browserBrokenX = \"The browser", 1)' \
  ./internal/httpapi 'TestVoiceInput_EveryFailureReachesTheNote/browser_recognition_blocked'

drill "a deployment with no transcriber is asked again on every hold" internal/httpapi/assets/voice.js \
  's = s.replace("        this.serverASR = null;\n        this._serverWhy =", "        this._serverWhy =", 1)' \
  ./internal/httpapi 'TestVoiceInput_EveryFailureReachesTheNote/not_served_here'

drill "an empty transcript is swallowed silently" internal/httpapi/assets/voice.js \
  's = s.replace("    if (!text) {\n      this.onError(\x27No words", "    if (false) {\n      this.onError(\x27No words", 1)' \
  ./internal/httpapi 'TestVoiceInput_EveryFailureReachesTheNote/nothing_recognised'

drill "a provider failure is not said" internal/httpapi/assets/voice.js \
  's = s.replace("      this.onError(\x27Transcription failed (\x27 +", "      void (\x27Transcription failed (\x27 +", 1)' \
  ./internal/httpapi 'TestVoiceInput_EveryFailureReachesTheNote/provider_error'

drill "an empty recording is uploaded" internal/httpapi/assets/voice.js \
  's = s.replace("    if (!blob.size) {", "    if (false) {", 1)' \
  ./internal/httpapi 'TestVoiceInput_EveryFailureReachesTheNote/nothing_captured'

drill "the mic ends the hold on mouseleave again" internal/httpapi/assets/workbench.js \
  's = s.replace("    var hold = ForgeVoice.bindHold($(\x27mic\x27), voice, { note: voiceNote });", "    var hold = ForgeVoice.makeHold(voice, { note: voiceNote }); $(\x27mic\x27).addEventListener(\x27mouseleave\x27, function () { hold.release(); });", 1)' \
  ./internal/httpapi 'TestWorkbench_TheMicIsWiredToTheHoldAndKeepsWhatWasSaid'

drill "a transcript goes straight to send() again" internal/httpapi/assets/workbench.js \
  's = s.replace("        if (ForgeVoice.deliverSpoken(text, { busy: state.busy, input: $(\x27say\x27), send: send, note: voiceNote }) === \x27sent\x27) {", "        send(text); if (false) {", 1)' \
  ./internal/httpapi 'TestWorkbench_TheMicIsWiredToTheHoldAndKeepsWhatWasSaid'

drill "a typed message is cleared while a turn is in flight" internal/httpapi/assets/workbench.js \
  's = s.replace("      if (state.busy) {\n        voiceNote(\x27FORGE is still answering. Your message", "      if (false) {\n        voiceNote(\x27FORGE is still answering. Your message", 1)' \
  ./internal/httpapi 'TestWorkbench_TheMicIsWiredToTheHoldAndKeepsWhatWasSaid'

drill "the workbench never learns the server transcribes" internal/httpapi/assets/workbench.js \
  's = s.replace("      if (voice) voice.setServerTranscription(", "      if (voice) void (", 1)' \
  ./internal/httpapi 'TestWorkbench_TheMicIsWiredToTheHoldAndKeepsWhatWasSaid'

drill "the transcription route is not mounted" internal/httpapi/router.go \
  's = s.replace("\tmux.Handle(\"POST /v1/transcribe\", authed(converse.Transcribe))", "\t_ = converse.Transcribe", 1)' \
  ./internal/httpapi 'TestTranscribe_TheRouteIsMountedAndRequiresASession'

drill "codec parameters reach the transcriber" internal/httpapi/transcribe.go \
  's = s.replace("\t\tif mt == c {\n\t\t\treturn c, true", "\t\tif mt == c {\n\t\t\treturn contentType, true", 1)' \
  ./internal/httpapi 'TestTranscribe_RecordedAudioComesBackAsText|TestTranscribe_EveryBrowserRecordingContainerIsAccepted'

drill "an oversized recording is sent to the provider" internal/httpapi/transcribe.go \
  's = s.replace("\tif len(audio) > maxRecordingBytes {", "\tif false {", 1)' \
  ./internal/httpapi 'TestTranscribe_AnOversizedRecordingIsRefusedByName'

drill "a deployment without a transcriber is not refused by name" internal/httpapi/transcribe.go \
  's = s.replace("\tif stt == nil {", "\tif false {", 1)' \
  ./internal/httpapi 'TestTranscribe_ADeploymentWithoutATranscriberSaysWhatTurnsItOn'

drill "a model that never answered is reported as silence" internal/httpapi/transcribe.go \
  's = s.replace("\tif out.Unanswered {", "\tif false {", 1)' \
  ./internal/httpapi 'TestTranscribe_AModelThatAnsweredWithoutATranscriptIsNotSilence'

drill "the page is never told the server transcribes" internal/httpapi/converse.go \
  's = s.replace("\"server\": transcriber != \"\"", "\"server\": false", 1)' \
  ./internal/httpapi 'TestTranscribe_TheWorkbenchIsToldWhetherTheServerTranscribes'

drill "an unserved transcription model is reported as an outage" internal/llm/transcribe.go \
  's = s.replace("\t\t\tcode = errs.CodeConnectorUnavailable", "\t\t\tcode = errs.CodeExternalUnavailable", 1)' \
  ./internal/llm 'TestTranscribe_AModelTheEndpointDoesNotServeIsUnavailableHereNotAnOutage'

drill "a 200 with no choices is indistinguishable from silence" internal/llm/transcribe.go \
  's = s.replace("return &Transcript{Model: model, Unanswered: true}, nil", "return &Transcript{Model: model}, nil", 1)' \
  ./internal/llm 'TestTranscribe_AnAnswerWithNoTranscriptIsMarkedUnanswered'

echo
echo "Scripts run their kernel on one thread"
# Added 2026-09-14. Under the 1 GiB address-space cap a multi-threaded build hung
# on 4+ CPU machines. docs/bugfix/2026-09-14-scripts-hung-on-machines-with-four-or-more-cores.md
drill "a script's kernel starts a thread per core again" internal/domain/cad/script.go \
  's = s.replace("\"OMP_NUM_THREADS=1\", ", "", 1)' \
  ./internal/domain/cad 'TestScriptEnv_RunsTheKernelOnOneThread'
echo "Goal project permission"
# Added 2026-09-15 (goal project permission). POST /v1/goals never checked the
# project_id it was given: a stranger could draft a goal, and spend a planning call,
# in someone else's project, and a viewer could plan in one it only reads. Create now
# requires goal.create on a named project before Draft; replan already required it
# through the goal's row, and is fenced so it stays that way. Both drills need
# FORGE_TEST_DATABASE_URL. See
# docs/bugfix/2026-09-15-a-goal-could-be-drafted-into-a-project-its-caller-was-not-in.md.
drill "a goal is drafted into a project its caller is not in" internal/httpapi/goals_start.go \
  's = s.replace("\tif req.ProjectID != \"\" {\n\t\tif err := h.deps.requirePermission(", "\tif false {\n\t\tif err := h.deps.requirePermission(", 1)' \
  ./internal/httpapi 'TestCreateGoal_RefusesAProjectTheCallerIsNotAMemberOf|TestCreateGoal_RefusesAViewerOfTheProject'

drill "a viewer replans a goal it can only read" internal/httpapi/goals_start.go \
  's = s.replace("h.loadGoalFor(r, goalID, user.ID, access.PermGoalCreate)", "h.loadGoalFor(r, goalID, user.ID, access.PermProjectRead)", 1)' \
  ./internal/httpapi 'TestReplan_RefusesAStrangerAndAViewerOfTheGoalsProject'
echo "Script refusals without a kernel"
# Added 2026-09-14. CI's check job has Python and no build123d on purpose; a refusal
# must still say why there, and a test that needs the kernel must skip there.
# docs/bugfix/2026-09-14-script-refusals-needed-a-kernel-to-say-why.md
drill "the refusal hint imports the kernel again" internal/domain/cad/script.py \
  's = s.replace("ns, _ = namespace(importlib.util.find_spec(\"build123d\") is not None)", "ns, _ = namespace(True)", 1)' \
  ./internal/domain/cad 'TestScript_RefusalsSayWhyWithoutAKernel'

drill "a test that needs the kernel runs without it" internal/domain/cad/script_test.go \
  's = s.replace("\tif !hasBuild123d(py) {\n\t\tt.Skip(", "\tif false {\n\t\tt.Skip(", 1)' \
  ./internal/domain/cad 'TestScript_ATestThatNeedsTheKernelSkipsWithoutIt'

if [ "$MODE" = "list" ]; then
  exit 0
fi

echo
restore
if shasum -c "$BACKUP/before.sha" >/dev/null 2>&1; then
  echo "The tree is byte-identical to how the drill found it."
else
  echo "!! THE TREE WAS NOT RESTORED. These files differ from the backup in $BACKUP:"
  shasum -c "$BACKUP/before.sha" 2>&1 | grep -v ": OK"
  trap - EXIT INT TERM   # keep the backup: it is the only copy of the original
  exit 1
fi

if [ "$MODE" = "dry" ]; then
  echo "Dry run: $MOVED anchor(s) moved."
  [ "$MOVED" -eq 0 ] || exit 1
  exit 0
fi

echo "$RED went red, $GREEN stayed green, $UNPROVEN unproven, $MOVED anchor(s) moved."
echo "Covered: the sweep fences. NOT covered: the extrusion and revolve fences,"
echo "which have no drills — see the note in this script."
[ "$GREEN" -eq 0 ] && [ "$MOVED" -eq 0 ] || exit 1
exit 0
