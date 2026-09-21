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

# Which commit this image is. Until 2026-09-20 nothing set these, so every
# container ever deployed reported version "dev", commit "unknown", and the
# first question of an incident — which build is running? — had no answer from
# the running system. `make image` fills them in from git; a plain
# `docker build` leaves them at "unknown", which every surface then reports as
# "unknown" rather than inventing a version. See internal/platform/buildinfo.
#
# The three are separate arguments rather than one string because they change at
# different times and are read for different reasons: the version is what a
# release is called, the commit is what it can be checked out at, and the date
# is what tells two images built from the same dirty tree apart.
ARG FORGE_VERSION=unknown
ARG FORGE_COMMIT=unknown
ARG FORGE_BUILD_DATE=unknown

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# ‼️ The version arguments are also why this RUN cannot be cached away: they
# change every commit, and a layer cached from the previous one would ship a
# binary stamped with the wrong commit, which is worse than an unstamped one.
RUN LD="-s -w -X main.version=${FORGE_VERSION} -X main.commit=${FORGE_COMMIT} -X main.date=${FORGE_BUILD_DATE}" && \
    go build -trimpath -ldflags="$LD" -o /out/forged       ./cmd/forged      && \
    go build -trimpath -ldflags="$LD" -o /out/forge-worker ./cmd/forge-worker && \
    go build -trimpath -ldflags="$LD" -o /out/forgectl     ./cmd/forgectl

# --- CAD kernel venv --------------------------------------------------------
# Built in its own stage so the ~1GB of pip cache and build metadata does not
# reach the runtime layer. Only the finished venv is copied forward.
#
# The packages are the pinned list the tests and CI use
# (internal/domain/cad/requirements.txt), so the kernel in the image is the one
# the kernel tests proved — not whatever PyPI held on the day of the build.
FROM python:3.13-slim-bookworm AS cad-build
ARG PIP_INDEX_URL=https://pypi.org/simple
COPY internal/domain/cad/requirements.txt /tmp/cad-requirements.txt
RUN python3 -m venv /opt/cad/venv && \
    /opt/cad/venv/bin/pip install --no-cache-dir --index-url ${PIP_INDEX_URL} \
        --upgrade pip && \
    /opt/cad/venv/bin/pip install --no-cache-dir --index-url ${PIP_INDEX_URL} \
        --no-deps -r /tmp/cad-requirements.txt && \
    /opt/cad/venv/bin/pip check

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

# The same three values again, as labels, so `docker inspect` and a registry
# listing answer "which commit is this?" without pulling and running the image.
# ARGs do not cross stage boundaries, which is why they are declared twice; they
# default to "unknown" here for the same reason they do in the builder.
ARG FORGE_VERSION=unknown
ARG FORGE_COMMIT=unknown
ARG FORGE_BUILD_DATE=unknown
LABEL org.opencontainers.image.version="${FORGE_VERSION}" \
      org.opencontainers.image.revision="${FORGE_COMMIT}" \
      org.opencontainers.image.created="${FORGE_BUILD_DATE}" \
      org.opencontainers.image.source="https://github.com/damonleelcx/J.A.R.V.I.S.-agent"

# Matches the fsGroup/runAsUser in the Deployment. The workspace PVC is chowned
# to this id by fsGroup; a mismatch shows up as a worker that cannot create its
# per-goal directory.
RUN useradd -u 10001 -m -s /usr/sbin/nologin forge
USER 10001
WORKDIR /var/lib/forge
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/forged"]
