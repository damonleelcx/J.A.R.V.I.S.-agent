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

FILES=(
  internal/domain/geometry/curve.go
  internal/domain/geometry/sweep.go
  internal/domain/geometry/mesh.go
  internal/domain/geometry/overlay.go
  internal/domain/geometry/solid.go
  internal/httpapi/assets/forge3d.js
  internal/domain/cad/sidecar.py
)

BACKUP=""
restore() {
  [ -n "$BACKUP" ] || return 0
  local f
  for f in "${FILES[@]}"; do
    [ -f "$BACKUP/$(basename "$f")" ] && cp "$BACKUP/$(basename "$f")" "$f"
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

  if cmp -s "$file" "$BACKUP/$(basename "$file")"; then
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
  for f in "${FILES[@]}"; do cp "$f" "$BACKUP/$(basename "$f")"; done
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
echo "The Go tessellator"
drill "the sweep case is gone from partTriangles" internal/domain/geometry/mesh.go \
  's = s.replace("\tcase \"sweep\":\n", "\tcase \"sweep-disabled\":\n", 1)' \
  ./internal/domain/geometry 'TestTessellate_ReachesTheOutlineShapes'

drill "corners are not mitred (the section stays perpendicular)" internal/domain/geometry/sweep.go \
  's = s.replace("\t\tbisector[j] = normalise(add3(tangent[j-1], tangent[j]))", "\t\tbisector[j] = tangent[j-1]", 1)' \
  ./internal/domain/geometry 'TestSwept_EnclosesAreaTimesPathLength'

drill "the section is re-framed each segment instead of carried" internal/domain/geometry/sweep.go \
  's = s.replace("\t\taxisX[i], axisY[i] = turn(axisX[i-1]), turn(axisY[i-1])", "\t\t_ = turn\n\t\taxisX[i], axisY[i] = initialSectionFrame(tangent[i])", 1)' \
  ./internal/domain/geometry 'TestSectionFrames_CarryTheSectionWithoutRollingIt'

drill "the section is ROLLED as it is carried" internal/domain/geometry/sweep.go \
  's = s.replace("\t\taxisX[i], axisY[i] = turn(axisX[i-1]), turn(axisY[i-1])", "\t\troll := smallestRotation(normalise(add3(tangent[i], scale3(axisX[i-1], 0.3))), tangent[i])\n\t\taxisX[i], axisY[i] = roll(turn(axisX[i-1])), roll(turn(axisY[i-1]))", 1)' \
  ./internal/domain/geometry 'TestSectionFrames_CarryTheSectionWithoutRollingIt|TestSwept_KeepsTheSectionSquareThroughABend'

drill "the fold check is gone" internal/domain/geometry/sweep.go \
  's = s.replace("\t\t\tif dot3(sub3(rings[i+1][k], rings[i][k]), tangent[i]) <= 1e-9 {", "\t\t\tif false {", 1)' \
  ./internal/domain/geometry 'TestSweptSections_RefusesThePathsThatAreNotSolids|TestProfileProblems_RefusesWhatASweepCannotBe'

drill "a reversal is no longer refused" internal/domain/geometry/sweep.go \
  's = s.replace("\t\tif length3(add3(tangent[i-1], tangent[i])) < 1e-9 {", "\t\tif false {", 1)' \
  ./internal/domain/geometry 'TestSweptSections_RefusesThePathsThatAreNotSolids'

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
