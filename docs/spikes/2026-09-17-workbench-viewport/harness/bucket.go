//go:build ignore

// Creates the local MinIO bucket forged and forge-worker keep exports in, the way
// TestExports_RequestJobWorkerBlobStatusAndDownloadAgree creates its own.
//
//	. env.sh && go run docs/spikes/2026-09-17-workbench-viewport/harness/bucket.go
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

func main() {
	ctx := context.Background()
	bucket, endpoint := os.Getenv("FORGE_BLOB_BUCKET"), os.Getenv("FORGE_BLOB_ENDPOINT")
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(os.Getenv("FORGE_BLOB_REGION")))
	if err != nil {
		panic(err)
	}
	client := s3.NewFromConfig(cfg, func(o *s3.Options) { o.BaseEndpoint = aws.String(endpoint); o.UsePathStyle = true })
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		var owned *types.BucketAlreadyOwnedByYou
		if !errors.As(err, &owned) {
			panic(err)
		}
	}
	fmt.Println("bucket", bucket, "ready on", endpoint)
}
