#!/usr/bin/env bash
# STEP labels and a small-design label, base (18350) vs this branch (18352)
S=/c/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad
TOK=$(sed -n 's/.*"token": "\(.*\)".*/\1/p' $S/vpcheck/owner.json)
for port in 18350 18352; do
  for v in ver_01M2QP7C99C415F0EZMGMAX37E ver_01M2QP3C174F9PXNK7VXXH61NZ ver_01M2QPXAAQB3KEE7CC60M56V83; do
    for route in "export/label?format=step" "export?format=step"; do
      [ "$route" = "export?format=step" ] && [ "$v" = ver_01M2QPXAAQB3KEE7CC60M56V83 ] && continue
      code=$(curl -s -o $S/vplarge/last.json -w "%{http_code}" -H "Authorization: Bearer $TOK" "http://127.0.0.1:$port/v1/geometry/$v/$route")
      echo "$port $v $route -> $code: $(head -c 420 $S/vplarge/last.json | tr -d '\n')"
    done
  done
done
