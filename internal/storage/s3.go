package storage

import (
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// defaultPresignTTL bounds a read URL when the caller passes a non-positive
// ttl, so a misconfiguration can never mint a never-expiring URL.
const defaultPresignTTL = 15 * time.Minute

// S3Config carries the env-driven settings needed to build the client.
type S3Config struct {
	Bucket          string
	Region          string
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	UsePathStyle    bool
}

type s3Storage struct {
	bucket  string
	client  *s3.Client
	presign *s3.PresignClient
}

// NewS3Storage builds the S3 client per ADR-011. Honors S3_ENDPOINT and
// S3_USE_PATH_STYLE so MinIO and R2 both work without code changes.
func NewS3Storage(ctx context.Context, cfg S3Config) (Storage, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("storage: load aws config: %w", err)
	}

	opts := []func(*s3.Options){
		func(o *s3.Options) {
			o.UsePathStyle = cfg.UsePathStyle
		},
	}
	if cfg.Endpoint != "" {
		endpoint := cfg.Endpoint
		opts = append(opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
		})
	}

	client := s3.NewFromConfig(awsCfg, opts...)
	return &s3Storage{
		bucket:  cfg.Bucket,
		client:  client,
		presign: s3.NewPresignClient(client),
	}, nil
}

func (s *s3Storage) Put(ctx context.Context, key string, body []byte, contentType string) (string, error) {
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(body),
		ContentType: aws.String(contentType),
	})
	if err != nil {
		return "", fmt.Errorf("storage: put %s: %w", key, err)
	}
	return CanonicalURL(s.bucket, key), nil
}

// Presign mints a time-limited authenticated GET URL for the object at key,
// valid for ttl. The signing is purely local (no network round-trip) and
// honors the same S3_ENDPOINT / S3_USE_PATH_STYLE settings as Put, so the URL
// works against MinIO (path-style) and R2 alike. The URL is computed per
// request from a deterministic object key and is never persisted.
func (s *s3Storage) Presign(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = defaultPresignTTL
	}
	req, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("storage: presign %s: %w", key, err)
	}
	return req.URL, nil
}
