package app_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	labelsadapter "github.com/ericfisherdev/nestorage/internal/labels/adapter"
	"github.com/ericfisherdev/nestorage/internal/labels/app"
	labelsdomain "github.com/ericfisherdev/nestorage/internal/labels/domain"
	storageapp "github.com/ericfisherdev/nestorage/internal/storage/app"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func testViewer() identity.Principal {
	return identity.NewUserPrincipal(identity.NewUserID(), identity.RoleMember, "Viewer")
}

// fakeVisibleBinLister is a configurable visibleBinLister fake for
// BatchService's hermetic unit tests, capturing the viewer/locationID it
// was last called with so a test can assert they reached the repository
// untouched.
type fakeVisibleBinLister struct {
	bins []storagedomain.Bin
	err  error

	gotViewer     identity.Principal
	gotLocationID storagedomain.LocationID
	calls         int
}

func (f *fakeVisibleBinLister) ListVisibleByLocation(_ context.Context, viewer identity.Principal, locationID storagedomain.LocationID) ([]storagedomain.Bin, error) {
	f.calls++
	f.gotViewer = viewer
	f.gotLocationID = locationID
	if f.err != nil {
		return nil, f.err
	}
	return f.bins, nil
}

// fakeLocationGetter is a configurable locationGetter fake.
type fakeLocationGetter struct {
	location *storagedomain.Location
	err      error
}

func (f *fakeLocationGetter) FindVisibleByID(_ context.Context, _ identity.Principal, _ storagedomain.LocationID) (*storagedomain.Location, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.location, nil
}

// fakeRenderer is a configurable labelsdomain.LabelRenderer fake, capturing
// the size/labels/startOffset it was last called with.
type fakeRenderer struct {
	doc labelsdomain.Document
	err error

	calls     int
	gotSize   labelsdomain.LabelSize
	gotLabels []labelsdomain.BinLabel
	gotOffset int
}

func (f *fakeRenderer) Render(_ context.Context, size labelsdomain.LabelSize, labels []labelsdomain.BinLabel, startOffset int) (labelsdomain.Document, error) {
	f.calls++
	f.gotSize = size
	f.gotLabels = labels
	f.gotOffset = startOffset
	if f.err != nil {
		return labelsdomain.Document{}, f.err
	}
	return f.doc, nil
}

// testRegistry returns a real *labelsdomain.Registry (NewRegistry never
// fails against the builtin sizes) so BatchService's own size lookup is
// exercised for real rather than faked.
func testRegistry(t *testing.T) *labelsdomain.Registry {
	t.Helper()
	registry, err := labelsdomain.NewRegistry()
	if err != nil {
		t.Fatalf("labelsdomain.NewRegistry: %v", err)
	}
	return registry
}

func TestNewBatchService_PanicsOnNilDeps(t *testing.T) {
	bins := &fakeVisibleBinLister{}
	locations := &fakeLocationGetter{}
	sizes := testRegistry(t)
	renderer := &fakeRenderer{}
	logger := testLogger()

	t.Run("nil visibleBinLister", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewBatchService did not panic")
			}
		}()
		app.NewBatchService(nil, locations, sizes, renderer, logger)
	})
	t.Run("nil locationGetter", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewBatchService did not panic")
			}
		}()
		app.NewBatchService(bins, nil, sizes, renderer, logger)
	})
	t.Run("nil registry", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewBatchService did not panic")
			}
		}()
		app.NewBatchService(bins, locations, nil, renderer, logger)
	})
	t.Run("nil renderer", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewBatchService did not panic")
			}
		}()
		app.NewBatchService(bins, locations, sizes, nil, logger)
	})
	t.Run("nil logger", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewBatchService did not panic")
			}
		}()
		app.NewBatchService(bins, locations, sizes, renderer, nil)
	})
}

func TestBatchService_RenderLocationBatch_Success(t *testing.T) {
	garage := &storagedomain.Location{ID: storagedomain.NewLocationID(), Name: "Garage"}
	binA := storagedomain.Bin{ID: storagedomain.NewBinID(), Code: "A1", Name: "Bin A", LocationID: garage.ID}
	binB := storagedomain.Bin{ID: storagedomain.NewBinID(), Code: "B2", Name: "Bin B", LocationID: garage.ID}

	locations := &fakeLocationGetter{location: garage}
	bins := &fakeVisibleBinLister{bins: []storagedomain.Bin{binA, binB}}
	renderer := &fakeRenderer{doc: labelsdomain.Document{ContentType: "application/pdf", Data: []byte("pdf-bytes")}}
	svc := app.NewBatchService(bins, locations, testRegistry(t), renderer, testLogger())
	viewer := testViewer()

	got, err := svc.RenderLocationBatch(context.Background(), viewer, garage.ID, labelsdomain.LabelSizeAvery5160, 3, "https://nestorage.example.test")
	if err != nil {
		t.Fatalf("RenderLocationBatch: %v", err)
	}

	if bins.gotViewer != viewer || bins.gotLocationID != garage.ID {
		t.Errorf("ListVisibleByLocation called with (%+v, %v), want (%+v, %v)", bins.gotViewer, bins.gotLocationID, viewer, garage.ID)
	}
	if renderer.gotOffset != 3 {
		t.Errorf("Render startOffset = %d, want 3", renderer.gotOffset)
	}
	if renderer.gotSize.ID != labelsdomain.LabelSizeAvery5160 {
		t.Errorf("Render size = %q, want %q", renderer.gotSize.ID, labelsdomain.LabelSizeAvery5160)
	}
	wantLabels := []labelsdomain.BinLabel{
		{Code: "A1", Name: "Bin A", LocationName: "Garage", QRPayload: storageapp.BinDeepLinkURL("https://nestorage.example.test", "A1")},
		{Code: "B2", Name: "Bin B", LocationName: "Garage", QRPayload: storageapp.BinDeepLinkURL("https://nestorage.example.test", "B2")},
	}
	if len(renderer.gotLabels) != len(wantLabels) {
		t.Fatalf("Render labels = %+v, want %+v", renderer.gotLabels, wantLabels)
	}
	for i, want := range wantLabels {
		if renderer.gotLabels[i] != want {
			t.Errorf("Render labels[%d] = %+v, want %+v", i, renderer.gotLabels[i], want)
		}
	}

	if got.ContentType != "application/pdf" || string(got.Data) != "pdf-bytes" {
		t.Errorf("BatchDocument = %+v, want the renderer's own Document passed through", got)
	}
	if got.Filename != "labels-garage-avery-5160.pdf" {
		t.Errorf("Filename = %q, want %q", got.Filename, "labels-garage-avery-5160.pdf")
	}
}

func TestBatchService_RenderLocationBatch_LocationNotFound(t *testing.T) {
	locations := &fakeLocationGetter{err: storagedomain.ErrLocationNotFound}
	bins := &fakeVisibleBinLister{}
	renderer := &fakeRenderer{}
	svc := app.NewBatchService(bins, locations, testRegistry(t), renderer, testLogger())

	_, err := svc.RenderLocationBatch(context.Background(), testViewer(), storagedomain.NewLocationID(), labelsdomain.LabelSizeAvery5160, 0, "https://x.test")
	if !errors.Is(err, storagedomain.ErrLocationNotFound) {
		t.Errorf("RenderLocationBatch() error = %v, want wrapped ErrLocationNotFound", err)
	}
	if bins.calls != 0 {
		t.Error("ListVisibleByLocation was called even though the location lookup failed")
	}
	if renderer.calls != 0 {
		t.Error("Render was called even though the location lookup failed")
	}
}

func TestBatchService_RenderLocationBatch_UnknownSizePassesThrough(t *testing.T) {
	garage := &storagedomain.Location{ID: storagedomain.NewLocationID(), Name: "Garage"}
	locations := &fakeLocationGetter{location: garage}
	bins := &fakeVisibleBinLister{bins: []storagedomain.Bin{{ID: storagedomain.NewBinID(), Code: "A1", Name: "Bin A"}}}
	renderer := &fakeRenderer{}
	svc := app.NewBatchService(bins, locations, testRegistry(t), renderer, testLogger())

	_, err := svc.RenderLocationBatch(context.Background(), testViewer(), garage.ID, "not-a-real-size", 0, "https://x.test")
	if !errors.Is(err, labelsdomain.ErrUnknownLabelSize) {
		t.Errorf("RenderLocationBatch() error = %v, want ErrUnknownLabelSize", err)
	}
	if bins.calls != 0 {
		t.Error("ListVisibleByLocation was called even though the size lookup failed")
	}
	if renderer.calls != 0 {
		t.Error("Render was called even though the size lookup failed")
	}
}

func TestBatchService_RenderLocationBatch_EmptyBatchRejected(t *testing.T) {
	garage := &storagedomain.Location{ID: storagedomain.NewLocationID(), Name: "Garage"}
	locations := &fakeLocationGetter{location: garage}
	bins := &fakeVisibleBinLister{bins: nil}
	renderer := &fakeRenderer{}
	svc := app.NewBatchService(bins, locations, testRegistry(t), renderer, testLogger())

	_, err := svc.RenderLocationBatch(context.Background(), testViewer(), garage.ID, labelsdomain.LabelSizeAvery5160, 0, "https://x.test")
	if !errors.Is(err, labelsdomain.ErrEmptyBatch) {
		t.Errorf("RenderLocationBatch() error = %v, want ErrEmptyBatch", err)
	}
	if renderer.calls != 0 {
		t.Error("Render was called even though the batch was empty")
	}
}

func TestBatchService_RenderLocationBatch_TooLargeRejected(t *testing.T) {
	garage := &storagedomain.Location{ID: storagedomain.NewLocationID(), Name: "Garage"}
	overCap := make([]storagedomain.Bin, labelsdomain.MaxBatchLabels+1)
	for i := range overCap {
		overCap[i] = storagedomain.Bin{ID: storagedomain.NewBinID(), Code: "A1", Name: "Bin"}
	}
	locations := &fakeLocationGetter{location: garage}
	bins := &fakeVisibleBinLister{bins: overCap}
	renderer := &fakeRenderer{}
	svc := app.NewBatchService(bins, locations, testRegistry(t), renderer, testLogger())

	_, err := svc.RenderLocationBatch(context.Background(), testViewer(), garage.ID, labelsdomain.LabelSizeAvery5160, 0, "https://x.test")
	var tooLarge *labelsdomain.BatchTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("RenderLocationBatch() error = %v, want *BatchTooLargeError", err)
	}
	if tooLarge.Count != labelsdomain.MaxBatchLabels+1 || tooLarge.Max != labelsdomain.MaxBatchLabels {
		t.Errorf("BatchTooLargeError = %+v, want Count=%d Max=%d", tooLarge, labelsdomain.MaxBatchLabels+1, labelsdomain.MaxBatchLabels)
	}
	if renderer.calls != 0 {
		t.Error("Render was called even though the batch exceeded the cap")
	}
}

func TestBatchService_RenderLocationBatch_FilenameSlug(t *testing.T) {
	tests := []struct {
		name         string
		locationName string
		want         string
	}{
		{"simple name", "Garage", "labels-garage-avery-5160.pdf"},
		{"punctuation collapsed", "Under the Stairs!", "labels-under-the-stairs-avery-5160.pdf"},
		{"extra whitespace collapsed", "  Hall   Closet  ", "labels-hall-closet-avery-5160.pdf"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			garage := &storagedomain.Location{ID: storagedomain.NewLocationID(), Name: tt.locationName}
			locations := &fakeLocationGetter{location: garage}
			bins := &fakeVisibleBinLister{bins: []storagedomain.Bin{{ID: storagedomain.NewBinID(), Code: "A1", Name: "Bin"}}}
			renderer := &fakeRenderer{}
			svc := app.NewBatchService(bins, locations, testRegistry(t), renderer, testLogger())

			got, err := svc.RenderLocationBatch(context.Background(), testViewer(), garage.ID, labelsdomain.LabelSizeAvery5160, 0, "https://x.test")
			if err != nil {
				t.Fatalf("RenderLocationBatch: %v", err)
			}
			if got.Filename != tt.want {
				t.Errorf("Filename = %q, want %q", got.Filename, tt.want)
			}
		})
	}
}

// TestBatchService_RenderLocationBatch_Paginates renders a batch through the
// REAL gopdf renderer (not a fake) with one more bin than single-4x6 fits
// per page (its CellsPerPage is 1), and asserts the resulting PDF contains
// two page objects — this pins NSTR-50's own "printing a location with more
// bins than fit on one sheet produces a correctly paginated multi-page PDF"
// acceptance criterion at the seam this ticket owns: BatchService actually
// invoking LabelRenderer with an unbounded label slice and trusting it to
// paginate, rather than this package re-implementing that itself.
func TestBatchService_RenderLocationBatch_Paginates(t *testing.T) {
	garage := &storagedomain.Location{ID: storagedomain.NewLocationID(), Name: "Garage"}
	locations := &fakeLocationGetter{location: garage}
	bins := &fakeVisibleBinLister{bins: []storagedomain.Bin{
		{ID: storagedomain.NewBinID(), Code: "A1", Name: "Bin A", LocationID: garage.ID},
		{ID: storagedomain.NewBinID(), Code: "A2", Name: "Bin B", LocationID: garage.ID},
	}}
	renderer := labelsadapter.NewPDFSheetRenderer()
	svc := app.NewBatchService(bins, locations, testRegistry(t), renderer, testLogger())

	got, err := svc.RenderLocationBatch(context.Background(), testViewer(), garage.ID, labelsdomain.LabelSizeSingle4x6, 0, "https://x.test")
	if err != nil {
		t.Fatalf("RenderLocationBatch: %v", err)
	}
	if !bytes.HasPrefix(got.Data, []byte("%PDF-")) {
		t.Fatalf("BatchDocument.Data does not start with the PDF magic bytes")
	}
	pageObjectMarker := []byte("/Type /Page\n")
	if pages := bytes.Count(got.Data, pageObjectMarker); pages != 2 {
		t.Errorf("rendered PDF has %d page objects, want 2 (single-4x6 fits 1 label/page, batch has 2)", pages)
	}
}
