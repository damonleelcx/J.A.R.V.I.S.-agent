# Deploying FORGE

`forge.heros-agent.space`, on the k3s node `i-05f4712279b04fac5` (t4g.large,
**arm64**, us-east-1c, 23.21.75.162).

## What you are deploying onto

This node is **not dedicated to FORGE**. It also runs:

| What | Where | Public |
|---|---|---|
| `opportunity-bridge` | ns `opportunity-bridge` | jobs.heros-agent.space |
| `heros-eval` | ns `heros-eval` | eval.heros-agent.space |
| docker-mailserver | ns `heros` | mail.heros-agent.space |
| postgres (shared) | ns `heros`, `postgres-0` | — |

FORGE gets its own namespace (`forge`), its own database and role on the
**shared** postgres, and its own ECR repository. It shares the mail relay and
the postgres instance with the products above, which is where all the care is.

## Access

**SSM only.** The security group opens 80/443 and nothing else; SSH times out
from everywhere. There is no kubectl context locally and no repo checkout on the
node. Commands run as root via `aws ssm send-command`.

`deploy/ssm.sh <script> [args]` handles the two framing problems: SSM collapses
newlines in `--parameters`, so scripts are base64-framed; and a script piped to
`bash` can be eaten by any command inside it that reads stdin (`kubectl exec -i`
does), so it is written to a file and run with `< /dev/null`.

## One-time setup (already done)

1. ECR repo `forge`.
2. Secrets Manager `forge/prod` — `database-url`, `llm-api-key`, `session-secret`.
   SMTP is deliberately NOT here: see below.
3. IAM inline policy `ForgeSecretsRead` on role `heros-vm`, scoped to `forge/*`.
   Without it the ExternalSecret never syncs **and** `bootstrap-db.sh` cannot
   read the password.
4. Route53 A record `forge.heros-agent.space` → 23.21.75.162.
5. `deploy/bootstrap-db.sh` — role + database + isolation.
6. `deploy/patch-heros-shared.sh` — three changes in the `heros` namespace.

## Deploying a new version

```sh
export DOCKER_HOST=unix:///Users/damon/.colima/default/docker.sock
docker build --platform linux/arm64 --build-arg APT_MIRROR=mirrors.ustc.edu.cn \
  -t forge:local -f deploy/Dockerfile .
# push by digest, then:
deploy/apply.sh <image@sha256:...> --dry-run   # reads the unfiltered diff
deploy/apply.sh <image@sha256:...>
```

## The traps

Every one of these fails **silently** — the product keeps serving and the thing
you wanted just never happens.

- 🔴 **The image cannot be distroless or Alpine.** `sh` is required
  (`internal/tools/workspace.go` runs `sh -c`), and the CAD kernel's
  `cadquery-ocp` wheels are `manylinux` — a glibc ABI tag, so musl finds no
  wheel. debian-slim satisfies both.
- 🔴 **SMTP must be addressed as `mail.heros-agent.space`**, via the pod
  `hostAliases` entry to the relay Service ClusterIP `10.43.120.55`. The relay's
  certificate carries that one SAN, so the Service name fails TLS — and mail
  then never delivers rather than erroring. Do **not** solve this with a CoreDNS
  rewrite: that also answers cert-manager's HTTP-01 self-check and breaks the
  relay's own renewal.
- 🔴 **The mail NetworkPolicy names each client namespace on 587.** A namespace
  not named there is refused at the network layer.
- 🔴 **The postgres NetworkPolicy is default-deny**, and cross-namespace access
  needs `namespaceSelector` + `podSelector` in **one list item** (ANDed). Two
  items are ORed and far wider than intended.
- 🔴 **The backup CronJob dumps only `BACKUP_DATABASES`.** A database missing
  from that list is unbacked-up while the job still reports OK.
- 🔴 **`REVOKE ... FROM forge` does nothing.** Postgres grants CONNECT to PUBLIC
  and every role inherits it. It must be revoked **from PUBLIC** and re-granted
  to the owner. `bootstrap-db.sh` verifies both directions rather than trusting
  the revoke ran.
- **`Recreate`, not RollingUpdate, for `forge-worker`.** Its workspace is a
  ReadWriteOnce PVC on one node; a rolling update wedges with the new pod
  Pending on the volume forever. `forged` has no PVC and rolls normally.
- **Never restart the colima VM** used to build — it hosts another project's
  live containers. Build on it; do not bounce it.
- **`deb.debian.org` stalls mid-download from that VM.** Hence `APT_MIRROR`.
  The same class of problem as `proxy.golang.org`, which is why `GOPROXY`
  defaults to `goproxy.cn`.
- **A wrapped build reports the wrapper's exit code.** Append the status INTO
  the log and grep the log; do not trust the task's completion code.

## Secrets

`forge/prod` holds FORGE's own. The SMTP credential is read from
`heros/platform` (`smtp-username` / `smtp-password`) — the account
`support@heros-agent.space`, already used by the platform. Copying it into
`forge/prod` would create a second place to rotate and a second place to get it
wrong.

A property named in the ExternalSecret and absent from the store fails the
**whole** secret, not just that key.
