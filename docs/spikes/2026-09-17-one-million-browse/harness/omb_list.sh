#!/usr/bin/env bash
# usage: omb_list.sh <port>   the fleet project's variants, and whether forged migrated
S=/c/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad
for i in $(seq 1 60); do curl -s -o /dev/null -w "%{http_code}" http://127.0.0.1:$1/healthz | grep -q 200 && break; sleep 1; done
grep -i "migration" $S/vplarge/omb-forged-$1.log | tail -2 | cut -c1-250
TOK=$(sed -n 's/.*"token": "\(.*\)".*/\1/p' $S/vplarge/fleet-owner.json)
curl -s -H "Authorization: Bearer $TOK" "http://127.0.0.1:$1/v1/geometry?project_id=prj_01M2QSNWR8BFH7XZPVC44NA0M7" |
  python -c "import json,sys; [print(v['version_id'], v['name'], v['occurrences']) for v in json.load(sys.stdin)['variants']]"
