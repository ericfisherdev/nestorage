package domain

import "context"

// UserRepository is the outbound port for persisting and retrieving users.
// Implementations live in the adapter package.
//
// Persistence contracts (the caller sets identity and valid enum values; the
// store sets timestamps):
//   - Create expects u.ID, u.DisplayName, u.Email, u.PasswordHash, and valid
//     u.Role/u.Color set; it populates CreatedAt/UpdatedAt. The caller is
//     responsible for supplying valid enum values (the store does not
//     re-validate on write).
//
// Error contracts:
//   - Create returns ErrDuplicateEmail when the email is already taken.
//   - FindByID returns ErrUserNotFound when id is unknown.
//   - FindByEmail returns ErrUserNotFound when no user has that email. This
//     port is not the login path, so it reports the specific error; NSTR-20
//     collapses it to a generic non-enumerating failure at the app layer.
//   - Update writes display name, email, role, and color only — it cannot
//     touch the password hash or the active flag, so a profile edit can
//     never blank a credential. Returns ErrUserNotFound or ErrDuplicateEmail.
//   - SetActive returns ErrUserNotFound when id is unknown. One method covers
//     both deactivating and reactivating a user.
//   - List returns an empty slice (not an error) when no users exist.
//   - Count and HasAnyUser never return a sentinel error.
type UserRepository interface {
	Create(ctx context.Context, u *User) error
	FindByID(ctx context.Context, id UserID) (*User, error)
	FindByEmail(ctx context.Context, email string) (*User, error)
	// List returns every user ordered by display name.
	List(ctx context.Context) ([]User, error)
	Update(ctx context.Context, u *User) error
	// SetActive sets id's active flag. Passing false deactivates the user;
	// passing true reactivates it.
	SetActive(ctx context.Context, id UserID, active bool) error
	// Count returns the total number of users.
	Count(ctx context.Context) (int, error)
	// HasAnyUser reports whether at least one user row exists. It is used by
	// NSTR-19's first-run guard to decide whether the initial-admin setup
	// flow should be shown, without loading every row on every request.
	HasAnyUser(ctx context.Context) (bool, error)
}
