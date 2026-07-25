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

	"github.com/a-h/templ"
	"github.com/alexedwards/scs/v2"

	identityadapter "github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/notify/adapter"
	"github.com/ericfisherdev/nestorage/internal/notify/app"
	"github.com/ericfisherdev/nestorage/internal/notify/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
)

// fakePreferencesWebService is a configurable preferencesWebService fake.
type fakePreferencesWebService struct {
	sections map[identity.UserID][]app.EventTypeSection

	preferencesErr error
	setErr         error

	setCalls []struct {
		userID    identity.UserID
		eventType domain.EventType
		enabled   bool
	}
}

func (f *fakePreferencesWebService) PreferencesForUser(_ context.Context, userID identity.UserID) ([]app.EventTypeSection, error) {
	if f.preferencesErr != nil {
		return nil, f.preferencesErr
	}
	if sections, ok := f.sections[userID]; ok {
		return sections, nil
	}
	return []app.EventTypeSection{
		{EventType: domain.EventTypeReturnRequested, InApp: true, EmailEnabled: false},
		{EventType: domain.EventTypeItemReturned, InApp: true, EmailEnabled: false},
	}, nil
}

func (f *fakePreferencesWebService) SetEmailEnabled(_ context.Context, userID identity.UserID, eventType domain.EventType, enabled bool) error {
	f.setCalls = append(f.setCalls, struct {
		userID    identity.UserID
		eventType domain.EventType
		enabled   bool
	}{userID, eventType, enabled})
	if f.setErr != nil {
		return f.setErr
	}
	if f.sections == nil {
		f.sections = make(map[identity.UserID][]app.EventTypeSection)
	}
	f.sections[userID] = []app.EventTypeSection{
		{EventType: domain.EventTypeReturnRequested, InApp: true, EmailEnabled: eventType == domain.EventTypeReturnRequested && enabled},
		{EventType: domain.EventTypeItemReturned, InApp: true, EmailEnabled: eventType == domain.EventTypeItemReturned && enabled},
	}
	return nil
}

// preferencesWebFixture wires PreferencesWebHandlers behind the real
// Authenticate/RequireUser middleware chain over an in-memory session
// store — mirrors deviceWebFixture in devicetokenweb_test.go (identity/adapter),
// the pattern this handler is required to match exactly.
type preferencesWebFixture struct {
	server *httptest.Server
	client *http.Client
	prefs  *fakePreferencesWebService
}

func passthroughLayout(_ *http.Request, content templ.Component) templ.Component { return content }

// fakeCurrentUserFinder duplicates identity/adapter's own test fixture of
// the same name (unexported to its package, so it cannot be reused
// directly across packages).
type fakeCurrentUserFinder struct {
	users map[identity.UserID]*identity.User
}

func (f *fakeCurrentUserFinder) FindByID(_ context.Context, id identity.UserID) (*identity.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, identity.ErrUserNotFound
	}
	return u, nil
}

func newPreferencesWebFixture(t *testing.T, user *identity.User) *preferencesWebFixture {
	t.Helper()
	sm := scs.New()
	userFinder := &fakeCurrentUserFinder{users: map[identity.UserID]*identity.User{user.ID: user}}
	prefs := &fakePreferencesWebService{}
	handlers := adapter.NewPreferencesWebHandlers(prefs, sm, passthroughLayout, testLogger())

	prefsMux := http.NewServeMux()
	handlers.Routes(prefsMux)

	outer := http.NewServeMux()
	outer.HandleFunc("POST /seed", func(w http.ResponseWriter, r *http.Request) {
		sm.Put(r.Context(), session.KeyUserID, r.FormValue("user_id"))
		w.WriteHeader(http.StatusNoContent)
	})
	outer.HandleFunc("GET /csrf", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, session.CSRFToken(r.Context(), sm))
	})
	outer.Handle("/settings/", identityadapter.RequireUser()(prefsMux))

	authenticate := identityadapter.Authenticate(sm, userFinder, testLogger())
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
	return &preferencesWebFixture{server: server, client: client, prefs: prefs}
}

func (f *preferencesWebFixture) seedSession(t *testing.T, userID identity.UserID) {
	t.Helper()
	resp, err := f.client.PostForm(f.server.URL+"/seed", url.Values{"user_id": {userID.String()}})
	if err != nil {
		t.Fatalf("POST /seed: %v", err)
	}
	_ = resp.Body.Close()
}

func (f *preferencesWebFixture) csrfToken(t *testing.T) string {
	t.Helper()
	resp, err := f.client.Get(f.server.URL + "/csrf")
	if err != nil {
		t.Fatalf("GET /csrf: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func TestNewPreferencesWebHandlers_NilDependenciesPanic(t *testing.T) {
	sm := scs.New()
	prefs := &fakePreferencesWebService{}
	tests := []struct {
		name string
		fn   func()
	}{
		{"nil service", func() { adapter.NewPreferencesWebHandlers(nil, sm, passthroughLayout, testLogger()) }},
		{"nil session manager", func() { adapter.NewPreferencesWebHandlers(prefs, nil, passthroughLayout, testLogger()) }},
		{"nil layout", func() { adapter.NewPreferencesWebHandlers(prefs, sm, nil, testLogger()) }},
		{"nil logger", func() { adapter.NewPreferencesWebHandlers(prefs, sm, passthroughLayout, nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewPreferencesWebHandlers(%s) did not panic", tt.name)
				}
			}()
			tt.fn()
		})
	}
}

func TestPreferencesWeb_Settings_Unauthenticated_RedirectsToLogin(t *testing.T) {
	f := newPreferencesWebFixture(t, &identity.User{ID: identity.NewUserID(), Active: true})

	resp, err := f.client.Get(f.server.URL + "/settings/notifications")
	if err != nil {
		t.Fatalf("GET /settings/notifications: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (RequireUser redirects an anonymous request)", resp.StatusCode, http.StatusSeeOther)
	}
}

// TestPreferencesWeb_Settings_RendersAlwaysOnInAppAndUncheckedEmail is the
// "new users get the documented defaults... with zero configuration"
// acceptance criterion at the HTTP layer.
func TestPreferencesWeb_Settings_RendersAlwaysOnInAppAndUncheckedEmail(t *testing.T) {
	user := &identity.User{ID: identity.NewUserID(), Active: true}
	f := newPreferencesWebFixture(t, user)
	f.seedSession(t, user.ID)

	resp, err := f.client.Get(f.server.URL + "/settings/notifications")
	if err != nil {
		t.Fatalf("GET /settings/notifications: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, http.StatusOK, body)
	}
	if !bytes.Contains(body, []byte("Return requested")) {
		t.Errorf("response missing the return_requested section: %s", body)
	}
	if !bytes.Contains(body, []byte("Item returned")) {
		t.Errorf("response missing the item_returned section: %s", body)
	}
	if !bytes.Contains(body, []byte("Always on")) {
		t.Errorf("response missing the always-on in-app row: %s", body)
	}
	if bytes.Contains(body, []byte("checked")) {
		t.Errorf("a brand new user's email toggle must render unchecked by default: %s", body)
	}
}

func TestPreferencesWeb_Settings_RepositoryErrorIs500(t *testing.T) {
	user := &identity.User{ID: identity.NewUserID(), Active: true}
	f := newPreferencesWebFixture(t, user)
	f.prefs.preferencesErr = errors.New("boom")
	f.seedSession(t, user.ID)

	resp, err := f.client.Get(f.server.URL + "/settings/notifications")
	if err != nil {
		t.Fatalf("GET /settings/notifications: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

// TestPreferencesWeb_SetEmailEnabled_HTMXRequestGetsFragment asserts an
// HTMX-originated toggle gets the re-rendered settings fragment directly.
func TestPreferencesWeb_SetEmailEnabled_HTMXRequestGetsFragment(t *testing.T) {
	user := &identity.User{ID: identity.NewUserID(), Active: true}
	f := newPreferencesWebFixture(t, user)
	f.seedSession(t, user.ID)
	csrf := f.csrfToken(t)

	form := url.Values{"csrf_token": {csrf}, "enabled": {"true"}}
	req, err := http.NewRequest(http.MethodPost, f.server.URL+"/settings/notifications/return_requested/email", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")

	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("POST email toggle: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d (HTMX request gets the fragment, not a redirect)", resp.StatusCode, http.StatusOK)
	}
}

// TestPreferencesWeb_SetEmailEnabled_PlainNavigationRedirects proves the
// no-JavaScript fallback: a plain form POST (no HX-Request header) gets a
// 303 redirect back to the settings page, matching DeviceTokenWebHandlers'
// own finishMutation contract.
func TestPreferencesWeb_SetEmailEnabled_PlainNavigationRedirects(t *testing.T) {
	user := &identity.User{ID: identity.NewUserID(), Active: true}
	f := newPreferencesWebFixture(t, user)
	f.seedSession(t, user.ID)
	csrf := f.csrfToken(t)

	resp, err := f.client.PostForm(f.server.URL+"/settings/notifications/return_requested/email", url.Values{"csrf_token": {csrf}, "enabled": {"true"}})
	if err != nil {
		t.Fatalf("POST email toggle: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
	if got := resp.Header.Get("Location"); got != "/settings/notifications" {
		t.Errorf("Location = %q, want %q", got, "/settings/notifications")
	}
}

// TestPreferencesWeb_SetEmailEnabled_ScopedToSessionUser asserts
// SetEmailEnabled is called with the SESSION user's id, not any value the
// request could supply — mirrors DeviceTokenWebHandlers' identical own
// test.
func TestPreferencesWeb_SetEmailEnabled_ScopedToSessionUser(t *testing.T) {
	user := &identity.User{ID: identity.NewUserID(), Active: true}
	f := newPreferencesWebFixture(t, user)
	f.seedSession(t, user.ID)
	csrf := f.csrfToken(t)

	resp, err := f.client.PostForm(f.server.URL+"/settings/notifications/item_returned/email", url.Values{"csrf_token": {csrf}, "enabled": {"true"}})
	if err != nil {
		t.Fatalf("POST email toggle: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if len(f.prefs.setCalls) != 1 {
		t.Fatalf("SetEmailEnabled calls = %d, want 1", len(f.prefs.setCalls))
	}
	call := f.prefs.setCalls[0]
	if call.userID != user.ID {
		t.Errorf("SetEmailEnabled called with userID = %v, want the session user %v", call.userID, user.ID)
	}
	if call.eventType != domain.EventTypeItemReturned {
		t.Errorf("SetEmailEnabled called with eventType = %v, want %v", call.eventType, domain.EventTypeItemReturned)
	}
	if !call.enabled {
		t.Error("SetEmailEnabled called with enabled = false, want true (checkbox value was present)")
	}
}

// TestPreferencesWeb_SetEmailEnabled_UncheckedBoxIsDisabled proves the
// standard HTML forms contract this handler relies on: an unchecked
// checkbox sends no "enabled" field at all, and that absence must be read
// as false, not rejected as a bad request.
func TestPreferencesWeb_SetEmailEnabled_UncheckedBoxIsDisabled(t *testing.T) {
	user := &identity.User{ID: identity.NewUserID(), Active: true}
	f := newPreferencesWebFixture(t, user)
	f.seedSession(t, user.ID)
	csrf := f.csrfToken(t)

	resp, err := f.client.PostForm(f.server.URL+"/settings/notifications/return_requested/email", url.Values{"csrf_token": {csrf}})
	if err != nil {
		t.Fatalf("POST email toggle: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
	if len(f.prefs.setCalls) != 1 || f.prefs.setCalls[0].enabled {
		t.Errorf("setCalls = %+v, want exactly one call with enabled=false", f.prefs.setCalls)
	}
}

func TestPreferencesWeb_SetEmailEnabled_WrongCSRFForbidden(t *testing.T) {
	user := &identity.User{ID: identity.NewUserID(), Active: true}
	f := newPreferencesWebFixture(t, user)
	f.seedSession(t, user.ID)

	resp, err := f.client.PostForm(f.server.URL+"/settings/notifications/return_requested/email", url.Values{"csrf_token": {"wrong-token"}, "enabled": {"true"}})
	if err != nil {
		t.Fatalf("POST email toggle: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	if len(f.prefs.setCalls) != 0 {
		t.Error("a wrong CSRF token must not reach the service's SetEmailEnabled method")
	}
}

func TestPreferencesWeb_SetEmailEnabled_MalformedEventBadRequest(t *testing.T) {
	user := &identity.User{ID: identity.NewUserID(), Active: true}
	f := newPreferencesWebFixture(t, user)
	f.seedSession(t, user.ID)

	resp, err := f.client.PostForm(f.server.URL+"/settings/notifications/task_due_soon/email", url.Values{"csrf_token": {"whatever"}, "enabled": {"true"}})
	if err != nil {
		t.Fatalf("POST email toggle: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
	if len(f.prefs.setCalls) != 0 {
		t.Error("a malformed event type must not reach the service")
	}
}

func TestPreferencesWeb_SetEmailEnabled_UnrecognizedErrorIs500(t *testing.T) {
	user := &identity.User{ID: identity.NewUserID(), Active: true}
	f := newPreferencesWebFixture(t, user)
	f.prefs.setErr = errors.New("boom")
	f.seedSession(t, user.ID)
	csrf := f.csrfToken(t)

	resp, err := f.client.PostForm(f.server.URL+"/settings/notifications/return_requested/email", url.Values{"csrf_token": {csrf}, "enabled": {"true"}})
	if err != nil {
		t.Fatalf("POST email toggle: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}
