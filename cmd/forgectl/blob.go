package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/blob"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// blobCheckPayload is what `forgectl blob check` stores and reads back.
//
// # Why the bytes are fixed
//
// A blob's key is the hash of its bytes, so the same bytes are the same object.
// The production role (deploy/bootstrap-s3.sh, ForgeGeometryBlobs) may Get, Put
// and List under blobs/ and may NOT delete. A check that wrote fresh bytes on
// every run would leave one object behind per run, forever, with nothing able to
// remove it. With fixed bytes the first run stores one small object and every
// later run's Put is a HEAD that finds it there.
//
// ‼️ Changing these bytes is allowed but not free: the old object stays in the
// bucket for good. There is no reason to change them.
var blobCheckPayload = []byte("FORGE blob store round-trip check (forgectl blob check). " +
	"These bytes never change, so running the check again stores nothing new.\n")

// blobCheckOK starts the one line a passing check prints. deploy/verify.sh looks
// for it by name, so it is a contract with that script, not a message.
const blobCheckOK = "BLOB-ROUNDTRIP-OK"

// cmdBlobCheck proves, from wherever it runs, that this deployment's blob store
// works end to end: configuration, credentials, network path and permissions.
//
// # Why a command and not a startup probe
//
// Constructing the store makes no request (blob.NewS3), so forged and the worker
// start even while S3 is unreachable — blob storage is a cache, and its absence
// must not take the product down. That leaves the question "can THIS pod reach
// the bucket?" unanswered by anything that runs on its own. verify.sh check 9
// asks it, inside both pods, because they reach the instance role and S3 by
// different network paths (forged on the host network, the worker behind
// 32-worker-egress.yaml).
func cmdBlobCheck(ctx context.Context, cfg *config.Config, log *logx.Logger) error {
	store, err := blob.New(ctx, cfg.Blob, log)
	if err != nil {
		return err
	}
	return blobCheck(ctx, store, os.Stdout)
}

// blobCheck puts blobCheckPayload, confirms Has sees it, reads it back and
// compares, and only then prints blobCheckOK with the digest. Any other outcome
// is an error, which forgectl turns into exit status 1.
func blobCheck(ctx context.Context, s blob.Store, out io.Writer) error {
	const op = "forgectl.blob.check"
	if !s.Available() {
		// The same refusal every Store call gives, naming FORGE_BLOB_BUCKET, rather
		// than three failures that each say it.
		return blob.Unavailable(op)
	}
	want, _, err := blob.KeyOf(bytes.NewReader(blobCheckPayload))
	if err != nil {
		return err
	}

	k, err := s.Put(ctx, bytes.NewReader(blobCheckPayload))
	if err != nil {
		return err
	}
	if k != want {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("Put returned key %s for bytes whose hash is %s; the store is not content-addressed", k, want)
	}

	there, err := s.Has(ctx, k)
	if err != nil {
		return err
	}
	if !there {
		return errs.New(op, errs.CodeInvariantViolated).
			WithDetail("Put stored %s and then Has did not find it", k)
	}

	rc, err := s.Get(ctx, k)
	if err != nil {
		return err
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil {
		// Includes the S3 reader's own refusal of bytes that do not hash to k.
		return err
	}
	// ‼️ Compared here as well, not left to the reader's hash check. That check is
	// a property of the S3 adapter; this command must not report a round trip it
	// did not observe, whatever Store it was handed.
	if !bytes.Equal(got, blobCheckPayload) {
		return errs.New(op, errs.CodeStateCorrupt).
			WithDetail("read back %d bytes for %s that are not the %d bytes that were stored", len(got), k, len(blobCheckPayload))
	}

	_, err = fmt.Fprintf(out, "%s %s\n", blobCheckOK, k.Hex())
	return err
}
