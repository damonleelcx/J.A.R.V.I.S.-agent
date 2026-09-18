#!/usr/bin/env bash
# usage: omb_get.sh <basePort> <newPort> <versionID> <rounds>
# GET /v1/geometry/{id} alternately on base and new, with CPU load, and whether "measured" agrees.
S=/c/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad
TOK=$(sed -n 's/.*"token": "\(.*\)".*/\1/p' $S/vplarge/fleet-owner.json)
load() { powershell -NoProfile -Command "(Get-CimInstance Win32_Processor | Measure-Object -Property LoadPercentage -Average).Average" 2>/dev/null | tr -d '\r'; }
echo "cpu load at start: $(load)%"
for i in $(seq 1 $4); do
  for port in $1 $2; do
    curl -s -o $S/vplarge/get-$port.json -w "round $i port $port %{http_code} %{time_total}s %{size_download}B\n" \
      -H "Authorization: Bearer $TOK" "http://127.0.0.1:$port/v1/geometry/$3"
  done
  python -c "
import json,sys
a=json.load(open(sys.argv[1]))['variant']; b=json.load(open(sys.argv[2]))['variant']
print('  measured identical:', a['measured']==b['measured'], '| whole variant identical:', a==b, '|', [(m['id'], m['value']) for m in b['measured']])
" $S/vplarge/get-$1.json $S/vplarge/get-$2.json
done
echo "cpu load at end: $(load)%"
