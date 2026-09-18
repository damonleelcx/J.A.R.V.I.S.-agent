# Shared environment for the local forged / forge-worker of run 3 (sourced, not run).
# The model key is NOT loaded here; the scripts that call the model load it themselves.
S=/c/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad
L=$S/lv
export GOWORK=off PYTHONUTF8=1 PATH=$S/shim:$PATH
export FORGE_ENV=development
export FORGE_DATABASE_URL='postgres://forge:forge_dev_pw@localhost:55840/forge?sslmode=disable&search_path=forge_liveverify'
export FORGE_LOG_FORMAT=json FORGE_LOG_LEVEL=debug
export FORGE_LLM_BASE_URL=https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1
export FORGE_LLM_CONVERSE_MODEL=qwen3.7-plus FORGE_LLM_VISION_MODEL=qwen3.8-max FORGE_LLM_PLANNER_MODEL=qwen3.8-max
export FORGE_DATA_BOUNDARY=no_training
export FORGE_MAX_TOKENS_PER_GOAL=70000
export FORGE_CAD_PYTHON=C:/Users/damon/Downloads/agents/jarvis-k2b/.cadvenv/Scripts/python.exe
export FORGE_CAD_POOL=1 FORGE_ALLOW_SCRIPTS=false
export FORGE_BLOB_BUCKET=forge-unverified-exports FORGE_BLOB_REGION=us-east-1 FORGE_BLOB_ENDPOINT=http://localhost:55841
export AWS_ACCESS_KEY_ID=forge AWS_SECRET_ACCESS_KEY=forge_dev_minio
export FORGE_HTTP_ADDR=127.0.0.1:18480
