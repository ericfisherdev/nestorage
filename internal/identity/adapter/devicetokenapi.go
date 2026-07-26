package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/api"
)

// maxDeviceTokenRequestBytes bounds the exchange endpoint's request body — a
// three-field JSON object never needs more than this, and the bound stops an
// unauthenticated caller (this route is reachable with no session) from
// forcing an unbounded json.Decode.
const maxDeviceTokenRequestBytes = 1 << 20 // 1 MiB

// deviceTokenIssuer is the narrow port (ISP) DeviceTokenAPIHandlers depends
// on, satisfied by *app.DeviceTokenService.
type deviceTokenIssuer interface {
	Issue(ctx context.Context, email, password, deviceName string) (plaintext string, token *domain.DeviceToken, err error)
}

// DeviceTokenAPIHandlers serves the credential-minting exchange endpoint
// Android trades a member's email/password for a device token through. It
// deliberately carries no session and no CSRF token: it has no cookie to
// protect, and it IS the route that mints one of the two credentials
// NSTR-24's principal middleware accepts — exempted from that middleware for
// exactly that reason.
type DeviceTokenAPIHandlers struct {
	issuer deviceTokenIssuer
	logger *slog.Logger
}

// NewDeviceTokenAPIHandlers constructs DeviceTokenAPIHandlers. All
// dependencies are required; a missing one panics at construction time,
// matching every other WebHandlers constructor in this codebase.
func NewDeviceTokenAPIHandlers(issuer deviceTokenIssuer, logger *slog.Logger) *DeviceTokenAPIHandlers {
	if issuer == nil {
		panic("identity/adapter: NewDeviceTokenAPIHandlers requires a non-nil deviceTokenIssuer")
	}
	if logger == nil {
		panic("identity/adapter: NewDeviceTokenAPIHandlers requires a non-nil logger")
	}
	return &DeviceTokenAPIHandlers{issuer: issuer, logger: logger}
}

// Routes registers the exchange route on mux — an api.Registrar (DIP),
// matching every /api/v1 handler group's own Routes method (see
// storage/adapter.LocationsAPIHandlers.Routes' own doc for why): NSTR-57's
// OpenAPI route/spec sync test substitutes a recording registrar in place
// of the concrete *http.ServeMux cmd/server/shell.go still passes here in
// production (which satisfies api.Registrar unchanged).
func (h *DeviceTokenAPIHandlers) Routes(mux api.Registrar) {
	mux.HandleFunc("POST /api/v1/auth/device-tokens", h.Issue)
}

// deviceTokenExchangeRequest is the exchange endpoint's request body.
type deviceTokenExchangeRequest struct {
	Email      string `json:"email"`
	Password   string `json:"password"`
	DeviceName string `json:"device_name"`
}

// deviceTokenExchangeResponse is the exchange endpoint's 201 body. Token
// carries the plaintext device token — the one and only time it appears in
// any response. It is never added to any later (e.g. list) endpoint.
type deviceTokenExchangeResponse struct {
	Token     string    `json:"token"`
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// Issue handles POST /api/v1/auth/device-tokens: decode, delegate to
// DeviceTokenService.Issue, and map its result to a JSON response.
func (h *DeviceTokenAPIHandlers) Issue(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxDeviceTokenRequestBytes)

	var req deviceTokenExchangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		api.WriteError(w, h.logger, http.StatusBadRequest, api.CodeInvalidRequest, "malformed request body")
		return
	}

	plaintext, token, err := h.issuer.Issue(r.Context(), req.Email, req.Password, req.DeviceName)
	if err != nil {
		h.handleIssueError(w, r, err)
		return
	}

	api.WriteJSON(w, h.logger, http.StatusCreated, deviceTokenExchangeResponse{
		Token:     plaintext,
		ID:        token.ID.String(),
		Name:      token.Name,
		CreatedAt: token.CreatedAt,
	})
}

// handleIssueError maps a DeviceTokenService.Issue error to the shared
// envelope (platform/api): 400 invalid_request for a rejected device name,
// with a field detail naming device_name; 401 invalid_credentials carrying
// the SAME generic message every credential failure gets (unknown email,
// wrong password, locked-out email, and an inactive user alike) so this
// endpoint cannot be used to enumerate accounts; or a logged 500 internal
// for anything else.
func (h *DeviceTokenAPIHandlers) handleIssueError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidDeviceToken):
		api.WriteError(w, h.logger, http.StatusBadRequest, api.CodeInvalidRequest, "invalid request",
			api.FieldDetail{Field: "device_name", Message: "must not be blank and at most 100 characters"})
	case errors.Is(err, domain.ErrInvalidCredentials):
		api.WriteError(w, h.logger, http.StatusUnauthorized, api.CodeInvalidCredentials, invalidCredentialsMessage)
	default:
		h.logger.ErrorContext(r.Context(), "device token api: issue", "error", err)
		api.WriteError(w, h.logger, http.StatusInternalServerError, api.CodeInternal, errInternalServerError)
	}
}
