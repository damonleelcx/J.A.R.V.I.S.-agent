# Common configuration for the live exercise (no secrets here). The model key is
# loaded from jarvis-a4/.env FIRST, so everything exported after it wins.
L=C:/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/livegoal
set -a; . /c/Users/damon/Downloads/agents/jarvis-a4/.env; set +a
export GOWORK=off
export FORGE_ENV=development
export FORGE_DATABASE_URL='postgres://forge:forge_dev_pw@localhost:55840/forge?sslmode=disable'
export FORGE_LLM_BASE_URL=https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1
export FORGE_LLM_CONVERSE_MODEL=qwen3.7-plus FORGE_LLM_VISION_MODEL=qwen3.8-max FORGE_LLM_PLANNER_MODEL=qwen3.8-max
export FORGE_DATA_BOUNDARY=no_training
export FORGE_LOG_LEVEL=debug
# The whole exercise is capped at 100k tokens; a goal may not exceed it either,
# including the planning call charged before the row's max_tokens can be set.
export FORGE_MAX_TOKENS_PER_GOAL=100000
export FORGE_CAD_PYTHON=C:/Users/damon/Downloads/agents/jarvis-k2b/.cadvenv/Scripts/python.exe
export FORGE_CAD_POOL=1 FORGE_ALLOW_SCRIPTS=false
