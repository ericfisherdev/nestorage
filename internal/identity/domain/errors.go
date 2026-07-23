package domain

import "errors"

// Domain errors for the identity bounded context.
var (
	// ErrUserNotFound is returned by UserRepository methods that look up a
	// specific user (FindByID, FindByEmail, Update, SetActive) when no
	// matching row exists.
	ErrUserNotFound = errors.New("identity: user not found")

	// ErrDuplicateEmail is returned by UserRepository.Create and Update when
	// the email is already assigned to a different user (the email column is
	// unique, compared case-insensitively via citext).
	ErrDuplicateEmail = errors.New("identity: email already in use")

	// ErrUserInactive marks a user that exists but has been deactivated
	// (active = false). Not currently returned by any UserRepository method —
	// SetActive's whole purpose is to set this state, not reject transitions
	// into or out of it — but reserved here for the login flow (NSTR-20),
	// which must reject an inactive user's credentials.
	ErrUserInactive = errors.New("identity: user is inactive")

	// ErrInvalidRole is returned (wrapped, with the offending value) by
	// ParseRole when given a string that is not a known Role.
	ErrInvalidRole = errors.New("identity: invalid role")

	// ErrInvalidColor is returned (wrapped, with the offending value) by
	// ParseUserColor when given a string that is not a known, user-assignable
	// UserColor.
	ErrInvalidColor = errors.New("identity: invalid user color")

	// ErrSetupComplete is returned by Provisioner.CreateFirstAdmin when at
	// least one user already exists. The first-run wizard treats it as "lost
	// the race" and redirects to / rather than surfacing it as a failure.
	ErrSetupComplete = errors.New("identity: setup already complete")

	// ErrPasswordTooShort is returned by ValidatePassword when password is
	// under minPasswordRunes.
	ErrPasswordTooShort = errors.New("identity: password must be at least 12 characters")

	// ErrPasswordTooLong is returned by ValidatePassword when password
	// exceeds maxPasswordRunes. The bound exists to cap the cost of the
	// argon2id derivation (nestcore/crypto.Hash), not to discourage long
	// passwords.
	ErrPasswordTooLong = errors.New("identity: password must be at most 128 characters")

	// ErrInvalidCredentials is returned by app.Authenticator.Login for every
	// failure mode — unknown email, wrong password, and an inactive user
	// alike — so the login handler cannot distinguish them and leak whether
	// an email address has an account.
	ErrInvalidCredentials = errors.New("identity: invalid email or password")

	// ErrLastActiveAdmin is returned by UserRepository.SetRole and SetActive
	// when the operation would leave the household with zero active admins —
	// demoting or deactivating its only remaining one.
	ErrLastActiveAdmin = errors.New("identity: cannot remove the last active admin")

	// ErrDeviceTokenNotFound is returned by DeviceTokenRepository methods
	// that look up or mutate a specific token (GetByTokenHash, Revoke) when
	// no matching row exists.
	ErrDeviceTokenNotFound = errors.New("identity: device token not found")

	// ErrDeviceTokenRevoked marks a token that exists but has been revoked
	// (revoked_at is set). Returned by DeviceTokenService.Authenticate, not
	// by the repository — GetByTokenHash returns a revoked row rather than
	// this error, so the caller can log/react to "known but revoked"
	// differently from "unknown token".
	ErrDeviceTokenRevoked = errors.New("identity: device token revoked")

	// ErrInvalidDeviceToken is returned (wrapped) by DeviceToken.Validate
	// for a malformed token.
	ErrInvalidDeviceToken = errors.New("identity: invalid device token")

	// ErrAPIKeyNotFound is returned by APIKeyRepository methods that look
	// up or mutate a specific key (GetBySecretHash, Revoke, Rotate's
	// incumbent) when no matching row exists.
	ErrAPIKeyNotFound = errors.New("identity: api key not found")

	// ErrAPIKeyRevoked marks a key that exists but has been revoked
	// (revoked_at is set). Returned by APIKeyService.Authenticate, not by
	// the repository — GetBySecretHash returns a revoked row rather than
	// this error, so the caller can log/react to "known but revoked"
	// differently from "unknown key".
	ErrAPIKeyRevoked = errors.New("identity: api key revoked")

	// ErrAPIKeyExpired marks a key that exists, is not revoked, but has
	// passed its expires_at (the end of a rotation's overlap window).
	// Returned by APIKeyService.Authenticate, for the same "distinguish
	// from unknown/revoked" reason as ErrAPIKeyRevoked.
	ErrAPIKeyExpired = errors.New("identity: api key expired")

	// ErrInvalidAPIKey is returned (wrapped) by APIKey.Validate for a
	// malformed key, and by APIKeyService.Create/Rotate for a blank or
	// over-long label.
	ErrInvalidAPIKey = errors.New("identity: invalid api key")

	// ErrAPIKeyExists is returned by APIKeyRepository.Create when a current
	// key (revoked_at and expires_at both nil) already exists — enforced by
	// the api_key_current_uniq partial unique index, not only by this
	// check. Create is unambiguous ("there is no key yet"); replacing an
	// existing one is Rotate.
	ErrAPIKeyExists = errors.New("identity: a current api key already exists")

	// ErrInvalidOverlapWindow is returned (wrapped, with the offending
	// value) by ParseOverlapWindow when given a string that is not a known
	// OverlapWindow.
	ErrInvalidOverlapWindow = errors.New("identity: invalid overlap window")
)
