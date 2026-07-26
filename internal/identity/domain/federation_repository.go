package domain

import "context"

// MemberLinkRepository is the outbound port for persisting and retrieving
// the link between a Nestova member and a Nestorage user (NSTR-101).
// Implementations live in the adapter package.
//
// Error contracts:
//   - Create returns ErrMemberAlreadyLinked when the member id is already
//     linked to a DIFFERENT user (provider_member_link_member_id_uniq), or
//     ErrUserAlreadyLinked when the user already carries a DIFFERENT
//     member identity (provider_member_link_user_id_uniq) — the
//     application-level counterpart to the migration's own two named
//     unique constraints (00017_provider_member_link.sql).
//   - FindByMemberID returns ErrMemberLinkNotFound when no link matches.
//   - List returns an empty slice, never an error, when no links exist.
type MemberLinkRepository interface {
	Create(ctx context.Context, link *MemberLink) error
	FindByMemberID(ctx context.Context, memberID string) (*MemberLink, error)
	// List returns every recorded link — this is also NSTR-102's own
	// callback-lookup surface, exposed here rather than duplicated.
	List(ctx context.Context) ([]MemberLink, error)
}

// HouseholdBindingRepository is the outbound port for the single-row
// federation_binding table (NSTR-101): the one Nestova household this
// install is bound to, once an attach call establishes it. The binding
// lives in the database, not configuration — config.ProviderConfig is
// env-only and validated fail-fast at startup, so a value recorded by an
// incoming attach call could never be a config field.
type HouseholdBindingRepository interface {
	// Get returns the current binding and true, or a zero HouseholdBinding
	// and false when no binding has been recorded yet.
	Get(ctx context.Context) (HouseholdBinding, bool, error)
	// Record writes householdID as the binding, replacing any existing row
	// (there is ever only one — federation_binding_single_row_uniq is what
	// enforces that at the database level). Callers are expected to have
	// already checked Get and decided this write is correct: Record itself
	// does not re-verify a mismatch — FederationProvisioner.checkBinding is
	// the one place that rule is enforced.
	Record(ctx context.Context, householdID string) (HouseholdBinding, error)
}
