package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"

	mediadomain "github.com/ericfisherdev/nestorage/internal/media/domain"
)

// fakeThumbnailRepo is the thumbnailRepo test double: an in-memory set of
// rows still missing a thumbnail, plus the ids SetThumbnailRef has recorded.
// ListMissingThumbnail pages through rows in insertion order, honoring
// afterID/limit the same shape the real cursor query does. listErr/setErr
// force a canned failure from either method, for the error-propagation
// tests below.
type fakeThumbnailRepo struct {
	rows      []mediadomain.MissingThumbnailPhoto
	backfiled map[mediadomain.PhotoID]mediadomain.StorageRef
	listErr   error
	setErr    map[mediadomain.PhotoID]error
}

func newFakeThumbnailRepo(rows ...mediadomain.MissingThumbnailPhoto) *fakeThumbnailRepo {
	return &fakeThumbnailRepo{
		rows: rows, backfiled: make(map[mediadomain.PhotoID]mediadomain.StorageRef),
		setErr: make(map[mediadomain.PhotoID]error),
	}
}

func (r *fakeThumbnailRepo) ListMissingThumbnail(_ context.Context, limit int, afterID mediadomain.PhotoID) ([]mediadomain.MissingThumbnailPhoto, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	var out []mediadomain.MissingThumbnailPhoto
	passedAfter := afterID == (mediadomain.PhotoID{})
	for _, row := range r.rows {
		if _, done := r.backfiled[row.ID]; done {
			continue
		}
		if !passedAfter {
			if row.ID == afterID {
				passedAfter = true
			}
			continue
		}
		out = append(out, row)
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

func (r *fakeThumbnailRepo) SetThumbnailRef(_ context.Context, photoID mediadomain.PhotoID, ref mediadomain.StorageRef) error {
	if err, ok := r.setErr[photoID]; ok {
		return err
	}
	r.backfiled[photoID] = ref
	return nil
}

// fakePhotoStore is the mediadomain.PhotoStore test double: an in-memory
// ref -> bytes map seeded with each row's "full" object, recording every
// Put. putErr forces every Put to fail, for the storage-failure test below.
type fakePhotoStore struct {
	objects map[mediadomain.StorageRef][]byte
	openErr map[mediadomain.StorageRef]error
	putErr  error
	puts    []mediadomain.PutMeta
}

func newFakePhotoStore() *fakePhotoStore {
	return &fakePhotoStore{objects: make(map[mediadomain.StorageRef][]byte), openErr: make(map[mediadomain.StorageRef]error)}
}

func (s *fakePhotoStore) Open(_ context.Context, ref mediadomain.StorageRef) (io.ReadCloser, error) {
	if err, ok := s.openErr[ref]; ok {
		return nil, err
	}
	data, ok := s.objects[ref]
	if !ok {
		return nil, mediadomain.ErrPhotoNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (s *fakePhotoStore) Put(_ context.Context, itemID storagedomain.ItemID, meta mediadomain.PutMeta, r io.Reader) (mediadomain.StorageRef, error) {
	if s.putErr != nil {
		return "", s.putErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	s.puts = append(s.puts, meta)
	key := meta.ContentHash
	if meta.Variant == mediadomain.PhotoVariantThumb {
		key += "_thumb"
	}
	ref := mediadomain.StorageRef("items/" + itemID.String() + "/" + key)
	s.objects[ref] = data
	return ref, nil
}

func (s *fakePhotoStore) Delete(context.Context, mediadomain.StorageRef) error { return nil }
func (s *fakePhotoStore) URL(context.Context, mediadomain.StorageRef, time.Duration) (string, error) {
	return "", nil
}
func (s *fakePhotoStore) SupportsDirectURL() bool { return false }

// fakeThumbnailer is the mediadomain.PhotoThumbnailer test double: succeeds
// by default with a distinguishable transformation, or fails for a
// caller-named set of inputs (errFor, keyed by the data string) — enough to
// simulate one photo among several failing to generate.
type fakeThumbnailer struct {
	errFor map[string]error
}

func (t *fakeThumbnailer) Thumbnail(data []byte, contentType string) (mediadomain.ThumbResult, error) {
	if err, ok := t.errFor[string(data)]; ok {
		return mediadomain.ThumbResult{}, err
	}
	return mediadomain.ThumbResult{Bytes: append([]byte("thumb:"), data...), ContentType: contentType, Width: 1, Height: 1}, nil
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func newMissingRow(itemID storagedomain.ItemID, data []byte) (mediadomain.MissingThumbnailPhoto, *fakePhotoStore) {
	store := newFakePhotoStore()
	ref := mediadomain.StorageRef("items/" + itemID.String() + "/full")
	store.objects[ref] = data
	return mediadomain.MissingThumbnailPhoto{
		ID: mediadomain.NewPhotoID(), ItemID: itemID, StorageRef: ref,
		ContentHash: "deadbeef", ContentType: mediadomain.ContentTypeJPEG,
	}, store
}

// TestBackfill_OnlyProcessesRowsListMissingThumbnailReturns proves backfill
// generates and records a thumbnail for exactly the rows the repository
// reports as missing one — a row the fake repo never returns (standing in
// for one that already has a thumbnail) is never touched.
func TestBackfill_OnlyProcessesRowsListMissingThumbnailReturns(t *testing.T) {
	itemID := storagedomain.NewItemID()
	row, store := newMissingRow(itemID, []byte("original bytes"))
	repo := newFakeThumbnailRepo(row)
	thumbnailer := &fakeThumbnailer{}

	failed, err := backfill(context.Background(), repo, store, thumbnailer, discardLogger())
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if failed != 0 {
		t.Errorf("failed = %d, want 0", failed)
	}
	ref, ok := repo.backfiled[row.ID]
	if !ok {
		t.Fatal("SetThumbnailRef was never called for the missing row")
	}
	if got := store.objects[ref]; string(got) != "thumb:original bytes" {
		t.Errorf("stored thumbnail bytes = %q, want %q", got, "thumb:original bytes")
	}
}

// TestBackfill_PerPhotoFailureContinues proves one photo's failure (an
// unopenable full object here) does not stop the run: the other row still
// gets backfilled, and the failure is counted.
func TestBackfill_PerPhotoFailureContinues(t *testing.T) {
	itemID := storagedomain.NewItemID()
	store := newFakePhotoStore()

	okRow := mediadomain.MissingThumbnailPhoto{
		ID: mediadomain.NewPhotoID(), ItemID: itemID,
		StorageRef:  mediadomain.StorageRef("items/" + itemID.String() + "/ok"),
		ContentHash: "ok-hash", ContentType: mediadomain.ContentTypeJPEG,
	}
	store.objects[okRow.StorageRef] = []byte("fine")

	badRow := mediadomain.MissingThumbnailPhoto{
		ID: mediadomain.NewPhotoID(), ItemID: itemID,
		StorageRef:  mediadomain.StorageRef("items/" + itemID.String() + "/bad"),
		ContentHash: "bad-hash", ContentType: mediadomain.ContentTypeJPEG,
	}
	store.openErr[badRow.StorageRef] = errors.New("boom")

	repo := newFakeThumbnailRepo(okRow, badRow)
	thumbnailer := &fakeThumbnailer{}

	failed, err := backfill(context.Background(), repo, store, thumbnailer, discardLogger())
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if failed != 1 {
		t.Fatalf("failed = %d, want 1", failed)
	}
	if _, ok := repo.backfiled[okRow.ID]; !ok {
		t.Error("the healthy row was not backfilled despite the other row's failure")
	}
	if _, ok := repo.backfiled[badRow.ID]; ok {
		t.Error("the failing row must not be recorded as backfilled")
	}
}

// TestBackfill_IdempotentRerun proves a second call over the same
// (now-backfilled) repository state does no further work — the property
// cmd/backfill-thumbs' safe-to-re-run contract depends on.
func TestBackfill_IdempotentRerun(t *testing.T) {
	itemID := storagedomain.NewItemID()
	row, store := newMissingRow(itemID, []byte("original bytes"))
	repo := newFakeThumbnailRepo(row)
	thumbnailer := &fakeThumbnailer{}

	if _, err := backfill(context.Background(), repo, store, thumbnailer, discardLogger()); err != nil {
		t.Fatalf("first backfill: %v", err)
	}
	putsAfterFirst := len(store.puts)

	failed, err := backfill(context.Background(), repo, store, thumbnailer, discardLogger())
	if err != nil {
		t.Fatalf("second backfill: %v", err)
	}
	if failed != 0 {
		t.Errorf("second backfill failed = %d, want 0", failed)
	}
	if len(store.puts) != putsAfterFirst {
		t.Errorf("second backfill issued %d more Put(s), want 0 (already-backfilled row must not be re-processed)", len(store.puts)-putsAfterFirst)
	}
}

// TestBackfill_EmptyRepositoryIsANoOp proves a library with nothing missing
// a thumbnail returns cleanly with zero failures.
func TestBackfill_EmptyRepositoryIsANoOp(t *testing.T) {
	repo := newFakeThumbnailRepo()
	failed, err := backfill(context.Background(), repo, newFakePhotoStore(), &fakeThumbnailer{}, discardLogger())
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	if failed != 0 {
		t.Errorf("failed = %d, want 0", failed)
	}
}

// TestBackfill_ListMissingThumbnailErrorAbortsRun proves a systemic listing
// failure (unlike a single photo's failure) stops the run immediately and
// propagates — there is no per-row context to continue past.
func TestBackfill_ListMissingThumbnailErrorAbortsRun(t *testing.T) {
	repo := newFakeThumbnailRepo()
	repo.listErr = errors.New("connection lost")

	failed, err := backfill(context.Background(), repo, newFakePhotoStore(), &fakeThumbnailer{}, discardLogger())
	if err == nil {
		t.Fatal("backfill(list error) = nil, want an error")
	}
	if failed != 0 {
		t.Errorf("failed = %d, want 0 (the run aborted before processing anything)", failed)
	}
}

// TestBackfillOne_ThumbnailGenerationErrorPropagates proves a thumbnailer
// failure fails that one row without ever calling Put or SetThumbnailRef.
func TestBackfillOne_ThumbnailGenerationErrorPropagates(t *testing.T) {
	itemID := storagedomain.NewItemID()
	row, store := newMissingRow(itemID, []byte("bad image"))
	thumbnailer := &fakeThumbnailer{errFor: map[string]error{"bad image": errors.New("decode failed")}}

	err := backfillOne(context.Background(), newFakeThumbnailRepo(row), store, thumbnailer, row)
	if err == nil {
		t.Fatal("backfillOne(thumbnail generation error) = nil, want an error")
	}
	if len(store.puts) != 0 {
		t.Errorf("store.puts = %v, want none (Put must not run after a generation failure)", store.puts)
	}
}

// TestBackfillOne_PutErrorPropagates proves a storage failure fails that
// row without ever calling SetThumbnailRef.
func TestBackfillOne_PutErrorPropagates(t *testing.T) {
	itemID := storagedomain.NewItemID()
	row, store := newMissingRow(itemID, []byte("fine bytes"))
	store.putErr = errors.New("disk full")
	repo := newFakeThumbnailRepo(row)

	err := backfillOne(context.Background(), repo, store, &fakeThumbnailer{}, row)
	if err == nil {
		t.Fatal("backfillOne(put error) = nil, want an error")
	}
	if _, ok := repo.backfiled[row.ID]; ok {
		t.Error("SetThumbnailRef must not have been recorded after a Put failure")
	}
}

// TestBackfillOne_SetThumbnailRefErrorPropagates proves a repository write
// failure on the final step still surfaces as an error, even though the
// thumbnail object was already stored.
func TestBackfillOne_SetThumbnailRefErrorPropagates(t *testing.T) {
	itemID := storagedomain.NewItemID()
	row, store := newMissingRow(itemID, []byte("fine bytes"))
	repo := newFakeThumbnailRepo(row)
	repo.setErr[row.ID] = errors.New("row vanished")

	err := backfillOne(context.Background(), repo, store, &fakeThumbnailer{}, row)
	if err == nil {
		t.Fatal("backfillOne(SetThumbnailRef error) = nil, want an error")
	}
}

// TestRun_ConfigLoadErrorPropagates covers run()'s first fail-fast branch —
// no database or media store is ever touched when configuration itself is
// invalid, so this needs no NESTORAGE_TEST_DATABASE_URL gate. Every field
// t.Setenv touches auto-restores, isolating this from every other test.
func TestRun_ConfigLoadErrorPropagates(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("DATABASE_URL", "") // required in every environment; see config_test.go

	if err := run(discardLogger()); err == nil {
		t.Fatal("run() with no DATABASE_URL = nil, want a configuration error")
	}
}
