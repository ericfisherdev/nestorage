package adapter_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"

	"github.com/ericfisherdev/nestorage/internal/media/adapter"
	"github.com/ericfisherdev/nestorage/internal/media/domain"
)

// s3TestEndpointEnv gates every test in this file: unset means "skip
// hermetically" (`make test` must stay dependency-free), mirroring
// NESTORAGE_TEST_DATABASE_URL's identical gate for the Postgres-backed
// suite. Point it at a disposable MinIO container — see docs/testing.md, or:
//
//	docker compose up -d minio
//	export NESTORAGE_TEST_S3_ENDPOINT=http://127.0.0.1:59001
//	export NESTORAGE_TEST_S3_ACCESS_KEY_ID=nestorage
//	export NESTORAGE_TEST_S3_SECRET_ACCESS_KEY=nestorage-dev-minio
//	go test ./internal/media/adapter/...
const s3TestEndpointEnv = "NESTORAGE_TEST_S3_ENDPOINT"

// s3TestAccessKeyEnv / s3TestSecretKeyEnv name the environment variables
// carrying the disposable MinIO container's credentials — read at test time
// only, never hardcoded here (Sonar secrets:S6698 is a BLOCKER on this
// public repo; see compose.yaml/.env.example for where the actual dev-only
// values live).
const (
	s3TestAccessKeyEnv = "NESTORAGE_TEST_S3_ACCESS_KEY_ID"
	s3TestSecretKeyEnv = "NESTORAGE_TEST_S3_SECRET_ACCESS_KEY"
	s3TestRegion       = "us-east-1"
	s3TestPresignTTL   = 5 * time.Minute
)

// requireS3Env skips the calling test unless s3TestEndpointEnv is set,
// otherwise returns it alongside whatever credentials are configured
// (possibly blank, which NewS3PhotoStore itself treats as "use the SDK's
// default credential chain" — not exercised by this file, whose disposable
// MinIO container always has static credentials).
func requireS3Env(t *testing.T) (endpoint, accessKey, secretKey string) {
	t.Helper()
	endpoint = os.Getenv(s3TestEndpointEnv)
	if endpoint == "" {
		t.Skipf("set %s to run the S3 photo store tests against MinIO (see docs/testing.md)", s3TestEndpointEnv)
	}
	return endpoint, os.Getenv(s3TestAccessKeyEnv), os.Getenv(s3TestSecretKeyEnv)
}

// newTestBucket creates a uniquely-named bucket against endpoint (so
// parallel/sequential test runs never collide) and registers cleanup to
// empty and delete it afterward.
func newTestBucket(t *testing.T, endpoint, accessKey, secretKey string) string {
	t.Helper()
	bucket := fmt.Sprintf("nstr35-test-%d", time.Now().UnixNano())
	ctx := context.Background()
	client := rawTestS3Client(ctx, t, endpoint, accessKey, secretKey)
	if _, err := client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)}); err != nil {
		t.Fatalf("create test bucket %q: %v", bucket, err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		emptyTestBucket(cleanupCtx, t, client, bucket)
		if _, err := client.DeleteBucket(cleanupCtx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)}); err != nil {
			t.Logf("cleanup: delete test bucket %q: %v", bucket, err)
		}
	})
	return bucket
}

// rawTestS3Client builds a plain *s3.Client against endpoint — used only for
// test-fixture bucket setup/teardown, which NewS3PhotoStore does not expose
// (it manages a bucket that already exists, not bucket lifecycle).
func rawTestS3Client(ctx context.Context, t *testing.T, endpoint, accessKey, secretKey string) *s3.Client {
	t.Helper()
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(s3TestRegion),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(accessKey, secretKey, "")),
	)
	if err != nil {
		t.Fatalf("load AWS config for raw test client: %v", err)
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(endpoint)
		o.UsePathStyle = true
	})
}

func emptyTestBucket(ctx context.Context, t *testing.T, client *s3.Client, bucket string) {
	t.Helper()
	paginator := s3.NewListObjectsV2Paginator(client, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			t.Logf("cleanup: list objects in %q: %v", bucket, err)
			return
		}
		for _, obj := range page.Contents {
			if _, err := client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: obj.Key}); err != nil {
				t.Logf("cleanup: delete object %q: %v", aws.ToString(obj.Key), err)
			}
		}
	}
}

// newTestS3Store creates a fresh bucket and returns an adapter.S3PhotoStore
// against it with presignTTL as its configured default.
func newTestS3Store(t *testing.T, presignTTL time.Duration) *adapter.S3PhotoStore {
	t.Helper()
	endpoint, accessKey, secretKey := requireS3Env(t)
	bucket := newTestBucket(t, endpoint, accessKey, secretKey)
	store, err := adapter.NewS3PhotoStore(context.Background(), adapter.S3Params{
		Endpoint: endpoint, Region: s3TestRegion, Bucket: bucket,
		AccessKeyID: accessKey, SecretAccessKey: secretKey,
		UsePathStyle: true, PresignTTL: presignTTL,
	})
	if err != nil {
		t.Fatalf("NewS3PhotoStore: %v", err)
	}
	return store
}

// TestS3PhotoStore_Conformance reruns the shared PhotoStore port suite
// (conformance_test.go) against S3PhotoStore/real MinIO — Sprint 5
// reconciliation's AC 4: the same suite passes against both adapters,
// asserting storage behavior only (byte-identical read-back, key layout,
// item-scoped dedup, idempotent delete/not-found open).
func TestS3PhotoStore_Conformance(t *testing.T) {
	RunPhotoStoreConformance(t, func(t *testing.T) domain.PhotoStore {
		return newTestS3Store(t, s3TestPresignTTL)
	})
}

// TestS3PhotoStore_URLReturnsFetchablePresignedGet covers AC 1: an upload
// lands in the bucket and URL returns a real, working presigned GET — not
// just a string, but one a plain HTTP client can actually fetch the exact
// uploaded bytes from.
func TestS3PhotoStore_URLReturnsFetchablePresignedGet(t *testing.T) {
	store := newTestS3Store(t, s3TestPresignTTL)
	itemID := storagedomain.NewItemID()
	data := jpegBytes(t)
	meta := computeMeta(data, domain.ContentTypeJPEG)

	ref, err := store.Put(context.Background(), itemID, meta, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if !store.SupportsDirectURL() {
		t.Fatal("SupportsDirectURL() = false, want true for S3PhotoStore")
	}

	presigned, err := store.URL(context.Background(), ref, 5*time.Minute)
	if err != nil {
		t.Fatalf("URL: %v", err)
	}
	if !strings.Contains(presigned, "X-Amz-Signature") {
		t.Fatalf("URL = %q, want a presigned URL with a signature query parameter", presigned)
	}

	resp, err := http.Get(presigned) //nolint:noctx // test-only fetch of a presigned URL
	if err != nil {
		t.Fatalf("fetch presigned URL: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET presigned URL: status=%d, want 200", resp.StatusCode)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read presigned URL response: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("presigned URL served %d bytes, want %d matching bytes", len(got), len(data))
	}
}

// TestS3PhotoStore_URLUnknownRefNotFound covers URL's HeadObject existence
// check: presigning alone never verifies existence, so an unknown ref must
// still report ErrPhotoNotFound rather than a URL for an object that was
// never written.
func TestS3PhotoStore_URLUnknownRefNotFound(t *testing.T) {
	store := newTestS3Store(t, s3TestPresignTTL)
	if _, err := store.URL(context.Background(), domain.StorageRef("items/nope/deadbeef.jpg"), 5*time.Minute); !errors.Is(err, domain.ErrPhotoNotFound) {
		t.Fatalf("URL(unknown) error = %v, want ErrPhotoNotFound", err)
	}
}

// TestS3PhotoStore_URLExpiresAfterTTL covers AC 2: a presigned URL minted
// with a short TTL stops working once that TTL elapses. A real (short)
// sleep is unavoidable here — unlike the reaper's own injectable clock,
// presigned-URL expiry is enforced by SigV4 signature verification against
// real wall-clock time on the MinIO/S3 server side.
func TestS3PhotoStore_URLExpiresAfterTTL(t *testing.T) {
	store := newTestS3Store(t, s3TestPresignTTL)
	itemID := storagedomain.NewItemID()
	data := pngBytes(t)
	meta := computeMeta(data, domain.ContentTypePNG)

	ref, err := store.Put(context.Background(), itemID, meta, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	const shortTTL = 2 * time.Second
	presigned, err := store.URL(context.Background(), ref, shortTTL)
	if err != nil {
		t.Fatalf("URL: %v", err)
	}

	resp, err := http.Get(presigned) //nolint:noctx // test-only fetch of a presigned URL
	if err != nil {
		t.Fatalf("fetch presigned URL before expiry: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET presigned URL before expiry: status=%d, want 200", resp.StatusCode)
	}

	time.Sleep(shortTTL + 2*time.Second)

	resp2, err := http.Get(presigned) //nolint:noctx // test-only fetch of a presigned URL
	if err != nil {
		t.Fatalf("fetch presigned URL after expiry: %v", err)
	}
	defer func() { _ = resp2.Body.Close() }()
	if resp2.StatusCode == http.StatusOK {
		t.Fatalf("GET presigned URL after its %v TTL elapsed: status=200, want a non-200 (expired signature)", shortTTL)
	}
}

// TestS3PhotoStore_URLNonPositiveTTLFallsBackToConfiguredDefault covers AC
// 2's other half: a non-positive ttl falls back to the store's own
// configured PresignTTL — asserted against the presigned URL's own
// X-Amz-Expires query parameter, the SigV4 signature's actual validity
// window, rather than waiting out a real TTL.
func TestS3PhotoStore_URLNonPositiveTTLFallsBackToConfiguredDefault(t *testing.T) {
	const configuredTTL = 10 * time.Minute
	store := newTestS3Store(t, configuredTTL)
	itemID := storagedomain.NewItemID()
	data := jpegBytes(t)
	meta := computeMeta(data, domain.ContentTypeJPEG)

	ref, err := store.Put(context.Background(), itemID, meta, bytes.NewReader(data))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	presigned, err := store.URL(context.Background(), ref, 0)
	if err != nil {
		t.Fatalf("URL(ttl=0): %v", err)
	}
	parsed, err := url.Parse(presigned)
	if err != nil {
		t.Fatalf("parse presigned URL: %v", err)
	}
	got := parsed.Query().Get("X-Amz-Expires")
	want := strconv.Itoa(int(configuredTTL.Seconds()))
	if got != want {
		t.Fatalf("URL(ttl=0) X-Amz-Expires = %q, want %q (the configured default)", got, want)
	}
}

// TestS3PhotoStore_PutUnsupportedContentTypeRejected proves Put itself still
// guards meta.ContentType (it builds the object key's extension from it),
// mirroring LocalPhotoStore's identical guard — both share
// extensionForContentType (photo_validation.go), so the two can never drift
// apart on which types are accepted.
func TestS3PhotoStore_PutUnsupportedContentTypeRejected(t *testing.T) {
	store := newTestS3Store(t, s3TestPresignTTL)
	meta := domain.PutMeta{ContentHash: "deadbeef", SizeBytes: 3, ContentType: "image/webp"}
	if _, err := store.Put(context.Background(), storagedomain.NewItemID(), meta, bytes.NewReader([]byte("abc"))); !errors.Is(err, domain.ErrUnsupportedMediaType) {
		t.Fatalf("Put(unsupported content type) = %v, want ErrUnsupportedMediaType", err)
	}
}

// TestNewS3PhotoStore_MissingBucketFailsFast covers AC 3: a bucket that was
// never created fails construction (HeadBucket) with a clear error, not on
// first upload.
func TestNewS3PhotoStore_MissingBucketFailsFast(t *testing.T) {
	endpoint, accessKey, secretKey := requireS3Env(t)
	_, err := adapter.NewS3PhotoStore(context.Background(), adapter.S3Params{
		Endpoint: endpoint, Region: s3TestRegion, Bucket: fmt.Sprintf("nstr35-never-created-%d", time.Now().UnixNano()),
		AccessKeyID: accessKey, SecretAccessKey: secretKey,
		UsePathStyle: true, PresignTTL: s3TestPresignTTL,
	})
	if err == nil {
		t.Fatal("NewS3PhotoStore(missing bucket) error = nil, want a fail-fast error")
	}
}

// TestNewS3PhotoStore_BadCredentialsFailsFast covers AC 3's other half:
// wrong credentials against a bucket that DOES exist still fail
// construction, rather than succeeding and failing later on first use.
func TestNewS3PhotoStore_BadCredentialsFailsFast(t *testing.T) {
	endpoint, accessKey, secretKey := requireS3Env(t)
	bucket := newTestBucket(t, endpoint, accessKey, secretKey)
	_, err := adapter.NewS3PhotoStore(context.Background(), adapter.S3Params{
		Endpoint: endpoint, Region: s3TestRegion, Bucket: bucket,
		AccessKeyID: "wrong-access-key-id", SecretAccessKey: "wrong-secret-access-key",
		UsePathStyle: true, PresignTTL: s3TestPresignTTL,
	})
	if err == nil {
		t.Fatal("NewS3PhotoStore(bad credentials) error = nil, want a fail-fast error")
	}
}

// TestNewS3PhotoStore_ValidationErrors covers NewS3PhotoStore's own
// parameter checks — these never reach the network, so they run in every
// `make test` invocation, not only the S3-gated suite.
func TestNewS3PhotoStore_ValidationErrors(t *testing.T) {
	valid := adapter.S3Params{Region: "us-east-1", Bucket: "nestorage-photos", PresignTTL: time.Minute}

	t.Run("blank bucket rejected", func(t *testing.T) {
		p := valid
		p.Bucket = "   "
		if _, err := adapter.NewS3PhotoStore(context.Background(), p); err == nil {
			t.Error("NewS3PhotoStore(blank bucket) error = nil, want an error")
		}
	})
	t.Run("blank region rejected", func(t *testing.T) {
		p := valid
		p.Region = "   "
		if _, err := adapter.NewS3PhotoStore(context.Background(), p); err == nil {
			t.Error("NewS3PhotoStore(blank region) error = nil, want an error")
		}
	})
	t.Run("non-positive presign ttl rejected", func(t *testing.T) {
		p := valid
		p.PresignTTL = 0
		if _, err := adapter.NewS3PhotoStore(context.Background(), p); err == nil {
			t.Error("NewS3PhotoStore(presign ttl=0) error = nil, want an error")
		}
	})
}
