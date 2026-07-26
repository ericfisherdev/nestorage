package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/ericfisherdev/nestorage/internal/identity/app"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/api"
)

// maxFederationRequestBytes bounds the provision endpoint's request body —
// mirrors maxDeviceTokenRequestBytes's identical rationale (devicetokenapi.go).
const maxFederationRequestBytes = 1 << 20 // 1 MiB

// invalidFederationIDMessage is the shared validation message for household,
// member_id, and household_id — all three are backed by
// domain.ValidateHouseholdID/ValidateMemberID's common validateFederationID
// rule (identity/domain/federation.go), which enforces the same
// non-blank/200-rune limit regardless of which field is being checked.
const invalidFederationIDMessage = "must not be blank and at most 200 characters"

// errInvalidRequestMessage is the generic 422 message every field-level
// validation failure in this file responds with — mirrors
// errInternalServerError's own doc convention (onboarding_web.go), but
// scoped to this file since its duplication here is confined to
// federation_api.go for this PR.
const errInvalidRequestMessage = "invalid request"

// federationService is the narrow port (ISP) FederationAPIHandlers depends
// on, satisfied by *app.FederationService.
type federationService interface {
	Accounts(ctx context.Context, householdID string) ([]app.FederationAccount, error)
	Link(ctx context.Context, memberID, householdID string, userID domain.UserID) (*domain.User, bool, error)
	Upsert(ctx context.Context, memberID, householdID string, profile domain.FederationProfile) (*domain.User, bool, error)
}

// FederationAPIHandlers serves NSTR-101's federation surface: the
// account-read reconciliation feeds on, and the link/provision endpoint
// NSTR-106/107/108's attach and re-push calls drive. Both routes require
// the account api key (requireIntegrationPrincipal) — the INVERSE of every
// create endpoint elsewhere in this codebase, which instead rejects that
// credential (storage/adapter's requireUserPrincipal): federation is
// Nestova's own integration talking to Nestorage, never a member acting
// through their own device or session. This widens the integration
// credential's reach to member personal data — a deliberate scope increase,
// which is why the household binding (FederationProvisioner) is verified on
// every call, not only at attach.
type FederationAPIHandlers struct {
	federation federationService
	logger     *slog.Logger
}

// NewFederationAPIHandlers constructs FederationAPIHandlers. Both
// dependencies are required; a missing one panics at construction time,
// matching every other WebHandlers/APIHandlers constructor in this
// codebase.
func NewFederationAPIHandlers(federation federationService, logger *slog.Logger) *FederationAPIHandlers {
	if federation == nil {
		panic("identity/adapter: NewFederationAPIHandlers requires a non-nil federationService")
	}
	if logger == nil {
		panic("identity/adapter: NewFederationAPIHandlers requires a non-nil logger")
	}
	return &FederationAPIHandlers{federation: federation, logger: logger}
}

// Routes registers the federation routes on mux — an api.Registrar (DIP),
// matching every /api/v1 handler group's own Routes method (see
// LocationsAPIHandlers.Routes' own doc, storage/adapter/locations_api.go).
func (h *FederationAPIHandlers) Routes(mux api.Registrar) {
	mux.HandleFunc("GET /api/v1/federation/accounts", h.Accounts)
	mux.HandleFunc("PUT /api/v1/federation/members/{member_id}", h.Provision)
}

// requireIntegrationPrincipal reports whether viewer may call a federation
// endpoint: true only for NSTR-23's account api key (domain.KindIntegration)
// — the inverse of storage/adapter's requireUserPrincipal. A device token
// resolves to domain.KindUser and is refused with 403 (AC 7); a bare
// session cookie never gets past RequireAPICredential's own 401, so it
// never reaches here at all.
func requireIntegrationPrincipal(w http.ResponseWriter, logger *slog.Logger, viewer domain.Principal) bool {
	if viewer.Kind == domain.KindIntegration {
		return true
	}
	api.WriteError(w, logger, http.StatusForbidden, api.CodeForbidden, "only the account api key may call this endpoint")
	return false
}

// federationAccountResponse is every federation endpoint's per-account
// shape: exactly the fields reconciliation needs and no more — id, display
// name, email, active, and the linked member id (null when unlinked).
// There is no hash field, by construction (AC 2).
type federationAccountResponse struct {
	ID          string  `json:"id"`
	DisplayName string  `json:"display_name"`
	Email       string  `json:"email"`
	Active      bool    `json:"active"`
	MemberID    *string `json:"member_id"`
}

// federationAccountsResponse is GET /api/v1/federation/accounts' response
// envelope — deliberately unpaginated: household scale bounds it, the same
// rationale LocationService documents for its own in-memory counts.
type federationAccountsResponse struct {
	Accounts []federationAccountResponse `json:"accounts"`
}

// federationLinkRequest is federationProvisionRequest's "link" variant:
// pair the path's member_id with an existing user id.
type federationLinkRequest struct {
	UserID string `json:"user_id"`
}

// federationAccountRequest is federationProvisionRequest's "account"
// variant: the profile fields to create or update a federated user from.
type federationAccountRequest struct {
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Role        string `json:"role"`
	Active      bool   `json:"active"`
}

// federationProvisionRequest is PUT /api/v1/federation/members/{member_id}'s
// request body: the household id plus EXACTLY ONE of Link or Account — both
// or neither answers 422 (see Provision's own doc).
type federationProvisionRequest struct {
	HouseholdID string                    `json:"household_id"`
	Link        *federationLinkRequest    `json:"link"`
	Account     *federationAccountRequest `json:"account"`
}

// Accounts handles GET /api/v1/federation/accounts?household={id}: every
// user paired with its linked member id, for Nestova's own reconciliation
// to propose matches from. Verifies the binding first — this is the first
// authenticated call NSTR-106's own attach flow makes, which is what
// records it.
func (h *FederationAPIHandlers) Accounts(w http.ResponseWriter, r *http.Request) {
	viewer, _ := CurrentPrincipal(r.Context())
	if !requireIntegrationPrincipal(w, h.logger, viewer) {
		return
	}

	householdID := r.URL.Query().Get("household")
	if err := domain.ValidateHouseholdID(householdID); err != nil {
		writeInvalidField(w, h.logger, "household", invalidFederationIDMessage)
		return
	}

	accounts, err := h.federation.Accounts(r.Context(), householdID)
	if err != nil {
		h.writeFederationError(w, r, err, "federation api: accounts")
		return
	}
	api.WriteJSON(w, h.logger, http.StatusOK, federationAccountsResponse{Accounts: federationAccountResponses(accounts)})
}

// Provision handles PUT /api/v1/federation/members/{member_id}: the single
// write path both an initial link/create and every later re-push
// (NSTR-108) reuse — the PUT verb itself is what carries the idempotency
// contract those re-pushes rely on. 201 when a user or link was newly
// created, 200 on an idempotent replay or a profile update.
func (h *FederationAPIHandlers) Provision(w http.ResponseWriter, r *http.Request) {
	viewer, _ := CurrentPrincipal(r.Context())
	if !requireIntegrationPrincipal(w, h.logger, viewer) {
		return
	}

	memberID := r.PathValue("member_id")
	if err := domain.ValidateMemberID(memberID); err != nil {
		writeInvalidField(w, h.logger, "member_id", invalidFederationIDMessage)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxFederationRequestBytes)
	var req federationProvisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.WriteError(w, h.logger, http.StatusBadRequest, api.CodeInvalidRequest, "malformed request body")
		return
	}
	if err := domain.ValidateHouseholdID(req.HouseholdID); err != nil {
		writeInvalidField(w, h.logger, "household_id", invalidFederationIDMessage)
		return
	}
	if (req.Link == nil) == (req.Account == nil) {
		writeInvalidField(w, h.logger, "link", "exactly one of link or account must be set")
		return
	}

	if req.Link != nil {
		h.provisionLink(w, r, memberID, req.HouseholdID, req.Link)
		return
	}
	h.provisionAccount(w, r, memberID, req.HouseholdID, req.Account)
}

// provisionLink handles Provision's "link" body variant: pair memberID with
// an already-existing user.
func (h *FederationAPIHandlers) provisionLink(w http.ResponseWriter, r *http.Request, memberID, householdID string, link *federationLinkRequest) {
	userID, err := domain.ParseUserID(link.UserID)
	if err != nil {
		writeInvalidField(w, h.logger, "link.user_id", "malformed id")
		return
	}
	u, created, err := h.federation.Link(r.Context(), memberID, householdID, userID)
	if err != nil {
		h.writeFederationError(w, r, err, "federation api: link")
		return
	}
	h.writeProvisionResult(w, u, memberID, created)
}

// provisionAccount handles Provision's "account" body variant: create or
// update a federated user from the supplied profile.
func (h *FederationAPIHandlers) provisionAccount(w http.ResponseWriter, r *http.Request, memberID, householdID string, account *federationAccountRequest) {
	role, err := domain.ParseRole(account.Role)
	if err != nil {
		writeInvalidField(w, h.logger, "account.role", `must be "admin" or "member"`)
		return
	}
	profile := domain.FederationProfile{
		DisplayName: account.DisplayName,
		Email:       account.Email,
		Role:        role,
		Active:      account.Active,
	}
	u, created, err := h.federation.Upsert(r.Context(), memberID, householdID, profile)
	if err != nil {
		h.writeFederationError(w, r, err, "federation api: provision")
		return
	}
	h.writeProvisionResult(w, u, memberID, created)
}

// writeProvisionResult writes Provision's own response: 201 when created is
// true, 200 otherwise — the same federationAccountResponse shape Accounts'
// own rows share, with member_id always the request's own memberID (the
// call just created or confirmed exactly that link).
func (h *FederationAPIHandlers) writeProvisionResult(w http.ResponseWriter, u *domain.User, memberID string, created bool) {
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	api.WriteJSON(w, h.logger, status, federationAccountResponse{
		ID:          u.ID.String(),
		DisplayName: u.DisplayName,
		Email:       u.Email,
		Active:      u.Active,
		MemberID:    &memberID,
	})
}

// federationAccountResponses projects Accounts' own read model into the
// response's per-row shape.
func federationAccountResponses(accounts []app.FederationAccount) []federationAccountResponse {
	out := make([]federationAccountResponse, 0, len(accounts))
	for _, a := range accounts {
		out = append(out, federationAccountResponse{
			ID:          a.User.ID.String(),
			DisplayName: a.User.DisplayName,
			Email:       a.User.Email,
			Active:      a.User.Active,
			MemberID:    a.MemberID,
		})
	}
	return out
}

// writeInvalidField answers 422 invalid_request naming field as malformed —
// this package's own copy of storage/adapter's identically-named helper
// (api_common.go); duplicated rather than shared since neither package
// imports the other's unexported helpers.
func writeInvalidField(w http.ResponseWriter, logger *slog.Logger, field, message string) {
	api.WriteError(w, logger, http.StatusUnprocessableEntity, api.CodeInvalidRequest, errInvalidRequestMessage,
		api.FieldDetail{Field: field, Message: message})
}

// writeFederationError maps err via mapFederationError and writes the
// shared envelope, or logs and answers a generic 500 when err is
// unrecognized — mirrors storage/adapter's writeDomainError/mapDomainError
// split (api_common.go).
func (h *FederationAPIHandlers) writeFederationError(w http.ResponseWriter, r *http.Request, err error, op string) {
	status, code, message, detail, ok := mapFederationError(err)
	if !ok {
		h.logger.ErrorContext(r.Context(), op, "error", err)
		api.WriteError(w, h.logger, http.StatusInternalServerError, api.CodeInternal, errInternalServerError)
		return
	}
	if detail == nil {
		api.WriteError(w, h.logger, status, code, message)
		return
	}
	api.WriteError(w, h.logger, status, code, message, *detail)
}

// mapFederationError maps a domain sentinel from a FederationService call to
// the shared envelope's status, code, message, and — for a 422 — the
// FieldDetail naming which field failed.
func mapFederationError(err error) (status int, code api.Code, message string, detail *api.FieldDetail, ok bool) {
	switch {
	case errors.Is(err, domain.ErrHouseholdMismatch):
		return http.StatusForbidden, api.CodeHouseholdMismatch, "request household does not match the bound household", nil, true
	case errors.Is(err, domain.ErrMemberAlreadyLinked):
		return http.StatusConflict, api.CodeConflict, "member is already linked to a different user", nil, true
	case errors.Is(err, domain.ErrUserAlreadyLinked):
		return http.StatusConflict, api.CodeConflict, "user is already linked to a different member", nil, true
	case errors.Is(err, domain.ErrDuplicateEmail):
		return http.StatusConflict, api.CodeConflict, "email is already in use", nil, true
	case errors.Is(err, domain.ErrLastActiveAdmin):
		return http.StatusConflict, api.CodeConflict, "cannot remove the last active admin", nil, true
	case errors.Is(err, domain.ErrUserNotFound):
		return http.StatusNotFound, api.CodeNotFound, "user not found", nil, true
	case errors.Is(err, domain.ErrInvalidRole):
		return http.StatusUnprocessableEntity, api.CodeInvalidRequest, errInvalidRequestMessage,
			&api.FieldDetail{Field: "account.role", Message: `must be "admin" or "member"`}, true
	case errors.Is(err, domain.ErrInvalidMemberLink):
		// Reached only if a request gets past this handler's own
		// pre-validation (ValidateMemberID/ValidateHouseholdID) with an
		// otherwise-malformed identifier — defensive, with no single field
		// to name at this remove (mirrors storage/adapter's own
		// ErrInvalidBin fallback, api_common.go).
		return http.StatusUnprocessableEntity, api.CodeInvalidRequest, errInvalidRequestMessage, nil, true
	default:
		return 0, "", "", nil, false
	}
}
