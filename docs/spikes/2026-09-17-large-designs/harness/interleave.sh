#!/usr/bin/env bash
# usage: interleave.sh <basePort> <newPort> <rounds>   GET /v1/geometry?project_id= alternately, with CPU load
S=/c/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad
TOK=$(sed -n 's/.*"token": "\(.*\)".*/\1/p' $S/vpcheck/owner.json)
U="/v1/geometry?project_id=prj_01M2QP0502YMCW1E5GY7KZGSA9"
load() { powershell -NoProfile -Command "(Get-CimInstance Win32_Processor | Measure-Object -Property LoadPercentage -Average).Average" 2>/dev/null | tr -d '\r'; }
echo "cpu load at start: $(load)%"
for i in $(seq 1 $3); do
  for port in $1 $2; do
    curl -s -o /dev/null -w "round $i port $port %{http_code} %{time_total}s %{size_download}B\n" -H "Authorization: Bearer $TOK" "http://127.0.0.1:$port$U"
  done
done
echo "cpu load at end: $(load)%"
