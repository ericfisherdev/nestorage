package adapter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ericfisherdev/nestcore/db"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

const (
	// foreignKeyViolation is the PostgreSQL SQLSTATE for a foreign-key violation.
	foreignKeyViolation = "23503"
	// deviceTokenUserFK is device_token's Postgres-assigned default FK
	// constraint name (<table>_<column>_fkey — the 00004_device_token
	// migration does not name it explicitly, unlike app_user_email_unique).
	deviceTokenUserFK = "device_token_user_id_fkey"
)

// deviceTokenColumns is shared by every read query, keeping the column list
// and scanDeviceToken in lockstep.
const deviceTokenColumns = `SELECT id, user_id, token_hash, name, created_at, last_used_at, revoked_at FROM device_token`

// DeviceTokenRepository is the pgx-backed domain.DeviceTokenRepository.
// UUIDs are passed and scanned as text, matching the other Nestorage
// adapters.
type DeviceTokenRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.DeviceTokenRepository = (*DeviceTokenRepository)(nil)

// NewDeviceTokenRepository constructs the repository with an injected query
// executor.
func NewDeviceTokenRepository(dbtx db.TX) *DeviceTokenRepository {
	if dbtx == nil {
		panic("identity/adapter: NewDeviceTokenRepository requires a non-nil db.TX")
	}
	return &DeviceTokenRepository{dbtx: dbtx}
}

// Create inserts a device token and populates its CreatedAt, mapping an
// unknown user id to domain.ErrUserNotFound.
func (r *DeviceTokenRepository) Create(ctx context.Context, t *domain.DeviceToken) error {
	if t == nil {
		return errors.New("identity/adapter: create device token: nil token")
	}
	const q = `
		INSERT INTO device_token (id, user_id, token_hash, name)
		VALUES ($1, $2, $3, $4)
		RETURNING created_at`
	err := r.dbtx.QueryRow(ctx, q, t.ID.String(), t.UserID.String(), t.TokenHash, t.Name).Scan(&t.CreatedAt)
	if err != nil {
		if isDeviceTokenUserFKViolation(err) {
			return domain.ErrUserNotFound
		}
		return fmt.Errorf("create device token: %w", err)
	}
	return nil
}

// GetByTokenHash returns the token whose hash matches, regardless of
// revocation state. Returns domain.ErrDeviceTokenNotFound when no token
// matches.
func (r *DeviceTokenRepository) GetByTokenHash(ctx context.Context, tokenHash string) (*domain.DeviceToken, error) {
	t, err := scanDeviceToken(r.dbtx.QueryRow(ctx, deviceTokenColumns+` WHERE token_hash = $1`, tokenHash))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrDeviceTokenNotFound
		}
		return nil, fmt.Errorf("get device token by hash: %w", err)
	}
	return t, nil
}

// ListByUser returns userID's tokens newest first, or an empty slice when
// none exist.
func (r *DeviceTokenRepository) ListByUser(ctx context.Context, userID domain.UserID) ([]*domain.DeviceToken, error) {
	rows, err := r.dbtx.Query(ctx, deviceTokenColumns+` WHERE user_id = $1 ORDER BY created_at DESC`, userID.String())
	if err != nil {
		return nil, fmt.Errorf("list device tokens: %w", err)
	}
	defer rows.Close()

	tokens := make([]*domain.DeviceToken, 0)
	for rows.Next() {
		t, err := scanDeviceToken(rows)
		if err != nil {
			return nil, fmt.Errorf("list device tokens: scan: %w", err)
		}
		tokens = append(tokens, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list device tokens: %w", err)
	}
	return tokens, nil
}

// Revoke sets revoked_at for the token within userID. Returns
// domain.ErrDeviceTokenNotFound when id is unknown for that user or already
// revoked (the WHERE clause's revoked_at IS NULL guard is what makes a
// second Revoke on the same token report not-found instead of silently
// overwriting the original revocation timestamp) — the same shape as
// UserRepository's last-active-admin guarded updates use for "not found".
func (r *DeviceTokenRepository) Revoke(ctx context.Context, userID domain.UserID, id domain.DeviceTokenID, revokedAt time.Time) error {
	const q = `
		UPDATE device_token SET revoked_at = $3
		 WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL
		RETURNING id`
	var scanned string
	err := r.dbtx.QueryRow(ctx, q, id.String(), userID.String(), revokedAt).Scan(&scanned)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrDeviceTokenNotFound
		}
		return fmt.Errorf("revoke device token: %w", err)
	}
	return nil
}

// RevokeAllForUser revokes every currently active token for userID and
// reports how many were revoked. "Nothing to revoke" is success (0, nil),
// not an error — NSTR-21's Deactivate (via DeviceTokenService.RevokeAll)
// calls this unconditionally.
func (r *DeviceTokenRepository) RevokeAllForUser(ctx context.Context, userID domain.UserID, revokedAt time.Time) (int64, error) {
	const q = `UPDATE device_token SET revoked_at = $2 WHERE user_id = $1 AND revoked_at IS NULL`
	tag, err := r.dbtx.Exec(ctx, q, userID.String(), revokedAt)
	if err != nil {
		return 0, fmt.Errorf("revoke all device tokens: %w", err)
	}
	return tag.RowsAffected(), nil
}

// TouchLastUsed sets last_used_at to now for id, but only when the existing
// value is NULL or older than staleBefore — throttling the write in the
// statement itself rather than in Go, so there is no read-modify-write race
// and no write at all on the common (already-fresh) path. A missing id is
// not an error: an authentication path must never fail because this
// best-effort bookkeeping found nothing to touch.
func (r *DeviceTokenRepository) TouchLastUsed(ctx context.Context, id domain.DeviceTokenID, now, staleBefore time.Time) error {
	const q = `
		UPDATE device_token SET last_used_at = $2
		 WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < $3)`
	if _, err := r.dbtx.Exec(ctx, q, id.String(), now, staleBefore); err != nil {
		return fmt.Errorf("touch device token last used: %w", err)
	}
	return nil
}

// isDeviceTokenUserFKViolation reports whether err is a foreign-key
// violation on device_token_user_id_fkey specifically.
func isDeviceTokenUserFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == foreignKeyViolation && pgErr.ConstraintName == deviceTokenUserFK
}

func scanDeviceToken(r scanner) (*domain.DeviceToken, error) {
	var (
		t              domain.DeviceToken
		idStr, userStr string
	)
	if err := r.Scan(&idStr, &userStr, &t.TokenHash, &t.Name, &t.CreatedAt, &t.LastUsedAt, &t.RevokedAt); err != nil {
		return nil, err
	}
	id, err := domain.ParseDeviceTokenID(idStr)
	if err != nil {
		return nil, fmt.Errorf("parse device token id: %w", err)
	}
	userID, err := domain.ParseUserID(userStr)
	if err != nil {
		return nil, fmt.Errorf("parse device token user id: %w", err)
	}
	t.ID = id
	t.UserID = userID
	return &t, nil
}
