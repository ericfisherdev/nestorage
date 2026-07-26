package config

// ratelimit.go adds NSTR-58's own configuration: the account-wide bucket
// over the whole /api/v1 surface, and the additional, stricter bucket over
// the token-exchange route alone. nestcore has no rate-limit loader (this is
// Nestorage's own in-process, in-memory limiter, not a shared platform
// concern), mirroring MediaConfig's identical "nestcore has nothing that
// fits" rationale.

import (
	"fmt"

	corecfg "github.com/ericfisherdev/nestcore/config"
)

// Defaults for every RateLimitConfig field, chosen so a fresh install boots
// with rate limiting already active — never "unlimited until configured" —
// with no new configuration required.
const (
	// defaultAPIRPS is API_RATE_LIMIT_RPS's default: sustained per-principal
	// requests per second across the whole /api/v1 surface.
	defaultAPIRPS = 10
	// defaultAPIBurst is API_RATE_LIMIT_BURST's default token-bucket burst.
	defaultAPIBurst = 30
	// defaultAuthRPM is AUTH_RATE_LIMIT_RPM's default: sustained
	// per-client-IP requests per MINUTE on the token-exchange route —
	// deliberately far stricter than the general API bucket (AC 3).
	defaultAuthRPM = 10
	// defaultAuthBurst is AUTH_RATE_LIMIT_BURST's default burst for that
	// same stricter bucket.
	defaultAuthBurst = 5
)

// RateLimitConfig configures NSTR-58's two in-process, in-memory token-
// bucket limiters (identityadapter.KeyedRateLimiter): the account-wide
// bucket over the whole /api/v1 surface, keyed per resolved principal, and
// the additional stricter bucket over the token-exchange route alone, keyed
// per client IP. There is no external limiter store (Redis or otherwise) —
// the appliance is a single Go binary, so an in-memory rate.Limiter per key
// is enough.
type RateLimitConfig struct {
	// APIRPS is the sustained per-principal requests-per-second rate for
	// the account-wide /api/v1 bucket.
	APIRPS int
	// APIBurst is that bucket's own token-bucket burst.
	APIBurst int
	// AuthRPM is the sustained per-client-IP requests-per-MINUTE rate for
	// the stricter token-exchange bucket.
	AuthRPM int
	// AuthBurst is that bucket's own burst.
	AuthBurst int
}

// LoadRateLimit reads RateLimitConfig from API_RATE_LIMIT_RPS,
// API_RATE_LIMIT_BURST, AUTH_RATE_LIMIT_RPM, and AUTH_RATE_LIMIT_BURST —
// every value defaulted (see the const block above) so an existing
// deployment keeps booting with no new configuration, mirroring
// LoadMedia/LoadEmail's identical always-defaulted composition.
func LoadRateLimit() (RateLimitConfig, []error) {
	var errs []error

	apiRPS, err := corecfg.Int32("API_RATE_LIMIT_RPS", defaultAPIRPS)
	if err != nil {
		errs = append(errs, err)
	}
	apiBurst, err := corecfg.Int32("API_RATE_LIMIT_BURST", defaultAPIBurst)
	if err != nil {
		errs = append(errs, err)
	}
	authRPM, err := corecfg.Int32("AUTH_RATE_LIMIT_RPM", defaultAuthRPM)
	if err != nil {
		errs = append(errs, err)
	}
	authBurst, err := corecfg.Int32("AUTH_RATE_LIMIT_BURST", defaultAuthBurst)
	if err != nil {
		errs = append(errs, err)
	}

	return RateLimitConfig{
		APIRPS:    int(apiRPS),
		APIBurst:  int(apiBurst),
		AuthRPM:   int(authRPM),
		AuthBurst: int(authBurst),
	}, errs
}

// Validate returns every RateLimitConfig problem found: all four values
// must be positive, mirroring MediaConfig/EmailConfig's identical
// aggregated-error convention.
func (c RateLimitConfig) Validate() []error {
	var errs []error
	if c.APIRPS <= 0 {
		errs = append(errs, fmt.Errorf("API_RATE_LIMIT_RPS must be positive, got %d", c.APIRPS))
	}
	if c.APIBurst <= 0 {
		errs = append(errs, fmt.Errorf("API_RATE_LIMIT_BURST must be positive, got %d", c.APIBurst))
	}
	if c.AuthRPM <= 0 {
		errs = append(errs, fmt.Errorf("AUTH_RATE_LIMIT_RPM must be positive, got %d", c.AuthRPM))
	}
	if c.AuthBurst <= 0 {
		errs = append(errs, fmt.Errorf("AUTH_RATE_LIMIT_BURST must be positive, got %d", c.AuthBurst))
	}
	return errs
}
