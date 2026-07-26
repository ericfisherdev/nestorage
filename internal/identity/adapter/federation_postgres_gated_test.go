package adapter_test

import (
	"errors"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ericfisherdev/nestorage/internal/identity/adapter"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/platform/db/dbtest"
)

// federationFixture wires the federation repositories and the
// FederationProvisioner over ONE derived database — NewIsolatedPool must be
// called exactly once per test (see deviceTokenFixture's own doc,
// device_token_postgres_gated_test.go), so every gated test in this file
// shares the "identity" suffix every other identity gated test uses, not a
// suffix of its own.
type federationFixture struct {
	pool        *pgxpool.Pool
	users       *adapter.UserRepository
	links       *adapter.MemberLinkRepository
	bindings    *adapter.HouseholdBindingRepository
	provisioner *adapter.FederationProvisioner
}

func newFederationFixture(t *testing.T) *federationFixture {
	t.Helper()
	pool := dbtest.Harness.NewIsolatedPool(t, "identity")
	return &federationFixture{
		pool:        pool,
		users:       adapter.NewUserRepository(pool),
		links:       adapter.NewMemberLinkRepository(pool),
		bindings:    adapter.NewHouseholdBindingRepository(pool),
		provisioner: adapter.NewFederationProvisioner(pool),
	}
}

// seedUser creates and returns an active, non-admin user directly through
// UserRepository — the "existing Nestorage user" a Link call pairs a member
// with.
func (f *federationFixture) seedUser(t *testing.T, displayName, email string) *domain.User {
	t.Helper()
	u := &domain.User{
		ID: domain.NewUserID(), DisplayName: displayName, Email: email,
		PasswordHash: "unused", Role: domain.RoleMember, Color: domain.ColorIndigo,
	}
	if err := f.users.Create(testCtx(t), u); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return u
}

const testHousehold = "household-1"

func adminProfile(displayName, email string) domain.FederationProfile {
	return domain.FederationProfile{DisplayName: displayName, Email: email, Role: domain.RoleAdmin, Active: true}
}

func memberProfile(displayName, email string) domain.FederationProfile {
	return domain.FederationProfile{DisplayName: displayName, Email: email, Role: domain.RoleMember, Active: true}
}

// TestFederationProvisioner_Upsert_RepeatedPushUpdatesRatherThanInserts is
// AC 1's automated equivalent: a second Upsert call for the SAME member_id
// updates the already-created user instead of inserting a second one.
func TestFederationProvisioner_Upsert_RepeatedPushUpdatesRatherThanInserts(t *testing.T) {
	f := newFederationFixture(t)

	first, created, err := f.provisioner.Upsert(testCtx(t), "member-1", testHousehold, memberProfile("Maya", "maya@example.com"))
	if err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	if !created {
		t.Error("first Upsert: created = false, want true")
	}

	second, created, err := f.provisioner.Upsert(testCtx(t), "member-1", testHousehold, memberProfile("Maya Renamed", "maya@example.com"))
	if err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	if created {
		t.Error("second Upsert (repeat push): created = true, want false (update, not insert)")
	}
	if second.ID != first.ID {
		t.Errorf("second Upsert created a DIFFERENT user (%v), want the same one (%v) updated", second.ID, first.ID)
	}
	if second.DisplayName != "Maya Renamed" {
		t.Errorf("second Upsert DisplayName = %q, want the pushed update to apply", second.DisplayName)
	}

	users, err := f.users.List(testCtx(t))
	if err != nil {
		t.Fatalf("List users: %v", err)
	}
	if len(users) != 1 {
		t.Fatalf("List users returned %d rows, want exactly 1 (no duplicate created by the repeated push)", len(users))
	}

	links, err := f.links.List(testCtx(t))
	if err != nil {
		t.Fatalf("List links: %v", err)
	}
	if len(links) != 1 {
		t.Errorf("List links returned %d rows, want exactly 1", len(links))
	}
}

// TestFederationProvisioner_Link_LeavesUserRowUntouched is AC 3's automated
// equivalent: linking a member to an existing user must not change that
// user's role, color, or any other field.
func TestFederationProvisioner_Link_LeavesUserRowUntouched(t *testing.T) {
	f := newFederationFixture(t)
	original := f.seedUser(t, "Bob", "bob@example.com")

	got, created, err := f.provisioner.Link(testCtx(t), "member-1", testHousehold, original.ID)
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if !created {
		t.Error("Link (first call): created = false, want true")
	}
	if got.ID != original.ID || got.DisplayName != original.DisplayName || got.Email != original.Email ||
		got.Role != original.Role || got.Color != original.Color || got.PasswordHash != original.PasswordHash {
		t.Errorf("Link returned a changed user: got %+v, want %+v unchanged", got, original)
	}

	reloaded, err := f.users.FindByID(testCtx(t), original.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if reloaded.Role != original.Role || reloaded.Color != original.Color || reloaded.DisplayName != original.DisplayName {
		t.Errorf("linking mutated the stored user row: got %+v, want %+v unchanged", reloaded, original)
	}
}

// TestFederationProvisioner_Upsert_CreateStoresEmptyHash is AC 4's automated
// equivalent: a federated create must never carry a usable local password.
func TestFederationProvisioner_Upsert_CreateStoresEmptyHash(t *testing.T) {
	f := newFederationFixture(t)

	u, _, err := f.provisioner.Upsert(testCtx(t), "member-1", testHousehold, memberProfile("Maya", "maya@example.com"))
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	reloaded, err := f.users.FindByID(testCtx(t), u.ID)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if reloaded.PasswordHash != "" {
		t.Errorf("federated create's PasswordHash = %q, want empty (no usable local password)", reloaded.PasswordHash)
	}
}

// TestFederationProvisioner_Link_IdempotentReplay is AC 5's automated
// equivalent for the link operation: calling Link twice with the SAME
// member/user pair succeeds both times, with no second row.
func TestFederationProvisioner_Link_IdempotentReplay(t *testing.T) {
	f := newFederationFixture(t)
	u := f.seedUser(t, "Bob", "bob@example.com")

	if _, created, err := f.provisioner.Link(testCtx(t), "member-1", testHousehold, u.ID); err != nil || !created {
		t.Fatalf("first Link: created=%v err=%v, want true, nil", created, err)
	}
	_, created, err := f.provisioner.Link(testCtx(t), "member-1", testHousehold, u.ID)
	if err != nil {
		t.Fatalf("replayed Link: %v", err)
	}
	if created {
		t.Error("replayed Link: created = true, want false")
	}

	links, err := f.links.List(testCtx(t))
	if err != nil {
		t.Fatalf("List links: %v", err)
	}
	if len(links) != 1 {
		t.Errorf("List links returned %d rows after a replayed Link, want exactly 1", len(links))
	}
}

// TestFederationProvisioner_Upsert_IdempotentReplay is AC 5's automated
// equivalent for the upsert operation.
func TestFederationProvisioner_Upsert_IdempotentReplay(t *testing.T) {
	f := newFederationFixture(t)
	profile := memberProfile("Maya", "maya@example.com")

	first, created, err := f.provisioner.Upsert(testCtx(t), "member-1", testHousehold, profile)
	if err != nil || !created {
		t.Fatalf("first Upsert: created=%v err=%v, want true, nil", created, err)
	}
	second, created, err := f.provisioner.Upsert(testCtx(t), "member-1", testHousehold, profile)
	if err != nil {
		t.Fatalf("replayed Upsert: %v", err)
	}
	if created {
		t.Error("replayed Upsert: created = true, want false")
	}
	if second.ID != first.ID {
		t.Errorf("replayed Upsert returned a different user: %v, want %v", second.ID, first.ID)
	}
}

// TestFederationProvisioner_Link_MemberAlreadyLinkedToDifferentUser is the
// negative case AC 5/6 sit alongside: a member id already linked to one
// user cannot be re-linked to a different one.
func TestFederationProvisioner_Link_MemberAlreadyLinkedToDifferentUser(t *testing.T) {
	f := newFederationFixture(t)
	first := f.seedUser(t, "Bob", "bob@example.com")
	second := f.seedUser(t, "Carol", "carol@example.com")

	if _, _, err := f.provisioner.Link(testCtx(t), "member-1", testHousehold, first.ID); err != nil {
		t.Fatalf("first Link: %v", err)
	}
	_, _, err := f.provisioner.Link(testCtx(t), "member-1", testHousehold, second.ID)
	if !errors.Is(err, domain.ErrMemberAlreadyLinked) {
		t.Fatalf("Link(different user, same member) error = %v, want ErrMemberAlreadyLinked", err)
	}
}

// TestFederationProvisioner_HouseholdMismatchRefusedOnceBound is AC 6's
// automated equivalent: once a binding is recorded, a call naming a
// different household is refused rather than merged.
func TestFederationProvisioner_HouseholdMismatchRefusedOnceBound(t *testing.T) {
	f := newFederationFixture(t)

	if err := f.provisioner.VerifyBinding(testCtx(t), testHousehold); err != nil {
		t.Fatalf("VerifyBinding(first call): %v", err)
	}

	if err := f.provisioner.VerifyBinding(testCtx(t), testHousehold); err != nil {
		t.Errorf("VerifyBinding(same household again): %v, want nil", err)
	}

	if err := f.provisioner.VerifyBinding(testCtx(t), "household-2"); !errors.Is(err, domain.ErrHouseholdMismatch) {
		t.Errorf("VerifyBinding(different household) error = %v, want ErrHouseholdMismatch", err)
	}

	// A refused household must not have overwritten the binding.
	binding, found, err := f.bindings.Get(testCtx(t))
	if err != nil {
		t.Fatalf("Get binding: %v", err)
	}
	if !found || binding.HouseholdID != testHousehold {
		t.Errorf("binding = (%+v, %v), want (%q, true) unchanged", binding, found, testHousehold)
	}
}

// TestFederationProvisioner_Upsert_HouseholdMismatchRefused asserts the same
// binding rule applies to the write path, not only VerifyBinding.
func TestFederationProvisioner_Upsert_HouseholdMismatchRefused(t *testing.T) {
	f := newFederationFixture(t)
	if _, _, err := f.provisioner.Upsert(testCtx(t), "member-1", testHousehold, memberProfile("Maya", "maya@example.com")); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}

	_, _, err := f.provisioner.Upsert(testCtx(t), "member-2", "household-2", memberProfile("Bob", "bob@example.com"))
	if !errors.Is(err, domain.ErrHouseholdMismatch) {
		t.Fatalf("Upsert(different household) error = %v, want ErrHouseholdMismatch", err)
	}

	users, err := f.users.List(testCtx(t))
	if err != nil {
		t.Fatalf("List users: %v", err)
	}
	if len(users) != 1 {
		t.Errorf("List users returned %d rows after a refused cross-household Upsert, want exactly 1 (no partial write)", len(users))
	}
}

// TestFederationProvisioner_Upsert_DemotingLastAdminRejected asserts the
// last-active-admin invariant survives a federation push: SetRole/SetActive
// (not Update's own role/color columns) are what a profile change goes
// through, so ErrLastActiveAdmin still surfaces.
func TestFederationProvisioner_Upsert_DemotingLastAdminRejected(t *testing.T) {
	f := newFederationFixture(t)
	if _, _, err := f.provisioner.Upsert(testCtx(t), "member-1", testHousehold, adminProfile("Admin", "admin@example.com")); err != nil {
		t.Fatalf("create admin: %v", err)
	}

	_, _, err := f.provisioner.Upsert(testCtx(t), "member-1", testHousehold, memberProfile("Admin", "admin@example.com"))
	if !errors.Is(err, domain.ErrLastActiveAdmin) {
		t.Fatalf("Upsert(demote last admin) error = %v, want ErrLastActiveAdmin", err)
	}
}

// TestFederationProvisioner_Upsert_ConcurrentPushesSerializeOnAdvisoryLock
// fires N simultaneous Upsert calls for the SAME member id and asserts
// exactly one user row results — the test the federationAdvisoryLock
// transaction exists for (mirrors
// TestWizard_ConcurrentSubmissionsCannotCreateTwoAdmins's identical
// rationale, onboarding_gated_test.go).
func TestFederationProvisioner_Upsert_ConcurrentPushesSerializeOnAdvisoryLock(t *testing.T) {
	const n = 10
	f := newFederationFixture(t)
	profile := memberProfile("Maya", "maya@example.com")

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := range n {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, _, err := f.provisioner.Upsert(testCtx(t), "member-1", testHousehold, profile)
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: Upsert error = %v, want nil", i, err)
		}
	}

	users, err := f.users.List(testCtx(t))
	if err != nil {
		t.Fatalf("List users: %v", err)
	}
	if len(users) != 1 {
		t.Errorf("List users returned %d rows after %d concurrent pushes for the same member, want exactly 1", len(users), n)
	}
	links, err := f.links.List(testCtx(t))
	if err != nil {
		t.Fatalf("List links: %v", err)
	}
	if len(links) != 1 {
		t.Errorf("List links returned %d rows after %d concurrent pushes, want exactly 1", len(links), n)
	}
}

func TestNewFederationProvisioner_NilPoolPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewFederationProvisioner(nil) did not panic")
		}
	}()
	adapter.NewFederationProvisioner(nil)
}

func TestNewMemberLinkRepository_NilExecutorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewMemberLinkRepository(nil) did not panic")
		}
	}()
	adapter.NewMemberLinkRepository(nil)
}

func TestNewHouseholdBindingRepository_NilExecutorPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("NewHouseholdBindingRepository(nil) did not panic")
		}
	}()
	adapter.NewHouseholdBindingRepository(nil)
}

func TestHouseholdBindingRepository_Get_EmptyIsNotFound(t *testing.T) {
	f := newFederationFixture(t)
	_, found, err := f.bindings.Get(testCtx(t))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if found {
		t.Error("Get(no binding recorded yet) found = true, want false")
	}
}

func TestMemberLinkRepository_FindByMemberID_UnknownReturnsNotFound(t *testing.T) {
	f := newFederationFixture(t)
	_, err := f.links.FindByMemberID(testCtx(t), "nonexistent-member")
	if !errors.Is(err, domain.ErrMemberLinkNotFound) {
		t.Fatalf("FindByMemberID(unknown) error = %v, want ErrMemberLinkNotFound", err)
	}
}
