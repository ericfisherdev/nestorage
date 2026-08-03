# SSO deployment topology (NSTR-117)

This records the deployment-topology verification NSTR-117's acceptance
criteria require: whether Nestova and Nestorage sit on the same tailnet
hostname (the precondition for the shared session cookie to reach both), and
whether the two apps' session cookies collide on the current deployment.

## Finding: no shared appliance is deployed yet

As of this ticket (2026-08-03), no tailnet node in this workspace's tailnet
has a `tailscale serve` configuration at all (`tailscale serve status` →
`No serve config`), and no peer resembling the household appliance (the
Raspberry Pi / HP 24-r014 touchscreen described in the root `CLAUDE.md`'s
"Deployment shape") exists among the tailnet's members. Provisioning that
appliance is Sprint 9's work (Deploy & Ops), not this ticket's.

Consequently the literal instruction to "inspect the tailscale serve config
on the appliance host, curl both apps, compare hostnames and Set-Cookie"
cannot be performed against a real deployment right now — there is no
deployment to inspect. What follows is what could be verified instead, plus
what is still outstanding.

## Cookie-collision check against the code, not a live host

Before this ticket, each app built its own `scs.SessionManager`
independently, and **neither set `Cookie.Name`**, so both defaulted to
scs's own default, `"session"`:

- Nestova: `internal/auth/adapter/session.go` (pre-NSTR-115, superseded by
  nestcore's `identity/session.NewManager`).
- Nestorage: `internal/platform/session/session.go` (pre-this-ticket,
  wrapped this repo's own `identityStore` shim, since removed).

Had the two apps ever been served from the **same** tailnet hostname before
this ticket — the intended single-appliance arrangement — they would have
collided on that shared cookie name: whichever app's session write landed
last would silently clobber the other's login, exactly the failure mode
nestcore's `identity/session` package doc warns about. No production
deployment exists yet, so this collision was never live, but it was latent
in the code.

## Fix delivered by this ticket

Nestorage now constructs its session manager through
`nestcore/identity/session.NewManager` (`internal/platform/session/session.go`),
the same constructor Nestova adopted in NSTR-115. Both apps therefore get an
**identical** cookie name/attributes and the same session data key
(`identity/session.KeyMemberID`) by construction — there is no longer a
place for the two apps' cookie configs to drift independently. This is
proven by a gated cross-instance test in nestcore
(`identity/session/session_test.go`'s
`TestNewManager_SessionWrittenByOneInstanceIsReadableByAnother`): a session
committed through one `SessionManager` instance is readable through a
second, independently-constructed one over the same store.

## Still outstanding (tracked under Sprint 9: Deploy & Ops)

Once the appliance exists and both apps are actually deployed to it:

1. Confirm both apps' `tailscale serve` targets resolve under **one**
   tailnet hostname — path- or port-based routing on that single hostname,
   never two independently-set hostnames (`tailscale set --hostname=...`
   run twice with different values would silently break SSO).
2. Re-run the live check this doc could not perform: log into one app, load
   the other, and confirm no login prompt appears — the manual end-to-end
   procedure this ticket's acceptance criteria describe.
3. `curl -v` both apps' `Set-Cookie` headers side by side as a final sanity
   check that they genuinely match in practice, not just in the source
   both now share.
