#!/usr/bin/env bash
# The prism yard's check at each size, each run under a hard timeout.
# Usage: yard.sh <dir holding yard-N.json> <sidecar> <results.jsonl> <sides, e.g. 1,3,6,10>
set -u
DIR=$1; S=$2; OUT=$3; SIDES=$4
PY=/c/Users/damon/Downloads/agents/jarvis-k2b/.cadvenv/Scripts/python.exe
HERE=$(cd "$(dirname "$0")" && pwd)
cd "$DIR" || exit 1
for side in ${SIDES//,/ }; do
  extra=""
  if [ "$side" = 1 ]; then extra="--noslide"; fi
  echo "== yard side $side"
  timeout 3600 "$PY" "$HERE/yard_check.py" "$S" "yard-$side.json" "$OUT" "$side" $extra > "yard-$side.out" 2>&1 \
    || echo "yard_check exited $? (see yard-$side.out)"
done
echo "== yard done"
