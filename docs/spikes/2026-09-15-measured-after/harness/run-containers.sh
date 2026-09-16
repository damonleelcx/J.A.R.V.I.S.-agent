#!/usr/bin/env bash
# Every container cell of stage 3, in one order, on a machine shared with other work.
#
# forged  (deploy/k8s/30-forged.yaml: 1 CPU, 1Gi): pool 1 and 2 x 2 and 4 concurrent mesh
#         requests of 8,192 parts. Two runs a cell, interleaved pool 1 / pool 2 so a drift
#         in machine load falls on both sides of the comparison rather than one.
# worker  (deploy/k8s/31-worker.yaml: 1 CPU, 2Gi): pool 1 and 2, two concurrent build
#         goals of 4,000 occurrences, at 1 CPU and at 2 CPUs. #110 measured 1 CPU only and
#         named the worker's CPU limit at 2 as what would revisit its decision.
#
#   BIN=... DOCS=... OUT=... bash harness/run-containers.sh [forged|worker|all]
set -uo pipefail

HARNESS="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PY="${PY:-C:/Users/damon/Downloads/agents/jarvis-k2b/.cadvenv/Scripts/python.exe}"
BIN="${BIN:?set BIN}"
DOCS="${DOCS:?set DOCS}"
OUT="${OUT:?set OUT}"
WHICH="${1:-all}"
mkdir -p "$OUT"

forged_cells() {
  # Interleaved: run r of pool 1 then pool 2, at each concurrency.
  for conc in 2 4; do
    for r in 1 2; do
      for pool in 1 2; do
        echo "### forged pool=$pool conc=$conc run=$r  $(date +%H:%M:%S)"
        "$PY" "$HARNESS/mesh_pool_measure.py" --label "f-p${pool}-c${conc}-r${r}" \
          --pool "$pool" --conc "$conc" --design barrel-8192 \
          --bin "$BIN" --docs "$DOCS" --cpus 1 --memory 1g --runs 1 \
          --out "$OUT/forged.jsonl" || echo "CELL FAILED: f-p${pool}-c${conc}-r${r}"
      done
    done
  done
}

worker_cells() {
  for cpus in 1 2; do
    for r in 1 2; do
      for pool in 1 2; do
        echo "### worker pool=$pool cpus=$cpus run=$r  $(date +%H:%M:%S)"
        "$PY" "$HARNESS/pool_measure.py" --label "w-p${pool}-cpu${cpus}-r${r}" \
          --pool "$pool" --conc 2 --size 4000 --copies 2 \
          --bin "$BIN" --cpus "$cpus" --memory 2g \
          --out "$OUT/worker.jsonl" || echo "CELL FAILED: w-p${pool}-cpu${cpus}-r${r}"
      done
    done
  done
}

case "$WHICH" in
  forged) forged_cells ;;
  worker) worker_cells ;;
  all)    forged_cells; worker_cells ;;
  *) echo "usage: run-containers.sh [forged|worker|all]"; exit 2 ;;
esac

echo "=== done $(date +%H:%M:%S) ==="
docker rm -f forge-meas-stub >/dev/null 2>&1
docker ps -a --format '{{.Names}}' | grep '^forge-meas' || echo "no forge-meas-* containers left"
