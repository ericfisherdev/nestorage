package adapter

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/ericfisherdev/nestorage/internal/platform/api"
	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// mapOperationError maps a domain sentinel from an operation or history
// endpoint (operations_api.go, history_api.go) to the shared envelope's
// status, code, and message — the typed-code analog of mapDomainError
// (api_common.go), needed because these endpoints route errors through
// api.Code's own NSTR-55 vocabulary (item_not_found, bin_already_in_bin,
// ...) rather than mapDomainError's generic not_found/conflict: a
// multi-resource endpoint (add-to-bin can 404 for either the item or its
// destination bin) needs a code a client can actually branch on to tell
// which. ok reports whether err was recognized; an unrecognized error is
// the caller's (writeOperationError's) cue to log it and answer a generic
// 500 instead.
//
// domain.ErrBinAlreadyInLocation and domain.ErrDuplicateReturnRequest are
// deliberately absent from this table: per BinMover.Move and
// ReturnRequestService.Request's own docs, each fires only on the exact
// retried request (never a genuine conflict), so operations_api.go's own
// handlers catch both before ever reaching this table and answer 200, not
// an error.
func mapOperationError(err error) (status int, code api.Code, message string, detail *api.FieldDetail, ok bool) {
	switch {
	case errors.Is(err, domain.ErrItemNotFound):
		return http.StatusNotFound, api.CodeItemNotFound, "item not found", nil, true
	case errors.Is(err, domain.ErrBinNotFound):
		return http.StatusNotFound, api.CodeBinNotFound, "bin not found", nil, true
	case errors.Is(err, domain.ErrLocationNotFound):
		return http.StatusNotFound, api.CodeLocationNotFound, "location not found", nil, true
	case errors.Is(err, domain.ErrReturnRequestNotFound):
		return http.StatusNotFound, api.CodeReturnRequestNotFound, "return request not found", nil, true

	case errors.Is(err, domain.ErrItemAlreadyInBin):
		return http.StatusConflict, api.CodeItemAlreadyInBin, "item is already in a different bin", nil, true
	case errors.Is(err, domain.ErrItemAlreadyCheckedOut):
		return http.StatusConflict, api.CodeItemAlreadyCheckedOut, "item is already checked out to someone else", nil, true
	case errors.Is(err, domain.ErrItemNotCheckedOut):
		return http.StatusConflict, api.CodeItemNotCheckedOut, "item is not checked out", nil, true
	case errors.Is(err, domain.ErrRequesterHoldsItem):
		return http.StatusConflict, api.CodeRequesterHoldsItem, "you already hold this item", nil, true
	case errors.Is(err, domain.ErrReturnRequestNotOpen):
		return http.StatusConflict, api.CodeReturnRequestNotOpen, "return request has already been resolved", nil, true

	case errors.Is(err, domain.ErrHolderRequired):
		return http.StatusForbidden, api.CodeHolderRequired, "only a user principal may perform this action", nil, true

	case errors.Is(err, domain.ErrInvalidReturnRequestMessage):
		return http.StatusUnprocessableEntity, api.CodeInvalidReturnRequestMessage, "invalid request",
			&api.FieldDetail{Field: "message", Message: fmt.Sprintf("must be %d characters or fewer", domain.MaxReturnRequestMessageRunes)}, true

	default:
		return 0, "", "", nil, false
	}
}

// writeOperationError maps err via mapOperationError and writes the shared
// envelope, or logs and answers a generic 500 when err is unrecognized —
// operations_api.go/history_api.go's own analog of writeDomainError
// (api_common.go), routed through this file's typed sentinel table instead.
func writeOperationError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error, op string) {
	status, code, message, detail, ok := mapOperationError(err)
	if !ok {
		logger.ErrorContext(r.Context(), op, "error", err)
		api.WriteError(w, logger, http.StatusInternalServerError, api.CodeInternal, errInternalServerError)
		return
	}
	if detail == nil {
		api.WriteError(w, logger, status, code, message)
		return
	}
	api.WriteError(w, logger, status, code, message, *detail)
}
