# MinIO could not be pulled in CI, so the blob store step never ran there

**Date:** 2026-09-14 · **Status:** fixed on `storage/blob-store` (#54) · **Severity:** high for CI: the `check` job stopped at this step on every PR built on #54

## Summary

`make blob-up` started `minio/minio:RELEASE.2025-09-07T16-13-09Z`. That name only ever worked on this Mac, where the
image had been pulled a year earlier. Docker Hub no longer has a `minio/minio` repository, so a clean CI runner
failed to pull it. The job stopped before any Go test ran. The fix changes the registry to
`quay.io/minio/minio`, with the same tag. That is the identical image: same multi-arch manifest digest
`sha256:14cea493d9a34af32f524e538b8346cf79f3321eff8e708c1e2960462bd8936e`.

## Symptom

On the `check` job of #54, and of #61 on top of it:

```
Unable to find image 'minio/minio:RELEASE.2025-09-07T16-13-09Z' locally
docker: Error response from daemon: pull access denied for minio/minio, repository does not exist or may require 'docker login'
make: *** [Makefile:293: blob-up] Error 125
```

## Impact

- The `check` job fails at "Blob store (MinIO)", about a minute in. Nothing after that step ran:
  - the Go tests against Postgres and MinIO;
  - the recovery drills;
  - the build.
- **Stage S2 ("MinIO in CI") was reported done without ever passing in CI.** The only proof was a local run against a
  locally cached image.

## Preconditions

Any machine without that image already cached. That means every CI runner, and any new laptop.

## Root cause

- **Surface.** The pinned image name points at a registry repository that no longer exists:
  `hub.docker.com/v2/repositories/minio/minio/` answers `404`. MinIO still publishes the same releases on quay.io.
- **Why it was not caught.** S2 was checked locally with `make blob-up blob-wait test-blob`. `docker run` found
  `minio/minio:RELEASE…` in the local cache (pulled 2025-09-07) and never contacted the registry. So the check proved
  the Makefile target worked, not that the image could be fetched. The CI change was pushed but its run was never read.
- **Owner:** stage S1/S2 of `docs/plan-2026-09-13-millions-of-parts.md`, which picked the image name and closed S2
  without a green CI run.

## Fix

`Makefile`: `BLOB_IMAGE ?= quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z`, with a comment giving the Why, the digest
and a link to this document. CI and laptops keep one definition of the image. The `ci.yml` step still calls
`make blob-up blob-wait`.

## Verification

- `docker pull quay.io/minio/minio:RELEASE.2025-09-07T16-13-09Z` from a clean name succeeds.
- The pulled image's `RepoDigests` is `quay.io/minio/minio@sha256:14cea493…`. `docker buildx imagetools inspect` shows
  the same manifest digest, for linux/arm64 and linux/amd64 among others. The image the blob store tests passed against
  is `minio/minio@sha256:14cea493…`, the same digest.
- **Still to confirm:** the `check` job on #54 gets past "Blob store (MinIO)" on its next run. This document is updated
  with that result.

## Regression prevention

- The image is referenced once, in `Makefile`, so a registry move changes one line.
- The lesson carried into the plan: **a CI stage is not done until its CI run has been read.** A local check against
  a cached image cannot prove that CI can fetch it.

## Related

- `docs/plan-2026-09-13-millions-of-parts.md`, stages S1 and S2.
- PR #54 (blob store); PR #61 (K0), which inherited the failure.
