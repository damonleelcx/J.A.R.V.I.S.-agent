// Package blob stores large, immutable bytes by the hash of their content.
//
// # What it is for, and what it is not
//
// FORGE keeps its record — the document, its versions, the audit trail — in
// Postgres, where a save is one transaction. What does NOT belong there is bulk
// that can be rebuilt from that record: a design's tessellated mesh, a cached
// B-rep, a STEP export. At the car milestone those are megabytes; at a million
// occurrences a single STEP export measured 662 MB
// (docs/plan-2026-09-13-millions-of-parts.md). This package is where that bulk
// goes. A lost blob is a cache miss, never lost work.
//
// # Why the key is the content's hash
//
// Two versions of an assembly that share a design share its mesh, and storing it
// twice would make storage grow with versions rather than with designs. A key
// derived from the bytes makes a second Put of the same bytes a no-op by
// construction, makes a read verifiable (the bytes either hash to their key or
// they are corrupt), and means nothing ever has to be overwritten — which is
// why the IAM policy grants no delete.
//
// # Absent, loudly
//
// A deployment with no bucket configured gets a Store whose every call refuses
// with the setting that fixes it. That is the rule the CAD kernel and the vision
// model follow: a capability that is not there says so, rather than a feature
// that quietly degrades into something else.
package blob

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
)

// Key names a blob by the SHA-256 of its bytes, written "sha256:<64 hex>".
//
// The algorithm is part of the key so that a future change of hash can coexist
// with every blob already written, instead of silently re-meaning old keys.
type Key string

const keyPrefix = "sha256:"

// KeyOf returns the key for these bytes, reading r to the end.
func KeyOf(r io.Reader) (Key, int64, error) {
	const op = "blob.KeyOf"
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", n, errs.Wrap(op, errs.CodeInternal, err).WithDetail("could not read the bytes to hash them")
	}
	return Key(keyPrefix + hex.EncodeToString(h.Sum(nil))), n, nil
}

// Hex is the 64-character digest, or "" for a key that is not well formed.
func (k Key) Hex() string {
	s := string(k)
	if !strings.HasPrefix(s, keyPrefix) {
		return ""
	}
	d := s[len(keyPrefix):]
	if len(d) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(d); err != nil {
		return ""
	}
	if strings.ToLower(d) != d {
		// Uppercase hex decodes to the same digest and would name a second object
		// for the same bytes, which is the one thing content addressing exists to
		// prevent.
		return ""
	}
	return d
}

// Validate refuses a key that is not "sha256:" followed by 64 lowercase hex digits.
func (k Key) Validate() error {
	if k.Hex() == "" {
		return errs.New("blob.Key.Validate", errs.CodeValidationFailed).
			WithDetail("%q is not a blob key; a key is \"sha256:\" followed by 64 lowercase hex digits, "+
				"as returned by Put", string(k))
	}
	return nil
}

// objectName is where a key lives in a bucket: under blobs/, which is the only
// prefix the node's IAM policy grants, and fanned out by the first byte so no
// single listing prefix holds everything.
func objectName(k Key) string {
	d := k.Hex()
	return fmt.Sprintf("blobs/sha256/%s/%s", d[:2], d)
}

// Store is content-addressed storage for large immutable bytes.
type Store interface {
	// Put stores what r holds and returns its key. Storing bytes that are
	// already there changes nothing and returns the same key. r is read once to
	// hash it and then rewound to upload it, so a caller can hand over a file of
	// any size without holding it in memory.
	Put(ctx context.Context, r io.ReadSeeker) (Key, error)
	// Get opens the blob. The reader verifies the bytes against the key as they
	// are read, and its final Read returns an error rather than io.EOF if they do
	// not match — so a corrupt blob can never be consumed as a good one.
	Get(ctx context.Context, k Key) (io.ReadCloser, error)
	// Has reports whether the blob exists.
	Has(ctx context.Context, k Key) (bool, error)
	// Available reports whether this deployment has blob storage at all.
	Available() bool
}

// Unavailable is the refusal every call gets when no bucket is configured, with
// the setting that fixes it. Exported so a caller that checks Available first
// can refuse in the same words.
func Unavailable(op string) error {
	return errs.New(op, errs.CodeConnectorUnavailable).
		WithDetail("this deployment has no blob storage, so large geometry (per-design meshes, " +
			"B-rep caches, STEP exports) cannot be stored. Set FORGE_BLOB_BUCKET to an S3 bucket " +
			"(and FORGE_BLOB_REGION; FORGE_BLOB_ENDPOINT for an S3-compatible server such as MinIO). " +
			"deploy/bootstrap-s3.sh creates a correctly locked-down bucket.")
}

// disabled is the Store of a deployment with no bucket.
type disabled struct{}

// Disabled returns a Store whose every call refuses with Unavailable.
func Disabled() Store { return disabled{} }

func (disabled) Put(context.Context, io.ReadSeeker) (Key, error) {
	return "", Unavailable("blob.Store.Put")
}

func (disabled) Get(context.Context, Key) (io.ReadCloser, error) {
	return nil, Unavailable("blob.Store.Get")
}

func (disabled) Has(context.Context, Key) (bool, error) {
	return false, Unavailable("blob.Store.Has")
}

func (disabled) Available() bool { return false }

// verifying wraps a blob's body and checks its hash when the body ends.
type verifying struct {
	body io.ReadCloser
	want string
	h    interface {
		io.Writer
		Sum([]byte) []byte
	}
	onMismatch func(got string)
	checked    bool
}

func newVerifying(body io.ReadCloser, k Key, onMismatch func(got string)) *verifying {
	return &verifying{body: body, want: k.Hex(), h: sha256.New(), onMismatch: onMismatch}
}

func (v *verifying) Read(p []byte) (int, error) {
	n, err := v.body.Read(p)
	if n > 0 {
		_, _ = v.h.Write(p[:n])
	}
	if err == io.EOF && !v.checked {
		v.checked = true
		got := hex.EncodeToString(v.h.Sum(nil))
		if got != v.want {
			if v.onMismatch != nil {
				v.onMismatch(got)
			}
			return n, errs.New("blob.Store.Get", errs.CodeStateCorrupt).
				WithDetail("the stored bytes hash to sha256:%s, not the key they were read by "+
					"(sha256:%s). The blob is corrupt and was not used; it is rebuilt from the "+
					"document the next time it is needed.", got, v.want)
		}
	}
	return n, err
}

func (v *verifying) Close() error { return v.body.Close() }
