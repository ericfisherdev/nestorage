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

// apiKeyCurrentUniq is api_key_current_uniq's index name — enforced by the
// 00005_api_key migration's partial unique index, and Postgres reports the
// index name as pgconn.PgError.ConstraintName for a unique_violation raised
// against it, the same as for a named UNIQUE CONSTRAINT.
const apiKeyCurrentUniq = "api_key_current_uniq"

// apiKeyLockNamespace is the first key of Rotate's pg_advisory_xact_lock,
// serializing concurrent rotations — see kioskHouseholdLockNamespace's own
// doc (Nestova's internal/kiosk/adapter/activation_code_postgres.go) for why
// the two-integer form is used (a private lock space distinct from any
// single-bigint advisory lock elsewhere in the app). Chosen as the ASCII
// bytes of "NSAK" ("Nestorage API Key"), purely as a memorable,
// human-readable tag.
const apiKeyLockNamespace int32 = 0x4E53414B

// apiKeyLockKey is Rotate's second lock key. Unlike Nestova's per-household
// hashtext(household_id) second key, there is only ever one account api
// key, so a single fixed value is enough to serialize every rotation
// against every other one.
const apiKeyLockKey int32 = 0

// apiKeyColumns is shared by every read query, keeping the column list and
// scanAPIKey in lockstep.
const apiKeyColumns = `SELECT id, key_prefix, secret_hash, label, created_at, last_used_at, expires_at, revoked_at FROM api_key`

// APIKeyRepository is the pgx-backed domain.APIKeyRepository. UUIDs are
// passed and scanned as text, matching the other Nestorage adapters.
type APIKeyRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.APIKeyRepository = (*APIKeyRepository)(nil)

// NewAPIKeyRepository constructs the repository with an injected query
// executor.
func NewAPIKeyRepository(dbtx db.TX) *APIKeyRepository {
	if dbtx == nil {
		panic("identity/adapter: NewAPIKeyRepository requires a non-nil db.TX")
	}
	return &APIKeyRepository{dbtx: dbtx}
}

// Create inserts an api key and populates its CreatedAt, mapping a
// unique-violation on api_key_current_uniq (a current key already exists)
// to domain.ErrAPIKeyExists.
func (r *APIKeyRepository) Create(ctx context.Context, k *domain.APIKey) error {
	if k == nil {
		return errors.New("identity/adapter: create api key: nil key")
	}
	const q = `
		INSERT INTO api_key (id, key_prefix, secret_hash, label)
		VALUES ($1, $2, $3, $4)
		RETURNING created_at`
	err := r.dbtx.QueryRow(ctx, q, k.ID.String(), k.KeyPrefix, k.SecretHash, k.Label).Scan(&k.CreatedAt)
	if err != nil {
		if isAPIKeyCurrentUniqViolation(err) {
			return domain.ErrAPIKeyExists
		}
		return fmt.Errorf("create api key: %w", err)
	}
	return nil
}

// GetBySecretHash returns the key whose hash matches, regardless of
// revocation/expiry state. Returns domain.ErrAPIKeyNotFound when no key
// matches.
func (r *APIKeyRepository) GetBySecretHash(ctx context.Context, secretHash string) (*domain.APIKey, error) {
	k, err := scanAPIKey(r.dbtx.QueryRow(ctx, apiKeyColumns+` WHERE secret_hash = $1`, secretHash))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrAPIKeyNotFound
		}
		return nil, fmt.Errorf("get api key by hash: %w", err)
	}
	return k, nil
}

// ListAll returns every key newest first, or an empty slice when none
// exist.
func (r *APIKeyRepository) ListAll(ctx context.Context) ([]*domain.APIKey, error) {
	rows, err := r.dbtx.Query(ctx, apiKeyColumns+` ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()

	keys := make([]*domain.APIKey, 0)
	for rows.Next() {
		k, err := scanAPIKey(rows)
		if err != nil {
			return nil, fmt.Errorf("list api keys: scan: %w", err)
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	return keys, nil
}

// Rotate atomically supersedes incumbentID (if non-zero) and inserts next —
// see domain.APIKeyRepository's own doc for the full contract. Mirrors
// Nestova's ActivationCodeRepository.Redeem: a transaction opened directly
// against the pool this repository was constructed with, an advisory lock
// serializing concurrent rotations against each other (a concurrent Create
// is still correctly rejected by api_key_current_uniq without needing the
// same lock — see apiKeyLockNamespace's own doc), and a single commit. If
// the insert fails, the whole transaction rolls back, so the incumbent (if
// any) is NOT left superseded.
func (r *APIKeyRepository) Rotate(ctx context.Context, incumbentID domain.APIKeyID, expiresAt time.Time, next *domain.APIKey) error {
	if next == nil {
		return errors.New("identity/adapter: rotate api key: nil next key")
	}

	beginner, ok := r.dbtx.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		return errors.New("rotate api key: executor does not support transactions")
	}
	tx, err := beginner.Begin(ctx)
	if err != nil {
		return fmt.Errorf("rotate api key: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1, $2)`, apiKeyLockNamespace, apiKeyLockKey); err != nil {
		return fmt.Errorf("rotate api key: acquire lock: %w", err)
	}

	var zero domain.APIKeyID
	if incumbentID != zero {
		const supersede = `
			UPDATE api_key SET expires_at = $2
			 WHERE id = $1 AND revoked_at IS NULL
			RETURNING id`
		var scanned string
		if err := tx.QueryRow(ctx, supersede, incumbentID.String(), expiresAt).Scan(&scanned); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return domain.ErrAPIKeyNotFound
			}
			return fmt.Errorf("rotate api key: supersede incumbent: %w", err)
		}
	}

	const insert = `
		INSERT INTO api_key (id, key_prefix, secret_hash, label)
		VALUES ($1, $2, $3, $4)
		RETURNING created_at`
	if err := tx.QueryRow(ctx, insert, next.ID.String(), next.KeyPrefix, next.SecretHash, next.Label).Scan(&next.CreatedAt); err != nil {
		return fmt.Errorf("rotate api key: insert: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("rotate api key: commit: %w", err)
	}
	return nil
}

// Revoke sets revoked_at for id. Returns domain.ErrAPIKeyNotFound when id is
// unknown or already revoked (the WHERE clause's revoked_at IS NULL guard
// is what makes a second Revoke on the same key report not-found instead of
// silently overwriting the original revocation timestamp) — the same shape
// as DeviceTokenRepository.Revoke.
func (r *APIKeyRepository) Revoke(ctx context.Context, id domain.APIKeyID, revokedAt time.Time) error {
	const q = `
		UPDATE api_key SET revoked_at = $2
		 WHERE id = $1 AND revoked_at IS NULL
		RETURNING id`
	var scanned string
	err := r.dbtx.QueryRow(ctx, q, id.String(), revokedAt).Scan(&scanned)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.ErrAPIKeyNotFound
		}
		return fmt.Errorf("revoke api key: %w", err)
	}
	return nil
}

// TouchLastUsed sets last_used_at to now for id, but only when the existing
// value is NULL or older than staleBefore — throttling the write in the
// statement itself rather than in Go, so there is no read-modify-write race
// and no write at all on the common (already-fresh) path. A missing id is
// not an error: an authentication path must never fail because this
// best-effort bookkeeping found nothing to touch. A failure here is logged
// and swallowed by the caller (APIKeyService.touchLastUsed) — a stale
// last-used timestamp must never fail an otherwise valid API request.
func (r *APIKeyRepository) TouchLastUsed(ctx context.Context, id domain.APIKeyID, now, staleBefore time.Time) error {
	const q = `
		UPDATE api_key SET last_used_at = $2
		 WHERE id = $1 AND (last_used_at IS NULL OR last_used_at < $3)`
	if _, err := r.dbtx.Exec(ctx, q, id.String(), now, staleBefore); err != nil {
		return fmt.Errorf("touch api key last used: %w", err)
	}
	return nil
}

// isAPIKeyCurrentUniqViolation reports whether err is a unique-violation on
// api_key_current_uniq specifically — other unique violations (e.g. a
// secret_hash collision, astronomically unlikely at 256 bits of
// crypto/rand) are left to surface as a wrapped error rather than
// misreported as ErrAPIKeyExists.
func isAPIKeyCurrentUniqViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == uniqueViolation && pgErr.ConstraintName == apiKeyCurrentUniq
}

func scanAPIKey(r scanner) (*domain.APIKey, error) {
	var (
		k     domain.APIKey
		idStr string
	)
	if err := r.Scan(&idStr, &k.KeyPrefix, &k.SecretHash, &k.Label, &k.CreatedAt, &k.LastUsedAt, &k.ExpiresAt, &k.RevokedAt); err != nil {
		return nil, err
	}
	id, err := domain.ParseAPIKeyID(idStr)
	if err != nil {
		return nil, fmt.Errorf("parse api key id: %w", err)
	}
	k.ID = id
	return &k, nil
}
