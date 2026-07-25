package adapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ericfisherdev/nestcore/db"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/labels/domain"
)

// SizePreferenceRepository is the pgx-backed implementation of
// domain.SizePreferenceRepository. UUIDs are passed and scanned as text,
// matching every other adapter in this codebase.
type SizePreferenceRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port it exists to
// implement.
var _ domain.SizePreferenceRepository = (*SizePreferenceRepository)(nil)

// NewSizePreferenceRepository constructs the repository with an injected
// query executor, always the shared pool at composition-root time — a
// label size preference write is never part of another aggregate's
// transaction, the same non-transactional shape notify/adapter's own
// PreferenceRepository already has.
func NewSizePreferenceRepository(dbtx db.TX) *SizePreferenceRepository {
	if dbtx == nil {
		panic("labels/adapter: NewSizePreferenceRepository requires a non-nil db.TX")
	}
	return &SizePreferenceRepository{dbtx: dbtx}
}

// Get returns userID's remembered size id, or domain.ErrPreferenceNotFound
// when userID has never printed a label.
func (r *SizePreferenceRepository) Get(ctx context.Context, userID identity.UserID) (domain.LabelSizeID, error) {
	const q = `SELECT size_id FROM label_preference WHERE user_id = $1`
	var sizeID string
	err := r.dbtx.QueryRow(ctx, q, userID.String()).Scan(&sizeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", domain.ErrPreferenceNotFound
	}
	if err != nil {
		return "", fmt.Errorf("get label size preference: %w", err)
	}
	return domain.LabelSizeID(sizeID), nil
}

// Upsert persists sizeID as userID's remembered size, replacing any
// existing row for the same user — the primary key is the natural upsert
// target, so this is always a single-statement upsert, never a
// read-modify-write.
func (r *SizePreferenceRepository) Upsert(ctx context.Context, userID identity.UserID, sizeID domain.LabelSizeID) error {
	const q = `
		INSERT INTO label_preference (user_id, size_id, updated_at)
		VALUES ($1, $2, now())
		ON CONFLICT (user_id)
		DO UPDATE SET size_id = EXCLUDED.size_id, updated_at = EXCLUDED.updated_at`
	if _, err := r.dbtx.Exec(ctx, q, userID.String(), sizeID.String()); err != nil {
		return fmt.Errorf("upsert label size preference: %w", err)
	}
	return nil
}
