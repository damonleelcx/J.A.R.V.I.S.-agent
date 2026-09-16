package blob

import (
	"context"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// New returns the Store this deployment is configured for: S3 when a bucket is
// set, and otherwise one that refuses every call naming FORGE_BLOB_BUCKET.
//
// Never nil, so no caller can dereference its way into a panic on a deployment
// that simply has no bucket — and never a silent substitute either.
func New(ctx context.Context, cfg config.BlobConfig, log *logx.Logger) (Store, error) {
	if !cfg.Configured() {
		return Disabled(), nil
	}
	return NewS3(ctx, Options{Bucket: cfg.Bucket, Region: cfg.Region, Endpoint: cfg.Endpoint}, log)
}
