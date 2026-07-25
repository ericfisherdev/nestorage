package domain

// This file is package domain (white-box), not domain_test — a deliberate,
// narrow exception needed only because newRegistryFrom is unexported: the
// duplicate-id and invalid-entry rejection paths this file proves are
// unreachable from a black-box test, since builtinLabelSizes itself carries
// neither. Mirrors storage/adapter's own item_history_internal_test.go
// exception for the identical reason (an internal seam a public-API test
// cannot drive).

import (
	"errors"
	"testing"
)

func TestNewRegistryFrom_DuplicateIDRejected(t *testing.T) {
	one := LabelSize{ID: "dup", DisplayName: "One", PageWidthMM: 100, PageHeightMM: 100, Columns: 1, Rows: 1, CellWidthMM: 100, CellHeightMM: 100}
	two := LabelSize{ID: "dup", DisplayName: "Two", PageWidthMM: 100, PageHeightMM: 100, Columns: 1, Rows: 1, CellWidthMM: 100, CellHeightMM: 100}

	_, err := newRegistryFrom([]LabelSize{one, two})
	if !errors.Is(err, ErrInvalidLabelSize) {
		t.Errorf("newRegistryFrom(duplicate ids) error = %v, want ErrInvalidLabelSize", err)
	}
}

func TestNewRegistryFrom_InvalidEntryRejected(t *testing.T) {
	invalid := LabelSize{ID: "bad", PageWidthMM: 100, PageHeightMM: 100, Columns: 1, Rows: 1, CellWidthMM: 200, CellHeightMM: 100}

	_, err := newRegistryFrom([]LabelSize{invalid})
	if !errors.Is(err, ErrInvalidLabelSize) {
		t.Errorf("newRegistryFrom(invalid entry) error = %v, want ErrInvalidLabelSize", err)
	}
}
