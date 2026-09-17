#!/usr/bin/env bash
# a project holding the 1M fleet as its newest variant, for the browse check
W=/c/Users/damon/Downloads/agents/J.A.R.V.I.S.-agent/.claude/worktrees/agent-a0ac1f5e140d6760a
. $W/docs/spikes/2026-09-17-workbench-viewport/harness/env.sh
V=/c/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/vplarge
cd $W || exit 1
export FORGE_SESSION_SECRET="$(cat $L/secret.txt)"
timeout 120 $L/bin/mintsession.exe -email vplarge-owner@forge.local -project "large designs check" -out $V/fleet-owner.json || exit 1
OWNER=$(sed -n 's/.*"user_id": "\(.*\)".*/\1/p' $V/fleet-owner.json)
PROJECT=$(sed -n 's/.*"project_id": "\(.*\)".*/\1/p' $V/fleet-owner.json)
FORGE_GEOMETRY_MAX_OCCURRENCES=1100000 timeout 600 go run docs/spikes/2026-09-17-workbench-viewport/harness/store.go -user $OWNER -project $PROJECT -design $L/fleet1m.json 2>&1 | grep -v level=INFO
