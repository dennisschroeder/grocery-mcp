# Session stability diagnosis — 2026-09-01

## Observed failure

The interrupted shopping run established a repeatable sequence:

1. Product searches and basket reads succeeded.
2. `basket_apply` failed after 69.003 seconds.
3. Immediate reads then failed after 5.003 seconds.
4. `auth_status` still reported an authenticated state and `auth_connect`
   returned immediately, but neither restored the transport.
5. Clicking the extension restored reads. The same failure occurred with a
   single-item mutation, so batch size was not the cause.

## Root causes

- The content script's REWE `fetch` and the service worker's
  `tabs.sendMessage` had no response deadline.
- The native host stopped after waiting 35 seconds for an extension response.
  Its exit closed Chrome's Native Messaging port.
- The bridge owner's combined socket and operation deadlines surfaced that
  loss to the MCP caller at 69 seconds. With no native host polling afterward,
  new calls hit the separate five-second dispatch timeout.
- Authentication and bridge liveness were conflated: the in-memory Auth
  service could remain `Active` after the transport had disappeared, and
  `auth_connect` was a no-op in that state.
- Each MCP process owns an intentionally separate, memory-only Auth service.
  A newly spawned Claude/Cowork/Code process therefore began as
  `Unauthenticated` even though the shared extension transport was usable.

## Implemented correction

- The service worker bounds a content-script operation at 30 seconds and
  reports `operation_timeout`. Reads may retry an unreachable content script,
  but it never retries a potentially dispatched mutation.
- The native host reports its own bounded timeout to the bridge owner and
  continues polling instead of terminating. A late response is discarded by
  request ID, so it cannot satisfy or poison the next operation.
- `ambiguous_result` is preserved across the bridge vocabulary so mutation
  callers fail safely and reconcile instead of treating it as an unknown
  upstream shape.
- Every `serve` process starts browser validation automatically. If the port
  is absent it exposes `action_required` but keeps retrying, so one extension
  click is sufficient; no second `auth_connect` call is required.
- An explicit `auth_connect` while Auth is still `Active` now performs a live
  refresh instead of returning the stale state unchanged.

No cookies, credentials, or browser-session identifiers are persisted or
shared by these changes.

## Verification gates

Automated regression coverage proves:

- a timed-out operation does not terminate the native host;
- its late response is ignored and the next operation succeeds;
- an unresponsive content script is bounded and not retried;
- an initially missing bridge becomes `Active` without another Connect call;
- a live server authenticator starts validation without an MCP tool call;
- the full Go race suite and extension test suite remain clean.

The release needs one live acceptance run with the installed extension:

1. Start two MCP surfaces while the extension is already connected; both must
   reach `Active` without another click.
2. Execute a basket mutation and reconcile it with `basket_get`.
3. If the mutation times out, verify that `basket_get` works afterward without
   reloading or clicking the extension.
4. Select and verify a pickup timeslot.

## Separate blockers for the full ordering objective

This change restores a continuous search/cart/timeslot session. It does not
enable order placement. The checkout commit remains deliberately fail-closed:
the irreversible `www.rewe.de` request shape, MARKET_PAYMENT behavior, order
identity, and post-commit reconciliation must be verified live before the
human approval gate may call it. `products_search` also lacks structured
brand, promotion, base-unit-price, and package-increment fields, so reliable
offer comparison and weight-to-quantity conversion remain separate contract
work rather than assumptions made by an agent.
