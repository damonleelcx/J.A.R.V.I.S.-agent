#!/bin/bash
# Post-deploy verification. Checks the things that fail SILENTLY on this node,
# not just "is the pod Running".
set -uo pipefail
K="k3s kubectl"
FAIL=0
ok(){ echo "  PASS  $1"; }
bad(){ echo "  FAIL  $1"; FAIL=1; }

echo "=== 1. ExternalSecret synced ==="
S=$($K -n forge get externalsecret forge -o jsonpath='{.status.conditions[0].reason}' 2>/dev/null)
[ "$S" = "SecretSynced" ] && ok "secret synced" || bad "externalsecret reason=$S (a property absent from the store fails the WHOLE secret)"

echo "=== 2. Secret has every key the pods read ==="
for k in FORGE_DATABASE_URL FORGE_LLM_API_KEY FORGE_SESSION_SECRET FORGE_SMTP_USER FORGE_SMTP_PASSWORD; do
  $K -n forge get secret forge -o jsonpath="{.data.$k}" 2>/dev/null | grep -q . && ok "$k present" || bad "$k MISSING"
done

echo "=== 3. Pods ==="
$K -n forge get pods -o wide 2>&1 | tail -n +1
for d in forged forge-worker; do
  R=$($K -n forge get deploy $d -o jsonpath='{.status.readyReplicas}' 2>/dev/null)
  [ "$R" = "1" ] && ok "$d ready" || bad "$d readyReplicas=$R"
done

echo "=== 4. Migrations actually ran (schema present, not just exit 0) ==="
N=$($K -n heros exec postgres-0 -- psql -U heros -d forge -tAc \
    "select count(*) from information_schema.tables where table_schema='public'" 2>/dev/null)
[ "${N:-0}" -gt 10 ] && ok "forge schema has $N tables" || bad "forge schema has only ${N:-0} tables"

echo "=== 5. TLS certificate issued ==="
C=$($K -n forge get certificate forge-tls -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null)
[ "$C" = "True" ] && ok "forge-tls Ready" || bad "forge-tls Ready=$C (if stuck: delete the CERTIFICATE, not the Order)"

echo "=== 6. Health endpoints over public TLS ==="
for p in healthz readyz; do
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 15 https://forge.heros-agent.space/$p)
  [ "$code" = "200" ] && ok "/$p -> 200" || bad "/$p -> $code"
done

echo "=== 7. CAD kernel live in the running pod ==="
POD=$($K -n forge get pod -l app.kubernetes.io/name=forge-worker -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
if [ -n "$POD" ]; then
  V=$($K -n forge exec "$POD" -- /opt/cad/venv/bin/python -c \
      'from build123d import *
with BuildPart() as p: Box(20,10,5)
print(round(p.part.volume,1))' 2>/dev/null | tr -d '\r')
  [ "$V" = "1000.0" ] && ok "kernel built a solid (volume $V)" || bad "kernel volume=$V (expected 1000.0)"
else bad "no forge-worker pod"; fi

echo "=== 8. SMTP reachable through the NetworkPolicy, with the right SAN ==="
# Proves three things at once: the netpol permits 587, hostAliases resolves the
# name, and the relay's cert validates for it. A failure in any of these makes
# mail silently never deliver.
POD=$($K -n forge get pod -l app.kubernetes.io/name=forged -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
if [ -n "$POD" ]; then
  R=$($K -n forge exec "$POD" -- /opt/cad/venv/bin/python -c '
import smtplib,ssl
s=smtplib.SMTP("mail.heros-agent.space",587,timeout=12)
s.starttls(context=ssl.create_default_context())
print("STARTTLS-OK")
s.quit()' 2>&1 | tr -d '\r' | tail -1)
  [ "$R" = "STARTTLS-OK" ] && ok "SMTP STARTTLS verified for mail.heros-agent.space" || bad "SMTP: $R"
else bad "no forged pod"; fi

echo
[ $FAIL -eq 0 ] && echo "ALL CHECKS PASSED" || echo "SOME CHECKS FAILED"
exit $FAIL
