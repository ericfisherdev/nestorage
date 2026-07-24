package adapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"

	// feature/s3/manager.Uploader is flagged deprecated in favor of the
	// newer feature/s3/transfermanager — a DELIBERATE choice to stay on it
	// anyway, mirroring Nestova's identical NES-132 justification:
	// transfermanager is still pre-1.0 (v0.x, breaking-change-prone), while
	// manager is a stable, widely used v1.x API with no functional gap for
	// this adapter's needs (streaming a single already-staged upload,
	// multipart only when it is large). Revisit once transfermanager
	// reaches 1.0.
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager" //nolint:staticcheck // SA1019: see above
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"

	"github.com/ericfisherdev/nestorage/internal/media/domain"
)

// S3Params configures NewS3PhotoStore. It mirrors config.MediaConfig.S3
// field-for-field but is its own type: this adapter package depends on
// configuration only through the composition root
// (internal/media/bootstrap) passing plain values in, never by importing
// internal/platform/config directly (DIP — adapter and platform/config stay
// peer packages, neither depending on the other).
type S3Params struct {
	// Endpoint is the S3-compatible API's base URL; blank targets real AWS
	// S3's regional default endpoint. A custom endpoint (MinIO/Garage on the
	// LAN) is a first-class target, not an afterthought.
	Endpoint string
	// Region is required (AWS S3 needs a real one; most self-hosted
	// S3-compatible servers accept any non-empty value).
	Region string
	// Bucket is the bucket every photo is stored under.
	Bucket string
	// AccessKeyID / SecretAccessKey are optional static credentials; when
	// both are blank, the AWS SDK's default credential chain (environment,
	// shared config/credentials file, EC2/ECS instance role, ...) supplies
	// credentials instead.
	AccessKeyID     string
	SecretAccessKey string
	// UsePathStyle forces path-style bucket addressing, required by MinIO
	// and most self-hosted S3-compatible servers.
	UsePathStyle bool
	// PresignTTL is URL's applied default when a caller passes a
	// non-positive ttl.
	PresignTTL time.Duration
}

// S3PhotoStore is a domain.PhotoStore backed by an S3-compatible object
// store — AWS S3, or a self-hosted MinIO/Garage endpoint on the LAN.
// Photos use the identical content-addressed, item-scoped key layout
// LocalPhotoStore uses (see buildStorageKey) — StorageRef IS the S3 object
// key verbatim, so a ref means the same thing regardless of which backend
// stored it.
//
// Unlike Nestova's reference S3PhotoStore (NES-132), Put here never sniffs,
// size-caps, or hashes: Sprint 5's reconciliation (R1) moved that whole
// stage/validate/scrub/hash pipeline into internal/media/app ahead of
// PhotoStore.Put ever being called, so Put trusts the caller-supplied
// domain.PutMeta completely and streams r straight through. Open is
// similarly simpler: it returns the GetObject response body directly
// (sequential-only, matching the port's own contract) rather than buffering
// the whole object into memory the way Nestova's adapter must for its own
// EXIF-on-reopen need — NSTR-36's scrubbing runs on the seekable upload
// staging file, never on a reopened stored object.
type S3PhotoStore struct {
	client  *s3.Client
	presign *s3.PresignClient
	// uploader: see the "feature/s3/manager" import's doc for why the
	// deprecated manager.Uploader is used deliberately, not by oversight.
	uploader   *manager.Uploader //nolint:staticcheck // SA1019: see the import's doc
	bucket     string
	presignTTL time.Duration
	// requestSSE gates whether Put asks for SSE-S3 (AES256) — endpoint-
	// conditional, not unconditional: verified against a real MinIO instance
	// (no KMS configured) in Nestova's NES-132, MinIO does NOT silently
	// ignore an SSE-S3 request — it rejects the PutObject outright with 501
	// NotImplemented ("Server side encryption specified but KMS is not
	// configured"). SSE-S3 is therefore requested only against real AWS S3
	// (no custom Endpoint); a custom endpoint (MinIO/Garage) never gets the
	// header at all.
	requestSSE bool
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.PhotoStore = (*S3PhotoStore)(nil)

// NewS3PhotoStore builds an S3PhotoStore against params and verifies the
// configured bucket is reachable (HeadBucket) before returning, so a
// misconfigured endpoint, bucket, or credentials fails startup loudly here
// rather than surfacing as an opaque error on the household's first photo
// upload (AC 3).
func NewS3PhotoStore(ctx context.Context, params S3Params) (*S3PhotoStore, error) {
	switch {
	case strings.TrimSpace(params.Bucket) == "":
		return nil, errors.New("media/adapter: S3 photo store bucket must not be blank")
	case strings.TrimSpace(params.Region) == "":
		return nil, errors.New("media/adapter: S3 photo store region must not be blank")
	case params.PresignTTL <= 0:
		return nil, fmt.Errorf("media/adapter: presign ttl must be positive, got %v", params.PresignTTL)
	}

	optFns := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(params.Region)}
	if params.AccessKeyID != "" && params.SecretAccessKey != "" {
		optFns = append(optFns, awsconfig.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(params.AccessKeyID, params.SecretAccessKey, ""),
		))
	}
	// Otherwise the SDK's default credential chain (environment, shared
	// config/credentials file, EC2/ECS instance role, etc.) applies
	// unchanged — see S3Params.AccessKeyID's doc for why this is supported
	// both ways.
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, optFns...)
	if err != nil {
		return nil, fmt.Errorf("media/adapter: load AWS config: %w", err)
	}

	client := s3.NewFromConfig(awsCfg, func(o *s3.Options) {
		if params.Endpoint != "" {
			o.BaseEndpoint = aws.String(params.Endpoint)
		}
		o.UsePathStyle = params.UsePathStyle
	})

	store := &S3PhotoStore{
		client:     client,
		presign:    s3.NewPresignClient(client),
		uploader:   manager.NewUploader(client), //nolint:staticcheck // SA1019: see the import's doc
		bucket:     params.Bucket,
		presignTTL: params.PresignTTL,
		// See the requestSSE field doc: only real AWS S3 (no custom
		// Endpoint) gets the SSE-S3 header.
		requestSSE: params.Endpoint == "",
	}

	if _, err := client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(params.Bucket)}); err != nil {
		return nil, fmt.Errorf("media/adapter: bucket %q is not reachable via endpoint %q: %w", params.Bucket, params.Endpoint, err)
	}
	return store, nil
}

// Put streams r to S3 under a key derived from itemID and meta.ContentHash
// (see buildStorageKey — the same content-addressed, item-scoped layout
// LocalPhotoStore.Put uses), via the manager.Uploader (multipart for a large
// upload, a single PUT otherwise). Requests SSE-S3 only against real AWS S3
// (see the requestSSE field doc). meta is trusted completely — see the type
// doc for why nothing here sniffs, size-caps, or hashes.
func (s *S3PhotoStore) Put(ctx context.Context, itemID storagedomain.ItemID, meta domain.PutMeta, r io.Reader) (domain.StorageRef, error) {
	ext, ok := extensionForContentType(meta.ContentType)
	if !ok {
		return "", fmt.Errorf("%w: %q", domain.ErrUnsupportedMediaType, meta.ContentType)
	}
	key := buildStorageKey(itemID, meta.ContentHash, ext)

	input := &s3.PutObjectInput{
		Bucket:      aws.String(s.bucket),
		Key:         aws.String(key),
		Body:        r,
		ContentType: aws.String(meta.ContentType),
	}
	if s.requestSSE {
		input.ServerSideEncryption = types.ServerSideEncryptionAes256
	}
	if _, err := s.uploader.Upload(ctx, input); err != nil { //nolint:staticcheck // SA1019: see the import's doc
		return "", fmt.Errorf("media/adapter: upload photo to s3: %w", err)
	}
	return domain.StorageRef(key), nil
}

// Open streams ref's bytes back directly from the GetObject response body —
// deliberately no buffering (see the type doc): a GetObject body already
// satisfies the port's sequential io.ReadCloser contract. Returns
// ErrPhotoNotFound when ref is unknown.
func (s *S3PhotoStore) Open(ctx context.Context, ref domain.StorageRef) (io.ReadCloser, error) {
	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(ref.String()),
	})
	if err != nil {
		var nsk *types.NoSuchKey
		if errors.As(err, &nsk) {
			return nil, fmt.Errorf("%w: %s", domain.ErrPhotoNotFound, ref)
		}
		return nil, fmt.Errorf("media/adapter: get photo from s3: %w", err)
	}
	return out.Body, nil
}

// Delete removes ref's object; a missing key is not an error — S3's
// DeleteObject is idempotent by design, mirroring LocalPhotoStore.Delete's
// identical contract.
func (s *S3PhotoStore) Delete(ctx context.Context, ref domain.StorageRef) error {
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(ref.String()),
	}); err != nil {
		return fmt.Errorf("media/adapter: delete photo from s3: %w", err)
	}
	return nil
}

// URL confirms ref exists (HeadObject — presigning alone never verifies
// existence, and the port's contract requires ErrPhotoNotFound for an
// unknown ref, mirroring LocalPhotoStore.URL's os.Stat check) and returns a
// presigned GET URL valid for ttl, or s.presignTTL when ttl is non-positive.
func (s *S3PhotoStore) URL(ctx context.Context, ref domain.StorageRef, ttl time.Duration) (string, error) {
	if _, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(s.bucket), Key: aws.String(ref.String())}); err != nil {
		var notFound *types.NotFound
		if errors.As(err, &notFound) {
			return "", fmt.Errorf("%w: %s", domain.ErrPhotoNotFound, ref)
		}
		return "", fmt.Errorf("media/adapter: check photo exists in s3: %w", err)
	}
	if ttl <= 0 {
		ttl = s.presignTTL
	}
	req, err := s.presign.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(ref.String()),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("media/adapter: presign photo url: %w", err)
	}
	return req.URL, nil
}

// SupportsDirectURL always reports true: S3PhotoStore's URL returns a real,
// browser-navigable presigned GET a caller may safely redirect a client to
// (unlike LocalPhotoStore, which always reports false — see its own doc).
func (s *S3PhotoStore) SupportsDirectURL() bool { return true }
