#!/usr/bin/env bash
# Before/after, interleaved B A B A at each size, each run one bounded measure_bounded.py
# invocation (hard cap, RSS and host-memory watchdogs). #146's interleave.sh with this
# directory's harness, which hashes every answer.
# Usage: interleave.sh <dir with barrel-N.json> <before sidecar> <after sidecar> <bays list> <modes> <results file> [reps]
set -u
DIR=$1; BEFORE=$2; AFTER=$3; BAYS=$4; MODES=$5; OUT=$6; REPS=${7:-2}
PY=/c/Users/damon/Downloads/agents/jarvis-k2b/.cadvenv/Scripts/python.exe
HERE=$(cd "$(dirname "$0")" && pwd)
cd "$DIR" || exit 1
for b in ${BAYS//,/ }; do
  for rep in $(seq 1 "$REPS"); do
    for side in before after; do
      if [ "$side" = before ]; then S=$BEFORE; else S=$AFTER; fi
      echo "== bays $b rep $rep $side ($S)"
      timeout 4000 "$PY" "$HERE/measure_bounded.py" --dir . --sidecar "$S" --bays "$b" --modes "$MODES" \
        --repeat 1 --cap 3600 --results "$OUT" || echo "measure_bounded exited $?"
    done
  done
done
echo "== interleave done"
