#!/usr/bin/env bash
# forged from jarvis-cardcheck on 127.0.0.1:18220; the pane reaches it via pane-proxy.js on 18221.
. C:/Users/damon/AppData/Local/Temp/claude/C--Users-damon-Downloads-agents/9c73bcba-7500-4641-9433-f7c8efd05843/scratchpad/cardcheck/env.sh
mkdir -p $L/run-forged $L/outbox && cd $L/run-forged || exit 1
[ -f $L/secret.txt ] || python -c "import secrets;print(secrets.token_urlsafe(48))" > $L/secret.txt
export FORGE_HTTP_ADDR=127.0.0.1:18220 FORGE_PUBLIC_URL=http://127.0.0.1:18221
export FORGE_SESSION_SECRET="$(cat $L/secret.txt)"
export FORGE_MAIL_OUTBOX_DIR=$L/outbox
exec $L/bin/forged.exe >> $L/forged.log 2>&1
