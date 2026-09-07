#!/bin/bash
# Let the node's own IP reach postgres and the mail relay.
#
# forged runs with hostNetwork so it can bind the WebRTC UDP range, and a
# hostNetwork pod has NO pod IP: its traffic arrives from the NODE. The existing
# rules select on namespaceSelector + podSelector, which a hostNetwork pod cannot
# satisfy — so without this, switching forged to hostNetwork takes its database
# and its mail away, and the symptom is a pod that starts and then cannot do
# anything.
#
# ⚠️ THIS IS WIDER THAN A POD RULE, and unavoidably so. An ipBlock for the node
# admits ANY hostNetwork workload on this node, because at the network layer
# they are indistinguishable. That is the price of hostNetwork and it is worth
# stating plainly rather than discovering later.
#
# Run BEFORE deploying the hostNetwork change; it is additive and idempotent, so
# it is safe to apply while the old pod is still running.
set -euo pipefail
K="k3s kubectl"
DRY=""
[ "${1:-}" = "--dry-run" ] && DRY="--dry-run=server" && echo "== DRY RUN =="

NODE_IP=$($K get node -o jsonpath='{.items[0].status.addresses[?(@.type=="InternalIP")].address}')
[ -z "$NODE_IP" ] && { echo "FATAL: could not read the node IP"; exit 1; }
echo "node ip: $NODE_IP"
PEER="{\"ipBlock\":{\"cidr\":\"$NODE_IP/32\"}}"

if $K -n heros get networkpolicy postgres -o json | grep -q "$NODE_IP/32"; then
  echo "postgres netpol: already admits the node"
else
  echo "postgres netpol: admitting $NODE_IP"
  $K -n heros patch networkpolicy postgres --type=json $DRY \
    -p "[{\"op\":\"add\",\"path\":\"/spec/ingress/0/from/-\",\"value\":$PEER}]"
fi

if $K -n heros get networkpolicy mail -o json | grep -q "$NODE_IP/32"; then
  echo "mail netpol: already admits the node"
else
  echo "mail netpol: admitting $NODE_IP on 587"
  $K -n heros patch networkpolicy mail --type=json $DRY \
    -p "[{\"op\":\"add\",\"path\":\"/spec/ingress/-\",\"value\":{\"from\":[$PEER],\"ports\":[{\"port\":587,\"protocol\":\"TCP\"}]}}]"
fi
echo "done"
