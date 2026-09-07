#!/bin/bash
# Three changes FORGE needs inside the `heros` namespace, which belongs to other
# products. Surgical patches, not applies: applying whole objects here is how
# the jobs@ mail account was once silently deleted.
#
# Each is idempotent and each fails SILENTLY if omitted, which is why they are
# scripted rather than left as manual steps:
#
#   1. postgres NetworkPolicy is default-deny. Without an entry, FORGE cannot
#      reach the database at all.
#   2. mail NetworkPolicy names each client namespace on port 587. Without an
#      entry, auth mail is refused at the network layer and never delivers —
#      the product does not stop, it just never sends.
#   3. The backup CronJob dumps only the databases named in BACKUP_DATABASES.
#      A database missing from that list is unbacked-up while the job still
#      reports OK.
set -euo pipefail
K="k3s kubectl"
DRY=""
[ "${1:-}" = "--dry-run" ] && DRY="--dry-run=server" && echo "== DRY RUN =="

FORGE_PEER='{"namespaceSelector":{"matchLabels":{"kubernetes.io/metadata.name":"forge"}},"podSelector":{"matchLabels":{"app.kubernetes.io/part-of":"forge"}}}'

# --- 1. postgres ingress ----------------------------------------------------
if $K -n heros get networkpolicy postgres -o json | grep -q '"kubernetes.io/metadata.name":"forge"'; then
  echo "postgres netpol: already allows forge"
else
  echo "postgres netpol: adding forge"
  $K -n heros patch networkpolicy postgres --type=json $DRY \
    -p "[{\"op\":\"add\",\"path\":\"/spec/ingress/0/from/-\",\"value\":$FORGE_PEER}]"
fi

# --- 2. mail ingress on 587 -------------------------------------------------
if $K -n heros get networkpolicy mail -o json | grep -q '"kubernetes.io/metadata.name":"forge"'; then
  echo "mail netpol: already allows forge"
else
  echo "mail netpol: adding forge on 587"
  $K -n heros patch networkpolicy mail --type=json $DRY \
    -p "[{\"op\":\"add\",\"path\":\"/spec/ingress/-\",\"value\":{\"from\":[$FORGE_PEER],\"ports\":[{\"port\":587,\"protocol\":\"TCP\"}]}}]"
fi

# --- 3. backup coverage -----------------------------------------------------
CUR=$($K -n heros get cronjob postgres-backup \
  -o jsonpath='{.spec.jobTemplate.spec.template.spec.containers[0].env[?(@.name=="BACKUP_DATABASES")].value}')
echo "backup databases currently: [$CUR]"
if echo " $CUR " | grep -q " forge "; then
  echo "backup: forge already covered"
else
  IDX=$($K -n heros get cronjob postgres-backup -o json \
    | python3 -c "import json,sys;e=json.load(sys.stdin)['spec']['jobTemplate']['spec']['template']['spec']['containers'][0]['env'];print([i for i,v in enumerate(e) if v['name']=='BACKUP_DATABASES'][0])")
  echo "backup: adding forge (env index $IDX)"
  $K -n heros patch cronjob postgres-backup --type=json $DRY \
    -p "[{\"op\":\"replace\",\"path\":\"/spec/jobTemplate/spec/template/spec/containers/0/env/$IDX/value\",\"value\":\"$CUR forge\"}]"
fi
echo "done"
