package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/blob"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// The fences for `forgectl blob check`, the command deploy/verify.sh check 9 runs
// inside both pods. What it prints is the only evidence a deploy gets that a pod
// can reach the bucket, so a check that prints OK without having seen the bytes
// come back would be worse than no check.

// Through run(), so the dispatch and the section it loads are held too: a
// deployment with no bucket gets a refusal naming the setting, not a round trip
// against nothing and not a demand for an unrelated database URL.
func TestBlobCheck_AnUnconfiguredDeploymentIsRefusedNamingTheBucketSetting(t *testing.T) {
	for _, k := range []string{"FORGE_BLOB_BUCKET", "FORGE_BLOB_REGION", "FORGE_BLOB_ENDPOINT", "FORGE_DATABASE_URL"} {
		t.Setenv(k, "")
	}
	err := run(context.Background(), "blob", []string{"check"})
	if err == nil {
		t.Fatal("blob check with no bucket configured succeeded")
	}
	if errs.CodeOf(err) != errs.CodeConnectorUnavailable {
		t.Errorf("code = %s, want %s: %v", errs.CodeOf(err), errs.CodeConnectorUnavailable, err)
	}
	if !strings.Contains(err.Error(), "FORGE_BLOB_BUCKET") {
		t.Errorf("the refusal does not name FORGE_BLOB_BUCKET: %v", err)
	}

	var out bytes.Buffer
	if err := blobCheck(context.Background(), blob.Disabled(), &out); err == nil {
		t.Fatal("blobCheck on the refusing store succeeded")
	}
	if strings.Contains(out.String(), blobCheckOK) {
		t.Errorf("a refused check printed %q", out.String())
	}
}

// lyingStore is a Store that is wrong in exactly one way at a time. It does not
// verify hashes, so the command's own comparison is the only thing that can
// catch what it hands back.
type lyingStore struct {
	putKey  blob.Key // "" means the honest key
	missing bool
	getBody []byte // nil means the honest bytes
}

func (s lyingStore) Available() bool { return true }

func (s lyingStore) Put(_ context.Context, r io.ReadSeeker) (blob.Key, error) {
	if s.putKey != "" {
		return s.putKey, nil
	}
	k, _, err := blob.KeyOf(r)
	return k, err
}

func (s lyingStore) Has(context.Context, blob.Key) (bool, error) { return !s.missing, nil }

func (s lyingStore) Get(context.Context, blob.Key) (io.ReadCloser, error) {
	body := s.getBody
	if body == nil {
		body = blobCheckPayload
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func TestBlobCheck_AStoreThatLiesIsNeverReportedOK(t *testing.T) {
	other := blob.Key("sha256:" + strings.Repeat("0", 64))
	altered := append([]byte(nil), blobCheckPayload...)
	altered[0] ^= 0xff

	for _, tc := range []struct {
		name  string
		store lyingStore
	}{
		{"put returns a key that is not the content hash", lyingStore{putKey: other}},
		{"has does not find what was put", lyingStore{missing: true}},
		{"get returns different bytes", lyingStore{getBody: altered}},
		{"get returns nothing", lyingStore{getBody: []byte{}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := blobCheck(context.Background(), tc.store, &out)
			if err == nil {
				t.Fatalf("the check passed and printed %q", out.String())
			}
			if strings.Contains(out.String(), blobCheckOK) {
				t.Errorf("a failed check still printed %q", out.String())
			}
		})
	}

	// And the honest version of the same store passes, so the cases above fail
	// for the lie and not for something every store would trip.
	var out bytes.Buffer
	if err := blobCheck(context.Background(), lyingStore{}, &out); err != nil {
		t.Fatalf("an honest store failed the check: %v", err)
	}
}

// Against a real S3-compatible server, as S1's store tests are, with the same
// skip convention: only when FORGE_TEST_BLOB_ENDPOINT is unset (`make blob-up`,
// and the Makefile's test-blob target sets the credentials).
//
// Run twice, because the second run is the one production sees on every deploy
// after the first: it must pass, and it must store nothing — the role cannot
// delete, so an object per run would be permanent.
func TestBlobCheck_RoundTripsAgainstARealBucketAndARepeatStoresNothing(t *testing.T) {
	endpoint := os.Getenv("FORGE_TEST_BLOB_ENDPOINT")
	if endpoint == "" {
		t.Skip("FORGE_TEST_BLOB_ENDPOINT is unset; skipping the blob check against MinIO. " +
			"Run `make blob-up` and `make test-blob`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	const bucket = "forge-test-forgectl-blob-check"

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"))
	if err != nil {
		t.Fatal(err)
	}
	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		var owned *types.BucketAlreadyOwnedByYou
		if !errors.As(err, &owned) {
			t.Fatalf("creating test bucket %s on %s: %v", bucket, endpoint, err)
		}
	}

	store, err := blob.New(ctx, config.BlobConfig{Bucket: bucket, Region: "us-east-1", Endpoint: endpoint}, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(blobCheckPayload)
	want := blobCheckOK + " " + hex.EncodeToString(sum[:]) + "\n"

	for run := 1; run <= 2; run++ {
		var out bytes.Buffer
		if err := blobCheck(ctx, store, &out); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		if out.String() != want {
			t.Fatalf("run %d printed %q, want %q", run, out.String(), want)
		}
	}

	listed, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String("blobs/")})
	if err != nil {
		t.Fatal(err)
	}
	if n := aws.ToInt32(listed.KeyCount); n != 1 {
		t.Errorf("the bucket holds %d objects after two checks, want 1: a repeated check must store nothing", n)
	}
}
