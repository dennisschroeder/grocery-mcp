---
status: accepted
date: 2026-08-18
---

# Browser-executed REWE transport

ADR-0001's provisional direct-HTTP-session topology (outcome B) is rejected:
card #4's live gate delivered a valid, browser-sourced `rstp` through the
exact-origin Native Messaging bridge, but REWE's API rejected it as
unauthenticated when presented by a Go `net/http` client. REWE sits behind
Cloudflare bot management, which fingerprints TLS/JS execution — a
characteristic a Go HTTP client cannot reproduce even with a valid session
cookie. No cookie scope was broadened to work around this.

Card #13 proved outcome C live on 2026-08-18: REWE API calls execute *inside*
the signed-in browser tab itself, through a fixed, allowlisted set of typed
operations run by an isolated content script. Only structured JSON results —
never cookies, tokens, or any credential material — cross back to the MCP
server. Two operations were proven against a real, live, signed-in account:
`session_identity` (`GET /shop/api/favorites`, 5887 bytes of real JSON,
correctly parsed) and `products_search` (`GET /shop/api/products`, 15518
bytes of real search results). The full auth state machine reached `Active`
on both passes. This is the first successful live authentication this project
has achieved.

## Process topology and lifetimes

```text
Chrome + MV3 extension
  owns the signed-in REWE tab; runs a content script limited to
  https://www.rewe.de/* that executes exactly the allowlisted operations
          |
          | Chrome Native Messaging (persistent port, extension-initiated)
          v
grocery-mcp native-host mode (long-lived while the port stays open)
  drives a poll loop: ask the MCP server for queued work, relay it down
  to the content script, relay the structured result back
          |
          | user-only local socket
          v
grocery-mcp MCP-server mode (one active v1 instance per OS user)
  owns ShoppingCore, ReweGateway, Shop Session, and Shopping Context;
  queues operations and blocks until a poll delivers a result
```

Chrome only ever lets the *extension* initiate a Native Messaging connection
— a Go process can never push work into Chrome unprompted. The native host
therefore long-polls the server ("is there queued work for the browser?")
once the extension's port is open, rather than the server pushing directly.
This keeps the server's per-connection socket handling (already hardened:
origin pinning, strict decoding, size limits) unchanged — one request per
connection — instead of turning it into a multiplexed duplex stream.

- The content script's fixed operation switch is the real security boundary:
  no operation string outside `session_identity`, `products_search`, or
  `basket_get` is ever acted on, and every fetch target is either a literal
  or built only from validated, typed parameters — never a caller-supplied
  URL, method, header, or script.
- The Go-side operation allowlist is defense in depth only; it cannot itself
  make the browser do anything the content script's switch doesn't already
  permit.
- The extension requests no cookie-reading permission at all. `chrome.cookies`
  is never called.

## Session and failure behavior

- `TabBinding` replaces outcome B's `Credential`: there is no secret to hold,
  only proof that a bound tab answered an operation recently. A `revision`
  counter bumps on each successful operation and stands in for outcome B's
  `SameRevision` rotation-proof; a refresh is trusted only if the previous
  binding was touched within a bounded idle window (10 minutes), since there
  is no rotating value left to compare.
- The 6-state auth state machine (`Unauthenticated`, `Bootstrapping`,
  `Active`, `Refreshing`, `ReauthRequired`, `Failed`) matches
  [`DESIGN.md`](../../DESIGN.md)'s transition diagram; only what
  "credential" means changed from ADR-0001.
- The live MCP server drives validation automatically: `auto_connect`
  entering `Bootstrapping` spawns one background `Accept` attempt per
  connect, mirroring what the `bridge-smoke` CLI harness already did by
  hand. Outcome B's version had this driven by an inbound push (the
  extension delivered a credential straight into a connection handler); the
  poll-based inversion required an explicit driver where none existed
  before — this was found and fixed by review before the live gate, not by
  the live gate itself.
- Failure codes distinguish `auth_invalid` (→ `ReauthRequired`),
  `rate_limited` (→ `RateLimitError`), `content_script_unreachable` and a
  transport-level `canceled` (→ `BridgeUnavailableError`, e.g. the bound tab
  closed or navigated away mid-operation), and everything else collapses to
  a generic upstream-change classification.
- Mutations are still never retried without an operation-specific
  idempotency or reconciliation rule; this card added no mutating
  operations.

## Live evidence (sanitized)

- `session_identity`: 5887 bytes of real JSON, twice, both times parsed to a
  valid `SessionIdentity`. Response shape recorded structurally (object keys,
  array lengths, scalar types) — never values — in the card #13 journal.
- `products_search`: 15518 bytes of real JSON for a live search term against
  a live market ID, structurally well-formed (products, facets, pagination).
- No credential, cookie, token, account data, or raw response content was
  logged, printed, or committed at any point.
- Endpoint shapes were cross-checked against `Tobi4s1337/karrt` (this
  project's own stated inspiration), which independently reverse-engineered
  the same REWE API and confirmed the same base URL
  (`https://www.rewe.de/shop/api`).

## Considered options

- **Broadening the exported cookie set** (e.g. adding Cloudflare's `__cf_bm`
  alongside `rstp`) was not attempted: Cloudflare bot management ties its
  cookies to TLS/JS fingerprinting, so a Go client would likely still fail
  even with more cookies. This was the reasoning that motivated outcome C
  over a smaller patch to outcome B.
- **A fully duplexed, multiplexed Unix-socket protocol** (server pushes
  operations down an always-open connection) was rejected in favor of the
  poll model to keep the server's existing hardened per-connection handling
  unchanged, at the cost of poll latency (bounded by a ~25s poll timeout,
  not felt as user-visible latency since operations resolve on the next poll
  cycle in practice).
- **General browser automation** (e.g. CDP against the user's normal
  profile, a persistent automation daemon) remains rejected for the same
  reasons as ADR-0001: it broadens browser access and couples the server to
  browser internals beyond a narrowly scoped extension and Native Messaging.

## Consequences

Chrome and one signed-in REWE tab become required runtime dependencies while
grocery-mcp is in use; closing either pauses MCP work until reconnected.
Reconnect requires one extension click but normally no new REWE login, since
the browser — not grocery-mcp — owns the actual authenticated session. This
trades the token-export UX of outcome B (repeated `rstp` refresh clicks) for
the requirement that a REWE tab stay open (in the background is fine).

This decision re-scopes cards #4 (BrowserBridge/auth bootstrap — largely
already rebuilt against this ADR by card #13) and #6–#10 (session state
machine and Phase 1 tool vertical slices), all of which assumed outcome B's
credential-export model. Card #13 is Done. `basket_get` remains allowlisted
but unwired pending card #8, which needs it anyway for basket context
binding (a `basketId` is only obtainable after adding an item — not a
standalone read).
