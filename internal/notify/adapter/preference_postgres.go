package adapter

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ericfisherdev/nestcore/db"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/notify/domain"
)

// preferenceColumns lists notification_preference's own columns in the
// exact order scanPreference expects — shared by every read query, the
// same convention notificationColumns already follows in this package.
const preferenceColumns = `user_id, event_type, channel, enabled`

// PreferenceRepository is the pgx-backed implementation of
// domain.PreferenceRepository. UUIDs are passed and scanned as text,
// matching every other adapter in this codebase.
type PreferenceRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port it exists to
// implement.
var _ domain.PreferenceRepository = (*PreferenceRepository)(nil)

// NewPreferenceRepository constructs the repository with an injected query
// executor, always the shared pool at composition-root time — a
// preference write is never part of another aggregate's transaction, the
// same non-transactional shape NotificationRepository already has.
func NewPreferenceRepository(dbtx db.TX) *PreferenceRepository {
	if dbtx == nil {
		panic("notify/adapter: NewPreferenceRepository requires a non-nil db.TX")
	}
	return &PreferenceRepository{dbtx: dbtx}
}

// ListForUser returns every stored row for userID, across every event type
// and channel.
func (r *PreferenceRepository) ListForUser(ctx context.Context, userID identity.UserID) ([]domain.Preference, error) {
	const q = `SELECT ` + preferenceColumns + ` FROM notification_preference WHERE user_id = $1`
	rows, err := r.dbtx.Query(ctx, q, userID.String())
	if err != nil {
		return nil, fmt.Errorf("list preferences for user: %w", err)
	}
	defer rows.Close()
	return scanPreferences(rows)
}

// Get returns userID's stored rows for eventType only.
func (r *PreferenceRepository) Get(ctx context.Context, userID identity.UserID, eventType domain.EventType) ([]domain.Preference, error) {
	const q = `SELECT ` + preferenceColumns + ` FROM notification_preference WHERE user_id = $1 AND event_type = $2`
	rows, err := r.dbtx.Query(ctx, q, userID.String(), eventType.String())
	if err != nil {
		return nil, fmt.Errorf("get preferences: %w", err)
	}
	defer rows.Close()
	return scanPreferences(rows)
}

// Upsert persists p, replacing any existing row for the same
// (user_id, event_type, channel) key — the primary key is the natural
// ON CONFLICT target, so this is always a single-statement upsert, never a
// read-modify-write.
func (r *PreferenceRepository) Upsert(ctx context.Context, p domain.Preference) error {
	const q = `
		INSERT INTO notification_preference (user_id, event_type, channel, enabled, updated_at)
		VALUES ($1, $2, $3, $4, now())
		ON CONFLICT (user_id, event_type, channel)
		DO UPDATE SET enabled = EXCLUDED.enabled, updated_at = EXCLUDED.updated_at`
	if _, err := r.dbtx.Exec(ctx, q, p.UserID.String(), p.EventType.String(), p.Channel.String(), p.Enabled); err != nil {
		return fmt.Errorf("upsert preference: %w", err)
	}
	return nil
}

// scanPreferences drains rows into a []domain.Preference, returning an
// empty (never nil) slice for zero rows — ListForUser/Get's own "empty,
// not an error" contract.
func scanPreferences(rows pgx.Rows) ([]domain.Preference, error) {
	preferences := make([]domain.Preference, 0)
	for rows.Next() {
		p, err := scanPreference(rows)
		if err != nil {
			return nil, fmt.Errorf("scan preference: %w", err)
		}
		preferences = append(preferences, *p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan preferences: %w", err)
	}
	return preferences, nil
}

// scanPreference scans one row into a domain.Preference, parsing every
// stored text column through its own domain Parse function so an
// unrecognized value fails loudly here rather than propagating a raw
// string past the domain boundary.
func scanPreference(r scanner) (*domain.Preference, error) {
	var userIDStr, eventTypeStr, channelStr string
	var p domain.Preference
	if err := r.Scan(&userIDStr, &eventTypeStr, &channelStr, &p.Enabled); err != nil {
		return nil, err
	}

	userID, err := identity.ParseUserID(userIDStr)
	if err != nil {
		return nil, fmt.Errorf("user id: %w", err)
	}
	eventType, err := domain.ParseEventType(eventTypeStr)
	if err != nil {
		return nil, fmt.Errorf("event type: %w", err)
	}
	channel, err := domain.ParseChannel(channelStr)
	if err != nil {
		return nil, fmt.Errorf("channel: %w", err)
	}
	p.UserID, p.EventType, p.Channel = userID, eventType, channel
	return &p, nil
}
