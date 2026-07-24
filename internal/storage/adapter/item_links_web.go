package adapter

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/ericfisherdev/nestcore/render"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
	"github.com/ericfisherdev/nestorage/web/components"
)

// itemLinkOperator is the narrow port (ISP) ItemsWebHandlers depends on for
// the item detail page's labeled-links section (NSTR-38), satisfied by
// *app.ItemLinkService. Every method mirrors the visibility-scoped shape
// app.ItemLinkService's own doc describes: an unknown/invisible itemID
// surfaces as domain.ErrItemNotFound before a link is ever read or mutated.
type itemLinkOperator interface {
	Add(ctx context.Context, viewer identity.Principal, itemID domain.ItemID, label, rawURL string) (*domain.ItemLink, error)
	Edit(ctx context.Context, viewer identity.Principal, itemID domain.ItemID, linkID domain.ItemLinkID, label, rawURL string) error
	Remove(ctx context.Context, viewer identity.Principal, itemID domain.ItemID, linkID domain.ItemLinkID) error
	ListForItem(ctx context.Context, viewer identity.Principal, itemID domain.ItemID) ([]domain.ItemLink, error)
}

// AddLink handles POST /items/{id}/links: CSRF, ItemLinkService.Add, then
// re-render the links section fragment either way — a successful add and a
// rejected one (a bad label/url, or a lost-race invisible item) both redraw
// #item-links only, never the placement panel item_detail.templ also
// renders (see renderLinksSection's own doc).
func (h *ItemsWebHandlers) AddLink(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	if !verifyRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	if _, err := h.links.Add(r.Context(), viewer, id, r.FormValue("label"), r.FormValue("url")); err != nil {
		status, msg, mapped := mapItemLinkError(err)
		if !mapped {
			h.logger.ErrorContext(r.Context(), "items: links: add", "error", err)
			http.Error(w, errInternalServerError, http.StatusInternalServerError)
			return
		}
		h.renderLinksSection(w, r, viewer, id, status, msg)
		return
	}
	h.renderLinksSection(w, r, viewer, id, http.StatusOK, "")
}

// EditLink handles POST /items/{id}/links/{linkID}: CSRF,
// ItemLinkService.Edit, then re-render the links section fragment either
// way — see AddLink's own doc.
func (h *ItemsWebHandlers) EditLink(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	linkID, ok := h.pathItemLinkID(w, r)
	if !ok {
		return
	}
	if !verifyRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	if err := h.links.Edit(r.Context(), viewer, id, linkID, r.FormValue("label"), r.FormValue("url")); err != nil {
		status, msg, mapped := mapItemLinkError(err)
		if !mapped {
			h.logger.ErrorContext(r.Context(), "items: links: edit", "error", err)
			http.Error(w, errInternalServerError, http.StatusInternalServerError)
			return
		}
		h.renderLinksSection(w, r, viewer, id, status, msg)
		return
	}
	h.renderLinksSection(w, r, viewer, id, http.StatusOK, "")
}

// DeleteLink handles POST /items/{id}/links/{linkID}/delete: CSRF,
// ItemLinkService.Remove, then re-render the links section fragment either
// way — see AddLink's own doc.
func (h *ItemsWebHandlers) DeleteLink(w http.ResponseWriter, r *http.Request) {
	id, ok := h.pathItemID(w, r)
	if !ok {
		return
	}
	linkID, ok := h.pathItemLinkID(w, r)
	if !ok {
		return
	}
	if !verifyRequest(w, r, h.sm) {
		return
	}
	viewer, _ := identityadapter.CurrentPrincipal(r.Context())

	if err := h.links.Remove(r.Context(), viewer, id, linkID); err != nil {
		status, msg, mapped := mapItemLinkError(err)
		if !mapped {
			h.logger.ErrorContext(r.Context(), "items: links: delete", "error", err)
			http.Error(w, errInternalServerError, http.StatusInternalServerError)
			return
		}
		h.renderLinksSection(w, r, viewer, id, status, msg)
		return
	}
	h.renderLinksSection(w, r, viewer, id, http.StatusOK, "")
}

// pathItemLinkID parses the {linkID} path value, answering 400 and
// reporting ok=false on a malformed one — mirrors pathItemID's own shape.
func (h *ItemsWebHandlers) pathItemLinkID(w http.ResponseWriter, r *http.Request) (domain.ItemLinkID, bool) {
	id, err := domain.ParseItemLinkID(r.PathValue("linkID"))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return domain.ItemLinkID{}, false
	}
	return id, true
}

// renderLinksSection loads itemID's current links and re-renders the links
// section fragment at status, carrying formError — AddLink/EditLink/
// DeleteLink's shared tail. Unlike renderDetail, this redraws ONLY
// #item-links, never the placement panel above it: the self-contained
// section arrangement Sprint 5 reconciliation binds (see
// web/components/item_links.templ's own doc) so a link mutation never
// disturbs the check-out/return controls' own state.
func (h *ItemsWebHandlers) renderLinksSection(w http.ResponseWriter, r *http.Request, viewer identity.Principal, id domain.ItemID, status int, formError string) {
	links, err := h.links.ListForItem(r.Context(), viewer, id)
	if err != nil {
		h.handleItemGetError(w, r, err, "items: links: list")
		return
	}

	view := buildItemLinksView(id, links, session.CSRFToken(r.Context(), h.sm), formError)
	content := components.ItemLinksSection(view)
	if !render.IsHTMX(r) {
		content = h.layout(r, content)
	}
	if err := render.Render(r.Context(), w, status, content); err != nil {
		h.logger.ErrorContext(r.Context(), "items: render links section", "error", err)
	}
}

// mapItemLinkError maps a domain error from an ItemLinkService call to the
// HTTP status and inline message the links section re-renders with. ok
// reports whether err was recognized; an unrecognized error is the caller's
// cue to log it and answer a generic 500 instead — mirrors
// mapItemOperationError's own shape.
func mapItemLinkError(err error) (status int, message string, ok bool) {
	switch {
	case errors.Is(err, domain.ErrInvalidItemLinkURL):
		return http.StatusUnprocessableEntity, "Only http and https links are allowed.", true
	case errors.Is(err, domain.ErrItemLinkLabelRequired):
		return http.StatusUnprocessableEntity, "Please enter a label.", true
	case errors.Is(err, domain.ErrItemLinkLabelTooLong):
		return http.StatusUnprocessableEntity, fmt.Sprintf("Label must be %d characters or fewer.", domain.MaxItemLinkLabelRunes), true
	case errors.Is(err, domain.ErrItemLinkNotFound):
		return http.StatusNotFound, "That link no longer exists.", true
	case errors.Is(err, domain.ErrItemNotFound):
		return http.StatusNotFound, "That item no longer exists.", true
	default:
		return 0, "", false
	}
}

// buildItemLinksView projects itemID's []domain.ItemLink into
// ItemLinksSection's view model — renderDetail's own call site populates
// ItemDetailView.Links with this, and renderLinksSection reuses it for the
// section's own standalone fragment re-render.
func buildItemLinksView(itemID domain.ItemID, links []domain.ItemLink, csrfToken, formError string) components.ItemLinksView {
	views := make([]components.ItemLinkView, 0, len(links))
	for _, l := range links {
		views = append(views, components.ItemLinkView{ID: l.ID.String(), Label: l.Label, URL: l.URL})
	}
	return components.ItemLinksView{ItemID: itemID.String(), CSRFToken: csrfToken, Links: views, FormError: formError}
}
