package app

import (
	"net/url"

	"github.com/ericfisherdev/nestorage/internal/storage/domain"
)

// BinDeepLinkURL builds the absolute URL a bin's printed label/QR code
// encodes: base, "/b/", and the path-escaped, normalized bin code — the one
// definition of a bin's deep-link shape, so NSTR-47's LabelRenderer port
// (BinLabel.QRPayload) and NSTR-50's batch label flow build the exact same
// link this ticket's own bin detail page renders a QR against.
//
// base must be an origin with no trailing slash (scheme + host only, e.g.
// "https://nestorage.tailnet-name.ts.net") — nestcore's corecfg.LoadServer
// already guarantees this shape for a configured PUBLIC_BASE_URL, and
// adapter.resolveBaseURL's own request-derived fallback is built the same
// way. code is run through domain.NormalizeBinCode before escaping, so the
// link always encodes a bin's canonical upper-cased printed form regardless
// of the caller's own casing.
//
// A plain exported function, not a port: it has no dependency to invert,
// and wrapping a pure string build in an interface would be speculative
// abstraction. Every caller — inside this package or out — treats the
// result as an opaque string, never parses or rebuilds it.
func BinDeepLinkURL(base, code string) string {
	return base + "/b/" + url.PathEscape(domain.NormalizeBinCode(code))
}
