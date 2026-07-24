package domain

import (
	"context"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"
)

// MaxItemLinkLabelRunes bounds ValidateItemLinkLabel's accepted label length,
// measured in runes (not bytes) so a label whose first characters are
// multi-byte UTF-8 is not truncated mid-character by a byte-length check.
const MaxItemLinkLabelRunes = 120

// MaxItemLinkURLRunes bounds ValidateItemLinkURL's accepted URL length,
// generous enough for a real product page or warranty-registration link
// (which often carry long tracking query strings) while still capping the
// column at something a UI can reasonably render.
const MaxItemLinkURLRunes = 2000

// ValidateItemLinkLabel reports whether label is well-formed: not blank
// (including not whitespace-only), wrapping ErrItemLinkLabelRequired, and no
// longer than MaxItemLinkLabelRunes, wrapping ErrItemLinkLabelTooLong.
// Exported (rather than inlined into app.ItemLinkService only) so both
// Add and Edit apply the identical rule, mirroring ValidateItemName's own
// rationale.
func ValidateItemLinkLabel(label string) error {
	if strings.TrimSpace(label) == "" {
		return ErrItemLinkLabelRequired
	}
	if utf8.RuneCountInString(label) > MaxItemLinkLabelRunes {
		return ErrItemLinkLabelTooLong
	}
	return nil
}

// ValidateItemLinkURL is the security core of NSTR-38: it reports whether
// raw is a URL a stored, user-clickable link may safely carry, returning the
// trimmed value on success.
//
// The checks, in order, and why (verified against net/url's actual
// behavior, not assumed):
//   - strings.TrimSpace first: url.Parse itself errors on a URL carrying
//     leading/trailing whitespace (verified: "  https://example.com  "
//     fails to parse with "first path segment in URL cannot contain
//     colon"), so trimming here — not relying on Parse to tolerate it — is
//     what lets a pasted URL with incidental surrounding spaces still
//     validate.
//   - url.Parse; a parse error is rejected outright.
//   - u.Scheme must be exactly "http" or "https". url.Parse lowercases the
//     scheme it parses (verified: "JAVASCRIPT:alert(1)" parses with
//     Scheme == "javascript"), so this equality check alone is already
//     case-insensitive — no separate strings.ToLower is needed. This is
//     what rejects javascript:, data:, ftp://, and every other scheme.
//   - u.Host must be non-empty. This is what rejects a schemeless string
//     (Parse leaves Scheme "" and treats the whole input as an opaque
//     Path), a protocol-relative "//host/path" (Parse leaves Scheme ""
//     even though Host is set, so the scheme check above already rejects
//     it — this Host check additionally rejects "https://" itself, which
//     Parse accepts with Scheme == "https" but Host == "").
//   - length, in runes, capped at MaxItemLinkURLRunes.
//
// Returns the trimmed (never otherwise rewritten) URL, wrapping
// ErrInvalidItemLinkURL on any rejection.
func ValidateItemLinkURL(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if utf8.RuneCountInString(trimmed) > MaxItemLinkURLRunes {
		return "", ErrInvalidItemLinkURL
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return "", ErrInvalidItemLinkURL
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", ErrInvalidItemLinkURL
	}
	if u.Host == "" {
		return "", ErrInvalidItemLinkURL
	}
	return trimmed, nil
}

// ItemLink is a labeled URL attached to an item: a manual, a product page, a
// warranty registration — a child entity of the Item aggregate (not its own
// bounded-context row; internal/media's Photo is the analogous child entity
// for images). Removing an item removes its links with it (item_id's ON
// DELETE CASCADE in 00011_item_link.sql); ItemLink carries no visibility of
// its own and inherits whatever the owning item's is.
type ItemLink struct {
	ID        ItemLinkID
	ItemID    ItemID
	Label     string
	URL       string
	Position  int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ItemLinkRepository is the outbound port for persisting and retrieving an
// item's labeled URLs. Implementations live in the adapter package.
//
// Deliberately NOT visibility-scoped, matching ItemRepository.Delete's own
// contract: every method here trusts the caller has already confirmed the
// owning item is visible to the acting principal. app.ItemLinkService is
// that caller — every one of its methods resolves the item through a
// visibility-scoped Get first (see its own doc).
//
// Persistence contracts (the caller sets identity and valid fields; the
// store sets timestamps):
//   - Create expects l.ID, a validated l.Label/l.URL (see
//     ValidateItemLinkLabel/ValidateItemLinkURL), a valid l.ItemID, and
//     l.Position (typically from a preceding NextPosition call); it
//     populates CreatedAt/UpdatedAt.
//   - Update overwrites only label and url — never item id or position.
//
// Error contracts:
//   - Create returns a wrapped ErrItemNotFound when item_id is unknown (the
//     item_link_item_id_fkey foreign-key violation).
//   - Update and Delete return ErrItemLinkNotFound when no row matches both
//     the link id and itemID — a link id from another item is treated as
//     not found, never mutated, so a caller cannot use one item's link id to
//     reach into another item's links.
//   - ListByItem and NextPosition return an empty slice / 0 respectively,
//     never an error, when the item has no links yet.
type ItemLinkRepository interface {
	Create(ctx context.Context, l *ItemLink) error
	// Update overwrites the (itemID, id) link's label and url. Returns
	// ErrItemLinkNotFound when no row matches both columns.
	Update(ctx context.Context, itemID ItemID, id ItemLinkID, label, rawURL string) error
	// Delete removes the (itemID, id) link. Returns ErrItemLinkNotFound when
	// no row matches both columns.
	Delete(ctx context.Context, itemID ItemID, id ItemLinkID) error
	// ListByItem returns itemID's links ordered by position, tie-broken by
	// id — the same deterministic-order convention every other list in this
	// codebase follows. Returns an empty slice, not an error, when itemID
	// has no links.
	ListByItem(ctx context.Context, itemID ItemID) ([]ItemLink, error)
	// NextPosition returns one past itemID's current highest link position
	// (0 when it has none yet), for Create's caller to assign to a newly
	// added link.
	NextPosition(ctx context.Context, itemID ItemID) (int, error)
}
