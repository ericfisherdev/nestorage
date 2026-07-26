package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// federationUserLister is the narrow port (ISP) FederationService depends
// on for Accounts' own join, satisfied by domain.UserRepository (a
// superset) and by test fakes.
type federationUserLister interface {
	List(ctx context.Context) ([]domain.User, error)
}

// federationLinkLister is the narrow port (ISP) FederationService depends
// on for Accounts' own join, satisfied by domain.MemberLinkRepository (a
// superset) and by test fakes.
type federationLinkLister interface {
	List(ctx context.Context) ([]domain.MemberLink, error)
}

// federationProvisioner is the narrow port (ISP) FederationService depends
// on for every federation write plus the account-read's own binding
// pre-flight, satisfied by *adapter.FederationProvisioner and by test
// fakes.
type federationProvisioner interface {
	VerifyBinding(ctx context.Context, householdID string) error
	Link(ctx context.Context, memberID, householdID string, userID domain.UserID) (*domain.User, bool, error)
	Upsert(ctx context.Context, memberID, householdID string, profile domain.FederationProfile) (*domain.User, bool, error)
}

// FederationAccount pairs a domain.User with its linked Nestova member id —
// nil when unlinked — the shape Accounts' own join produces for the
// account-read endpoint.
type FederationAccount struct {
	User     domain.User
	MemberID *string
}

// FederationService implements NSTR-101's federation surface: the
// account-read reconciliation feeds on, and the link/provision writes
// NSTR-106/107/108's attach and re-push calls drive.
type FederationService struct {
	users       federationUserLister
	links       federationLinkLister
	provisioner federationProvisioner
	logger      *slog.Logger
}

// NewFederationService constructs FederationService. All dependencies are
// required; a missing one panics at construction time, matching every other
// constructor in this codebase (see NewAdminService).
func NewFederationService(users federationUserLister, links federationLinkLister, provisioner federationProvisioner, logger *slog.Logger) *FederationService {
	if users == nil {
		panic("identity/app: NewFederationService requires a non-nil federationUserLister")
	}
	if links == nil {
		panic("identity/app: NewFederationService requires a non-nil federationLinkLister")
	}
	if provisioner == nil {
		panic("identity/app: NewFederationService requires a non-nil federationProvisioner")
	}
	if logger == nil {
		panic("identity/app: NewFederationService requires a non-nil logger")
	}
	return &FederationService{users: users, links: links, provisioner: provisioner, logger: logger}
}

// Accounts returns every user paired with its linked member id (nil when
// unlinked), after verifying — and, on the very first call, recording — the
// household binding (see federationProvisioner.VerifyBinding's own doc).
// Returns a wrapped domain.ErrInvalidMemberLink for a malformed
// householdID, or domain.ErrHouseholdMismatch once a different household is
// already bound.
func (s *FederationService) Accounts(ctx context.Context, householdID string) ([]FederationAccount, error) {
	if err := domain.ValidateHouseholdID(householdID); err != nil {
		return nil, err
	}
	if err := s.provisioner.VerifyBinding(ctx, householdID); err != nil {
		return nil, err
	}

	users, err := s.users.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("federation: accounts: list users: %w", err)
	}
	links, err := s.links.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("federation: accounts: list links: %w", err)
	}

	byUser := make(map[domain.UserID]string, len(links))
	for _, l := range links {
		byUser[l.UserID] = l.MemberID
	}
	accounts := make([]FederationAccount, 0, len(users))
	for _, u := range users {
		account := FederationAccount{User: u}
		if memberID, ok := byUser[u.ID]; ok {
			account.MemberID = &memberID
		}
		accounts = append(accounts, account)
	}
	return accounts, nil
}

// Link records that memberID (within householdID) names the existing user
// userID. created reports whether a new link row was inserted (see
// FederationProvisioner.Link's own doc for the full contract). Returns a
// wrapped domain.ErrInvalidMemberLink for a malformed memberID/householdID.
func (s *FederationService) Link(ctx context.Context, memberID, householdID string, userID domain.UserID) (*domain.User, bool, error) {
	if err := domain.ValidateMemberID(memberID); err != nil {
		return nil, false, err
	}
	if err := domain.ValidateHouseholdID(householdID); err != nil {
		return nil, false, err
	}
	u, created, err := s.provisioner.Link(ctx, memberID, householdID, userID)
	if err != nil {
		return nil, false, err
	}
	s.logAction(ctx, "member linked", memberID, u.ID)
	return u, created, nil
}

// Upsert creates or updates the federated user named by memberID (see
// FederationProvisioner.Upsert's own doc for the full contract). profile.Role
// must already be a parsed domain.Role — ParseRole is the handler's own job.
// Returns a wrapped domain.ErrInvalidMemberLink for a malformed
// memberID/householdID.
func (s *FederationService) Upsert(ctx context.Context, memberID, householdID string, profile domain.FederationProfile) (*domain.User, bool, error) {
	if err := domain.ValidateMemberID(memberID); err != nil {
		return nil, false, err
	}
	if err := domain.ValidateHouseholdID(householdID); err != nil {
		return nil, false, err
	}
	u, created, err := s.provisioner.Upsert(ctx, memberID, householdID, profile)
	if err != nil {
		return nil, false, err
	}
	s.logAction(ctx, "member provisioned", memberID, u.ID)
	return u, created, nil
}

// logAction writes one INFO-level audit line naming the member and user
// ids only — never an email or display name, matching AdminService's own
// PII-free logging convention (admin.go).
func (s *FederationService) logAction(ctx context.Context, msg, memberID string, userID domain.UserID) {
	s.logger.InfoContext(ctx, "federation: "+msg, "member_id", memberID, "user_id", userID.String())
}
