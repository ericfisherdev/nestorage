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
		{"s3", domain.StorageBackendS3, true},
		{"unknown", domain.StorageBackend("azure-blob"), false},
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

// TestParseStorageBackend_S3 covers NSTR-35's addition to the enum.
func TestParseStorageBackend_S3(t *testing.T) {
	got, err := domain.ParseStorageBackend(" S3 ")
	if err != nil {
		t.Fatalf("ParseStorageBackend(\" S3 \") error = %v, want nil", err)
	}
	if got != domain.StorageBackendS3 {
		t.Errorf("ParseStorageBackend(\" S3 \") = %q, want %q", got, domain.StorageBackendS3)
	}
}

// TestParseStorageBackend_DefaultCase exercises the default switch case.
func TestParseStorageBackend_DefaultCase(t *testing.T) {
	if _, err := domain.ParseStorageBackend("bogus"); err == nil {
		t.Error("ParseStorageBackend(\"bogus\") error = nil, want an error")
	}
}

func TestStorageBackend_String(t *testing.T) {
	if got := domain.StorageBackendLocal.String(); got != "local" {
		t.Errorf("StorageBackendLocal.String() = %q, want %q", got, "local")
	}
}
