#!/usr/bin/env bash
# The graceful-stop drill for a started build goal:
#   1. worker 1 (already running under ctrlrun) builds step 1 and keeps it;
#   2. STOP_AFTER seconds into step 2, a real CTRL_BREAK_EVENT is sent (touch stop-1);
#   3. step 2's row is sampled every 250 ms until it is ready with no lease owner;
#   4. worker 2 starts RESTART_AFTER seconds after worker 1 exits, and the driver waits for
#      step 2 to be claimed again.
#   stop-resume.sh <goal id>
L=C:/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/livegoal
GOAL=$1
STOP_AFTER=${STOP_AFTER:-20}
RESTART_AFTER=${RESTART_AFTER:-5}
psq() { docker exec forge-pg psql -U forge -d forge -Atc "$1"; }
ms() { date '+%H:%M:%S.%3N'; }
log() { echo "$(ms) $*" | tee -a $L/drill.log; }

log "drill for $GOAL: stop ${STOP_AFTER}s into step 2, restart ${RESTART_AFTER}s after exit"
# The steps, in order: the second task by creation is step 2.
while :; do
  s=$(psq "select string_agg(status, ',' order by created_at, idempotency_key) from forge_tasks where goal_id='$GOAL'")
  st1=$(echo $s | cut -d, -f1); st2=$(echo $s | cut -d, -f2)
  g=$(psq "select status from forge_goals where id='$GOAL'")
  [ "$g" != "active" ] && { log "goal is $g before the stop; drill not run ($s)"; exit 1; }
  [ -f $L/stop-1 ] && { log "stop-1 already exists (watchdog?); drill not run"; exit 1; }
  if [ "$st1" = "succeeded" ] && [ "$st2" = "running" ]; then
    age=$(psq "select extract(epoch from now() - started_at)::int from forge_tasks where goal_id='$GOAL' order by created_at, idempotency_key offset 1 limit 1")
    [ "${age:-0}" -ge $STOP_AFTER ] && break
  fi
  sleep 1
done
T2=$(psq "select id from forge_tasks where goal_id='$GOAL' order by created_at, idempotency_key offset 1 limit 1")
log "before stop: $(psq "select idempotency_key||' '||status||' attempt='||attempt_count||' owner='||coalesce(lease_owner,'-')||' lease_expires='||coalesce(to_char(lease_expires_at,'HH24:MI:SS'),'-') from forge_tasks where id='$T2'")"
log "spent before stop: $(psq "select tokens_spent from forge_goals where id='$GOAL'")"
touch $L/stop-1
log "STOP sent (stop-1 created; ctrlrun polls every 200 ms)"
for i in $(seq 1 240); do
  row=$(psq "select status||' owner='||coalesce(lease_owner,'-') from forge_tasks where id='$T2'")
  case "$row" in
    "ready owner=-"*) log "step 2 handed back: $row (sample $i)"; break ;;
  esac
  sleep 0.25
done
for i in $(seq 1 160); do grep -q 'child exited' $L/ctrlrun-1.log && break; sleep 0.25; done
log "ctrlrun: $(grep -E 'GenerateConsoleCtrlEvent|child exited' $L/ctrlrun-1.log | tr '\n' ' ')"
log "after stop: $(psq "select idempotency_key||' '||status||' attempt='||attempt_count||' owner='||coalesce(lease_owner,'-') from forge_tasks where id='$T2'")"
log "events since stop: $(psq "select string_agg(kind||'@'||to_char(created_at,'HH24:MI:SS.MS'), ' ' order by created_at) from forge_events where goal_id='$GOAL' and created_at > now() - interval '30 seconds'")"
sleep $RESTART_AFTER
bash $L/${WORKER_SCRIPT:-worker.sh} 2 &
log "worker 2 started"
for i in $(seq 1 240); do
  row=$(psq "select status||' attempt='||attempt_count||' owner='||coalesce(lease_owner,'-') from forge_tasks where id='$T2'")
  case "$row" in running*) log "step 2 claimed again: $row (after ${i}x0.5s)"; break ;; esac
  sleep 0.5
done
log "all tasks: $(psq "select string_agg(idempotency_key||':'||status||':'||attempt_count, ' ' order by created_at, idempotency_key) from forge_tasks where goal_id='$GOAL'")"
log "drill done; worker 2 keeps running"
wait
