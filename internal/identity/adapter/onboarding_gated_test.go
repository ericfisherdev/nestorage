package adapter_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	corecfg "github.com/ericfisherdev/nestcore/config"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
)

// newWizardServer wires the real UserRepository, Provisioner, and a
// pgxstore-backed session manager over one derived database, then starts an
// httptest.Server over the wizard's routes — the same composition
// cmd/server/main.go builds, minus the setup guard (this test drives the
// wizard's own routes directly).
func newWizardServer(t *testing.T) (*httptest.Server, *adapter.UserRepository) {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "identity")
	repo := adapter.NewUserRepository(pool)
	provisioner := adapter.NewProvisioner(pool)
	sm := session.New(pool, corecfg.SessionConfig{Lifetime: time.Hour})
	handlers := adapter.NewOnboardingHandlers(repo, provisioner, sm, testLogger())

	mux := http.NewServeMux()
	handlers.Routes(mux)
	server := httptest.NewServer(sm.LoadAndSave(mux))
	t.Cleanup(server.Close)
	return server, repo
}

func newWizardClient(t *testing.T) *http.Client {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("cookiejar.New: %v", err)
	}
	return &http.Client{
		Jar: jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// sessionCookieValue returns client's current "session" cookie value for
// serverURL, or "" if none is set. Presence alone cannot distinguish a
// signed-in response from any other: sm.IdleTimeout > 0 makes scs reissue
// Set-Cookie on EVERY request that reuses an existing session, sliding the
// idle expiry regardless of what the handler did. Only RenewToken (called
// on a successful setup, never on a lost-race redirect) actually changes
// the token value — so comparing this before and after a request is what
// tells the two apart.
func sessionCookieValue(client *http.Client, serverURL string) string {
	u, err := url.Parse(serverURL)
	if err != nil {
		return ""
	}
	for _, c := range client.Jar.Cookies(u) {
		if c.Name == "session" {
			return c.Value
		}
	}
	return ""
}

// fetchWizardCSRF performs a GET against server+"/setup" and returns the
// embedded CSRF token, failing the test if the form (or its token) is
// missing. Only safe to call from the test's own goroutine (t.Fatalf); the
// concurrency test below uses csrfFromGET instead, which reports failures
// via a returned error so a spawned goroutine can use t.Errorf.
func fetchWizardCSRF(t *testing.T, client *http.Client, serverURL string) (*http.Response, string) {
	t.Helper()
	resp, err := client.Get(serverURL + "/setup")
	if err != nil {
		t.Fatalf("GET /setup: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	m := csrfRe.FindSubmatch(body)
	if m == nil {
		t.Fatalf("no CSRF token in form:\n%s", body)
	}
	return resp, string(m[1])
}

// csrfFromGET is fetchWizardCSRF's error-returning twin, safe to call from a
// spawned goroutine (which must never call t.Fatal — only the test's own
// goroutine may).
func csrfFromGET(client *http.Client, serverURL string) (string, error) {
	resp, err := client.Get(serverURL + "/setup")
	if err != nil {
		return "", fmt.Errorf("GET /setup: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	m := csrfRe.FindSubmatch(body)
	if m == nil {
		return "", errors.New("no CSRF token in form")
	}
	return string(m[1]), nil
}

// TestWizard_EndToEnd drives the first-run onboarding wizard against a real,
// freshly migrated database: the form serves from a fresh schema, a
// successful submission creates exactly one admin and signs them in, and
// every later visit — GET or POST — is a no-op redirect that leaves the row
// count at 1. This is the automated equivalent of NSTR-19's first three
// acceptance criteria.
func TestWizard_EndToEnd(t *testing.T) {
	server, repo := newWizardServer(t)
	client := newWizardClient(t)

	getResp, csrf := fetchWizardCSRF(t, client, server.URL)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("first GET /setup = %d, want 200 (a fresh database must serve the wizard)", getResp.StatusCode)
	}
	preToken := sessionCookieValue(client, server.URL)
	if preToken == "" {
		t.Fatal("no session cookie after the initial GET /setup")
	}

	postResp, err := client.PostForm(server.URL+"/setup", url.Values{
		"csrf_token":            {csrf},
		"display_name":          {"Maya"},
		"email":                 {"maya@example.com"},
		"password":              {"correct-horse-battery-staple"},
		"password_confirmation": {"correct-horse-battery-staple"},
	})
	if err != nil {
		t.Fatalf("POST /setup: %v", err)
	}
	_ = postResp.Body.Close()
	if postResp.StatusCode != http.StatusSeeOther {
		t.Fatalf("POST /setup = %d, want 303", postResp.StatusCode)
	}
	if postToken := sessionCookieValue(client, server.URL); postToken == "" || postToken == preToken {
		t.Error("POST /setup did not rotate the session token — the new admin must be signed in via a renewed session")
	}

	n, err := repo.Count(testCtx(t))
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 1 {
		t.Fatalf("app_user row count after setup = %d, want 1", n)
	}

	// A second GET must redirect, never render the wizard again.
	secondGet, err := client.Get(server.URL + "/setup")
	if err != nil {
		t.Fatalf("second GET /setup: %v", err)
	}
	_ = secondGet.Body.Close()
	if secondGet.StatusCode != http.StatusSeeOther {
		t.Errorf("second GET /setup = %d, want 303 (setup is already complete)", secondGet.StatusCode)
	}

	// A second POST — even with a stale-but-valid CSRF token from the
	// original session — must also redirect and must not create a second
	// admin under any input.
	secondPost, err := client.PostForm(server.URL+"/setup", url.Values{
		"csrf_token":            {csrf},
		"display_name":          {"Someone Else"},
		"email":                 {"someone-else@example.com"},
		"password":              {"another-correct-horse-battery"},
		"password_confirmation": {"another-correct-horse-battery"},
	})
	if err != nil {
		t.Fatalf("second POST /setup: %v", err)
	}
	_ = secondPost.Body.Close()
	if secondPost.StatusCode != http.StatusSeeOther {
		t.Errorf("second POST /setup = %d, want 303 (revisiting must not create a user under any input)", secondPost.StatusCode)
	}

	n, err = repo.Count(testCtx(t))
	if err != nil {
		t.Fatalf("Count after second POST: %v", err)
	}
	if n != 1 {
		t.Fatalf("app_user row count after a second POST = %d, want still 1", n)
	}
}

// concurrentSubmissionResult is one goroutine's outcome in
// TestWizard_ConcurrentSubmissionsCannotCreateTwoAdmins.
type concurrentSubmissionResult struct {
	status  int
	renewed bool
}

// submitWizardEntry drives one independent client (its own cookie jar, its
// own GET-then-POST) through the wizard with a unique display name/email,
// and reports whether the submission actually signed a user in — its
// session token rotated, which only RenewToken causes (see
// sessionCookieValue's doc for why mere Set-Cookie presence cannot tell a
// winner from a loser). Returns an error instead of calling t.Fatal so a
// spawned goroutine can report it via t.Errorf.
func submitWizardEntry(serverURL string, i int) (concurrentSubmissionResult, error) {
	jar, err := cookiejar.New(nil)
	if err != nil {
		return concurrentSubmissionResult{}, fmt.Errorf("cookiejar.New: %w", err)
	}
	client := &http.Client{
		Jar: jar,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	csrf, err := csrfFromGET(client, serverURL)
	if err != nil {
		return concurrentSubmissionResult{}, err
	}
	preToken := sessionCookieValue(client, serverURL)

	resp, err := client.PostForm(serverURL+"/setup", url.Values{
		"csrf_token":            {csrf},
		"display_name":          {fmt.Sprintf("Admin %d", i)},
		"email":                 {fmt.Sprintf("admin%d@example.com", i)},
		"password":              {"correct-horse-battery-staple"},
		"password_confirmation": {"correct-horse-battery-staple"},
	})
	if err != nil {
		return concurrentSubmissionResult{}, fmt.Errorf("POST /setup: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	postToken := sessionCookieValue(client, serverURL)
	return concurrentSubmissionResult{
		status:  resp.StatusCode,
		renewed: postToken != "" && postToken != preToken,
	}, nil
}

// assertAllRedirected checks that every concurrent submission got the
// wizard's standard 303 (success and lost-race both redirect to /; the
// difference is which one rotated its session — see assertExactlyOneWinner).
func assertAllRedirected(t *testing.T, results []concurrentSubmissionResult) {
	t.Helper()
	for i, r := range results {
		if r.status != http.StatusSeeOther {
			t.Errorf("goroutine %d: POST /setup = %d, want 303", i, r.status)
		}
	}
}

// assertExactlyOneWinner checks that exactly one submission rotated its
// session token — the signal that it, and only it, actually signed a user
// in.
func assertExactlyOneWinner(t *testing.T, results []concurrentSubmissionResult) {
	t.Helper()
	winners := 0
	for _, r := range results {
		if r.renewed {
			winners++
		}
	}
	if winners != 1 {
		t.Errorf("submissions that rotated their session token (signed in) = %d, want exactly 1 out of %d concurrent POSTs", winners, len(results))
	}
}

// TestWizard_ConcurrentSubmissionsCannotCreateTwoAdmins fires N simultaneous
// wizard submissions — each its own session, its own CSRF token, its own
// client — at the handler over one real pool, and asserts exactly one
// app_user row results and exactly one response actually signed a user in.
// This is the test the advisory-lock transaction in
// Provisioner.CreateFirstAdmin exists for.
func TestWizard_ConcurrentSubmissionsCannotCreateTwoAdmins(t *testing.T) {
	const n = 10
	server, repo := newWizardServer(t)

	results := make([]concurrentSubmissionResult, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := submitWizardEntry(server.URL, i)
			if err != nil {
				t.Errorf("goroutine %d: %v", i, err)
				return
			}
			results[i] = result
		}(i)
	}
	wg.Wait()

	assertAllRedirected(t, results)
	assertExactlyOneWinner(t, results)

	got, err := repo.Count(testCtx(t))
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if got != 1 {
		t.Fatalf("app_user row count after %d concurrent submissions = %d, want 1", n, got)
	}
}
