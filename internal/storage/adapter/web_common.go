package adapter

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestcore/httpserver/middleware"
	"github.com/ericfisherdev/nestcore/render"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
	"github.com/ericfisherdev/nestorage/internal/storage/app"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
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

// primaryPhotoRefLister is the narrow port (ISP) the item list views depend
// on to render thumbnails, satisfied by *mediaapp.PhotoService (NSTR-37,
// Sprint 5 reconciliation R8). Declared here — the house convention for a
// consumer-declared narrow port is an unexported interface in the consuming
// package, mirroring memberLister/locationLister's own identical shape —
// rather than in either ItemsWebHandlers' or BinsWebHandlers' own file,
// since both handler groups share it (search results and a bin's contents
// list alike). internal/storage never imports internal/media to reference
// this port's concrete implementation: the composition root
// (cmd/server/main.go) injects *mediaapp.PhotoService by value, satisfying
// this interface structurally.
type primaryPhotoRefLister interface {
	ListPrimaryThumbRefs(ctx context.Context, viewer identity.Principal, itemIDs []domain.ItemID) (map[domain.ItemID]domain.PhotoRef, error)
}

// loadPrimaryPhotoRefs calls photos.ListPrimaryThumbRefs for itemIDs (the
// ONE call per list render Sprint 5 reconciliation R8 requires — never one
// per row), logging and returning a nil map on failure rather than failing
// the whole list render: a thumbnail is a decoration, and a nil map's
// lookups all miss, which is exactly the "no photo" case thumbURL already
// handles. Skips the call entirely for an empty itemIDs (an empty search
// result, a photoless-by-definition empty bin).
func loadPrimaryPhotoRefs(ctx context.Context, photos primaryPhotoRefLister, viewer identity.Principal, itemIDs []domain.ItemID, logger *slog.Logger, op string) map[domain.ItemID]domain.PhotoRef {
	if len(itemIDs) == 0 {
		return nil
	}
	refs, err := photos.ListPrimaryThumbRefs(ctx, viewer, itemIDs)
	if err != nil {
		logger.WarnContext(ctx, op, "error", err)
		return nil
	}
	return refs
}

// thumbURL returns itemID's list-view thumbnail URL from refs (built by
// loadPrimaryPhotoRefs), or "" when itemID has no entry — every list-view
// builder in this package (buildSearchResultViews, buildItemRows) renders
// the neutral placeholder block instead of an <img> when this is empty. The
// route always requests size=thumb: list views and the gallery grid never
// request the full image (this ticket's own AC) — PhotosWebHandlers.Serve
// (internal/media/adapter) resolves the actual bytes, falling back to the
// full image internally when a photo's thumbnail variant does not exist.
func thumbURL(itemID domain.ItemID, refs map[domain.ItemID]domain.PhotoRef) string {
	ref, ok := refs[itemID]
	if !ok || ref.PhotoID == "" {
		return ""
	}
	return "/items/" + itemID.String() + "/photos/" + ref.PhotoID + "?size=thumb"
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

// resolveBaseURL resolves the origin app.BinDeepLinkURL builds a printed
// QR's deep link against: publicBaseURL (PUBLIC_BASE_URL, see
// corecfg.ServerConfig.PublicBaseURL's own doc) always wins when configured,
// since a printed label must stay scannable by a phone that is not on the
// server's own LAN segment — the request's own Host is never the right
// answer for a code that leaves the building. Otherwise the origin is
// derived per request: nestcore's middleware.ForwardedHeaders' resolved
// scheme, falling back to the on-wire TLS state when that middleware did
// not run, plus r.Host.
//
// Port of Nestova's kiosk resolveBaseURL (internal/kiosk/adapter/
// kiosk_web.go in the nestova repo) — see that function's own doc for the
// identical rationale this one mirrors. Placed here, rather than as a
// method on BinsWebHandlers, because NSTR-50/51's label handlers reuse it
// unchanged.
func resolveBaseURL(r *http.Request, publicBaseURL string) string {
	if publicBaseURL != "" {
		return publicBaseURL
	}
	scheme := middleware.RequestScheme(r.Context())
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	return scheme + "://" + r.Host
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
// historyActorView projects an item_event row's own attribution snapshot
// (ActorLabel — never a live identity lookup) into OwnerAvatar's view
// model, shared by ItemsWebHandlers.History and BinsWebHandlers.Activity
// (NSTR-42). Every history avatar renders in the neutral shared tint:
// item_event stores no color for its actor, on purpose — resolving one live
// from identity would re-couple the append-only log to current household
// membership, the exact coupling ActorLabel's own name snapshot exists to
// avoid (a departed member's history must still render correctly).
func historyActorView(e domain.ItemEvent) components.OwnerView {
	return components.OwnerView{Name: e.ActorLabel, Initials: holderInitials(e.ActorLabel), Color: components.OwnerShared}
}

// formatHistoryTime renders an item_event row's own OccurredAt for display —
// the one place a history row's time.Time is formatted, so the templ layer
// never receives one directly (formatStoredDate's own contract). Always
// t.Local(), never UTC, so a non-UTC viewer sees a time that agrees with
// their own wall clock.
func formatHistoryTime(t time.Time) string {
	return t.Local().Format("3:04 PM")
}

// historyEventSummary builds the timeline/activity-panel action sentence for
// one item_event row — the single place every EventKind's own phrasing is
// defined, shared by ItemsWebHandlers.History and BinsWebHandlers.Activity
// so the two surfaces never drift on wording for the same event. BinLabel
// is already the formatted "code — name" snapshot (app/events.go's own
// binLabel helper), not a bare code, so it renders as plain sentence text
// here rather than through the mono BinCode component — that component is
// reserved for a genuine bare bin-code field, which no ItemEvent carries a
// separate one of. moved's own phrasing is Sprint 6 reconciliation R4's
// binding example ("moved to PANTRY, previously HALL CLOSET"): the
// containing bin does not change, only the location snapshot fields do.
// return_requested/return_request_cancelled/edited carry no bin context at
// all (NULL bin_id/bin_label), so none of their sentences reference one.
func historyEventSummary(e domain.ItemEvent) string {
	switch e.Kind {
	case domain.EventCreated:
		return "Created"
	case domain.EventAdded:
		return "Added to " + e.BinLabel
	case domain.EventRemoved:
		return "Checked out from " + e.BinLabel
	case domain.EventReturned:
		return "Returned to " + e.BinLabel
	case domain.EventMoved:
		return "Moved to " + e.ToLocationLabel + ", previously " + e.FromLocationLabel
	case domain.EventDeleted:
		return "Deleted"
	case domain.EventReturnRequested:
		return "Requested return"
	case domain.EventReturnRequestCancelled:
		return "Return request cancelled"
	case domain.EventEdited:
		return "Edited " + joinChangedFieldLabels(e.ChangedFields)
	default:
		return "Unknown activity"
	}
}

// joinChangedFieldLabels renders an edited event's ChangedFields as "the
// name", "the name and quantity", or "the name, description, and quantity"
// — always in the canonical name/description/quantity order (Sprint 6's own
// binding instruction), regardless of the array's stored order, and never
// each field's prior value, since item_event does not store one (see
// domain.EventEdited's own doc).
func joinChangedFieldLabels(fields []domain.EditedField) string {
	changed := make(map[domain.EditedField]bool, len(fields))
	for _, f := range fields {
		changed[f] = true
	}
	var labels []string
	for _, f := range []domain.EditedField{domain.FieldName, domain.FieldDescription, domain.FieldQuantity} {
		if changed[f] {
			labels = append(labels, f.String())
		}
	}
	switch len(labels) {
	case 0:
		return "the item"
	case 1:
		return "the " + labels[0]
	case 2:
		return "the " + labels[0] + " and " + labels[1]
	default:
		return "the " + strings.Join(labels[:len(labels)-1], ", ") + ", and " + labels[len(labels)-1]
	}
}

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
