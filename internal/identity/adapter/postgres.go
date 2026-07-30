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
	// memberEmailUnique is the unique constraint on identity.member.email,
	// named explicitly in the 00017_identity_schema migration — matching
	// Nestova's own member_email_unique (its 00002_auth.sql) — so it can be
	// matched here instead of parsing an error message, and keeps matching
	// once nestcore owns the identity schema.
	memberEmailUnique = "member_email_unique"
)

// userColumns is shared by every read query, keeping the column list and
// scanUser in lockstep. color lives on Nestorage's own profile table
// (NSTR-116); the join is LEFT, not INNER, so a member created by another
// app against the shared identity schema (e.g. Nestova, adopting an existing
// household per resolveHouseholdForSetup's own doc) stays readable — and
// loginable — with the default color rather than disappearing from every
// read while Count/HasAnyUser (which query identity.member directly, no
// join) still count it.
const userColumns = `
	SELECT m.id, m.household_id, m.display_name, m.email, m.password_hash, m.role,
	       COALESCE(p.color, 'indigo') AS color, m.active, m.created_at, m.updated_at
	  FROM identity.member m
	  LEFT JOIN profile p ON p.member_id = m.id`

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
// identity.member.active column's own DEFAULT true); deactivation is
// SetActive's job, not Create's. The member row and its profile row (color)
// are inserted inside one transaction, opened over dbtx when it supports
// transactions (userTxBeginner) — the pgx.Tx case (Provisioner's own
// transaction) opens a nested transaction via savepoint instead. Returns
// domain.ErrDuplicateEmail when the email is already taken.
func (r *UserRepository) Create(ctx context.Context, u *domain.User) error {
	if u == nil {
		return errors.New("identity/adapter: create user: nil user")
	}
	beginner, ok := r.dbtx.(userTxBeginner)
	if !ok {
		return errors.New("identity/adapter: create user: executor does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("create user: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const memberQ = `
		INSERT INTO identity.member (id, household_id, display_name, email, password_hash, role)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING active, created_at, updated_at`
	if err := tx.QueryRow(ctx, memberQ,
		u.ID.String(), u.HouseholdID.String(), u.DisplayName, u.Email, u.PasswordHash, u.Role.String(),
	).Scan(&u.Active, &u.CreatedAt, &u.UpdatedAt); err != nil {
		if isDuplicateEmail(err) {
			return domain.ErrDuplicateEmail
		}
		return fmt.Errorf("create user: %w", err)
	}

	const profileQ = `INSERT INTO profile (member_id, color) VALUES ($1, $2)`
	if _, err := tx.Exec(ctx, profileQ, u.ID.String(), u.Color.String()); err != nil {
		return fmt.Errorf("create user: create profile: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("create user: commit: %w", err)
	}
	return nil
}

// FindByID returns the user, or domain.ErrUserNotFound.
func (r *UserRepository) FindByID(ctx context.Context, id domain.UserID) (*domain.User, error) {
	u, err := scanUser(r.dbtx.QueryRow(ctx, userColumns+` WHERE m.id = $1`, id.String()))
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
	u, err := scanUser(r.dbtx.QueryRow(ctx, userColumns+` WHERE m.email = $1`, email))
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
	rows, err := r.dbtx.Query(ctx, userColumns+` ORDER BY m.display_name, m.id`)
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
// SetActive own those, respectively. color lives on the app-side profile
// table (NSTR-116), so this runs two statements inside one transaction.
// Returns domain.ErrUserNotFound for an unknown id, or domain.ErrDuplicateEmail
// when the new email is already taken by a different user.
func (r *UserRepository) Update(ctx context.Context, u *domain.User) error {
	if u == nil {
		return errors.New("identity/adapter: update user: nil user")
	}
	beginner, ok := r.dbtx.(userTxBeginner)
	if !ok {
		return errors.New("identity/adapter: update user: executor does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("update user: begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	const memberQ = `
		UPDATE identity.member
		   SET display_name = $2, email = $3, role = $4, updated_at = now()
		 WHERE id = $1
		RETURNING updated_at`
	if err := tx.QueryRow(ctx, memberQ, u.ID.String(), u.DisplayName, u.Email, u.Role.String()).Scan(&u.UpdatedAt); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrUserNotFound
		}
		if isDuplicateEmail(err) {
			return domain.ErrDuplicateEmail
		}
		return fmt.Errorf("update user: %w", err)
	}

	// Upsert, not a bare UPDATE: a plain UPDATE matching no row (a member with
	// no profile row — see userColumns' own LEFT JOIN doc) returns a nil
	// error, which would report a successful color change that persisted
	// nothing.
	const profileQ = `
		INSERT INTO profile (member_id, color)
		VALUES ($1, $2)
		ON CONFLICT (member_id) DO UPDATE SET color = EXCLUDED.color`
	if _, err := tx.Exec(ctx, profileQ, u.ID.String(), u.Color.String()); err != nil {
		return fmt.Errorf("update user: update profile: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("update user: commit: %w", err)
	}
	return nil
}

// SetActive sets id's active flag, covering both deactivation (active=false)
// and NSTR-21's reactivation (active=true) with one method. Reactivation can
// never violate the last-active-admin invariant, so it runs a plain UPDATE;
// deactivation runs inside lastAdminGuardedUpdate, which rejects with
// domain.ErrLastActiveAdmin when id is the household's only active admin.
// Returns domain.ErrUserNotFound for an unknown id.
func (r *UserRepository) SetActive(ctx context.Context, id domain.UserID, active bool) error {
	const q = `UPDATE identity.member SET active = $2, updated_at = now() WHERE id = $1`
	if active {
		return execUserUpdate(ctx, r.dbtx, q, id, active)
	}
	return r.lastAdminGuardedUpdate(ctx, id, "set active", func(tx pgx.Tx) error {
		return execUserUpdate(ctx, tx, q, id, active)
	})
}

// SetRole changes id's role. Promoting to domain.RoleOwner can never violate
// the last-active-admin invariant, so it runs a plain UPDATE; any other role
// runs inside lastAdminGuardedUpdate, which rejects with
// domain.ErrLastActiveAdmin when id is the household's only active admin —
// covering both an explicit demotion and a no-op "set member to member".
// Returns domain.ErrUserNotFound for an unknown id.
func (r *UserRepository) SetRole(ctx context.Context, id domain.UserID, role domain.Role) error {
	const q = `UPDATE identity.member SET role = $2, updated_at = now() WHERE id = $1`
	if role == domain.RoleOwner {
		return execUserUpdate(ctx, r.dbtx, q, id, role.String())
	}
	return r.lastAdminGuardedUpdate(ctx, id, "set role", func(tx pgx.Tx) error {
		return execUserUpdate(ctx, tx, q, id, role.String())
	})
}

// SetPasswordHash overwrites id's stored password hash. Unlike Update, it
// touches nothing else, so a credential change can never blank or leak into
// the profile fields. Never risks the last-active-admin invariant, so it
// runs a plain UPDATE. Returns domain.ErrUserNotFound for an unknown id.
func (r *UserRepository) SetPasswordHash(ctx context.Context, id domain.UserID, hash string) error {
	const q = `UPDATE identity.member SET password_hash = $2, updated_at = now() WHERE id = $1`
	return execUserUpdate(ctx, r.dbtx, q, id, hash)
}

// execUserUpdate runs a single-row UPDATE keyed by id against dbtx (either
// r.dbtx or an open transaction) — the id/value UPDATE shape shared by
// SetActive, SetRole, and SetPasswordHash. Returns domain.ErrUserNotFound
// when no row matched id.
func execUserUpdate(ctx context.Context, dbtx db.TX, q string, id domain.UserID, value any) error {
	tag, err := dbtx.Exec(ctx, q, id.String(), value)
	if err != nil {
		return fmt.Errorf("update user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.ErrUserNotFound
	}
	return nil
}

// userTxBeginner is the slice of a pgx executor lastAdminGuardedUpdate needs
// to open its own transaction, satisfied by both *pgxpool.Pool and pgx.Tx
// (mirroring Nestova's mfaTxBeginner and this codebase's own Provisioner).
type userTxBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// lockActiveAdminIDsQuery locks every currently active admin row for the
// duration of the caller's transaction. Postgres rejects FOR UPDATE
// alongside an aggregate, so wouldRemoveLastActiveAdmin counts the rows in
// Go rather than via SQL count().
//
// The lock is the whole point, not an optimization: a second transaction's
// own SELECT ... FOR UPDATE over this same row set blocks until the first
// commits or rolls back, which is what serializes two concurrent demotions
// (or deactivations) of two DIFFERENT admins — the second one sees the
// first's committed result before deciding whether it would leave zero
// active admins.
const lockActiveAdminIDsQuery = `SELECT id FROM identity.member WHERE role = 'owner' AND active = true FOR UPDATE`

// lockActiveAdminIDs returns the ids of every currently active admin, row-locked
// for the duration of tx.
func lockActiveAdminIDs(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, lockActiveAdminIDsQuery)
	if err != nil {
		return nil, fmt.Errorf("lock active admin ids: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("lock active admin ids: scan: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("lock active admin ids: %w", err)
	}
	return ids, nil
}

// wouldRemoveLastActiveAdmin reports whether id is the one and only id in
// activeAdminIDs — i.e. whether taking id out of the active-admin set would
// leave the household with zero. A user who is not currently an active
// admin at all is never "the last one", regardless of what operation is
// being attempted on them.
func wouldRemoveLastActiveAdmin(activeAdminIDs []string, id string) bool {
	return len(activeAdminIDs) == 1 && activeAdminIDs[0] == id
}

// lastAdminGuardedUpdate runs update inside a transaction that first locks
// every active admin row (lockActiveAdminIDs) and rejects with
// domain.ErrLastActiveAdmin when id is the household's only one — shared by
// SetActive's deactivation branch and SetRole's demotion branch, the two
// mutations that can take an admin out of the active-admin set. op names the
// caller for error-wrapping context.
func (r *UserRepository) lastAdminGuardedUpdate(ctx context.Context, id domain.UserID, op string, update func(tx pgx.Tx) error) error {
	beginner, ok := r.dbtx.(userTxBeginner)
	if !ok {
		return fmt.Errorf("%s: executor does not support transactions", op)
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("%s: begin tx: %w", op, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	activeAdminIDs, err := lockActiveAdminIDs(ctx, tx)
	if err != nil {
		return fmt.Errorf("%s: %w", op, err)
	}
	if wouldRemoveLastActiveAdmin(activeAdminIDs, id.String()) {
		return domain.ErrLastActiveAdmin
	}

	if err := update(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("%s: commit: %w", op, err)
	}
	return nil
}

// Count returns the total number of users.
func (r *UserRepository) Count(ctx context.Context) (int, error) {
	const q = `SELECT count(*) FROM identity.member`
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
	const q = `SELECT EXISTS(SELECT 1 FROM identity.member)`
	var exists bool
	if err := r.dbtx.QueryRow(ctx, q).Scan(&exists); err != nil {
		return false, fmt.Errorf("has any user: %w", err)
	}
	return exists, nil
}

// isDuplicateEmail reports whether err is a unique-violation on
// member_email_unique specifically — other unique violations (e.g. the
// primary key) are left to surface as a wrapped error rather than
// misreported as a duplicate email.
func isDuplicateEmail(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == memberEmailUnique
}

// scanner abstracts pgx.Row and pgx.Rows for the shared scan helper.
type scanner interface {
	Scan(dest ...any) error
}

func scanUser(r scanner) (*domain.User, error) {
	var (
		u            domain.User
		idStr        string
		householdStr string
		role         string
		color        string
	)
	if err := r.Scan(&idStr, &householdStr, &u.DisplayName, &u.Email, &u.PasswordHash, &role, &color, &u.Active, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, err
	}
	id, err := domain.ParseUserID(idStr)
	if err != nil {
		return nil, fmt.Errorf("scan user: %w", err)
	}
	householdID, err := domain.ParseHouseholdID(householdStr)
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
	u.ID, u.HouseholdID, u.Role, u.Color = id, householdID, parsedRole, parsedColor
	return &u, nil
}
