package adapter_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alexedwards/scs/v2"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/session"
)

// adminGatedServer wires Authenticate then RequireAdmin in front of a
// simple 200 handler — the same chain cmd/server/shell.go's newAppRoutes
// builds for the admin route group, minus Postgres. The /seed route stands
// in for a real login, mirroring currentuser_test.go's own
// authenticatedServer/newSeededClient pattern so these tests can drive
// RequireAdmin directly.
func adminGatedServer(t *testing.T, repo *fakeCurrentUserRepo) *httptest.Server {
	t.Helper()
	sm := scs.New()
	authenticate := adapter.Authenticate(sm, repo, testLogger())
	requireAdmin := adapter.RequireAdmin(testLogger())

	mux := http.NewServeMux()
	mux.Handle("GET /admin/users", authenticate(requireAdmin(protectedHandler())))
	mux.HandleFunc("POST /seed", func(w http.ResponseWriter, r *http.Request) {
		sm.Put(r.Context(), session.KeyUserID, r.FormValue("user_id"))
		w.WriteHeader(http.StatusNoContent)
	})

	server := httptest.NewServer(sm.LoadAndSave(mux))
	t.Cleanup(server.Close)
	return server
}

func TestRequireAdmin_Anonymous_Returns403(t *testing.T) {
	server := adminGatedServer(t, &fakeCurrentUserRepo{users: map[domain.UserID]*domain.User{}})

	resp, err := http.Get(server.URL + "/admin/users")
	if err != nil {
		t.Fatalf("GET /admin/users: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
}

func TestRequireAdmin_Member_FullNavigation_Returns403WithRenderedPage(t *testing.T) {
	userID := domain.NewUserID()
	repo := &fakeCurrentUserRepo{users: map[domain.UserID]*domain.User{
		userID: {ID: userID, Active: true, Role: domain.RoleMember},
	}}
	server := adminGatedServer(t, repo)
	client := newSeededClient(t, server, userID)

	resp, err := client.Get(server.URL + "/admin/users")
	if err != nil {
		t.Fatalf("GET /admin/users: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d:\n%s", resp.StatusCode, http.StatusForbidden, body)
	}
	if !strings.Contains(strings.ToLower(string(body)), "<!doctype html>") {
		t.Error("a full-navigation request must get the rendered forbidden page, not a bare error")
	}
}

func TestRequireAdmin_Member_HTMXRequest_Returns403Bare(t *testing.T) {
	userID := domain.NewUserID()
	repo := &fakeCurrentUserRepo{users: map[domain.UserID]*domain.User{
		userID: {ID: userID, Active: true, Role: domain.RoleMember},
	}}
	server := adminGatedServer(t, repo)
	client := newSeededClient(t, server, userID)

	req, err := http.NewRequest(http.MethodGet, server.URL+"/admin/users", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("HX-Request", "true")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET /admin/users (HTMX): %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusForbidden)
	}
	if strings.Contains(strings.ToLower(string(body)), "<!doctype") {
		t.Error("an HTMX request must get a bare 403, not the rendered forbidden page")
	}
}

func TestRequireAdmin_Admin_PassesThrough(t *testing.T) {
	userID := domain.NewUserID()
	repo := &fakeCurrentUserRepo{users: map[domain.UserID]*domain.User{
		userID: {ID: userID, Active: true, Role: domain.RoleAdmin},
	}}
	server := adminGatedServer(t, repo)
	client := newSeededClient(t, server, userID)

	resp, err := client.Get(server.URL + "/admin/users")
	if err != nil {
		t.Fatalf("GET /admin/users: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want %d (an admin must pass)", resp.StatusCode, http.StatusOK)
	}
}
