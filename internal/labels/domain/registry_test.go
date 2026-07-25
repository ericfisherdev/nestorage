package domain_test

import (
	"errors"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/labels/domain"
)

// TestNewRegistry_BuiltinsSucceed confirms the fail-fast construction the
// AC requires ("a geometry whose columns and margins exceed the page width
// fails validation at startup") does not itself trip on the real builtins.
func TestNewRegistry_BuiltinsSucceed(t *testing.T) {
	registry, err := domain.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry() error = %v, want nil", err)
	}
	if registry == nil {
		t.Fatal("NewRegistry() registry = nil, want non-nil")
	}
}

func TestRegistry_Get(t *testing.T) {
	registry, err := domain.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry() error = %v, want nil", err)
	}

	t.Run("known id", func(t *testing.T) {
		size, err := registry.Get(domain.LabelSizeAvery5160)
		if err != nil {
			t.Fatalf("Get(%q) error = %v, want nil", domain.LabelSizeAvery5160, err)
		}
		if size.ID != domain.LabelSizeAvery5160 {
			t.Errorf("Get(%q).ID = %q, want %q", domain.LabelSizeAvery5160, size.ID, domain.LabelSizeAvery5160)
		}
	})

	t.Run("unknown id rejected", func(t *testing.T) {
		_, err := registry.Get("no-such-size")
		if !errors.Is(err, domain.ErrUnknownLabelSize) {
			t.Errorf("Get(unknown) error = %v, want ErrUnknownLabelSize", err)
		}
	})
}

// TestRegistry_List_OrderIsDeterministic covers the AC/NSTR-51 dependency:
// List's order must be identical across separate Registry instances (and,
// by extension, across process restarts), never a map-iteration accident.
func TestRegistry_List_OrderIsDeterministic(t *testing.T) {
	want := []domain.LabelSizeID{domain.LabelSizeAvery5160, domain.LabelSizeAvery5163, domain.LabelSizeSingle4x6}

	for i := 0; i < 5; i++ {
		registry, err := domain.NewRegistry()
		if err != nil {
			t.Fatalf("NewRegistry() error = %v, want nil", err)
		}
		sizes := registry.List()
		if len(sizes) != len(want) {
			t.Fatalf("List() len = %d, want %d", len(sizes), len(want))
		}
		for j, size := range sizes {
			if size.ID != want[j] {
				t.Errorf("run %d: List()[%d].ID = %q, want %q", i, j, size.ID, want[j])
			}
		}
	}
}

// TestRegistry_List_ReturnsACopy confirms a caller mutating the returned
// slice cannot corrupt the registry's own state for a later List/Get call —
// List documents no aliasing contract, so it must not have one in practice.
func TestRegistry_List_ReturnsACopy(t *testing.T) {
	registry, err := domain.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry() error = %v, want nil", err)
	}
	sizes := registry.List()
	sizes[0].DisplayName = "mutated"

	again := registry.List()
	if again[0].DisplayName == "mutated" {
		t.Error("List() aliases internal state — mutating the returned slice affected a later List() call")
	}
}
