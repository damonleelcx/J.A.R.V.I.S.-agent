#!/usr/bin/env bash
# Run 4: V4 vision per sub-assembly on kept cars. The key is loaded into THIS shell only and never printed.
set -uo pipefail
S=/c/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad
L=$S/lv
cd /c/Users/damon/Downloads/agents/J.A.R.V.I.S.-agent/.claude/worktrees/agent-a325f3417a864b26b || exit 1
set -a; . /c/Users/damon/Downloads/agents/jarvis-a4/.env; set +a
export GOWORK=off PYTHONUTF8=1 PATH=$S/shim:$PATH
export FORGE_LLM_BASE_URL=https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1
export FORGE_LIVE_LLM_TESTS=1 FORGE_MEASURE_TOKEN_BUDGET=${CAP:-25000}
export FORGE_CAD_PYTHON=C:/Users/damon/Downloads/agents/jarvis-k2b/.cadvenv/Scripts/python.exe
export FORGE_LIVE_LOOK_DOCS="$(cygpath -w $L/car-run1.json),$(cygpath -w $L/car-merged.json)"
date '+start %H:%M:%S'
timeout 1500 go test -count=1 -v -timeout 25m -run 'TestLiveLookAtKeptCars$' ./internal/agent/
echo "exit=$?"
date '+end %H:%M:%S'
