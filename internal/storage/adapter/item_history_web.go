package adapter

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/ericfisherdev/nestcore/render"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
	"github.com/ericfisherdev/nestorage/web/components"
)

// itemHistoryPageSize is the timeline's own keyset page size (Sprint 6
// reconciliation, item 8 withdrawn — NSTR-40 owns ListByItem/ListByBin and
// their indexes; this ticket only reads them). loadHistoryPage requests one
// row beyond this so it can tell whether a next page exists without a
// second round trip, mirroring the ticket's own "LIMIT 31" framing.
const itemHistoryPageSize = 30

// historyCursorSeparator joins encodeHistoryCursor's two parts. Neither an
// RFC3339Nano timestamp nor a UUID's hex-and-hyphens text can contain it.
const historyCursorSeparator = "_"

// itemEventLister is the narrow port (ISP) ItemsWebHandlers depends on for
// the timeline's own keyset-paginated read, satisfied by
// *ItemEventRepository (NSTR-40, this package). Method name and signature
// are NSTR-40's own ListByItem/HistoryPage exactly — Sprint 6 reconciliation
// R16 withdrew this ticket's originally planned ListForItem/EventCursor
// names (Go interfaces are satisfied structurally, so those would never
// have compiled) and also withdrew a viewer parameter: History already
// enforces visibility via h.items.Detail before ever calling this, so a
// second viewer-scoped check here would be redundant, and NSTR-40's
// repository takes no viewer.
type itemEventLister interface {
	ListByItem(ctx context.Context, itemID domain.ItemID, page domain.HistoryPage) ([]domain.ItemEvent, error)
}

// Compile-time assurance *ItemEventRepository satisfies itemEventLister —
// both types live in this package, so this is the intra-package analog of
// mediaapp.PhotoService's own var _ photoOperator assertion in
// media/adapter/photos_web.go.
var _ itemEventLister = (*ItemEventRepository)(nil)

// History handles GET /items/{id}/history: ItemHistoryStub's initial
// hx-trigger="load" fetch (the first page, no ?before), a click-to-load
// request for a later page (?before=<cursor>), or a direct/full-page
// navigation to the same URL. h.items.Detail runs before any event read on
// every request — including a later page's — so visibility is re-checked
// per request (an item could be deleted or become invisible between an
// initial load and a later click) and a 404 masks "unknown" and "invisible"
// identically, exactly as handleItemGetError already does for Detail.
func (h *ItemsWebHandlers) History(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	before, err := parseHistoryCursor(r.URL.Query().Get("before"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	result, err := h.items.Detail(r.Context(), viewer, id)
	if err != nil {
		h.handleItemGetError(w, r, err, "items: history")
		return
	}

	events, nextCursor, err := h.loadHistoryPage(r.Context(), id, before)
	if err != nil {
		h.logger.ErrorContext(r.Context(), "items: history: list events", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	days := buildItemHistoryDays(events, time.Now())

	// A ?before request is always a click-to-load fragment targeting
	// #item-history-more (historyMoreRow's own hx-target) — never a full
	// page, and never the outer #item-history div, so it renders without
	// the current-state headline or the outer section wrapper.
	if before != nil {
		content := components.ItemHistoryMoreFragment(id.String(), days, nextCursor)
		if err := render.Render(r.Context(), w, http.StatusOK, content); err != nil {
			h.logger.ErrorContext(r.Context(), "items: render history page", "error", err)
		}
		return
	}

	view := components.ItemHistoryView{
		ItemID: id.String(), CurrentState: historyCurrentStateLine(*result),
		Days: days, NextCursor: nextCursor,
	}
	w.Header().Set("Vary", "HX-Request")
	content := components.ItemHistorySection(view)
	if !render.IsHTMX(r) {
		content = h.layout(r, content)
	}
	if err := render.Render(r.Context(), w, http.StatusOK, content); err != nil {
		h.logger.ErrorContext(r.Context(), "items: render history", "error", err)
	}
}

// loadHistoryPage requests one row beyond itemHistoryPageSize so it can
// report whether a next page exists (nextCursor non-empty) without a second
// round trip, then trims back to itemHistoryPageSize before returning.
func (h *ItemsWebHandlers) loadHistoryPage(ctx context.Context, id domain.ItemID, before *domain.HistoryCursor) (events []domain.ItemEvent, nextCursor string, err error) {
	events, err = h.events.ListByItem(ctx, id, domain.HistoryPage{Limit: itemHistoryPageSize + 1, Before: before})
	if err != nil {
		return nil, "", err
	}
	if len(events) <= itemHistoryPageSize {
		return events, "", nil
	}
	last := events[itemHistoryPageSize-1]
	cursor := encodeHistoryCursor(domain.HistoryCursor{OccurredAt: last.OccurredAt, ID: last.ID})
	return events[:itemHistoryPageSize], cursor, nil
}

// encodeHistoryCursor renders a domain.HistoryCursor as the opaque,
// URL-safe "before" query value historyMoreRow's own hx-get carries —
// base64.RawURLEncoding's alphabet needs no percent-encoding, so the templ
// layer concatenates it directly (see historyMoreRow's own doc).
func encodeHistoryCursor(c domain.HistoryCursor) string {
	raw := c.OccurredAt.UTC().Format(time.RFC3339Nano) + historyCursorSeparator + c.ID.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// parseHistoryCursor decodes the "before" query parameter into a
// *domain.HistoryCursor, or nil for an absent/empty parameter (the first
// page). A malformed value is reported via the returned error — History
// maps it to 400 rather than silently treating it as "no cursor", which
// would let a corrupted or tampered value silently restart the page at the
// newest events instead of failing loudly.
func parseHistoryCursor(raw string) (*domain.HistoryCursor, error) {
	if raw == "" {
		return nil, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("decode history cursor: %w", err)
	}
	parts := strings.SplitN(string(decoded), historyCursorSeparator, 2)
	if len(parts) != 2 {
		return nil, errors.New("malformed history cursor")
	}
	occurredAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return nil, fmt.Errorf("parse history cursor time: %w", err)
	}
	id, err := domain.ParseItemEventID(parts[1])
	if err != nil {
		return nil, fmt.Errorf("parse history cursor id: %w", err)
	}
	return &domain.HistoryCursor{OccurredAt: occurredAt, ID: id}, nil
}

// buildItemHistoryDays groups a newest-first page of events into
// consecutive local-calendar-day runs — safe because ListByItem's own
// ordering guarantee (occurred_at DESC) means every event sharing a local
// day is already contiguous in events, so a single pass suffices.
func buildItemHistoryDays(events []domain.ItemEvent, now time.Time) []components.ItemHistoryDayView {
	days := make([]components.ItemHistoryDayView, 0)
	var lastKey string
	for _, e := range events {
		key := e.OccurredAt.Local().Format("2006-01-02")
		if len(days) == 0 || key != lastKey {
			days = append(days, components.ItemHistoryDayView{Heading: historyDayHeading(e.OccurredAt, now)})
			lastKey = key
		}
		days[len(days)-1].Events = append(days[len(days)-1].Events, buildHistoryEventView(e))
	}
	return days
}

// historyDayHeading names the local calendar day t falls on, relative to
// now: "Today", "Yesterday", or "Jan 2, 2006" — always computed from
// time.Local (t.Local()/now.Local()), never a UTC-derived string, so a
// non-UTC viewer's headings agree with their own wall clock (the AC's own
// requirement).
func historyDayHeading(t, now time.Time) string {
	tl := t.Local()
	switch daysBetween(tl, now.Local()) {
	case 0:
		return "Today"
	case 1:
		return "Yesterday"
	default:
		return tl.Format("Jan 2, 2006")
	}
}

// daysBetween returns the whole number of local calendar days between
// earlier and later (0 when they fall on the same local day), computed from
// each time's own Date() at local midnight rather than a raw duration
// subtraction — a duration-based comparison would misclassify two
// timestamps that cross local midnight by less than 24h as still "the same
// day". A negative result (earlier is later in wall-clock terms, e.g. clock
// skew) falls through historyDayHeading's own default case, which is a safe
// fallback since it is never 0 or 1.
func daysBetween(earlier, later time.Time) int {
	ey, em, ed := earlier.Date()
	e := time.Date(ey, em, ed, 0, 0, 0, 0, earlier.Location())
	ly, lm, ld := later.Date()
	l := time.Date(ly, lm, ld, 0, 0, 0, 0, later.Location())
	return int(l.Sub(e).Hours() / 24)
}

// buildHistoryEventView projects one domain.ItemEvent into
// ItemHistoryEventView, sharing historyActorView/historyEventSummary/
// formatHistoryTime with BinsWebHandlers.Activity's own buildBinActivityView
// (web_common.go) so the two surfaces never drift on wording.
func buildHistoryEventView(e domain.ItemEvent) components.ItemHistoryEventView {
	return components.ItemHistoryEventView{
		Actor: historyActorView(e), Summary: historyEventSummary(e), Time: formatHistoryTime(e.OccurredAt),
	}
}

// historyCurrentStateLine renders the timeline's own headline — the same
// two facts ItemDetailView's own in-bin/holder panels render (where the
// item sits, or who holds it and since when), phrased as one sentence for
// the top of the history section. LocationName/BinCode/HolderName render in
// their stored casing, matching every other place this codebase displays
// them (e.g. itemInBinPanel) rather than a forced uppercase transform.
func historyCurrentStateLine(result domain.ItemDetailResult) string {
	if result.Item.State() == domain.StateInBin {
		return "In " + result.LocationName + " — " + result.BinCode
	}
	return "Held by " + result.HolderName + " since " + formatHeldSince(result.Item.PlacementChangedAt)
}
