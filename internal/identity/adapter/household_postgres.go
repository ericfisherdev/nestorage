package adapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/ericfisherdev/nestcore/db"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// householdColumns is shared by List, keeping the column list and
// scanHousehold in lockstep.
const householdColumns = `SELECT id, name, created_at, updated_at FROM identity.household`

// HouseholdRepository is the pgx-backed domain.HouseholdRepository.
type HouseholdRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.HouseholdRepository = (*HouseholdRepository)(nil)

// NewHouseholdRepository constructs the repository with an injected query
// executor, mirroring NewUserRepository's dbtx seam so the same code runs
// both directly against the pool and inside a transaction.
func NewHouseholdRepository(dbtx db.TX) *HouseholdRepository {
	if dbtx == nil {
		panic("identity/adapter: NewHouseholdRepository requires a non-nil db.TX")
	}
	return &HouseholdRepository{dbtx: dbtx}
}

// List returns every household, ordered by created_at so the household
// attachment rule (household_resolve.go) sees a stable order. Returns an
// empty slice, not an error, when none exist.
func (r *HouseholdRepository) List(ctx context.Context) ([]domain.Household, error) {
	rows, err := r.dbtx.Query(ctx, householdColumns+` ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("list households: %w", err)
	}
	defer rows.Close()

	households := make([]domain.Household, 0)
	for rows.Next() {
		h, err := scanHousehold(rows)
		if err != nil {
			return nil, fmt.Errorf("list households: scan: %w", err)
		}
		households = append(households, *h)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list households: %w", err)
	}
	return households, nil
}

// Create inserts a household and populates its timestamps.
func (r *HouseholdRepository) Create(ctx context.Context, h *domain.Household) error {
	if h == nil {
		return errors.New("identity/adapter: create household: nil household")
	}
	const q = `
		INSERT INTO identity.household (id, name)
		VALUES ($1, $2)
		RETURNING created_at, updated_at`
	err := r.dbtx.QueryRow(ctx, q, h.ID.String(), h.Name).Scan(&h.CreatedAt, &h.UpdatedAt)
	if err != nil {
		return fmt.Errorf("create household: %w", err)
	}
	return nil
}

// scanner is declared in postgres.go; reused here for the row/rows dual scan.
func scanHousehold(r scanner) (*domain.Household, error) {
	var (
		h     domain.Household
		idStr string
	)
	if err := r.Scan(&idStr, &h.Name, &h.CreatedAt, &h.UpdatedAt); err != nil {
		return nil, err
	}
	id, err := domain.ParseHouseholdID(idStr)
	if err != nil {
		return nil, fmt.Errorf("scan household: %w", err)
	}
	h.ID = id
	return &h, nil
}

// resolveHouseholdForSetup implements NSTR-116's first-run household
// attachment rule: adopt the single existing identity.household when exactly
// one exists (the shared-box case, where Nestova already created it); create
// one when none exists (a Nestorage-only install); fail loudly
// (ErrAmbiguousHousehold) when several exist rather than guessing.
func resolveHouseholdForSetup(ctx context.Context, repo domain.HouseholdRepository) (domain.HouseholdID, error) {
	households, err := repo.List(ctx)
	if err != nil {
		return domain.HouseholdID{}, fmt.Errorf("resolve household: %w", err)
	}
	switch len(households) {
	case 0:
		h := &domain.Household{ID: domain.NewHouseholdID(), Name: "Household"}
		if err := repo.Create(ctx, h); err != nil {
			return domain.HouseholdID{}, fmt.Errorf("resolve household: create: %w", err)
		}
		return h.ID, nil
	case 1:
		return households[0].ID, nil
	default:
		return domain.HouseholdID{}, domain.ErrAmbiguousHousehold
	}
}
