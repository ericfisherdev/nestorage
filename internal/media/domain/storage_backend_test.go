package domain_test

import (
	"testing"

	"github.com/ericfisherdev/nestorage/internal/media/domain"
)

func TestStorageBackend_Valid(t *testing.T) {
	cases := []struct {
		name string
		b    domain.StorageBackend
		want bool
	}{
		{"local", domain.StorageBackendLocal, true},
		{"unknown", domain.StorageBackend("s3"), false},
		{"blank", domain.StorageBackend(""), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.b.Valid(); got != c.want {
				t.Errorf("%q.Valid() = %v, want %v", c.b, got, c.want)
			}
		})
	}
}

func TestParseStorageBackend(t *testing.T) {
	got, err := domain.ParseStorageBackend(" Local ")
	if err != nil {
		t.Fatalf("ParseStorageBackend(\" Local \") error = %v, want nil", err)
	}
	if got != domain.StorageBackendLocal {
		t.Errorf("ParseStorageBackend(\" Local \") = %q, want %q", got, domain.StorageBackendLocal)
	}
}

// TestParseStorageBackend_DefaultCase exercises the default switch case —
// written now so NSTR-35 can add StorageBackendS3 without this function's
// shape changing.
func TestParseStorageBackend_DefaultCase(t *testing.T) {
	if _, err := domain.ParseStorageBackend("s3"); err == nil {
		t.Error("ParseStorageBackend(\"s3\") error = nil, want an error (S3 is NSTR-35's addition)")
	}
	if _, err := domain.ParseStorageBackend("bogus"); err == nil {
		t.Error("ParseStorageBackend(\"bogus\") error = nil, want an error")
	}
}

func TestStorageBackend_String(t *testing.T) {
	if got := domain.StorageBackendLocal.String(); got != "local" {
		t.Errorf("StorageBackendLocal.String() = %q, want %q", got, "local")
	}
}
