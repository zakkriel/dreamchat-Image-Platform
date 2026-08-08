package storage

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Presign is purely local signing (no network), so it is exercised as a unit
// test: it never connects to the endpoint. These assertions cover unit test #1
// (https URL for a derived key, TTL honored, path-style honored for MinIO).

func newTestStorage(t *testing.T, endpoint string, pathStyle bool) Storage {
	t.Helper()
	store, err := NewS3Storage(context.Background(), S3Config{
		Bucket:          "image-platform",
		Region:          "us-east-1",
		Endpoint:        endpoint,
		AccessKeyID:     "testkey",
		SecretAccessKey: "testsecret",
		UsePathStyle:    pathStyle,
	})
	if err != nil {
		t.Fatalf("NewS3Storage: %v", err)
	}
	return store
}

func TestPresignReturnsHTTPSSignedURLForDerivedKey(t *testing.T) {
	// An https endpoint (R2/S3-style) yields an https presigned URL.
	store := newTestStorage(t, "https://s3.example.com", false)
	key := ObjectKey("asset_presign", VariantHigh, "png")

	got, err := store.Presign(context.Background(), key, 15*time.Minute)
	if err != nil {
		t.Fatalf("Presign: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse presigned url %q: %v", got, err)
	}
	if u.Scheme != "https" {
		t.Fatalf("expected https URL, got scheme %q (%s)", u.Scheme, got)
	}
	if !strings.Contains(u.Path, key) {
		t.Fatalf("presigned URL must reference the derived key %q, got path %q", key, u.Path)
	}
	q := u.Query()
	if q.Get("X-Amz-Signature") == "" {
		t.Fatalf("presigned URL must carry a signature, got %s", got)
	}
	if q.Get("X-Amz-Expires") != "900" {
		t.Fatalf("expected X-Amz-Expires=900 (15m), got %q", q.Get("X-Amz-Expires"))
	}
}

func TestPresignHonorsPathStyleForMinIO(t *testing.T) {
	// Path-style (MinIO) puts the bucket in the path, not the host.
	store := newTestStorage(t, "http://localhost:9000", true)
	key := ObjectKey("asset_minio", VariantThumb, "png")

	got, err := store.Presign(context.Background(), key, time.Minute)
	if err != nil {
		t.Fatalf("Presign: %v", err)
	}
	u, _ := url.Parse(got)
	if u.Host != "localhost:9000" {
		t.Fatalf("path-style must keep the endpoint host, got %q", u.Host)
	}
	if !strings.HasPrefix(u.Path, "/image-platform/") {
		t.Fatalf("path-style must put the bucket in the path, got %q", u.Path)
	}
	if !strings.Contains(u.Path, key) {
		t.Fatalf("presigned URL must reference the derived key, got %q", u.Path)
	}
}

func TestPresignDifferentKeysDifferentURLs(t *testing.T) {
	store := newTestStorage(t, "https://s3.example.com", false)
	a, err := store.Presign(context.Background(), ObjectKey("asset_z", VariantHigh, "png"), time.Minute)
	if err != nil {
		t.Fatalf("presign a: %v", err)
	}
	b, err := store.Presign(context.Background(), ObjectKey("asset_z", VariantThumb, "png"), time.Minute)
	if err != nil {
		t.Fatalf("presign b: %v", err)
	}
	if a == b {
		t.Fatal("distinct object keys must produce distinct presigned URLs")
	}
}

func TestPresignZeroTTLFallsBackToDefault(t *testing.T) {
	store := newTestStorage(t, "https://s3.example.com", false)
	got, err := store.Presign(context.Background(), ObjectKey("asset_def", VariantLow, "png"), 0)
	if err != nil {
		t.Fatalf("Presign: %v", err)
	}
	u, _ := url.Parse(got)
	if exp := u.Query().Get("X-Amz-Expires"); exp != "900" {
		t.Fatalf("zero ttl must fall back to the 15m default (900s), got %q", exp)
	}
}

// TestPresignUsesPublicEndpointWhileWritesUseInternal is the split-endpoint
// contract: a presigned read URL is signed for the CLIENT-reachable origin
// (S3_PUBLIC_ENDPOINT) while writes still go to the internal one. SigV4 signs
// the Host header, so this cannot be done by rewriting the URL afterwards —
// the presign client must be built against the public endpoint.
func TestPresignUsesPublicEndpointWhileWritesUseInternal(t *testing.T) {
	var gotWriteHost, gotWritePath string
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotWriteHost, gotWritePath = r.Host, r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer internal.Close()

	store, err := NewS3Storage(context.Background(), S3Config{
		Bucket:          "image-platform",
		Region:          "us-east-1",
		Endpoint:        internal.URL,
		PublicEndpoint:  "http://localhost:9000",
		AccessKeyID:     "testkey",
		SecretAccessKey: "testsecret",
		UsePathStyle:    true,
	})
	if err != nil {
		t.Fatalf("NewS3Storage: %v", err)
	}

	key := ObjectKey("asset_split", VariantHigh, "png")
	if _, err := store.Put(context.Background(), key, []byte("png"), "image/png"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if want := strings.TrimPrefix(internal.URL, "http://"); gotWriteHost != want {
		t.Fatalf("writes must target the internal endpoint %q, got %q", want, gotWriteHost)
	}
	if !strings.Contains(gotWritePath, key) {
		t.Fatalf("write path must reference the key %q, got %q", key, gotWritePath)
	}

	got, err := store.Presign(context.Background(), key, time.Minute)
	if err != nil {
		t.Fatalf("Presign: %v", err)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse presigned url %q: %v", got, err)
	}
	if u.Host != "localhost:9000" {
		t.Fatalf("presigned URL must be signed for the public endpoint, got host %q (%s)", u.Host, got)
	}
	if u.Query().Get("X-Amz-Signature") == "" {
		t.Fatalf("presigned URL must carry a signature, got %s", got)
	}
}

// An empty PublicEndpoint keeps the previous behavior: presign against the same
// endpoint as writes.
func TestPresignFallsBackToEndpointWhenPublicUnset(t *testing.T) {
	store := newTestStorage(t, "http://minio:9000", true)
	got, err := store.Presign(context.Background(), ObjectKey("asset_fb", VariantLow, "png"), time.Minute)
	if err != nil {
		t.Fatalf("Presign: %v", err)
	}
	u, _ := url.Parse(got)
	if u.Host != "minio:9000" {
		t.Fatalf("unset public endpoint must presign against S3_ENDPOINT, got %q", u.Host)
	}
}
