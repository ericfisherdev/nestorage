package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// passwordChanger is the narrow port (ISP) PasswordService depends on: only
// the by-id lookup and hash-write NSTR-103's self-service change needs,
// satisfied by domain.UserRepository (a superset) and by test fakes.
// SetPasswordHash is the exact method NSTR-20's own repository doc already
// names as belonging to this flow (domain.UserRepository's own comment).
type passwordChanger interface {
	FindByID(ctx context.Context, id domain.UserID) (*domain.User, error)
	SetPasswordHash(ctx context.Context, id domain.UserID, hash string) error
}

// PasswordService implements NSTR-103's self-service password change: a
// signed-in household member changes their own password without an admin's
// involvement — the standalone-mode half of NSTR-20's deferred scope that
// AdminService.ResetPassword (the admin-reset half) never covered.
type PasswordService struct {
	users   passwordChanger
	hasher  passwordHasher
	revoker CredentialRevoker
	logger  *slog.Logger
}

// NewPasswordService constructs PasswordService. All dependencies are
// required; a missing one panics at construction time, matching every
// other constructor in this codebase (see NewAdminService). hasher is the
// same passwordHasher seam Authenticator uses (authenticator.go) — Verify
// AND Hash, unlike AdminService's hash-only passwordCreator seam, since
// ChangeOwn has to verify the caller's current password before deriving a
// new one.
func NewPasswordService(users passwordChanger, hasher passwordHasher, revoker CredentialRevoker, logger *slog.Logger) *PasswordService {
	if users == nil {
		panic("identity/app: NewPasswordService requires a non-nil passwordChanger")
	}
	if hasher == nil {
		panic("identity/app: NewPasswordService requires a non-nil password hasher")
	}
	if revoker == nil {
		panic("identity/app: NewPasswordService requires a non-nil CredentialRevoker")
	}
	if logger == nil {
		panic("identity/app: NewPasswordService requires a non-nil logger")
	}
	return &PasswordService{users: users, hasher: hasher, revoker: revoker, logger: logger}
}

// ChangeOwn changes id's own password: FindByID looks up the caller, an
// inactive user is refused, currentPassword must verify against the stored
// hash, and newPassword must pass domain.ValidatePassword — the exact rule
// account creation uses, no second rule. On success, ChangeOwn revokes
// every outstanding credential belonging to id — sessions and device
// tokens alike, through the same Revokers fan-out AdminService.ResetPassword
// already uses (one invariant, one revocation path; no weaker parallel
// path). The caller (the web handler) is responsible for re-establishing
// the session that made the change under a fresh token; the Android app
// must re-pair, which is deliberate — matching the admin reset's posture
// that a changed password makes prior credentials suspect.
//
// Returns domain.ErrInvalidCredentials for a wrong current password or an
// inactive user — the same generic sentinel either way, so the caller
// cannot distinguish them (mirrors Authenticator.Login's identical
// rationale); domain.ErrPasswordTooShort/ErrPasswordTooLong from
// validation; or a wrapped domain.ErrUserNotFound for an unknown id.
func (s *PasswordService) ChangeOwn(ctx context.Context, id domain.UserID, currentPassword, newPassword string) error {
	u, err := s.users.FindByID(ctx, id)
	if err != nil {
		return fmt.Errorf("password: change own: find user: %w", err)
	}
	if !u.Active {
		return domain.ErrInvalidCredentials
	}

	ok, err := s.hasher.Verify(currentPassword, u.PasswordHash)
	if err != nil || !ok {
		return domain.ErrInvalidCredentials
	}

	if err := domain.ValidatePassword(newPassword); err != nil {
		return err
	}
	hash, err := s.hasher.Hash(newPassword)
	if err != nil {
		return fmt.Errorf("password: change own: hash new password: %w", err)
	}
	if err := s.users.SetPasswordHash(ctx, id, hash); err != nil {
		return fmt.Errorf("password: change own: set password hash: %w", err)
	}
	if err := s.revoker.RevokeAll(ctx, id); err != nil {
		return fmt.Errorf("password: change own: revoke credentials: %w", err)
	}
	s.logAction(ctx, id)
	return nil
}

// logAction writes one INFO-level audit line for a completed self-service
// password change. It logs the user's id only, never their name or email —
// matching AdminService.logAction's identical PII-free convention.
func (s *PasswordService) logAction(ctx context.Context, id domain.UserID) {
	s.logger.InfoContext(ctx, "password: user changed own password", "user_id", id.String())
}
