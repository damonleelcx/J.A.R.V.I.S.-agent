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
  internal/domain/cad/sidecar_process.go
  internal/llm/deliberation.go
  internal/llm/stream.go
  internal/llm/openai_compatible.go
  internal/agent/scriptrepair.go
  internal/agent/converse.go
  internal/agent/converse_stream.go
  internal/agent/assemble.go
  internal/agent/subtree.go
  internal/agent/georepair.go
  internal/agent/settledoc.go
  internal/agent/worker.go
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
  deploy/k8s/20-config.yaml
  deploy/k8s/32-worker-egress.yaml
  deploy/verify.sh
  cmd/forgectl/blob.go
  internal/domain/geometry/mesh.go
  internal/platform/errs/code.go
  internal/httpapi/assets/voice.js
  internal/httpapi/assets/workbench.js
  internal/httpapi/transcribe.go
  internal/httpapi/router.go
  internal/llm/transcribe.go
  internal/platform/config/config.go
  internal/domain/geometry/interference.go
  internal/agent/interference.go
  internal/domain/geometry/render.go
  internal/domain/geometry/assembly.go
  internal/domain/geometry/mass.go
  internal/agent/turned.go
  internal/domain/geometry/tree.go
  internal/domain/geometry/binding.go
  internal/domain/geometry/variant.go
  internal/domain/geometry/frame.go
  internal/domain/geometry/standard.go
  internal/domain/geometry/repetition.go
  internal/domain/geometry/pattern.go
  internal/domain/geometry/interface.go
  internal/domain/geometry/tree_features.go
  internal/domain/geometry/edit.go
  internal/agent/currentmodel.go
  internal/domain/geometry/limits.go
  internal/domain/geometry/repeat.go
  internal/domain/geometry/export.go
  internal/domain/geometry/service.go
  internal/platform/config/config.go
  internal/httpapi/assets/workbench.js
  internal/agent/car_tree_measure_test.go
  internal/agent/repetition.go
  internal/domain/geometry/compare_structure.go
  internal/httpapi/geometry.go
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
s = s.replace("clashes, clash_truncated, box_tests, clash_pairs = _interferences(built, ids, names, kept_placed)", "clashes, clash_truncated, box_tests, clash_pairs = _interferences(*_pre)", 1)' \
  ./internal/domain/cad 'TestKernel_ACutToolIsNotAnInterference'

# ‼️ Both anchors here moved when stage K2b unfolded the pair loop in _interferences
# (one indent less) and gave it a third return value, and neither drill said so:
# this one's FIRST replacement still applied, so the file changed and the script
# saw no moved anchor, while the second — the one that makes it a drill — matched
# nothing. Found by a whole-script dry run on 2026-09-15.
drill "the pair is reported in build order, not smaller first" internal/domain/cad/sidecar.py \
  "s = s.replace('        lo, hi = (i, j) if volumes[i] <= volumes[j] else (j, i)', '        lo, hi = i, j', 1)" \
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
echo "Features on repeated parts"
# Added 2026-09-13. The kernel was sent solids from the EXPANDED document and
# operations from the AUTHORED one, so a fuse naming "spoke" reached a sidecar
# holding "spoke-1" … and was dropped from every export. Scripts on repeated
# parts were looked up by an id the authored document never had.
# docs/bugfix/2026-09-13-features-on-repeated-parts-were-never-applied.md
drill "the kernel reads the operations from the authored document again" internal/domain/cad/cad.go \
  's = s.replace("\tsolids, operations, featureProblems, inferred := geometry.SolidsAndOperations(doc, unit)\n", "\tsolids, _, _, inferred := geometry.SolidsAndOperations(doc, unit)\n\toperations, featureProblems := doc.Operations()\n", 1)' \
  ./internal/domain/cad 'TestKernel_AFeatureNamingARepeatedPartIsApplied'

drill "the operations are read from the document before it is expanded" internal/domain/geometry/solid.go \
  's = s.replace("func SolidsAndOperations(d Document, unit Unit) ([]Solid, []Operation, []Problem, []string) {\n", "func SolidsAndOperations(d Document, unit Unit) ([]Solid, []Operation, []Problem, []string) {\n\tauthored := d\n", 1)
s = s.replace("\toperations, featureProblems := d.Operations()\n", "\toperations, featureProblems := authored.Operations()\n", 1)' \
  ./internal/domain/geometry 'TestRepeat_TheKernelIsSentFeaturesNamingTheCopiesItIsSent'

drill "a copy of a scripted part reaches the kernel without its script" internal/domain/geometry/solid.go \
  's = s.replace("\t\t\tscript = p.Script\n", "", 1)' \
  ./internal/domain/geometry 'TestRepeat_EveryCopyOfAScriptedPartCarriesItsScript'

echo
echo "Feature radii are lengths"
# Added 2026-09-13. A fillet or chamfer radius was sent in the document's units
# while every other length was converted to mm, so a cm model's 1 cm fillet was
# built as 1 mm. docs/bugfix/2026-09-13-feature-radii-were-sent-in-the-documents-units.md
drill "a feature radius is sent in the document's units again" internal/domain/geometry/solid.go \
  's = s.replace("\t\toperations[i].Radius *= toMM\n", "", 1)' \
  ./internal/domain/geometry 'TestSolids_ConvertsAFeatureRadiusToMillimetres'

drill "the kernel builds a cm fillet ten times too small again" internal/domain/geometry/solid.go \
  's = s.replace("\t\toperations[i].Radius *= toMM\n", "", 1)' \
  ./internal/domain/cad 'TestKernel_AFilletIsTheSameSizeInEveryUnit'

drill "the kernel quotes radii without a unit" internal/domain/cad/sidecar.py \
  "s = s.replace('cannot take a %s of %g mm here. The largest that DOES build on these \"\n            \"edges is %g mm,', 'cannot take a %s of %g here. The largest that DOES build on these \"\n            \"edges is %g,', 1)" \
  ./internal/domain/cad 'TestKernel_ARadiusThatDoesNotFitIsReportedInMillimetres'

echo
echo "Every reader sees repeat copies"
# Added 2026-09-13. Only the kernel, the mesh and Faults expanded a repeat; every
# other reader read the parts as written: Measure left copies out, the contact
# sheet drew them grey, states could not name one, the resize check reported them
# without a name, and the browser drew each pattern once.
# docs/bugfix/2026-09-13-repeat-copies-were-invisible-to-most-readers.md
drill "Measure reads the parts as written again" internal/domain/geometry/overlay.go \
  's = s.replace("\twithCopies, _ := expandRepeats(tree)\n", "\twithCopies := tree\n", 1)' \
  ./internal/domain/geometry 'TestMeasure_IncludesEveryCopyOfARepeatedPart'

drill "copies are coloured from the authored list again" internal/domain/geometry/render.go \
  's = s.replace("\t\tfor _, q := range (Document{Parts: []Part{p}}).Expanded().Parts {\n", "\t\tfor _, q := range []Part{p} {\n", 1)' \
  ./internal/domain/geometry 'TestContactSheet_ColoursACopyLikeItsPart'

drill "a state cannot name a copy again" internal/domain/geometry/assembly.go \
  's = s.replace("\tfor _, p := range (Document{Parts: parts}).Expanded().Parts {\n", "\tfor _, p := range parts {\n", 1)' \
  ./internal/domain/geometry 'TestValidateStates_NamesACopyOrThePattern'

drill "a resized copy is reported without its name again" internal/agent/turned.go \
  's = s.replace("\tfor _, p := range after.Expanded().Parts {\n", "\tfor _, p := range after.Parts {\n", 1)' \
  ./internal/agent 'TestTurned_NamesACopyOfARepeatedPart'

drill "the browser draws each repeated part once again" internal/httpapi/assets/forge3d.js \
  "s = s.replace('      var r = p.repeat;\n', '      var r = null;\n', 1)" \
  ./internal/httpapi 'TestRendererExpandsARepeatLikeTheExporter'

drill "the browser turns a copy differently from the exporter" internal/httpapi/assets/forge3d.js \
  "s = s.replace('var a = repeatSweep(r) * k * 180 / Math.PI;', 'var a = repeatSweep(r) * k;', 1)" \
  ./internal/httpapi 'TestRendererExpandsARepeatLikeTheExporter'

echo
echo "A kernel mesh is already placed"
# Added 2026-09-13. The sidecar tessellates each solid after placing it; the browser
# placed and turned the mesh a second time, so every kernel-built part away from the
# origin was drawn somewhere else. docs/bugfix/2026-09-13-kernel-built-parts-were-placed-twice.md
# Re-anchored 2026-09-15 (Phase 6, stage W1): the draw places through drawBatches.
drill "the browser places a kernel mesh a second time" internal/httpapi/assets/forge3d.js \
  "s = s.replace('        matrix = inst ? Array.prototype.slice.call(inst.matrix) : IDENTITY.slice();\n', '        matrix = inst ? Array.prototype.slice.call(inst.matrix) : placementMatrix(part);\n', 1)" \
  ./internal/httpapi 'TestRendererDoesNotPlaceAKernelMeshTwice'

echo
echo "Designs placed inside assemblies"
# Added 2026-09-14 with the tree (Phase 1, stage D1b). Each guards one way a tree
# could be read as something other than the parts it places.
drill "Faults does not read the tree" internal/domain/geometry/faults.go \
  's = s.replace("\ttree, treeProblems := expandAssemblies(*d)\n", "\ttree, treeProblems := *d, []Problem(nil)\n", 1)' \
  ./internal/domain/geometry 'TestTree_ABrokenTreeIsAFaultAndTheFileSaysSo'

drill "a clone shares the tree with its original" internal/domain/geometry/binding.go \
  's = s.replace("\tout.Definitions = clonePartList(d.Definitions)\n", "\tout.Definitions = d.Definitions\n", 1)' \
  ./internal/domain/geometry 'TestTree_ACloneSharesNothingWithTheOriginal'

drill "a placement ignores the definition's own frame" internal/domain/geometry/tree.go \
  's = s.replace("\t\t\t\t\tq.Position, q.Rotation, q.Mirrored = childFrame.then(placementOf(lp.Position, lp.Rotation, lp.Mirrored)).stored()\n", "\t\t\t\t\tq.Position, q.Rotation, q.Mirrored = childFrame.stored()\n", 1)' \
  ./internal/domain/geometry 'TestTree_APartIsPlacedThroughEveryFrameAboveIt'

drill "the storage door reads only top-level parts" internal/domain/geometry/variant.go \
  's = s.replace("\tplaced := n.Document.PlacedParts()\n", "\tplaced := n.Document.Parts\n", 1)' \
  ./internal/domain/geometry 'TestTree_TheStorageDoorReadsThePlacedParts|TestTree_TheStorageDoorChecksThePartsATreePlaces'

drill "Bind skips definitions" internal/domain/geometry/binding.go \
  's = s.replace("\tif len(d.Definitions) > 0 {\n", "\tif false {\n", 1)' \
  ./internal/domain/geometry 'TestTree_BindEvaluatesADefinitionsSizesForEveryPlacement'

echo
echo "The browser flattens a tree like the exporter"
# Added 2026-09-14 (Phase 1, stage D1b). forge3d.js holds a copy of tree.go and frame.go.
drill "the browser places a definition ignoring its own frame" internal/httpapi/assets/forge3d.js \
  "s = s.replace('            var st = storedPlacement(thenPlacement(childFrame, placementOf(lp.position, lp.rotation, !!lp.mirrored)));\n', '            var st = storedPlacement(childFrame);\n', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

drill "the browser reads a rotation back with the wrong sign" internal/httpapi/assets/forge3d.js \
  "s = s.replace('      x = Math.atan2(-m[5], m[8]);\n', '      x = Math.atan2(m[5], m[8]);\n', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

echo
echo "Mirror is a reflection"
# Added 2026-09-14 (Phase 1, stage D1c). A reflection cannot be a rotation, so a
# placed part stores one flag, and every reader has to honour it.
drill "the mesh does not reflect a mirrored part" internal/domain/geometry/mesh.go \
  's = s.replace("\t\tif p.Mirrored {\n\t\t\tt = mirrorTriangle(t)\n\t\t}\n", "", 1)' \
  ./internal/domain/geometry 'TestMirror_TheMeshOfAMirroredPartIsItsReflectionFacingOut'

drill "a mirrored mesh keeps its winding and faces in" internal/domain/geometry/mesh.go \
  's = s.replace("return Triangle{A: flip(t.A), B: flip(t.C), C: flip(t.B), Normal: flip(t.Normal)}", "return Triangle{A: flip(t.A), B: flip(t.B), C: flip(t.C), Normal: flip(t.Normal)}", 1)' \
  ./internal/domain/geometry 'TestMirror_TheMeshOfAMirroredPartIsItsReflectionFacingOut'

drill "the stored form ignores a reflection" internal/domain/geometry/frame.go \
  's = s.replace("\tif det3(m) < 0 {\n", "\tif false {\n", 1)' \
  ./internal/domain/geometry 'TestPlacement_TheStoredFormRebuildsTheSamePlacement'

drill "the tree ignores a child's mirror" internal/domain/geometry/tree.go \
  's = s.replace("\t\t\tlocal.m = mulMat3(local.m, reflect)\n", "\t\t\t_ = reflect\n", 1)' \
  ./internal/domain/geometry 'TestMirror_AChildMirroredAcrossYIsReflectedNotTurned'

drill "measurement ignores mirror" internal/domain/geometry/overlay.go \
  's = s.replace("\t\tif p.Mirrored {\n\t\t\t// A reflected part reaches", "\t\tif false {\n\t\t\t// A reflected part reaches", 1)' \
  ./internal/domain/geometry 'TestMirror_MeasurementFlipsAnAsymmetricExtent'

drill "the kernel is not told to mirror" internal/domain/geometry/solid.go \
  's = s.replace("Script: script, Mirrored: p.Mirrored,", "Script: script,", 1)' \
  ./internal/domain/geometry 'TestMirror_TheKernelIsToldToMirror'

drill "the sidecar never mirrors" internal/domain/cad/sidecar.py \
  "s = s.replace('        if s.get(\"mirrored\"):\n', '        if False:\n', 1)" \
  ./internal/domain/cad 'TestKernel_MirrorsAPartBeforePlacingIt'

drill "the browser does not reflect a mirrored primitive" internal/httpapi/assets/forge3d.js \
  "s = s.replace('    if (s.mirrored) sc = [-sc[0], sc[1], sc[2]];\n', '', 1)" \
  ./internal/httpapi 'TestRendererPlacesAMirroredPartLikeTheExporter'

drill "the browser tree ignores a child's mirror" internal/httpapi/assets/forge3d.js \
  "s = s.replace('        local.m = mulMat3(local.m, reflect);\n        var sub = asms[c.ref], def = defs[c.ref];\n', '        var sub = asms[c.ref], def = defs[c.ref];\n', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

drill "the browser stores no reflection" internal/httpapi/assets/forge3d.js \
  "s = s.replace('    if (det3(m) < 0) {\n', '    if (false) {\n', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

echo
echo "Patterns on a placed child"
# Added 2026-09-14 (Phase 1, stage D1c-2). A pattern is a transform per copy in the
# PARENT's frame; each drill breaks one term of that, in Go and in the browser's copy.
drill "a linear pattern forgets which copy it is placing" internal/domain/geometry/pattern.go \
  's = s.replace("k := float64(n - 1)", "k := float64(n)", 1)' \
  ./internal/domain/geometry 'TestPattern_Linear'

drill "a polar pattern starts one step round" internal/domain/geometry/pattern.go \
  's = s.replace("axisRotation(p.About, between*float64(n-1))", "axisRotation(p.About, between*float64(n))", 1)' \
  ./internal/domain/geometry 'TestPattern_PolarFullAndPartial'

drill "a grid is numbered column by column" internal/domain/geometry/pattern.go \
  's = s.replace("r, c := float64(n/p.Columns), float64(n%p.Columns)", "r, c := float64(n%p.Columns), float64(n/p.Columns)", 1)' \
  ./internal/domain/geometry 'TestPattern_GridIsNumberedRowByRow'

drill "a copy on a path corner follows the incoming segment" internal/domain/geometry/pattern.go \
  's = s.replace("if s < sg.start+sg.length {", "if s <= sg.start+sg.length {", 1)' \
  ./internal/domain/geometry 'TestPattern_PathSpacingAndAlignment'

drill "the alignment turns about the wrong axis" internal/domain/geometry/pattern.go \
  's = s.replace("axis := [3]float64{0, -d[2], d[1]}", "axis := [3]float64{0, d[2], -d[1]}", 1)' \
  ./internal/domain/geometry 'TestPattern_AlignmentIsTheSmallestTurn'

drill "a path corner radius is silently ignored" internal/domain/geometry/pattern.go \
  's = s.replace("case q.Radius != 0 || q.RadiusFrom != \"\":", "case false:", 1)' \
  ./internal/domain/geometry 'TestPattern_RefusesWhatItCannotPlace'

drill "the pattern moves the child inside its own frame" internal/domain/geometry/tree.go \
  's = s.replace("childFrame := frame.then(reference.then(slot.at.then(local)))", "childFrame := frame.then(reference.then(local.then(slot.at)))", 1)' \
  ./internal/domain/geometry 'TestPattern_Linear'

drill "the storage door accepts an id placed twice" internal/domain/geometry/variant.go \
  's = s.replace("if seen[p.ID] {\n\t\t\t// Comparison matches", "if false {\n\t\t\t// Comparison matches", 1)' \
  ./internal/domain/geometry 'TestPattern_ACopyThatTakesASiblingsIdIsRefusedAtTheStorageDoor|TestTree_TheStorageDoorReadsThePlacedParts|TestNewVariant_AnIdPlacedTwiceIsRefusedHoweverItWasMade'

# Added 2026-09-14. The duplicate check read the placed parts, which expand the tree
# but not a top-level repeat, so a repeat copy could take another part's id.
# docs/bugfix/2026-09-14-a-repeat-copy-could-take-another-parts-id.md
drill "the storage door checks ids before repeats are written out" internal/domain/geometry/variant.go \
  's = s.replace("\tfor _, p := range n.Document.Expanded().Parts {\n\t\tif seen[p.ID] {", "\tfor _, p := range placed {\n\t\tif seen[p.ID] {", 1)' \
  ./internal/domain/geometry 'TestNewVariant_AnIdPlacedTwiceIsRefusedHoweverItWasMade'

drill "a clone shares its pattern with the original" internal/domain/geometry/binding.go \
  's = s.replace("c.Pattern = &p", "_ = p", 1)' \
  ./internal/domain/geometry 'TestPattern_ACloneDoesNotShareAPattern'

drill "the kernel is sent one copy of a patterned child" internal/domain/geometry/tree.go \
  's = s.replace("slots, patternProblem := c.Pattern.copies()", "slots, patternProblem := (*Pattern)(nil).copies()", 1)' \
  ./internal/domain/cad 'TestKernel_BuildsAGridAndACircleOfCopies'

drill "the browser forgets which linear copy it is placing" internal/httpapi/assets/forge3d.js \
  "s = s.replace('step[0] * (n - 1), step[1] * (n - 1), step[2] * (n - 1)', 'step[0] * n, step[1] * n, step[2] * n', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

drill "the browser polar pattern ignores a partial sweep" internal/httpapi/assets/forge3d.js \
  "s = s.replace('repeatSweep({ count: count, angle: p.angle })', 'repeatSweep({ count: count })', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

drill "the browser numbers a grid column by column" internal/httpapi/assets/forge3d.js \
  "s = s.replace('var r = Math.floor(n / columns), cc = n % columns;', 'var r = n % columns, cc = Math.floor(n / columns);', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

drill "the browser never aligns copies to a path" internal/httpapi/assets/forge3d.js \
  "s = s.replace('if (p.align) at.m = rotationTaking(st.direction);', 'if (false) at.m = rotationTaking(st.direction);', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

drill "the browser turns a doubled-back path about y" internal/httpapi/assets/forge3d.js \
  "s = s.replace(': [-1, 0, 0, 0, -1, 0, 0, 0, 1];', ': [-1, 0, 0, 0, 1, 0, 0, 0, -1];', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

drill "the browser draws a path with a corner radius" internal/httpapi/assets/forge3d.js \
  "s = s.replace('if (q.radius || q.radius_from', 'if (q.radius_from', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

drill "the browser moves the child inside its own frame" internal/httpapi/assets/forge3d.js \
  "s = s.replace('thenPlacement(reference, thenPlacement(slot.at, local)))', 'thenPlacement(reference, thenPlacement(local, slot.at)))', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

echo
echo "Interfaces"
# Added 2026-09-14 (Phase 1, stage D1d). A child attached `at` an interface is
# measured in that interface's frame; each drill breaks one term of the resolution
# in Go and in the browser's copy of it.
drill "a child ignores the interface it is attached at" internal/domain/geometry/tree.go \
  's = s.replace("childFrame := frame.then(reference.then(slot.at.then(local)))", "childFrame := frame.then(slot.at.then(local)); _ = reference", 1)' \
  ./internal/domain/geometry 'TestInterface_AChildSitsOnItsParentsInterface'

drill "a pattern at an interface turns about the parent's axis" internal/domain/geometry/tree.go \
  's = s.replace("childFrame := frame.then(reference.then(slot.at.then(local)))", "childFrame := frame.then(slot.at.then(reference.then(local)))", 1)' \
  ./internal/domain/geometry 'TestInterface_APatternIsMeasuredInTheInterfacesFrame'

drill "a sibling's interface ignores where the sibling is placed" internal/domain/geometry/interface.go \
  's = s.replace("\t\t\treturn ref.then(slot.at.then(local)), sub, \"\"\n", "\t\t\t_ = local\n\t\t\treturn ref.then(slot.at), sub, \"\"\n", 1)' \
  ./internal/domain/geometry 'TestInterface_APartMatesToASiblingsInterfaceThroughAMirror'

drill "a sibling's own attachment is ignored" internal/domain/geometry/interface.go \
  's = s.replace("ref, problem := r.reference(a, c, whole)", "ref, problem := placementOf(nil, nil, false), \"\"", 1)' \
  ./internal/domain/geometry 'TestInterface_AChainOfAttachmentsTwoLevelsDown'

drill "a patterned sibling named without a copy is not refused" internal/domain/geometry/interface.go \
  's = s.replace("if c.ID == seg && len(slots) > 1 {", "if false {", 1)' \
  ./internal/domain/geometry 'TestInterface_RefusesWhatItCannotAttach'

drill "interfaces are not checked where they are declared" internal/domain/geometry/tree.go \
  's = s.replace("for _, detail := range interfaceProblems(a) {", "for _, detail := range interfaceProblems(Assembly{}) {", 1)' \
  ./internal/domain/geometry 'TestInterface_RefusesWhatItCannotAttach'

drill "a clone shares its interfaces with the original" internal/domain/geometry/binding.go \
  's = s.replace("f.Position = append([]float64(nil), f.Position...)", "f.Position = f.Position", 1)' \
  ./internal/domain/geometry 'TestInterface_ACloneDoesNotShareAnInterface'

drill "the kernel is sent an attached child without its interface" internal/domain/geometry/tree.go \
  's = s.replace("childFrame := frame.then(reference.then(slot.at.then(local)))", "childFrame := frame.then(slot.at.then(local)); _ = reference", 1)' \
  ./internal/domain/cad 'TestKernel_MovingAnInterfaceMovesWhatIsAttachedToIt'

drill "the browser ignores the interface a child is attached at" internal/httpapi/assets/forge3d.js \
  "s = s.replace('thenPlacement(frame, thenPlacement(reference, thenPlacement(slot.at, local)))', 'thenPlacement(frame, thenPlacement(slot.at, local))', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

drill "the browser turns a pattern at an interface about the parent's axis" internal/httpapi/assets/forge3d.js \
  "s = s.replace('thenPlacement(frame, thenPlacement(reference, thenPlacement(slot.at, local)))', 'thenPlacement(frame, thenPlacement(slot.at, thenPlacement(reference, local)))', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

drill "the browser ignores where a sibling is placed" internal/httpapi/assets/forge3d.js \
  "s = s.replace('return { frame: thenPlacement(ref, thenPlacement(slots[s].at, local)), sub: sub };', 'return { frame: thenPlacement(ref, slots[s].at), sub: sub };', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

drill "the browser ignores a sibling's own attachment" internal/httpapi/assets/forge3d.js \
  "s = s.replace('          var ref = reference(a, c);\n', '          var ref = placementOf(null, null, false);\n', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

echo
echo "Features declared on an assembly"
# Added 2026-09-14 (Phase 1, stage D1e). An assembly's features are written out per
# occurrence, naming the parts its placements wrote out; each drill breaks one term.
drill "an assembly's features are ignored" internal/domain/geometry/tree.go \
  's = s.replace("occurrenceFeatures(a, path, index, out.Parts, fail)", "occurrenceFeatures(Assembly{}, path, index, out.Parts, fail)", 1)' \
  ./internal/domain/geometry 'TestTreeFeature_AWeldDeclaredOnceIsAppliedInEveryOccurrence'

drill "an outer assembly's features are written before its children's" internal/domain/geometry/tree.go \
  's = s.replace("out.Features = append(out.Features, occurrenceFeatures(a, path, index, out.Parts, fail)...)", "out.Features = append(occurrenceFeatures(a, path, index, out.Parts, fail), out.Features...)", 1)' \
  ./internal/domain/geometry 'TestTreeFeature_InnerFeaturesComeBeforeOuterOnes'

drill "a whole pattern names only its first copy" internal/domain/geometry/tree.go \
  's = s.replace("index[c.ID] = partRange{childStart, len(out.Parts)}", "index[c.ID] = partRange{childStart, childStart + 1}", 1)' \
  ./internal/domain/geometry 'TestTreeFeature_APathNamesAPlacementAtAnyDepth'

drill "a path into a sub-assembly names nothing" internal/domain/geometry/tree.go \
  's = s.replace("index[c.ID+slot.suffix+PathSeparator+rel] = r", "_, _ = rel, r", 1)' \
  ./internal/domain/geometry 'TestTreeFeature_APathNamesAPlacementAtAnyDepth'

drill "a definition's repeat copy cannot be named" internal/domain/geometry/tree.go \
  's = s.replace("index[c.ID+slot.suffix+suffix] = partRange{partStart, len(out.Parts)}", "_ = partStart", 1)' \
  ./internal/domain/geometry 'TestTreeFeature_APathNamesAPlacementAtAnyDepth'

drill "expansion appends to the caller's features" internal/domain/geometry/tree.go \
  's = s.replace("out.Features = append([]Feature(nil), d.Features...)", "out.Features = d.Features", 1)' \
  ./internal/domain/geometry 'TestTreeFeature_ExpansionDoesNotWriteIntoTheCallersFeatures'

drill "a feature on a group applies to its last part" internal/domain/geometry/tree_features.go \
  's = s.replace("q.Of = of[0]", "q.Of = of[len(of)-1]", 1)' \
  ./internal/domain/geometry 'TestTreeFeature_APathNamesAPlacementAtAnyDepth'

drill "a tree feature keeps its bare id in every occurrence" internal/domain/geometry/tree_features.go \
  's = s.replace("name = prefix + PathSeparator + id", "name = id", 1)' \
  ./internal/domain/geometry 'TestTreeFeature_AWeldDeclaredOnceIsAppliedInEveryOccurrence'

drill "a clone shares an assembly's features" internal/domain/geometry/binding.go \
  's = s.replace("f.With = append([]string(nil), f.With...)", "f.With = f.With", 1)' \
  ./internal/domain/geometry 'TestTreeFeature_ACloneDoesNotShareAnAssemblysFeatures'

drill "the kernel never sees an assembly's weld" internal/domain/geometry/tree.go \
  's = s.replace("occurrenceFeatures(a, path, index, out.Parts, fail)", "occurrenceFeatures(Assembly{}, path, index, out.Parts, fail)", 1)' \
  ./internal/domain/cad 'TestKernel_AWheelWeldedAsASubAssemblyBuildsLikeTheFlatOne'

drill "the browser ignores an assembly's features" internal/httpapi/assets/forge3d.js \
  "s = s.replace('      occurrenceFeatures(a, path, index);\n', '', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

drill "the browser cuts with one copy of a whole pattern" internal/httpapi/assets/forge3d.js \
  "s = s.replace('if (slots.length > 1) index[cid] = [childStart, parts.length];', 'if (slots.length > 1) index[cid] = [childStart, childStart + 1];', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

drill "the browser cannot name a path into a child" internal/httpapi/assets/forge3d.js \
  "s = s.replace('for (var rel in subIndex) index[cid + slot.suffix + PATH_SEPARATOR + rel] = subIndex[rel];', '', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

drill "the browser keeps a feature whose tool names nothing" internal/httpapi/assets/forge3d.js \
  "s = s.replace('if (!got.length) refused = true;', '', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

echo
echo "The contract and the agent read trees"
# Added 2026-09-14 (Phase 1, stage D1f). An edit changes a tree through its design,
# the agent's readers see a tree as a model, and the contract's example is a tree
# FORGE builds.
drill "an edit cannot patch a definition" internal/domain/geometry/edit.go \
  's = s.replace("out.Definitions = upsertPart(out.Definitions, in)", "_ = in", 1)' \
  ./internal/domain/geometry 'TestEdit_ADefinitionPatchedByIDChangesEveryPlacement'

drill "an edit cannot patch an assembly" internal/domain/geometry/edit.go \
  's = s.replace("out.Assemblies = upsertAssembly(out.Assemblies, in)", "_ = in", 1)' \
  ./internal/domain/geometry 'TestEdit_AnAssemblyPatchedByIDIsReplacedWhole'

drill "removing a child rewrites the base's children" internal/domain/geometry/edit.go \
  's = s.replace("kept := make([]Child, 0, len(list[i].Children))", "kept := list[i].Children[:0]", 1)' \
  ./internal/domain/geometry 'TestEdit_RemovesOneChildWithoutTouchingTheBase'

drill "a tree change counts as an empty edit" internal/domain/geometry/edit.go \
  's = s.replace("len(e.Remove.Definitions) == 0 && len(e.Remove.Assemblies) == 0 && len(e.Remove.Children) == 0 &&", "true &&", 1)' \
  ./internal/domain/geometry 'TestEdit_ATreeChangeIsNotAnEmptyEdit'

drill "Spans ignore definitions" internal/domain/geometry/binding.go \
  's = s.replace("range [][]Part{d.Parts, d.Definitions}", "range [][]Part{d.Parts}", 1)' \
  ./internal/domain/geometry 'TestSpans_MeasuresDefinitionsInTheirOwnFrame'

drill "Spans group a definition with a top-level part" internal/domain/geometry/binding.go \
  's = s.replace("string(rune(\x270\x27+frame))", "string(rune(\x270\x27+frame*0))", 1)' \
  ./internal/domain/geometry 'TestSpans_MeasuresDefinitionsInTheirOwnFrame'

drill "settling drops a design written as a tree" internal/agent/settledoc.go \
  's = s.replace("\tif !d.HasGeometry() {\n", "\tif len(d.Parts) == 0 {\n", 1)' \
  ./internal/agent 'TestSettle_KeepsADesignWrittenAsATree'

drill "the current model hides the tree" internal/agent/currentmodel.go \
  's = s.replace("\t\tDefinitions: d.Definitions, Assemblies: d.Assemblies, Root: d.Root,\n", "", 1)' \
  ./internal/agent 'TestCurrentModel_ShowsTheTreeItIsRevising'

drill "an edit to a tree on screen is refused" internal/agent/converse.go \
  's = s.replace("\tif current == nil || !current.HasGeometry() {\n\t\treturn errs.New(op", "\tif current == nil || len(current.Parts) == 0 {\n\t\treturn errs.New(op", 1)' \
  ./internal/agent 'TestResolveEdit_EditsATreeOnScreenThroughItsDesign'

drill "the contract's tree example drifts from the schema" internal/agent/converse.go \
  's = s.replace("\"at\": \"left-end\"", "\"attach\": \"left-end\"", 1)' \
  ./internal/agent 'TestTheContractOffersATreeAndItsExampleBuilds'

drill "the contract's remove names a field an edit does not have" internal/agent/converse.go \
  's = s.replace("\"children\": [\"assembly-id/child-id\"]}", "\"kids\": [\"assembly-id/child-id\"]}", 1)' \
  ./internal/agent 'TestTheContractNamesTheTreeEditFieldsAnEditHas'

echo
echo "What one stored design may be"
# Added 2026-09-14 (Phase 3, stage S0). Storage is bounded by bytes, definitions and
# occurrences counted before expanding; drawing and building are bounded at 4096.
drill "the tree expands past the occurrence bound" internal/domain/geometry/tree.go \
  's = s.replace("\tif p := occurrenceProblem(d); p != nil {\n\t\tproblems = append(problems, *p)", "\tif p := occurrenceProblem(Document{}); p != nil {\n\t\tproblems = append(problems, *p)", 1)' \
  ./internal/domain/geometry 'TestLimits_TheOccurrenceBoundHoldsEverywhere'

drill "a flat document's repeats expand past the occurrence bound" internal/domain/geometry/repeat.go \
  's = s.replace("\tif p := occurrenceProblem(d); p != nil {\n\t\tout := d", "\tif p := occurrenceProblem(Document{}); p != nil {\n\t\tout := d", 1)' \
  ./internal/domain/geometry 'TestLimits_TheOccurrenceBoundHoldsEverywhere'

drill "the storage door does not count occurrences" internal/domain/geometry/variant.go \
  's = s.replace("if p := occurrenceProblem(n.Document); p != nil {", "if p := occurrenceProblem(Document{}); p != nil {", 1)' \
  ./internal/domain/geometry 'TestLimits_TheOccurrenceBoundHoldsEverywhere'

drill "the count forgets a pattern's copies" internal/domain/geometry/limits.go \
  's = s.replace("n = sat(n + mul(len(slots), count(c.Ref)))", "n = sat(n + count(c.Ref))", 1)' \
  ./internal/domain/geometry 'TestOccurrences_CountsWithoutPlacing'

drill "the count does not saturate" internal/domain/geometry/limits.go \
  's = s.replace("\t\tif n > limit {\n\t\t\treturn limit + 1\n\t\t}\n\t\treturn n\n", "\t\treturn n\n", 1)' \
  ./internal/domain/geometry 'TestOccurrences_CountsWithoutPlacing'

drill "a zero setting switches a bound off" internal/domain/geometry/limits.go \
  's = s.replace("\tif l.MaxDocumentBytes <= 0 {\n", "\tif false {\n", 1)' \
  ./internal/domain/geometry 'TestLimits_ANonPositiveSettingKeepsItsDefault'

drill "a design too large to draw is meshed anyway" internal/domain/geometry/mesh.go \
  's = s.replace("\tif refusal := doc.DrawRefusal(); refusal != \"\" {\n\t\treturn &Mesh{", "\tif refusal := doc.DrawRefusal(); false {\n\t\treturn &Mesh{", 1)' \
  ./internal/domain/geometry 'TestLimits_ThirtyThousandOccurrencesAreStoredButNotDrawn'

drill "the kernel request carries a design too large to build" internal/domain/geometry/solid.go \
  's = s.replace("\tif refusal := d.DrawRefusal(); refusal != \"\" {\n\t\treturn nil, nil, nil, []string{refusal}", "\tif refusal := d.DrawRefusal(); false {\n\t\treturn nil, nil, nil, []string{refusal}", 1)' \
  ./internal/domain/geometry 'TestLimits_ThirtyThousandOccurrencesAreStoredButNotDrawn'

drill "a mesh file of a design too large to draw is written" internal/domain/geometry/export.go \
  's = s.replace("\tif refusal := v.Document.DrawRefusal(); refusal != \"\" {\n", "\tif refusal := v.Document.DrawRefusal(); false {\n", 1)' \
  ./internal/domain/geometry 'TestLimits_ThirtyThousandOccurrencesAreStoredButNotDrawn'

drill "the kernel is sent a design too large to build" internal/domain/cad/cad.go \
  's = s.replace("\tif refusal := doc.DrawRefusal(); refusal != \"\" {\n", "\tif refusal := doc.DrawRefusal(); false {\n", 1)' \
  ./internal/domain/cad 'TestKernel_ADesignTooLargeToBuildIsRefusedAndSaysWhy'

drill "the storage door stores a design over the byte ceiling" internal/domain/geometry/service.go \
  's = s.replace("\tif int64(len(body)) > lim.MaxDocumentBytes {\n", "\tif false {\n", 1)' \
  ./internal/domain/geometry 'TestSizeFence_RefusesADesignOverTheByteCeiling'

drill "the storage door stores more definitions than the ceiling" internal/domain/geometry/service.go \
  's = s.replace("\tif len(doc.Definitions) > lim.MaxDefinitions {\n", "\tif false {\n", 1)' \
  ./internal/domain/geometry 'TestSizeFence_RefusesMoreDefinitionsThanTheCeiling'

drill "the saved record does not say how big the design is" internal/domain/geometry/service.go \
  's = s.replace("\t\t\"bytes\", len(body), \"definitions\", len(v.Document.Definitions),\n", "\t\t\"definitions\", len(v.Document.Definitions),\n", 1)' \
  ./internal/domain/geometry 'TestSizeFence_AThirtyThousandOccurrenceCarIsStoredAndItsSizeRecorded'

drill "configuration accepts a zero occurrence bound" internal/platform/config/config.go \
  's = s.replace("\tif cfg.Geometry.MaxOccurrences <= 0 {\n", "\tif false {\n", 1)' \
  ./internal/platform/config 'TestGeometryLimitsMustBePositive'

drill "the browser draws a design too large to draw" internal/httpapi/assets/forge3d.js \
  "s = s.replace('    if (drawRefusal(spec)) return [];\n', '', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

drill "the browser's count forgets a pattern's copies" internal/httpapi/assets/forge3d.js \
  "s = s.replace('n = sat(n + sat((slots ? slots.length : 0) * count(c.ref)));', 'n = sat(n + count(c.ref));', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

drill "the browser refuses in other words than the exporter" internal/httpapi/assets/forge3d.js \
  "s = s.replace(\"'nothing was drawn.'\", \"'nothing was shown.'\", 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

echo
echo "A tree part is named by its occurrence path"
# Added 2026-09-14. A part a tree places is named by every child above it, then its
# own name (docs/bugfix/2026-09-14-tree-copies-shared-display-names.md).
drill "a tree part is named by its own label only" internal/domain/geometry/tree.go \
  "s = s.replace('q.Name = strings.Join(append(append([]string(nil), childNames...), lp.Label()), NameSeparator)', 'q.Name = lp.Label()', 1)" \
  ./internal/domain/geometry 'TestTree_EveryPartItPlacesIsNamedByItsOccurrence'

drill "an unnamed child adds nothing to the name" internal/domain/geometry/tree.go \
  "s = s.replace('\t\t\t\t\tchildLabel = c.ID\n', '', 1)" \
  ./internal/domain/geometry 'TestTree_EveryPartItPlacesIsNamedByItsOccurrence'

drill "the browser names a tree part by its own label only" internal/httpapi/assets/forge3d.js \
  "s = s.replace('q.name = childNames.concat([lp.name || lp.id]).join(NAME_SEPARATOR);', 'q.name = lp.name || lp.id;', 1)" \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'
echo "Each distinct shape is built once"
# Added 2026-09-14 (Phase 4, stage K1). The sidecar builds each distinct shape once per
# request and places located copies; Go runs each distinct script once.
drill "every occurrence builds its own shape again" internal/domain/cad/sidecar.py \
  "s = s.replace('        if key in built_once:\n', '        if False:\n', 1)" \
  ./internal/domain/cad 'TestKernel_OneDefinitionPlacedManyTimesIsBuiltOnce'

drill "a mirrored copy shares the unmirrored build" internal/domain/cad/sidecar.py \
  "s = s.replace('\"section_frame\", \"axis\", \"step\", \"mirrored\")', '\"section_frame\", \"axis\", \"step\")', 1)" \
  ./internal/domain/cad 'TestKernel_DistinctShapesAreBuiltOnceEach'

drill "the build does not say how many shapes it built" internal/domain/cad/cad.go \
  's = s.replace("ShapeBuilds: res.ShapeBuilds, ScriptRuns: scriptRuns,", "ScriptRuns: scriptRuns,", 1)' \
  ./internal/domain/cad 'TestKernel_OneDefinitionPlacedManyTimesIsBuiltOnce'

drill "a repeated scripted part runs its script once per copy" internal/domain/cad/cad.go \
  's = s.replace("outcome, ran := scripts[source]", "outcome, ran := scripts[\"\"]", 1)' \
  ./internal/domain/cad 'TestKernel_ARepeatedScriptedPartRunsItsScriptOnce'

echo
echo "A STEP export is one assembly of shared definitions"
# Added 2026-09-14 (Phase 4, stage K2). The sidecar writes STEP from an XDE document
# (one shape label per definition, a located component per occurrence), assembles
# in one pass, and reports its time per phase.
drill "the assembly attaches its children one by one again" internal/domain/cad/sidecar.py \
  "s = s.replace('    assembly = Compound(built)\n', '    assembly = Compound(children=built)\n', 1)" \
  ./internal/domain/cad 'TestKernel_ExportingManyOccurrencesGrowsLinearly'

drill "a shared shape is written with its own location left on" internal/domain/cad/sidecar.py \
  "s = s.replace('tool.AddShape(solid.wrapped.Located(TopLoc_Location()), False, False)', 'tool.AddShape(solid.wrapped, False, False)', 1)" \
  ./internal/domain/cad 'TestKernel_AnExportedFilePlacesEveryPartWhereTheBuildDid'

drill "an occurrence is written without its placement" internal/domain/cad/sidecar.py \
  "s = s.replace('tool.AddComponent(root, label, solid.wrapped.Location())', 'tool.AddComponent(root, label, TopLoc_Location())', 1)" \
  ./internal/domain/cad 'TestKernel_AnExportedFilePlacesEveryPartWhereTheBuildDid'

drill "the build does not say where its time went" internal/domain/cad/sidecar.py \
  "s = s.replace('        \"phases\": phases,\n', '', 1)" \
  ./internal/domain/cad 'TestKernel_ExportingManyOccurrencesGrowsLinearly'

echo
echo "The interference broad phase tests only boxes that could overlap"
# Added 2026-09-15 (Phase 4, stage K2b) as a one-axis sweep. Since Phase 5, stage V1
# the broad phase is a grid over all three axes, so the sweep's own drills (a box
# never closed, always sweeping along x, sorting by where a box ends) went with the
# sweep; these hold what both share.
drill "the budget is spent in broad-phase order" internal/domain/cad/sidecar.py \
  "s = s.replace('    pairs.sort()\n', '', 1)" \
  ./internal/domain/cad 'TestKernel_ATruncatedBroadPhaseStopsWhereEveryPairStops'

drill "the build does not say how many boxes it compared" internal/domain/cad/cad.go \
  's = s.replace(" InterferenceBoxTests: res.InterferenceBoxTests,\n", "\n", 1)' \
  ./internal/domain/cad 'TestKernel_InterferenceBoxTestsGrowLinearly'

echo
echo "Interference at scale: a grid, and a clash measured once per pose"
# Added 2026-09-15 (Phase 5, stage V1). Boxes are filed in a grid over all three axes
# and each pair is tested in one cell; boxes and volumes are read once per shape; the
# common volume of two copies is measured once per (shape, shape, relative pose), and
# the budget counts booleans paid for.
drill "every box lands in one cell" internal/domain/cad/sidecar.py \
  "s = s.replace('    cell = max(longest[len(longest) // 2], 1e-6)\n', '    cell = 1e12\n', 1)" \
  ./internal/domain/cad 'TestKernel_APlaneOfPartsCostsAFewBoxTestsEach'

# ‼️ The next two anchors moved on 2026-09-15 (large-box index): cell indices became
# integers computed once, and the every-box loop for long boxes became passes between
# groups of boxes with the same grid level on each axis.
drill "a pair is tested where the earlier box starts, which the other may not reach" internal/domain/cad/sidecar.py \
  "s = s.replace('if (max(qa[0], qb[0]) >> sx != home[0]', 'if (min(qa[0], qb[0]) >> sx != home[0]', 1)" \
  ./internal/domain/cad 'TestKernel_TheBroadPhaseFindsWhatEveryPairFinds'

drill "a box too long for the grid is never tested" internal/domain/cad/sidecar.py \
  "s = s.replace('            filed, looking = groups[level_a], groups[level_b]\n', '            continue\n', 1)" \
  ./internal/domain/cad 'TestKernel_TheBroadPhaseFindsWhatEveryPairFinds'

drill "every clash is measured again" internal/domain/cad/sidecar.py \
  "s = s.replace('_INTERFERENCE_CACHE = True\n', '_INTERFERENCE_CACHE = False\n', 1)" \
  ./internal/domain/cad 'TestKernel_RepeatedClashesPayForOneBooleanEachPose'

drill "a pose is compared without its rotation" internal/domain/cad/sidecar.py \
  "s = s.replace('for r in (1, 2, 3) for c in (1, 2, 3, 4))', 'for r in (1, 2, 3) for c in (4,))', 1)" \
  ./internal/domain/cad 'TestKernel_AReusedClashIsTheClashMeasuredAgain'

drill "the budget counts answers it did not pay for" internal/domain/cad/sidecar.py \
  "s = s.replace('            shared = cache[key]\n            reused += 1\n', '            shared = cache[key]\n            reused += 1\n            booleans += 1\n', 1)" \
  ./internal/domain/cad 'TestKernel_RepeatedClashesPayForOneBooleanEachPose'

drill "the build does not say how many booleans it paid for" internal/domain/cad/cad.go \
  's = s.replace(" InterferenceBooleans: res.InterferenceBooleans,", "", 1)' \
  ./internal/domain/cad 'TestKernel_RepeatedClashesPayForOneBooleanEachPose'

echo
echo "The interference check's large boxes: a level per axis, and a clash slid along a box"
# Added 2026-09-15 (large-box index). A box longer than four cells was tested against
# every box, which is quadratic when the long parts grow with the model: an airframe
# barrel's panels, frames and stringers made 16.2 billion tests at 1M occurrences. Each
# box now has a grid level per axis, and two groups of boxes are tested in the grid of
# their larger levels. A clash is reused along a box the other solid lies wholly inside,
# and only then: a finite stringer is not the same at its ends.
drill "a long box is tested against every box of another size again" internal/domain/cad/sidecar.py \
  "s = s.replace('            sz = _GRID_LEVEL_SHIFT * max(level_a[2], level_b[2])\n', '            sz = _GRID_LEVEL_SHIFT * max(level_a[2], level_b[2])\n            if level_a != level_b:\n                sx = sy = sz = 64\n', 1)" \
  ./internal/domain/cad 'TestKernel_BoxTestsGrowLinearlyWhenTheLongPartsGrowWithTheModel'

drill "one level for all three axes, a cubic cell as long as the longest side" internal/domain/cad/sidecar.py \
  "s = s.replace('        groups.setdefault(level, []).append(k)\n', '        groups.setdefault((max(level),) * 3, []).append(k)\n', 1)" \
  ./internal/domain/cad 'TestKernel_BoxTestsGrowLinearlyWhenTheLongPartsGrowWithTheModel'

drill "a pair from two groups is tested where it does not begin" internal/domain/cad/sidecar.py \
  "s = s.replace('if (max(qa[0], qb[0]) >> sx != cx', 'if (min(qa[0], qb[0]) >> sx != cx', 1)" \
  ./internal/domain/cad 'TestKernel_LongBoxesAreFoundAsEveryPairFindsThem'

drill "a box is filed without the last cell it reaches" internal/domain/cad/sidecar.py \
  "s = s.replace('            for cx in range(q[0] >> sx, (r[0] >> sx) + 1):\n', '            for cx in range(q[0] >> sx, r[0] >> sx):\n', 1)" \
  ./internal/domain/cad 'TestKernel_LongBoxesAreFoundAsEveryPairFindsThem'

drill "a clash is slid along a box it is not inside" internal/domain/cad/sidecar.py \
  "s = s.replace('        if mid - reach >= _SLIDE_MARGIN - half[r] and mid + reach <= half[r] - _SLIDE_MARGIN:\n', '        if True:\n', 1)" \
  ./internal/domain/cad 'TestKernel_PinsAlongARailPayForOneBooleanAndTheEndsAreMeasured'

drill "containment is checked at one end of the box only" internal/domain/cad/sidecar.py \
  "s = s.replace('        if mid - reach >= _SLIDE_MARGIN - half[r] and mid + reach <= half[r] - _SLIDE_MARGIN:\n', '        if mid - reach >= _SLIDE_MARGIN - half[r]:\n', 1)" \
  ./internal/domain/cad 'TestKernel_PinsAlongARailPayForOneBooleanAndTheEndsAreMeasured'

drill "a pin along a rail is measured at every pose again" internal/domain/cad/sidecar.py \
  "s = s.replace('_INTERFERENCE_SLIDE = True\n', '_INTERFERENCE_SLIDE = False\n', 1)" \
  ./internal/domain/cad 'TestKernel_PinsAlongARailPayForOneBooleanAndTheEndsAreMeasured'

drill "a slide seen only from the other box is not carried" internal/domain/cad/sidecar.py \
  "s = s.replace('    out = []\n    for v in axes:\n', '    out = []\n    for v in []:\n', 1)" \
  ./internal/domain/cad 'TestKernel_PinsAlongARailPayForOneBooleanAndTheEndsAreMeasured'

drill "a carried slide ignores a rotation that does not line the axes up" internal/domain/cad/sidecar.py \
  "s = s.replace('            if (abs(pose[4 * w + v]) >= 1 - 1e-9\n                    and all(abs(pose[4 * o + v]) <= 1e-9 for o in range(3) if o != w)):\n', '            if w == v:\n', 1)" \
  ./internal/domain/cad 'TestKernel_AReusedClashIsTheClashMeasuredAgain'

drill "a clash is keyed in the frame that slides less" internal/domain/cad/sidecar.py \
  "s = s.replace('        return forward if marked_f > marked_b else backward\n', '        return backward if marked_f > marked_b else forward\n', 1)" \
  ./internal/domain/cad 'TestKernel_PinsAlongARailPayForOneBooleanAndTheEndsAreMeasured'

echo
echo "A check that covered part of the model says how much"
# Added 2026-09-15 (Phase 5, stage V2). repairIfPartsOverlap returned without a word
# when it found nothing, and a check the pair budget stopped found nothing too; parts
# the kernel could not build were never in the check and never mentioned.
drill "a truncated check reads as a clean one" internal/agent/interference.go \
  's = s.replace("\tdefer func() {\n\t\tif note := coverageNote(sheet); note != \"\" {\n\t\t\treply.noteRepair(note)\n\t\t}\n\t}()\n", "", 1)' \
  ./internal/agent 'TestInterference_ATruncatedCheckSaysSoInTheTurn'

drill "truncation is not what the note is about" internal/agent/interference.go \
  's = s.replace("\tif sheet.Truncated {\n\t\tif sheet.Pairs > 0 {", "\tif false {\n\t\tif sheet.Pairs > 0 {", 1)' \
  ./internal/agent 'TestInterference_ATruncatedCheckSaysSoInTheTurn'

drill "a part that was never built is never mentioned" internal/agent/interference.go \
  's = s.replace("\tif n := len(sheet.Skipped); n > 0 {", "\tif n := len(sheet.Skipped); false && n > 0 {", 1)' \
  ./internal/agent 'TestInterference_APartThatWasNotBuiltIsNamedAsUnchecked'

drill "the render drops how much was checked" internal/agent/render.go \
  's = s.replace("\n\t\t\t\t\tChecked: built.Checked, Pairs: built.Pairs, Skipped: built.Skipped}", "}", 1)' \
  ./internal/agent 'TestRender_CarriesHowMuchTheCheckCovered'

echo
echo "Mass, centre of gravity and envelope roll up through the tree"
# Added 2026-09-15 (Phase 5, stage V3). The kernel measures each part's volume and centre
# once per shape (moved by each copy's placement) and its box from the placed solid; Go
# weighs them by density, or by volume and says so, for the model and every assembly.
drill "a copy's centre stays where its shape was built" internal/domain/cad/sidecar.py \
  "s = s.replace('    return tuple(t.Value(r, 1) * x + t.Value(r, 2) * y + t.Value(r, 3) * z + t.Value(r, 4) for r in (1, 2, 3))', '    return point', 1)" \
  ./internal/domain/cad 'TestKernel_APartsCentreMeasuredPerShapeIsItsSolidsCentre'

drill "every part is weighed by volume however dense it is" internal/domain/geometry/mass.go \
  's = s.replace("\t\tif report.Basis == MassByDensity {\n\t\t\tweight = mass\n\t\t}\n", "", 1)' \
  ./internal/domain/geometry 'TestMassProperties_WeighsEachPartByItsDensity'

drill "a part without a density still lets mass be claimed" internal/domain/geometry/mass.go \
  's = s.replace("\tif len(report.WithoutDensity) > 0 {\n\t\treport.Basis = MassByVolume\n\t}\n", "", 1)' \
  ./internal/domain/geometry 'TestMassProperties_WithoutEveryDensityIsWeighedByVolumeAndSaysSo'

drill "the roll-up forgets the assemblies a part is placed under" internal/domain/geometry/mass.go \
  's = s.replace("\t\tfor n := 1; n < len(segments); n++ {", "\t\tfor n := len(segments); n < len(segments); n++ {", 1)' \
  ./internal/domain/geometry 'TestMassProperties_RollsUpThroughTheTree'

drill "the build drops each part's measurements" internal/domain/cad/cad.go \
  's = s.replace("\t\tout.Properties = append(out.Properties, m)\n", "", 1)' \
  ./internal/domain/cad 'TestKernel_EachPartIsMeasuredWhereItWasBuilt'

drill "a density no material has is accepted" internal/domain/geometry/assembly.go \
  's = s.replace("\tif m.Density < 0 || m.Density != m.Density || m.Density > maxDensity {", "\tif false {", 1)' \
  ./internal/domain/geometry 'TestMaterial_RefusesADensityNoMaterialHas'

echo
echo "The vision check looks at sub-assemblies on their own"
# Added 2026-09-15 (Phase 5, stage V4). Beside the whole model, each assembly the root
# places is drawn once from the same build and looked at on its own — clashing ones
# first, at most four a turn — and the turn says how many of how many.
drill "sub-assemblies are never looked at" internal/agent/look.go \
  's = s.replace("\tsubs, of := subAssemblySheets(doc, sheet)", "\tsubs, of := []subSheet(nil), 0", 1)' \
  ./internal/agent 'TestLook_LooksAtEachDistinctSubAssemblyOnce'

drill "every occurrence of an assembly is stacked into one picture" internal/agent/look.go \
  's = s.replace("\t\tif seg == g.path {", "\t\tif true {", 1)' \
  ./internal/agent 'TestLook_LooksAtEachDistinctSubAssemblyOnce'

drill "every sub-assembly is looked at however many there are" internal/agent/look.go \
  's = s.replace("\tif len(groups) > maxSubAssemblyLooks {", "\tif false {", 1)' \
  ./internal/agent 'TestLook_CapsSubAssemblyLooksAndSaysHowMany'

drill "a clash does not move its sub-assembly forward" internal/agent/look.go \
  's = s.replace("\t\tif groups[i].clash != groups[j].clash {\n\t\t\treturn groups[i].clash\n\t\t}\n", "", 1)' \
  ./internal/agent 'TestLook_AClashingSubAssemblyIsLookedAtFirst'

drill "a tree's parts go unnamed in the prompt" internal/agent/look.go \
  's = s.replace("\tif doc.Root != \"\" {\n\t\tsource = doc.Expanded().Parts\n\t}\n", "", 1)' \
  ./internal/agent 'TestLook_ATreesPartsAreNamedInThePrompt'

drill "the turn does not say how closely it looked" internal/agent/look.go \
  's = s.replace("\tif covered.Of > covered.Looked {", "\tif false {", 1)' \
  ./internal/agent 'TestLook_CapsSubAssemblyLooksAndSaysHowMany'

echo
echo "The live car is measured as a tree"
# Added 2026-09-15 (Phase 2, stage A4). The live measurement counted len(doc.Parts),
# so a car written as a tree measured as zero parts. It now counts definitions,
# placed parts, occurrences and standard parts, tokens per design, and how much of
# the car the interference check covered. These drills need no model and no key.
drill "the car is counted by its top-level parts" internal/agent/car_tree_measure_test.go \
  's = s.replace("\tc.Occurrences = len(d.Expanded().Parts)\n", "\tc.Occurrences = len(d.Parts)\n", 1)' \
  ./internal/agent 'TestCarMeasure_CountsATreeByDefinitionAndByOccurrence'

drill "a standard part is not counted" internal/agent/car_tree_measure_test.go \
  's = s.replace("\t\tif p.Standard != \"\" {", "\t\tif false {", 1)' \
  ./internal/agent 'TestCarMeasure_CountsATreeByDefinitionAndByOccurrence'

drill "a tree's designs are its placed parts" internal/agent/car_tree_measure_test.go \
  's = s.replace("\tif c.Definitions > 0 {\n\t\treturn c.Definitions", "\tif false {\n\t\treturn c.Definitions", 1)' \
  ./internal/agent 'TestCarMeasure_CountsATreeByDefinitionAndByOccurrence'

drill "a pair answered from a measured pose is not counted as checked" internal/agent/car_tree_measure_test.go \
  's = s.replace("b.InterferenceBooleans+b.InterferenceReused, ", "b.InterferenceBooleans, ", 1)' \
  ./internal/agent 'TestCarMeasure_CoverageCountsReusedPairsAsChecked'

echo
echo "A build step on a tree is shown the assembly it builds"
# Added 2026-09-15 (Phase 2, stage A2). A step names the sub-assembly it builds and is
# shown that subtree, how the root places it and the interfaces it attaches at; its
# prompt stays the same size however many subsystems exist, and over a ceiling it is
# refused by name.
drill "a step on a tree is shown the whole model" internal/agent/assemble.go \
  's = s.replace("\tif view := SubtreeModel(doc, step.Assembly); view != \"\" {", "\tif view := \"\"; view != \"\" {", 1)' \
  ./internal/agent 'TestAssemble_AStepOnATreeIsShownOnlyItsAssembly'

drill "a step over its ceiling is sent anyway" internal/agent/assemble.go \
  's = s.replace("\tif doc.Root != \"\" && len(body) > maxStepContextBytes {", "\tif false {", 1)' \
  ./internal/agent 'TestAssemble_AStepShownMoreThanItsCeilingIsRefused'

drill "the plan drops the assembly a step builds" internal/agent/assemble.go \
  's = s.replace("json:\"assembly,omitempty\"", "json:\"-\"", 1)' \
  ./internal/agent 'TestPlanBuild_ReadsTheAssemblyAStepBuilds'

drill "the view carries every subsystem" internal/agent/subtree.go \
  's = s.replace("\twalk(focus)\n", "\tfor id := range assemblies {\n\t\twalk(id)\n\t}\n", 1)' \
  ./internal/agent 'TestSubtreeModel_StaysTheSameSizeAsTheModelGrows'

drill "a placement's interfaces are not carried" internal/agent/subtree.go \
  's = s.replace("\t\tview.AttachesTo = append(view.AttachesTo, attachment{Assembly: a.ID, Interfaces: a.Interfaces})\n", "", 1)' \
  ./internal/agent 'TestSubtreeModel_CarriesWhatTheFocusPlacesAndAttachesTo'

echo
echo "A pool of kernel processes builds side by side"
# Added 2026-09-15 (Phase 4, stage K3). A Kernel is FORGE_CAD_POOL processes behind a
# FIFO channel; a build takes a free one, retries once on that slot, and gives it back.
drill "the pool is always one process" internal/domain/cad/sidecar_process.go \
  's = s.replace("\t\tk.slots = make(chan *sidecar, k.size)\n\t\tfor i := 0; i < k.size; i++ {", "\t\tk.slots = make(chan *sidecar, 1)\n\t\tfor i := 0; i < 1; i++ {", 1)' \
  ./internal/domain/cad 'TestKernel_ConcurrentBuildsDoNotSerialise'

drill "the retry writes to the dead process again" internal/domain/cad/cad.go \
  's = s.replace("\t\ts.stop()\n\t\tk.log.Warn(ctx, logx.EventCADRestarted", "\t\tk.log.Warn(ctx, logx.EventCADRestarted", 1)' \
  ./internal/domain/cad 'TestRetryAfterTheProcessDies|TestRetryAfterEveryProcessInThePoolDies'

drill "configuration accepts a pool of no processes" internal/platform/config/config.go \
  's = s.replace("\tif cfg.CAD.Pool <= 0 {\n", "\tif false {\n", 1)' \
  ./internal/platform/config 'TestCADPoolMustBePositive'

echo
echo "A mesh is tessellated once per definition"
# Added 2026-09-15 (Phase 4, stage K4). The sidecar tessellates each distinct shape once
# in its own frame and sends every untouched copy as a 4x4 column-major matrix; Go
# (WorldMeshes) and the browser (expandMeshInstances) move each copy into place.
drill "every part is meshed on its own again" internal/domain/cad/sidecar.py \
  's = s.replace("_MESH_PER_DEFINITION = True\n", "_MESH_PER_DEFINITION = False\n", 1)' \
  ./internal/domain/cad 'TestKernel_AMeshIsTessellatedOncePerDefinition'

drill "an instance matrix is written row by row" internal/domain/cad/sidecar.py \
  's = s.replace("return [t.Value(1, 1), t.Value(2, 1), t.Value(3, 1), 0.0,", "return [t.Value(1, 1), t.Value(1, 2), t.Value(1, 3), 0.0,", 1)' \
  ./internal/domain/cad 'TestKernel_AnInstancedMeshIsTheMeshEveryPartUsedToGet'

drill "a part a feature changed is drawn as the shape it started from" internal/domain/cad/sidecar.py \
  's = s.replace("        touched.add(op[\"of\"])\n", "", 1)' \
  ./internal/domain/cad 'TestKernel_AnInstancedMeshIsTheMeshEveryPartUsedToGet'

drill "Go moves a copy without its translation" internal/domain/cad/cad.go \
  's = s.replace("v[i] = m[0]*x + m[4]*y + m[8]*z + m[12]", "v[i] = m[0]*x + m[4]*y + m[8]*z", 1)' \
  ./internal/domain/cad 'TestWorldMeshesPlacesEachCopyByItsMatrix'

drill "the browser moves a copy without its translation" internal/httpapi/assets/forge3d.js \
  "s = s.replace('v[i] = m[0] * x + m[4] * y + m[8] * z + m[12];', 'v[i] = m[0] * x + m[4] * y + m[8] * z;', 1)" \
  ./internal/httpapi 'TestRendererExpandsMeshInstancesLikeTheBuild'

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
  's = s.replace("\tserved, err := c.servedModels(ctx, base, key)", "\tserved, err := []string(nil), error(nil)\n\t_, _ = base, key\n\t_ = c.servedModels", 1)' \
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

echo
echo "Standard parts and patterns in the contract"
# Added 2026-09-15 (Phase 2, stage A3). A part names a catalogued designation and is
# written out as the revolve or extrusion it is, at the published figures, the same in
# Go and in the browser; an unknown designation is refused with the nearest named. The
# contract offers the catalogue rendered from the table, and children written out one
# at a time where one pattern would place them are named to the turn and to the next
# build step — a warning, never a refusal.
drill "an M8 cap screw's head is 14 across" internal/domain/geometry/standard.go \
  's = s.replace("{\"M8\", 8, 13, 8, 12, 80},", "{\"M8\", 8, 14, 8, 12, 80},", 1)' \
  ./internal/domain/geometry 'TestStandard_TheCatalogueCarriesThePublishedFigures'

drill "the browser's M8 cap screw head is 14 across" internal/httpapi/assets/forge3d.js \
  's = s.replace("[\x27M8\x27, 8, 13, 8, 12, 80]", "[\x27M8\x27, 8, 14, 8, 12, 80]", 1)' \
  ./internal/httpapi 'TestRendererDrawsTheSameStandardPartAsTheExporter'

drill "a hex nut is not stood up along Y" internal/domain/geometry/standard.go \
  's = s.replace("shape: \"extrusion\", turned: true,", "shape: \"extrusion\", turned: false,", 1)' \
  ./internal/domain/geometry 'TestStandard_TheKernelAndTheMeasurementSeeTheDrawnPart'

drill "a refused designation names no near match" internal/domain/geometry/standard.go \
  's = s.replace("quotedList(nearestStandards(p.Standard, 3))", "quotedList(nil)", 1)' \
  ./internal/domain/geometry 'TestStandard_AnUnknownDesignationIsRefusedWithTheNearestNamed'

drill "an unknown designation is not a fault" internal/domain/geometry/faults.go \
  's = s.replace("profileProblems = append(profileProblems, standardProblems...)", "_ = standardProblems", 1)' \
  ./internal/domain/geometry 'TestStandard_AnUnknownDesignationIsRefusedWithTheNearestNamed'

drill "the kernel is sent the word standard" internal/domain/geometry/solid.go \
  's = s.replace("d, standardProblems := expandStandards(d)", "standardProblems := []Problem(nil)", 1)' \
  ./internal/domain/geometry 'TestStandard_TheKernelAndTheMeasurementSeeTheDrawnPart|TestStandard_EveryDesignationBuilds'

drill "measurement reads a standard part as a unit box" internal/domain/geometry/overlay.go \
  's = s.replace("\ttree, _ = expandStandards(tree)\n", "", 1)' \
  ./internal/domain/geometry 'TestStandard_TheKernelAndTheMeasurementSeeTheDrawnPart'

drill "the contract's catalogue is typed rather than rendered" internal/agent/converse.go \
  's = s.replace("geometry.StandardGuide(), strings.Join(", "\"    \\\"ISO 4762 M8x30\\\"\", strings.Join(", 1)' \
  ./internal/agent 'TestTheContractTeachesEveryStandardPartFORGEHas'

drill "irregular spacing counts as a row" internal/domain/geometry/repetition.go \
  's = s.replace("const repetitionTolerance = 1e-3", "const repetitionTolerance = 0.2", 1)' \
  ./internal/domain/geometry 'TestRepetition_IrregularSpacingIsNotFlagged'

drill "a grid is never looked for" internal/domain/geometry/repetition.go \
  's = s.replace("if p, ok := gridOf(g.siblings); ok {", "if p, ok := gridOf(g.siblings); ok && false {", 1)' \
  ./internal/domain/geometry 'TestRepetition_FindsAGrid'

drill "a ring is never looked for" internal/domain/geometry/repetition.go \
  's = s.replace("if p, unturned, ok := ringOf(g.siblings); ok {", "if p, unturned, ok := ringOf(g.siblings); ok && false {", 1)' \
  ./internal/domain/geometry 'TestRepetition_FindsAPolarRing'

drill "groups are read in map order" internal/domain/geometry/repetition.go \
  's = s.replace("\tfor _, g := range groups {", "\tfor _, g := range byKey {", 1)' \
  ./internal/domain/geometry 'TestRepetition_IsDeterministic'

drill "the turn does not say what could be one pattern" internal/agent/converse.go \
  's = s.replace("\tnoteRepetition(&reply)\n\treturn &reply, nil", "\treturn &reply, nil", 1)' \
  ./internal/agent 'TestRepetition_TheTurnSaysWhatCouldBeOnePattern'

drill "the streaming turn does not say what could be one pattern" internal/agent/converse_stream.go \
  's = s.replace("\t\tnoteRepetition(&reply)\n", "", 1)' \
  ./internal/agent 'TestRepetition_TheTurnSaysWhatCouldBeOnePattern'

drill "a build step is not told what could be one pattern" internal/agent/assemble.go \
  's = s.replace("\tsofar += repetitionForStep(doc)\n", "", 1)' \
  ./internal/agent 'TestAssemble_AStepIsToldWhatCouldBeOnePattern'
# Added 2026-09-15 (Phase 7, stage E2). Comparing designs placed in assemblies
# reports a changed definition ONCE, with how many times each variant places it,
# and leaves a comparison of flat documents exactly as it was. Two of these break
# the same line and read it from two packages: the domain fence says a flat
# comparison has no structure, and the HTTP fence says its response is byte for
# byte the one pinned before trees could be compared.
echo
echo "Comparing trees"
drill "a flat comparison grows a structure section" internal/domain/geometry/compare_structure.go \
  's = s.replace("\tif !trees {", "\tif false {", 1)' \
  ./internal/domain/geometry 'TestCompare_AFlatComparisonHasNoStructure'

drill "a flat comparison's response gains a key" internal/domain/geometry/compare_structure.go \
  's = s.replace("\tif !trees {", "\tif false {", 1)' \
  ./internal/httpapi 'TestCompare_AFlatComparisonComesBackExactlyAsItDidBeforeTreesCouldBeCompared'

drill "a definition's size is not compared" internal/domain/geometry/compare_structure.go \
  's = s.replace("{\"size\", func(a, b Part, l *lengths) bool {", "{\"size\", func(a, b Part, l *lengths) bool {\n\t\treturn true", 1)' \
  ./internal/domain/geometry 'TestCompare_ChangingADefinitionPlacedFourTimesIsOneChangedDefinition'

drill "a pattern's copies are not counted" internal/domain/geometry/compare_structure.go \
  's = s.replace("k := len(slots)", "k := 1 + 0*len(slots)", 1)' \
  ./internal/domain/geometry 'TestCompare_APatternCopyChangesTheCountsAndTheChildsPattern'

drill "a pattern on a sub-assembly does not multiply it" internal/domain/geometry/compare_structure.go \
  's = s.replace("m[def] = sat(m[def] + mul(k, n))", "m[def] = sat(m[def] + n)", 1)' \
  ./internal/domain/geometry 'TestCompare_APatternedAssemblyMultipliesWhatItPlaces'

drill "a moved interface is not compared" internal/domain/geometry/compare_structure.go \
  's = s.replace("func(a, b Interface, l *lengths) bool { return l.vector(a.Position, b.Position) }", "func(a, b Interface, l *lengths) bool { return true }", 1)' \
  ./internal/domain/geometry 'TestCompare_AMovedInterfaceIsReportedOnItsAssembly'

drill "a definition or child in one variant only is not reported" internal/domain/geometry/compare_structure.go \
  's = s.replace("missing = append(missing, col+1)", "missing = missing[:len(missing):len(missing)]", 1)' \
  ./internal/domain/geometry 'TestCompare_AddedAndRemovedDefinitionsAndChildrenAreReported'

drill "the response leaves the structure out" internal/httpapi/geometry.go \
  's = s.replace("body[\"structure\"] = structureBody(cmp.Structure)", "_ = structureBody(cmp.Structure)", 1)' \
  ./internal/httpapi 'TestCompare_TheResponseCarriesTheStructure'
echo
echo "A kernel build that runs out of time"
# Added 2026-09-15 (kernel timeout is not a crash). A build past its limit was
# killed, retried on a fresh process, killed again and reported as
# CONNECTOR_UNAVAILABLE — 501 "no working backend" after two limits for a design
# that was only large. The kernel fences run against cadtest's fake process, so
# they need no build123d; the HTTP one needs FORGE_TEST_DATABASE_URL.
# docs/bugfix/2026-09-15-a-kernel-build-that-ran-out-of-time-was-reported-as-no-kernel.md
drill "a timed-out build is retried like a crashed one" internal/domain/cad/cad.go \
  's = s.replace("\tif err != nil && !errors.As(err, &late) {", "\tif err != nil {", 1)' \
  ./internal/domain/cad 'TestKernel_ABuildThatRunsOutOfTimeIsNotRetriedAndSaysSo'

# Re-anchored 2026-09-16 (K3): the round trip, and the kill that bounds it, moved
# onto the SLOT — sidecar_process.go — and the reset after a late error is that
# slot's rather than the kernel's. The three drills below follow the code.
drill "the kill does not record that the kernel's limit ran out" internal/domain/cad/sidecar_process.go \
  's = s.replace("\t\t\tstopped.Store(&lateError{limit: s.timeout})\n", "", 1)' \
  ./internal/domain/cad 'TestKernel_ABuildThatRunsOutOfTimeIsNotRetriedAndSaysSo'

drill "a timeout leaves the killed process in the slot" internal/domain/cad/cad.go \
  's = s.replace("\t\t\ts.stop()\n\t\t\tk.log.Warn(ctx, logx.EventCADTimedOut", "\t\t\tk.log.Warn(ctx, logx.EventCADTimedOut", 1)' \
  ./internal/domain/cad 'TestKernel_AfterATimeoutTheKernelStartsAFreshProcessForTheNextBuild'

drill "the kernel's limit is reported as no working backend" internal/domain/cad/cad.go \
  's = s.replace("\tcase late.caller == nil:\n\t\treturn errs.Wrap(op, errs.CodeKernelTimeout, late).", "\tcase late.caller == nil:\n\t\treturn errs.Wrap(op, errs.CodeConnectorUnavailable, late).", 1)' \
  ./internal/domain/cad 'TestKernel_ABuildThatRunsOutOfTimeIsNotRetriedAndSaysSo'

drill "the caller's deadline is reported as no working backend" internal/domain/cad/cad.go \
  's = s.replace("\tcase errors.Is(late.caller, context.DeadlineExceeded):\n\t\treturn errs.Wrap(op, errs.CodeKernelTimeout, late).", "\tcase errors.Is(late.caller, context.DeadlineExceeded):\n\t\treturn errs.Wrap(op, errs.CodeConnectorUnavailable, late).", 1)' \
  ./internal/httpapi 'TestAPI_AKernelBuildThatTakesTooLongIsA504ThatSaysSo'

drill "a kernel timeout is offered as retryable" internal/platform/errs/code.go \
  's = s.replace("A kernel build is allowed 30 seconds.\", false},", "A kernel build is allowed 30 seconds.\", true},", 1)' \
  ./internal/httpapi 'TestAPI_AKernelBuildThatTakesTooLongIsA504ThatSaysSo'

drill "a kernel timeout is a 501" internal/platform/errs/code.go \
  's = s.replace("CodeKernelTimeout: {CodeKernelTimeout, CategoryExternal, 504,", "CodeKernelTimeout: {CodeKernelTimeout, CategoryExternal, 501,", 1)' \
  ./internal/httpapi 'TestAPI_AKernelBuildThatTakesTooLongIsA504ThatSaysSo'

drill "a process that dies mid-build is not retried" internal/domain/cad/cad.go \
  's = s.replace("\t\tres, err = s.roundTrip(ctx, req)\n\t}\n\tif err != nil {", "\t}\n\tif err != nil {", 1)' \
  ./internal/domain/cad 'TestKernel_AProcessThatDiesMidBuildIsStillRetriedOnce'

# Added 2026-09-16 (K3 × the timeout work). With a pool, "the kernel is reset after
# a late error" and "the other processes are untouched" stop being the same
# sentence: a slow assembly must cost ITS slot's process and leave the rest of the
# pool building. Neither PR could hold this alone — #73 had no timeout to cross,
# #103 had no second process to spare.
drill "a timeout resets every slot in the pool" internal/domain/cad/cad.go \
  's = s.replace("\t\t\ts.stop()\n\t\t\tk.log.Warn(ctx, logx.EventCADTimedOut", "\t\t\tfor _, o := range k.all {\n\t\t\t\to.stop()\n\t\t\t}\n\t\t\tk.log.Warn(ctx, logx.EventCADTimedOut", 1)' \
  ./internal/domain/cad 'TestKernel_ATimeoutInOneSlotLeavesTheOtherSlotsServing'

drill "a slot enforces a limit that is not the kernel's" internal/domain/cad/sidecar_process.go \
  's = s.replace("slot: i, timeout: k.timeout}", "slot: i, timeout: buildTimeout}", 1)' \
  ./internal/domain/cad 'TestKernel_ABuildThatRunsOutOfTimeIsNotRetriedAndSaysSo'
echo "Workers in one process"
# Added 2026-09-15 (worker lease identity). NewWorker sliced a fresh id's
# timestamp rather than its random tail, so every worker forge-worker started
# together had one identity and no lease guard could tell siblings apart. Found
# building the off-node STEP export (#99). The second fence needs
# FORGE_TEST_DATABASE_URL. See
# docs/bugfix/2026-09-15-workers-started-together-shared-one-lease-identity.md.
drill "workers started together share one identity" internal/agent/worker.go \
  's = s.replace("run[len(run)-8:]", "run[4:12]", 1)' \
  ./internal/agent 'TestNewWorker_WorkersStartedTogetherHaveDistinctIdentities|TestWorker_ASiblingCannotExtendOrReleaseALeaseItDoesNotHold'

drill "a worker is named by its host and pid alone" internal/agent/worker.go \
  's = s.replace("run[len(run)-8:]", "run[len(run)-8:len(run)-8]", 1)' \
  ./internal/agent 'TestNewWorker_WorkersStartedTogetherHaveDistinctIdentities|TestWorker_ASiblingCannotExtendOrReleaseALeaseItDoesNotHold'
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

# ---------------------------------------------------------------------------
# Added 2026-09-15 (transcriber endpoint). FORGE_LLM_TRANSCRIBER_BASE_URL and
# FORGE_LLM_TRANSCRIBER_API_KEY: speech to text on an endpoint of its own. The
# property every drill here breaks is the same one from a different side — which
# host hears a recording, and whose key it is sent with.
# ---------------------------------------------------------------------------

echo "Transcriber endpoint"
drill "a transcriber key without an endpoint loads" internal/platform/config/config.go \
  's = s.replace("tURL == \"\" && tKey != \"\"", "false && tKey != \"\"", 1)' \
  ./internal/platform/config 'TestTranscriberEndpoint_AKeyWithoutAnEndpointIsRefusedByName'

drill "another host with no key of its own loads" internal/platform/config/config.go \
  's = s.replace("case tKey == \"\" && !SameOrigin(tURL, cfg.LLM.BaseURL):", "case false:", 1)' \
  ./internal/platform/config 'TestTranscriberEndpoint_AnotherHostWithoutAKeyIsRefusedByName'

drill "a second host hearing speech is not named at startup" internal/platform/config/config.go \
  's = s.replace("\t\tcase !SameOrigin(tURL, cfg.LLM.BaseURL):", "\t\tcase false:", 1)' \
  ./internal/platform/config 'TestTranscriberEndpoint_ASeparateEndpointWithItsOwnKeyIsUsed'

drill "the resolver lends the chat key to another host" internal/platform/config/config.go \
  's = s.replace("return own, \"\"", "return own, c.APIKey", 1)' \
  ./internal/platform/config 'TestTranscriberEndpoint_NeverHandsTheChatKeyToAnotherHost'

drill "config print shows the transcriber key" internal/platform/config/config.go \
  's = s.replace("c.LLM.TranscriberAPIKey != \"\",", "c.LLM.TranscriberAPIKey,", 1)' \
  ./internal/platform/config 'TestTranscriberEndpoint_TheKeyNeverReachesConfigPrint'

drill "the client keeps the chat key for speech" internal/llm/openai_compatible.go \
  's = s.replace("transcriberKey: sttKey,", "transcriberKey: cfg.APIKey,", 1)' \
  ./internal/llm 'TestTranscribe_AnotherHostWithNoKeyOfItsOwnIsNeverSentTheChatKey'

drill "transcription is sent to the chat host" internal/llm/transcribe.go \
  's = s.replace("c.transcriberURL+\"/chat/completions\"", "c.baseURL+\"/chat/completions\"", 1)' \
  ./internal/llm 'TestTranscribe_ASeparateEndpointGetsTheAudioWithItsOwnKeyAndTheChatHostGetsNothing'

drill "transcription carries the chat key" internal/llm/transcribe.go \
  's = s.replace("\"Bearer \"+c.transcriberKey", "\"Bearer \"+c.apiKey", 1)' \
  ./internal/llm 'TestTranscribe_ASeparateEndpointGetsTheAudioWithItsOwnKeyAndTheChatHostGetsNothing'

drill "a transcriber 404 lists the chat host's models" internal/llm/openai_compatible.go \
  's = s.replace("base, key := c.endpointFor(role)", "base, key := c.baseURL, c.apiKey", 1)' \
  ./internal/llm 'TestTranscribe_AnUnservedModelOnItsOwnEndpointListsThatEndpointAndNamesItsSettings'

drill "a transcriber 404 names only the model" internal/llm/openai_compatible.go \
  's = s.replace("\tif role != RoleTranscriber {\n\t\treturn \"\"", "\tif true {\n\t\treturn \"\"", 1)' \
  ./internal/llm 'TestTranscribe_AnUnservedModelOnTheChatEndpointNamesTheSettingsThatMoveSpeech'

drill "room speech is sent to the chat host" internal/llm/openai_compatible.go \
  's = s.replace("transcriberURL: sttURL,", "transcriberURL: base,", 1)' \
  ./internal/media 'TestRoomSpeech_GoesToTheTranscriberEndpointWithItsKeyAndNeverToTheChatHost'

drill "the 501 never names the transcriber endpoint" internal/httpapi/transcribe.go \
  's = s.replace("FORGE_LLM_TRANSCRIBER_BASE_URL with \"+", "the transcriber endpoint with \"+", 1)' \
  ./internal/httpapi 'TestTranscribe_ADeploymentWithoutATranscriberSaysWhatTurnsItOn'

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

# Added 2026-09-15. The variables above do not reach OpenCASCADE's own thread pool,
# which build123d's booleans run on and which is sized to the processors online: a
# 16-CPU machine still started 16 threads.
# docs/bugfix/2026-09-15-scripts-still-failed-on-machines-with-many-cores.md
drill "OpenCASCADE's own pool starts a thread per core again" internal/domain/cad/script.py \
  "s = s.replace('    pool = OSD_ThreadPool.DefaultPool_s(1)\n    if pool.NbThreads() != 1:\n        pool.Init(1)\n', '    pool = OSD_ThreadPool.DefaultPool_s()\n', 1)" \
  ./internal/domain/cad 'TestScript_RunsOpenCascadeOnOneThread'

echo
echo "Blob storage, wired into the deployment"
# Added 2026-09-15 (Phase 3, stage S4). The manifests are committed and NOT
# applied, so nothing on the cluster notices them drifting: these fences are all
# that holds them until the next deploy runs verify.sh. The last drill is the
# command that check runs, reporting a round trip it did not observe.
drill "the ConfigMap loses the bucket" deploy/k8s/20-config.yaml \
  's = s.replace("  FORGE_BLOB_BUCKET: \"forge-geometry-373468206837\"\n", "", 1)' \
  ./deploy 'TestConfigMap_'

drill "the worker's egress policy drops the IMDS rule" deploy/k8s/32-worker-egress.yaml \
  'i = s.index("    # 3. IMDS"); j = s.index("    # 4. HTTPS"); s = s[:i] + s[j:]' \
  ./deploy 'TestWorkerEgress_'

drill "the worker's egress policy lets 443 into the cluster" deploy/k8s/32-worker-egress.yaml \
  's = s.replace("            except:\n              - 10.0.0.0/8\n", "            except:\n", 1)' \
  ./deploy 'TestWorkerEgress_'

drill "verify.sh checks the blob round trip in forged only" deploy/verify.sh \
  's = s.replace("for d in forged forge-worker; do\n  POD=", "for d in forged; do\n  POD=", 1)' \
  ./deploy 'TestVerify_ChecksTheBlobRoundTripInsideBothPods'

drill "forgectl blob check does not compare the bytes it read back" cmd/forgectl/blob.go \
  's = s.replace("if !bytes.Equal(got, blobCheckPayload) {", "if !bytes.Equal(got, got) {", 1)' \
  ./cmd/forgectl 'TestBlobCheck_AStoreThatLiesIsNeverReportedOK'

# Added 2026-09-15 (Phase 6, stages W1–W3). Every copy of a shape is one instance of one
# uploaded mesh, its matrix, colour and highlight read by the GPU at a stride and offset
# from a buffer written per frame; what is out of view is not sent and what is tiny is
# drawn as its box; a click is resolved back to the occurrence path through the matrix
# the copy was drawn with, and a tree row reaches exactly what Go places under it. The
# fences read what was SENT through scripts/webgl-stub.js, over WebGL2, WebGL1 with
# ANGLE_instanced_arrays and WebGL1 without it.
drill "an instance's colour is read one float out" internal/httpapi/assets/forge3d.js \
  's = s.replace("false, stride, off + 64);", "false, stride, off + 68);", 1)' \
  ./internal/httpapi 'TestRendererUploadsTheInstancesTheExporterPlaces'

drill "a mirrored primitive is drawn unmirrored" internal/httpapi/assets/forge3d.js \
  's = s.replace("(spec.mirrored ? -1 : 1)", "1", 1)' \
  ./internal/httpapi 'TestRendererUploadsTheInstancesTheExporterPlaces'

drill "instance attributes are left on for the grid" internal/httpapi/assets/forge3d.js \
  's = s.replace("    this._resetInstanceAttributes();\n", "", 1)' \
  ./internal/httpapi 'TestRendererUploadsTheInstancesTheExporterPlaces'

drill "the mesh reply's copies are drawn as their primitives" internal/httpapi/assets/forge3d.js \
  's = s.replace("var inst = instanceOf[part.id];", "var inst = null;", 1)' \
  ./internal/httpapi 'TestRendererDrawsTheMeshReplysInstancesByTheirMatrices'

drill "the workbench stops handing the mesh reply to the studio" internal/httpapi/assets/workbench.js \
  's = s.replace("studio.load(proto, b);", "studio.load(proto);", 1)' \
  ./internal/httpapi 'TestWorkbenchDrawsTheMeshReplyInstanced'

drill "nothing out of view is culled" internal/httpapi/assets/forge3d.js \
  's = s.replace("if (!sphereInFrustum(f.planes, cx, cy, cz, r)) {", "if (false) {", 1)' \
  ./internal/httpapi 'TestRendererPicksAnInstanceBackToItsOccurrencePath'

drill "a far copy is never drawn as its box" internal/httpapi/assets/forge3d.js \
  's = s.replace("var proxy = lod > 0 &&", "var proxy = false &&", 1)' \
  ./internal/httpapi 'TestRendererPicksAnInstanceBackToItsOccurrencePath'

drill "a click names the first copy of whatever it hit" internal/httpapi/assets/forge3d.js \
  's = s.replace("best = { id: b.ids[i],", "best = { id: b.ids[0],", 1)' \
  ./internal/httpapi 'TestRendererPicksAnInstanceBackToItsOccurrencePath'

# chr(92): the anchor is a regular expression's escaped backslash, and a backslash
# written here passes through bash and Python before it reaches the file.
drill "a tree row misses the copies a patterned ancestor made" internal/httpapi/assets/forge3d.js \
  's = s.replace("\x27(-" + chr(92) * 2 + "d+)*\x27", "\x27\x27", 1)' \
  ./internal/httpapi 'TestRendererSelectsAndIsolatesTheOccurrencesUnderATreeNode'

drill "the browser still stops drawing at the kernel's 4096" internal/httpapi/assets/forge3d.js \
  's = s.replace("var MAX_VIEWPORT_PARTS = 100000;", "var MAX_VIEWPORT_PARTS = 4096;", 1)' \
  ./internal/httpapi 'TestRendererFlattensATreeLikeTheExporter'

drill "the viewport's ceiling is the kernel's" internal/domain/geometry/limits.go \
  's = s.replace("const maxViewportParts = DefaultMaxOccurrences", "const maxViewportParts = maxDrawnParts", 1)' \
  ./internal/domain/geometry 'TestLimits_TheViewportDrawsWhatStorageAcceptsAndNoMore'

echo "A3's gaps: the catalogue read, a real build, the turn told, flat parts, density taught"
# Added 2026-09-15 (A3 gaps and density). Every catalogue family was read against a
# published table (standard.go names each), which found the L20x20x3 toe radius
# wrong; one standard part per family is built by OCCT and measured against the
# published figures; an ordinary turn's model is told what could be one pattern;
# a flat document's top-level parts are checked like siblings; and the contract
# teaches "density" with the ceiling and the mass rule the code applies.
drill "an L20 angle's toe radius is 2 again" internal/domain/geometry/standard.go \
  's = s.replace("{20, 3, 3.5, 1.75},", "{20, 3, 3.5, 2},", 1)' \
  ./internal/domain/geometry 'TestStandard_EveryFamilyCarriesTheFiguresItsSourcePublishes'

drill "OCCT builds an L20 angle with a toe radius of 2" internal/domain/geometry/standard.go \
  's = s.replace("{20, 3, 3.5, 1.75},", "{20, 3, 3.5, 2},", 1)' \
  ./internal/domain/cad 'TestKernel_EveryStandardFamilyBuildsTheSolidItsFiguresDescribe'

drill "the browser's L20 angle's toe radius is 2 again" internal/httpapi/assets/forge3d.js \
  's = s.replace("[20, 3, 3.5, 1.75]", "[20, 3, 3.5, 2]", 1)' \
  ./internal/httpapi 'TestRendererDrawsTheSameStandardPartAsTheExporter'

drill "an M10 nut is DIN 934's 17 across flats" internal/domain/geometry/standard.go \
  's = s.replace("{\"M10\", 10, 16, 8.4},", "{\"M10\", 10, 17, 8.4},", 1)' \
  ./internal/domain/geometry 'TestStandard_EveryFamilyCarriesTheFiguresItsSourcePublishes'

drill "an M6 washer's hole is the thread's 6" internal/domain/geometry/standard.go \
  's = s.replace("{\"M6\", 6.4, 12, 1.6},", "{\"M6\", 6, 12, 1.6},", 1)' \
  ./internal/domain/geometry 'TestStandard_EveryFamilyCarriesTheFiguresItsSourcePublishes'

drill "a 6003 bearing is 11 wide" internal/domain/geometry/standard.go \
  's = s.replace("{\"6003\", 17, 35, 10},", "{\"6003\", 17, 35, 11},", 1)' \
  ./internal/domain/geometry 'TestStandard_EveryFamilyCarriesTheFiguresItsSourcePublishes'

drill "M8 cap screws run to 90, as one supplier stocks them" internal/domain/geometry/standard.go \
  's = s.replace("{\"M8\", 8, 13, 8, 12, 80},", "{\"M8\", 8, 13, 8, 12, 90},", 1)' \
  ./internal/domain/geometry 'TestStandard_EveryFamilyCarriesTheFiguresItsSourcePublishes'

drill "a hollow section's inside corner is 1.5T" internal/domain/geometry/standard.go \
  's = s.replace("outside, inside := 2*h.T*k, h.T*k", "outside, inside := 2*h.T*k, 1.5*h.T*k", 1)' \
  ./internal/domain/geometry 'TestStandard_EveryFamilyCarriesTheFiguresItsSourcePublishes'

drill "OCCT turns a revolve half way round" internal/domain/cad/sidecar.py \
  's = s.replace("revolve(f, axis, 360)", "revolve(f, axis, 180)", 1)' \
  ./internal/domain/cad 'TestKernel_EveryStandardFamilyBuildsTheSolidItsFiguresDescribe'

drill "OCCT extrudes a section twice its depth" internal/domain/cad/sidecar.py \
  's = s.replace("amount=d[\"depth\"] / 2.0, both=True", "amount=d[\"depth\"], both=True", 1)' \
  ./internal/domain/cad 'TestKernel_EveryStandardFamilyBuildsTheSolidItsFiguresDescribe'

drill "an outline is sent to the kernel in the document's unit" internal/domain/geometry/solid.go \
  's = s.replace("c, err := section.Outer.scaled(toMM).exact(\"outline\")", "c, err := section.Outer.exact(\"outline\")", 1)' \
  ./internal/domain/cad 'TestKernel_EveryStandardFamilyBuildsTheSolidItsFiguresDescribe'

drill "an ordinary turn is not told what could be one pattern" internal/agent/converse_stream.go \
  's = s.replace("\"]\" + repetitionForTurn(current) + \"\\n\\n\" + message", "\"]\" + \"\\n\\n\" + message", 1)' \
  ./internal/agent 'TestBuildMessages_ATurnIsToldWhatCouldBeOnePattern'

drill "a turn is told every run, however many" internal/agent/repetition.go \
  's = s.replace("if i == maxRepetitionNotes {", "if i == 1000 {", 1)' \
  ./internal/agent 'TestBuildMessages_ATurnIsToldWhatCouldBeOnePattern'

drill "top-level parts are never looked at" internal/domain/geometry/repetition.go \
  's = s.replace("\tout := topLevelRepetition(d)\n", "\tvar out []Repetition\n", 1)' \
  ./internal/domain/geometry 'TestRepetition_FindsARowOfTopLevelPartsAndTheRepeatThatWritesThemOut'

drill "top-level parts are reported after the assemblies" internal/domain/geometry/repetition.go \
  's = s.replace("\tout := topLevelRepetition(d)\n\tfor _, a := range d.Assemblies {\n\t\tout = append(out, assemblyRepetition(a)...)\n\t}\n", "\tvar out []Repetition\n\tfor _, a := range d.Assemblies {\n\t\tout = append(out, assemblyRepetition(a)...)\n\t}\n\tout = append(out, topLevelRepetition(d)...)\n", 1)' \
  ./internal/domain/geometry 'TestRepetition_TopLevelPartsComeFirstInWrittenOrder'

drill "top-level groups are read in map order" internal/domain/geometry/repetition.go \
  'a = "\tfor _, g := range groups {"; i = s.index(a); j = s.index(a, i + 1); s = s[:j] + "\tfor _, g := range byKey {" + s[j + len(a):]' \
  ./internal/domain/geometry 'TestRepetition_TopLevelPartsComeFirstInWrittenOrder'

drill "a part of another size counts as the same part" internal/domain/geometry/repetition.go \
  's = s.replace("\tq.Position, q.Rotation = nil, nil\n", "\tq.Position, q.Rotation, q.Size = nil, nil, nil\n", 1)' \
  ./internal/domain/geometry 'TestRepetition_TopLevelPartsThatAreNotOneRunAreNotFlagged'

drill "a feature's tool is folded into a repeat" internal/domain/geometry/repetition.go \
  's = s.replace("\t\t\tnamed[w] = true\n", "\t\t\t_ = w\n", 1)' \
  ./internal/domain/geometry 'TestRepetition_TopLevelPartsThatAreNotOneRunAreNotFlagged'

drill "the offered repeat's turn is not checked" internal/domain/geometry/repetition.go \
  's = s.replace("if !run.unturned && !sameMatrix(s.m, m) {", "if false && !sameMatrix(s.m, m) {", 1)' \
  ./internal/domain/geometry 'TestRepetition_FindsARingOfTopLevelParts'

drill "the contract never teaches density" internal/agent/converse.go \
  's = s.replace("geometry.FinishGuide() + \".\\n\" + densityContract + ", "geometry.FinishGuide() + \".\\n\" + ", 1)' \
  ./internal/agent 'TestTheContractTeachesDensityAsTheValidatorReadsIt'

drill "the contract's material shows no density" internal/agent/converse.go \
  's = s.replace("\"finish\": \"metal\", \"density\": 2700,", "\"finish\": \"metal\",", 1)' \
  ./internal/agent 'TestTheContractTeachesDensityAsTheValidatorReadsIt'

drill "the contract states a ceiling the validator does not enforce" internal/agent/converse.go \
  's = s.replace("`, geometry.MaxDensity)", "`, 50000)", 1)' \
  ./internal/agent 'TestTheContractTeachesDensityAsTheValidatorReadsIt'

drill "the validator enforces a ceiling the contract does not state" internal/domain/geometry/assembly.go \
  's = s.replace("const maxDensity = MaxDensity", "const maxDensity = 50000", 1)' \
  ./internal/agent 'TestTheContractTeachesDensityAsTheValidatorReadsIt'

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
