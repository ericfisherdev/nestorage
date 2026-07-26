package api

import (
	_ "embed"
	"log/slog"
	"net/http"
)

// openAPISpecYAML is NSTR-57's own hand-written OpenAPI 3.1 document —
// embedded rather than generated, the same pattern this codebase already
// uses for web assets and goose migrations, so the served bytes are always
// exactly what spec_test.go validated at build time.
//
//go:embed openapi.yaml
var openAPISpecYAML []byte

// SpecHandlers serves the published OpenAPI 3.1 specification — the
// machine-readable contract behind every route this package and the
// bounded-context adapters' own API packages register.
type SpecHandlers struct {
	logger *slog.Logger
}

// NewSpecHandlers constructs SpecHandlers. logger is required, matching
// every other constructor in this codebase.
func NewSpecHandlers(logger *slog.Logger) *SpecHandlers {
	if logger == nil {
		panic("platform/api: NewSpecHandlers requires a non-nil logger")
	}
	return &SpecHandlers{logger: logger}
}

// Routes registers the spec route on mux — a Registrar (DIP), matching
// every other handler group's own Routes method. Unlike every route the
// bounded-context adapters mount through Router, this one is registered by
// cmd/server/shell.go directly on the outer shell mux, outside
// RequireAPICredential: the contract is public (the repository is public);
// the data behind it is not. "GET /api/v1/openapi.yaml" is a more specific
// pattern than the "/api/v1/" subtree Router answers, so net/http.ServeMux
// keeps routing it here regardless of registration order — the same
// escape-the-gate mechanism the device-token exchange endpoint already
// relies on (see cmd/server/shell.go's newAppRoutes doc).
func (h *SpecHandlers) Routes(mux Registrar) {
	mux.HandleFunc("GET /api/v1/openapi.yaml", h.Serve)
}

// Serve answers GET /api/v1/openapi.yaml with the embedded specification
// verbatim, as application/yaml.
func (h *SpecHandlers) Serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/yaml")
	if _, err := w.Write(openAPISpecYAML); err != nil {
		h.logger.ErrorContext(r.Context(), "api: serve openapi spec", "error", err)
	}
}
