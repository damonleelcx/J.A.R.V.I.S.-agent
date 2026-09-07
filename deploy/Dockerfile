# FORGE production image — forged, forge-worker and forgectl in one image.
#
# # Why this is not distroless, and not Alpine
#
# Two hard constraints, both discovered rather than assumed:
#
#   1. `sh` must exist. internal/tools/workspace.go runs `sh -c` for the
#      shell_run tool. A distroless image starts, serves the console, signs
#      people in, and fails the first time a task shells out.
#   2. glibc, not musl. The CAD kernel is build123d on OpenCASCADE, whose
#      cadquery-ocp wheels are published as manylinux_2_28_aarch64 — a glibc
#      ABI tag. On Alpine pip finds no wheel and there is no source fallback
#      worth having.
#
# So the runtime is debian-slim, which satisfies both.

# --- Go builder -------------------------------------------------------------
FROM golang:1.26-trixie AS go-build
# proxy.golang.org is unreachable from the colima VM this is built on; goproxy.cn
# answers in ~0.1s. Override at build time if building somewhere with direct
# access.
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0 GOOS=linux
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags="-s -w" -o /out/forged       ./cmd/forged      && \
    go build -trimpath -ldflags="-s -w" -o /out/forge-worker ./cmd/forge-worker && \
    go build -trimpath -ldflags="-s -w" -o /out/forgectl     ./cmd/forgectl

# --- CAD kernel venv --------------------------------------------------------
# Built in its own stage so the ~1GB of pip cache and build metadata does not
# reach the runtime layer. Only the finished venv is copied forward.
FROM python:3.13-slim-bookworm AS cad-build
ARG PIP_INDEX_URL=https://pypi.org/simple
RUN python3 -m venv /opt/cad/venv && \
    /opt/cad/venv/bin/pip install --no-cache-dir --index-url ${PIP_INDEX_URL} \
        --upgrade pip && \
    /opt/cad/venv/bin/pip install --no-cache-dir --index-url ${PIP_INDEX_URL} \
        build123d

# --- Runtime ----------------------------------------------------------------
FROM python:3.13-slim-bookworm
# OpenCASCADE links against the system X/GL stack even when it never opens a
# window. Without these the venv imports and then dies on the first build123d
# call, which looks like a kernel bug rather than a missing package.
#
# APT_MIRROR exists because deb.debian.org stalls mid-download from the colima
# VM this is built on — the same class of problem as proxy.golang.org being
# unreachable there. Retries cover a single wedged connection; the mirror
# override covers the host being slow as a whole.
ARG APT_MIRROR=deb.debian.org
RUN sed -i "s|deb.debian.org|${APT_MIRROR}|g" /etc/apt/sources.list.d/debian.sources 2>/dev/null || true; \
    printf 'Acquire::Retries "5";\nAcquire::http::Timeout "20";\n' > /etc/apt/apt.conf.d/99retries && \
    apt-get update && apt-get install -y --no-install-recommends \
        libgl1 libglx-mesa0 libglu1-mesa \
        libx11-6 libxext6 libxrender1 libxi6 \
        libsm6 libice6 libfontconfig1 libfreetype6 \
        ca-certificates \
    && rm -rf /var/lib/apt/lists/*

COPY --from=cad-build /opt/cad/venv /opt/cad/venv
COPY --from=go-build  /out/forged       /usr/local/bin/forged
COPY --from=go-build  /out/forge-worker /usr/local/bin/forge-worker
COPY --from=go-build  /out/forgectl     /usr/local/bin/forgectl

# Matches the fsGroup/runAsUser in the Deployment. The workspace PVC is chowned
# to this id by fsGroup; a mismatch shows up as a worker that cannot create its
# per-goal directory.
RUN useradd -u 10001 -m -s /usr/sbin/nologin forge
USER 10001
WORKDIR /var/lib/forge
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/forged"]
