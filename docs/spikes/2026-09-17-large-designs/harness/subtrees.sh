#!/usr/bin/env bash
# subtree mesh requests on the 1M fleet, base (18350) and this branch (18352) interleaved
S=/c/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad
TOK=$(sed -n 's/.*"token": "\(.*\)".*/\1/p' $S/vpcheck/owner.json)
V=ver_01M2QP3C174F9PXNK7VXXH61NZ
for i in $(seq 1 30); do curl -s -o /dev/null http://127.0.0.1:18352/healthz && break; sleep 1; done
load() { powershell -NoProfile -Command "(Get-CimInstance Win32_Processor | Measure-Object -Property LoadPercentage -Average).Average" 2>/dev/null | tr -d '\r'; }
echo "cpu load at start: $(load)%"
for i in 1 2 3; do
  for path in car-20/seam-3/rivet-17 car-7/seam-12 car-9; do
    for port in 18350 18352; do
      curl -s -o $S/vplarge/sub-$port.json -w "round $i $path port $port %{http_code} %{time_total}s %{size_download}B\n" -H "Authorization: Bearer $TOK" "http://127.0.0.1:$port/v1/geometry/$V/mesh?subtree=$path"
    done
    cmp -s $S/vplarge/sub-18350.json $S/vplarge/sub-18352.json && echo "  same body" || echo "  bodies differ"
  done
done
echo "cpu load at end: $(load)%"
