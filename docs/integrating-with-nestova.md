# Integrating with Nestorage

This is the "how do I actually call this thing" guide for an external
client — Nestova's own integration, or the Android app. It assumes you have
already read [`docs/api.md`](api.md), which documents the authentication
scheme, the error envelope, and the full endpoint-by-endpoint contract; this
guide does not repeat that material, only points at it and then walks
through using it. The machine-readable version of the same contract is the
OpenAPI 3.1 specification: [`internal/platform/api/openapi.yaml`](../internal/platform/api/openapi.yaml)
in this repository, served at runtime by every deployment at
`GET /api/v1/openapi.yaml` (unauthenticated — the contract is public even
though the data behind it is not).

## Getting a credential

Every `/api/v1` request needs exactly one bearer credential in its
`Authorization` header. Which one you mint depends on who you are:

- **Nestova's own integration** uses the **account API key** — one
  credential for the whole household, minted (and rotated or revoked) by an
  admin at `/settings/api-key`. It authenticates as a system principal with
  no attached user, which is why every create endpoint that needs a real
  person (locations, bins, items, check-out, return requests) answers `403`
  for it — Nestova's own writes on a household's behalf should go through a
  real member's device token instead wherever the action needs attribution.
- **The Android client** trades a member's email and password for a
  **per-device token**, one per phone, revocable independently of any
  other device:

  ```sh
  curl -X POST https://<host>/api/v1/auth/device-tokens \
    -H 'Content-Type: application/json' \
    -d '{"email":"maya@example.com","password":"correct-horse-battery-staple","device_name":"Maya'"'"'s phone"}'
  ```

  ```json
  {"token": "nsd_...", "id": "...", "name": "Maya's phone", "created_at": "2026-07-25T14:30:00Z"}
  ```

  `token` is the plaintext device token. It is returned exactly once, here
  — store it; there is no way to recover it later, only to revoke it and
  mint a new one.

Both credentials are presented the same way:

```
Authorization: Bearer nsd_<secret>     # device token
Authorization: Bearer ns_<secret>      # account api key
```

## Base URL and transport

There is no fixed public origin — each household appliance serves this API
from its own LAN or Tailscale hostname, with TLS terminated by
`tailscale serve` (or a reverse proxy on the LAN). This is exactly why the
OpenAPI document's own `servers` entry is the single relative path `/`
rather than a real host: fill in whichever host you were configured to
reach.

## Request conventions

- **Errors** are always the same JSON envelope, `{"error": {"code",
  "message", "details"}}` — branch on `code`, never on `message` (see
  `docs/api.md`'s own code table for the full vocabulary).
- **Pagination** is cursor-based on every list and history endpoint: pass
  the previous page's `next_cursor` as the `before` query parameter to
  fetch the next one; `next_cursor` is `null` on the last page.
- **Enums** (bin `visibility`, item `state`, return request `status`,
  history event `kind`) are always their lower-case string values, never a
  numeric code.
- **Item and bin operations are safe to retry.** A network timeout after
  you sent a check-out or add-to-bin request does not require you to first
  read back the item's state before deciding whether to resend it — resend
  it. If it already applied, you get `200` back with the item's current
  state instead of a duplicate mutation or an error; see `docs/api.md`'s
  own idempotency table for exactly which conflict each endpoint's retry
  resolves to a success.

## Worked example: an item's full lifecycle

Every request below carries `Authorization: Bearer $TOKEN` — a device token
or an account API key, either works for every step except item creation and
check-out, which need a real person (a device token). Set:

```sh
export HOST=https://nestorage.example.ts.net
export TOKEN=nsd_...
```

### 1. Create a location

```sh
curl -s -X POST "$HOST/api/v1/locations" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Garage","description":"Attached garage"}'
```

```json
{"id":"loc_01H...","name":"Garage","description":"Attached garage","parent_id":null,"created_at":"...","updated_at":"..."}
```

### 2. Create a bin in that location

```sh
curl -s -X POST "$HOST/api/v1/bins" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"code":"BIN-A01","name":"Camping shelf","location_id":"loc_01H..."}'
```

```json
{"id":"bin_01H...","code":"BIN-A01","name":"Camping shelf","location_id":"loc_01H...","visibility":"public","item_count":0,"created_at":"...","updated_at":"..."}
```

`visibility` defaulted to `"public"` — it was omitted from the request body.

### 3. Create an item, sitting in that bin

```sh
curl -s -X POST "$HOST/api/v1/items" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"name":"Camping stove","quantity":1,"bin_id":"bin_01H..."}'
```

```json
{"id":"item_01H...","name":"Camping stove","quantity":1,"state":"in_bin","bin_id":"bin_01H...","held_by":null,"created_at":"...","updated_at":"..."}
```

### 4. Check it out

```sh
curl -s -X POST "$HOST/api/v1/items/item_01H.../check-out" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"note":"Weekend trip"}'
```

```json
{"id":"item_01H...","state":"checked_out","bin_id":null,"held_by":"user_01H...", "...": "..."}
```

### 5. Return it to the same bin

```sh
curl -s -X POST "$HOST/api/v1/items/item_01H.../return" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"bin_id":"bin_01H..."}'
```

```json
{"id":"item_01H...","state":"in_bin","bin_id":"bin_01H...","held_by":null, "...": "..."}
```

### 6. Read its history

```sh
curl -s "$HOST/api/v1/items/item_01H.../history" \
  -H "Authorization: Bearer $TOKEN"
```

```json
{
  "events": [
    {"id":"...", "kind":"returned", "occurred_at":"...", "bin":{"id":"bin_01H...","label":"BIN-A01 — Camping shelf"}, "...": "..."},
    {"id":"...", "kind":"removed",  "occurred_at":"...", "note":"Weekend trip", "...": "..."},
    {"id":"...", "kind":"added",    "occurred_at":"...", "bin":{"id":"bin_01H...","label":"BIN-A01 — Camping shelf"}, "...": "..."},
    {"id":"...", "kind":"created",  "occurred_at":"...", "...": "..."}
  ],
  "next_cursor": null
}
```

Newest first, one row per state transition — this is the same event log the
web item detail page's own history panel reads.

### 7. An invalid transition, on purpose

Trying to check out the same item a second time while it is already sitting
in a bin (rather than resending a check-out that already succeeded) fails
with a typed error a client can branch on:

```sh
curl -s -o /dev/null -w '%{http_code}\n' -X POST "$HOST/api/v1/items/item_01H.../check-out" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d '{}'
```

Run twice in a row, the second call answers `200` with the item's current
state (the retry case above) — but if someone else already holds it, both
calls instead answer:

```
409
```

```json
{"error": {"code": "item_already_checked_out", "message": "item is already checked out to someone else"}}
```

`item_already_checked_out` is exactly the code to branch on — not the
`message` string, which is free to change.

## Federation (Nestova's own reconciliation)

`GET /api/v1/federation/accounts` and `PUT /api/v1/federation/members/{member_id}`
(NSTR-101) are the inverse of the "creates need a person" rule above: every
other create endpoint (locations, bins, items, check-out, return requests)
**rejects** the account API key and requires a real person's device token.
These two endpoints do the opposite — they **require** the account API key
and reject a device token with `403`, because reconciling Nestova's members
against Nestorage's accounts is Nestova's own integration acting on the
household's behalf, never something a member's own phone should drive. See
`docs/api.md`'s own Federation section for the full request/response
contract, the household-binding rule, and the idempotency guarantee a
repeated re-push relies on.

## Where to go next

- [`docs/api.md`](api.md) — the full prose contract: authentication, the
  error envelope, every endpoint's request/response shape, and the
  idempotency table this guide's step 7 draws from.
- [`internal/platform/api/openapi.yaml`](../internal/platform/api/openapi.yaml)
  — the same contract, machine-readable, also served live at
  `GET /api/v1/openapi.yaml`. Point an OpenAPI-aware client generator at
  the served URL rather than vendoring a copy of the file, so a generated
  client always matches whichever version of Nestorage it is actually
  talking to.
