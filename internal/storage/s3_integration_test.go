//go:build integration

package storage_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/zakkriel/drchat-image-platform/internal/storage"
)

// To run:
//   S3_BUCKET=image-platform S3_REGION=us-east-1 \
//   S3_ENDPOINT=http://localhost:9000 \
//   S3_ACCESS_KEY_ID=minioadmin S3_SECRET_ACCESS_KEY=minioadmin \
//   S3_USE_PATH_STYLE=true \
//   go test -tags=integration ./internal/storage/...

func TestS3StoragePutAndCanonicalURL(t *testing.T) {
	bucket := os.Getenv("S3_BUCKET")
	region := os.Getenv("S3_REGION")
	endpoint := os.Getenv("S3_ENDPOINT")
	access := os.Getenv("S3_ACCESS_KEY_ID")
	secret := os.Getenv("S3_SECRET_ACCESS_KEY")
	if bucket == "" || region == "" || endpoint == "" || access == "" || secret == "" {
		t.Skip("S3 env vars not set; skipping integration test")
	}

	ctx := context.Background()
	store, err := storage.NewS3Storage(ctx, storage.S3Config{
		Bucket:          bucket,
		Region:          region,
		Endpoint:        endpoint,
		AccessKeyID:     access,
		SecretAccessKey: secret,
		UsePathStyle:    true,
	})
	if err != nil {
		t.Fatalf("NewS3Storage: %v", err)
	}

	key := storage.ObjectKey("asset_it_test", storage.VariantHigh, "png")
	body := []byte("\x89PNG\r\n\x1a\n" + strings.Repeat("x", 32))
	url, err := store.Put(ctx, key, body, "image/png")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	want := "s3://" + bucket + "/" + key
	if url != want {
		t.Fatalf("expected url %q, got %q", want, url)
	}

	// Verify the object actually exists by reading it back via a raw S3 client.
	cfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(access, secret, "")),
	)
	if err != nil {
		t.Fatalf("LoadDefaultConfig: %v", err)
	}
	cli := s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.UsePathStyle = true
		o.BaseEndpoint = aws.String(endpoint)
	})
	out, err := cli.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("HeadObject: %v", err)
	}
	if out.ContentLength == nil || *out.ContentLength == 0 {
		t.Fatalf("expected non-empty object")
	}
}
