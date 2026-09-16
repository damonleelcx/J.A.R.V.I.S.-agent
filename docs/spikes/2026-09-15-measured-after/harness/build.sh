#!/usr/bin/env bash
# Linux binaries for the container runs, and the designs they serve.
#
# # Why this cross-compiles on the host instead of building in the image
#
# #110's harness/build.sh untarred a `git archive` inside forge-linux-test and ran Go
# there, so no CRLF working tree reached Linux. That is not available here: this image
# carries no Go module cache (`/root/go/pkg/mod` is absent) and no reachable Go proxy, so
# `go mod download` inside it cannot resolve this module's dependencies.
#
# Cross-compiling on the Windows host reaches the same end by the same reasoning. The
# CRLF tree is never bind-mounted; only $BIN, a directory of finished ELF binaries, is
# (`-v $BIN:/opt/forge:ro`), exactly as #110 mounted its own. Go itself is indifferent to
# CRLF in source, and no shell script crosses into the container.
#
# Two binaries are built from an edit that is deliberately NOT committed, the same way
# #110 built `forged-lifted`:
#
#   forged-lifted -- maxDrawnParts 4096 -> 100_000. On this branch (scale/last-hot-spots)
#     `maxDrawnParts` is 4,096 and there is no `maxBuiltParts`: #110's 8,192 view ceiling
#     is on kernel/ceiling-on-linux, which is not merged here. Without the lift an
#     8,192-part mesh request is refused by the storage door before it reaches the
#     kernel, so the pool could not be measured at the size asked for at all.
#
# The edit is applied to a copy-protected file and the tree is verified byte-identical
# afterwards, so a failed run cannot leave the lift in the working tree.
#
#   BIN=/path/to/bin DOCS=/path/to/docs bash harness/build.sh
set -uo pipefail

AFTER="${AFTER:-C:/Users/damon/Downloads/agents/jarvis-after}"   # scale/measured-after (#121's code)
A1="${A1:-C:/Users/damon/Downloads/agents/jarvis-a1b}"           # agent/build-goal-entry: the worker with a kernel
BIN="${BIN:?set BIN}"
DOCS="${DOCS:?set DOCS}"
HARNESS="$AFTER/docs/spikes/2026-09-15-measured-after/harness"

mkdir -p "$BIN" "$DOCS"
export GOWORK=off CGO_ENABLED=0

echo "=== designs (built and run natively, so they execute here) ==="
( cd "$AFTER" && unset GOOS GOARCH && go run "$HARNESS/barrel.go" -out "$DOCS" ) || exit 1

export GOOS=linux GOARCH=amd64

echo "=== forged, shipped and lifted, from $(git -C "$AFTER" rev-parse --short HEAD) ==="
LIMITS="$AFTER/internal/domain/geometry/limits.go"
cp "$LIMITS" "$BIN/limits.go.orig" || exit 1
restore() { cp "$BIN/limits.go.orig" "$LIMITS"; }
trap restore EXIT
( cd "$AFTER" && go build -trimpath -o "$BIN/forged" ./cmd/forged ) || exit 1
grep -q '^const maxDrawnParts = 4096$' "$LIMITS" || { echo "anchor moved in limits.go"; exit 1; }
sed -i 's/^const maxDrawnParts = 4096$/const maxDrawnParts = 100_000/' "$LIMITS" || exit 1
( cd "$AFTER" && go build -trimpath -o "$BIN/forged-lifted" ./cmd/forged ) || exit 1
restore
trap - EXIT
cmp "$BIN/limits.go.orig" "$LIMITS" || { echo "limits.go was NOT restored"; exit 1; }
rm -f "$BIN/limits.go.orig"
echo "limits.go restored byte-identical"

echo "=== store (geometry.Service.Save; FORGE has no endpoint that stores client geometry) ==="
( cd "$AFTER" && go build -trimpath -o "$BIN/store" "$HARNESS/store.go" ) || exit 1

echo "=== forge-worker and forgectl from $(git -C "$A1" rev-parse --short HEAD) (A1: the worker holds a kernel) ==="
( cd "$A1" && go build -trimpath -o "$BIN/forge-worker-a1" ./cmd/forge-worker ) || exit 1
( cd "$A1" && go build -trimpath -o "$BIN/forgectl-a1" ./cmd/forgectl ) || exit 1

echo "=== #90's planning stub (its own module; stdlib only) ==="
STUB="$BIN/stubsrc"
rm -rf "$STUB" && mkdir -p "$STUB" && cp "$HARNESS/stub.go" "$STUB/" || exit 1
( cd "$STUB" && go mod init stub >/dev/null 2>&1; go build -trimpath -o "$BIN/stub" . ) || exit 1
rm -rf "$STUB"

ls -la "$BIN" "$DOCS"
file "$BIN"/forged-lifted "$BIN"/forge-worker-a1 2>/dev/null | head -4
