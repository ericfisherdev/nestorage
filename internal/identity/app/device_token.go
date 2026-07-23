package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// maxDeviceNameRunes bounds a device's display name — the same "count runes,
// not bytes" rule as domain.ValidatePassword, so a multi-byte name is not
// rejected for exceeding a byte count a human never actually typed.
const maxDeviceNameRunes = 100

// lastUsedInterval throttles DeviceToken.LastUsedAt writes: a device
// authenticating repeatedly writes at most once per interval, keyed off the
// stored value rather than an in-memory cache (see touchLastUsed).
const lastUsedInterval = 15 * time.Minute

// deviceTokenRepository is the narrow port (ISP) DeviceTokenService depends
// on, satisfied by domain.DeviceTokenRepository (a superset) and by test
// fakes.
type deviceTokenRepository interface {
	Create(ctx context.Context, t *domain.DeviceToken) error
	GetByTokenHash(ctx context.Context, tokenHash string) (*domain.DeviceToken, error)
	ListByUser(ctx context.Context, userID domain.UserID) ([]*domain.DeviceToken, error)
	Revoke(ctx context.Context, userID domain.UserID, id domain.DeviceTokenID, revokedAt time.Time) error
	RevokeAllForUser(ctx context.Context, userID domain.UserID, revokedAt time.Time) (int64, error)
	TouchLastUsed(ctx context.Context, id domain.DeviceTokenID, now, staleBefore time.Time) error
}

// deviceOwnerFinder is the narrow read port (ISP) Authenticate depends on to
// check a token's owning user is still active, satisfied by
// domain.UserRepository (a superset) and by test fakes.
type deviceOwnerFinder interface {
	FindByID(ctx context.Context, id domain.UserID) (*domain.User, error)
}

// credentialVerifier is the narrow port (ISP) Issue depends on to verify the
// email/password pair presented to the exchange endpoint, satisfied by
// *Authenticator (same package) — declared as an interface anyway so a
// hermetic unit test can fake it without paying a real argon2 derivation.
type credentialVerifier interface {
	Login(ctx context.Context, email, password string) (domain.UserID, error)
}

// attemptLimiter is the narrow port (ISP) Issue depends on to rate-limit
// wrong-password attempts, satisfied by *adapter.LoginAttemptLimiter. The
// composition root injects the SAME instance here and into NSTR-20's login
// Handlers, so an attacker locked out of /login cannot get a fresh run of
// attempts against this endpoint instead — see LoginAttemptLimiter's own
// doc for why that sharing matters.
type attemptLimiter interface {
	Locked(email string, now time.Time) bool
	RecordFailure(email string, now time.Time) bool
	RecordSuccess(email string)
}

// DeviceTokenService issues and manages the per-device bearer tokens
// NSTR-22's Android client authenticates with. It implements CredentialRevoker
// directly (see RevokeAll below) so the composition root can register it
// into NSTR-21's Revokers slice without any adapter-side wrapper type.
type DeviceTokenService struct {
	tokens  deviceTokenRepository
	users   deviceOwnerFinder
	authn   credentialVerifier
	limiter attemptLimiter
	clock   func() time.Time
	logger  *slog.Logger
}

// Compile-time assurance DeviceTokenService satisfies the port NSTR-21's
// Revokers slice fans out to.
var _ CredentialRevoker = (*DeviceTokenService)(nil)

// NewDeviceTokenService constructs DeviceTokenService. All dependencies are
// required; a missing one panics at construction time, matching every other
// constructor in this codebase (see NewAuthenticator, NewAdminService).
func NewDeviceTokenService(tokens deviceTokenRepository, users deviceOwnerFinder, authn credentialVerifier, limiter attemptLimiter, clock func() time.Time, logger *slog.Logger) *DeviceTokenService {
	if tokens == nil {
		panic("identity/app: NewDeviceTokenService requires a non-nil deviceTokenRepository")
	}
	if users == nil {
		panic("identity/app: NewDeviceTokenService requires a non-nil deviceOwnerFinder")
	}
	if authn == nil {
		panic("identity/app: NewDeviceTokenService requires a non-nil credentialVerifier")
	}
	if limiter == nil {
		panic("identity/app: NewDeviceTokenService requires a non-nil attemptLimiter")
	}
	if clock == nil {
		panic("identity/app: NewDeviceTokenService requires a non-nil clock func")
	}
	if logger == nil {
		panic("identity/app: NewDeviceTokenService requires a non-nil logger")
	}
	return &DeviceTokenService{tokens: tokens, users: users, authn: authn, limiter: limiter, clock: clock, logger: logger}
}

// Issue verifies email/password (through the same verifier and limiter
// NSTR-20's login uses), then mints and persists a new device token for the
// authenticated user. deviceName is trimmed and must be non-blank and at
// most maxDeviceNameRunes long. plaintext is returned exactly once — the
// caller must not expect to recover it later; only its hash is stored.
//
// Every credential failure — an unknown email, a wrong password, and a
// locked-out email alike — returns the same domain.ErrInvalidCredentials,
// so the exchange endpoint cannot be used to enumerate accounts.
func (s *DeviceTokenService) Issue(ctx context.Context, email, password, deviceName string) (plaintext string, token *domain.DeviceToken, err error) {
	deviceName = strings.TrimSpace(deviceName)
	if err := validateDeviceName(deviceName); err != nil {
		return "", nil, err
	}

	email = domain.NormalizeEmail(email)
	now := s.clock()
	if s.limiter.Locked(email, now) {
		return "", nil, domain.ErrInvalidCredentials
	}

	userID, err := s.authn.Login(ctx, email, password)
	if err != nil {
		if errors.Is(err, domain.ErrInvalidCredentials) {
			if s.limiter.RecordFailure(email, now) {
				s.logger.WarnContext(ctx, "device token: account locked out after repeated failures")
			}
			return "", nil, domain.ErrInvalidCredentials
		}
		return "", nil, fmt.Errorf("device token: issue: authenticate: %w", err)
	}
	s.limiter.RecordSuccess(email)

	raw, err := domain.GenerateDeviceToken()
	if err != nil {
		return "", nil, fmt.Errorf("device token: issue: generate: %w", err)
	}

	t := &domain.DeviceToken{
		ID:        domain.NewDeviceTokenID(),
		UserID:    userID,
		TokenHash: domain.HashDeviceToken(raw),
		Name:      deviceName,
	}
	if err := s.tokens.Create(ctx, t); err != nil {
		return "", nil, fmt.Errorf("device token: issue: create: %w", err)
	}
	return raw, t, nil
}

// validateDeviceName wraps domain.ErrInvalidDeviceToken for a blank or
// over-long device name — checked before any credential is touched, since
// it depends on neither.
func validateDeviceName(name string) error {
	n := len([]rune(name))
	switch {
	case n == 0:
		return fmt.Errorf("%w: device name must not be blank", domain.ErrInvalidDeviceToken)
	case n > maxDeviceNameRunes:
		return fmt.Errorf("%w: device name must be at most %d characters", domain.ErrInvalidDeviceToken, maxDeviceNameRunes)
	default:
		return nil
	}
}

// Authenticate resolves a presented device token into its owning User.
// Nothing on this path is cached: AC 2 requires a revocation to take effect
// immediately, and caching the lookup would silently break that.
//
// Returns domain.ErrDeviceTokenNotFound for an unrecognized token,
// domain.ErrDeviceTokenRevoked for a known-but-revoked one, and
// domain.ErrUserInactive when the owning user has since been deactivated —
// three distinct outcomes so the caller can log/react to each differently,
// unlike the deliberately generic domain.ErrInvalidCredentials the
// password-based Login/Issue paths return.
func (s *DeviceTokenService) Authenticate(ctx context.Context, presented string) (*domain.User, *domain.DeviceToken, error) {
	hash := domain.HashDeviceToken(presented)
	token, err := s.tokens.GetByTokenHash(ctx, hash)
	if err != nil {
		if errors.Is(err, domain.ErrDeviceTokenNotFound) {
			return nil, nil, domain.ErrDeviceTokenNotFound
		}
		return nil, nil, fmt.Errorf("device token: authenticate: %w", err)
	}
	if !token.Active() {
		return nil, nil, domain.ErrDeviceTokenRevoked
	}

	user, err := s.users.FindByID(ctx, token.UserID)
	if err != nil {
		if errors.Is(err, domain.ErrUserNotFound) {
			return nil, nil, domain.ErrUserNotFound
		}
		return nil, nil, fmt.Errorf("device token: authenticate: %w", err)
	}
	if !user.Active {
		return nil, nil, domain.ErrUserInactive
	}

	s.touchLastUsed(ctx, token.ID)
	return user, token, nil
}

// touchLastUsed best-effort records this authentication on token id,
// throttled to once per lastUsedInterval by TouchLastUsed's own
// stale-before argument. A failure is logged and swallowed — it must never
// fail an otherwise valid authentication.
func (s *DeviceTokenService) touchLastUsed(ctx context.Context, id domain.DeviceTokenID) {
	now := s.clock()
	if err := s.tokens.TouchLastUsed(ctx, id, now, now.Add(-lastUsedInterval)); err != nil {
		s.logger.ErrorContext(ctx, "device token: touch last used", "error", err)
	}
}

// ListForUser returns userID's devices, newest first.
func (s *DeviceTokenService) ListForUser(ctx context.Context, userID domain.UserID) ([]*domain.DeviceToken, error) {
	tokens, err := s.tokens.ListByUser(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("device token: list: %w", err)
	}
	return tokens, nil
}

// Revoke revokes one of userID's own devices. Scoped by userID so a request
// cannot revoke another user's token by guessing its id.
func (s *DeviceTokenService) Revoke(ctx context.Context, userID domain.UserID, id domain.DeviceTokenID) error {
	if err := s.tokens.Revoke(ctx, userID, id, s.clock()); err != nil {
		return fmt.Errorf("device token: revoke: %w", err)
	}
	return nil
}

// RevokeAll implements CredentialRevoker: it revokes every device token
// belonging to id. The composition root registers this method into
// NSTR-21's Revokers slice, so deactivating (or resetting the password of) a
// user also invalidates their device tokens — without NSTR-21's own code
// changing at all (OCP). A user with no tokens at all is success, not an
// error, matching RevokeAllForUser's own contract.
func (s *DeviceTokenService) RevokeAll(ctx context.Context, id domain.UserID) error {
	if _, err := s.tokens.RevokeAllForUser(ctx, id, s.clock()); err != nil {
		return fmt.Errorf("device token: revoke all: %w", err)
	}
	return nil
}
