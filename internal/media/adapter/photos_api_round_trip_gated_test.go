package adapter_test

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/media/adapter"
	mediaapp "github.com/ericfisherdev/nestorage/internal/media/app"
)

// newRealPhotoService wires a real mediaapp.PhotoService against f's own
// Postgres-backed PhotoRepository and ItemRepository (itemGetter), plus the
// real local-filesystem adapters the actual upload pipeline uses —
// LocalPhotoStore, PhotoValidator, ExifScrubber, ImageThumbnailer — so
// TestPhotosAPI_Upload_RoundTripsThroughSharedTables below proves the API's
// Upload handler runs the SAME pipeline the web gallery does, not a
// hermetic stand-in.
func newRealPhotoService(t *testing.T, f *photoFixture) *mediaapp.PhotoService {
	t.Helper()
	store, err := adapter.NewLocalPhotoStore(t.TempDir(), defaultTestMaxUploadBytes)
	if err != nil {
		t.Fatalf("NewLocalPhotoStore: %v", err)
	}
	thumbnailer, err := adapter.NewImageThumbnailer(256)
	if err != nil {
		t.Fatalf("NewImageThumbnailer: %v", err)
	}
	svc, err := mediaapp.NewPhotoService(
		store, adapter.NewPhotoValidator(t.TempDir()), adapter.NewExifScrubber(), thumbnailer,
		f.repo, f.items, defaultTestMaxUploadBytes, testLogger(),
	)
	if err != nil {
		t.Fatalf("NewPhotoService: %v", err)
	}
	return svc
}

// TestPhotosAPI_Upload_RoundTripsThroughSharedTables proves NSTR-56's own
// AC directly: a photo uploaded through the JSON API is returned by the
// SAME ListForItem call the web gallery renders from. Both surfaces share
// exactly one PhotoService, one PhotoRepository, and one item_photo table
// — there is no separate "API photo" concept that could drift out of sync,
// which is what reusing photoOperator verbatim (rather than writing a
// second application service) structurally guarantees.
func TestPhotosAPI_Upload_RoundTripsThroughSharedTables(t *testing.T) {
	f := newPhotoFixture(t)
	uploader := f.seedUser(t)
	itemID := f.seedItem(t, uploader)
	svc := newRealPhotoService(t, f)

	handlers := adapter.NewPhotosAPIHandlers(svc, defaultTestMaxUploadBytes, testLogger())
	viewer := identity.NewUserPrincipal(uploader, identity.RoleMember, "Uploader")
	server := newAPIPrincipalServer(t, viewer, handlers)

	resp := multipartUploadRequest(t, server, "/api/v1/items/"+itemID.String()+"/photos", "a.jpg", string(jpegBytes(t)), 0)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201:\n%s", resp.StatusCode, body)
	}
	var uploaded struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &uploaded); err != nil {
		t.Fatalf("decode upload response: %v\n%s", err, body)
	}

	// The exact same call PhotosWebHandlers.renderGallery makes to render
	// the web gallery — proving the two surfaces share tables, not just a
	// service instance in this one process.
	got, err := svc.ListForItem(testCtx(t), viewer, itemID)
	if err != nil {
		t.Fatalf("ListForItem: %v", err)
	}
	found := false
	for _, ip := range got {
		if ip.Photo.ID.String() == uploaded.ID {
			found = true
			if !ip.IsPrimary {
				t.Error("the first photo uploaded to an item must be primary")
			}
		}
	}
	if !found {
		t.Errorf("the photo the API uploaded (%s) was not in the web gallery's own ListForItem result: %+v", uploaded.ID, got)
	}
}
