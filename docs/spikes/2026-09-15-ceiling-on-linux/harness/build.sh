set -euo pipefail
mkdir -p /src/head /src/a1
tar -xf /w/head.tar -C /src/head; tar -xf /w/a1.tar -C /src/a1
cd /src/head
grep -q '^const maxDrawnParts = 4096$' internal/domain/geometry/limits.go
sed -i 's/^const maxDrawnParts = 4096$/const maxDrawnParts = 100_000/' internal/domain/geometry/limits.go
grep -q 'buildTimeout = 30 \* time.Second' internal/domain/cad/cad.go
export CGO_ENABLED=0
go build -trimpath -o /w/bin/forged-lifted ./cmd/forged
go build -trimpath -o /w/bin/store docs/spikes/2026-09-15-subtree-loading/store.go
go run docs/spikes/2026-09-15-kernel-build-ceiling/barrel.go -out /w/docs
node /w/car.js
cd /src/a1
go build -trimpath -o /w/bin/forge-worker-a1 ./cmd/forge-worker
go build -trimpath -o /w/bin/forgectl-a1 ./cmd/forgectl
mkdir -p /tmp/stub && cp docs/spikes/2026-09-15-worker-kernel-memory/harness/stub.go /tmp/stub/ && cd /tmp/stub && go mod init stub >/dev/null 2>&1 && go build -o /w/bin/stub .
ls -la /w/bin /w/docs
