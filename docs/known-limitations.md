# Known limitations and reauthentication UX

Phase 1 status as of 2026-08-31, including the multi-process reconciliation
iteration and earlier live-testing rounds
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
- Bridge-socket restart: the native host waits without a retry deadline while
  Chrome's port remains open and reconnects when a new owner starts. A new
  process starts validation automatically before `auth_connect` asks for a click.
  Individual validation attempts time out and retry, so a lost bridge response
  cannot leave one MCP process permanently stuck in `Validating`.
- Tab close/reopen: closing the REWE tab and re-clicking the extension
  correctly recreates it and resumes operation.
- `basket_apply` (add path) — `POST /shop/api/baskets/listings/{listingId}`
  confirmed live (card #11/#12, 2026-08-21, explicit human sign-off):
  no `basketId` prefix needed, REWE resolves/creates the basket implicitly.
  `basket_get` and `basket_apply`'s update/remove paths remain untested.
- `basket_listings_get` — read-only recovery of validated listing IDs from
  recent basket request URLs in the connected shop tab. It returns newest
  first and never exposes request headers, cookies, or response bodies.
- `timeslot_select` — `POST /shop/api/timeslot-reservations` confirmed live
  (card #11/#12, 2026-08-21, explicit human sign-off) with a request
  body of `{slotId}` only, no `customerId`/`wwIdent`/`zipCode` — see below.

## Known limitations

- **The 2026-09-01 session-stability fix still needs a live release round.**
  Regression tests now prove that a timed-out browser operation reports
  `operation_timeout`, keeps the Native Messaging port alive, discards its
  eventual late response by request ID, and successfully executes the next
  operation. A separate extension regression bounds an unresponsive content
  script at 30 seconds. The remaining acceptance gate is a real installed
  build performing a slow/failed basket mutation followed by `basket_get`
  without another extension click.

- **Listing discovery depends on recent tab activity.**
  `basket_listings_get` returns an empty list when the tab's resource timing
  buffer contains no recent basket request; it does not scrape the DOM or
  issue a mutation to manufacture one.

- **Basket update/remove still need final live verification.** The add path
  and live basket response shape are confirmed. `basket_apply` now performs
  changes sequentially, discovers the basket ID, and reads the authoritative
  basket afterward; `reconciled=false` explicitly marks an unresolved read.
- **Timeslot reconciliation needs final live verification.** The live-proven
  reservation request is `{slotId}` only. `timeslot_select` now checks the
  overview before and after the single mutation and returns an ambiguous
  result if the post-mutation read cannot determine the selected slot.
- **No digital receipts feature.** `receipts_list`/`receipt_get` were
  removed after live investigation found no REWE UI path for a
  receipts-specific view — REWE's own "Deine Einkäufe" order-history page
  and each order's detail view are the only purchase-history surfaces that
  exist, and both are already covered by `orders_list`/`order_get`.
- **No order-placement, cancellation, or payment capability anywhere.**
  `order_prepare`/`order_status` (card #12) create and check a human-approved
  `CheckoutGate` snapshot, but the actual REWE commit call is deliberately
  stubbed and always fails closed — checkout creation happens server-side
  on `www.rewe.de` and was unobservable during card #11's investigation
  (`docs/spikes/checkout.md`), so no unverified request shape was wired to a
  real, irreversible order-placing endpoint. No MCP tool can reach the
  commit path at all; only a human's own action on the local approval page
  can attempt it, and today that attempt always reports `commit_failed`.
- **The approval page's action token raises the bar but isn't proof of a
  human.** Card #12's review found `order_prepare`'s `ApprovalURL` alone was
  a bearer credential sufficient to approve/decline (a `POST` to it needed
  nothing else) — fixed by requiring a second, per-approval token that only
  the rendered review page's HTML carries, never any MCP tool output
  (`internal/checkout/page.go`, `TestOrderPrepareOutputAloneCannotApprove`).
  This means the value the model actually receives from `order_prepare` is
  no longer independently sufficient. It is not a complete fix: an agent
  with unrestricted HTTP tool access could still fetch and parse the review
  page itself, since nothing here can cryptographically distinguish a real
  browser click from a sufficiently capable HTTP-scripting client on the
  same machine. Real proof-of-human-interaction needs the server-initiated
  MCP URL elicitation path DESIGN.md already flags as an open follow-up.
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
| `Bootstrapping` / prolonged `Validating` | true | "Click the grocery-mcp extension in signed-in Chrome." |
| `ReauthRequired` | true | "Log into REWE in Chrome, then click the grocery-mcp extension." |
| `Active` | false | (empty) |

Confirmed live (2026-08-19): signing out of REWE and attempting an
operation classifies the failure as `AuthError`, and `Accept()` moves the
state to `ReauthRequired` in ~70ms — not a hang, not misclassified as a
generic failure. No password, 2FA, or CAPTCHA ever passes through the
extension; the human always completes REWE login in their own browser tab
first.

A full MCP-server process restart loses its memory-only Auth service and
`ShopSessionID`, but not Chrome's native port. Every new process starts
automatic validation through that existing port and keeps retrying while an
absent port produces the click instruction. Clicking the extension is enough
for a waiting process to recover; a second `auth_connect` call is unnecessary.
Account-scoped Store/Basket/Timeslot identifiers are persisted separately in
the private bridge runtime directory and contain no authentication material.
