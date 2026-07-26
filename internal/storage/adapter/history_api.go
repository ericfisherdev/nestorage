package adapter

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/api"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// defaultHistoryPageLimit and maxHistoryPageLimit bound every history
// endpoint's own "limit" query parameter — the ticket's own bound, distinct
// from api_common.go's defaultPageLimit/maxPageLimit (which govern the
// in-memory-windowed CRUD list endpoints, not this file's DB-pushed-down
// keyset paging), mirroring item_history_web.go's own itemHistoryPageSize
// being a separate constant from those same CRUD bounds for the identical
// reason.
const (
	defaultHistoryPageLimit = 30
	maxHistoryPageLimit     = 100
)

// binDetailReader is the narrow port (ISP) HistoryAPIHandlers depends on to
// confirm a bin is visible to viewer before ever reading its event
// history — the bin analog of itemDetailReader (operations_api.go),
// satisfied by *app.BinService (a superset, via GetByID).
type binDetailReader interface {
	GetByID(ctx context.Context, viewer identity.Principal, id domain.BinID) (*app.BinView, error)
}

// HistoryAPIHandlers serves NSTR-55's two read-only event-history
// endpoints. Every route is registered on the api.Router NSTR-53 mounts
// behind its bearer-only gate; this type carries no session and no CSRF
// dependency, matching every other *APIHandlers type in this package.
type HistoryAPIHandlers struct {
	items      itemDetailReader
	itemEvents itemEventLister
	bins       binDetailReader
	binEvents  binActivityLister
	logger     *slog.Logger
}

// NewHistoryAPIHandlers constructs HistoryAPIHandlers. Every dependency is
// required; a missing one panics at construction time, matching every
// other WebHandlers/APIHandlers constructor in this codebase.
func NewHistoryAPIHandlers(items itemDetailReader, itemEvents itemEventLister, bins binDetailReader, binEvents binActivityLister, logger *slog.Logger) *HistoryAPIHandlers {
	if items == nil {
		panic("storage/adapter: NewHistoryAPIHandlers requires a non-nil itemDetailReader")
	}
	if itemEvents == nil {
		panic("storage/adapter: NewHistoryAPIHandlers requires a non-nil itemEventLister")
	}
	if bins == nil {
		panic("storage/adapter: NewHistoryAPIHandlers requires a non-nil binDetailReader")
	}
	if binEvents == nil {
		panic("storage/adapter: NewHistoryAPIHandlers requires a non-nil binActivityLister")
	}
	if logger == nil {
		panic("storage/adapter: NewHistoryAPIHandlers requires a non-nil logger")
	}
	return &HistoryAPIHandlers{items: items, itemEvents: itemEvents, bins: bins, binEvents: binEvents, logger: logger}
}

// Routes registers the history routes on mux — an api.Registrar (DIP), see
// LocationsAPIHandlers.Routes' own doc for why.
func (h *HistoryAPIHandlers) Routes(mux api.Registrar) {
	mux.HandleFunc("GET /api/v1/items/{id}/history", h.ItemHistory)
	mux.HandleFunc("GET /api/v1/bins/{id}/history", h.BinHistory)
}

// historyActorResponse is a history event's actor field: label and kind
// always present for both principal kinds (AC 3), user_id null exactly for
// the integration principal, which has no person behind it to name.
type historyActorResponse struct {
	Kind   string  `json:"kind"`
	Label  string  `json:"label"`
	UserID *string `json:"user_id"`
}

// historyBinResponse and historyLocationResponse are a history event's
// nullable bin/location fields: id plus the label snapshotted onto the
// event row at write time, never a live lookup — see ItemEvent's own doc
// for why (a departed member or renamed bin must not change what past
// history reads).
type historyBinResponse struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type historyLocationResponse struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// historyItemRefResponse is a history event's item field: id plus the name
// snapshotted at event time — needed on bin history, which spans items, and
// carried on item history too so both endpoints share one event shape.
type historyItemRefResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// historyEventResponse is one row in either history endpoint's events
// array — the one event JSON shape ItemHistory and BinHistory both answer.
type historyEventResponse struct {
	ID            string                   `json:"id"`
	Kind          string                   `json:"kind"`
	OccurredAt    time.Time                `json:"occurred_at"`
	Note          *string                  `json:"note"`
	Actor         historyActorResponse     `json:"actor"`
	Bin           *historyBinResponse      `json:"bin"`
	FromLocation  *historyLocationResponse `json:"from_location"`
	ToLocation    *historyLocationResponse `json:"to_location"`
	ChangedFields []string                 `json:"changed_fields"`
	Item          historyItemRefResponse   `json:"item"`
}

// historyPageResponse is both history endpoints' response envelope.
type historyPageResponse struct {
	Events     []historyEventResponse `json:"events"`
	NextCursor *string                `json:"next_cursor"`
}

// ItemHistory handles GET /api/v1/items/{id}/history: a visibility-scoped
// detail read (ItemQueryService.Detail) before any event read — masking
// unknown and invisible identically, exactly as the web History handler
// does (item_history_web.go) — then a keyset page of itemID's own events,
// newest first.
func (h *HistoryAPIHandlers) ItemHistory(w http.ResponseWriter, r *http.Request) {
	itemID, ok := parsePathID(w, r, h.logger, "id", domain.ParseItemID)
	if !ok {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	if _, err := h.items.Detail(r.Context(), viewer, itemID); err != nil {
		writeOperationError(w, r, h.logger, err, "history api: item history")
		return
	}

	before, ok := parseRequestHistoryCursor(w, h.logger, r)
	if !ok {
		return
	}
	limit := requestHistoryLimit(r)
	events, nextCursor, err := loadHistoryEventPage(before, limit, func(page domain.HistoryPage) ([]domain.ItemEvent, error) {
		return h.itemEvents.ListByItem(r.Context(), itemID, page)
	})
	if err != nil {
		h.logger.ErrorContext(r.Context(), "history api: item history: list", "error", err)
		api.WriteError(w, h.logger, http.StatusInternalServerError, api.CodeInternal, errInternalServerError)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, buildHistoryPageResponse(events, nextCursor))
}

// BinHistory handles GET /api/v1/bins/{id}/history: the bin's own
// visibility-scoped lookup before any event read — the bin analog of
// ItemHistory's Detail call — then a keyset page of binID's own events,
// newest first. Because EventRemoved carries the bin an item left (not
// only EventAdded, the bin it entered), this surfaces both.
func (h *HistoryAPIHandlers) BinHistory(w http.ResponseWriter, r *http.Request) {
	binID, ok := parsePathID(w, r, h.logger, "id", domain.ParseBinID)
	if !ok {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())
	if _, err := h.bins.GetByID(r.Context(), viewer, binID); err != nil {
		writeOperationError(w, r, h.logger, err, "history api: bin history")
		return
	}

	before, ok := parseRequestHistoryCursor(w, h.logger, r)
	if !ok {
		return
	}
	limit := requestHistoryLimit(r)
	events, nextCursor, err := loadHistoryEventPage(before, limit, func(page domain.HistoryPage) ([]domain.ItemEvent, error) {
		return h.binEvents.ListByBin(r.Context(), binID, page)
	})
	if err != nil {
		h.logger.ErrorContext(r.Context(), "history api: bin history: list", "error", err)
		api.WriteError(w, h.logger, http.StatusInternalServerError, api.CodeInternal, errInternalServerError)
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, buildHistoryPageResponse(events, nextCursor))
}

// parseRequestHistoryCursor decodes r's "before" query parameter via
// item_history_web.go's own parseHistoryCursor (unchanged, reused as-is —
// domain.HistoryCursor's wire form is exactly the same whether a browser's
// click-to-load fragment or this API asks for it), answering 400
// invalid_request naming "before" and reporting ok=false on a malformed
// value — exactly as the web handler treats it.
func parseRequestHistoryCursor(w http.ResponseWriter, logger *slog.Logger, r *http.Request) (*domain.HistoryCursor, bool) {
	cursor, err := parseHistoryCursor(requestCursor(r))
	if err != nil {
		writeMalformedCursor(w, logger)
		return nil, false
	}
	return cursor, true
}

// requestHistoryLimit parses r's "limit" query parameter for a history
// endpoint: defaultHistoryPageLimit when absent, blank, or not a positive
// integer, clamped to maxHistoryPageLimit otherwise — mirrors
// api_common.go's own requestLimit, with this file's own bounds.
func requestHistoryLimit(r *http.Request) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultHistoryPageLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultHistoryPageLimit
	}
	if n > maxHistoryPageLimit {
		return maxHistoryPageLimit
	}
	return n
}

// loadHistoryEventPage requests one row beyond limit so it can report
// whether an older page exists (a non-nil nextCursor) without a second
// round trip, then trims back to limit before returning — mirrors
// item_history_web.go's own loadHistoryPage "LIMIT+1" trick exactly, shared
// here via fetch since ItemHistory and BinHistory differ only in which
// lister method (ListByItem vs ListByBin) they page through.
func loadHistoryEventPage(before *domain.HistoryCursor, limit int, fetch func(domain.HistoryPage) ([]domain.ItemEvent, error)) (events []domain.ItemEvent, nextCursor *string, err error) {
	events, err = fetch(domain.HistoryPage{Limit: limit + 1, Before: before})
	if err != nil {
		return nil, nil, err
	}
	if len(events) <= limit {
		return events, nil, nil
	}
	last := events[limit-1]
	cursor := encodeHistoryCursor(domain.HistoryCursor{OccurredAt: last.OccurredAt, ID: last.ID})
	return events[:limit], &cursor, nil
}

// buildHistoryPageResponse projects a page of domain.ItemEvent into both
// history endpoints' shared response envelope.
func buildHistoryPageResponse(events []domain.ItemEvent, nextCursor *string) historyPageResponse {
	out := make([]historyEventResponse, 0, len(events))
	for _, e := range events {
		out = append(out, historyEventResponseFrom(e))
	}
	return historyPageResponse{Events: out, NextCursor: nextCursor}
}

// historyEventResponseFrom projects one domain.ItemEvent into the response
// shape — the one place every field pairing ItemEvent.Validate enforces
// (bin, move, edit) is read back out into JSON.
func historyEventResponseFrom(e domain.ItemEvent) historyEventResponse {
	return historyEventResponse{
		ID: e.ID.String(), Kind: e.Kind.String(), OccurredAt: e.OccurredAt,
		Note: stringPtrOrNil(e.Note),
		Actor: historyActorResponse{
			Kind: e.ActorKind.String(), Label: e.ActorLabel, UserID: historyActorUserID(e),
		},
		Bin:           historyBinResponseFrom(e),
		FromLocation:  historyLocationResponseFrom(e.FromLocationID, e.FromLocationLabel),
		ToLocation:    historyLocationResponseFrom(e.ToLocationID, e.ToLocationLabel),
		ChangedFields: changedFieldStrings(e.ChangedFields),
		Item:          historyItemRefResponse{ID: e.ItemID.String(), Name: e.ItemName},
	}
}

// historyActorUserID returns e's actor user id as a *string: nil for the
// integration principal (which NewItemEvent leaves at the zero UserID, see
// its own doc), the resolved id's string form otherwise — AC 3's "actor for
// user and integration principals alike" is Kind/Label always being
// present above; this field is the one that tells the two apart.
func historyActorUserID(e domain.ItemEvent) *string {
	if e.ActorKind != identity.KindUser {
		return nil
	}
	s := e.ActorUserID.String()
	return &s
}

// historyBinResponseFrom projects e's nullable bin snapshot: nil when
// e.BinID is nil (every kind but added/removed/returned/moved), the id/label
// pair otherwise.
func historyBinResponseFrom(e domain.ItemEvent) *historyBinResponse {
	if e.BinID == nil {
		return nil
	}
	return &historyBinResponse{ID: e.BinID.String(), Label: e.BinLabel}
}

// historyLocationResponseFrom projects one of e's two nullable location
// snapshots (FromLocationID/FromLocationLabel or ToLocationID/ToLocationLabel)
// — both nil together on every kind but moved, per ItemEvent's own
// all-or-nothing pairing.
func historyLocationResponseFrom(id *domain.LocationID, label string) *historyLocationResponse {
	if id == nil {
		return nil
	}
	return &historyLocationResponse{ID: id.String(), Label: label}
}

// changedFieldStrings projects []domain.EditedField into []string, always
// non-nil (an "edited" empty page group of one, or any other kind, both
// answer changed_fields as [] rather than null) — the JSON-friendlier
// contract this response format prefers over a nullable array.
func changedFieldStrings(fields []domain.EditedField) []string {
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		out = append(out, f.String())
	}
	return out
}

// stringPtrOrNil converts "" into nil, mirroring ItemEvent's own "empty
// string means unset" contract for Note/BinLabel/FromLocationLabel/
// ToLocationLabel (see ItemEvent's own doc) — the response-shape analog of
// item_event_postgres.go's nullableString, which does the same conversion
// for a SQL query parameter rather than a JSON field.
func stringPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
