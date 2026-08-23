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
- `basket_apply` (add path) — `POST /shop/api/baskets/listings/{listingId}`
  confirmed live (card #11/#12, 2026-08-21, explicit human sign-off):
  no `basketId` prefix needed, REWE resolves/creates the basket implicitly.
  `basket_get` and `basket_apply`'s update/remove paths remain untested.
- `timeslot_select` — `POST /shop/api/timeslot-reservations` confirmed live
  (card #11/#12, 2026-08-21, explicit human sign-off) with a request
  body of `{slotId}` only, no `customerId`/`wwIdent`/`zipCode` — see below.

## Known limitations

- **`basket_get` and `basket_apply`'s update/remove paths are un-live-tested.**
  Wired against REWE's real `/shop/api/baskets/*` endpoints (URL prefix and
  field shapes confirmed against `Tobi4s1337/karrt`'s source, and
  `Product.ID` decodes from the confirmed-real `listingId`); only the add
  path has been exercised live so far (see above).
- **`timeslot_select` sends no response-shape guarantee.** The request path
  and body are confirmed live (see above), but the response body wasn't
  independently observable from that capture (browser performance timing
  exposes request paths, not POST response bodies) — `SelectTimeSlot`
  deliberately does not decode REWE's response at all; a successful call is
  trusted at face value and the context is rebound locally. If REWE's real
  response ever needs surfacing (e.g. a reservation expiry), that needs its
  own live-verified decode. The `customerId` this previously failed closed
  on was resolved by dropping it entirely — `Tobi4s1337/karrt`'s
  never-resolved guess turned out not to be required at all, per
  `yannick-cw/korb`'s real, working implementation and card #11's live
  confirmation (`docs/spikes/checkout.md`).
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
