#!/usr/bin/env bash
# Measure a solid with Zoo's own engine, so the numbers come from the geometry
# rather than from reading the source that produced it.
#
# The figures in this spike's table are read out of KCL text. That proves what
# the model WROTE, not what the geometry IS — and the whole point of preferring a
# CAD kernel is that those can differ. These endpoints close that gap: they take
# a STEP file and return mass properties computed by the kernel.
#
#   ZOO_API_KEY=... ./measure-step.sh part.step [--dry-run]
#
# Density is aluminium 6061 because the reply named 6061. It only affects the mass
# line, and it is stated here rather than hidden in a default.
#
# The unit is kg:m3, not g:cm3. The API accepts exactly two:
#   "unable to parse query string: unknown variant `g:cm3`, expected `lb:ft3` or `kg:m3`"
# Found by running this script, not by reading the schema — UnitDensity's enum
# does not appear in the generated OpenAPI document.

set -euo pipefail
FILE="${1:?usage: measure-step.sh <file.step> [--dry-run]}"
DRY="${2:-}"
API="https://api.zoo.dev"
DENSITY_KG_M3=2700

if [ "$DRY" = "--dry-run" ]; then
  echo "DRY RUN — nothing sent"
  echo "  file    : $FILE ($( [ -f "$FILE" ] && wc -c < "$FILE" || echo '?') bytes)"
  echo "  would POST $API/file/volume?src_format=step&output_unit=cm3"
  echo "  would POST $API/file/surface-area?src_format=step&output_unit=cm2"
  echo "  would POST $API/file/center-of-mass?src_format=step&output_unit=mm"
  echo "  would POST $API/file/mass?src_format=step&material_density=$DENSITY_KG_M3&material_density_unit=kg:m3&output_unit=g"
  exit 0
fi

: "${ZOO_API_KEY:?ZOO_API_KEY is not set}"
[ -f "$FILE" ] || { echo "no such file: $FILE" >&2; exit 1; }

call () {
  local label="$1" path="$2"
  local body
  body=$(curl -sS --max-time 120 -X POST "$API$path" \
    -H "Authorization: Bearer $ZOO_API_KEY" \
    -H "Content-Type: application/octet-stream" \
    --data-binary "@$FILE")
  printf '%-14s %s\n' "$label" "$body"
}

echo "measuring $FILE ($(wc -c < "$FILE") bytes)"
call "volume"      "/file/volume?src_format=step&output_unit=cm3"
call "surface-area" "/file/surface-area?src_format=step&output_unit=cm2"
call "center-of-mass" "/file/center-of-mass?src_format=step&output_unit=mm"
call "mass"        "/file/mass?src_format=step&material_density=$DENSITY_KG_M3&material_density_unit=kg:m3&output_unit=g"
