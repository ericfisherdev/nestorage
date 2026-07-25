package adapter

import (
	"testing"

	"github.com/ericfisherdev/nestorage/internal/labels/domain"
	storageapp "github.com/ericfisherdev/nestorage/internal/storage/app"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// testGridSize is a small, easy-to-reason-about 2x2 grid (4 cells) used by
// every pure-function test below instead of a real registry entry — the
// exact millimetre values do not matter to these tests, only the
// column/row/cell shape they imply.
var testGridSize = domain.LabelSize{
	ID: "test-2x2", DisplayName: "Test 2x2",
	PageWidthMM: 20, PageHeightMM: 20,
	Columns: 2, Rows: 2,
	CellWidthMM: 10, CellHeightMM: 10,
}

func testBinView(name string) storageapp.BinView {
	return storageapp.BinView{Bin: storagedomain.Bin{ID: storagedomain.NewBinID(), Code: "BIN-" + name, Name: name}}
}

func TestPreviewCell_OffsetSkipsCells(t *testing.T) {
	bins := []storageapp.BinView{testBinView("First"), testBinView("Second")}
	const offset = 1

	cell0 := previewCell(testGridSize, "Garage", bins, offset, 0)
	if !cell0.Dimmed || cell0.Filled {
		t.Errorf("cell 0 (before offset %d) = %+v, want Dimmed only", offset, cell0)
	}

	cell1 := previewCell(testGridSize, "Garage", bins, offset, 1)
	if !cell1.Filled || cell1.Dimmed || cell1.BinName != "First" || cell1.BinCode != "BIN-First" {
		t.Errorf("cell 1 (first occupied) = %+v, want Filled with bins[0]'s name and code", cell1)
	}

	cell2 := previewCell(testGridSize, "Garage", bins, offset, 2)
	if !cell2.Filled || cell2.BinName != "Second" || cell2.BinCode != "BIN-Second" || cell2.LocationName != "Garage" {
		t.Errorf("cell 2 (second occupied) = %+v, want Filled with bins[1]'s name/code and the location name", cell2)
	}

	cell3 := previewCell(testGridSize, "Garage", bins, offset, 3)
	if cell3.Filled || cell3.Dimmed {
		t.Errorf("cell 3 (past the batch's own last label) = %+v, want neither Filled nor Dimmed", cell3)
	}
}

func TestPreviewCell_PositionIsPagePercentage(t *testing.T) {
	// Cell index 3 is the grid's bottom-right cell (col=1, row=1): origin at
	// (10mm, 10mm) on a 20x20mm page, i.e. 50% left/top, and each cell is
	// half the page in both dimensions.
	cell := previewCell(testGridSize, "", nil, 0, 3)
	if cell.LeftPercent != 50 || cell.TopPercent != 50 {
		t.Errorf("cell 3 position = (%g%%, %g%%), want (50%%, 50%%)", cell.LeftPercent, cell.TopPercent)
	}
	if cell.WidthPercent != 50 || cell.HeightPercent != 50 {
		t.Errorf("cell 3 size = (%g%%, %g%%), want (50%%, 50%%)", cell.WidthPercent, cell.HeightPercent)
	}
}

func TestBuildPreviewView_CellsCoverWholeSheetAndSummary(t *testing.T) {
	target := printTarget{
		LocationName: "Garage",
		Bins:         []storageapp.BinView{testBinView("First"), testBinView("Second"), testBinView("Third")},
	}
	view := buildPreviewView(target, testGridSize, 1)

	if len(view.Cells) != testGridSize.CellsPerPage() {
		t.Fatalf("len(Cells) = %d, want %d (one sheet's own capacity)", len(view.Cells), testGridSize.CellsPerPage())
	}
	// offset=1, 3 bins, 4 cells/page: cell 0 dimmed, cells 1-3 filled,
	// nothing left over — 1+3=4 fits exactly one sheet.
	if !view.Cells[0].Dimmed || !view.Cells[1].Filled || !view.Cells[3].Filled {
		t.Errorf("Cells = %+v, want [dimmed, filled, filled, filled]", view.Cells)
	}
	wantSummary := "3 labels across 1 sheet"
	if view.Summary != wantSummary {
		t.Errorf("Summary = %q, want %q", view.Summary, wantSummary)
	}
}

func TestBuildPreviewView_NoBins_EmptySummary(t *testing.T) {
	view := buildPreviewView(printTarget{LocationName: "Garage"}, testGridSize, 0)
	if view.Summary != "No labels to print yet." {
		t.Errorf("Summary = %q, want the no-labels message", view.Summary)
	}
}

func TestSheetsNeeded(t *testing.T) {
	tests := []struct {
		name                           string
		offset, binCount, cellsPerPage int
		want                           int
	}{
		{"no bins", 0, 0, 4, 0},
		{"exact fit", 0, 4, 4, 1},
		{"one over spills a sheet", 0, 5, 4, 2},
		{"offset pushes onto a second sheet", 2, 3, 4, 2},
		{"offset plus bins fits one sheet", 1, 3, 4, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sheetsNeeded(tt.offset, tt.binCount, tt.cellsPerPage); got != tt.want {
				t.Errorf("sheetsNeeded(%d, %d, %d) = %d, want %d", tt.offset, tt.binCount, tt.cellsPerPage, got, tt.want)
			}
		})
	}
}

func TestClampOffset(t *testing.T) {
	tests := []struct {
		name         string
		offset       int
		cellsPerPage int
		want         int
	}{
		{"negative clamps to 0", -5, 30, 0},
		{"in range unchanged", 12, 30, 12},
		{"past capacity clamps to last cell", 9999, 30, 29},
		{"exactly at capacity clamps to last cell", 30, 30, 29},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clampOffset(tt.offset, tt.cellsPerPage); got != tt.want {
				t.Errorf("clampOffset(%d, %d) = %d, want %d", tt.offset, tt.cellsPerPage, got, tt.want)
			}
		})
	}
}

func TestParseOffsetOrZero(t *testing.T) {
	tests := []struct {
		raw  string
		want int
	}{
		{"", 0},
		{"not-a-number", 0},
		{"12", 12},
		{"-3", -3},
	}
	for _, tt := range tests {
		if got := parseOffsetOrZero(tt.raw); got != tt.want {
			t.Errorf("parseOffsetOrZero(%q) = %d, want %d", tt.raw, got, tt.want)
		}
	}
}
