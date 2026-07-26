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

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestcore/crypto/cryptotest"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	identityapp "github.com/ericfisherdev/nestorage/internal/identity/app"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/config"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
)

// fakePasswordChanger is a configurable passwordChanger fake,
// mirroring fakeDeviceTokenWebService's identical shape (devicetokenweb_test.go).
type fakePasswordChanger struct {
	err error

	calls []struct {
		userID          domain.UserID
		currentPassword string
		newPassword     string
	}
}

func (f *fakePasswordChanger) ChangeOwn(_ context.Context, id domain.UserID, currentPassword, newPassword string) error {
	f.calls = append(f.calls, struct {
		userID          domain.UserID
		currentPassword string
		newPassword     string
	}{id, currentPassword, newPassword})
	return f.err
}

// passwordWebFixture wires PasswordWebHandlers behind the real
// Authenticate/RequireUser middleware chain over an in-memory session store,
// backed by a fake passwordChanger — mirrors deviceWebFixture's
// identical shape (devicetokenweb_test.go). It reuses that file's
// fakeCurrentUserFinder and passthroughLayout, since both live in this same
// adapter_test package.
type passwordWebFixture struct {
	server    *httptest.Server
	client    *http.Client
	passwords *fakePasswordChanger
}

func newPasswordWebFixture(t *testing.T, user *domain.User, mode config.FederationMode, providerURL string) *passwordWebFixture {
	t.Helper()
	sm := scs.New()
	userFinder := &fakeCurrentUserFinder{users: map[domain.UserID]*domain.User{user.ID: user}}
	passwords := &fakePasswordChanger{}
	handlers := adapter.NewPasswordWebHandlers(passwords, sm, passthroughLayout, mode, providerURL, testLogger())

	passwordMux := http.NewServeMux()
	handlers.Routes(passwordMux)

	outer := http.NewServeMux()
	outer.HandleFunc("POST /seed", func(w http.ResponseWriter, r *http.Request) {
		sm.Put(r.Context(), session.KeyUserID, r.FormValue("user_id"))
		w.WriteHeader(http.StatusNoContent)
	})
	outer.HandleFunc("GET /csrf", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, session.CSRFToken(r.Context(), sm))
	})
	outer.Handle("/settings/", adapter.RequireUser()(passwordMux))

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
	return &passwordWebFixture{server: server, client: client, passwords: passwords}
}

func (f *passwordWebFixture) seedSession(t *testing.T, userID domain.UserID) {
	t.Helper()
	resp, err := f.client.PostForm(f.server.URL+"/seed", url.Values{"user_id": {userID.String()}})
	if err != nil {
		t.Fatalf("POST /seed: %v", err)
	}
	_ = resp.Body.Close()
}

func (f *passwordWebFixture) csrfToken(t *testing.T) string {
	t.Helper()
	resp, err := f.client.Get(f.server.URL + "/csrf")
	if err != nil {
		t.Fatalf("GET /csrf: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func (f *passwordWebFixture) changeForm(csrfToken, current, newPassword, confirmation string) url.Values {
	return url.Values{
		"csrf_token":                {csrfToken},
		"current_password":          {current},
		"new_password":              {newPassword},
		"new_password_confirmation": {confirmation},
	}
}

func TestNewPasswordWebHandlers_NilDependenciesPanic(t *testing.T) {
	sm := scs.New()
	passwords := &fakePasswordChanger{}
	tests := []struct {
		name string
		fn   func()
	}{
		{"nil service", func() {
			adapter.NewPasswordWebHandlers(nil, sm, passthroughLayout, config.FederationModeStandalone, "", testLogger())
		}},
		{"nil session manager", func() {
			adapter.NewPasswordWebHandlers(passwords, nil, passthroughLayout, config.FederationModeStandalone, "", testLogger())
		}},
		{"nil layout", func() {
			adapter.NewPasswordWebHandlers(passwords, sm, nil, config.FederationModeStandalone, "", testLogger())
		}},
		{"nil logger", func() {
			adapter.NewPasswordWebHandlers(passwords, sm, passthroughLayout, config.FederationModeStandalone, "", nil)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewPasswordWebHandlers(%s) did not panic", tt.name)
				}
			}()
			tt.fn()
		})
	}
}

func TestPasswordWeb_Show_Unauthenticated_RedirectsToLogin(t *testing.T) {
	f := newPasswordWebFixture(t, &domain.User{ID: domain.NewUserID(), Active: true}, config.FederationModeStandalone, "")

	resp, err := f.client.Get(f.server.URL + "/settings/password")
	if err != nil {
		t.Fatalf("GET /settings/password: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (RequireUser redirects an anonymous request)", resp.StatusCode, http.StatusSeeOther)
	}
}

func TestPasswordWeb_Show_Standalone_RendersForm(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newPasswordWebFixture(t, user, config.FederationModeStandalone, "")
	f.seedSession(t, user.ID)

	resp, err := f.client.Get(f.server.URL + "/settings/password")
	if err != nil {
		t.Fatalf("GET /settings/password: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, http.StatusOK, body)
	}
	if !bytes.Contains(body, []byte(`name="current_password"`)) {
		t.Errorf("response missing the password form: %s", body)
	}
}

// TestPasswordWeb_Show_Federated_NoticeLinksProviderNoForm is the automated
// equivalent of this ticket's "in federated mode the interface does not
// present a change-password form and instead directs the person to the
// provider" criterion.
func TestPasswordWeb_Show_Federated_NoticeLinksProviderNoForm(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newPasswordWebFixture(t, user, config.FederationModeFederated, "https://provider.example.com")
	f.seedSession(t, user.ID)

	resp, err := f.client.Get(f.server.URL + "/settings/password")
	if err != nil {
		t.Fatalf("GET /settings/password: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", resp.StatusCode, http.StatusOK, body)
	}
	if bytes.Contains(body, []byte(`name="current_password"`)) {
		t.Errorf("federated GET must not render the password form: %s", body)
	}
	if !bytes.Contains(body, []byte("https://provider.example.com")) {
		t.Errorf("federated GET missing the provider link: %s", body)
	}
}

// TestPasswordWeb_Change_Federated_HandConstructedRequestForbidden is the
// automated equivalent of this ticket's "in federated mode a
// hand-constructed change-password request is refused rather than partially
// applied" criterion: a well-formed POST with a valid CSRF token still gets
// 403, and the service is never called.
func TestPasswordWeb_Change_Federated_HandConstructedRequestForbidden(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newPasswordWebFixture(t, user, config.FederationModeFederated, "https://provider.example.com")
	f.seedSession(t, user.ID)
	csrf := f.csrfToken(t)

	resp, err := f.client.PostForm(f.server.URL+"/settings/password", f.changeForm(csrf, "current", "a-new-correct-horse", "a-new-correct-horse"))
	if err != nil {
		t.Fatalf("POST /settings/password: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	if len(f.passwords.calls) != 0 {
		t.Error("a federated household's change request must never reach ChangeOwn")
	}
}

func TestPasswordWeb_Change_MissingCSRF_Forbidden(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newPasswordWebFixture(t, user, config.FederationModeStandalone, "")
	f.seedSession(t, user.ID)

	resp, err := f.client.PostForm(f.server.URL+"/settings/password", f.changeForm("wrong-token", "current", "a-new-correct-horse", "a-new-correct-horse"))
	if err != nil {
		t.Fatalf("POST /settings/password: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	if len(f.passwords.calls) != 0 {
		t.Error("a wrong CSRF token must not reach ChangeOwn")
	}
}

func TestPasswordWeb_Change_Mismatch_UnprocessableEntity(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newPasswordWebFixture(t, user, config.FederationModeStandalone, "")
	f.seedSession(t, user.ID)
	csrf := f.csrfToken(t)

	resp, err := f.client.PostForm(f.server.URL+"/settings/password", f.changeForm(csrf, "current", "a-new-correct-horse", "does-not-match"))
	if err != nil {
		t.Fatalf("POST /settings/password: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	if !bytes.Contains(body, []byte("Passwords do not match.")) {
		t.Errorf("response missing the mismatch message: %s", body)
	}
	if len(f.passwords.calls) != 0 {
		t.Error("a mismatched confirmation must not reach ChangeOwn")
	}
}

func TestPasswordWeb_Change_WrongCurrentPassword_UnprocessableEntity(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newPasswordWebFixture(t, user, config.FederationModeStandalone, "")
	f.passwords.err = domain.ErrInvalidCredentials
	f.seedSession(t, user.ID)
	csrf := f.csrfToken(t)

	resp, err := f.client.PostForm(f.server.URL+"/settings/password", f.changeForm(csrf, "wrong-current", "a-new-correct-horse", "a-new-correct-horse"))
	if err != nil {
		t.Fatalf("POST /settings/password: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	if !bytes.Contains(body, []byte("Current password is incorrect.")) {
		t.Errorf("response missing the wrong-current-password message: %s", body)
	}
}

func TestPasswordWeb_Change_TooShort_UnprocessableEntity(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newPasswordWebFixture(t, user, config.FederationModeStandalone, "")
	f.passwords.err = domain.ErrPasswordTooShort
	f.seedSession(t, user.ID)
	csrf := f.csrfToken(t)

	resp, err := f.client.PostForm(f.server.URL+"/settings/password", f.changeForm(csrf, "current", "short", "short"))
	if err != nil {
		t.Fatalf("POST /settings/password: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	if !bytes.Contains(body, []byte("Password must be at least 12 characters.")) {
		t.Errorf("response missing the too-short message: %s", body)
	}
}

func TestPasswordWeb_Change_TooLong_UnprocessableEntity(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newPasswordWebFixture(t, user, config.FederationModeStandalone, "")
	f.passwords.err = domain.ErrPasswordTooLong
	f.seedSession(t, user.ID)
	csrf := f.csrfToken(t)

	resp, err := f.client.PostForm(f.server.URL+"/settings/password", f.changeForm(csrf, "current", "long", "long"))
	if err != nil {
		t.Fatalf("POST /settings/password: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusUnprocessableEntity)
	}
	if !bytes.Contains(body, []byte("Password must be at most 128 characters.")) {
		t.Errorf("response missing the too-long message: %s", body)
	}
}

func TestPasswordWeb_Change_UnrecognizedError_MapsTo500(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newPasswordWebFixture(t, user, config.FederationModeStandalone, "")
	f.passwords.err = errors.New("boom")
	f.seedSession(t, user.ID)
	csrf := f.csrfToken(t)

	resp, err := f.client.PostForm(f.server.URL+"/settings/password", f.changeForm(csrf, "current", "a-new-correct-horse", "a-new-correct-horse"))
	if err != nil {
		t.Fatalf("POST /settings/password: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
}

func TestPasswordWeb_Change_HTMXRequestGetsFragmentWithConfirmation(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newPasswordWebFixture(t, user, config.FederationModeStandalone, "")
	f.seedSession(t, user.ID)
	csrf := f.csrfToken(t)

	form := f.changeForm(csrf, "current", "a-new-correct-horse", "a-new-correct-horse")
	req, err := http.NewRequest(http.MethodPost, f.server.URL+"/settings/password", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")

	resp, err := f.client.Do(req)
	if err != nil {
		t.Fatalf("POST /settings/password: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d (HTMX request gets the fragment, not a redirect)", resp.StatusCode, http.StatusOK)
	}
	if !bytes.Contains(body, []byte("Your password has been changed.")) {
		t.Errorf("response missing the confirmation message: %s", body)
	}
	if len(f.passwords.calls) != 1 || f.passwords.calls[0].userID != user.ID {
		t.Errorf("ChangeOwn calls = %v, want exactly one call for %v", f.passwords.calls, user.ID)
	}
}

func TestPasswordWeb_Change_FullNavigation_Redirects(t *testing.T) {
	user := &domain.User{ID: domain.NewUserID(), Active: true}
	f := newPasswordWebFixture(t, user, config.FederationModeStandalone, "")
	f.seedSession(t, user.ID)
	csrf := f.csrfToken(t)

	resp, err := f.client.PostForm(f.server.URL+"/settings/password", f.changeForm(csrf, "current", "a-new-correct-horse", "a-new-correct-horse"))
	if err != nil {
		t.Fatalf("POST /settings/password: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}
}

// fakePasswordWebRepo is an in-memory fake satisfying BOTH currentUserFinder
// (FindByID, for adapter.Authenticate) and app's own passwordChanger
// (FindByID + SetPasswordHash, for identityapp.NewPasswordService) — the one
// fake realServicePasswordWebFixture needs to wire the REAL
// app.PasswordService end to end with no database.
type fakePasswordWebRepo struct {
	users map[domain.UserID]*domain.User
}

func (f *fakePasswordWebRepo) FindByID(_ context.Context, id domain.UserID) (*domain.User, error) {
	u, ok := f.users[id]
	if !ok {
		return nil, domain.ErrUserNotFound
	}
	return u, nil
}

func (f *fakePasswordWebRepo) SetPasswordHash(_ context.Context, id domain.UserID, hash string) error {
	u, ok := f.users[id]
	if !ok {
		return domain.ErrUserNotFound
	}
	u.PasswordHash = hash
	return nil
}

// realServicePasswordWebFixture wires PasswordWebHandlers over the REAL
// app.PasswordService and the REAL SessionRevoker(sm) — the only way to
// exercise ChangeOwn's actual session-revocation effect, as opposed to
// fakePasswordChanger's recorded calls above. sm is plain scs.New()
// (memstore), which implements scs.IterableStore (memstore.All), so
// SessionRevoker.RevokeAll runs for real — see SessionRevoker's own doc.
type realServicePasswordWebFixture struct {
	server *httptest.Server
}

func newRealServicePasswordWebFixture(t *testing.T, user *domain.User) *realServicePasswordWebFixture {
	t.Helper()
	sm := scs.New()
	userFinder := &fakePasswordWebRepo{users: map[domain.UserID]*domain.User{user.ID: user}}
	revoker := adapter.NewSessionRevoker(sm)
	service := identityapp.NewPasswordService(userFinder, cryptotest.Hasher(), revoker, testLogger())
	handlers := adapter.NewPasswordWebHandlers(service, sm, passthroughLayout, config.FederationModeStandalone, "", testLogger())

	passwordMux := http.NewServeMux()
	handlers.Routes(passwordMux)

	outer := http.NewServeMux()
	outer.HandleFunc("POST /seed", func(w http.ResponseWriter, r *http.Request) {
		sm.Put(r.Context(), session.KeyUserID, r.FormValue("user_id"))
		w.WriteHeader(http.StatusNoContent)
	})
	outer.HandleFunc("GET /csrf", func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, session.CSRFToken(r.Context(), sm))
	})
	outer.Handle("/settings/", adapter.RequireUser()(passwordMux))

	authenticate := adapter.Authenticate(sm, userFinder, testLogger())
	server := httptest.NewServer(sm.LoadAndSave(authenticate(outer)))
	t.Cleanup(server.Close)

	return &realServicePasswordWebFixture{server: server}
}

func newFixtureClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (f *realServicePasswordWebFixture) seedSession(t *testing.T, client *http.Client, userID domain.UserID) {
	t.Helper()
	resp, err := client.PostForm(f.server.URL+"/seed", url.Values{"user_id": {userID.String()}})
	if err != nil {
		t.Fatalf("POST /seed: %v", err)
	}
	_ = resp.Body.Close()
}

func (f *realServicePasswordWebFixture) csrfToken(t *testing.T, client *http.Client) string {
	t.Helper()
	resp, err := client.Get(f.server.URL + "/csrf")
	if err != nil {
		t.Fatalf("GET /csrf: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func (f *realServicePasswordWebFixture) settingsReachable(t *testing.T, client *http.Client) bool {
	t.Helper()
	resp, err := client.Get(f.server.URL + "/settings/password")
	if err != nil {
		t.Fatalf("GET /settings/password: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

// TestPasswordWeb_Change_Success_RevokesOtherSessionsKeepsChangingSession is
// the automated equivalent of this ticket's "the effect on the member's
// other sessions is deliberate, documented and tested" criterion: a second
// session for the same user is destroyed by ChangeOwn's own RevokeAll, while
// the session that made the change survives under a renewed token
// (finishChange's own doc).
func TestPasswordWeb_Change_Success_RevokesOtherSessionsKeepsChangingSession(t *testing.T) {
	const currentPassword = "correct-horse-battery-staple"
	hash, err := cryptotest.Hasher().Hash(currentPassword)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	user := &domain.User{ID: domain.NewUserID(), Active: true, PasswordHash: hash}
	f := newRealServicePasswordWebFixture(t, user)

	changingClient := newFixtureClient(t)
	otherClient := newFixtureClient(t)
	f.seedSession(t, changingClient, user.ID)
	f.seedSession(t, otherClient, user.ID)

	if !f.settingsReachable(t, changingClient) {
		t.Fatal("the changing session must be valid before the change")
	}
	if !f.settingsReachable(t, otherClient) {
		t.Fatal("the other session must be valid before the change")
	}

	csrf := f.csrfToken(t, changingClient)
	form := url.Values{
		"csrf_token":                {csrf},
		"current_password":          {currentPassword},
		"new_password":              {"a-new-correct-horse-battery"},
		"new_password_confirmation": {"a-new-correct-horse-battery"},
	}
	resp, err := changingClient.PostForm(f.server.URL+"/settings/password", form)
	if err != nil {
		t.Fatalf("POST /settings/password: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusSeeOther)
	}

	if !f.settingsReachable(t, changingClient) {
		t.Error("the changing session must survive its own password change under a renewed token")
	}
	if f.settingsReachable(t, otherClient) {
		t.Error("every OTHER session belonging to the user must be revoked by a successful password change")
	}
}
