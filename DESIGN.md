# Design

## Goal

Provide a local, agent-friendly REWE Pickup MCP server with a stable session,
typed shopping operations, and a fail-closed checkout.

## Modules

```text
MCP tools
   |
ShoppingCore
   |-- ReweGateway  (decoding, retry policy, error taxonomy)
   |     |
   |     BrowserBridge -- Chrome Native Messaging -- MV3 extension -- content script
   |                                                  (executes typed REWE requests
   |                                                   inside the signed-in tab)
   |-- CheckoutGate (human approval, commit, reconciliation)
```

`ShoppingCore` is the external interface and test surface. `ReweGateway` hides
the undocumented REWE interface — it owns response decoding, the retry
policy, and the error taxonomy, but the HTTP request itself executes inside
the browser tab via `BrowserBridge`, not as a Go `http.Client` call (see
[`ADR-0002`](docs/adr/0002-browser-executed-rewe-transport.md) for why).
Session sources are internal adapters and do not leak cookie or browser
concepts into MCP tools.

The distribution consists of one Go binary with MCP-server and native-host
modes plus a small MV3 extension. This is preferred over localhost cookie
transfer, direct cookie-database access, or CDP against a normal Chrome profile.

## Authentication

1. The human logs into `rewe.de` normally in Chrome.
2. `auth_connect` enters `Bootstrapping` and asks the human to click the
   narrowly permissioned browser extension.
3. The extension opens a persistent Native Messaging port. The native host
   long-polls the MCP server for queued work — Chrome only lets the
   extension initiate this connection, so the server cannot push into it.
4. `auth_connect` queues a `session_identity` operation. The content script
   executes it inside the signed-in tab and returns a structured JSON
   result; no cookie, token, or credential ever crosses the bridge.
5. The server validates that result with a read-only check. Only then does
   `auth_connect` report `Active`; the tab binding remains in memory only.
6. An `auth_invalid` result on a read re-queues `session_identity` through
   the same live path (`Refreshing`).
7. Validation must resolve the same account and logical session identity as
   the previous active session, then confirm every store, basket, and
   timeslot binding required by the pending operation. A mismatch fails
   closed and invalidates the changed context plus all of its dependents
   before any request can continue. A successful refresh changes the tab
   binding's revision, not logical identity.
8. The failed read is retried once only after those bindings pass. Mutations
   are not retried without an operation-specific idempotency or
   reconciliation rule.
9. Full browser-session expiry (`auth_invalid`) produces `ReauthRequired`;
   the human logs in and reconnects.

The session state machine is:

```text
Unauthenticated -> Bootstrapping
Bootstrapping -> Active                     (live validation and bindings passed)
Bootstrapping -> Failed                     (bridge unreachable, or any other non-auth failure)
Bootstrapping -> ReauthRequired             (Browser Identity is missing, expired, or rejected)
Active -> Refreshing -> Active              (validated refresh)
Refreshing -> Failed                        (bridge unreachable, or any other non-auth failure)
Refreshing -> ReauthRequired                (Browser Identity is missing, expired, or rejected)
ReauthRequired -> Bootstrapping             (human reconnects)
Active -> Unauthenticated                   (`auth_disconnect` or MCP-server exit)
```

`Failed` is one state covering every non-auth rejection (`BridgeUnavailableError`,
`RateLimitError`, `UpstreamChangeError`) — `auth_status` reports the state, not
which of these occurred, matching this project's sanitized-output posture.
Refresh is single-flight. The browser owns OpenID Connect, remembered-account,
and anti-bot state; `ReweGateway` owns the in-memory API session and retry
policy. The browser must be available for refresh, but does not need to stay
on an active REWE tab between refreshes. The process and session lifetimes
are recorded in
[`ADR-0002`](docs/adr/0002-browser-executed-rewe-transport.md) (ADR-0001 is
`rejected`).

## MCP interface

The interface is task-oriented and typed:

- `auth_connect`, `auth_status`, `auth_disconnect`
- `stores_search`, `store_select`
- `products_search`
- `basket_get`, `basket_apply`
- `timeslots_list`, `timeslot_select`
- `orders_list`, `order_get`
- Phase 2: `order_prepare`, `order_status`

`receipts_list`/`receipt_get` were removed after live investigation found no
REWE UI path for a receipts-specific feature — see
[`docs/known-limitations.md`](docs/known-limitations.md).

Phase 1 registers no order-placement, order-cancellation, checkout, payment, or
approval tools. An upstream endpoint is not reachable merely because it exists;
only explicitly registered Phase 1 operations can cross `ShoppingCore`.

Raw REWE responses remain behind `ReweGateway`. Tool annotations describe
read-only, destructive, idempotent, and open-world behavior, but the server
enforces all safety rules itself.

## Checkout

`order_prepare` reloads the authoritative basket, prices, fees, store, and
timeslot, then creates a short-lived approval ID. The human reviews that state
on a local approval page presented through URL elicitation, or through a local
fallback when the MCP client lacks that capability.

Approval is invalidated by any relevant state change. The `CheckoutGate`
performs the commit only after the human approves. A timeout is never treated as
a simple failure or retried blindly; order history and basket state are used to
reconcile an ambiguous outcome.

## Compatibility strategy

- Decode upstream payloads into stable domain types at the `ReweGateway` seam.
- Validate critical responses and fail with an `UpstreamChange` error.
- Maintain sanitized contract fixtures for every supported flow.
- Separate authentication, rate-limit, validation, upstream-change, and ambiguous-result errors.
- Redact cookies, tokens, addresses, and receipt content from logs and diagnostics.
