package adapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// federationAdvisoryLock is the fixed-key transaction-scoped advisory lock
// serializing every federation write (Link, Upsert, and VerifyBinding's own
// record-on-first-call) against every other one — bytes spell "NSTR FED",
// mirroring firstAdminAdvisoryLock's identical "ASCII bytes, human
// readable, top bit 0 so it fits int64" rationale (provision.go). A single
// fixed key, not one per member: two concurrent pushes for two DIFFERENT
// members must still not double-record the household binding or race two
// link inserts, which a per-member lock would not serialize against each
// other.
const federationAdvisoryLock int64 = 0x4E53545220464544

// FederationProvisioner performs every federation write (NSTR-101) as one
// atomic, race-proof operation: a transaction-scoped advisory lock makes
// the household-binding check and the member-link lookup/create/update it
// decides on all atomic across connections — see checkBinding's own doc for
// the binding rule shared by every method here.
type FederationProvisioner struct {
	pool *pgxpool.Pool
}

// NewFederationProvisioner constructs a FederationProvisioner over the
// shared pool. Panics on a nil pool, matching NewProvisioner's own
// fail-fast-at-composition rationale.
func NewFederationProvisioner(pool *pgxpool.Pool) *FederationProvisioner {
	if pool == nil {
		panic("identity/adapter: NewFederationProvisioner requires a non-nil pool")
	}
	return &FederationProvisioner{pool: pool}
}

// VerifyBinding checks (and, on the very first call, records) the household
// binding for householdID — the account-read endpoint's own pre-flight,
// sharing checkBinding's exact rule with Link and Upsert rather than
// duplicating it. Returns domain.ErrHouseholdMismatch once a different
// household is already bound.
func (p *FederationProvisioner) VerifyBinding(ctx context.Context, householdID string) error {
	return p.withBindingCheckedTx(ctx, householdID, func(context.Context, pgx.Tx) error { return nil })
}

// Link records that memberID (within householdID) names the existing user
// userID — creating the link row only; the user row itself is never
// touched, so its UserID, role, color, and every attributed bin/item/photo/
// history event stay exactly as they were (AC 3). Returns the user's own
// current state either way, so the caller always has a full account to
// answer with, and created reports whether a new link row was inserted
// (false on an idempotent replay to the SAME user — AC 5). An existing link
// to a DIFFERENT user returns domain.ErrMemberAlreadyLinked. Returns
// domain.ErrHouseholdMismatch when a binding is already recorded for a
// different household (AC 6).
func (p *FederationProvisioner) Link(ctx context.Context, memberID, householdID string, userID domain.UserID) (user *domain.User, created bool, err error) {
	txErr := p.withBindingCheckedTx(ctx, householdID, func(ctx context.Context, tx pgx.Tx) error {
		users := NewUserRepository(tx)
		links := NewMemberLinkRepository(tx)

		u, findErr := users.FindByID(ctx, userID)
		if findErr != nil {
			return fmt.Errorf("federation: link: find user: %w", findErr)
		}

		existing, linkErr := links.FindByMemberID(ctx, memberID)
		switch {
		case linkErr == nil:
			if existing.UserID != userID {
				return domain.ErrMemberAlreadyLinked
			}
			// Already linked to this exact user: succeed unchanged.
		case errors.Is(linkErr, domain.ErrMemberLinkNotFound):
			link := &domain.MemberLink{
				ID:          domain.NewMemberLinkID(),
				UserID:      userID,
				MemberID:    memberID,
				HouseholdID: householdID,
			}
			if createErr := links.Create(ctx, link); createErr != nil {
				return fmt.Errorf("federation: link: create: %w", createErr)
			}
			created = true
		default:
			return fmt.Errorf("federation: link: find existing link: %w", linkErr)
		}

		user = u
		return nil
	})
	if txErr != nil {
		return nil, false, txErr
	}
	return user, created, nil
}

// Upsert creates or updates the federated user named by memberID: with an
// existing link, it updates that linked user's profile (AC 5's "create
// resolves to a link" case — display name and email only, Role/Active only
// when they actually differ, through SetRole/SetActive so
// domain.ErrLastActiveAdmin can never be bypassed by a push); with no
// link, it creates a fresh federated user (empty password hash — AC 4 — and
// a color from domain.NextFederatedColor) plus its link. created reports
// which branch ran. Returns domain.ErrHouseholdMismatch the same way Link
// does.
func (p *FederationProvisioner) Upsert(ctx context.Context, memberID, householdID string, profile domain.FederationProfile) (user *domain.User, created bool, err error) {
	txErr := p.withBindingCheckedTx(ctx, householdID, func(ctx context.Context, tx pgx.Tx) error {
		users := NewUserRepository(tx)
		links := NewMemberLinkRepository(tx)

		existing, linkErr := links.FindByMemberID(ctx, memberID)
		switch {
		case linkErr == nil:
			u, findErr := users.FindByID(ctx, existing.UserID)
			if findErr != nil {
				return fmt.Errorf("federation: upsert: find linked user: %w", findErr)
			}
			if err := applyProfile(ctx, users, u, profile); err != nil {
				return err
			}
			user = u
			return nil
		case errors.Is(linkErr, domain.ErrMemberLinkNotFound):
			newUser, createErr := createFederatedUser(ctx, users, profile)
			if createErr != nil {
				return createErr
			}
			link := &domain.MemberLink{
				ID:          domain.NewMemberLinkID(),
				UserID:      newUser.ID,
				MemberID:    memberID,
				HouseholdID: householdID,
			}
			if err := links.Create(ctx, link); err != nil {
				return fmt.Errorf("federation: upsert: create link: %w", err)
			}
			user = newUser
			created = true
			return nil
		default:
			return fmt.Errorf("federation: upsert: find existing link: %w", linkErr)
		}
	})
	if txErr != nil {
		return nil, false, txErr
	}
	return user, created, nil
}

// applyProfile updates u in place to profile's display name and email
// (through Update, which also re-writes u's own unchanged Role/Color —
// "preserving color" means never handing Update a DIFFERENT one, not
// skipping the call), then changes Role/Active only when profile actually
// differs, through SetRole/SetActive so domain.ErrLastActiveAdmin surfaces
// rather than letting a push demote or disable the household's only admin.
func applyProfile(ctx context.Context, users *UserRepository, u *domain.User, profile domain.FederationProfile) error {
	u.DisplayName = profile.DisplayName
	u.Email = profile.Email
	if err := users.Update(ctx, u); err != nil {
		return fmt.Errorf("federation: upsert: update profile: %w", err)
	}
	if u.Role != profile.Role {
		if err := users.SetRole(ctx, u.ID, profile.Role); err != nil {
			return err
		}
		u.Role = profile.Role
	}
	if u.Active != profile.Active {
		if err := users.SetActive(ctx, u.ID, profile.Active); err != nil {
			return err
		}
		u.Active = profile.Active
	}
	return nil
}

// createFederatedUser inserts a brand-new user for profile: empty password
// hash (nestcore/crypto can never verify it — AC 4), and a color from
// domain.NextFederatedColor over the household's existing users. Active
// defaults true on Create (app_user.active's own DEFAULT); a false profile
// is applied afterward through SetActive, so the last-active-admin guard
// still runs even on a brand-new federated admin.
func createFederatedUser(ctx context.Context, users *UserRepository, profile domain.FederationProfile) (*domain.User, error) {
	existingUsers, err := users.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("federation: upsert: list users: %w", err)
	}
	u := &domain.User{
		ID:          domain.NewUserID(),
		DisplayName: profile.DisplayName,
		Email:       profile.Email,
		Role:        profile.Role,
		Color:       domain.NextFederatedColor(existingUsers),
	}
	if err := users.Create(ctx, u); err != nil {
		return nil, fmt.Errorf("federation: upsert: create user: %w", err)
	}
	if !profile.Active {
		if err := users.SetActive(ctx, u.ID, false); err != nil {
			return nil, err
		}
		u.Active = false
	}
	return u, nil
}

// withBindingCheckedTx runs fn inside a transaction serialized by
// federationAdvisoryLock, after checkBinding has verified (or recorded) the
// household binding.
func (p *FederationProvisioner) withBindingCheckedTx(ctx context.Context, householdID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("federation: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", federationAdvisoryLock); err != nil {
		return fmt.Errorf("federation: acquire advisory lock: %w", err)
	}

	if err := p.checkBinding(ctx, tx, householdID); err != nil {
		return err
	}

	if err := fn(ctx, tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("federation: commit: %w", err)
	}
	return nil
}

// checkBinding enforces NSTR-101's binding rule, shared by every operation
// in this file: no binding recorded means householdID is recorded now (the
// first authenticated federation call is what establishes it); a recorded,
// equal binding proceeds; a different one returns
// domain.ErrHouseholdMismatch — refused, never merged.
func (p *FederationProvisioner) checkBinding(ctx context.Context, tx pgx.Tx, householdID string) error {
	bindings := NewHouseholdBindingRepository(tx)
	binding, found, err := bindings.Get(ctx)
	if err != nil {
		return fmt.Errorf("federation: check binding: %w", err)
	}
	if !found {
		if _, err := bindings.Record(ctx, householdID); err != nil {
			return fmt.Errorf("federation: record binding: %w", err)
		}
		return nil
	}
	if binding.HouseholdID != householdID {
		return domain.ErrHouseholdMismatch
	}
	return nil
}
