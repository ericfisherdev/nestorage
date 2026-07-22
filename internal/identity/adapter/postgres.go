package adapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ericfisherdev/nestcore/db"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

const (
	// uniqueViolation is the PostgreSQL SQLSTATE for a unique-constraint violation.
	uniqueViolation = "23505"
	// appUserEmailUnique is the unique constraint on app_user.email, named
	// explicitly in the 00002_identity migration so it can be matched here
	// instead of parsing an error message.
	appUserEmailUnique = "app_user_email_unique"
)

// userColumns is shared by every read query, keeping the column list and
// scanUser in lockstep.
const userColumns = `SELECT id, display_name, email, password_hash, role, color, active, created_at, updated_at FROM app_user`

// UserRepository is the pgx-backed domain.UserRepository. UUIDs are passed
// and scanned as text, matching the other Nest adapters, so no pgx UUID
// codec registration is required.
type UserRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.UserRepository = (*UserRepository)(nil)

// NewUserRepository constructs the repository with an injected query
// executor. The executor is a db.TX, satisfied by both *pgxpool.Pool (the
// default composition) and pgx.Tx (so NSTR-19's first-run admin creation can
// run inside a transaction); the same methods work against either.
func NewUserRepository(dbtx db.TX) *UserRepository {
	if dbtx == nil {
		panic("identity/adapter: NewUserRepository requires a non-nil db.TX")
	}
	return &UserRepository{dbtx: dbtx}
}

// Create inserts a user and populates its Active flag and timestamps. Active
// is never read from u — a newly created user is always active (the
// app_user.active column's own DEFAULT true); deactivation is SetActive's
// job, not Create's. Returns domain.ErrDuplicateEmail when the email is
// already taken.
func (r *UserRepository) Create(ctx context.Context, u *domain.User) error {
	if u == nil {
		return errors.New("identity/adapter: create user: nil user")
	}
	const q = `
		INSERT INTO app_user (id, display_name, email, password_hash, role, color)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING active, created_at, updated_at`
	err := r.dbtx.QueryRow(ctx, q,
		u.ID.String(), u.DisplayName, u.Email, u.PasswordHash, u.Role.String(), u.Color.String(),
	).Scan(&u.Active, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		if isDuplicateEmail(err) {
			return domain.ErrDuplicateEmail
		}
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

// FindByID returns the user, or domain.ErrUserNotFound.
func (r *UserRepository) FindByID(ctx context.Context, id domain.UserID) (*domain.User, error) {
	u, err := scanUser(r.dbtx.QueryRow(ctx, userColumns+` WHERE id = $1`, id.String()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrUserNotFound
		}
		return nil, fmt.Errorf("find user by id: %w", err)
	}
	return u, nil
}

// FindByEmail returns the user, or domain.ErrUserNotFound. The comparison is
// case-insensitive: email is a citext column.
func (r *UserRepository) FindByEmail(ctx context.Context, email string) (*domain.User, error) {
	u, err := scanUser(r.dbtx.QueryRow(ctx, userColumns+` WHERE email = $1`, email))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrUserNotFound
		}
		return nil, fmt.Errorf("find user by email: %w", err)
	}
	return u, nil
}

// List returns every user ordered by display name, tie-broken by id for a
// stable order between rows sharing a display name. Returns an empty slice,
// not an error, when no users exist.
func (r *UserRepository) List(ctx context.Context) ([]domain.User, error) {
	rows, err := r.dbtx.Query(ctx, userColumns+` ORDER BY display_name, id`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	defer rows.Close()

	users := make([]domain.User, 0)
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("list users: scan: %w", err)
		}
		users = append(users, *u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	return users, nil
}

// Update rewrites the user's mutable profile fields: display name, email,
// role, and color. It cannot touch the password hash or the active flag, so a
// profile edit can never blank a credential — SetPasswordHash (NSTR-20) and
// SetActive own those, respectively. Returns domain.ErrUserNotFound for an
// unknown id, or domain.ErrDuplicateEmail when the new email is already taken
// by a different user.
func (r *UserRepository) Update(ctx context.Context, u *domain.User) error {
	if u == nil {
		return errors.New("identity/adapter: update user: nil user")
	}
	const q = `
		UPDATE app_user
		   SET display_name = $2, email = $3, role = $4, color = $5, updated_at = now()
		 WHERE id = $1
		RETURNING updated_at`
	err := r.dbtx.QueryRow(ctx, q, u.ID.String(), u.DisplayName, u.Email, u.Role.String(), u.Color.String()).
		Scan(&u.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrUserNotFound
		}
		if isDuplicateEmail(err) {
			return domain.ErrDuplicateEmail
		}
		return fmt.Errorf("update user: %w", err)
	}
	return nil
}

// SetActive sets id's active flag, covering both deactivation (active=false)
// and NSTR-21's reactivation (active=true) with one method. Returns
// domain.ErrUserNotFound for an unknown id.
func (r *UserRepository) SetActive(ctx context.Context, id domain.UserID, active bool) error {
	const q = `UPDATE app_user SET active = $2, updated_at = now() WHERE id = $1`
	tag, err := r.dbtx.Exec(ctx, q, id.String(), active)
	if err != nil {
		return fmt.Errorf("set active: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrUserNotFound
	}
	return nil
}

// Count returns the total number of users.
func (r *UserRepository) Count(ctx context.Context) (int, error) {
	const q = `SELECT count(*) FROM app_user`
	var n int
	if err := r.dbtx.QueryRow(ctx, q).Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// HasAnyUser reports whether at least one user row exists. It is used by
// NSTR-19's first-run guard to decide whether the initial-admin setup flow
// should be shown, without loading every row on every request.
func (r *UserRepository) HasAnyUser(ctx context.Context) (bool, error) {
	const q = `SELECT EXISTS(SELECT 1 FROM app_user)`
	var exists bool
	if err := r.dbtx.QueryRow(ctx, q).Scan(&exists); err != nil {
		return false, fmt.Errorf("has any user: %w", err)
	}
	return exists, nil
}

// isDuplicateEmail reports whether err is a unique-violation on
// app_user_email_unique specifically — other unique violations (e.g. the
// primary key) are left to surface as a wrapped error rather than
// misreported as a duplicate email.
func isDuplicateEmail(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == appUserEmailUnique
}

// scanner abstracts pgx.Row and pgx.Rows for the shared scan helper.
type scanner interface {
	Scan(dest ...any) error
}

func scanUser(r scanner) (*domain.User, error) {
	var (
		u     domain.User
		idStr string
		role  string
		color string
	)
	if err := r.Scan(&idStr, &u.DisplayName, &u.Email, &u.PasswordHash, &role, &color, &u.Active, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, err
	}
	id, err := domain.ParseUserID(idStr)
	if err != nil {
		return nil, fmt.Errorf("scan user: %w", err)
	}
	parsedRole, err := domain.ParseRole(role)
	if err != nil {
		return nil, fmt.Errorf("scan user: %w", err)
	}
	parsedColor, err := domain.ParseUserColor(color)
	if err != nil {
		return nil, fmt.Errorf("scan user: %w", err)
	}
	u.ID, u.Role, u.Color = id, parsedRole, parsedColor
	return &u, nil
}
