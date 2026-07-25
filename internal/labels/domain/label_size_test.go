package domain_test

import (
	"errors"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/labels/domain"
)

// validLabelSize returns a LabelSize that passes Validate, so each
// TestLabelSize_Validate subtest can mutate exactly one field away from
// valid and confirm that field alone is what Validate rejects — the same
// mutate-one-field pattern storage/domain's validBin helper follows.
func validLabelSize() domain.LabelSize {
	return domain.LabelSize{
		ID:             "test-size",
		DisplayName:    "Test size",
		PageWidthMM:    100,
		PageHeightMM:   100,
		MarginTopMM:    5,
		MarginBottomMM: 5,
		MarginLeftMM:   5,
		MarginRightMM:  5,
		Columns:        2,
		Rows:           2,
		CellWidthMM:    40,
		CellHeightMM:   40,
		GutterXMM:      2,
		GutterYMM:      2,
	}
}

func TestLabelSize_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*domain.LabelSize)
		wantErr error
	}{
		{"valid size accepted", func(*domain.LabelSize) {}, nil},
		{"blank id rejected", func(s *domain.LabelSize) { s.ID = "  " }, domain.ErrInvalidLabelSize},
		{"non-positive page width rejected", func(s *domain.LabelSize) { s.PageWidthMM = 0 }, domain.ErrInvalidLabelSize},
		{"non-positive page height rejected", func(s *domain.LabelSize) { s.PageHeightMM = -1 }, domain.ErrInvalidLabelSize},
		{"non-positive cell width rejected", func(s *domain.LabelSize) { s.CellWidthMM = 0 }, domain.ErrInvalidLabelSize},
		{"non-positive cell height rejected", func(s *domain.LabelSize) { s.CellHeightMM = 0 }, domain.ErrInvalidLabelSize},
		{"zero columns rejected", func(s *domain.LabelSize) { s.Columns = 0 }, domain.ErrInvalidLabelSize},
		{"zero rows rejected", func(s *domain.LabelSize) { s.Rows = 0 }, domain.ErrInvalidLabelSize},
		{"negative top margin rejected", func(s *domain.LabelSize) { s.MarginTopMM = -1 }, domain.ErrInvalidLabelSize},
		{"negative bottom margin rejected", func(s *domain.LabelSize) { s.MarginBottomMM = -1 }, domain.ErrInvalidLabelSize},
		{"negative left margin rejected", func(s *domain.LabelSize) { s.MarginLeftMM = -1 }, domain.ErrInvalidLabelSize},
		{"negative right margin rejected", func(s *domain.LabelSize) { s.MarginRightMM = -1 }, domain.ErrInvalidLabelSize},
		{"negative column gutter rejected", func(s *domain.LabelSize) { s.GutterXMM = -1 }, domain.ErrInvalidLabelSize},
		{"negative row gutter rejected", func(s *domain.LabelSize) { s.GutterYMM = -1 }, domain.ErrInvalidLabelSize},
		{
			"width overflow rejected",
			func(s *domain.LabelSize) { s.CellWidthMM = 100 },
			domain.ErrInvalidLabelSize,
		},
		{
			"height overflow rejected",
			func(s *domain.LabelSize) { s.CellHeightMM = 100 },
			domain.ErrInvalidLabelSize,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := validLabelSize()
			tt.mutate(&s)
			if err := s.Validate(); !errors.Is(err, tt.wantErr) {
				t.Errorf("Validate() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

// TestLabelSize_Validate_BuiltinsPass confirms every registry builtin is
// itself well-formed — the same fail-fast fact NewRegistry relies on at
// construction, checked directly here against each entry in isolation.
func TestLabelSize_Validate_BuiltinsPass(t *testing.T) {
	registry, err := domain.NewRegistry()
	if err != nil {
		t.Fatalf("NewRegistry() error = %v, want nil", err)
	}
	for _, size := range registry.List() {
		if err := size.Validate(); err != nil {
			t.Errorf("builtin %q: Validate() error = %v, want nil", size.ID, err)
		}
	}
}

// TestLabelSize_CellsPerPage covers the per-sheet capacity NSTR-50's
// start-offset validation and batch-cap wording depend on, including the
// degenerate 1x1 single-label case.
func TestLabelSize_CellsPerPage(t *testing.T) {
	tests := []struct {
		name string
		size domain.LabelSize
		want int
	}{
		{"3x10 grid", domain.LabelSize{Columns: 3, Rows: 10}, 30},
		{"2x5 grid", domain.LabelSize{Columns: 2, Rows: 5}, 10},
		{"single 1x1", domain.LabelSize{Columns: 1, Rows: 1}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.size.CellsPerPage(); got != tt.want {
				t.Errorf("CellsPerPage() = %d, want %d", got, tt.want)
			}
		})
	}
}
