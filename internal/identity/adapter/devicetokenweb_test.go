package adapter_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
)

// fakeCurrentUserFinder is a minimal currentUserFinder fake (FindByID only)
// so adapter.Authenticate can resolve a seeded session into a *domain.User
// without a database.
type fakeCurrentUserFinder struct {
	users map[domain.UserID]*domain.User
}

func (f *fakeCurrentUserFinder) FindByID(_ context.Context, id domain.UserID) (*domain.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, domain.ErrUserNotFound
	}
	return u, nil
}

// fakeDeviceTokenWebService is a configurable deviceTokenWebService fake.
type fakeDeviceTokenWebService struct {
	tokens map[domain.UserID][]*domain.DeviceToken

	listErr   error
	revokeErr error

	revokeCalls []struct {
		userID domain.UserID
		id     domain.DeviceTokenID
	}
}

func (f *fakeDeviceTokenWebService) ListForUser(_ context.Context, userID domain.UserID) ([]*domain.DeviceToken, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.tokens[userID], nil
}

func (f *fakeDeviceTokenWebService) Revoke(_ context.Context, userID domain.UserID, id domain.DeviceTokenID) error {
	f.revokeCalls = append(f.revokeCalls, struct {
		userID domain.UserID
		id     domain.DeviceTokenID
	}{userID, id})
	if f.revokeErr != nil {
		return f.revokeErr
	}
	return nil
}

// deviceWebFixture wires DeviceTokenWebHandlers behind the real
// Authenticate/RequireUser middleware chain over an in-memory session store,
// plus two test-only routes (unauthenticated) that let a test establish a
// session for an arbitrary user and read that session's CSRF token as plain
// text — avoiding any dependency on this package's own rendered HTML shape.
type deviceWebFixture struct {
	server  *httptest.Server
	client  *http.Client
	devices *fakeDeviceTokenWebService
}

// passthroughLayout is the requestLayoutFunc-shaped test layout: it renders
// bare content, matching every other test in this package that has no
// reason to assert on the full app shell.
func passthroughLayout(_ *http.Request, content templ.Component) templ.Component { return content }

func newDeviceWebFixture(t *testing.T, user *domain.User) *deviceWebFixture {
	t.Helper()
	sm := scs.New()
	userFinder := &fakeCurrentUserFinder{users: map[domain.UserID]*domain.User{user.ID: user}}
	devices := &fakeDeviceTokenWebService{tokens: make(map[domain.UserID][]*domain.DeviceToken)}
	handlers := adapter.NewDeviceTokenWebHandlers(devices, sm, passthroughLayout, testLogger())

	deviceMux := http.NewServeMux()
	handlers.Routes(deviceMux)

	outer := http.NewServeMux()
	outer.HandleFunc("POST /seed", func(w http.ResponseWriter, r *http.Request) {
		sm.Put(r.Context(), session.KeyUserID, r.FormValue("user_id"))
		w.WriteHeader(http.StatusNoContent)
	})
	outer.HandleFunc("GET /csrf", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, session.CSRFToken(r.Context(), sm))
	})
	outer.Handle("/settings/", adapter.RequireUser()(deviceMux))

	authenticate := adapter.Authenticate(sm, userFinder, testLogger())
	server := httptest.NewServer(sm.LoadAndSave(authenticate(outer)))
	t.Cleanup(server.Close)

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &deviceWebFixture{server: server, client: client, devices: devices}
}

func (f *deviceWebFixture) seedSession(t *testing.T, userID domain.UserID) {
	t.Helper()
	resp, err := f.client.PostForm(f.server.URL+"/seed", url.Values{"user_id": {userID.String()}})
	if err != nil {
		t.Fatalf("POST /seed: %v", err)
	}
	_ = resp.Body.Close()
}

func (f *deviceWebFixture) csrfToken(t *testing.T) string {
	t.Helper()
	resp, err := f.client.Get(f.server.URL + "/csrf")
	if err != nil {
		t.Fatalf("GET /csrf: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func TestNewDeviceTokenWebHandlers_NilDependenciesPanic(t *testing.T) {
	sm := scs.New()
	devices := &fakeDeviceTokenWebService{}
	tests := []struct {
		name string
		fn   func()
	}{
		{"nil service", func() { adapter.NewDeviceTokenWebHandlers(nil, sm, passthroughLayout, testLogger()) }},
		{"nil session manager", func() { adapter.NewDeviceTokenWebHandlers(devices, nil, passthroughLayout, testLogger()) }},
		{"nil layout", func() { adapter.NewDeviceTokenWebHandlers(devices, sm, nil, testLogger()) }},
		{"nil logger", func() { adapter.NewDeviceTokenWebHandlers(devices, sm, passthroughLayout, nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewDeviceTokenWebHandlers(%s) did not panic", tt.name)
				}
			}()
			tt.fn()
		})
	}
}

func TestDeviceTokenWeb_List_Unauthenticated_RedirectsToLogin(t *testing.T) {
	f := newDeviceWebFixture(t, &domain.User{ID: domain.NewUserID(), Active: true})

	resp, err := f.client.Get(f.server.URL + "/settings/devices")
	if err != nil {
		t.Fatalf("GET /settings/devices: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (RequireUser redirects an anonymous request)", resp.StatusCode, http.StatusSeeOther)
	}
}

func TestDeviceTokenWeb_List_RendersOwnDevices(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newDeviceWebFixture(t, user)
	lastUsed := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	f.devices.tokens[user.ID] = []*domain.DeviceToken{
		{ID: domain.NewDeviceTokenID(), UserID: user.ID, Name: "Maya Phone", LastUsedAt: nil},
		{ID: domain.NewDeviceTokenID(), UserID: user.ID, Name: "Maya Tablet", LastUsedAt: &lastUsed},
	}
	f.seedSession(t, user.ID)

	resp, err := f.client.Get(f.server.URL + "/settings/devices")
	if err != nil {
		t.Fatalf("GET /settings/devices: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, http.StatusOK, body)
	}
	if !bytes.Contains(body, []byte("Maya Phone")) {
		t.Errorf("response missing the never-used device's name: %s", body)
	}
	if !bytes.Contains(body, []byte("Never")) {
		t.Errorf("response missing the Never last-used fallback: %s", body)
	}
	if !bytes.Contains(body, []byte("Maya Tablet")) {
		t.Errorf("response missing the used device's name: %s", body)
	}
	if !bytes.Contains(body, []byte("Jul 20, 2026")) {
		t.Errorf("response missing the formatted last-used date: %s", body)
	}
}

func TestDeviceTokenWeb_List_RepositoryErrorIs500(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newDeviceWebFixture(t, user)
	f.devices.listErr = errors.New("boom")
	f.seedSession(t, user.ID)

	resp, err := f.client.Get(f.server.URL + "/settings/devices")
	if err != nil {
		t.Fatalf("GET /settings/devices: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestDeviceTokenWeb_Revoke_HTMXRequestGetsFragment asserts an HTMX-originated
// revoke gets the re-rendered list fragment directly (finishMutation's other
// branch from ScopedToSessionUser's plain-navigation redirect).
func TestDeviceTokenWeb_Revoke_HTMXRequestGetsFragment(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newDeviceWebFixture(t, user)
	f.seedSession(t, user.ID)
	csrf := f.csrfToken(t)

	form := url.Values{"csrf_token": {csrf}}
	req, err := http.NewRequest(http.MethodPost, f.server.URL+"/settings/devices/"+domain.NewDeviceTokenID().String()+"/revoke", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")

	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("POST revoke: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d (HTMX request gets the fragment, not a redirect)", resp.StatusCode, http.StatusOK)
	}
}

// TestDeviceTokenWeb_Revoke_ScopedToSessionUser asserts Revoke is called
// with the SESSION user's id, not any value the request could supply — a
// direct check that this handler never lets a caller revoke another user's
// device.
func TestDeviceTokenWeb_Revoke_ScopedToSessionUser(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newDeviceWebFixture(t, user)
	f.seedSession(t, user.ID)
	csrf := f.csrfToken(t)
	deviceID := domain.NewDeviceTokenID()

	resp, err := f.client.PostForm(f.server.URL+"/settings/devices/"+deviceID.String()+"/revoke", url.Values{"csrf_token": {csrf}})
	if err != nil {
		t.Fatalf("POST revoke: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
	if len(f.devices.revokeCalls) != 1 {
		t.Fatalf("Revoke calls = %d, want 1", len(f.devices.revokeCalls))
	}
	if got := f.devices.revokeCalls[0].userID; got != user.ID {
		t.Errorf("Revoke called with userID = %v, want the session user %v", got, user.ID)
	}
	if got := f.devices.revokeCalls[0].id; got != deviceID {
		t.Errorf("Revoke called with device id = %v, want %v", got, deviceID)
	}
}

func TestDeviceTokenWeb_Revoke_WrongCSRFForbidden(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newDeviceWebFixture(t, user)
	f.seedSession(t, user.ID)

	resp, err := f.client.PostForm(f.server.URL+"/settings/devices/"+domain.NewDeviceTokenID().String()+"/revoke", url.Values{"csrf_token": {"wrong-token"}})
	if err != nil {
		t.Fatalf("POST revoke: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	if len(f.devices.revokeCalls) != 0 {
		t.Error("a wrong CSRF token must not reach the service's Revoke method")
	}
}

func TestDeviceTokenWeb_Revoke_MalformedIDBadRequest(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newDeviceWebFixture(t, user)
	f.seedSession(t, user.ID)

	resp, err := f.client.PostForm(f.server.URL+"/settings/devices/not-a-uuid/revoke", url.Values{"csrf_token": {"whatever"}})
	if err != nil {
		t.Fatalf("POST revoke: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestDeviceTokenWeb_Revoke_NotFoundMapsTo404(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newDeviceWebFixture(t, user)
	f.devices.revokeErr = domain.ErrDeviceTokenNotFound
	f.seedSession(t, user.ID)
	csrf := f.csrfToken(t)

	resp, err := f.client.PostForm(f.server.URL+"/settings/devices/"+domain.NewDeviceTokenID().String()+"/revoke", url.Values{"csrf_token": {csrf}})
	if err != nil {
		t.Fatalf("POST revoke: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestDeviceTokenWeb_Revoke_UnrecognizedErrorIs500(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newDeviceWebFixture(t, user)
	f.devices.revokeErr = errors.New("boom")
	f.seedSession(t, user.ID)
	csrf := f.csrfToken(t)

	resp, err := f.client.PostForm(f.server.URL+"/settings/devices/"+domain.NewDeviceTokenID().String()+"/revoke", url.Values{"csrf_token": {csrf}})
	if err != nil {
		t.Fatalf("POST revoke: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}
