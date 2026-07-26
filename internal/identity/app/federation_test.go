package app_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/ericfisherdev/nestorage/internal/identity/app"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

func federationTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// fakeFederationUsers is an in-memory federationUserLister.
type fakeFederationUsers struct {
	users []domain.User
	err   error
}

func (f *fakeFederationUsers) List(context.Context) ([]domain.User, error) {
	return f.users, f.err
}

// fakeFederationLinks is an in-memory federationLinkLister.
type fakeFederationLinks struct {
	links []domain.MemberLink
	err   error
}

func (f *fakeFederationLinks) List(context.Context) ([]domain.MemberLink, error) {
	return f.links, f.err
}

// fakeFederationProvisioner is a recording federationProvisioner double.
type fakeFederationProvisioner struct {
	verifyBindingErr error

	linkUser    *domain.User
	linkCreated bool
	linkErr     error

	upsertUser    *domain.User
	upsertCreated bool
	upsertErr     error

	gotLinkMemberID, gotLinkHouseholdID     string
	gotLinkUserID                           domain.UserID
	gotUpsertMemberID, gotUpsertHouseholdID string
	gotUpsertProfile                        domain.FederationProfile
}

func (f *fakeFederationProvisioner) VerifyBinding(context.Context, string) error {
	return f.verifyBindingErr
}

func (f *fakeFederationProvisioner) Link(_ context.Context, memberID, householdID string, userID domain.UserID) (*domain.User, bool, error) {
	f.gotLinkMemberID, f.gotLinkHouseholdID, f.gotLinkUserID = memberID, householdID, userID
	return f.linkUser, f.linkCreated, f.linkErr
}

func (f *fakeFederationProvisioner) Upsert(_ context.Context, memberID, householdID string, profile domain.FederationProfile) (*domain.User, bool, error) {
	f.gotUpsertMemberID, f.gotUpsertHouseholdID, f.gotUpsertProfile = memberID, householdID, profile
	return f.upsertUser, f.upsertCreated, f.upsertErr
}

func TestFederationService_Accounts_JoinsUsersAndLinks(t *testing.T) {
	t.Parallel()
	linkedID := domain.NewUserID()
	unlinkedID := domain.NewUserID()
	users := &fakeFederationUsers{users: []domain.User{{ID: linkedID}, {ID: unlinkedID}}}
	links := &fakeFederationLinks{links: []domain.MemberLink{{UserID: linkedID, MemberID: "member-1"}}}
	provisioner := &fakeFederationProvisioner{}
	svc := app.NewFederationService(users, links, provisioner, federationTestLogger())

	got, err := svc.Accounts(context.Background(), "household-1")
	if err != nil {
		t.Fatalf("Accounts: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("Accounts returned %d entries, want 2", len(got))
	}
	for _, a := range got {
		switch a.User.ID {
		case linkedID:
			if a.MemberID == nil || *a.MemberID != "member-1" {
				t.Errorf("linked user's MemberID = %v, want \"member-1\"", a.MemberID)
			}
		case unlinkedID:
			if a.MemberID != nil {
				t.Errorf("unlinked user's MemberID = %v, want nil", *a.MemberID)
			}
		}
	}
}

func TestFederationService_Accounts_InvalidHouseholdID(t *testing.T) {
	t.Parallel()
	svc := app.NewFederationService(&fakeFederationUsers{}, &fakeFederationLinks{}, &fakeFederationProvisioner{}, federationTestLogger())

	_, err := svc.Accounts(context.Background(), "")
	if !errors.Is(err, domain.ErrInvalidMemberLink) {
		t.Errorf("Accounts(blank household) error = %v, want wrapped ErrInvalidMemberLink", err)
	}
}

func TestFederationService_Accounts_HouseholdMismatchPropagates(t *testing.T) {
	t.Parallel()
	provisioner := &fakeFederationProvisioner{verifyBindingErr: domain.ErrHouseholdMismatch}
	svc := app.NewFederationService(&fakeFederationUsers{}, &fakeFederationLinks{}, provisioner, federationTestLogger())

	_, err := svc.Accounts(context.Background(), "household-1")
	if !errors.Is(err, domain.ErrHouseholdMismatch) {
		t.Errorf("Accounts error = %v, want ErrHouseholdMismatch", err)
	}
}

func TestFederationService_Link_ValidatesBeforeDelegating(t *testing.T) {
	t.Parallel()
	provisioner := &fakeFederationProvisioner{}
	svc := app.NewFederationService(&fakeFederationUsers{}, &fakeFederationLinks{}, provisioner, federationTestLogger())

	_, _, err := svc.Link(context.Background(), "", "household-1", domain.NewUserID())
	if !errors.Is(err, domain.ErrInvalidMemberLink) {
		t.Errorf("Link(blank member id) error = %v, want wrapped ErrInvalidMemberLink", err)
	}
	if provisioner.gotLinkMemberID != "" {
		t.Error("Link must not delegate to the provisioner when validation fails")
	}
}

func TestFederationService_Link_DelegatesAndReturnsCreated(t *testing.T) {
	t.Parallel()
	userID := domain.NewUserID()
	want := &domain.User{ID: userID}
	provisioner := &fakeFederationProvisioner{linkUser: want, linkCreated: true}
	svc := app.NewFederationService(&fakeFederationUsers{}, &fakeFederationLinks{}, provisioner, federationTestLogger())

	got, created, err := svc.Link(context.Background(), "member-1", "household-1", userID)
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	if got != want || !created {
		t.Errorf("Link = (%v, %v), want (%v, true)", got, created, want)
	}
	if provisioner.gotLinkMemberID != "member-1" || provisioner.gotLinkHouseholdID != "household-1" || provisioner.gotLinkUserID != userID {
		t.Errorf("Link did not forward its arguments to the provisioner unchanged")
	}
}

func TestFederationService_Upsert_ValidatesBeforeDelegating(t *testing.T) {
	t.Parallel()
	provisioner := &fakeFederationProvisioner{}
	svc := app.NewFederationService(&fakeFederationUsers{}, &fakeFederationLinks{}, provisioner, federationTestLogger())

	_, _, err := svc.Upsert(context.Background(), "member-1", "", domain.FederationProfile{Role: domain.RoleMember})
	if !errors.Is(err, domain.ErrInvalidMemberLink) {
		t.Errorf("Upsert(blank household id) error = %v, want wrapped ErrInvalidMemberLink", err)
	}
	if provisioner.gotUpsertMemberID != "" {
		t.Error("Upsert must not delegate to the provisioner when validation fails")
	}
}

func TestFederationService_Upsert_DelegatesAndReturnsCreated(t *testing.T) {
	t.Parallel()
	want := &domain.User{ID: domain.NewUserID()}
	profile := domain.FederationProfile{DisplayName: "Maya", Email: "maya@example.com", Role: domain.RoleMember, Active: true}
	provisioner := &fakeFederationProvisioner{upsertUser: want, upsertCreated: true}
	svc := app.NewFederationService(&fakeFederationUsers{}, &fakeFederationLinks{}, provisioner, federationTestLogger())

	got, created, err := svc.Upsert(context.Background(), "member-1", "household-1", profile)
	if err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if got != want || !created {
		t.Errorf("Upsert = (%v, %v), want (%v, true)", got, created, want)
	}
	if provisioner.gotUpsertProfile != profile {
		t.Errorf("Upsert profile = %+v, want %+v", provisioner.gotUpsertProfile, profile)
	}
}

func TestNewFederationService_NilDependenciesPanic(t *testing.T) {
	t.Parallel()
	users := &fakeFederationUsers{}
	links := &fakeFederationLinks{}
	provisioner := &fakeFederationProvisioner{}
	logger := federationTestLogger()

	cases := []struct {
		name string
		fn   func()
	}{
		{"nil users", func() { app.NewFederationService(nil, links, provisioner, logger) }},
		{"nil links", func() { app.NewFederationService(users, nil, provisioner, logger) }},
		{"nil provisioner", func() { app.NewFederationService(users, links, nil, logger) }},
		{"nil logger", func() { app.NewFederationService(users, links, provisioner, nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Errorf("NewFederationService(%s) did not panic", tc.name)
				}
			}()
			tc.fn()
		})
	}
}
