#!/bin/bash
# Interleaved: main, branch without normals, branch with normals; fresh process each.
K=/c/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/kv
PY=/c/Users/damon/Downloads/agents/jarvis-k2b/.cadvenv/Scripts/python.exe
BR=$K/sidecar_branch.py
OUT=$K/mesh_compare.jsonl
: > $OUT
for round in 1 2 3; do
  for c in copies4096 distinct256; do
    if [ $((round % 2)) -eq 1 ]; then order="main off on"; else order="on off main"; fi
    for v in $order; do
      case $v in
        main) timeout 900 $PY $K/mesh_one.py $K/sidecar_main.py main $c 0 >> $OUT 2>&1 ;;
        off)  timeout 900 $PY $K/mesh_one.py $BR branch-no-normals $c 0 >> $OUT 2>&1 ;;
        on)   timeout 900 $PY $K/mesh_one.py $BR branch-normals $c 1 >> $OUT 2>&1 ;;
      esac
    done
  done
done
echo done >> $OUT
