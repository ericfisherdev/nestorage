package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// userRepository is the narrow port (ISP) AdminService depends on: only the
// create/list/mutate methods admin user management actually calls, satisfied
// by domain.UserRepository (a superset) and by test fakes.
type userRepository interface {
	Create(ctx context.Context, u *domain.User) error
	List(ctx context.Context) ([]domain.User, error)
	SetRole(ctx context.Context, id domain.UserID, role domain.Role) error
	SetActive(ctx context.Context, id domain.UserID, active bool) error
	SetPasswordHash(ctx context.Context, id domain.UserID, hash string) error
}

// passwordCreator is the narrow seam (ISP) AdminService depends on to derive
// a new or reset password hash: only Hash, never Verify — verifying a
// credential is Authenticator's job, not admin user management's. Satisfied
// by *crypto.Hasher.
type passwordCreator interface {
	Hash(password string) (string, error)
}

// AdminService implements the household admin's user-management operations:
// creating members, changing roles, deactivating/reactivating, and resetting
// a forgotten password. Every mutation that can end a session (Deactivate,
// ResetPassword) also fans out to revoker, so a stale credential cannot
// outlive the action that was supposed to invalidate it.
type AdminService struct {
	users   userRepository
	hasher  passwordCreator
	revoker CredentialRevoker
	logger  *slog.Logger
}

// NewAdminService constructs AdminService. All dependencies are required; a
// missing one panics at construction time, matching every other constructor
// in this codebase (see NewAuthenticator).
func NewAdminService(users userRepository, hasher passwordCreator, revoker CredentialRevoker, logger *slog.Logger) *AdminService {
	if users == nil {
		panic("identity/app: NewAdminService requires a non-nil userRepository")
	}
	if hasher == nil {
		panic("identity/app: NewAdminService requires a non-nil password hasher")
	}
	if revoker == nil {
		panic("identity/app: NewAdminService requires a non-nil CredentialRevoker")
	}
	if logger == nil {
		panic("identity/app: NewAdminService requires a non-nil logger")
	}
	return &AdminService{users: users, hasher: hasher, revoker: revoker, logger: logger}
}

// List returns every user in the household, ordered by display name.
func (s *AdminService) List(ctx context.Context) ([]domain.User, error) {
	users, err := s.users.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("admin: list users: %w", err)
	}
	return users, nil
}

// Create validates password against domain.ValidatePassword, hashes it, and
// creates a new user with the given role and color. Returns a wrapped
// domain.ErrPasswordTooShort/ErrPasswordTooLong from validation, or a
// wrapped domain.ErrDuplicateEmail when email is already taken.
func (s *AdminService) Create(ctx context.Context, displayName, email, password string, role domain.Role, color domain.UserColor) (*domain.User, error) {
	if err := domain.ValidatePassword(password); err != nil {
		return nil, err
	}
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return nil, fmt.Errorf("admin: hash password: %w", err)
	}

	u := &domain.User{
		ID:           domain.NewUserID(),
		DisplayName:  displayName,
		Email:        email,
		PasswordHash: hash,
		Role:         role,
		Color:        color,
	}
	if err := s.users.Create(ctx, u); err != nil {
		return nil, fmt.Errorf("admin: create user: %w", err)
	}
	s.logAction(ctx, "user created", u.ID, "role", role.String())
	return u, nil
}

// ChangeRole sets id's role. Returns a wrapped domain.ErrLastActiveAdmin
// when id is the household's only active admin and role is not
// domain.RoleAdmin, or a wrapped domain.ErrUserNotFound for an unknown id.
func (s *AdminService) ChangeRole(ctx context.Context, id domain.UserID, role domain.Role) error {
	if err := s.users.SetRole(ctx, id, role); err != nil {
		return fmt.Errorf("admin: change role: %w", err)
	}
	s.logAction(ctx, "user role changed", id, "role", role.String())
	return nil
}

// Deactivate flips id's active flag to false first, then revokes every
// outstanding credential — in that order, so a request racing the
// deactivation cannot re-establish a session after revocation already ran.
// A revocation failure is returned, never swallowed: the user IS
// deactivated, but a credential may linger, which the caller has to see and
// act on. Returns a wrapped domain.ErrLastActiveAdmin when id is the
// household's only active admin, or a wrapped domain.ErrUserNotFound for an
// unknown id.
func (s *AdminService) Deactivate(ctx context.Context, id domain.UserID) error {
	if err := s.users.SetActive(ctx, id, false); err != nil {
		return fmt.Errorf("admin: deactivate user: %w", err)
	}
	if err := s.revoker.RevokeAll(ctx, id); err != nil {
		return fmt.Errorf("admin: deactivate user: revoke credentials: %w", err)
	}
	s.logAction(ctx, "user deactivated", id)
	return nil
}

// Reactivate flips id's active flag to true. Returns a wrapped
// domain.ErrUserNotFound for an unknown id. Reactivation can never violate
// the last-active-admin invariant, so unlike Deactivate it never returns
// domain.ErrLastActiveAdmin.
func (s *AdminService) Reactivate(ctx context.Context, id domain.UserID) error {
	if err := s.users.SetActive(ctx, id, true); err != nil {
		return fmt.Errorf("admin: reactivate user: %w", err)
	}
	s.logAction(ctx, "user reactivated", id)
	return nil
}

// ResetPassword validates and hashes a new password for id, then revokes
// every outstanding credential — an admin resetting someone's password
// implies the old one is suspect. Returns a wrapped
// domain.ErrPasswordTooShort/ErrPasswordTooLong from validation, or a
// wrapped domain.ErrUserNotFound for an unknown id.
func (s *AdminService) ResetPassword(ctx context.Context, id domain.UserID, password string) error {
	if err := domain.ValidatePassword(password); err != nil {
		return err
	}
	hash, err := s.hasher.Hash(password)
	if err != nil {
		return fmt.Errorf("admin: hash password: %w", err)
	}
	if err := s.users.SetPasswordHash(ctx, id, hash); err != nil {
		return fmt.Errorf("admin: reset password: %w", err)
	}
	if err := s.revoker.RevokeAll(ctx, id); err != nil {
		return fmt.Errorf("admin: reset password: revoke credentials: %w", err)
	}
	s.logAction(ctx, "user password reset", id)
	return nil
}

// logAction writes one INFO-level audit line for a completed admin
// mutation. It logs the user's id, never their name or email — Nestorage's
// convention for keeping PII out of logs (see the household's other
// logging call sites) — plus any extra fields the caller supplies.
func (s *AdminService) logAction(ctx context.Context, msg string, id domain.UserID, extra ...any) {
	args := append([]any{"user_id", id.String()}, extra...)
	s.logger.InfoContext(ctx, "admin: "+msg, args...)
}
