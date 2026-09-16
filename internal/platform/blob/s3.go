package blob

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// Options says where the bucket is.
type Options struct {
	Bucket string
	Region string
	// Endpoint is set only for an S3-compatible server (MinIO in tests and local
	// development). Empty means AWS itself. When set, requests use path-style
	// addressing, which is what such servers expect.
	Endpoint string
}

// s3Store is a Store backed by an S3 bucket.
//
// # Credentials are never passed in
//
// It uses the SDK's default chain. In production that is the node's instance
// role through IMDS — deploy/bootstrap-s3.sh grants that role Get/Put/List under
// blobs/ and nothing else — so no key exists to leak or rotate. In tests and
// local development it is AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY for MinIO,
// set by the Makefile.
type s3Store struct {
	client *s3.Client
	bucket string
	log    *logx.Logger
}

// NewS3 returns a Store over an S3 bucket. It makes no request: whether the
// bucket is reachable is answered by the first call, not guessed here.
func NewS3(ctx context.Context, o Options, log *logx.Logger) (Store, error) {
	const op = "blob.NewS3"
	if strings.TrimSpace(o.Bucket) == "" {
		return nil, Unavailable(op)
	}
	if strings.TrimSpace(o.Region) == "" {
		return nil, errs.New(op, errs.CodeConfigInvalid).
			WithDetail("FORGE_BLOB_BUCKET is set but FORGE_BLOB_REGION is not; S3 requests are signed " +
				"for a region, so set it (us-east-1 for the production bucket)")
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(o.Region))
	if err != nil {
		return nil, errs.Wrap(op, errs.CodeConfigInvalid, err).
			WithDetail("the AWS SDK could not load its configuration for blob storage")
	}
	client := s3.NewFromConfig(cfg, func(so *s3.Options) {
		if o.Endpoint != "" {
			so.BaseEndpoint = aws.String(o.Endpoint)
			so.UsePathStyle = true
		}
	})
	return &s3Store{client: client, bucket: o.Bucket, log: log}, nil
}

func (s *s3Store) Available() bool { return true }

// Put hashes r, and uploads it only if the bucket does not already hold it.
//
// Three things make a second Put of the same bytes harmless, and each covers a
// case the others do not:
//   - Has first, so the common repeat costs a HEAD rather than an upload;
//   - If-None-Match: * on the upload, so two writers racing past Has cannot
//     overwrite each other (the loser gets 412, which means "already there");
//   - the key is the content hash, so even a server that ignores the condition
//     overwrites the object with identical bytes.
//
// The SHA-256 travels with the upload and S3 verifies it, so bytes damaged in
// transit are refused by the server instead of stored under a key they do not
// hash to.
func (s *s3Store) Put(ctx context.Context, r io.ReadSeeker) (Key, error) {
	const op = "blob.Store.Put"
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", errs.Wrap(op, errs.CodeInternal, err).WithDetail("could not rewind the bytes to hash them")
	}
	k, size, err := KeyOf(r)
	if err != nil {
		return "", err
	}
	if there, err := s.Has(ctx, k); err != nil {
		return "", err
	} else if there {
		return k, nil
	}
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return "", errs.Wrap(op, errs.CodeInternal, err).WithDetail("could not rewind the bytes to upload them")
	}
	digest, _ := hex.DecodeString(k.Hex())
	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:         aws.String(s.bucket),
		Key:            aws.String(objectName(k)),
		Body:           r,
		ContentLength:  aws.Int64(size),
		ChecksumSHA256: aws.String(base64.StdEncoding.EncodeToString(digest)),
		IfNoneMatch:    aws.String("*"),
	})
	if err != nil {
		if statusOf(err) == http.StatusPreconditionFailed {
			// Another writer stored these exact bytes between Has and here.
			return k, nil
		}
		wrapped := s.unreachable(op, err, "store")
		s.log.WarnWith(ctx, logx.EventBlobStoreFailed, wrapped, "key", string(k), "bytes", size)
		return "", wrapped
	}
	s.log.Info(ctx, logx.EventBlobStored, "key", string(k), "bytes", size)
	return k, nil
}

// Get opens a blob; the reader refuses bytes that do not hash to k.
func (s *s3Store) Get(ctx context.Context, k Key) (io.ReadCloser, error) {
	const op = "blob.Store.Get"
	if err := k.Validate(); err != nil {
		return nil, err
	}
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectName(k)),
	})
	if err != nil {
		var missing *types.NoSuchKey
		if errors.As(err, &missing) || statusOf(err) == http.StatusNotFound {
			return nil, errs.New(op, errs.CodeNotFound).
				WithDetail("no blob %s in bucket %s; it is rebuilt from the document when needed", k, s.bucket)
		}
		return nil, s.unreachable(op, err, "read")
	}
	return newVerifying(out.Body, k, func(got string) {
		s.log.ErrorWith(ctx, logx.EventBlobCorrupt,
			errs.New(op, errs.CodeStateCorrupt).WithDetail("bytes hash to sha256:%s", got),
			"key", string(k), "bucket", s.bucket)
	}), nil
}

// Has reports whether the bucket holds k.
func (s *s3Store) Has(ctx context.Context, k Key) (bool, error) {
	const op = "blob.Store.Has"
	if err := k.Validate(); err != nil {
		return false, err
	}
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(objectName(k)),
	})
	if err == nil {
		return true, nil
	}
	var missing *types.NotFound
	if errors.As(err, &missing) || statusOf(err) == http.StatusNotFound {
		return false, nil
	}
	return false, s.unreachable(op, err, "check")
}

// unreachable is every failure that is not "not there": access denied, a wrong
// region, no network, a bucket that does not exist. The detail names the three
// things that fix those, because the SDK's own message is rarely enough.
func (s *s3Store) unreachable(op string, err error, verb string) error {
	return errs.Wrap(op, errs.CodeConnectorUnavailable, err).
		WithDetail("could not %s a blob in S3 bucket %q (HTTP %d). Check FORGE_BLOB_BUCKET and "+
			"FORGE_BLOB_REGION, that the node's role carries the ForgeGeometryBlobs policy "+
			"(deploy/bootstrap-s3.sh), and that the pod may reach S3", verb, s.bucket, statusOf(err))
}

// statusOf is the HTTP status of an SDK error, or 0 when there was no response.
func statusOf(err error) int {
	var re *awshttp.ResponseError
	if errors.As(err, &re) {
		return re.HTTPStatusCode()
	}
	return 0
}
