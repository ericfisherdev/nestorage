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
)
