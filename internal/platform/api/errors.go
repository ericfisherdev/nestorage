package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
)

// PathPrefix marks nestorage's JSON API surface. It is the one place this
// literal is declared — identity/adapter's Denier (deny.go) reads it back
// through this package rather than declaring its own copy, so every
// JSON-vs-HTML split in the codebase agrees on the exact same prefix.
const PathPrefix = "/api/"

// Code is the stable, machine-readable identifier every error envelope
// carries, kept as a typed enum (rather than a bare string) so a typo in a
// call site fails to compile instead of shipping a silently-wrong code.
type Code string

// The full error-code vocabulary every /api/v1 endpoint answers through.
// RateLimited has no caller yet in this ticket — NSTR-58's rate-limiting
// middleware is the first to use it — but the vocabulary is declared
// complete here, in one place, rather than grown one code at a time across
// unrelated tickets. CodeConflict is NSTR-54's own addition: NSTR-53 did not
// anticipate a 409 response (its own /api/v1 mount has nothing that can
// conflict), and this is still the one place the vocabulary lives, so it is
// added here rather than let a bounded-context adapter invent its own code.
const (
	CodeUnauthorized       Code = "unauthorized"
	CodeForbidden          Code = "forbidden"
	CodeNotFound           Code = "not_found"
	CodeMethodNotAllowed   Code = "method_not_allowed"
	CodeInvalidRequest     Code = "invalid_request"
	CodeInvalidCredentials Code = "invalid_credentials"
	CodeSetupRequired      Code = "setup_required"
	CodeInternal           Code = "internal"
	CodeRateLimited        Code = "rate_limited"
	CodeConflict           Code = "conflict"
)

// FieldDetail names one field-level validation failure. Details is only
// populated on CodeInvalidRequest responses — every other code describes a
// failure with nothing field-shaped to report.
type FieldDetail struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// errorBody is the one JSON shape every /api/v1 endpoint answers an error
// with — see errorDetail for the fields it carries.
type errorBody struct {
	Error errorDetail `json:"error"`
}

// errorDetail is errorBody's inner value: a stable Code, a human-readable
// Message, and optional field-level Details (see FieldDetail).
type errorDetail struct {
	Code    Code          `json:"code"`
	Message string        `json:"message"`
	Details []FieldDetail `json:"details,omitempty"`
}

// WriteJSON encodes v as status's JSON response body. logger is required —
// it records the one failure mode this function can have, an encode error,
// matching identityadapter.Denier's own writeJSON convention (duplicated
// rather than shared, since Denier is scoped to identity/adapter and this
// package must stay free of any bounded-context import).
func WriteJSON(w http.ResponseWriter, logger *slog.Logger, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.Error("api: encode response", "error", err)
	}
}

// WriteError writes status as the shared error envelope: code is the stable
// identifier, message the human-readable text, and details — optional,
// meaningful only alongside CodeInvalidRequest — names the specific field(s)
// that failed validation.
func WriteError(w http.ResponseWriter, logger *slog.Logger, status int, code Code, message string, details ...FieldDetail) {
	WriteJSON(w, logger, status, errorBody{Error: errorDetail{Code: code, Message: message, Details: details}})
}
