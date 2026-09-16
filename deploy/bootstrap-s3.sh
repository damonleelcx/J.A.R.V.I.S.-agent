#!/usr/bin/env bash
# bootstrap-s3.sh — create (or converge) the private S3 bucket FORGE keeps large,
# immutable geometry blobs in, and grant the node's instance role access to it.
#
# WHY THIS EXISTS
#   The millions-of-parts plan (docs/plan-2026-09-13-millions-of-parts.md, Phase 3)
#   keeps the document in Postgres and puts content-addressed blobs — per-design
#   meshes, B-rep caches, STEP exports — in S3. Those blobs are rebuildable from the
#   document, so the bucket holds a cache and an export store, not the record.
#
# WHAT IT DOES (every step is idempotent: re-running converges, never duplicates)
#   1. the bucket, private: Block Public Access fully ON, ACLs disabled
#      (BucketOwnerEnforced), SSE-S3 encryption, TLS-only bucket policy
#   2. an inline policy on the ROLE behind the node's instance profile, scoped to
#      this bucket's blobs/ prefix: read, write, list — no delete (blobs are
#      immutable and keyed by content hash)
#   3. REPORTS the instance's IMDS hop limit. Pods without hostNetwork
#      (forge-worker) cannot reach the instance role through IMDSv2 when the hop
#      limit is 1. Changing it is a separate, explicit flag.
#
# WHY THE ROLE IS LOOKED UP, NOT NAMED
#   The instance profile and the role behind it can have different names, and a
#   policy put on a guessed role name silently grants nothing to the node.
#
# USAGE
#   deploy/bootstrap-s3.sh --dry-run          # read-only: shows what would change
#   deploy/bootstrap-s3.sh                    # apply
#   deploy/bootstrap-s3.sh --fix-imds-hop-limit   # also raise the IMDS hop limit to 2
#
# PREREQUISITE
#   An active AWS session: run `aws login` first. The session expires; if a step
#   fails with an expired-session error, log in again and re-run — nothing is left
#   half-applied that a re-run does not converge.
set -euo pipefail

REGION="us-east-1"
INSTANCE_ID="i-05f4712279b04fac5"
BUCKET=""
POLICY_NAME="ForgeGeometryBlobs"
PREFIX="blobs/"
DRY_RUN=0
FIX_IMDS=0

usage() { sed -n '2,33p' "$0" | sed 's/^# \{0,1\}//'; }

while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY_RUN=1 ;;
    --fix-imds-hop-limit) FIX_IMDS=1 ;;
    --region) REGION="$2"; shift ;;
    --instance-id) INSTANCE_ID="$2"; shift ;;
    --bucket) BUCKET="$2"; shift ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown argument: $1 (see --help)" >&2; exit 2 ;;
  esac
  shift
done

say()  { printf '%s\n' "$*"; }
step() { printf '\n== %s\n' "$*"; }
fail() {
  printf 'ERROR: %s\n' "$1" >&2
  [ -n "${2:-}" ] && printf '  Fix: %s\n' "$2" >&2
  exit 1
}
# change runs a mutating AWS call, or prints it under --dry-run.
change() {
  if [ "$DRY_RUN" = 1 ]; then
    printf '  would run: aws'; printf ' %q' "$@"; printf '\n'
  else
    aws "$@" >/dev/null
    printf '  applied: %s\n' "$1 $2"
  fi
}

command -v aws >/dev/null 2>&1 || fail "the AWS CLI is not installed" \
  "install AWS CLI v2: https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html"

step "session"
if ! ACCOUNT=$(aws sts get-caller-identity --query Account --output text 2>/dev/null); then
  fail "no active AWS session (it expires)" "run 'aws login', then re-run this script"
fi
say "  account $ACCOUNT"
[ -n "$BUCKET" ] || BUCKET="forge-geometry-${ACCOUNT}"
say "  bucket  $BUCKET ($REGION)"
[ "$DRY_RUN" = 1 ] && say "  mode    DRY RUN — read-only calls only"

step "bucket"
if aws s3api head-bucket --bucket "$BUCKET" --region "$REGION" >/dev/null 2>&1; then
  say "  exists"
else
  # us-east-1 is the one region that must NOT be given a LocationConstraint.
  if [ "$REGION" = "us-east-1" ]; then
    change s3api create-bucket --bucket "$BUCKET" --region "$REGION"
  else
    change s3api create-bucket --bucket "$BUCKET" --region "$REGION" \
      --create-bucket-configuration "LocationConstraint=$REGION"
  fi
fi

step "block all public access"
change s3api put-public-access-block --bucket "$BUCKET" --region "$REGION" \
  --public-access-block-configuration \
  "BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true"

step "disable ACLs (bucket owner enforced)"
change s3api put-bucket-ownership-controls --bucket "$BUCKET" --region "$REGION" \
  --ownership-controls 'Rules=[{ObjectOwnership=BucketOwnerEnforced}]'

step "default encryption (SSE-S3)"
change s3api put-bucket-encryption --bucket "$BUCKET" --region "$REGION" \
  --server-side-encryption-configuration \
  '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"},"BucketKeyEnabled":true}]}'

step "TLS-only bucket policy"
TLS_POLICY=$(cat <<JSON
{"Version":"2012-10-17","Statement":[{"Sid":"DenyInsecureTransport","Effect":"Deny","Principal":"*",
"Action":"s3:*","Resource":["arn:aws:s3:::${BUCKET}","arn:aws:s3:::${BUCKET}/*"],
"Condition":{"Bool":{"aws:SecureTransport":"false"}}}]}
JSON
)
change s3api put-bucket-policy --bucket "$BUCKET" --region "$REGION" --policy "$TLS_POLICY"

step "instance role"
PROFILE_ARN=$(aws ec2 describe-instances --region "$REGION" --instance-ids "$INSTANCE_ID" \
  --query 'Reservations[0].Instances[0].IamInstanceProfile.Arn' --output text 2>/dev/null) \
  || fail "could not read instance $INSTANCE_ID" "check --instance-id and --region, and that your session can call ec2:DescribeInstances"
[ -n "$PROFILE_ARN" ] && [ "$PROFILE_ARN" != "None" ] || fail "instance $INSTANCE_ID has no instance profile" \
  "attach an instance profile to the node first; FORGE authenticates to S3 through it, with no static keys"
PROFILE_NAME=${PROFILE_ARN##*/}
ROLE=$(aws iam get-instance-profile --instance-profile-name "$PROFILE_NAME" \
  --query 'InstanceProfile.Roles[0].RoleName' --output text)
[ -n "$ROLE" ] && [ "$ROLE" != "None" ] || fail "instance profile $PROFILE_NAME has no role" \
  "add a role to the instance profile"
say "  profile $PROFILE_NAME -> role $ROLE"

ROLE_POLICY=$(cat <<JSON
{"Version":"2012-10-17","Statement":[
{"Sid":"ForgeBlobsReadWrite","Effect":"Allow","Action":["s3:GetObject","s3:PutObject"],
 "Resource":"arn:aws:s3:::${BUCKET}/${PREFIX}*"},
{"Sid":"ForgeBlobsList","Effect":"Allow","Action":"s3:ListBucket",
 "Resource":"arn:aws:s3:::${BUCKET}","Condition":{"StringLike":{"s3:prefix":["${PREFIX}*"]}}}]}
JSON
)
change iam put-role-policy --role-name "$ROLE" --policy-name "$POLICY_NAME" --policy-document "$ROLE_POLICY"

step "IMDS hop limit (pods without hostNetwork)"
read -r TOKENS HOPS <<EOF
$(aws ec2 describe-instances --region "$REGION" --instance-ids "$INSTANCE_ID" \
  --query 'Reservations[0].Instances[0].MetadataOptions.[HttpTokens,HttpPutResponseHopLimit]' --output text)
EOF
say "  HttpTokens=$TOKENS HttpPutResponseHopLimit=$HOPS"
if [ "$TOKENS" = "required" ] && [ "${HOPS:-1}" -lt 2 ]; then
  say "  ‼️ forge-worker (no hostNetwork) cannot reach the instance role at hop limit $HOPS."
  if [ "$FIX_IMDS" = 1 ]; then
    change ec2 modify-instance-metadata-options --region "$REGION" --instance-id "$INSTANCE_ID" \
      --http-put-response-hop-limit 2
  else
    say "  not changed. Re-run with --fix-imds-hop-limit to raise it to 2 (affects every pod on the node)."
  fi
else
  say "  ok for pods without hostNetwork"
fi

step "done"
if [ "$DRY_RUN" = 1 ]; then
  say "  dry run: nothing was changed"
else
  say "  set in deploy/k8s/20-config.yaml: FORGE_BLOB_BUCKET=$BUCKET FORGE_BLOB_REGION=$REGION"
fi
