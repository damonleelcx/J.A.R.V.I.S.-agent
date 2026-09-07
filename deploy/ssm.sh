#!/bin/bash
# Run a local script on the k3s node, as root, via SSM.
#
# # Why this exists rather than ssh
#
# The security group opens 80 and 443 and nothing else; SSH times out from
# everywhere. There is no kubectl context locally and no repo checkout on the
# node, so every deploy step is a local script shipped to the node and run
# there.
#
# # The two framing problems, both of which fail QUIETLY
#
# 1. SSM collapses newlines in `--parameters`, so a multi-line script arrives as
#    a single line and everything after the first command is lost. The script is
#    base64-framed, which survives that intact.
#
# 2. A script piped to `bash` is read from the same stdin its own commands read.
#    `kubectl exec -i` — which appears in verify.sh — consumes THE REST OF THE
#    SCRIPT as that pod's input. The run then exits 0 having silently skipped
#    every check after that line, which looks exactly like a clean pass. So the
#    script is written to a file and run with `< /dev/null`.
#
# Usage: deploy/ssm.sh <local-script> [args...]
set -euo pipefail

INSTANCE="${FORGE_NODE:-i-05f4712279b04fac5}"
REGION="${AWS_REGION:-us-east-1}"
SCRIPT="${1:?usage: ssm.sh <local-script> [args...]}"
shift || true

[ -r "$SCRIPT" ] || { echo "ssm.sh: no such script: $SCRIPT" >&2; exit 1; }

# Built in Python rather than by shell quoting: the payload is base64 inside a
# JSON string inside a shell argument, and one wrong layer of escaping produces
# a command that runs and does the wrong thing rather than one that fails.
PARAMS=$(python3 - "$SCRIPT" "$*" <<'PY'
import base64, json, sys
b64 = base64.b64encode(open(sys.argv[1], 'rb').read()).decode()
args = sys.argv[2] if len(sys.argv) > 2 else ''
print(json.dumps({"commands": [
    "set -euo pipefail\n"
    f"echo {b64} | base64 -d > /tmp/forge-run.sh\n"
    "chmod +x /tmp/forge-run.sh\n"
    f"/tmp/forge-run.sh {args} < /dev/null\n"
]}))
PY
)

CMD_ID=$(aws ssm send-command \
  --region "$REGION" \
  --instance-ids "$INSTANCE" \
  --document-name AWS-RunShellScript \
  --parameters "$PARAMS" \
  --query 'Command.CommandId' --output text)
echo "ssm.sh: $SCRIPT -> $INSTANCE (command $CMD_ID)" >&2

# Poll rather than `aws ssm wait`: the waiter treats a non-zero exit as a
# failure to wait for and gives no output, and the output is the whole point.
for _ in $(seq 1 120); do
  STATUS=$(aws ssm get-command-invocation --region "$REGION" \
    --command-id "$CMD_ID" --instance-id "$INSTANCE" \
    --query 'Status' --output text 2>/dev/null || echo Pending)
  case "$STATUS" in
    Success|Failed|Cancelled|TimedOut) break ;;
  esac
  sleep 5
done

aws ssm get-command-invocation --region "$REGION" \
  --command-id "$CMD_ID" --instance-id "$INSTANCE" \
  --query 'StandardOutputContent' --output text
ERR=$(aws ssm get-command-invocation --region "$REGION" \
  --command-id "$CMD_ID" --instance-id "$INSTANCE" \
  --query 'StandardErrorContent' --output text)
[ -n "$ERR" ] && [ "$ERR" != "None" ] && echo "--- stderr ---" >&2 && echo "$ERR" >&2

echo "ssm.sh: status=$STATUS" >&2
[ "$STATUS" = "Success" ]
