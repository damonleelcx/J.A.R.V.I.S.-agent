#!/usr/bin/env bash
# Progress and the spend watchdog for this exercise.
# Total spend = every call the recording proxy saw (provider-reported usage, forged AND the
# worker, including calls a stopping worker abandoned) + calls made before the proxy existed
# (EXTRA: the 82-token probe and an ~11,300-token estimate for the failed first turn, whose
# usage nothing recorded). At WATCHDOG it creates every stop-<n> file: a graceful stop.
L=C:/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/livegoal
USER_ID=$(python -c "import json;print(json.load(open('$L/signin.json'))['user'])")
WATCHDOG=${WATCHDOG:-88000}
EXTRA=${EXTRA:-11382}
psq() { docker exec forge-pg psql -U forge -d forge -Atc "$1"; }
last=""
while :; do
  goals=$(psq "select string_agg(right(id,6)||':'||status||':'||tokens_spent||'/'||coalesce(max_tokens::text,'-'), ' ' order by created_at) from forge_goals where created_by = '$USER_ID'")
  tasks=$(psq "select string_agg(right(t.idempotency_key,2)||':'||t.status||':'||t.attempt_count||':'||coalesce(right(t.lease_owner,4),'-'), ' ' order by t.idempotency_key) from forge_tasks t join forge_goals g on g.id=t.goal_id where g.created_by = '$USER_ID' and g.status in ('active','draft')")
  proxied=$(python -c "
import json
t=0
for l in open(r'$L/rec/calls.jsonl'):
    u=json.loads(l).get('usage') or {}
    t+=u.get('total_tokens') or 0
print(t)" 2>/dev/null)
  total=$(( ${proxied:-0} + EXTRA ))
  line="goals=[$goals] proxied=$proxied total=$total tasks=[$tasks]"
  if [ "$line" != "$last" ]; then echo "$(date '+%H:%M:%S') $line" >> $L/progress.log; last=$line; fi
  if [ "$total" -ge "$WATCHDOG" ]; then
    echo "$(date '+%H:%M:%S') WATCHDOG total=$total >= $WATCHDOG: stopping workers" >> $L/progress.log
    for n in 1 2 3 4; do touch $L/stop-$n; done
    exit 0
  fi
  sleep 2
done
