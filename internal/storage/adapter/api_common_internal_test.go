package adapter

// This file is package adapter (white-box), not adapter_test — mirroring
// web_common_internal_test.go's own exception: page, pageCursor,
// mapDomainError, and the rest of api_common.go's helpers are unexported,
// and NSTR-55 (history pagination) depends on the exact stability contract
// page's own doc promises, which is only directly testable from inside this
// package.

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/api"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// assertEnvelopeCode decodes rec's body as the shared envelope (platform/api)
// and asserts its error code — mirrors platform/api's own router_test.go
// helper of the same name, duplicated here since that one lives in a
// different package (api_test) this white-box file cannot import from.
func assertEnvelopeCode(t *testing.T, rec *httptest.ResponseRecorder, wantCode string) {
	t.Helper()
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Error.Code != wantCode {
		t.Errorf("code = %q, want %q", body.Error.Code, wantCode)
	}
}

func testAPILogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

// stringPair is page's own test fixture: a minimal (key, id) carrier so
// cursorKeyFunc's contract can be exercised without any real domain type.
type stringPair struct {
	key, id string
}

func stringPairKey(p stringPair) (string, string) { return p.key, p.id }

func TestEncodeDecodeCursor_RoundTrips(t *testing.T) {
	want := pageCursor{K: "Garage", ID: "abc-123"}
	got, err := decodeCursor(encodeCursor(want))
	if err != nil {
		t.Fatalf("decodeCursor(encodeCursor(...)): %v", err)
	}
	if got != want {
		t.Errorf("round-tripped cursor = %+v, want %+v", got, want)
	}
}

func TestDecodeCursor_MalformedRejected(t *testing.T) {
	tests := []string{
		"not-base64url!!!",
		"aGVsbG8", // valid base64url, but not JSON at all
	}
	for _, raw := range tests {
		if _, err := decodeCursor(raw); !errors.Is(err, errMalformedCursor) {
			t.Errorf("decodeCursor(%q) error = %v, want errMalformedCursor", raw, err)
		}
	}
}

func TestPage_MalformedCursorRejected(t *testing.T) {
	items := []stringPair{{"a", "1"}, {"b", "2"}}
	if _, _, err := page(items, "not-a-cursor!!!", 50, stringPairKey); !errors.Is(err, errMalformedCursor) {
		t.Errorf("page(malformed cursor) error = %v, want errMalformedCursor", err)
	}
}

func TestPage_FirstPageNoCursor(t *testing.T) {
	items := []stringPair{{"a", "1"}, {"b", "2"}, {"c", "3"}}
	window, next, err := page(items, "", 2, stringPairKey)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if len(window) != 2 || window[0].key != "a" || window[1].key != "b" {
		t.Errorf("page() window = %+v, want [a b]", window)
	}
	if next == "" {
		t.Error("page() next cursor = \"\", want a non-empty continuation token (more rows remain)")
	}
}

func TestPage_LastPageHasNoNextCursor(t *testing.T) {
	items := []stringPair{{"a", "1"}, {"b", "2"}}
	window, next, err := page(items, "", 50, stringPairKey)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if len(window) != 2 {
		t.Errorf("page() window = %+v, want both rows (limit exceeds total)", window)
	}
	if next != "" {
		t.Errorf("page() next cursor = %q, want \"\" (last page)", next)
	}
}

func TestPage_ContinuesFromCursor(t *testing.T) {
	items := []stringPair{{"a", "1"}, {"b", "2"}, {"c", "3"}, {"d", "4"}}
	_, cursor, err := page(items, "", 2, stringPairKey)
	if err != nil {
		t.Fatalf("page (first): %v", err)
	}
	if cursor == "" {
		t.Fatal("page (first) next cursor = \"\", want a continuation token")
	}

	secondWindow, next, err := page(items, cursor, 2, stringPairKey)
	if err != nil {
		t.Fatalf("page (second): %v", err)
	}
	if len(secondWindow) != 2 || secondWindow[0].key != "c" || secondWindow[1].key != "d" {
		t.Errorf("page (second) window = %+v, want [c d]", secondWindow)
	}
	if next != "" {
		t.Errorf("page (second) next cursor = %q, want \"\" (last page)", next)
	}
}

// TestPage_StableAcrossConcurrentInsertBeforeAnchor is the AC this ticket's
// own pagination contract makes directly: a row inserted before the anchor
// between two page requests must never shift the second page's own window,
// and must never appear twice across the two pages combined — the keyset
// (not offset) design's entire point.
func TestPage_StableAcrossConcurrentInsertBeforeAnchor(t *testing.T) {
	items := []stringPair{{"b", "2"}, {"c", "3"}, {"d", "4"}}
	firstWindow, cursor, err := page(items, "", 1, stringPairKey)
	if err != nil {
		t.Fatalf("page (first): %v", err)
	}
	if len(firstWindow) != 1 || firstWindow[0].key != "b" {
		t.Fatalf("page (first) window = %+v, want [b]", firstWindow)
	}

	// Simulate a concurrent insert of a row that sorts BEFORE the anchor
	// ("b") between the first and second request — the exact scenario the
	// AC names.
	withInsert := []stringPair{{"a", "0"}, {"b", "2"}, {"c", "3"}, {"d", "4"}}

	secondWindow, _, err := page(withInsert, cursor, 2, stringPairKey)
	if err != nil {
		t.Fatalf("page (second): %v", err)
	}

	seen := map[string]bool{}
	for _, it := range firstWindow {
		seen[it.id] = true
	}
	for _, it := range secondWindow {
		if seen[it.id] {
			t.Errorf("row id=%s appeared in both pages — duplicate after concurrent insert", it.id)
		}
		seen[it.id] = true
	}
	if seen["0"] {
		t.Error("the newly-inserted row (id=0) leaked into the second page — it sorts before the anchor and must not appear")
	}
	if !seen["3"] || !seen["4"] {
		t.Errorf("second page = %+v, want rows c and d (no skip)", secondWindow)
	}
}

func TestPage_AnchorNoLongerPresentEndsListing(t *testing.T) {
	// The anchor row ("b") was deleted between requests — decodeCursor still
	// succeeds (it is well-formed), but no item matches it.
	items := []stringPair{{"c", "3"}, {"d", "4"}}
	cursor := encodeCursor(pageCursor{K: "b", ID: "2"})

	window, next, err := page(items, cursor, 50, stringPairKey)
	if err != nil {
		t.Fatalf("page: %v", err)
	}
	if len(window) != 0 || next != "" {
		t.Errorf("page(stale anchor) = (%+v, %q), want (empty, \"\") rather than restarting from the beginning", window, next)
	}
}

func TestCursorOrNil(t *testing.T) {
	if got := cursorOrNil(""); got != nil {
		t.Errorf("cursorOrNil(\"\") = %v, want nil", got)
	}
	got := cursorOrNil("abc")
	if got == nil || *got != "abc" {
		t.Errorf("cursorOrNil(%q) = %v, want a pointer to it", "abc", got)
	}
}

func TestRequestLimit(t *testing.T) {
	tests := []struct {
		name  string
		query string
		want  int
	}{
		{"absent defaults", "", defaultPageLimit},
		{"blank defaults", "limit=", defaultPageLimit},
		{"zero defaults", "limit=0", defaultPageLimit},
		{"negative defaults", "limit=-5", defaultPageLimit},
		{"non-numeric defaults", "limit=abc", defaultPageLimit},
		{"within range honored", "limit=10", 10},
		{"exceeding max clamped", "limit=" + strconv.Itoa(maxPageLimit+50), maxPageLimit},
		{"exactly at max honored", "limit=" + strconv.Itoa(maxPageLimit), maxPageLimit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/x?"+tt.query, nil)
			if got := requestLimit(req); got != tt.want {
				t.Errorf("requestLimit(%q) = %d, want %d", tt.query, got, tt.want)
			}
		})
	}
}

func TestRequestCursor(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/x?before=abc123", nil)
	if got := requestCursor(req); got != "abc123" {
		t.Errorf("requestCursor() = %q, want %q", got, "abc123")
	}
}

func TestOptionalIDString(t *testing.T) {
	if got := optionalIDString[domain.LocationID](nil); got != nil {
		t.Errorf("optionalIDString(nil) = %v, want nil", got)
	}
	id := domain.NewLocationID()
	got := optionalIDString(&id)
	if got == nil || *got != id.String() {
		t.Errorf("optionalIDString(&id) = %v, want %q", got, id.String())
	}
}

func TestParseOptionalID(t *testing.T) {
	got, err := parseOptionalID[domain.LocationID](nil, domain.ParseLocationID)
	if err != nil || got != nil {
		t.Errorf("parseOptionalID(nil) = (%v, %v), want (nil, nil)", got, err)
	}

	empty := ""
	got, err = parseOptionalID(&empty, domain.ParseLocationID)
	if err != nil || got != nil {
		t.Errorf("parseOptionalID(&\"\") = (%v, %v), want (nil, nil)", got, err)
	}

	id := domain.NewLocationID()
	s := id.String()
	got, err = parseOptionalID(&s, domain.ParseLocationID)
	if err != nil || got == nil || *got != id {
		t.Errorf("parseOptionalID(&valid) = (%v, %v), want (%v, nil)", got, err, id)
	}

	malformed := "not-a-uuid"
	if _, err := parseOptionalID(&malformed, domain.ParseLocationID); err == nil {
		t.Error("parseOptionalID(malformed) error = nil, want a parse error")
	}
}

func TestParseOptionalUserID(t *testing.T) {
	got, err := parseOptionalUserID("")
	if err != nil || got != nil {
		t.Errorf("parseOptionalUserID(\"\") = (%v, %v), want (nil, nil)", got, err)
	}
	if _, err := parseOptionalUserID("not-a-uuid"); err == nil {
		t.Error("parseOptionalUserID(malformed) error = nil, want a parse error")
	}
}

func TestRequireUserPrincipal(t *testing.T) {
	t.Run("user principal allowed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		viewer := identity.NewUserPrincipal(identity.NewUserID(), identity.RoleAdult, "Alice")
		if !requireUserPrincipal(rec, testAPILogger(), viewer) {
			t.Error("requireUserPrincipal(user) = false, want true")
		}
		if rec.Body.Len() != 0 {
			t.Errorf("requireUserPrincipal(user) wrote a body %q, want none", rec.Body.String())
		}
	})

	t.Run("integration principal denied with 403", func(t *testing.T) {
		rec := httptest.NewRecorder()
		viewer := identity.NewIntegrationPrincipal("Nestova")
		if requireUserPrincipal(rec, testAPILogger(), viewer) {
			t.Error("requireUserPrincipal(integration) = true, want false")
		}
		if rec.Code != http.StatusForbidden {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusForbidden)
		}
		assertEnvelopeCode(t, rec, string(api.CodeForbidden))
	})
}

func TestMapDomainError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantField  string
	}{
		{"location not found", domain.ErrLocationNotFound, http.StatusNotFound, ""},
		{"bin not found", domain.ErrBinNotFound, http.StatusNotFound, ""},
		{"item not found", domain.ErrItemNotFound, http.StatusNotFound, ""},
		{"invalid location name", domain.ErrInvalidLocationName, http.StatusUnprocessableEntity, "name"},
		{"item name required", domain.ErrItemNameRequired, http.StatusUnprocessableEntity, "name"},
		{"invalid quantity", domain.ErrInvalidQuantity, http.StatusUnprocessableEntity, "quantity"},
		{"invalid visibility", domain.ErrInvalidVisibility, http.StatusUnprocessableEntity, "visibility"},
		{"invalid placement", domain.ErrInvalidPlacement, http.StatusUnprocessableEntity, "bin_id"},
		{"invalid bin (defensive)", domain.ErrInvalidBin, http.StatusUnprocessableEntity, ""},
		{"duplicate bin code", domain.ErrDuplicateBinCode, http.StatusConflict, ""},
		{"location not empty", domain.ErrLocationNotEmpty, http.StatusConflict, ""},
		{"bin not empty", domain.ErrBinNotEmpty, http.StatusConflict, ""},
		{"user not found", identity.ErrUserNotFound, http.StatusUnprocessableEntity, "owner_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, _, _, detail, ok := mapDomainError(tt.err)
			if !ok {
				t.Fatalf("mapDomainError(%v) ok = false, want true", tt.err)
			}
			if status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
			gotField := ""
			if detail != nil {
				gotField = detail.Field
			}
			if gotField != tt.wantField {
				t.Errorf("field = %q, want %q", gotField, tt.wantField)
			}
		})
	}
}

func TestMapDomainError_UnrecognizedReportsNotOK(t *testing.T) {
	if _, _, _, _, ok := mapDomainError(errors.New("boom")); ok {
		t.Error("mapDomainError(unrecognized) ok = true, want false")
	}
}

func TestWriteDomainError_UnrecognizedIsLogged500(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	writeDomainError(rec, req, testAPILogger(), errors.New("boom"), "test op")

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	assertEnvelopeCode(t, rec, string(api.CodeInternal))
}

func TestDecodeJSONBody(t *testing.T) {
	t.Run("malformed body rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader("{not json"))
		var dst struct{}
		if decodeJSONBody(rec, req, testAPILogger(), &dst) {
			t.Error("decodeJSONBody(malformed) = true, want false")
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("well-formed body decoded", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"name":"Garage"}`))
		var dst struct {
			Name string `json:"name"`
		}
		if !decodeJSONBody(rec, req, testAPILogger(), &dst) {
			t.Fatal("decodeJSONBody(well-formed) = false, want true")
		}
		if dst.Name != "Garage" {
			t.Errorf("decoded name = %q, want %q", dst.Name, "Garage")
		}
	})
}

func TestParsePathID(t *testing.T) {
	t.Run("malformed rejected", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x/not-a-uuid", nil)
		req.SetPathValue("id", "not-a-uuid")
		if _, ok := parsePathID(rec, req, testAPILogger(), "id", domain.ParseLocationID); ok {
			t.Error("parsePathID(malformed) ok = true, want false")
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("well-formed parsed", func(t *testing.T) {
		id := domain.NewLocationID()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x/"+id.String(), nil)
		req.SetPathValue("id", id.String())
		got, ok := parsePathID(rec, req, testAPILogger(), "id", domain.ParseLocationID)
		if !ok || got != id {
			t.Errorf("parsePathID(%q) = (%v, %v), want (%v, true)", id.String(), got, ok, id)
		}
	})
}
