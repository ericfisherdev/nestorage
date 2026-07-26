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

// Named explicitly in 00017_provider_member_link.sql so this adapter can
// match pgconn.PgError.ConstraintName instead of parsing messages — the
// same convention appUserEmailUnique/apiKeyCurrentUniq already follow.
const (
	providerMemberLinkMemberIDUniq = "provider_member_link_member_id_uniq"
	providerMemberLinkUserIDUniq   = "provider_member_link_user_id_uniq"
)

// memberLinkColumns is shared by every read query, keeping the column list
// and scanMemberLink in lockstep.
const memberLinkColumns = `SELECT id, user_id, member_id, household_id, linked_at FROM provider_member_link`

// MemberLinkRepository is the pgx-backed domain.MemberLinkRepository. UUIDs
// are passed and scanned as text, matching every other Nestorage adapter.
type MemberLinkRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.MemberLinkRepository = (*MemberLinkRepository)(nil)

// NewMemberLinkRepository constructs the repository with an injected query
// executor — the same db.TX seam NewUserRepository uses, satisfied by both
// *pgxpool.Pool and pgx.Tx, so FederationProvisioner can compose it into its
// own transaction.
func NewMemberLinkRepository(dbtx db.TX) *MemberLinkRepository {
	if dbtx == nil {
		panic("identity/adapter: NewMemberLinkRepository requires a non-nil db.TX")
	}
	return &MemberLinkRepository{dbtx: dbtx}
}

// Create inserts a member link and populates its LinkedAt, mapping a
// unique-violation on provider_member_link_member_id_uniq to
// domain.ErrMemberAlreadyLinked and on provider_member_link_user_id_uniq to
// domain.ErrUserAlreadyLinked.
func (r *MemberLinkRepository) Create(ctx context.Context, link *domain.MemberLink) error {
	if link == nil {
		return errors.New("identity/adapter: create member link: nil link")
	}
	const q = `
		INSERT INTO provider_member_link (id, user_id, member_id, household_id)
		VALUES ($1, $2, $3, $4)
		RETURNING linked_at`
	err := r.dbtx.QueryRow(ctx, q, link.ID.String(), link.UserID.String(), link.MemberID, link.HouseholdID).
		Scan(&link.LinkedAt)
	if err != nil {
		switch memberLinkUniqueViolation(err) {
		case providerMemberLinkMemberIDUniq:
			return domain.ErrMemberAlreadyLinked
		case providerMemberLinkUserIDUniq:
			return domain.ErrUserAlreadyLinked
		default:
			return fmt.Errorf("create member link: %w", err)
		}
	}
	return nil
}

// FindByMemberID returns the link, or domain.ErrMemberLinkNotFound.
func (r *MemberLinkRepository) FindByMemberID(ctx context.Context, memberID string) (*domain.MemberLink, error) {
	link, err := scanMemberLink(r.dbtx.QueryRow(ctx, memberLinkColumns+` WHERE member_id = $1`, memberID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, domain.ErrMemberLinkNotFound
		}
		return nil, fmt.Errorf("find member link by member id: %w", err)
	}
	return link, nil
}

// List returns every recorded link, oldest first. Returns an empty slice,
// not an error, when no links exist.
func (r *MemberLinkRepository) List(ctx context.Context) ([]domain.MemberLink, error) {
	rows, err := r.dbtx.Query(ctx, memberLinkColumns+` ORDER BY linked_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list member links: %w", err)
	}
	defer rows.Close()

	links := make([]domain.MemberLink, 0)
	for rows.Next() {
		link, err := scanMemberLink(rows)
		if err != nil {
			return nil, fmt.Errorf("list member links: scan: %w", err)
		}
		links = append(links, *link)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list member links: %w", err)
	}
	return links, nil
}

// memberLinkUniqueViolation returns the offending constraint name for a
// unique-violation, or "" for any other error (including a non-violation
// error), so Create's switch can distinguish which of the two named
// constraints fired.
func memberLinkUniqueViolation(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return pgErr.ConstraintName
	}
	return ""
}

func scanMemberLink(r scanner) (*domain.MemberLink, error) {
	var (
		link    domain.MemberLink
		idStr   string
		userStr string
	)
	if err := r.Scan(&idStr, &userStr, &link.MemberID, &link.HouseholdID, &link.LinkedAt); err != nil {
		return nil, err
	}
	id, err := domain.ParseMemberLinkID(idStr)
	if err != nil {
		return nil, fmt.Errorf("scan member link: %w", err)
	}
	userID, err := domain.ParseUserID(userStr)
	if err != nil {
		return nil, fmt.Errorf("scan member link: %w", err)
	}
	link.ID, link.UserID = id, userID
	return &link, nil
}

// HouseholdBindingRepository is the pgx-backed
// domain.HouseholdBindingRepository, over the single-row federation_binding
// table.
type HouseholdBindingRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.HouseholdBindingRepository = (*HouseholdBindingRepository)(nil)

// NewHouseholdBindingRepository constructs the repository with an injected
// query executor, the same db.TX seam MemberLinkRepository uses.
func NewHouseholdBindingRepository(dbtx db.TX) *HouseholdBindingRepository {
	if dbtx == nil {
		panic("identity/adapter: NewHouseholdBindingRepository requires a non-nil db.TX")
	}
	return &HouseholdBindingRepository{dbtx: dbtx}
}

// Get returns the current binding and true, or a zero HouseholdBinding and
// false when the table is still empty (no attach call has ever landed).
func (r *HouseholdBindingRepository) Get(ctx context.Context) (domain.HouseholdBinding, bool, error) {
	const q = `SELECT household_id, bound_at FROM federation_binding`
	var b domain.HouseholdBinding
	err := r.dbtx.QueryRow(ctx, q).Scan(&b.HouseholdID, &b.BoundAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.HouseholdBinding{}, false, nil
		}
		return domain.HouseholdBinding{}, false, fmt.Errorf("get household binding: %w", err)
	}
	return b, true, nil
}

// Record writes householdID as the binding. Only ever called (via
// FederationProvisioner.checkBinding) when Get has already reported no
// binding exists, so there is nothing here to conflict with — the table
// starts empty and this is its only insert.
func (r *HouseholdBindingRepository) Record(ctx context.Context, householdID string) (domain.HouseholdBinding, error) {
	const q = `
		INSERT INTO federation_binding (household_id)
		VALUES ($1)
		RETURNING household_id, bound_at`
	var b domain.HouseholdBinding
	if err := r.dbtx.QueryRow(ctx, q, householdID).Scan(&b.HouseholdID, &b.BoundAt); err != nil {
		return domain.HouseholdBinding{}, fmt.Errorf("record household binding: %w", err)
	}
	return b, nil
}
