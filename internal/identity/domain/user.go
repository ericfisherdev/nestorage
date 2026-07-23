package domain

import "time"

// User is the aggregate root for the identity bounded context.
//
// PasswordHash holds a PHC-encoded argon2id string produced by
// nestcore/crypto (see that package's Hash). This context never handles a
// plaintext password directly; deriving and verifying hashes is NSTR-20's
// work.
type User struct {
	ID           UserID
	DisplayName  string
	Email        string
	PasswordHash string
	Role         Role
	Color        UserColor
	Active       bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// IsAdmin reports whether u carries administrative privileges. Delegates to
// Role.IsAdmin so the one rule stays defined in one place.
func (u User) IsAdmin() bool { return u.Role.IsAdmin() }
