package adapter

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/api"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// maxAPIRequestBytes bounds every /api/v1 CRUD endpoint's request body in
// this package — a handful of scalar fields never needs more than this,
// mirroring identity/adapter's own maxDeviceTokenRequestBytes bound
// (devicetokenapi.go), generalized here since Locations/Bins/ItemsAPIHandlers
// all share it.
const maxAPIRequestBytes = 1 << 20 // 1 MiB

// defaultPageLimit and maxPageLimit bound every list endpoint's "limit"
// query parameter: 50 when absent, clamped to 200 — household scale (dozens
// of rows per resource, the same rationale LocationService documents for its
// own in-memory bin counts) makes both the default and the ceiling generous
// rather than tight.
const (
	defaultPageLimit = 50
	maxPageLimit     = 200
)

// errMalformedCursor is returned by decodeCursor/page for a "before" value
// that does not decode to a well-formed pageCursor — the caller answers 400
// invalid_request with a field detail naming "before", never a 500.
var errMalformedCursor = errors.New("storage/adapter: malformed pagination cursor")

// pageCursor is the opaque keyset-pagination anchor every list endpoint in
// this package (and NSTR-55's own history paging, which reuses this exact
// type and the page func below) encodes into the "before" request parameter
// and the next_cursor response field: the last row's own sort key plus its
// id, so an insert before the anchor can never shift or duplicate a page —
// the AC this ticket's own api_common_test.go proves directly.
type pageCursor struct {
	K  string `json:"k"`
	ID string `json:"id"`
}

// encodeCursor base64url-encodes (no padding) c into the wire form every
// "before" parameter and next_cursor field shares. pageCursor cannot fail to
// marshal — two string fields, no cycles — so the error is deliberately
// discarded.
func encodeCursor(c pageCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// decodeCursor reverses encodeCursor, returning errMalformedCursor for
// anything that does not decode to a well-formed pageCursor.
func decodeCursor(s string) (pageCursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return pageCursor{}, errMalformedCursor
	}
	var c pageCursor
	if err := json.Unmarshal(b, &c); err != nil {
		return pageCursor{}, errMalformedCursor
	}
	return c, nil
}

// requestLimit parses r's "limit" query parameter: defaultPageLimit when
// absent, blank, or not a positive integer, clamped to maxPageLimit
// otherwise — a malformed limit degrades to the default rather than
// rejecting the request, since (unlike a cursor) a bad limit cannot corrupt
// pagination stability, only the page size.
func requestLimit(r *http.Request) int {
	raw := r.URL.Query().Get("limit")
	if raw == "" {
		return defaultPageLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return defaultPageLimit
	}
	if n > maxPageLimit {
		return maxPageLimit
	}
	return n
}

// requestCursor returns r's "before" query parameter — the one name every
// list endpoint's continuation token is passed under, matching next_cursor's
// own response-side name (see pageCursor's own doc).
func requestCursor(r *http.Request) string {
	return r.URL.Query().Get("before")
}

// cursorKeyFunc projects one item of a page[T]-paginated slice into the
// (sort key, id) pair pageCursor anchors on — items must already be sorted
// by exactly this (key, id) ordering; page does not sort them itself.
type cursorKeyFunc[T any] func(item T) (key, id string)

// page returns the window of items strictly after cursor's anchor (the
// start of items when cursor is ""), up to limit items, plus the cursor
// encoding the window's own last row — "" when this is the last page. items
// must already be in keyFn's own (key, id) order (name-then-id for
// locations/bins/items today). Returns errMalformedCursor, unwrapped, when
// cursor does not decode — the caller answers 400 invalid_request, naming
// "before" as the offending field.
//
// An anchor that no longer appears in items (the row it pointed to was
// deleted between requests) resolves to the end of the slice rather than the
// start: restarting from position zero would re-serve rows the caller
// already has, which is a worse failure mode than simply ending the listing
// early.
func page[T any](items []T, cursor string, limit int, keyFn cursorKeyFunc[T]) (window []T, nextCursor string, err error) {
	start := 0
	if cursor != "" {
		anchor, decodeErr := decodeCursor(cursor)
		if decodeErr != nil {
			return nil, "", errMalformedCursor
		}
		start = len(items)
		for i, it := range items {
			k, id := keyFn(it)
			if k == anchor.K && id == anchor.ID {
				start = i + 1
				break
			}
		}
	}
	if start >= len(items) {
		return []T{}, "", nil
	}

	end := start + limit
	if end >= len(items) {
		return items[start:], "", nil
	}
	lastK, lastID := keyFn(items[end-1])
	return items[start:end], encodeCursor(pageCursor{K: lastK, ID: lastID}), nil
}

// cursorOrNil converts page's "" (no next page) into a nil *string and any
// other cursor into a pointer to it — next_cursor's own wire contract:
// present and null on the last page, never an omitted field.
func cursorOrNil(cursor string) *string {
	if cursor == "" {
		return nil
	}
	return &cursor
}

// decodeJSONBody bounds r's body to maxAPIRequestBytes and decodes it into
// dst, answering 400 invalid_request and reporting ok=false on a malformed
// or oversized body — every POST/PUT handler in this package's own first
// step, mirroring identity/adapter's DeviceTokenAPIHandlers.Issue.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, logger *slog.Logger, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxAPIRequestBytes)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		api.WriteError(w, logger, http.StatusBadRequest, api.CodeInvalidRequest, "malformed request body")
		return false
	}
	return true
}

// parsePathID parses r's path parameter named param with parse (e.g.
// domain.ParseBinID), answering 400 invalid_request and reporting ok=false
// on a malformed value — the JSON-envelope analog of the web adapter's own
// pathBinID/pathLocationID helpers (bins_web.go/locations_web.go), generic
// over every id type this package's three API handlers path-address.
func parsePathID[T any](w http.ResponseWriter, r *http.Request, logger *slog.Logger, param string, parse func(string) (T, error)) (T, bool) {
	id, err := parse(r.PathValue(param))
	if err != nil {
		var zero T
		api.WriteError(w, logger, http.StatusBadRequest, api.CodeInvalidRequest, "malformed "+param)
		return zero, false
	}
	return id, true
}

// parseOptionalUserID parses s into a *identity.UserID: nil for an empty
// string (a query parameter, e.g. items_api.go's own ?holder=, left unset),
// or a wrapped parse error otherwise.
func parseOptionalUserID(s string) (*identity.UserID, error) {
	if s == "" {
		return nil, nil
	}
	id, err := identity.ParseUserID(s)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// parseOptionalID parses *s into a *T with parse (e.g. domain.ParseLocationID),
// reporting nil, nil for a nil or empty s — a JSON request body's own
// "field omitted or explicitly blank" case, distinct from parseOptionalUserID's
// query-parameter shape only in accepting a *string rather than a bare one.
func parseOptionalID[T any](s *string, parse func(string) (T, error)) (*T, error) {
	if s == nil || *s == "" {
		return nil, nil
	}
	id, err := parse(*s)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

// optionalIDString projects a nullable typed id (e.g. *domain.LocationID)
// into its response DTO's *string form: nil stays nil, a set id becomes its
// canonical String() form. Generic over every id type this package's three
// API handlers expose as a nullable response field (location's ParentID,
// bin's OwnerID, item's CurrentBinID/HeldBy).
func optionalIDString[T interface{ String() string }](id *T) *string {
	if id == nil {
		return nil
	}
	s := (*id).String()
	return &s
}

// writeMalformedField answers 400 invalid_request naming field as malformed
// — a request-body id that fails to parse (as opposed to a domain validation
// failure, which goes through writeDomainError/mapDomainError instead).
func writeMalformedField(w http.ResponseWriter, logger *slog.Logger, field string) {
	api.WriteError(w, logger, http.StatusBadRequest, api.CodeInvalidRequest, "malformed request body",
		api.FieldDetail{Field: field, Message: "malformed id"})
}

// writeMalformedCursor answers 400 invalid_request naming "before" as
// malformed — every list endpoint's own page(...) failure path.
func writeMalformedCursor(w http.ResponseWriter, logger *slog.Logger) {
	api.WriteError(w, logger, http.StatusBadRequest, api.CodeInvalidRequest, "malformed request",
		api.FieldDetail{Field: "before", Message: "malformed cursor"})
}

// requireUserPrincipal reports whether viewer may drive a create endpoint in
// this package: true for a real person (identity.KindUser), false — after
// answering 403 forbidden with a machine-readable code — for NSTR-23's
// account api key (identity.KindIntegration), which has no person behind it
// to attribute a created_by column to. Mirrors the same rule
// ErrHolderRequired already enforces for item check-out (operations.go);
// every create endpoint (Locations/Bins/Items) calls this first, while every
// read and every remaining mutation stays open to both credential kinds, per
// the ticket's own scope.
func requireUserPrincipal(w http.ResponseWriter, logger *slog.Logger, viewer identity.Principal) bool {
	if viewer.Kind == identity.KindUser {
		return true
	}
	api.WriteError(w, logger, http.StatusForbidden, api.CodeForbidden, "only a user principal may create this resource")
	return false
}

// mapDomainError maps a domain/identity sentinel from a Locations/Bins/Items
// service call to the shared envelope's status, code, message, and — for a
// 422 — the FieldDetail naming which field failed. ok reports whether err
// was recognized; an unrecognized error is the caller's cue (writeDomainError)
// to log it and answer a logged 500 instead — the JSON-envelope
// generalization of mapBinError/mapLocationError's own recognized/
// unrecognized split (bins_web.go/locations_web.go), shared across all three
// resources rather than duplicated per handler file.
//
// Every not-found sentinel — location, bin, or item, regardless of which
// service call raised it (including a dangling foreign key on a create, e.g.
// an unknown location_id on POST /api/v1/bins) — answers 404, per the
// ticket's own table: this API never distinguishes "the thing you asked for
// doesn't exist" from "the thing you referenced doesn't exist" with a
// different status.
func mapDomainError(err error) (status int, code api.Code, message string, detail *api.FieldDetail, ok bool) {
	switch {
	case errors.Is(err, domain.ErrLocationNotFound),
		errors.Is(err, domain.ErrBinNotFound),
		errors.Is(err, domain.ErrItemNotFound):
		return http.StatusNotFound, api.CodeNotFound, "resource not found", nil, true

	case errors.Is(err, domain.ErrInvalidLocationName):
		return invalidField("name", "must not be blank and at most 100 characters")
	case errors.Is(err, domain.ErrItemNameRequired):
		return invalidField("name", "must not be blank")
	case errors.Is(err, domain.ErrInvalidQuantity):
		return invalidField("quantity", "must be a positive integer")
	case errors.Is(err, domain.ErrInvalidVisibility):
		return invalidField("visibility", `must be "public" or "private"`)
	case errors.Is(err, domain.ErrInvalidPlacement):
		return invalidField("bin_id", "item must be placed in exactly one bin")
	case errors.Is(err, domain.ErrInvalidBin):
		// Reached only if a request gets past this package's own
		// pre-validation (ValidateBinCode/ValidateBinName,
		// domain.ParseVisibility) with an otherwise-malformed bin —
		// defensive, with no single field to name at this remove.
		return http.StatusUnprocessableEntity, api.CodeInvalidRequest, "invalid request", nil, true

	case errors.Is(err, domain.ErrDuplicateBinCode):
		return http.StatusConflict, api.CodeConflict, "that code is already in use", nil, true
	case errors.Is(err, domain.ErrLocationNotEmpty),
		errors.Is(err, domain.ErrBinNotEmpty):
		return http.StatusConflict, api.CodeConflict, "resource is not empty", nil, true

	case errors.Is(err, identity.ErrUserNotFound):
		// The only reachable call site for this sentinel through this
		// ticket's own endpoints is a bin's owner_id (bins_api.go's Create
		// and Edit) — items never accept a user id through this API.
		return invalidField("owner_id", "references an unknown user")

	default:
		return 0, "", "", nil, false
	}
}

// invalidField builds mapDomainError's common 422 shape: the generic
// "invalid request" top-level message every field-level failure shares
// (matching identity/adapter's DeviceTokenAPIHandlers.handleIssueError
// convention), with message naming the specific rule field failed.
func invalidField(field, message string) (int, api.Code, string, *api.FieldDetail, bool) {
	return http.StatusUnprocessableEntity, api.CodeInvalidRequest, "invalid request",
		&api.FieldDetail{Field: field, Message: message}, true
}

// writeInvalidField writes invalidField's exact 422 shape directly — for a
// handler's own pre-validation (e.g. bins_api.go's ValidateBinCode/
// ValidateBinName calls, ahead of a service method that does not itself
// distinguish which field an ErrInvalidBin names), as opposed to
// writeDomainError's job of mapping an error a service call already
// returned.
func writeInvalidField(w http.ResponseWriter, logger *slog.Logger, field, message string) {
	status, code, msg, detail, _ := invalidField(field, message)
	api.WriteError(w, logger, status, code, msg, *detail)
}

// writeDomainError maps err via mapDomainError and writes the shared
// envelope, or logs and answers a generic 500 when err is unrecognized — the
// one place every handler in this package routes a service-layer error
// through, so no handler duplicates mapDomainError's switch.
func writeDomainError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error, op string) {
	status, code, message, detail, ok := mapDomainError(err)
	if !ok {
		logger.ErrorContext(r.Context(), op, "error", err)
		api.WriteError(w, logger, http.StatusInternalServerError, api.CodeInternal, errInternalServerError)
		return
	}
	if detail == nil {
		api.WriteError(w, logger, status, code, message)
		return
	}
	api.WriteError(w, logger, status, code, message, *detail)
}
