package blob

import (
	"context"
	"testing"

	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/config"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/errs"
	"github.com/damonleelcx/J.A.R.V.I.S.-agent/internal/platform/logx"
)

func TestNew_NoBucketIsTheRefusingStoreNotNil(t *testing.T) {
	s, err := New(context.Background(), config.BlobConfig{}, logx.Discard())
	if err != nil || s == nil {
		t.Fatalf("New with no bucket = %v, %v; want the refusing store", s, err)
	}
	if s.Available() {
		t.Error("a deployment with no bucket reports blob storage as available")
	}
}

func TestNew_ABucketWithoutARegionIsRefused(t *testing.T) {
	_, err := New(context.Background(), config.BlobConfig{Bucket: "b"}, logx.Discard())
	if errs.CodeOf(err) != errs.CodeConfigInvalid {
		t.Fatalf("a bucket with no region = %v, want %s", err, errs.CodeConfigInvalid)
	}
}

// Constructing the store makes no request, so a configured deployment boots
// even while S3 is unreachable; the first call is what reports that.
func TestNew_AConfiguredStoreIsAvailableWithoutARequest(t *testing.T) {
	s, err := New(context.Background(),
		config.BlobConfig{Bucket: "b", Region: "us-east-1", Endpoint: "http://127.0.0.1:1"}, logx.Discard())
	if err != nil {
		t.Fatal(err)
	}
	if !s.Available() {
		t.Error("a configured store is not available")
	}
}
