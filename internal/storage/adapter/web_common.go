package adapter

import (
	"context"
	"net/http"
	"time"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestcore/render"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/web/components"
)

// errInternalServerError is the generic message every unexpected failure in
// this package's web handlers answers with. Mirrors identity/adapter's own
// constant of the same name — that one is unexported to its package, so it
// cannot be reused directly here.
const errInternalServerError = "internal server error"

// requestLayoutFunc wraps page content in the app shell for a specific
// request — it needs r because the shell's Owners/Stats and shellNav's
// active entry are all real, per-request data (household members, live bin/
// item/room counts, which page is current), never the request-agnostic
// closure identity/adapter's plain layoutFunc type is for pages with no such
// data. Mirrors identity/adapter's own requestLayoutFunc type exactly (see
// devicetokenweb.go's own doc for why that one exists too) — both are
// unexported to their package, so neither can be reused directly here.
// Injected by the composition root (cmd/server/shell.go), which alone knows
// ShellProps/shellNav.
type requestLayoutFunc func(r *http.Request, content templ.Component) templ.Component

// memberLister is the narrow port (ISP) BinsWebHandlers depends on to
// populate the bin form's owner picker and to resolve a bin's owner
// display, satisfied by identity's UserRepository (a superset, via List)
// and by test fakes. Named for the single method it exposes, per Go's
// single-method-interface naming convention (io.Reader, fmt.Stringer, ...)
// — mirrors app.memberLister's own naming rationale (owner.go).
type memberLister interface {
	List(ctx context.Context) ([]identity.User, error)
}

// locationLister is the narrow port (ISP) BinsWebHandlers depends on to
// resolve a bin's location name and to populate the bin create form's
// location picker, satisfied by *app.LocationService (a superset, via
// List) and by test fakes.
type locationLister interface {
	List(ctx context.Context, viewer identity.Principal) ([]app.LocationSummary, error)
}

// verifyRequest parses the form and verifies the CSRF token — the two
// checks every POST in this package's handlers run before doing anything
// else, factored out of identity/adapter's per-handler verifyRequest method
// (users_web.go) since BinsWebHandlers and LocationsWebHandlers both need
// the identical check against their own *scs.SessionManager. Answers 400 or
// 403 and reports ok=false on failure.
func verifyRequest(w http.ResponseWriter, r *http.Request, sm *scs.SessionManager) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	if !session.VerifyCSRF(r, sm) {
		http.Error(w, "invalid CSRF token", http.StatusForbidden)
		return false
	}
	return true
}

// redirectTo sends a successful mutation to a different page than the one
// the form was submitted from (a newly created bin/location's detail page,
// or the index after a delete) — an HX-Redirect for HTMX, so htmx performs
// a full client-side navigation instead of swapping a fragment, or a real
// 303 for a full browser navigation.
func redirectTo(w http.ResponseWriter, r *http.Request, target string) {
	if render.IsHTMX(r) {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// ownerOptions builds the bin form's owner <select> options: the shared/
// Family option (empty value, unowned) first, then one option per household
// member.
func ownerOptions(members []identity.User) []components.OwnerOptionView {
	opts := make([]components.OwnerOptionView, 0, len(members)+1)
	opts = append(opts, components.OwnerOptionView{ID: "", Name: "Family (shared)"})
	for _, m := range members {
		opts = append(opts, components.OwnerOptionView{ID: m.ID.String(), Name: m.DisplayName})
	}
	return opts
}

// locationOptions builds a location <select>'s options from
// LocationService.List's result, in the same (by-name) order it returns.
func locationOptions(summaries []app.LocationSummary) []components.LocationOptionView {
	opts := make([]components.LocationOptionView, 0, len(summaries))
	for _, s := range summaries {
		opts = append(opts, components.LocationOptionView{ID: s.Location.ID.String(), Name: s.Location.Name})
	}
	return opts
}

// locationNameIndex maps a location id to its name, built once per request
// so BinsWebHandlers can resolve every bin card's location line without a
// query per bin.
func locationNameIndex(summaries []app.LocationSummary) map[string]string {
	names := make(map[string]string, len(summaries))
	for _, s := range summaries {
		names[s.Location.ID.String()] = s.Location.Name
	}
	return names
}

// ownerView projects an *app.OwnerInfo (nil for the shared/Family bin) into
// the OwnerAvatar view model.
func ownerView(owner *app.OwnerInfo) components.OwnerView {
	if owner == nil {
		return components.OwnerView{Name: "Family", Initials: "F", Color: components.OwnerShared}
	}
	return components.OwnerView{Name: owner.Name, Initials: owner.Initials, Color: components.ParseOwnerColor(owner.Color.String())}
}

// ownerIDValue returns the bin form's owner <select> value for owner: empty
// for the shared/Family bin, matching ownerOptions' own "empty means
// unowned" contract.
func ownerIDValue(owner *app.OwnerInfo) string {
	if owner == nil {
		return ""
	}
	return owner.UserID.String()
}

// formatStoredDate renders a bin's CreatedAt as the reference's "Oct 2025"
// short form — the one place a time.Time is formatted for display, so the
// templ layer never receives one directly.
func formatStoredDate(t time.Time) string {
	return t.Format("Jan 2006")
}

// buildBinCard projects one app.BinView into BinCard's view model —
// BinsWebHandlers' grid (locationName resolved per bin from a
// once-per-request index) and LocationsWebHandlers' own bin list (every
// card sharing the one location it is already scoped to) both build a
// []components.BinCardView by mapping this over their own view slice, so
// the projection itself is defined exactly once. The private marker is set
// only when viewer is the bin's own creator: BinService already scopes
// which bins reach here at all (a non-owner never sees another member's
// private bin), but an admin CAN see one without being its owner, and
// "the requester already owns it" is the narrower fact the badge promises.
func buildBinCard(v app.BinView, locationName string, viewer identity.Principal, tintIndex int) components.BinCardView {
	return components.BinCardView{
		Code:         v.Bin.Code,
		Name:         v.Bin.Name,
		LocationName: locationName,
		ItemCount:    v.ItemCount,
		Owner:        ownerView(v.Owner),
		Private:      v.Bin.Visibility.IsPrivate() && v.Bin.CreatedBy == viewer.UserID,
		StoredDate:   formatStoredDate(v.Bin.CreatedAt),
		TintIndex:    tintIndex,
	}
}
