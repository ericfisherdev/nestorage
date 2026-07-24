package adapter

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"

	"github.com/ericfisherdev/nestorage/internal/media/domain"
)

// localStagingPattern names the temp file Put stages a write into before
// the atomic rename below — kept under root so the rename is same-filesystem
// (a cross-filesystem os.Rename fails).
const localStagingPattern = "upload-*.tmp"

// LocalPhotoStore is a domain.PhotoStore backed by the local filesystem —
// the zero-config default backend every fresh Nestorage install gets (NSTR-
// 35 adds an S3-backed alternative). Photos are content-addressed (sha256)
// and item-scoped under MEDIA_ROOT/items/<item>/<hash>.<ext> (see
// buildStorageKey), so identical bytes uploaded to the same item de-duplicate
// on disk and a ref never collides across items.
//
// Put trusts the domain.PutMeta the caller (PhotoService) supplies
// completely — see PutMeta's own doc for why no sniffing, hashing, or
// decode validation happens here; that already ran upstream.
type LocalPhotoStore struct {
	root           string
	maxUploadBytes int64
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.PhotoStore = (*LocalPhotoStore)(nil)

// NewLocalPhotoStore returns a store rooted at root (created if missing),
// rejecting a blank root or a non-positive size cap. maxUploadBytes is a
// defense-in-depth cap on Put's own write — PhotoService's PhotoValidator
// already enforces MEDIA_MAX_UPLOAD_BYTES before Put is ever called, but
// Put does not trust that to be the ONLY layer that ever calls it.
func NewLocalPhotoStore(root string, maxUploadBytes int64) (*LocalPhotoStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("media/adapter: photo store root must not be blank")
	}
	if maxUploadBytes <= 0 {
		return nil, fmt.Errorf("media/adapter: max upload bytes must be positive, got %d", maxUploadBytes)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("media/adapter: create photo store root: %w", err)
	}
	return &LocalPhotoStore{root: root, maxUploadBytes: maxUploadBytes}, nil
}

// Put streams r to a staging file under root, then atomically renames it
// into its content-addressed, item-scoped home (see buildStorageKey). Any
// rejection — including an unrecognized content type or an oversize
// write — removes the staging file, leaving no partial upload behind.
func (s *LocalPhotoStore) Put(_ context.Context, itemID storagedomain.ItemID, meta domain.PutMeta, r io.Reader) (domain.StorageRef, error) {
	ext, ok := extensionForContentType(meta.ContentType)
	if !ok {
		return "", fmt.Errorf("%w: %q", domain.ErrUnsupportedMediaType, meta.ContentType)
	}
	key := buildStorageKey(itemID, meta.ContentHash, ext, meta.Variant)
	full, err := s.resolve(key)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", fmt.Errorf("media/adapter: create photo dir: %w", err)
	}

	tmp, err := os.CreateTemp(s.root, localStagingPattern)
	if err != nil {
		return "", fmt.Errorf("media/adapter: create staging file: %w", err)
	}
	tmpPath := tmp.Name()
	finalized := false
	defer func() {
		if !finalized {
			_ = os.Remove(tmpPath)
		}
	}()

	written, copyErr := io.Copy(tmp, io.LimitReader(r, s.maxUploadBytes+1))
	closeErr := tmp.Close()
	if copyErr != nil {
		return "", fmt.Errorf("media/adapter: write photo: %w", copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("media/adapter: close staged photo: %w", closeErr)
	}
	if written > s.maxUploadBytes {
		return "", fmt.Errorf("%w: %d bytes exceeds the %d-byte limit", domain.ErrPhotoTooLarge, written, s.maxUploadBytes)
	}

	// Content-addressed, so a file already at full is byte-identical; Rename
	// is atomic and harmlessly replaces it.
	if err := os.Rename(tmpPath, full); err != nil {
		return "", fmt.Errorf("media/adapter: finalize upload: %w", err)
	}
	finalized = true
	return domain.StorageRef(key), nil
}

// Open streams a stored photo's bytes; ErrPhotoNotFound when the ref is
// unknown. The returned *os.File satisfies io.ReadCloser, matching
// domain.PhotoStore.Open's deliberately sequential contract.
func (s *LocalPhotoStore) Open(_ context.Context, ref domain.StorageRef) (io.ReadCloser, error) {
	full, err := s.resolve(ref.String())
	if err != nil {
		return nil, err
	}
	f, err := os.Open(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", domain.ErrPhotoNotFound, ref)
		}
		return nil, fmt.Errorf("media/adapter: open photo: %w", err)
	}
	return f, nil
}

// Delete removes a stored photo; a missing file is not an error (idempotent).
func (s *LocalPhotoStore) Delete(_ context.Context, ref domain.StorageRef) error {
	full, err := s.resolve(ref.String())
	if err != nil {
		return err
	}
	if err := os.Remove(full); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("media/adapter: delete photo: %w", err)
	}
	return nil
}

// URL confirms ref resolves to a stored object and returns ref's own string
// as a stable, non-navigable locator, or ErrPhotoNotFound when ref is
// unknown; ttl is ignored (see domain.PhotoStore.URL's doc for why a local
// backend cannot honestly return a browser-navigable URL from ref alone).
func (s *LocalPhotoStore) URL(_ context.Context, ref domain.StorageRef, _ time.Duration) (string, error) {
	full, err := s.resolve(ref.String())
	if err != nil {
		return "", err
	}
	info, err := os.Stat(full)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: %s", domain.ErrPhotoNotFound, ref)
		}
		return "", fmt.Errorf("media/adapter: stat photo: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s", domain.ErrPhotoNotFound, ref)
	}
	return ref.String(), nil
}

// SupportsDirectURL always reports false: LocalPhotoStore's URL never
// returns a browser-navigable locator (see URL's own doc).
func (s *LocalPhotoStore) SupportsDirectURL() bool { return false }

// resolve joins ref onto root and guards against path traversal: the
// cleaned absolute path must stay within root.
func (s *LocalPhotoStore) resolve(ref string) (string, error) {
	full := filepath.Join(s.root, filepath.FromSlash(ref))
	rootAbs, err := filepath.Abs(s.root)
	if err != nil {
		return "", fmt.Errorf("media/adapter: resolve root: %w", err)
	}
	fullAbs, err := filepath.Abs(full)
	if err != nil {
		return "", fmt.Errorf("media/adapter: resolve ref: %w", err)
	}
	if fullAbs != rootAbs && !strings.HasPrefix(fullAbs, rootAbs+string(os.PathSeparator)) {
		return "", fmt.Errorf("%w: storage ref escapes the store root", domain.ErrInvalidPhoto)
	}
	return full, nil
}
