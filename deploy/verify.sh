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

echo "=== 9. Blob store round-trips from inside both pods ==="
# Proves, per pod, what nothing else here does: the ConfigMap names the bucket,
# the pod gets the instance role's credentials through IMDS, the role's
# ForgeGeometryBlobs policy allows the put, head and get under blobs/, and the
# pod may reach S3 on 443. Each of these fails silently at boot — the store
# makes no request until it is used — so a pod can be Running without any of it.
#
# BOTH pods, because they take different paths: forged is on the host network,
# where no NetworkPolicy applies; forge-worker is behind 32-worker-egress.yaml
# and the IMDS hop limit. A pass from forged says nothing about the worker.
#
# The bytes are fixed, so running this again stores nothing; the role has no
# delete, and a check that left an object behind per run would do so forever.
# ‼️ When ONLY forge-worker fails this check, suspect its egress policy before
# the bucket or the role: 32-worker-egress.yaml rule 4 excludes the private ranges,
# so S3 reached through a VPC interface endpoint (whose private DNS resolves into
# 172.31/16) is blocked there; and rule 3 cannot beat an instance metadata hop limit
# of 1, which deploy/bootstrap-s3.sh reports. forged runs on the host network, where
# neither applies, which is why a worker-only failure points at the policy rather
# than at S3.
for d in forged forge-worker; do
  POD=$($K -n forge get pod -l app.kubernetes.io/name=$d -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
  if [ -z "$POD" ]; then bad "no $d pod"; continue; fi
  OUT=$($K -n forge exec "$POD" -c "$d" -- /usr/local/bin/forgectl blob check 2>&1 | tr -d '\r')
  R=$(echo "$OUT" | grep -o 'BLOB-ROUNDTRIP-OK [0-9a-f]\{64\}' | tail -1)
  WHY=$(echo "$OUT" | grep -m1 'error :' || echo "$OUT" | tail -1)
  if [ -n "$R" ]; then ok "$d: $R"; else
    if [ "$d" = "forge-worker" ]; then
      bad "$d: no blob round trip: $WHY (worker only: check 32-worker-egress.yaml rule 4 and the IMDS hop limit)"
    else
      bad "$d: no blob round trip: $WHY"
    fi
  fi
done

echo
[ $FAIL -eq 0 ] && echo "ALL CHECKS PASSED" || echo "SOME CHECKS FAILED"
exit $FAIL
