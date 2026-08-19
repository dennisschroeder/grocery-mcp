# Known limitations and reauthentication UX

Phase 1 status as of 2026-08-19, after card #10's live-testing rounds
against a real, signed-in REWE account. Supersedes the acceptance record in
[`browser-bridge.md`](browser-bridge.md), which only covers card #13's
2026-08-18 proof.

## Confirmed working live

- `auth_connect` / `auth_status` / `auth_disconnect` — full state machine,
  including `ReauthRequired` on a genuinely logged-out session (see below).
- `session_identity` (internal, drives `auth_connect`) — `GET
  /shop/api/favorites`.
- `orders_list` / `order_get` — `GET /orders`, `GET /orders/{id}`.
- `products_search` — `GET /shop/api/products`, given a real market ID.
- `timeslots_list` — `GET /shop/api/timeslots/pickup/overview`, given a real
  market ID and postal code.
- `stores_search` — `GET /api/marketselection/zipcodes/{zip}/services/pickup`,
  REWE's real store locator (discovered live 2026-08-19 by capturing REWE's
  own "change market" flow in DevTools, replacing an earlier heuristic that
  reused product search and never actually worked). 17 real markets
  returned for a real postal code, nearest first, with real market IDs,
  names, and distances — no more manual DevTools workaround needed.
- Bridge-socket restart: the native host survives the MCP server *process*
  dying and reconnects automatically once a new one starts listening on the
  same socket, no fresh extension click needed. This is distinct from a full
  MCP-server restart losing its in-memory auth/session state — see
  Reauthentication UX below for that case, which still needs a click.
- Tab close/reopen: closing the REWE tab and re-clicking the extension
  correctly recreates it and resumes operation.

## Known limitations

- **`basket_get` / `basket_apply` are entirely un-live-tested.** Both are
  wired against REWE's real `/shop/api/baskets/*` endpoints (URL prefix and
  field shapes confirmed against `Tobi4s1337/karrt`'s source, and
  `Product.ID` now decodes from the confirmed-real `listingId`), but no live
  call has actually been made — deliberately deferred, since a real call
  would add a real item to the signed-in account's real REWE basket. Needs
  explicit sign-off before testing live.
- **`timeslot_select` always fails closed.** REWE's `POST
  /timeslot-reservations` needs a `customerId` with no known source in
  anything this project's auth flow produces. Even `Tobi4s1337/karrt`'s own
  reference client ships this same gap unresolved. Returns a typed
  `ValidationError` rather than guessing at a value that could corrupt a
  real reservation.
- **No digital receipts feature.** `receipts_list`/`receipt_get` were
  removed after live investigation found no REWE UI path for a
  receipts-specific view — REWE's own "Deine Einkäufe" order-history page
  and each order's detail view are the only purchase-history surfaces that
  exist, and both are already covered by `orders_list`/`order_get`.
- **No checkout, order-placement, cancellation, payment, or approval
  capability anywhere in Phase 1.** This is by design (Phase 2 scope, cards
  #11/#12, untouched), not an oversight — confirmed by grepping the
  registered tool list.
- **Linux is untested.** `NativeHostManifestPath` supports it and the code
  has no macOS-specific logic, but every live round this session ran on
  macOS only.
- **`products_search` results report every product as available with no
  brand.** REWE's real `/shop/api/products` response carries no
  `available`/`isBuyable` field on search hits at all (confirmed live), so
  `shopping.Product.Available` is now hardcoded `true` for every result
  rather than decoded from the wire — REWE may still reject an unavailable
  item at `basket_apply` time. There's also no `brand` field in this shape;
  `Product.Brand` stays permanently empty.

## Reauthentication UX

`auth_status` (and any tool call that surfaces `AuthError`) returns an
`instruction` field with the exact human-facing text to act on:

| State | `action_required` | `instruction` |
|---|---|---|
| `Bootstrapping` | true | "Click the grocery-mcp extension in signed-in Chrome." |
| `ReauthRequired` | true | "Log into REWE in Chrome, then click the grocery-mcp extension." |
| `Active` | false | (empty) |

Confirmed live (2026-08-19): signing out of REWE and attempting an
operation classifies the failure as `AuthError`, and `Accept()` moves the
state to `ReauthRequired` in ~70ms — not a hang, not misclassified as a
generic failure. No password, 2FA, or CAPTCHA ever passes through the
extension; the human always completes REWE login in their own browser tab
first.

A full MCP-server *process* restart (not just its bridge socket reconnecting
— see above) loses the memory-only tab binding and auth state entirely
(nothing is persisted to disk), so the next connection needs one fresh
extension click even though the underlying REWE browser session is usually
still valid. This is intentional (AGENTS.md: "store sessions in memory by
default"), not a bug — the fix in `internal/browserbridge/host.go` only
covers the socket reconnecting under an already-running server, not a full
process restart.
