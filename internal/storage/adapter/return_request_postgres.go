package adapter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ericfisherdev/nestcore/db"

	identity "github.com/ericfisherdev/nestorage/internal/identity/domain"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// Explicit constraint names from 00013_return_request.sql. Named (matching
// bin/item's own explicit constraints) so Create can tell apart the
// duplicate-open-request guard from the row's three foreign keys.
const (
	returnRequestOpenUniqueConstraint  = "return_request_open_uniq"
	returnRequestItemFKConstraint      = "return_request_item_id_fkey"
	returnRequestRequesterFKConstraint = "return_request_requester_id_fkey"
	returnRequestHolderFKConstraint    = "return_request_holder_id_fkey"
)

// returnRequestReturningColumns lists return_request's own columns in the
// exact order scanReturnRequest expects — shared by ListByItem's SELECT and
// Cancel/FulfillOpenForItem's RETURNING clauses so all three stay in
// lockstep with the scan function.
const returnRequestReturningColumns = `id, item_id, requester_id, holder_id, message, status, created_at, resolved_at`

// returnRequestColumns is ListByItem's own SELECT, built from
// returnRequestReturningColumns.
const returnRequestColumns = `SELECT ` + returnRequestReturningColumns + ` FROM return_request`

// ReturnRequestRepository is the pgx-backed domain.ReturnRequestRepository.
// UUIDs are passed and scanned as text, matching every other adapter in
// this package, so no pgx UUID codec registration is required.
type ReturnRequestRepository struct {
	dbtx db.TX
}

// Compile-time assurance the adapter satisfies the port.
var _ domain.ReturnRequestRepository = (*ReturnRequestRepository)(nil)

// NewReturnRequestRepository constructs the repository with an injected
// query executor. Pass a pgx.Tx (rather than the pool) when the caller
// needs Create/Cancel to share a transaction with the item_event row that
// records it (app.ReturnRequestService's own returnRequestTransactor), or
// FulfillOpenForItem to share OperationService's placement-swap transaction
// (app.OperationStores.ReturnRequests) — mirrors NewItemEventRepository's
// own doc.
func NewReturnRequestRepository(dbtx db.TX) *ReturnRequestRepository {
	if dbtx == nil {
		panic("storage/adapter: NewReturnRequestRepository requires a non-nil db.TX")
	}
	return &ReturnRequestRepository{dbtx: dbtx}
}

// Create inserts r and populates its CreatedAt. The caller supplies a
// validated r.Message (see domain.ValidateReturnRequestMessage) and
// r.Status left at domain.ReturnRequestStatusOpen (the column's own
// DEFAULT, not re-sent); Create does not re-validate the message in Go,
// relying on the database CHECK, but still maps the duplicate-open-request
// unique violation and the row's three foreign-key violations back to their
// matching domain sentinels.
func (r *ReturnRequestRepository) Create(ctx context.Context, req *domain.ReturnRequest) error {
	if req == nil {
		return errors.New("storage/adapter: create return request: nil request")
	}
	const q = `
		INSERT INTO return_request (id, item_id, requester_id, holder_id, message)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at`
	err := r.dbtx.QueryRow(ctx, q,
		req.ID.String(), req.ItemID.String(), req.RequesterID.String(), req.HolderID.String(), req.Message,
	).Scan(&req.CreatedAt)
	if err != nil {
		switch {
		case isPgConstraint(err, uniqueViolation, returnRequestOpenUniqueConstraint):
			return domain.ErrDuplicateReturnRequest
		case isPgConstraint(err, foreignKeyViolation, returnRequestItemFKConstraint):
			return domain.ErrItemNotFound
		case isPgConstraint(err, foreignKeyViolation, returnRequestRequesterFKConstraint),
			isPgConstraint(err, foreignKeyViolation, returnRequestHolderFKConstraint):
			return identity.ErrUserNotFound
		}
		return fmt.Errorf("create return request: %w", err)
	}
	req.Status = domain.ReturnRequestStatusOpen
	return nil
}

// ListByItem returns itemID's requests (every status) newest-first
// (created_at DESC, id DESC). Returns an empty slice, not an error, when
// itemID has none.
func (r *ReturnRequestRepository) ListByItem(ctx context.Context, itemID domain.ItemID) ([]domain.ReturnRequest, error) {
	q := returnRequestColumns + ` WHERE item_id = $1 ORDER BY created_at DESC, id DESC`
	rows, err := r.dbtx.Query(ctx, q, itemID.String())
	if err != nil {
		return nil, fmt.Errorf("list return requests: %w", err)
	}
	defer rows.Close()

	requests := make([]domain.ReturnRequest, 0)
	for rows.Next() {
		rr, err := scanReturnRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("list return requests: scan: %w", err)
		}
		requests = append(requests, *rr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list return requests: %w", err)
	}
	return requests, nil
}

// Cancel withdraws id on requesterID's behalf: the UPDATE's own WHERE
// clause enforces ownership (id AND requester_id) and openness (status =
// 'open') in one statement — the "ownership enforced in the SQL" the
// ticket calls for. When no row matches, cancelNotFoundOrNotOpen issues one
// cheap follow-up read to tell "unknown id or wrong requester" apart from
// "found, owned, but already resolved", since those two cases map to
// different sentinels (ErrReturnRequestNotFound vs
// ErrReturnRequestNotOpen) that a single UPDATE...RETURNING cannot
// distinguish on its own.
func (r *ReturnRequestRepository) Cancel(ctx context.Context, id domain.ReturnRequestID, requesterID identity.UserID) (*domain.ReturnRequest, error) {
	const q = `
		UPDATE return_request
		SET status = 'cancelled', resolved_at = now()
		WHERE id = $1 AND requester_id = $2 AND status = 'open'
		RETURNING ` + returnRequestReturningColumns
	rr, err := scanReturnRequest(r.dbtx.QueryRow(ctx, q, id.String(), requesterID.String()))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, r.cancelNotFoundOrNotOpen(ctx, id, requesterID)
		}
		return nil, fmt.Errorf("cancel return request: %w", err)
	}
	return rr, nil
}

// cancelNotFoundOrNotOpen resolves Cancel's own ambiguous zero-rows case: a
// plain read (no ownership-narrowed UPDATE this time) against the same id +
// requesterID pair, so an absent row (unknown id, or one belonging to a
// different requester — masked identically) is told apart from a present,
// owned, but no-longer-open one.
func (r *ReturnRequestRepository) cancelNotFoundOrNotOpen(ctx context.Context, id domain.ReturnRequestID, requesterID identity.UserID) error {
	const q = `SELECT status FROM return_request WHERE id = $1 AND requester_id = $2`
	var status string
	err := r.dbtx.QueryRow(ctx, q, id.String(), requesterID.String()).Scan(&status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return domain.ErrReturnRequestNotFound
	case err != nil:
		return fmt.Errorf("cancel return request: check status: %w", err)
	default:
		return domain.ErrReturnRequestNotOpen
	}
}

// FulfillOpenForItem flips every open request on itemID to fulfilled at at,
// returning the flipped rows so the caller's notifier can fan out without a
// second query. Returns an empty slice, not an error, when itemID has no
// open requests.
func (r *ReturnRequestRepository) FulfillOpenForItem(ctx context.Context, itemID domain.ItemID, at time.Time) ([]domain.ReturnRequest, error) {
	const q = `
		UPDATE return_request
		SET status = 'fulfilled', resolved_at = $2
		WHERE item_id = $1 AND status = 'open'
		RETURNING ` + returnRequestReturningColumns
	rows, err := r.dbtx.Query(ctx, q, itemID.String(), at)
	if err != nil {
		return nil, fmt.Errorf("fulfill open return requests: %w", err)
	}
	defer rows.Close()

	fulfilled := make([]domain.ReturnRequest, 0)
	for rows.Next() {
		rr, err := scanReturnRequest(rows)
		if err != nil {
			return nil, fmt.Errorf("fulfill open return requests: scan: %w", err)
		}
		fulfilled = append(fulfilled, *rr)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("fulfill open return requests: %w", err)
	}
	return fulfilled, nil
}

func scanReturnRequest(r scanner) (*domain.ReturnRequest, error) {
	var (
		rr                                                       domain.ReturnRequest
		idStr, itemIDStr, requesterIDStr, holderIDStr, statusStr string
		message                                                  *string
	)
	if err := r.Scan(&idStr, &itemIDStr, &requesterIDStr, &holderIDStr, &message, &statusStr, &rr.CreatedAt, &rr.ResolvedAt); err != nil {
		return nil, err
	}

	id, err := domain.ParseReturnRequestID(idStr)
	if err != nil {
		return nil, fmt.Errorf("id: %w", err)
	}
	itemID, err := domain.ParseItemID(itemIDStr)
	if err != nil {
		return nil, fmt.Errorf("item id: %w", err)
	}
	requesterID, err := identity.ParseUserID(requesterIDStr)
	if err != nil {
		return nil, fmt.Errorf("requester id: %w", err)
	}
	holderID, err := identity.ParseUserID(holderIDStr)
	if err != nil {
		return nil, fmt.Errorf("holder id: %w", err)
	}
	status, err := domain.ParseReturnRequestStatus(statusStr)
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}

	rr.ID, rr.ItemID, rr.RequesterID, rr.HolderID, rr.Status = id, itemID, requesterID, holderID, status
	rr.Message = message
	return &rr, nil
}
