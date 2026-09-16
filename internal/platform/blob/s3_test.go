package blob

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

// These run against a REAL S3-compatible server — MinIO locally (`make blob-up`)
// and in CI — through the same SDK path production uses against AWS. A fake
// S3 would be asserting what the test author believes S3 does, and the whole
// point is conditional writes, checksums and error shapes, which are properties
// of the server.
func minio(t *testing.T) *s3Store {
	t.Helper()
	endpoint := os.Getenv("FORGE_TEST_BLOB_ENDPOINT")
	if endpoint == "" {
		t.Skip("FORGE_TEST_BLOB_ENDPOINT is unset; skipping the blob store tests against MinIO. " +
			"Run `make blob-up` and `make test-blob`")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	bucket := "forge-test-" + strings.ToLower(strings.ReplaceAll(t.Name(), "_", "-"))
	if len(bucket) > 63 {
		bucket = bucket[:63]
	}
	bucket = strings.TrimRight(bucket, "-")
	st, err := NewS3(ctx, Options{Bucket: bucket, Region: "us-east-1", Endpoint: endpoint}, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	s := st.(*s3Store)
	if _, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		var owned *types.BucketAlreadyOwnedByYou
		if !errors.As(err, &owned) {
			t.Fatalf("creating test bucket %s on %s: %v", bucket, endpoint, err)
		}
	}
	return s
}

func readAll(t *testing.T, s Store, k Key) ([]byte, error) {
	t.Helper()
	rc, err := s.Get(context.Background(), k)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(rc)
}

func TestS3_ARoundTripReturnsTheSameBytesUnderTheirHash(t *testing.T) {
	s := minio(t)
	ctx := context.Background()
	payload := []byte("a per-design mesh, pretending")

	k, err := s.Put(ctx, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	want, _, _ := KeyOf(bytes.NewReader(payload))
	if k != want {
		t.Fatalf("Put returned %s, want the content hash %s", k, want)
	}
	got, err := readAll(t, s, k)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("read back %q, %v", got, err)
	}
}

// Storing the same bytes twice is not an error and changes nothing.
func TestS3_StoringTheSameBytesTwiceIsANoOp(t *testing.T) {
	s := minio(t)
	ctx := context.Background()
	payload := []byte("shared by two versions of an assembly")

	first, err := s.Put(ctx, bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Put(ctx, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("the second Put of identical bytes failed: %v", err)
	}
	if first != second {
		t.Errorf("identical bytes got two keys: %s and %s", first, second)
	}
	if there, err := s.Has(ctx, first); err != nil || !there {
		t.Errorf("Has = %v, %v after Put", there, err)
	}
}

func TestS3_AMissingBlobIsNotFoundNotAnOutage(t *testing.T) {
	s := minio(t)
	absent, _, _ := KeyOf(strings.NewReader("never stored"))
	if there, err := s.Has(context.Background(), absent); err != nil || there {
		t.Errorf("Has on an absent blob = %v, %v; want false, nil", there, err)
	}
	if _, err := readAll(t, s, absent); errs.CodeOf(err) != errs.CodeNotFound {
		t.Errorf("Get on an absent blob = %v, want %s — a miss must not read as the bucket being down",
			err, errs.CodeNotFound)
	}
}

// Bytes changed behind the store's back are refused, not served.
func TestS3_ACorruptedObjectIsRefusedOnRead(t *testing.T) {
	s := minio(t)
	ctx := context.Background()
	k, err := s.Put(ctx, bytes.NewReader([]byte("the bytes that were stored")))
	if err != nil {
		t.Fatal(err)
	}
	// Overwrite the object directly, without the store and without the condition.
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(s.bucket), Key: aws.String(objectName(k)),
		Body: bytes.NewReader([]byte("something else entirely")),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := readAll(t, s, k); errs.CodeOf(err) != errs.CodeStateCorrupt {
		t.Fatalf("reading tampered bytes ended with %v, want %s", err, errs.CodeStateCorrupt)
	}
}

// A file far larger than a JSON field is stored from disk, streamed, not loaded.
func TestS3_AFileIsStoredFromDisk(t *testing.T) {
	s := minio(t)
	path := filepath.Join(t.TempDir(), "export.step")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(f, rand.Reader, 8<<20); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	k, err := s.Put(context.Background(), f)
	if err != nil {
		t.Fatalf("storing an 8 MB file failed: %v", err)
	}
	got, err := readAll(t, s, k)
	if err != nil || len(got) != 8<<20 {
		t.Fatalf("read back %d bytes, %v", len(got), err)
	}
}
