package api

import (
	"log/slog"
	"net/http"
)

// Registrar is the minimal surface a bounded-context adapter needs to mount
// its own handlers under /api/v1: Handle and HandleFunc, matching
// *http.ServeMux's own method set. Bounded-context adapters depend on this
// interface, never on the concrete *http.ServeMux Router wraps (DIP) — Go
// 1.26's http.ServeMux has no route-enumeration API, so NSTR-57's OpenAPI
// spec-sync test substitutes a recording Registrar in place of Router to
// observe every pattern an adapter registers, which a concrete-type
// dependency would make impossible.
type Registrar interface {
	Handle(pattern string, handler http.Handler)
	HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request))
}

// Router is the /api/v1 mux. Every bounded context mounts its own handlers
// onto it through Handle/HandleFunc (the Registrar interface above), and
// Handler wraps the result with a JSON 404/405 fallback so an unmatched
// request under /api/v1 never falls through to net/http's own plain-text
// body.
type Router struct {
	mux    *http.ServeMux
	logger *slog.Logger
}

// NewRouter constructs Router. logger is required, matching every other
// constructor in this codebase; it is used only by the JSON fallback
// (jsonFallback) to log an encode failure.
func NewRouter(logger *slog.Logger) *Router {
	if logger == nil {
		panic("platform/api: NewRouter requires a non-nil logger")
	}
	return &Router{mux: http.NewServeMux(), logger: logger}
}

// Handle registers handler for pattern, matching http.ServeMux.Handle. This,
// and HandleFunc below, is the seam every bounded context's API adapter
// mounts its routes through — never the underlying *http.ServeMux directly
// (see Registrar's own doc for why).
func (rt *Router) Handle(pattern string, handler http.Handler) {
	rt.mux.Handle(pattern, handler)
}

// HandleFunc registers handler for pattern, matching http.ServeMux.HandleFunc.
func (rt *Router) HandleFunc(pattern string, handler func(http.ResponseWriter, *http.Request)) {
	rt.mux.HandleFunc(pattern, handler)
}

// Handler returns rt's mux wrapped with the JSON 404/405 fallback described
// in jsonFallback's own doc — the http.Handler the composition root mounts
// at "/api/v1/".
func (rt *Router) Handler() http.Handler {
	return jsonFallback(rt.mux, rt.logger)
}

// jsonFallback wraps mux so an unmatched request answers with the shared
// error envelope instead of net/http's own plain-text 404/405 body.
//
// mux.Handler(r) is probed first, read-only, purely to classify the
// request — it does not mutate r. An empty returned pattern means mux
// itself would answer — no registered route matched the request — and its
// handler is one of exactly two the stdlib returns in that case: a 404
// (NotFoundHandler) or a 405 with an Allow header set (verified against Go
// 1.26's own net/http.ServeMux.findHandler). That handler is served through
// statusCapturer, which discards the plain-text body so writeFallbackError
// can substitute the JSON envelope, while leaving Header() untouched so the
// 405 branch's Allow header still lands on the real response.
//
// A non-empty pattern means either a real handler matched, or ServeMux
// issued its own 307 path-canonicalization redirect (which carries a
// Location header and no HTML page — not the redirect class the "no API
// route ever returns an HTML redirect" AC forbids). Either way the request
// is handed to mux.ServeHTTP itself, not to the already-probed handler
// directly: ServeHTTP is what writes the matched pattern into r.Pattern (see
// net/http.ServeMux.ServeHTTP), which Observe (observe.go) reads back for
// its route label, and which a path-parameter handler (r.PathValue) needs
// populated too.
func jsonFallback(mux *http.ServeMux, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		capture := &statusCapturer{ResponseWriter: w}
		h.ServeHTTP(capture, r)
		writeFallbackError(w, logger, capture.status)
	})
}

// writeFallbackError translates a status captured from mux's own fallback
// handler into the shared envelope: 405 keeps its status and gets
// CodeMethodNotAllowed, anything else (in practice always 404) is answered
// as CodeNotFound.
func writeFallbackError(w http.ResponseWriter, logger *slog.Logger, status int) {
	if status == http.StatusMethodNotAllowed {
		WriteError(w, logger, status, CodeMethodNotAllowed, http.StatusText(status))
		return
	}
	WriteError(w, logger, http.StatusNotFound, CodeNotFound, http.StatusText(http.StatusNotFound))
}

// statusCapturer intercepts WriteHeader/Write so writeFallbackError can
// substitute the JSON envelope for whatever plain-text body the wrapped
// handler wrote. Header() is deliberately NOT overridden: it passes through
// to the real ResponseWriter, so a header the wrapped handler sets directly
// on it (the 405 branch's Allow header) still reaches the client even though
// the body is discarded.
type statusCapturer struct {
	http.ResponseWriter
	status int
}

func (c *statusCapturer) WriteHeader(status int) { c.status = status }

func (c *statusCapturer) Write(b []byte) (int, error) { return len(b), nil }
