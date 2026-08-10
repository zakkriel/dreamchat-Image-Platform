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
	Bucket   string
	Region   string
	Endpoint string
	// PublicEndpoint is the browser/client-reachable origin used ONLY when
	// signing presigned read URLs. Leave empty when the service and its clients
	// reach the object store at the same address (the common case) — presigning
	// then uses Endpoint. Set it when they differ: locally the API and worker
	// talk to MinIO at http://minio:9000 (a Docker network name) while the
	// browser can only reach http://localhost:9000, and in production a bucket
	// written through an internal endpoint may be read through a CDN origin.
	// SigV4 signs the Host header, so a presigned URL cannot be rewritten after
	// the fact — it must be signed for the host the client will actually call.
	PublicEndpoint string
	// ReferenceEndpoint is the origin used when signing reference images handed
	// to an external provider that fetches them itself (fal). Empty falls back
	// to PublicEndpoint, then Endpoint — so a deployment whose single origin is
	// publicly reachable (R2/CDN) needs no extra configuration, while local dev
	// can deliver over http://localhost:9000 and still hand fal a tunnel URL.
	ReferenceEndpoint string
	AccessKeyID       string
	SecretAccessKey   string
	UsePathStyle      bool
}

type s3Storage struct {
	bucket string
	client *s3.Client
	// presign signs against PublicEndpoint when set, otherwise against the same
	// client as writes. It is a separate client precisely because the signature
	// is bound to the endpoint host.
	presign *s3.PresignClient
	// presignRef signs reference URLs for an external provider's own fetcher;
	// it equals presign unless ReferenceEndpoint differs.
	presignRef *s3.PresignClient
}

// NewS3Storage builds the S3 client per ADR-011. Honors S3_ENDPOINT and
// S3_USE_PATH_STYLE so MinIO and R2 both work without code changes, and
// S3_PUBLIC_ENDPOINT so presigned read URLs can be signed for a different,
// client-reachable origin than the one used for writes.
func NewS3Storage(ctx context.Context, cfg S3Config) (Storage, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")),
	)
	if err != nil {
		return nil, fmt.Errorf("storage: load aws config: %w", err)
	}

	newClient := func(endpoint string) *s3.Client {
		opts := []func(*s3.Options){
			func(o *s3.Options) {
				o.UsePathStyle = cfg.UsePathStyle
			},
		}
		if endpoint != "" {
			opts = append(opts, func(o *s3.Options) {
				o.BaseEndpoint = aws.String(endpoint)
			})
		}
		return s3.NewFromConfig(awsCfg, opts...)
	}

	client := newClient(cfg.Endpoint)
	presignClient := client
	if cfg.PublicEndpoint != "" && cfg.PublicEndpoint != cfg.Endpoint {
		presignClient = newClient(cfg.PublicEndpoint)
	}
	// Reference signing falls back to the delivery presign client, so behavior is
	// unchanged for every deployment that does not set ReferenceEndpoint.
	refClient := presignClient
	refEndpoint := cfg.ReferenceEndpoint
	if refEndpoint != "" && refEndpoint != cfg.PublicEndpoint {
		refClient = newClient(refEndpoint)
	}
	return &s3Storage{
		bucket:     cfg.Bucket,
		client:     client,
		presign:    s3.NewPresignClient(presignClient),
		presignRef: s3.NewPresignClient(refClient),
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
// valid for ttl, addressed to the CALLER-reachable origin (S3_PUBLIC_ENDPOINT,
// falling back to S3_ENDPOINT). The signing is purely local (no network
// round-trip) and honors S3_USE_PATH_STYLE, so the URL works against MinIO
// (path-style) and R2 alike. The URL is computed per request from a
// deterministic object key and is never persisted.
func (s *s3Storage) Presign(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return sign(ctx, s.presign, s.bucket, key, ttl)
}

// PresignForProvider mints the same kind of URL as Presign but addressed to
// S3_REFERENCE_ENDPOINT — the origin an EXTERNAL provider's servers can reach.
// fal downloads reference `image_urls` itself, so a delivery URL pointing at
// localhost would fail with file_download_error. Falls back to the delivery
// presign client when no separate reference origin is configured.
func (s *s3Storage) PresignForProvider(ctx context.Context, key string, ttl time.Duration) (string, error) {
	return sign(ctx, s.presignRef, s.bucket, key, ttl)
}

func sign(ctx context.Context, p *s3.PresignClient, bucket, key string, ttl time.Duration) (string, error) {
	if ttl <= 0 {
		ttl = defaultPresignTTL
	}
	req, err := p.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("storage: presign %s: %w", key, err)
	}
	return req.URL, nil
}
