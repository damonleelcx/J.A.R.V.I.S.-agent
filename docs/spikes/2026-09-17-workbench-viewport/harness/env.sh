# Workbench check: forged + forge-worker, local Postgres and MinIO (make db-up, make blob-up).
# No model is asked anything: the endpoint points at a port nothing listens on, and the
# "key" is a placeholder. Blob credentials are the Makefile's development ones.
L=C:/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/vpcheck
export GOWORK=off FORGE_ENV=development
export FORGE_DATABASE_URL='postgres://forge:forge_dev_pw@127.0.0.1:55840/forge?sslmode=disable'
export FORGE_LLM_BASE_URL=http://127.0.0.1:18339/v1 FORGE_LLM_API_KEY=not-a-key-nothing-listens
export FORGE_DATA_BOUNDARY=no_training FORGE_LOG_LEVEL=info
export FORGE_CAD_PYTHON=C:/Users/damon/Downloads/agents/jarvis-k2b/.cadvenv/Scripts/python.exe
export FORGE_CAD_POOL=1 FORGE_ALLOW_SCRIPTS=false
export FORGE_BLOB_BUCKET=forge-vpcheck-exports FORGE_BLOB_REGION=us-east-1 FORGE_BLOB_ENDPOINT=http://127.0.0.1:55841
export AWS_ACCESS_KEY_ID=forge AWS_SECRET_ACCESS_KEY=forge_dev_minio
