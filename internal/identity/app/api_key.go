package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ericfisherdev/nestorage/internal/identity/domain"
)

// maxAPIKeyLabelRunes bounds the key's display label — the same "count
// runes, not bytes" rule as validateDeviceName, so a multi-byte label is not
// rejected for exceeding a byte count a human never actually typed.
const maxAPIKeyLabelRunes = 100

// apiKeyLastUsedInterval throttles APIKey.LastUsedAt writes: a key
// authenticating repeatedly writes at most once per interval, keyed off the
// stored value rather than an in-memory cache (see touchLastUsed) — the
// same throttle DeviceTokenService applies to device tokens.
const apiKeyLastUsedInterval = 15 * time.Minute

// apiKeyRepository is the narrow port (ISP) APIKeyService depends on,
// satisfied by domain.APIKeyRepository (a superset) and by test fakes.
type apiKeyRepository interface {
	Create(ctx context.Context, k *domain.APIKey) error
	GetBySecretHash(ctx context.Context, secretHash string) (*domain.APIKey, error)
	ListAll(ctx context.Context) ([]*domain.APIKey, error)
	Rotate(ctx context.Context, incumbentID domain.APIKeyID, expiresAt time.Time, next *domain.APIKey) error
	Revoke(ctx context.Context, id domain.APIKeyID, revokedAt time.Time) error
	TouchLastUsed(ctx context.Context, id domain.APIKeyID, now, staleBefore time.Time) error
}

// APIKeyService issues and manages the account's single api key: the
// credential NSTR-23's Nestova integration authenticates with. It returns a
// domain.APIKey from Authenticate rather than a Principal — NSTR-24 owns the
// Principal abstraction and adapts this return value into one of kind
// integration.
type APIKeyService struct {
	keys   apiKeyRepository
	clock  func() time.Time
	logger *slog.Logger
}

// NewAPIKeyService constructs APIKeyService, returning an error on a nil
// dependency rather than panicking — the same constructor shape as
// Nestova's NewKioskService, the reference implementation this ticket ports
// from.
func NewAPIKeyService(keys apiKeyRepository, clock func() time.Time, logger *slog.Logger) (*APIKeyService, error) {
	if keys == nil {
		return nil, errors.New("identity/app: NewAPIKeyService requires a non-nil APIKeyRepository")
	}
	if clock == nil {
		return nil, errors.New("identity/app: NewAPIKeyService requires a non-nil clock func")
	}
	if logger == nil {
		return nil, errors.New("identity/app: NewAPIKeyService requires a non-nil logger")
	}
	return &APIKeyService{keys: keys, clock: clock, logger: logger}, nil
}

// Current returns the key the admin screen shows as active — the one row
// with APIKeyStatusCurrent, if any. found is false when no key has ever
// been created.
func (s *APIKeyService) Current(ctx context.Context) (key *domain.APIKey, found bool, err error) {
	keys, err := s.keys.ListAll(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("api key: current: %w", err)
	}
	now := s.clock()
	for _, k := range keys {
		if k.Status(now) == domain.APIKeyStatusCurrent {
			return k, true, nil
		}
	}
	return nil, false, nil
}

// List returns every key (current, retiring, expired, and revoked) newest
// first, for the settings screen's history view.
func (s *APIKeyService) List(ctx context.Context) ([]*domain.APIKey, error) {
	keys, err := s.keys.ListAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("api key: list: %w", err)
	}
	return keys, nil
}

// Create issues the account's first api key. label is trimmed and must be
// non-blank and at most maxAPIKeyLabelRunes long. plaintext is returned
// exactly once — the caller must not expect to recover it later; only its
// hash is stored. Returns domain.ErrAPIKeyExists when a current key already
// exists, so creating is unambiguous and replacing an existing key is
// Rotate.
func (s *APIKeyService) Create(ctx context.Context, label string) (key *domain.APIKey, plaintext string, err error) {
	label = strings.TrimSpace(label)
	if err := validateAPIKeyLabel(label); err != nil {
		return nil, "", err
	}

	raw, k, err := newAPIKey(label)
	if err != nil {
		return nil, "", fmt.Errorf("api key: create: %w", err)
	}
	if err := s.keys.Create(ctx, k); err != nil {
		return nil, "", fmt.Errorf("api key: create: %w", err)
	}
	return k, raw, nil
}

// Rotate mints a new api key labeled label, atomically superseding the
// current key (if any): the incumbent stays usable until now plus overlap
// (or is invalidated immediately, when overlap is OverlapNone), and the new
// key becomes current — one repository call, one transaction (see
// domain.APIKeyRepository.Rotate's own contract). plaintext is returned
// exactly once, the same as Create.
func (s *APIKeyService) Rotate(ctx context.Context, label string, overlap domain.OverlapWindow) (key *domain.APIKey, plaintext string, err error) {
	label = strings.TrimSpace(label)
	if err := validateAPIKeyLabel(label); err != nil {
		return nil, "", err
	}
	if !overlap.Valid() {
		return nil, "", fmt.Errorf("%w: %q", domain.ErrInvalidOverlapWindow, overlap)
	}

	incumbent, hasIncumbent, err := s.Current(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("api key: rotate: %w", err)
	}
	var incumbentID domain.APIKeyID
	if hasIncumbent {
		incumbentID = incumbent.ID
	}

	raw, next, err := newAPIKey(label)
	if err != nil {
		return nil, "", fmt.Errorf("api key: rotate: %w", err)
	}
	expiresAt := s.clock().Add(overlap.Duration())
	if err := s.keys.Rotate(ctx, incumbentID, expiresAt, next); err != nil {
		return nil, "", fmt.Errorf("api key: rotate: %w", err)
	}
	return next, raw, nil
}

// Revoke immediately invalidates the key named by id, regardless of its
// current status. Returns domain.ErrAPIKeyNotFound when id is unknown or
// already revoked.
func (s *APIKeyService) Revoke(ctx context.Context, id domain.APIKeyID) error {
	if err := s.keys.Revoke(ctx, id, s.clock()); err != nil {
		return fmt.Errorf("api key: revoke: %w", err)
	}
	return nil
}

// Authenticate resolves a presented bearer secret into its APIKey. presented
// not carrying APIKeyPrefix is rejected as domain.ErrAPIKeyNotFound without
// a database round trip — the same short-circuit validateDeviceName's
// blank-name check gives DeviceTokenService.Issue, and the defense that
// keeps a misrouted device token (NSTR-24 dispatches by prefix) from ever
// reaching a hash lookup here.
//
// Returns domain.ErrAPIKeyNotFound for an unrecognized secret,
// domain.ErrAPIKeyRevoked for a known-but-revoked key, and
// domain.ErrAPIKeyExpired for a known key past its overlap window — three
// distinct outcomes so the caller can log/react to each differently.
func (s *APIKeyService) Authenticate(ctx context.Context, presented string) (*domain.APIKey, error) {
	presented = strings.TrimSpace(presented)
	if presented == "" || !strings.HasPrefix(presented, domain.APIKeyPrefix) {
		return nil, domain.ErrAPIKeyNotFound
	}

	k, err := s.keys.GetBySecretHash(ctx, domain.HashAPIKeySecret(presented))
	if err != nil {
		if errors.Is(err, domain.ErrAPIKeyNotFound) {
			return nil, domain.ErrAPIKeyNotFound
		}
		return nil, fmt.Errorf("api key: authenticate: %w", err)
	}

	now := s.clock()
	switch {
	case k.RevokedAt != nil:
		return nil, domain.ErrAPIKeyRevoked
	case !k.Usable(now):
		return nil, domain.ErrAPIKeyExpired
	}

	s.touchLastUsed(ctx, k.ID)
	return k, nil
}

// touchLastUsed best-effort records this authentication on key id, throttled
// to once per apiKeyLastUsedInterval by TouchLastUsed's own stale-before
// argument. A failure is logged and swallowed — it must never fail an
// otherwise valid authentication.
func (s *APIKeyService) touchLastUsed(ctx context.Context, id domain.APIKeyID) {
	now := s.clock()
	if err := s.keys.TouchLastUsed(ctx, id, now, now.Add(-apiKeyLastUsedInterval)); err != nil {
		s.logger.ErrorContext(ctx, "api key: touch last used", "error", err)
	}
}

// newAPIKey generates a fresh secret and the domain.APIKey row wrapping it,
// validated before it is ever handed to a repository. Shared by Create and
// Rotate so the "generate, derive the prefix and hash, validate" sequence
// stays defined in exactly one place.
func newAPIKey(label string) (raw string, key *domain.APIKey, err error) {
	raw, err = domain.GenerateAPIKeySecret()
	if err != nil {
		return "", nil, fmt.Errorf("generate: %w", err)
	}
	k := &domain.APIKey{
		ID:         domain.NewAPIKeyID(),
		KeyPrefix:  domain.KeyPrefixOf(raw),
		SecretHash: domain.HashAPIKeySecret(raw),
		Label:      label,
	}
	if err := k.Validate(); err != nil {
		return "", nil, err
	}
	return raw, k, nil
}

// validateAPIKeyLabel wraps domain.ErrInvalidAPIKey for a blank or
// over-long label — checked before any secret is generated, mirroring
// validateDeviceName's own ordering rationale.
func validateAPIKeyLabel(label string) error {
	n := len([]rune(label))
	switch {
	case n == 0:
		return fmt.Errorf("%w: label must not be blank", domain.ErrInvalidAPIKey)
	case n > maxAPIKeyLabelRunes:
		return fmt.Errorf("%w: label must be at most %d characters", domain.ErrInvalidAPIKey, maxAPIKeyLabelRunes)
	default:
		return nil
	}
}
