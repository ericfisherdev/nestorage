package app_test

import (
	"context"
	"errors"
	"testing"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/notify/app"
	"github.com/ericfisherdev/nestorage/internal/notify/domain"
)

// testLogger is declared in notifier_test.go (same package) and reused
// here — one shared no-op logger per package, not one per file.

// fakePreferenceRepository is a configurable in-memory domain.PreferenceRepository,
// keyed the same way the real table's primary key is: (userID, eventType, channel).
type fakePreferenceRepository struct {
	rows map[fakePreferenceKey]domain.Preference

	listErr, getErr, upsertErr error
}

type fakePreferenceKey struct {
	userID    identity.UserID
	eventType domain.EventType
	channel   domain.Channel
}

func newFakePreferenceRepository() *fakePreferenceRepository {
	return &fakePreferenceRepository{rows: make(map[fakePreferenceKey]domain.Preference)}
}

func (f *fakePreferenceRepository) ListForUser(_ context.Context, userID identity.UserID) ([]domain.Preference, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var out []domain.Preference
	for k, v := range f.rows {
		if k.userID == userID {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakePreferenceRepository) Get(_ context.Context, userID identity.UserID, eventType domain.EventType) ([]domain.Preference, error) {
	if f.getErr != nil {
		return nil, f.getErr
	}
	var out []domain.Preference
	for k, v := range f.rows {
		if k.userID == userID && k.eventType == eventType {
			out = append(out, v)
		}
	}
	return out, nil
}

func (f *fakePreferenceRepository) Upsert(_ context.Context, p domain.Preference) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.rows[fakePreferenceKey{userID: p.UserID, eventType: p.EventType, channel: p.Channel}] = p
	return nil
}

func TestNewPreferenceService_NilDependenciesPanic(t *testing.T) {
	repo := newFakePreferenceRepository()
	tests := []struct {
		name string
		fn   func()
	}{
		{"nil repo", func() { app.NewPreferenceService(nil, testLogger()) }},
		{"nil logger", func() { app.NewPreferenceService(repo, nil) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewPreferenceService(%s) did not panic", tt.name)
				}
			}()
			tt.fn()
		})
	}
}

// TestPreferenceService_ChannelsFor_NoStoredRows_ReturnsInAppOnly is the
// "new users get the documented defaults with zero configuration"
// acceptance criterion, and the "ChannelsFor never returns an empty slice"
// one, exercised together at the service layer (domain.EffectiveChannels'
// own unit tests cover the merge logic itself).
func TestPreferenceService_ChannelsFor_NoStoredRows_ReturnsInAppOnly(t *testing.T) {
	repo := newFakePreferenceRepository()
	svc := app.NewPreferenceService(repo, testLogger())
	userID := identity.NewUserID()

	got, err := svc.ChannelsFor(context.Background(), userID, domain.EventTypeReturnRequested)
	if err != nil {
		t.Fatalf("ChannelsFor: %v", err)
	}
	if len(got) != 1 || got[0] != domain.ChannelInApp {
		t.Errorf("ChannelsFor(no stored rows) = %v, want exactly [in_app]", got)
	}
}

// TestPreferenceService_SetEmailEnabled_TakesEffectOnNextCall is the
// "preference changes take effect on the next notification with no
// restart" acceptance criterion: ChannelsFor is consulted fresh, with no
// cache, so a SetEmailEnabled(true) is visible on the very next call.
func TestPreferenceService_SetEmailEnabled_TakesEffectOnNextCall(t *testing.T) {
	repo := newFakePreferenceRepository()
	svc := app.NewPreferenceService(repo, testLogger())
	userID := identity.NewUserID()
	ctx := context.Background()

	if err := svc.SetEmailEnabled(ctx, userID, domain.EventTypeReturnRequested, true); err != nil {
		t.Fatalf("SetEmailEnabled(true): %v", err)
	}

	got, err := svc.ChannelsFor(ctx, userID, domain.EventTypeReturnRequested)
	if err != nil {
		t.Fatalf("ChannelsFor: %v", err)
	}
	want := []domain.Channel{domain.ChannelInApp, domain.ChannelEmail}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ChannelsFor after SetEmailEnabled(true) = %v, want %v", got, want)
	}
}

// TestPreferenceService_SetEmailEnabled_FalseRemovesEmail proves disabling
// email keeps the recipient reachable via in_app rather than silencing
// them entirely — "disabling email for return requests stops those emails
// and keeps the in-app notification".
func TestPreferenceService_SetEmailEnabled_FalseRemovesEmail(t *testing.T) {
	repo := newFakePreferenceRepository()
	svc := app.NewPreferenceService(repo, testLogger())
	userID := identity.NewUserID()
	ctx := context.Background()

	if err := svc.SetEmailEnabled(ctx, userID, domain.EventTypeReturnRequested, true); err != nil {
		t.Fatalf("SetEmailEnabled(true): %v", err)
	}
	if err := svc.SetEmailEnabled(ctx, userID, domain.EventTypeReturnRequested, false); err != nil {
		t.Fatalf("SetEmailEnabled(false): %v", err)
	}

	got, err := svc.ChannelsFor(ctx, userID, domain.EventTypeReturnRequested)
	if err != nil {
		t.Fatalf("ChannelsFor: %v", err)
	}
	if len(got) != 1 || got[0] != domain.ChannelInApp {
		t.Errorf("ChannelsFor after SetEmailEnabled(false) = %v, want exactly [in_app] (never silenced)", got)
	}
}

func TestPreferenceService_SetEmailEnabled_InvalidEventTypeRejected(t *testing.T) {
	repo := newFakePreferenceRepository()
	svc := app.NewPreferenceService(repo, testLogger())

	err := svc.SetEmailEnabled(context.Background(), identity.NewUserID(), domain.EventType("task_due_soon"), true)
	if !errors.Is(err, domain.ErrInvalidEventType) {
		t.Errorf("SetEmailEnabled(invalid event type) = %v, want ErrInvalidEventType", err)
	}
	if len(repo.rows) != 0 {
		t.Error("an invalid event type must not reach the repository")
	}
}

func TestPreferenceService_ChannelsFor_RepositoryErrorWrapped(t *testing.T) {
	repo := newFakePreferenceRepository()
	repo.getErr = errors.New("boom")
	svc := app.NewPreferenceService(repo, testLogger())

	_, err := svc.ChannelsFor(context.Background(), identity.NewUserID(), domain.EventTypeReturnRequested)
	if err == nil {
		t.Fatal("ChannelsFor with a failing repository = nil error, want the failure surfaced")
	}
}

// TestPreferenceService_PreferencesForUser_DefaultsMergedMatrix backs the
// settings screen's GET: every known event type appears, InApp always
// true, EmailEnabled reflecting only what is actually stored.
func TestPreferenceService_PreferencesForUser_DefaultsMergedMatrix(t *testing.T) {
	repo := newFakePreferenceRepository()
	svc := app.NewPreferenceService(repo, testLogger())
	userID := identity.NewUserID()
	ctx := context.Background()

	if err := svc.SetEmailEnabled(ctx, userID, domain.EventTypeReturnRequested, true); err != nil {
		t.Fatalf("SetEmailEnabled: %v", err)
	}

	sections, err := svc.PreferencesForUser(ctx, userID)
	if err != nil {
		t.Fatalf("PreferencesForUser: %v", err)
	}
	if len(sections) != 2 {
		t.Fatalf("PreferencesForUser returned %d sections, want 2 (one per known event type)", len(sections))
	}
	byType := make(map[domain.EventType]app.EventTypeSection, len(sections))
	for _, s := range sections {
		if !s.InApp {
			t.Errorf("section %v: InApp = false, want true (always on)", s.EventType)
		}
		byType[s.EventType] = s
	}
	if !byType[domain.EventTypeReturnRequested].EmailEnabled {
		t.Error("return_requested section: EmailEnabled = false, want true (was set above)")
	}
	if byType[domain.EventTypeItemReturned].EmailEnabled {
		t.Error("item_returned section: EmailEnabled = true, want false (default, never set)")
	}
}

// TestPreferenceService_PreferencesForUser_NoRows_AllEmailDisabled is the
// "new users get the documented defaults... with zero configuration"
// acceptance criterion, exercised at the settings-screen read path.
func TestPreferenceService_PreferencesForUser_NoRows_AllEmailDisabled(t *testing.T) {
	repo := newFakePreferenceRepository()
	svc := app.NewPreferenceService(repo, testLogger())

	sections, err := svc.PreferencesForUser(context.Background(), identity.NewUserID())
	if err != nil {
		t.Fatalf("PreferencesForUser: %v", err)
	}
	for _, s := range sections {
		if !s.InApp {
			t.Errorf("section %v: InApp = false, want true", s.EventType)
		}
		if s.EmailEnabled {
			t.Errorf("section %v: EmailEnabled = true, want false (a brand new user has zero stored rows)", s.EventType)
		}
	}
}

func TestPreferenceService_PreferencesForUser_RepositoryErrorWrapped(t *testing.T) {
	repo := newFakePreferenceRepository()
	repo.listErr = errors.New("boom")
	svc := app.NewPreferenceService(repo, testLogger())

	_, err := svc.PreferencesForUser(context.Background(), identity.NewUserID())
	if err == nil {
		t.Fatal("PreferencesForUser with a failing repository = nil error, want the failure surfaced")
	}
}

func TestPreferenceService_SetEmailEnabled_RepositoryErrorWrapped(t *testing.T) {
	repo := newFakePreferenceRepository()
	repo.upsertErr = errors.New("boom")
	svc := app.NewPreferenceService(repo, testLogger())

	err := svc.SetEmailEnabled(context.Background(), identity.NewUserID(), domain.EventTypeReturnRequested, true)
	if err == nil {
		t.Fatal("SetEmailEnabled with a failing repository = nil error, want the failure surfaced")
	}
}
