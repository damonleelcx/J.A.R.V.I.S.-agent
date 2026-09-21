#!/bin/bash
S=/c/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/kv
PY=/c/Users/damon/Downloads/agents/jarvis-k2b/.cadvenv/Scripts/python.exe
timeout 1500 $PY $S/holes.py 1000 sequential,multi 2 > $S/holes_1000.jsonl 2>&1
timeout 1200 $PY $S/holes.py 100,1000,5000 multi 3 > $S/holes_multi.jsonl 2>&1
echo done > $S/holes_done
