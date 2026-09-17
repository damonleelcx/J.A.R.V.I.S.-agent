#!/usr/bin/env bash
# forged on 127.0.0.1:18330; the Browser pane reaches it through
# docs/spikes/2026-09-15-card-checked/harness/pane-proxy.js on 18331.
. "$(dirname "$0")/env.sh"
mkdir -p $L/run-forged $L/outbox && cd $L/run-forged || exit 1
[ -f $L/secret.txt ] || python -c "import secrets;print(secrets.token_urlsafe(48))" > $L/secret.txt
export FORGE_HTTP_ADDR=127.0.0.1:18330 FORGE_PUBLIC_URL=http://127.0.0.1:18331
export FORGE_SESSION_SECRET="$(cat $L/secret.txt)" FORGE_MAIL_OUTBOX_DIR=$L/outbox
exec $L/bin/forged.exe >> $L/forged.log 2>&1
