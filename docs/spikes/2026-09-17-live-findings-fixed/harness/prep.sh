#!/usr/bin/env bash
. /c/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/lv2/env.sh
W=/c/Users/damon/Downloads/agents/J.A.R.V.I.S.-agent/.claude/worktrees/agent-a8a11870f431a16f2
cd $W || exit 1
mkdir -p $L/bin
for c in forged forge-worker forgectl; do timeout 900 go build -o $L/bin/$c.exe ./cmd/$c || { echo "build $c failed"; exit 1; }; done
ls -la $L/bin
docker exec forge-pg psql -U forge -d forge -c "drop schema if exists forge_livefix cascade" -c "create schema forge_livefix"
timeout 300 $L/bin/forgectl.exe migrate; echo "migrate exit=$?"
timeout 600 go run docs/spikes/2026-09-17-unverified-paths/harness/principals/main.go -out $L/principals.json; echo "principals exit=$?"
