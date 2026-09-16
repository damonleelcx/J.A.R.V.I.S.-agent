# Card check: forged + worker against the stand-in model. No provider, no key.
L=C:/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/cardcheck
export GOWORK=off FORGE_ENV=development
export FORGE_DATABASE_URL='postgres://forge:forge_dev_pw@127.0.0.1:55840/forge?sslmode=disable'
export FORGE_LLM_BASE_URL=http://127.0.0.1:18223/v1 FORGE_LLM_API_KEY=stand-in-not-a-key
export FORGE_LLM_CONVERSE_MODEL=stand-in-converse FORGE_LLM_VISION_MODEL=stand-in-vision FORGE_LLM_PLANNER_MODEL=stand-in-converse
export FORGE_DATA_BOUNDARY=no_training FORGE_LOG_LEVEL=debug FORGE_MAX_TOKENS_PER_GOAL=100000
export FORGE_CAD_PYTHON=C:/Users/damon/Downloads/agents/jarvis-k2b/.cadvenv/Scripts/python.exe
export FORGE_CAD_POOL=1 FORGE_ALLOW_SCRIPTS=false
