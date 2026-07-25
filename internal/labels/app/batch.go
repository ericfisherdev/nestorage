// Package app holds the labels bounded context's application services —
// NSTR-50's BatchService, the only one so far. It composes NSTR-47's
// LabelSize registry and LabelRenderer port with storage's own
// BinRepository/LocationRepository (narrow, ISP-scoped ports declared here,
// not in storage) to turn "every bin in this location" into one rendered
// label document, and NSTR-48's exported BinDeepLinkURL to build each
// label's QR payload.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	labelsdomain "github.com/ericfisherdev/nestorage/internal/labels/domain"
	storageapp "github.com/ericfisherdev/nestorage/internal/storage/app"
	storagedomain "github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// visibleBinLister is the narrow port (ISP) BatchService depends on to list
// a location's bins scoped to what viewer may see, satisfied by storage's
// domain.BinRepository (a superset, via ListVisibleByLocation) and by test
// fakes. Its SQL visibility predicate — not any filtering in this package —
// is what guarantees another member's private bins never enter a batch
// (NSTR-50's privacy acceptance criterion); this service passes the result
// through untouched.
type visibleBinLister interface {
	ListVisibleByLocation(ctx context.Context, viewer identity.Principal, locationID storagedomain.LocationID) ([]storagedomain.Bin, error)
}

// locationGetter is the narrow port (ISP) BatchService depends on to
// resolve locationID for the 404 check and the batch's suggested filename,
// satisfied by storage's domain.LocationRepository (a superset, via
// FindVisibleByID) and by test fakes.
type locationGetter interface {
	FindVisibleByID(ctx context.Context, viewer identity.Principal, id storagedomain.LocationID) (*storagedomain.Location, error)
}

// BatchDocument is one rendered location label batch: the PDF bytes
// (embedded labelsdomain.Document) plus a filename the download this
// batch's own web handler serves (NSTR-50) suggests to the browser.
type BatchDocument struct {
	labelsdomain.Document
	Filename string
}

// BatchService implements NSTR-50's "print every bin in a location" batch
// flow: resolve the location, look up the requested LabelSize, list its
// visible bins, enforce labelsdomain.MaxBatchLabels, map bins into
// labelsdomain.BinLabel values, and render once — sheet pagination across
// however many pages the batch needs is the injected LabelRenderer's own
// job (see its port doc), never re-implemented here. NSTR-51 depends on
// this being an injectable service, not logic buried in an HTTP handler.
type BatchService struct {
	bins      visibleBinLister
	locations locationGetter
	sizes     *labelsdomain.Registry
	renderer  labelsdomain.LabelRenderer
	logger    *slog.Logger
}

// NewBatchService constructs BatchService. Every dependency is required; a
// missing one panics at construction time, matching every other constructor
// in this codebase (see storage/app.NewLocationService).
func NewBatchService(bins visibleBinLister, locations locationGetter, sizes *labelsdomain.Registry, renderer labelsdomain.LabelRenderer, logger *slog.Logger) *BatchService {
	if bins == nil {
		panic("labels/app: NewBatchService requires a non-nil visibleBinLister")
	}
	if locations == nil {
		panic("labels/app: NewBatchService requires a non-nil locationGetter")
	}
	if sizes == nil {
		panic("labels/app: NewBatchService requires a non-nil *labelsdomain.Registry")
	}
	if renderer == nil {
		panic("labels/app: NewBatchService requires a non-nil labelsdomain.LabelRenderer")
	}
	if logger == nil {
		panic("labels/app: NewBatchService requires a non-nil logger")
	}
	return &BatchService{bins: bins, locations: locations, sizes: sizes, renderer: renderer, logger: logger}
}

// RenderLocationBatch renders every bin viewer may see in locationID onto
// size's sheet, starting at startOffset's first-page cell (see
// labelsdomain.LabelRenderer's own doc for that parameter's exact
// contract). baseURL is the already-resolved origin (PUBLIC_BASE_URL, or a
// per-request derived fallback — see storage/adapter.resolveBaseURL, whose
// exact rationale this package's own web handler mirrors) each bin's QR
// deep link is built against; it varies per request, which is why it is a
// parameter here rather than a constructor dependency.
//
// Checks run in this order, matching NSTR-50's plan exactly: the location
// (a wrapped domain.ErrLocationNotFound when missing or not visible to
// viewer), then the size (registry's own domain.ErrUnknownLabelSize passes
// through unwrapped), then the visible bin list — an empty result returns
// domain.ErrEmptyBatch, a count above domain.MaxBatchLabels returns a
// *domain.BatchTooLargeError.
func (s *BatchService) RenderLocationBatch(ctx context.Context, viewer identity.Principal, locationID storagedomain.LocationID, sizeID labelsdomain.LabelSizeID, startOffset int, baseURL string) (*BatchDocument, error) {
	location, err := s.locations.FindVisibleByID(ctx, viewer, locationID)
	if err != nil {
		return nil, fmt.Errorf("app: render location batch: %w", err)
	}

	size, err := s.sizes.Get(sizeID)
	if err != nil {
		return nil, err
	}

	bins, err := s.bins.ListVisibleByLocation(ctx, viewer, locationID)
	if err != nil {
		return nil, fmt.Errorf("app: render location batch: %w", err)
	}
	if len(bins) == 0 {
		return nil, labelsdomain.ErrEmptyBatch
	}
	if len(bins) > labelsdomain.MaxBatchLabels {
		return nil, &labelsdomain.BatchTooLargeError{Count: len(bins), Max: labelsdomain.MaxBatchLabels}
	}

	labels := make([]labelsdomain.BinLabel, len(bins))
	for i, b := range bins {
		labels[i] = labelsdomain.BinLabel{
			Code:         b.Code,
			Name:         b.Name,
			LocationName: location.Name,
			QRPayload:    storageapp.BinDeepLinkURL(baseURL, b.Code),
		}
	}

	doc, err := s.renderer.Render(ctx, size, labels, startOffset)
	if err != nil {
		return nil, fmt.Errorf("app: render location batch: %w", err)
	}

	s.logBatch(ctx, locationID, len(bins), sizeID)
	return &BatchDocument{Document: doc, Filename: batchFilename(location.Name, sizeID)}, nil
}

// logBatch writes one INFO-level audit line per rendered batch: the
// location id, how many bins it held, and the size id — never the
// location's or any bin's name, Nestorage's PII-out-of-logs convention (see
// storage/app.LocationService.logAction).
func (s *BatchService) logBatch(ctx context.Context, locationID storagedomain.LocationID, binCount int, sizeID labelsdomain.LabelSizeID) {
	s.logger.InfoContext(ctx, "labels: batch rendered", "location_id", locationID.String(), "bin_count", binCount, "size_id", sizeID.String())
}

// slugSanitizer matches every run of characters that is not a lowercase
// letter or digit, collapsed to a single hyphen by slugify — mirrors
// cmd/migrate's own migration-filename slug (nameSanitizer), with "-" as
// the separator this ticket's labels-{slug}-{sizeID}.pdf shape uses instead
// of migrate's "_".
var slugSanitizer = regexp.MustCompile(`[^a-z0-9]+`)

// maxSlugRunes bounds the location-name portion of a batch's suggested
// filename: generous enough that a location name stays recognizable, short
// enough that the filename never gets unwieldy. Location names are already
// capped well above this (storage's own maxLocationNameRunes, 100).
const maxSlugRunes = 60

// slugify lowercases name, collapses every run of non a-z0-9 characters
// (including any multi-byte rune, which never matches the sanitizer's
// class) to a single hyphen, trims leading/trailing hyphens, and truncates
// to maxSlugRunes. Sanitizing first guarantees the result is pure ASCII, so
// truncating by byte length here is equivalent to truncating by rune count
// — no rune-slicing needed, unlike a truncation over untrusted text (e.g.
// PDFSheetRenderer's own truncateToWidth).
func slugify(name string) string {
	slug := strings.Trim(slugSanitizer.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(slug) > maxSlugRunes {
		slug = strings.Trim(slug[:maxSlugRunes], "-")
	}
	return slug
}

// batchFilename builds a batch's suggested download filename:
// labels-{location-slug}-{sizeID}.pdf, per NSTR-50's plan.
func batchFilename(locationName string, sizeID labelsdomain.LabelSizeID) string {
	return fmt.Sprintf("labels-%s-%s.pdf", slugify(locationName), sizeID.String())
}
