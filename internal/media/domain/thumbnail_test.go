package domain_test

import (
	"testing"

	"github.com/ericfisherdev/nestorage/internal/media/domain"
)

func TestPhotoVariant_Valid(t *testing.T) {
	cases := []struct {
		name string
		v    domain.PhotoVariant
		want bool
	}{
		{"full", domain.PhotoVariantFull, true},
		{"thumb", domain.PhotoVariantThumb, true},
		{"unknown", domain.PhotoVariant("original"), false},
		{"blank", domain.PhotoVariant(""), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.v.Valid(); got != c.want {
				t.Errorf("%q.Valid() = %v, want %v", c.v, got, c.want)
			}
		})
	}
}

func TestParsePhotoVariant(t *testing.T) {
	got, err := domain.ParsePhotoVariant(" Thumb ")
	if err != nil {
		t.Fatalf("ParsePhotoVariant(\" Thumb \") error = %v, want nil", err)
	}
	if got != domain.PhotoVariantThumb {
		t.Errorf("ParsePhotoVariant(\" Thumb \") = %q, want %q", got, domain.PhotoVariantThumb)
	}
}

// TestParsePhotoVariant_DefaultCase exercises the default switch case.
func TestParsePhotoVariant_DefaultCase(t *testing.T) {
	if _, err := domain.ParsePhotoVariant("original"); err == nil {
		t.Error(`ParsePhotoVariant("original") error = nil, want an error`)
	}
}

func TestPhotoVariant_String(t *testing.T) {
	if got := domain.PhotoVariantThumb.String(); got != "thumb" {
		t.Errorf("PhotoVariantThumb.String() = %q, want %q", got, "thumb")
	}
}

// TestThumbResult_ZeroValue proves the zero value carries no bytes and no
// content type — PhotoService treats a zero ThumbResult as "nothing to
// store," never as a valid empty thumbnail.
func TestThumbResult_ZeroValue(t *testing.T) {
	var r domain.ThumbResult
	if r.Bytes != nil || r.ContentType != "" || r.Width != 0 || r.Height != 0 {
		t.Errorf("zero ThumbResult = %+v, want every field at its zero value", r)
	}
}
