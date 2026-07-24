package domain_test

import (
	"errors"
	"strings"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/media/domain"
)

// validHash is the well-known sha256 of the empty string — 64 lowercase hex
// characters, the shape contentHashPattern requires.
const validHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func validPhoto() domain.Photo {
	return domain.Photo{
		ID:             domain.NewPhotoID(),
		StorageRef:     "items/some-item/abc.jpg",
		ContentHash:    validHash,
		SizeBytes:      1024,
		ContentType:    domain.ContentTypeJPEG,
		StorageBackend: domain.StorageBackendLocal,
		UploadedBy:     identity.NewUserID(),
	}
}

func TestPhoto_Validate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(p *domain.Photo)
		wantErr bool
	}{
		{"valid", func(*domain.Photo) {}, false},
		{"blank storage ref", func(p *domain.Photo) { p.StorageRef = "  " }, true},
		{"blank content hash", func(p *domain.Photo) { p.ContentHash = "" }, true},
		{"short content hash", func(p *domain.Photo) { p.ContentHash = "abc123" }, true},
		{"uppercase content hash", func(p *domain.Photo) { p.ContentHash = strings.ToUpper(validHash) }, true},
		{"zero size", func(p *domain.Photo) { p.SizeBytes = 0 }, true},
		{"negative size", func(p *domain.Photo) { p.SizeBytes = -1 }, true},
		{"unaccepted content type", func(p *domain.Photo) { p.ContentType = "image/webp" }, true},
		{"blank content type", func(p *domain.Photo) { p.ContentType = "" }, true},
		{"invalid storage backend", func(p *domain.Photo) { p.StorageBackend = domain.StorageBackend("s3") }, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := validPhoto()
			c.mutate(&p)
			err := p.Validate()
			if c.wantErr && !errors.Is(err, domain.ErrInvalidPhoto) {
				t.Errorf("Validate() = %v, want ErrInvalidPhoto", err)
			}
			if !c.wantErr && err != nil {
				t.Errorf("Validate() = %v, want nil", err)
			}
		})
	}
}

func TestPhoto_Validate_AcceptsJPEGAndPNGOnly(t *testing.T) {
	for _, ct := range []string{domain.ContentTypeJPEG, domain.ContentTypePNG} {
		p := validPhoto()
		p.ContentType = ct
		if err := p.Validate(); err != nil {
			t.Errorf("Validate() with content type %q = %v, want nil", ct, err)
		}
	}
}

func TestStorageRef_String(t *testing.T) {
	ref := domain.StorageRef("items/abc/def.jpg")
	if ref.String() != "items/abc/def.jpg" {
		t.Errorf("StorageRef.String() = %q, want %q", ref.String(), "items/abc/def.jpg")
	}
}
