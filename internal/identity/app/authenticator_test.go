package app_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ericfisherdev/nestcore/crypto"
	"github.com/ericfisherdev/nestcore/crypto/cryptotest"

	"github.com/ericfisherdev/nestorage/internal/identity/app"
	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// fakeUserFinder is an in-memory userFinder used for hermetic unit tests.
type fakeUserFinder struct {
	// users maps lowercase email → User.
	users map[string]*domain.User
}

func (f *fakeUserFinder) FindByEmail(_ context.Context, email string) (*domain.User, error) {
	u, ok := f.users[email]
	if !ok {
		return nil, domain.ErrUserNotFound
	}
	return u, nil
}

// newFixture creates a fakeUserFinder with one seeded, active user for email
// using the given plaintext password, hashed at cheap test parameters. It is
// still a realistic PHC string, and Verify reads the cost back out of it, so
// the login path under test behaves exactly as it does in production.
func newFixture(t *testing.T, email, password string, active bool) (*fakeUserFinder, domain.UserID) {
	t.Helper()
	hash, err := cryptotest.Hasher().Hash(password)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	id := domain.NewUserID()
	repo := &fakeUserFinder{
		users: map[string]*domain.User{
			email: {ID: id, Email: email, PasswordHash: hash, Active: active},
		},
	}
	return repo, id
}

func TestLogin_Success(t *testing.T) {
	t.Parallel()
	const (
		email    = "alice@example.com"
		password = "correct-horse-battery"
	)
	repo, wantID := newFixture(t, email, password, true)
	authn := app.NewAuthenticator(repo, cryptotest.Hasher())

	gotID, err := authn.Login(context.Background(), email, password)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if gotID != wantID {
		t.Errorf("Login UserID = %v, want %v", gotID, wantID)
	}
}

// TestLogin_NormalizesEmail asserts Login matches against the normalized
// (trimmed, lowercased) form of both the stored and the submitted email, so
// a login is not sensitive to case or surrounding whitespace.
func TestLogin_NormalizesEmail(t *testing.T) {
	t.Parallel()
	const (
		email    = "alice@example.com"
		password = "correct-horse-battery"
	)
	repo, wantID := newFixture(t, email, password, true)
	authn := app.NewAuthenticator(repo, cryptotest.Hasher())

	gotID, err := authn.Login(context.Background(), "  Alice@Example.com  ", password)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if gotID != wantID {
		t.Errorf("Login UserID = %v, want %v", gotID, wantID)
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	t.Parallel()
	repo, _ := newFixture(t, "bob@example.com", "rightpassword", true)
	authn := app.NewAuthenticator(repo, cryptotest.Hasher())

	_, err := authn.Login(context.Background(), "bob@example.com", "wrongpassword")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("Login(wrong password) error = %v, want ErrInvalidCredentials", err)
	}
}

func TestLogin_UnknownEmail(t *testing.T) {
	t.Parallel()
	repo := &fakeUserFinder{users: make(map[string]*domain.User)}
	authn := app.NewAuthenticator(repo, cryptotest.Hasher())

	_, err := authn.Login(context.Background(), "nobody@example.com", "anypassword")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("Login(unknown email) error = %v, want ErrInvalidCredentials", err)
	}
}

// TestLogin_InactiveUser asserts a deactivated user's otherwise-correct
// credentials are rejected with the same generic sentinel as a wrong
// password — distinguishing it here would leak account existence and state.
func TestLogin_InactiveUser(t *testing.T) {
	t.Parallel()
	const (
		email    = "carol@example.com"
		password = "correct-horse-battery"
	)
	repo, _ := newFixture(t, email, password, false)
	authn := app.NewAuthenticator(repo, cryptotest.Hasher())

	_, err := authn.Login(context.Background(), email, password)
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Errorf("Login(inactive user) error = %v, want ErrInvalidCredentials", err)
	}
}

func TestNewAuthenticator_NilDependenciesPanic(t *testing.T) {
	t.Parallel()
	t.Run("nil repo", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if recover() == nil {
				t.Error("NewAuthenticator(nil, hasher) did not panic")
			}
		}()
		app.NewAuthenticator(nil, cryptotest.Hasher())
	})
	t.Run("nil hasher", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if recover() == nil {
				t.Error("NewAuthenticator(repo, nil) did not panic")
			}
		}()
		app.NewAuthenticator(&fakeUserFinder{users: make(map[string]*domain.User)}, nil)
	})
}

// countingHasher wraps a real hasher and records how many derivations it is
// asked to perform, so tests can assert on argon2 usage rather than on wall
// time (which would be flaky).
type countingHasher struct {
	inner    *crypto.Hasher
	hashes   int
	verifies int
}

func newCountingHasher() *countingHasher {
	return &countingHasher{inner: cryptotest.Hasher()}
}

func (c *countingHasher) Hash(password string) (string, error) {
	c.hashes++
	return c.inner.Hash(password)
}

func (c *countingHasher) Verify(password, encoded string) (bool, error) {
	c.verifies++
	return c.inner.Verify(password, encoded)
}

// TestNewAuthenticator_DerivesTimingDummyOncePerInstance guards against the
// dummy hash becoming a package-level var: merely importing this package
// must cost no argon2 derivation, and the dummy's cost must track the
// injected hasher, so it is derived once per Authenticator, in the
// constructor.
func TestNewAuthenticator_DerivesTimingDummyOncePerInstance(t *testing.T) {
	t.Parallel()
	counter := newCountingHasher()
	repo := &fakeUserFinder{users: make(map[string]*domain.User)}

	app.NewAuthenticator(repo, counter)

	if counter.hashes != 1 {
		t.Errorf("NewAuthenticator performed %d derivations, want exactly 1 (the timing dummy)", counter.hashes)
	}
}

// TestLogin_UnknownEmailStillVerifiesForTiming guards the user-enumeration
// defence: the unknown-email path must perform a verification against the
// dummy hash so it costs about as much as the wrong-password path.
// Asserting on the call count rather than on elapsed time keeps this
// deterministic.
func TestLogin_UnknownEmailStillVerifiesForTiming(t *testing.T) {
	t.Parallel()
	counter := newCountingHasher()
	repo := &fakeUserFinder{users: make(map[string]*domain.User)}
	authn := app.NewAuthenticator(repo, counter)

	before := counter.verifies
	_, err := authn.Login(context.Background(), "nobody@example.com", "anypassword")
	if !errors.Is(err, domain.ErrInvalidCredentials) {
		t.Fatalf("Login(unknown email) error = %v, want ErrInvalidCredentials", err)
	}
	if got := counter.verifies - before; got != 1 {
		t.Errorf("unknown-email path performed %d verifications, want 1 (the timing equalizer)", got)
	}
}
