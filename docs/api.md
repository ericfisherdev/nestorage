# API

Nestorage's JSON API is versioned and mounted at `/api/v1`. Every route under
it is JSON-native end to end: no handler ever answers an HTML page or an
HTML redirect, and every error shares the one envelope documented below.
NSTR-57 extends this file with a generated OpenAPI spec; nothing here is a
substitute for reading that spec once it exists — this is the human-readable
contract behind it.

## Authentication

`/api/v1` accepts exactly two credentials, both presented as a bearer token
in the `Authorization` header:

| Credential | Prefix | Minted by |
|---|---|---|
| Device token | `nsd_` | `POST /api/v1/auth/device-tokens` (email + password exchange) |
| Account API key | `ns_` | `/settings/api-key` (admin, session-authenticated) |

```
Authorization: Bearer nsd_<secret>
Authorization: Bearer ns_<secret>
```

A request with neither an `Authorization` header nor a recognized prefix on
the token it does carry is rejected with `401 unauthorized`.

### Session cookies are never accepted here

`/api/v1` requires the `Authorization` header itself — a session cookie is
never enough, even for a browser that is otherwise signed in. Cookies are
ambient: a browser attaches them to every request automatically, including
one it did not intend to send (the classic CSRF shape). A bearer token
cannot be attached cross-site by another page, so requiring one is what
keeps the API surface immune to that attack. This is enforced by
`identityadapter.RequireAPICredential`, a gate distinct from the global
principal-resolution middleware (`identityadapter.Resolve`, NSTR-24) that
still runs first — `Resolve` is what actually validates whichever bearer
credential is present (or lets a cookie-only request through anonymously,
for the web UI); `RequireAPICredential` is what additionally insists the
`Authorization` header itself was there at all.

## Error envelope

Every error response, from every `/api/v1` endpoint, shares one JSON shape:

```json
{
  "error": {
    "code": "invalid_request",
    "message": "human readable",
    "details": [
      { "field": "device_name", "message": "why" }
    ]
  }
}
```

`details` is omitted entirely except on `invalid_request` responses that
have a specific field to name.

### Code table

| Code | Typical status |
|---|---|
| `unauthorized` | 401 |
| `forbidden` | 403 |
| `not_found` | 404 |
| `method_not_allowed` | 405 |
| `invalid_request` | 400 |
| `invalid_credentials` | 401 |
| `setup_required` | 503 |
| `internal` | 500 |
| `rate_limited` | 429 (NSTR-58) |

An unmatched route or method under `/api/v1` answers through this same
envelope (`not_found` / `method_not_allowed`) rather than net/http's own
plain-text body — see `internal/platform/api.Router` for how.

## Before first-run setup

Every route in the app is blocked until the household's first admin exists
(`identityadapter.SetupGuard`) — except that a web browser normally gets
redirected to the onboarding wizard (`303`, or `HX-Redirect` for an HTMX
request). An API caller has no browser to follow a redirect, so any request
under `/api/v1` reaching the app before setup completes gets `503
setup_required` instead — never an HTML redirect. This also covers the
device-token exchange endpoint, which is reachable with no credential at
all: it answers `setup_required` the same way until an admin exists to
authenticate against.

## Metrics

`/api/v1` traffic is counted separately from the app's generic per-request
metrics, distinguishing which of the three principal kinds served each
request — the generic `nestorage_http_requests_total` (nestcore) has no way
to know this:

```
nestorage_api_requests_total{route, principal_kind, status}
```

`principal_kind` is one of `user`, `integration`, or `anonymous` (a denied,
unauthenticated request) — a bounded set client input can never widen, since
the label is read back from the resolved `domain.Principal`, never from
anything on the request itself.
