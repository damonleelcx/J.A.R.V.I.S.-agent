#!/bin/bash
# Create FORGE's role and database on the shared postgres (postgres-0, ns heros).
#
# The password is NEVER passed in: it is read from Secrets Manager ON the node,
# because SSM retains command history and a password in a command parameter is
# a password on disk in CloudTrail.
#
# The isolation step is the one that matters. `REVOKE ... FROM forge` does
# NOTHING: postgres grants CONNECT to PUBLIC by default and every role inherits
# it, so without revoking from PUBLIC the forge role can open the other
# products' databases and enumerate their tables. Measured on this instance
# before — oba could list 191 tables in `heros`.
set -euo pipefail
DRY=""
[ "${1:-}" = "--dry-run" ] && DRY=1 && echo "== DRY RUN =="

DSN=$(aws secretsmanager get-secret-value --secret-id forge/prod --region us-east-1 \
      --query SecretString --output text | python3 -c 'import json,sys;print(json.load(sys.stdin)["database-url"])')
PW=$(python3 -c "
import urllib.parse as u,sys
print(u.urlparse('$DSN').password)")
[ -z "$PW" ] && { echo "FATAL: no password in database-url"; exit 1; }
echo "read password from Secrets Manager (length ${#PW})"

SQL=$(cat <<EOF
do \$\$ begin
  if not exists (select 1 from pg_roles where rolname = 'forge') then
    create role forge login password '$PW';
  else
    alter role forge login password '$PW';
  end if;
end \$\$;
select 'role ok';
EOF
)

if [ -n "$DRY" ]; then echo "would create role forge and database forge"; exit 0; fi

k3s kubectl -n heros exec -i postgres-0 -- psql -U heros -d postgres -v ON_ERROR_STOP=1 <<< "$SQL"

# createdb is not transactional and has no IF NOT EXISTS.
if k3s kubectl -n heros exec postgres-0 -- psql -U heros -lqt | cut -d'|' -f1 | grep -qw forge; then
  echo "database forge: already exists"
else
  k3s kubectl -n heros exec postgres-0 -- psql -U heros -d postgres -v ON_ERROR_STOP=1 \
    -c "create database forge owner forge"
  echo "database forge: created"
fi

# Isolation. Scoped to FORGE's OWN database on purpose.
#
# The same PUBLIC-inherits-CONNECT gap very likely applies to heros_eval and
# oba, but fixing those is NOT part of deploying FORGE: their apps may connect
# as a role other than their namesake, and a blanket revoke would take them
# down. Raised separately as a finding rather than fixed in passing.
k3s kubectl -n heros exec -i postgres-0 -- psql -U heros -d postgres -v ON_ERROR_STOP=1 <<'EOF'
revoke connect on database forge from public;
grant  connect on database forge to forge;
EOF

# Verify the revoke actually bit, rather than trusting that it ran. `forge`
# must NOT be able to open another product's database.
if k3s kubectl -n heros exec postgres-0 -- env PGPASSWORD="$PW" \
     psql -U forge -h 127.0.0.1 -d heros_eval -c 'select 1' >/dev/null 2>&1; then
  echo "FATAL: forge can open heros_eval — isolation did NOT take"; exit 1
else
  echo "verified: forge cannot open heros_eval"
fi
if ! k3s kubectl -n heros exec postgres-0 -- env PGPASSWORD="$PW" \
     psql -U forge -h 127.0.0.1 -d forge -c 'select 1' >/dev/null 2>&1; then
  echo "FATAL: forge cannot open its OWN database"; exit 1
else
  echo "verified: forge can open its own database"
fi
echo "isolation applied"
