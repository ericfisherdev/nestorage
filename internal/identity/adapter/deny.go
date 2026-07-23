package adapter

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/ericfisherdev/nestcore/render"
)

// apiPathPrefix marks NSTR-54's JSON api surface — a request under it gets
// Denier's JSON body regardless of HX-Request, matching every other
// JSON-vs-HTML split in this codebase.
const apiPathPrefix = "/api/"

// unauthorizedMessage and forbiddenMessage are the fixed, detail-free bodies
// every denial carries — see Deny's own doc for why no check's specific
// failure reason is ever included.
const (
	unauthorizedMessage = "unauthorized"
	forbiddenMessage    = "forbidden"
)

// denyBody is the JSON shape both denials answer with.
type denyBody struct {
	Error string `json:"error"`
}

// Denier writes a uniform 401/403 response for HTML, HTMX, and JSON callers,
// so no handler or middleware in this context invents its own denial shape.
// See Resolve, RequireAuthenticated, and RequireAdmin (middleware.go), its
// only callers.
type Denier struct {
	logger *slog.Logger
}

// NewDenier constructs Denier. logger is required; a nil value panics at
// construction time, matching every other constructor in this package.
func NewDenier(logger *slog.Logger) *Denier {
	if logger == nil {
		panic("identity/adapter: NewDenier requires a non-nil logger")
	}
	return &Denier{logger: logger}
}

// Deny writes status (http.StatusUnauthorized or http.StatusForbidden) in
// the shape r's caller expects:
//   - JSON, for a request under apiPathPrefix or naming application/json in
//     Accept: status plus a fixed {"error": "..."} body.
//   - HTMX (HX-Request: true): 401 carries an HX-Redirect: /login response
//     header so htmx performs a full-page navigation there instead of
//     swapping an error fragment into the page; 403 is a bare plain-text
//     response — the caller is already signed in, so redirecting to /login
//     would loop.
//   - Full navigation: 401 redirects (303) to /login?next=<original URI>,
//     reusing sanitizeNext's open-redirect guard; 403 is plain text, same as
//     the HTMX case — a styled forbidden page is out of scope for now.
//
// Every shape's body is fixed and carries no detail identifying which check
// failed, and is identical whether or not the underlying resource exists —
// denial happens here, before any handler or repository runs.
func (d *Denier) Deny(w http.ResponseWriter, r *http.Request, status int) {
	message := unauthorizedMessage
	if status == http.StatusForbidden {
		message = forbiddenMessage
	}
	switch {
	case isJSONRequest(r):
		d.writeJSON(w, status, message)
	case render.IsHTMX(r):
		denyHTMX(w, status, message)
	default:
		denyNavigation(w, r, status, message)
	}
}

// isJSONRequest reports whether r's caller expects a JSON error body: the
// public API surface (NSTR-54, under apiPathPrefix) or an explicit
// Accept: application/json.
func isJSONRequest(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, apiPathPrefix) || strings.Contains(r.Header.Get("Accept"), "application/json")
}

func (d *Denier) writeJSON(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(denyBody{Error: message}); err != nil {
		d.logger.Error("deny: encode response", "error", err)
	}
}

// denyHTMX answers an HTMX request — see Deny's own doc.
func denyHTMX(w http.ResponseWriter, status int, message string) {
	if status == http.StatusUnauthorized {
		w.Header().Set("HX-Redirect", "/login")
	}
	http.Error(w, message, status)
}

// denyNavigation answers a full browser navigation — see Deny's own doc.
func denyNavigation(w http.ResponseWriter, r *http.Request, status int, message string) {
	if status == http.StatusUnauthorized {
		target := "/login?next=" + url.QueryEscape(sanitizeNext(r.URL.RequestURI()))
		http.Redirect(w, r, target, http.StatusSeeOther)
		return
	}
	http.Error(w, message, status)
}
