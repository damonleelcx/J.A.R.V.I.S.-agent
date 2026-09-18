#!/usr/bin/env bash
. /c/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/lv/env.sh
docker exec forge-pg psql -U forge -d forge -c "drop schema if exists forge_liveverify cascade" -c "create schema forge_liveverify"
timeout 300 $L/bin/forgectl.exe migrate
echo "migrate exit=$?"
