#!/bin/bash
# Render FORGE's manifests with a pinned image digest and apply them on the node.
#
# There is no repo checkout on the node and no kubectl context locally, so the
# manifests travel gzip+base64 inside an SSM command. They are re-checksummed ON
# the node before kubectl sees them: truncation in transit produces VALID YAML
# describing LESS than was intended, and kubectl applies that happily.
#
# Usage: deploy/apply.sh <image-ref> [--dry-run]
set -euo pipefail
IMAGE="${1:?usage: apply.sh <image-ref> [--dry-run]}"
DRY="${2:-}"
HERE="$(cd "$(dirname "$0")" && pwd)"
OUT=$(mktemp -d)

for f in "$HERE"/k8s/*.yaml; do
  sed "s|FORGE_IMAGE|$IMAGE|g" "$f" > "$OUT/$(basename "$f")"
done
if grep -rq "FORGE_IMAGE" "$OUT"; then echo "FATAL: unsubstituted FORGE_IMAGE"; exit 1; fi

# A document separator between every file, NOT a plain cat. Concatenating YAML
# files directly merges the last document of one into the first of the next:
# 20-config.yaml's ConfigMap `data:` silently became a field on the Deployment
# in 30-forged.yaml, and only the API server's strict decoder caught it.
# Per-file validation cannot catch this — it validates them apart.
: > "$OUT/all.yaml"
for f in "$OUT"/[0-9]*.yaml; do
  printf -- '---\n' >> "$OUT/all.yaml"
  cat "$f" >> "$OUT/all.yaml"
  printf -- '\n' >> "$OUT/all.yaml"
done
SUM=$(shasum -a 256 "$OUT/all.yaml" | cut -d' ' -f1)
echo "rendered $(wc -l < "$OUT/all.yaml") lines, sha256=$SUM"

PAYLOAD=$(gzip -9 < "$OUT/all.yaml" | base64 | tr -d '\n')
cat > "$OUT/remote.sh" <<EOF
set -euo pipefail
echo "$PAYLOAD" | base64 -d | gunzip > /opt/forge.yaml
GOT=\$(sha256sum /opt/forge.yaml | cut -d' ' -f1)
if [ "\$GOT" != "$SUM" ]; then
  echo "FATAL: checksum mismatch — manifest truncated in transit"
  echo "  expected $SUM"
  echo "  got      \$GOT"
  exit 1
fi
echo "checksum verified on node: \$GOT"
# Unfiltered diff. A diff filtered to the lines you expect to change cannot show
# you the thing you did not expect — that is how an apply here once nearly
# deleted another product's mail account.
echo "===== DIFF (unfiltered) ====="
k3s kubectl diff -f /opt/forge.yaml || true
echo "===== END DIFF ====="
EOF
if [ "$DRY" = "--dry-run" ]; then
  echo 'echo "DRY RUN — not applying"' >> "$OUT/remote.sh"
else
  echo 'k3s kubectl apply -f /opt/forge.yaml' >> "$OUT/remote.sh"
fi
echo "$OUT/remote.sh"
