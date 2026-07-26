package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/identity/app"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// This file is a white-box (package adapter) test so it can inject a
// domain.Principal directly via withPrincipal — CurrentPrincipal's context
// key is unexported, and routing every case through the full Resolve/Chain
// middleware (the only option available to the black-box adapter_test
// package, see storage/adapter's own fixedPrincipalResolver) would be far
// more setup than these handler-level cases need.

func federationAPITestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeFederationService is a configurable federationService double.
type fakeFederationService struct {
	accounts    []app.FederationAccount
	accountsErr error

	linkUser    *domain.User
	linkCreated bool
	linkErr     error

	upsertUser    *domain.User
	upsertCreated bool
	upsertErr     error
}

func (f *fakeFederationService) Accounts(context.Context, string) ([]app.FederationAccount, error) {
	return f.accounts, f.accountsErr
}

func (f *fakeFederationService) Link(context.Context, string, string, domain.UserID) (*domain.User, bool, error) {
	return f.linkUser, f.linkCreated, f.linkErr
}

func (f *fakeFederationService) Upsert(context.Context, string, string, domain.FederationProfile) (*domain.User, bool, error) {
	return f.upsertUser, f.upsertCreated, f.upsertErr
}

// withTestPrincipal returns a copy of r carrying p, retrievable by
// CurrentPrincipal — the same withPrincipal this package's own Resolve
// middleware uses in production, called directly here since this file
// lives in package adapter.
func withTestPrincipal(r *http.Request, p domain.Principal) *http.Request {
	return r.WithContext(withPrincipal(r.Context(), p))
}

func TestFederationAPIHandlers_Accounts_RejectsUserPrincipal(t *testing.T) {
	h := NewFederationAPIHandlers(&fakeFederationService{}, federationAPITestLogger())
	r := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/api/v1/federation/accounts?household=h1", nil),
		domain.NewUserPrincipal(domain.NewUserID(), domain.RoleMember, "Maya"))
	w := httptest.NewRecorder()

	h.Accounts(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("Accounts(user principal) status = %d, want 403", w.Code)
	}
}

func TestFederationAPIHandlers_Accounts_MissingHousehold(t *testing.T) {
	h := NewFederationAPIHandlers(&fakeFederationService{}, federationAPITestLogger())
	r := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/api/v1/federation/accounts", nil),
		domain.NewIntegrationPrincipal("Nestova"))
	w := httptest.NewRecorder()

	h.Accounts(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("Accounts(no household) status = %d, want 422", w.Code)
	}
}

func TestFederationAPIHandlers_Accounts_HouseholdMismatch(t *testing.T) {
	h := NewFederationAPIHandlers(&fakeFederationService{accountsErr: domain.ErrHouseholdMismatch}, federationAPITestLogger())
	r := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/api/v1/federation/accounts?household=h1", nil),
		domain.NewIntegrationPrincipal("Nestova"))
	w := httptest.NewRecorder()

	h.Accounts(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("Accounts(household mismatch) status = %d, want 403", w.Code)
	}
	if !strings.Contains(w.Body.String(), "household_mismatch") {
		t.Errorf("Accounts(household mismatch) body = %q, want household_mismatch code", w.Body.String())
	}
}

// TestFederationAPIHandlers_Accounts_NeverLeaksPasswordField is the
// automated equivalent of AC 2: the raw response JSON must never contain a
// password/hash field, regardless of what the domain.User carries.
func TestFederationAPIHandlers_Accounts_NeverLeaksPasswordField(t *testing.T) {
	memberID := "member-1"
	svc := &fakeFederationService{accounts: []app.FederationAccount{
		{User: domain.User{ID: domain.NewUserID(), DisplayName: "Maya", Email: "maya@example.com", PasswordHash: "$argon2id$...secret...", Active: true}, MemberID: &memberID},
	}}
	h := NewFederationAPIHandlers(svc, federationAPITestLogger())
	r := withTestPrincipal(httptest.NewRequest(http.MethodGet, "/api/v1/federation/accounts?household=h1", nil),
		domain.NewIntegrationPrincipal("Nestova"))
	w := httptest.NewRecorder()

	h.Accounts(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("Accounts status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
	raw := w.Body.String()
	if strings.Contains(strings.ToLower(raw), "hash") || strings.Contains(strings.ToLower(raw), "password") || strings.Contains(raw, "secret") {
		t.Errorf("Accounts response leaked a password/hash field: %s", raw)
	}
	var got federationAccountsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(got.Accounts) != 1 || got.Accounts[0].MemberID == nil || *got.Accounts[0].MemberID != memberID {
		t.Errorf("Accounts response = %+v, want one row linked to %q", got, memberID)
	}
}

func TestFederationAPIHandlers_Provision_RejectsUserPrincipal(t *testing.T) {
	h := NewFederationAPIHandlers(&fakeFederationService{}, federationAPITestLogger())
	body := bytes.NewBufferString(`{"household_id":"h1","link":{"user_id":"` + domain.NewUserID().String() + `"}}`)
	r := withTestPrincipal(httptest.NewRequest(http.MethodPut, "/api/v1/federation/members/m1", body),
		domain.NewUserPrincipal(domain.NewUserID(), domain.RoleAdmin, "Maya"))
	r.SetPathValue("member_id", "m1")
	w := httptest.NewRecorder()

	h.Provision(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("Provision(user principal) status = %d, want 403", w.Code)
	}
}

func TestFederationAPIHandlers_Provision_BothLinkAndAccountRejected(t *testing.T) {
	h := NewFederationAPIHandlers(&fakeFederationService{}, federationAPITestLogger())
	body := `{"household_id":"h1","link":{"user_id":"` + domain.NewUserID().String() + `"},"account":{"display_name":"Maya","email":"maya@example.com","role":"member","active":true}}`
	r := withTestPrincipal(httptest.NewRequest(http.MethodPut, "/api/v1/federation/members/m1", bytes.NewBufferString(body)),
		domain.NewIntegrationPrincipal("Nestova"))
	r.SetPathValue("member_id", "m1")
	w := httptest.NewRecorder()

	h.Provision(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("Provision(both link and account) status = %d, want 422", w.Code)
	}
}

func TestFederationAPIHandlers_Provision_NeitherLinkNorAccountRejected(t *testing.T) {
	h := NewFederationAPIHandlers(&fakeFederationService{}, federationAPITestLogger())
	r := withTestPrincipal(httptest.NewRequest(http.MethodPut, "/api/v1/federation/members/m1", bytes.NewBufferString(`{"household_id":"h1"}`)),
		domain.NewIntegrationPrincipal("Nestova"))
	r.SetPathValue("member_id", "m1")
	w := httptest.NewRecorder()

	h.Provision(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("Provision(neither link nor account) status = %d, want 422", w.Code)
	}
}

func TestFederationAPIHandlers_Provision_LinkCreated_Answers201(t *testing.T) {
	u := &domain.User{ID: domain.NewUserID(), DisplayName: "Maya", Email: "maya@example.com", Active: true}
	svc := &fakeFederationService{linkUser: u, linkCreated: true}
	h := NewFederationAPIHandlers(svc, federationAPITestLogger())
	body := `{"household_id":"h1","link":{"user_id":"` + u.ID.String() + `"}}`
	r := withTestPrincipal(httptest.NewRequest(http.MethodPut, "/api/v1/federation/members/m1", bytes.NewBufferString(body)),
		domain.NewIntegrationPrincipal("Nestova"))
	r.SetPathValue("member_id", "m1")
	w := httptest.NewRecorder()

	h.Provision(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("Provision(link, created) status = %d, want 201 (body %q)", w.Code, w.Body.String())
	}
}

func TestFederationAPIHandlers_Provision_LinkReplay_Answers200(t *testing.T) {
	u := &domain.User{ID: domain.NewUserID(), DisplayName: "Maya", Email: "maya@example.com", Active: true}
	svc := &fakeFederationService{linkUser: u, linkCreated: false}
	h := NewFederationAPIHandlers(svc, federationAPITestLogger())
	body := `{"household_id":"h1","link":{"user_id":"` + u.ID.String() + `"}}`
	r := withTestPrincipal(httptest.NewRequest(http.MethodPut, "/api/v1/federation/members/m1", bytes.NewBufferString(body)),
		domain.NewIntegrationPrincipal("Nestova"))
	r.SetPathValue("member_id", "m1")
	w := httptest.NewRecorder()

	h.Provision(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("Provision(link, replay) status = %d, want 200 (body %q)", w.Code, w.Body.String())
	}
}

func TestFederationAPIHandlers_Provision_LinkMalformedUserID(t *testing.T) {
	h := NewFederationAPIHandlers(&fakeFederationService{}, federationAPITestLogger())
	body := `{"household_id":"h1","link":{"user_id":"not-a-uuid"}}`
	r := withTestPrincipal(httptest.NewRequest(http.MethodPut, "/api/v1/federation/members/m1", bytes.NewBufferString(body)),
		domain.NewIntegrationPrincipal("Nestova"))
	r.SetPathValue("member_id", "m1")
	w := httptest.NewRecorder()

	h.Provision(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("Provision(malformed link.user_id) status = %d, want 422", w.Code)
	}
}

func TestFederationAPIHandlers_Provision_AccountCreated_Answers201(t *testing.T) {
	u := &domain.User{ID: domain.NewUserID(), DisplayName: "Maya", Email: "maya@example.com", Active: true}
	svc := &fakeFederationService{upsertUser: u, upsertCreated: true}
	h := NewFederationAPIHandlers(svc, federationAPITestLogger())
	body := `{"household_id":"h1","account":{"display_name":"Maya","email":"maya@example.com","role":"member","active":true}}`
	r := withTestPrincipal(httptest.NewRequest(http.MethodPut, "/api/v1/federation/members/m1", bytes.NewBufferString(body)),
		domain.NewIntegrationPrincipal("Nestova"))
	r.SetPathValue("member_id", "m1")
	w := httptest.NewRecorder()

	h.Provision(w, r)

	if w.Code != http.StatusCreated {
		t.Fatalf("Provision(account, created) status = %d, want 201 (body %q)", w.Code, w.Body.String())
	}
}

func TestFederationAPIHandlers_Provision_AccountInvalidRole(t *testing.T) {
	h := NewFederationAPIHandlers(&fakeFederationService{}, federationAPITestLogger())
	body := `{"household_id":"h1","account":{"display_name":"Maya","email":"maya@example.com","role":"superuser","active":true}}`
	r := withTestPrincipal(httptest.NewRequest(http.MethodPut, "/api/v1/federation/members/m1", bytes.NewBufferString(body)),
		domain.NewIntegrationPrincipal("Nestova"))
	r.SetPathValue("member_id", "m1")
	w := httptest.NewRecorder()

	h.Provision(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("Provision(invalid role) status = %d, want 422", w.Code)
	}
}

func TestFederationAPIHandlers_Provision_ConflictMapsTo409(t *testing.T) {
	svc := &fakeFederationService{linkErr: domain.ErrMemberAlreadyLinked}
	h := NewFederationAPIHandlers(svc, federationAPITestLogger())
	body := `{"household_id":"h1","link":{"user_id":"` + domain.NewUserID().String() + `"}}`
	r := withTestPrincipal(httptest.NewRequest(http.MethodPut, "/api/v1/federation/members/m1", bytes.NewBufferString(body)),
		domain.NewIntegrationPrincipal("Nestova"))
	r.SetPathValue("member_id", "m1")
	w := httptest.NewRecorder()

	h.Provision(w, r)

	if w.Code != http.StatusConflict {
		t.Fatalf("Provision(already linked) status = %d, want 409", w.Code)
	}
}

// TestFederationAPIHandlers_Provision_MemberIDInvalid covers the member_id
// pre-validation branch (ValidateMemberID) — reached before the request
// body is even read, so a nil body is fine here.
func TestFederationAPIHandlers_Provision_MemberIDInvalid(t *testing.T) {
	h := NewFederationAPIHandlers(&fakeFederationService{}, federationAPITestLogger())
	r := withTestPrincipal(httptest.NewRequest(http.MethodPut, "/api/v1/federation/members/", nil),
		domain.NewIntegrationPrincipal("Nestova"))
	r.SetPathValue("member_id", "")
	w := httptest.NewRecorder()

	h.Provision(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("Provision(blank member_id) status = %d, want 422 (body %q)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `"member_id"`) {
		t.Errorf("Provision(blank member_id) body = %q, want field %q", body, "member_id")
	}
	if !strings.Contains(body, invalidFederationIDMessage) {
		t.Errorf("Provision(blank member_id) body = %q, want message %q", body, invalidFederationIDMessage)
	}
}

// TestFederationAPIHandlers_Provision_HouseholdIDInvalid covers
// ValidateHouseholdID's check on the decoded body's household_id — reached
// only once member_id has already passed and the body has decoded.
func TestFederationAPIHandlers_Provision_HouseholdIDInvalid(t *testing.T) {
	h := NewFederationAPIHandlers(&fakeFederationService{}, federationAPITestLogger())
	body := `{"household_id":""}`
	r := withTestPrincipal(httptest.NewRequest(http.MethodPut, "/api/v1/federation/members/m1", bytes.NewBufferString(body)),
		domain.NewIntegrationPrincipal("Nestova"))
	r.SetPathValue("member_id", "m1")
	w := httptest.NewRecorder()

	h.Provision(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("Provision(blank household_id) status = %d, want 422 (body %q)", w.Code, w.Body.String())
	}
	got := w.Body.String()
	if !strings.Contains(got, `"household_id"`) {
		t.Errorf("Provision(blank household_id) body = %q, want field %q", got, "household_id")
	}
	if !strings.Contains(got, invalidFederationIDMessage) {
		t.Errorf("Provision(blank household_id) body = %q, want message %q", got, invalidFederationIDMessage)
	}
}

// TestFederationAPIHandlers_Provision_UpsertInvalidRoleMapsTo422 covers
// mapFederationError's domain.ErrInvalidRole branch: the request's own
// role string parses fine (ParseRole succeeds locally), but the service
// call itself rejects it — the defensive path a stale or race-losing
// caller can still hit at the app layer.
func TestFederationAPIHandlers_Provision_UpsertInvalidRoleMapsTo422(t *testing.T) {
	svc := &fakeFederationService{upsertErr: domain.ErrInvalidRole}
	h := NewFederationAPIHandlers(svc, federationAPITestLogger())
	body := `{"household_id":"h1","account":{"display_name":"Maya","email":"maya@example.com","role":"member","active":true}}`
	r := withTestPrincipal(httptest.NewRequest(http.MethodPut, "/api/v1/federation/members/m1", bytes.NewBufferString(body)),
		domain.NewIntegrationPrincipal("Nestova"))
	r.SetPathValue("member_id", "m1")
	w := httptest.NewRecorder()

	h.Provision(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("Provision(service rejects role) status = %d, want 422 (body %q)", w.Code, w.Body.String())
	}
	got := w.Body.String()
	if !strings.Contains(got, `"account.role"`) {
		t.Errorf("Provision(service rejects role) body = %q, want field %q", got, "account.role")
	}
	if !strings.Contains(got, "invalid_request") {
		t.Errorf("Provision(service rejects role) body = %q, want code %q", got, "invalid_request")
	}
}

// TestFederationAPIHandlers_Provision_LinkInvalidMemberLinkMapsTo422
// covers mapFederationError's domain.ErrInvalidMemberLink branch — its
// comment notes this is reached only if a request gets past the handler's
// own ValidateMemberID/ValidateHouseholdID pre-validation with an
// otherwise-malformed identifier the service itself then rejects. Unlike
// the ErrInvalidRole case, this branch carries no FieldDetail, so the
// response must have no "details" key at all.
func TestFederationAPIHandlers_Provision_LinkInvalidMemberLinkMapsTo422(t *testing.T) {
	svc := &fakeFederationService{linkErr: domain.ErrInvalidMemberLink}
	h := NewFederationAPIHandlers(svc, federationAPITestLogger())
	body := `{"household_id":"h1","link":{"user_id":"` + domain.NewUserID().String() + `"}}`
	r := withTestPrincipal(httptest.NewRequest(http.MethodPut, "/api/v1/federation/members/m1", bytes.NewBufferString(body)),
		domain.NewIntegrationPrincipal("Nestova"))
	r.SetPathValue("member_id", "m1")
	w := httptest.NewRecorder()

	h.Provision(w, r)

	if w.Code != http.StatusUnprocessableEntity {
		t.Fatalf("Provision(service rejects member link) status = %d, want 422 (body %q)", w.Code, w.Body.String())
	}
	got := w.Body.String()
	if !strings.Contains(got, "invalid_request") {
		t.Errorf("Provision(service rejects member link) body = %q, want code %q", got, "invalid_request")
	}
	if !strings.Contains(got, errInvalidRequestMessage) {
		t.Errorf("Provision(service rejects member link) body = %q, want message %q", got, errInvalidRequestMessage)
	}
	if strings.Contains(got, `"details"`) {
		t.Errorf("Provision(service rejects member link) body = %q, want no details key (nil FieldDetail)", got)
	}
}

func TestNewFederationAPIHandlers_NilDependenciesPanic(t *testing.T) {
	t.Run("nil service", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewFederationAPIHandlers(nil, logger) did not panic")
			}
		}()
		NewFederationAPIHandlers(nil, federationAPITestLogger())
	})
	t.Run("nil logger", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("NewFederationAPIHandlers(service, nil) did not panic")
			}
		}()
		NewFederationAPIHandlers(&fakeFederationService{}, nil)
	})
}
