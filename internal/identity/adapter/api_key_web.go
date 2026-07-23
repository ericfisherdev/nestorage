package adapter

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestcore/render"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
	"github.com/ericfisherdev/nestorage/web/components"
)

// apiKeyDateFormat is how a key row's timestamps render — formatted here in
// the handler, never in the template (components.APIKeyRowView carries
// pre-formatted strings, never a time.Time), matching every other web
// handler in this package.
const apiKeyDateFormat = "Jan 2, 2006"

// apiKeyOverlapOptions backs the rotate form's overlap <select>, in the
// same order every time.
var apiKeyOverlapOptions = []components.OverlapOption{
	{Value: domain.OverlapNone.String(), Label: "No overlap — invalidate immediately"},
	{Value: domain.Overlap24h.String(), Label: "24 hours"},
	{Value: domain.Overlap7d.String(), Label: "7 days"},
}

// apiKeyWebService is the narrow port (ISP) APIKeyWebHandlers depends on,
// satisfied by *app.APIKeyService.
type apiKeyWebService interface {
	Current(ctx context.Context) (*domain.APIKey, bool, error)
	List(ctx context.Context) ([]*domain.APIKey, error)
	Create(ctx context.Context, label string) (*domain.APIKey, string, error)
	Rotate(ctx context.Context, label string, overlap domain.OverlapWindow) (*domain.APIKey, string, error)
	Revoke(ctx context.Context, id domain.APIKeyID) error
}

// APIKeyWebHandlers serves the account's single api key (NSTR-23): status,
// history, creation, rotation, and revocation, all on one screen. Every
// route is registered on its own mux, mounted behind RequireUser then
// RequireAdmin by the composition root — unlike NSTR-22's per-user device
// screen, this credential is account-wide, so only an admin may touch it.
type APIKeyWebHandlers struct {
	keys   apiKeyWebService
	sm     *scs.SessionManager
	layout layoutFunc
	logger *slog.Logger
}

// NewAPIKeyWebHandlers constructs APIKeyWebHandlers. All dependencies are
// required; a missing one panics at construction time, matching every other
// WebHandlers constructor in this codebase.
func NewAPIKeyWebHandlers(keys apiKeyWebService, sm *scs.SessionManager, layout layoutFunc, logger *slog.Logger) *APIKeyWebHandlers {
	if keys == nil {
		panic("identity/adapter: NewAPIKeyWebHandlers requires a non-nil apiKeyWebService")
	}
	if sm == nil {
		panic("identity/adapter: NewAPIKeyWebHandlers requires a non-nil session manager")
	}
	if layout == nil {
		panic("identity/adapter: NewAPIKeyWebHandlers requires a non-nil layout func")
	}
	if logger == nil {
		panic("identity/adapter: NewAPIKeyWebHandlers requires a non-nil logger")
	}
	return &APIKeyWebHandlers{keys: keys, sm: sm, layout: layout, logger: logger}
}

// Routes registers the account api key routes on mux. mux is expected to be
// mounted behind RequireUser then RequireAdmin by the caller — see this
// type's own doc.
func (h *APIKeyWebHandlers) Routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /settings/api-key", h.View)
	mux.HandleFunc("POST /settings/api-key", h.Create)
	mux.HandleFunc("POST /settings/api-key/rotate", h.Rotate)
	mux.HandleFunc("POST /settings/api-key/revoke", h.Revoke)
}

// View handles GET /settings/api-key.
func (h *APIKeyWebHandlers) View(w http.ResponseWriter, r *http.Request) {
	h.renderView(w, r, http.StatusOK, "", nil)
}

// Create handles POST /settings/api-key: CSRF, then issues the account's
// first key. The create form is only ever shown when no key exists, but
// domain.ErrAPIKeyExists (mapped by mapAPIKeyError) is still the database's
// guard against racing this with a concurrent create, not just this
// handler's UI state.
func (h *APIKeyWebHandlers) Create(w http.ResponseWriter, r *http.Request) {
	if !h.verifyRequest(w, r) {
		return
	}
	key, raw, err := h.keys.Create(r.Context(), r.FormValue("label"))
	if err != nil {
		h.handleMutationError(w, r, err)
		return
	}
	h.renderView(w, r, http.StatusOK, "", &components.APIKeySecretReveal{Secret: raw, Label: key.Label})
}

// Rotate handles POST /settings/api-key/rotate: CSRF, then supersedes the
// current key (if any) and mints its replacement. overlap chooses how long
// the superseded key stays usable.
func (h *APIKeyWebHandlers) Rotate(w http.ResponseWriter, r *http.Request) {
	if !h.verifyRequest(w, r) {
		return
	}
	overlap, err := domain.ParseOverlapWindow(r.FormValue("overlap"))
	if err != nil {
		h.renderView(w, r, http.StatusUnprocessableEntity, "Please choose a valid overlap window.", nil)
		return
	}
	key, raw, err := h.keys.Rotate(r.Context(), r.FormValue("label"), overlap)
	if err != nil {
		h.handleMutationError(w, r, err)
		return
	}
	h.renderView(w, r, http.StatusOK, "", &components.APIKeySecretReveal{Secret: raw, Label: key.Label})
}

// Revoke handles POST /settings/api-key/revoke: CSRF, then revokes the key
// named by the id form field.
func (h *APIKeyWebHandlers) Revoke(w http.ResponseWriter, r *http.Request) {
	if !h.verifyRequest(w, r) {
		return
	}
	id, err := domain.ParseAPIKeyID(r.FormValue("id"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if err := h.keys.Revoke(r.Context(), id); err != nil {
		h.handleMutationError(w, r, err)
		return
	}
	h.renderView(w, r, http.StatusOK, "", nil)
}

// verifyRequest parses the form and verifies the CSRF token — the two
// checks every POST in this handler runs before doing anything else.
// Answers 400 or 403 and reports ok=false on failure.
func (h *APIKeyWebHandlers) verifyRequest(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	if !session.VerifyCSRF(r, h.sm) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return false
	}
	return true
}

// handleMutationError maps err to the screen's inline error state when
// recognized (mapAPIKeyError), or logs and answers a generic 500 otherwise.
func (h *APIKeyWebHandlers) handleMutationError(w http.ResponseWriter, r *http.Request, err error) {
	if status, msg, ok := mapAPIKeyError(err); ok {
		h.renderView(w, r, status, msg, nil)
		return
	}
	h.logger.ErrorContext(r.Context(), "api key: mutate", "error", err)
	http.Error(w, errInternalServerError, http.StatusInternalServerError)
}

// renderView loads the account's current key and full history and renders
// the screen at status, carrying formError and reveal — the shared tail of
// every handler above, success or failure alike. reveal is non-nil only in
// the same response as a successful create or rotate: it is never
// persisted or recomputed, so it can appear in no other response.
func (h *APIKeyWebHandlers) renderView(w http.ResponseWriter, r *http.Request, status int, formError string, reveal *components.APIKeySecretReveal) {
	current, hasCurrent, err := h.keys.Current(r.Context())
	if err != nil {
		h.logger.ErrorContext(r.Context(), "api key: current", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	keys, err := h.keys.List(r.Context())
	if err != nil {
		h.logger.ErrorContext(r.Context(), "api key: list", "error", err)
		http.Error(w, errInternalServerError, http.StatusInternalServerError)
		return
	}
	view := h.buildView(r.Context(), current, hasCurrent, keys, formError, reveal)
	h.renderPage(w, r, status, view)
}

// renderPage renders view at status: the bare fragment for an HX-Request, or
// the full shell page for a normal navigation. This reimplements
// render.Page's own HX-Request split locally (matching
// UsersWebHandlers.renderPage's own rationale) because render.Page
// hardcodes 200, and this handler needs to answer 404/409/422/500 on a
// mapped domain error while still re-rendering the screen's current state.
func (h *APIKeyWebHandlers) renderPage(w http.ResponseWriter, r *http.Request, status int, view components.APIKeySettingsView) {
	w.Header().Set("Vary", "HX-Request")
	content := components.APIKeySettingsSection(view)
	c := content
	if !render.IsHTMX(r) {
		c = h.layout(content)
	}
	if err := render.Render(r.Context(), w, status, c); err != nil {
		h.logger.ErrorContext(r.Context(), "api key: render", "error", err)
	}
}

// buildView maps the current key, the full history, and this request's
// pending form state into the view-layer model APIKeySettingsSection
// renders, formatting every timestamp here so the template never carries a
// time.Time.
func (h *APIKeyWebHandlers) buildView(ctx context.Context, current *domain.APIKey, hasCurrent bool, keys []*domain.APIKey, formError string, reveal *components.APIKeySecretReveal) components.APIKeySettingsView {
	now := time.Now()
	history := make([]components.APIKeyRowView, 0, len(keys))
	for _, k := range keys {
		history = append(history, toAPIKeyRowView(k, now))
	}
	var currentView *components.APIKeyRowView
	if hasCurrent {
		v := toAPIKeyRowView(current, now)
		currentView = &v
	}
	return components.APIKeySettingsView{
		CSRFToken: session.CSRFToken(ctx, h.sm),
		Current:   currentView,
		History:   history,
		Reveal:    reveal,
		FormError: formError,
		Overlaps:  apiKeyOverlapOptions,
	}
}

// toAPIKeyRowView maps one domain.APIKey into its view-layer row.
// ExpiresAt/RevokedAt render as "" when nil.
func toAPIKeyRowView(k *domain.APIKey, now time.Time) components.APIKeyRowView {
	return components.APIKeyRowView{
		ID:         k.ID.String(),
		Label:      k.Label,
		KeyPrefix:  k.KeyPrefix,
		Status:     k.Status(now).String(),
		CreatedAt:  k.CreatedAt.Format(apiKeyDateFormat),
		LastUsedAt: formatLastUsed(k.LastUsedAt),
		ExpiresAt:  formatOptionalDate(k.ExpiresAt),
		RevokedAt:  formatOptionalDate(k.RevokedAt),
	}
}

// formatOptionalDate renders "" for a nil timestamp, or apiKeyDateFormat
// otherwise.
func formatOptionalDate(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.Format(apiKeyDateFormat)
}

// mapAPIKeyError maps a domain error from an APIKeyService call to the HTTP
// status and inline message the screen re-renders with. ok reports whether
// err was recognized; an unrecognized error is the caller's cue to log it
// and answer a generic 500 instead.
func mapAPIKeyError(err error) (status int, message string, ok bool) {
	switch {
	case errors.Is(err, domain.ErrAPIKeyExists):
		return http.StatusConflict, "A current key already exists — rotate it instead of creating a new one.", true
	case errors.Is(err, domain.ErrAPIKeyNotFound):
		return http.StatusNotFound, "That key no longer exists.", true
	case errors.Is(err, domain.ErrInvalidAPIKey):
		return http.StatusUnprocessableEntity, "Please enter a label.", true
	default:
		return 0, "", false
	}
}
