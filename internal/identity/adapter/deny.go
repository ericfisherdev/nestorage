package adapter

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/ericfisherdev/nestcore/render"

	"github.com/ericfisherdev/nestorage/internal/platform/api"
)

// unauthorizedMessage and forbiddenMessage are the fixed, detail-free bodies
// every denial carries — see Deny's own doc for why no check's specific
// failure reason is ever included.
const (
	unauthorizedMessage = "unauthorized"
	forbiddenMessage    = "forbidden"
)

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
//   - JSON, for a request under api.PathPrefix or naming application/json in
//     Accept: status plus the shared error envelope (platform/api), coded
//     api.CodeUnauthorized or api.CodeForbidden.
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
// public API surface (under api.PathPrefix) or an explicit
// Accept: application/json.
func isJSONRequest(r *http.Request) bool {
	return strings.HasPrefix(r.URL.Path, api.PathPrefix) || strings.Contains(r.Header.Get("Accept"), "application/json")
}

// writeJSON answers through the shared envelope (platform/api), coding
// status as api.CodeUnauthorized or api.CodeForbidden — Deny's only two
// possible statuses.
func (d *Denier) writeJSON(w http.ResponseWriter, status int, message string) {
	code := api.CodeUnauthorized
	if status == http.StatusForbidden {
		code = api.CodeForbidden
	}
	api.WriteError(w, d.logger, status, code, message)
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
