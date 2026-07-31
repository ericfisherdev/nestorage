package config

// peer.go adds NSTR-124's own configuration: the sibling app's (Nestova)
// origin the cross-app nav control links to. nestcore has no peer-app
// loader (which peer app, if any, is installed is a Nestorage-local
// concern, not a shared platform one), so this is Nestorage-local,
// mirroring MediaConfig/EmailConfig/RateLimitConfig's identical
// "nestcore has nothing that fits" rationale.

import (
	"fmt"
	"net/url"
	"strings"

	corecfg "github.com/ericfisherdev/nestcore/config"
)

// PeerConfig configures the sidebar's cross-app nav control (NSTR-124):
// the origin of the sibling app (Nestova) it links to.
type PeerConfig struct {
	// NestovaURL is PEER_NESTOVA_URL, optional. When unset, Nestova is not
	// installed on this appliance and no cross-app nav control renders at
	// all (see components.AppSwitchEntry's own doc) — a bare "not
	// configured" state, not an error.
	NestovaURL string
}

// LoadPeer reads PeerConfig from PEER_NESTOVA_URL, mirroring
// corecfg.LoadServer/LoadDB's (value, []error) shape so Load can aggregate
// its errors the same way LoadMedia/LoadEmail/LoadRateLimit already do.
func LoadPeer() (PeerConfig, []error) {
	return PeerConfig{
		NestovaURL: strings.TrimSpace(corecfg.String("PEER_NESTOVA_URL", "")),
	}, nil
}

// Validate returns every PeerConfig problem found, so callers can surface
// them together. An empty NestovaURL is valid (the feature is simply
// absent — see NestovaURL's own doc); only a NON-empty value that fails to
// parse as an origin-only http(s) URL is rejected.
func (p PeerConfig) Validate() []error {
	if p.NestovaURL == "" {
		return nil
	}

	var errs []error

	// PEER_NESTOVA_URL must be an absolute http(s) URL so it can be used
	// directly as a full-page navigation target and as the probe's own
	// {baseURL}/healthz base, mirroring PUBLIC_BASE_URL's identical
	// two-branch scheme/host-then-query/fragment check in nestcore's
	// config/server.go. u may be nil when err != nil, so the second case
	// below is only ever reached once the first has already ruled that out
	// (a switch, not independent ifs, so u is never dereferenced unparsed).
	u, err := url.Parse(p.NestovaURL)
	switch {
	case err != nil, u.Scheme != "http" && u.Scheme != "https", u.Host == "":
		errs = append(errs, fmt.Errorf("PEER_NESTOVA_URL must be an absolute http(s) URL, got %q", p.NestovaURL))
	case u.RawQuery != "" || u.Fragment != "":
		errs = append(errs, fmt.Errorf("PEER_NESTOVA_URL must be an origin only (no query or fragment), got %q", p.NestovaURL))
	}

	return errs
}
