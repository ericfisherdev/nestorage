package config

// provider.go adds NSTR-100's own configuration: whether Nestorage
// authenticates members locally (standalone) or delegates to a Nestova
// identity provider (federated) — see FederationMode's own doc for why this
// is explicit configuration, never inferred from whether a call to the
// provider happened to succeed. nestcore has no federation loader (this is
// Nestorage's own auth concern, not a shared platform concern), mirroring
// MediaConfig/EmailConfig/RateLimitConfig's identical "nestcore has nothing
// that fits" rationale.

import (
	"errors"
	"fmt"
	"net/url"
	"strings"

	corecfg "github.com/ericfisherdev/nestcore/config"
)

// FederationMode selects whether Nestorage authenticates members locally or
// delegates to a Nestova identity provider. It is a plain string type (not an
// iota) so its .env/log spelling is its own zero-translation representation,
// mirroring MediaStorageBackend's identical rationale.
type FederationMode string

// FederationMode values. Standalone is the zero-configuration default: a
// fresh install authenticates entirely locally with no outbound network
// dependency.
const (
	FederationModeStandalone FederationMode = "standalone"
	FederationModeFederated  FederationMode = "federated"
)

// String implements fmt.Stringer, so a FederationMode logs and formats as
// its plain spelling with no further translation.
func (m FederationMode) String() string {
	return string(m)
}

// ProviderConfig configures the Nestova identity provider Nestorage
// federates against (NSTR-100). All three fields are env-only, read once at
// startup, and fail-fast through Validate — a runtime attach call from
// Nestova could never write into this package. There is deliberately no
// PROVIDER_HOUSEHOLD_ID: the household binding lives in the single-row
// federation_binding table NSTR-101 adds; this struct carries only what an
// operator can know before attach — base URL, client id, and secret.
//
// ProviderConfig itself must NEVER be logged. Logging goes through Mode(),
// Host(), and ClientID only — ClientID is public (it travels in the browser
// redirect to the provider's authorize endpoint, NSTR-102, so it already
// appears in browser history, Referer headers, and server access logs) and
// may appear in logs; ClientSecret never may.
type ProviderConfig struct {
	// BaseURL is the provider's http(s) origin (env PROVIDER_BASE_URL).
	BaseURL string
	// ClientID is the public client identifier for this Nestorage install
	// (env PROVIDER_CLIENT_ID). Safe to log.
	ClientID string
	// ClientSecret is the confidential credential Nestorage authenticates
	// itself to Nestova with (env PROVIDER_CLIENT_SECRET), used only on
	// back-channel (server-to-server) calls and never placed in any URL.
	// NEVER log this value.
	ClientSecret string
}

// LoadProvider reads ProviderConfig from PROVIDER_BASE_URL, PROVIDER_CLIENT_ID,
// and PROVIDER_CLIENT_SECRET — each read via corecfg.String with an empty
// default and trimmed. There is no PROVIDER_HOUSEHOLD_ID (see ProviderConfig's
// own doc). The PROVIDER_ prefix follows this package's feature-prefix
// convention (MEDIA_, EMAIL_, API_RATE_LIMIT_); the reserved NESTORAGE_
// prefix stays test-gate-only per email.go's Sprint 6 reconciliation note.
// The []error return mirrors LoadMedia/LoadEmail/LoadRateLimit's identical
// shape for Load to aggregate the same way, even though corecfg.String
// itself never fails.
func LoadProvider() (ProviderConfig, []error) {
	return ProviderConfig{
		// TrimRight (not TrimSuffix, which removes only ONE occurrence)
		// strips EVERY trailing slash, mirroring nestcore's own
		// PUBLIC_BASE_URL loader (config/server.go): "https://host/" is the
		// same origin as "https://host", so it must not fail Validate's
		// origin-only check just because an operator pasted a trailing
		// slash.
		BaseURL:      strings.TrimRight(strings.TrimSpace(corecfg.String("PROVIDER_BASE_URL", "")), "/"),
		ClientID:     strings.TrimSpace(corecfg.String("PROVIDER_CLIENT_ID", "")),
		ClientSecret: strings.TrimSpace(corecfg.String("PROVIDER_CLIENT_SECRET", "")),
	}, nil
}

// Mode resolves the FederationMode: federated iff all three fields are
// non-empty, standalone iff all three are empty. Meaningful only after
// Validate has passed — Validate rejects every partial state, so a running
// process can never observe one. This is the single accessor the rest of
// the codebase consumes; no handler re-derives the mode from ProviderConfig
// directly.
func (p ProviderConfig) Mode() FederationMode {
	if p.BaseURL != "" && p.ClientID != "" && p.ClientSecret != "" {
		return FederationModeFederated
	}
	return FederationModeStandalone
}

// Host returns the provider's parsed host, for the startup log line only —
// empty in standalone mode. BaseURL has already been validated as an
// absolute http(s) origin by the time Mode() can report federated (see
// Validate), so the parse here cannot fail in practice; a failure is
// treated the same as standalone (empty) rather than panicking on a log
// helper.
func (p ProviderConfig) Host() string {
	if p.BaseURL == "" {
		return ""
	}
	u, err := url.Parse(p.BaseURL)
	if err != nil {
		return ""
	}
	return u.Host
}

// Validate returns every ProviderConfig problem found: all-empty is valid
// (standalone); all-set is valid (federated); any other combination appends
// one error per missing variable, naming it, so an operator fixes every
// problem in one pass rather than one at a time (mirrors
// MediaConfig/EmailConfig's identical aggregated-error convention). In the
// all-set case, BaseURL must additionally be an absolute http(s) origin —
// scheme + host, no user/path/query/fragment — mirroring nestcore's
// PUBLIC_BASE_URL check in config/server.go.
func (p ProviderConfig) Validate() []error {
	switch {
	case p.BaseURL == "" && p.ClientID == "" && p.ClientSecret == "":
		return nil
	case p.BaseURL != "" && p.ClientID != "" && p.ClientSecret != "":
		return p.validateBaseURL()
	}

	var errs []error
	if p.BaseURL == "" {
		errs = append(errs, errors.New("PROVIDER_BASE_URL is required when PROVIDER_CLIENT_ID or PROVIDER_CLIENT_SECRET is set"))
	}
	if p.ClientID == "" {
		errs = append(errs, errors.New("PROVIDER_CLIENT_ID is required when PROVIDER_BASE_URL or PROVIDER_CLIENT_SECRET is set"))
	}
	if p.ClientSecret == "" {
		errs = append(errs, errors.New("PROVIDER_CLIENT_SECRET is required when PROVIDER_BASE_URL or PROVIDER_CLIENT_ID is set"))
	}
	return errs
}

// validateBaseURL checks BaseURL is an absolute http(s) origin only —
// scheme + host(:port), nothing else — so it can be concatenated directly
// with a path with no further normalization, the same "origin only"
// contract corecfg.ServerConfig.Validate enforces for PUBLIC_BASE_URL.
func (p ProviderConfig) validateBaseURL() []error {
	u, err := url.Parse(p.BaseURL)
	switch {
	case err != nil, u.Scheme != "http" && u.Scheme != "https", u.Host == "":
		return []error{fmt.Errorf("PROVIDER_BASE_URL must be an absolute http(s) URL, got %q", p.BaseURL)}
	case u.User != nil, u.Path != "", u.RawQuery != "", u.Fragment != "":
		return []error{fmt.Errorf("PROVIDER_BASE_URL must be an origin only (scheme + host, no user/path/query/fragment), got %q", p.BaseURL)}
	}
	return nil
}
