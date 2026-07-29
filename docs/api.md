# API

Nestorage's JSON API is versioned and mounted at `/api/v1`. Every route under
it is JSON-native end to end: no handler ever answers an HTML page or an
HTML redirect, and every error shares the one envelope documented below.
NSTR-57's own OpenAPI 3.1 specification
([`internal/platform/api/openapi.yaml`](../internal/platform/api/openapi.yaml),
served at `GET /api/v1/openapi.yaml`) is the machine-readable version of the
same contract — this file is the human-readable prose behind it. See
[`docs/integrating-with-nestova.md`](integrating-with-nestova.md) for a
worked walkthrough of driving the API end to end.

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
| `conflict` | 409 |
| `household_mismatch` | 403 (NSTR-101) — a household binding is already recorded and the request names a different one. |

An unmatched route or method under `/api/v1` answers through this same
envelope (`not_found` / `method_not_allowed`) rather than net/http's own
plain-text body — see `internal/platform/api.Router` for how.

### Typed codes for the operation and history endpoints

The two sections below route errors through a second, more specific code
vocabulary instead of the generic `not_found`/`conflict` above — a
multi-resource endpoint (add-to-bin can fail on either the item or its
destination bin) needs a code a client can actually branch on to tell which:

| Code | Status | Meaning |
|---|---|---|
| `item_not_found` | 404 | The item in the path is unknown or not visible to the caller. |
| `bin_not_found` | 404 | The bin (in the path, or named by `bin_id`) is unknown or not visible. |
| `location_not_found` | 404 | The location named by `location_id` is unknown or not visible. |
| `return_request_not_found` | 404 | The return request in the path is unknown, or belongs to a different item/requester. |
| `item_already_in_bin` | 409 | The item is already sitting in a *different* bin than the one requested. |
| `item_already_checked_out` | 409 | The item is already held by a *different* user than the caller. |
| `item_not_checked_out` | 409 | The item is not checked out, and is not already sitting in the requested bin either. |
| `requester_holds_item` | 409 | A return was requested against the caller's own held item. |
| `return_request_not_open` | 409 | The return request has already been fulfilled (cancelling it is no longer possible). |
| `holder_required` | 403 | The account API key (an integration principal) attempted a check-out or return-request — both require a real person. |
| `invalid_return_request_message` | 422 | The `message` field is blank or over `MaxReturnRequestMessageRunes` (500) characters — carries a field detail naming `message`. |

## Item and bin state transitions

Every state transition available in the web UI is available here too, over
the identical `OperationService`/`BinMover`/`ReturnRequestService` rules —
web and API can never disagree on what a valid transition is.

| Method & path | Body | Delegates to |
|---|---|---|
| `POST /api/v1/items/{id}/add-to-bin` | `{"bin_id": "..."}` | `OperationService.AddToBin` |
| `POST /api/v1/items/{id}/check-out` | `{"note": "..."}` (optional) | `OperationService.RemoveFromBin` |
| `POST /api/v1/items/{id}/return` | `{"bin_id": "...", "note": "..."}` (`note` optional) | `OperationService.ReturnToBin` |
| `POST /api/v1/bins/{id}/move` | `{"location_id": "..."}` | `BinMover.Move` |
| `POST /api/v1/items/{id}/return-requests` | `{"message": "..."}` (optional) | `ReturnRequestService.Request` |
| `POST /api/v1/items/{id}/return-requests/{requestID}/cancel` | *(none)* | `ReturnRequestService.Cancel` |

A successful item transition (add-to-bin, check-out, return) answers `200`
with the item projection `GET /api/v1/items/{id}` itself returns. Move-bin
answers `200` with `{"bin_id", "from_location_id", "to_location_id",
"moved_at"}`. Creating a return request answers `201` with the created
request; every other mutation here answers `200` with the return request's
current state.

### Idempotency

The domain guards already fail a retried request inside its own transaction
before any event row is appended, so a retry can never double-apply a
placement change or duplicate an event. On top of that guard, this API
answers each retry as a **success**, not an error, whenever the retry's
requested end state already holds:

| Endpoint | Retried when | Answers |
|---|---|---|
| add-to-bin | Item already sitting in the requested bin | `200`, current item state |
| check-out | Item already held by the calling user | `200`, current item state |
| return | Item already sitting in the requested bin | `200`, current item state |
| move-bin | Bin already at the requested location | `200`, `moved_at: null` (no move happened this call) |
| create return request | Caller already has an open request on the item | `200`, that existing request |
| cancel return request | Request is already cancelled | `200`, that request's (cancelled) state |

Any other case — the item is somewhere else, held by someone else, the bin
is elsewhere, or the return request has been fulfilled rather than
cancelled — answers the matching `409` from the typed-code table above.

## Item photos

| Method & path | Body | Answers |
|---|---|---|
| `GET /api/v1/items/{id}/photos` | — | `200`, JSON array of photo DTOs |
| `POST /api/v1/items/{id}/photos` | `multipart/form-data`, one file part named `photo` | `201`, photo DTO |
| `GET /api/v1/items/{id}/photos/{photoID}?size=full\|thumb` | — | `302` redirect (S3 backend) or the image bytes (local backend) |
| `PUT /api/v1/items/{id}/photos/{photoID}/primary` | — | `204` |
| `DELETE /api/v1/items/{id}/photos/{photoID}` | — | `204` |

Every route delegates to the exact same `PhotoService` (NSTR-34/37) the web
gallery uses — EXIF stripping, validation, and storage are identical on
both surfaces; nothing photo-related is validated twice. There is no
reorder endpoint: photo ordering is a web-only gallery affordance.

A photo DTO:

```json
{
  "id": "...",
  "primary": true,
  "content_type": "image/jpeg",
  "size_bytes": 182933,
  "url": "/api/v1/items/{id}/photos/{photoID}",
  "thumb_url": "/api/v1/items/{id}/photos/{photoID}?size=thumb"
}
```

`url`/`thumb_url` always point back at this API's own serve route — never a
raw storage locator — so a client never has to special-case which storage
backend is configured; the serve route itself is what redirects to a
presigned URL when one is available.

### Upload

The upload request body is streamed — never buffered whole — so an
oversized upload is rejected promptly rather than after the server has
received the entire file:

| Failure | Answers |
|---|---|
| `Content-Length` already declares more than the configured max upload size | `413`, before any body byte is read |
| The actual body exceeds the max upload size (a lying or absent `Content-Length`) | `413`, after at most the max upload size plus a small framing margin |
| No `photo` file part in the body | `422` |
| Unsupported image type (not JPEG or PNG) | `415` |
| Unreadable or corrupt image data | `400` |
| Item unknown or not visible to the caller | `404` |

Every not-found here — the item or the photo — answers the generic
`not_found` code: like every other route in this API, "doesn't exist" and
"exists but you can't see it" are never distinguished.

## Event history

| Method & path | Query | Delegates to |
|---|---|---|
| `GET /api/v1/items/{id}/history` | `before`, `limit` | `ItemEventRepository.ListByItem` |
| `GET /api/v1/bins/{id}/history` | `before`, `limit` | `ItemEventRepository.ListByBin` |

Both endpoints check the item's or bin's own visibility (a 404 masks
"unknown" and "invisible" identically) before ever reading an event, and
page newest-first: `limit` defaults to 30, clamped to 100; `before` is the
opaque cursor `next_cursor` carries, `null` on the response's own last page.

```json
{
  "events": [
    {
      "id": "...",
      "kind": "added",
      "occurred_at": "2026-07-25T14:30:00Z",
      "note": null,
      "actor": { "kind": "user", "label": "Maya", "user_id": "..." },
      "bin": { "id": "...", "label": "BIN-A01 — Pantry shelf" },
      "from_location": null,
      "to_location": null,
      "changed_fields": [],
      "item": { "id": "...", "name": "Camping stove" }
    }
  ],
  "next_cursor": null
}
```

`actor.user_id` is `null` exactly for the account API key (an integration
principal, which has no person behind it) — `actor.kind`/`actor.label` are
always present for both principal kinds. `bin`/`from_location`/`to_location`
are `null` except on the event kinds that carry them (added/removed/returned
for `bin`, moved for the two locations). Bin history's own `item` field is
what lets one bin's history span the several items that have sat in it.

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

## Rate limiting

Every `/api/v1` request is rate limited, in-process and in-memory — no
Redis or other external limiter store; the appliance is a single Go
binary. Limits are per **principal**, not per connection or per process, so
one runaway client is contained without affecting anyone else in the
household, and the web UI is unaffected regardless of how hard an API
client is being limited: the limiter mounts only on the `/api/v1` surface,
and session cookies are never accepted there in the first place (see
Authentication above).

| Bucket | Scope | Rate | Burst | Keyed by |
|---|---|---|---|---|
| Account-wide | `api` | `API_RATE_LIMIT_RPS` (default 10/s) | `API_RATE_LIMIT_BURST` (default 30) | The resolved principal — `user:<id>`, the fixed string `integration` (there is exactly one account API key), or the caller's client IP for an anonymous request |
| Token exchange | `auth` | `AUTH_RATE_LIMIT_RPM` (default 10/min) | `AUTH_RATE_LIMIT_BURST` (default 5) | Always the caller's client IP — `POST /api/v1/auth/device-tokens` is unauthenticated by nature |

`POST /api/v1/auth/device-tokens` sits behind **both** buckets — whichever
is tighter is what a caller observes, and in practice that is always the
stricter `auth` bucket.

A denied request answers `429` through the shared error envelope, coded
`rate_limited`, with a `Retry-After` header naming whole seconds (rounded
up, floored at 1 — never "retry immediately"):

```json
{
  "error": {
    "code": "rate_limited",
    "message": "too many requests"
  }
}
```

Recovery needs no separate action: once the token bucket refills, the very
next request succeeds. Every value above is defaulted, so an existing
deployment keeps booting with rate limiting already active and no new
configuration required.

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

Every `429` a caller receives is additionally counted here, distinguishing
which of the two buckets above (see "Rate limiting") denied it:

```
nestorage_api_rate_limited_requests_total{principal, scope}
```

`principal` is `user:<id>`/`integration` for an authenticated caller, or the
fixed label `anonymous` for one keyed by client IP — never the raw
(attacker-controlled) address, which would let a spray of distinct source
IPs blow up the metric's own cardinality. `scope` is `api` or `auth`.
