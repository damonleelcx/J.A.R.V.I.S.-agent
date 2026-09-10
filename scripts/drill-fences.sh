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
  internal/httpapi/assets/forge3d.js
  internal/domain/cad/sidecar.py
  internal/llm/deliberation.go
  internal/llm/stream.go
  internal/llm/openai_compatible.go
  internal/agent/scriptrepair.go
  internal/agent/converse.go
  internal/agent/converse_stream.go
  internal/agent/assemble.go
  internal/httpapi/converse.go
  internal/agent/look.go
  internal/domain/cad/script.py
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

drill "the vision check is not told what it cannot see" internal/agent/look.go \
  's = s.replace("\tif len(scripted) > 0 {", "\tif false {", 1)' \
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
echo "The kernel"
drill "the kernel uses OCCT's default transition" internal/domain/cad/sidecar.py \
  "s = s.replace('transition=Transition.RIGHT', 'transition=Transition.TRANSFORMED', 1)" \
  ./internal/domain/cad 'TestKernel_SweepsAnOutlineAlongAPath'

drill "the kernel ignores the section frame it was sent" internal/domain/cad/sidecar.py \
  "s = s.replace('x_dir=Vector(m[0], m[3], m[6]),\n                      z_dir=Vector(m[2], m[5], m[8]))', 'x_dir=Vector(1, 0, 0),\n                      z_dir=Vector(0, 0, 1))', 1)" \
  ./internal/domain/cad 'TestKernel_ASweptSolidIsTheOneTheRendererDrew'

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
