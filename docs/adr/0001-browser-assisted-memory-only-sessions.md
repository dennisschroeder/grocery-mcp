---
status: rejected
date: 2026-08-18
---

# Browser-assisted, memory-only sessions

The live acceptance gate rejected this provisional topology on 2026-08-18. A
browser-sourced `rstp` reached the server through the exact-origin bridge, but a
read-only direct API request rejected it as unauthenticated. No cookie scope was
broadened; a replacement outcome-C ADR is required.

Adopt provisional Spike 1 outcome B: the browser owns REWE login and anti-bot
state, while the MCP server owns a validated, memory-only Shop Session. This
keeps credentials out of files and avoids reproducing an unverified HTTP refresh
flow. Card #4 must prove the live `rstp`-only bridge before any session can become
`Active`; failure supersedes this path with outcome C rather than broadening it.

## Process topology and lifetimes

```text
Chrome + MV3 extension
  owns Browser Identity and REWE cookie storage
          |
          | Chrome Native Messaging
          v
grocery-mcp native-host mode (short-lived, stateless relay)
          |
          | user-only local socket
          v
grocery-mcp MCP-server mode (one active v1 instance per OS user)
  owns ShoppingCore, ReweGateway, Shop Session, and Shopping Context
```

- The extension service worker may suspend and reconnect. Chrome's cookie store,
  not the service worker, owns Browser Identity state.
- Chrome is human-owned and may start or stop independently. It must be running
  for bootstrap or Refresh, but an already validated in-memory Credential
  Revision can serve requests until it expires.
- Chrome starts the same `grocery-mcp` binary in native-host mode for a Native
  Messaging connection. It forwards framed messages and retains no credential
  after forwarding them.
- MCP-server mode lives for the MCP client process. It owns the exclusive local
  socket and all credential-bearing application state.
- V1 permits one active MCP-server instance per OS user so the relay cannot send
  a credential to an ambiguous recipient. A second instance fails explicitly.
- The local socket and its user-only runtime directory live only for the server
  process. The directory and socket filesystem entry persist no credential;
  socket buffers carry the exported credential only until it is consumed or the
  connection closes, and the server removes the socket entry on shutdown.

## Session and failure behavior

- The extension transfers only a live `rstp` applicable to `rewe.de` or its
  subdomains. Native Messaging restricts which extension can launch the host.
  Exported copies remain memory-only across the extension, native host, local
  socket buffers, and MCP server; none of those components logs or persists
  them. Only Chrome may persist its browser-owned authentication state.
- The native host treats credential bytes as transient message data and never
  logs or persists them.
- The MCP server stores the current Credential Revision in memory only. Explicit
  `auth_disconnect`, MCP-server restart, or MCP-server exit destroys the Shop
  Session and returns it to `Unauthenticated`. Chrome, extension-service-worker,
  native-host, and socket disconnects do not destroy an unexpired validated
  revision already held by the MCP server.
- `auth_connect` becomes `Active` only after a live read-only direct API request
  validates the Account Identity and required Shopping Context bindings.
- A 401/403 on a read triggers one single-flight browser-assisted Refresh. The
  read retries once only after live validation succeeds for the same context.
- An unreachable browser or bridge produces `BridgeUnavailable`, which asks the
  human to start or reconnect Chrome without logging in. A missing, expired, or
  rejected Browser Identity produces `ReauthRequired`; only then must the human
  log into REWE in Chrome before reconnecting.
- If the account, store, basket, timeslot, or logical session changes, the changed
  context and all dependent context are invalidated before work continues.
- Mutations are not retried without an operation-specific idempotency or
  reconciliation rule.

## Considered options

- **HTTP-only refresh** was rejected for v1 because no reproducible refresh
  exchange was identified and current evidence depends on browser-owned state.
- **Persisting exported cookies** was rejected because it increases credential
  lifetime and makes restarts silently reuse authentication material.
- **Reading Chrome's cookie database or attaching CDP to a normal profile** was
  rejected because it broadens browser access and couples the server to browser
  internals.
- **A localhost cookie-transfer endpoint** was rejected because Native Messaging
  provides a narrower extension-origin and process boundary.
- **A permanently running browser daemon** was rejected because the extension
  and short-lived native relay can reconnect while Chrome is available.

## Consequences

The user may need to reconnect after an MCP-server restart but usually does not
need to enter credentials again while Browser Identity remains valid. Refresh
requires Chrome and the extension to be available. V1 trades multi-client
concurrency for an unambiguous local credential recipient; a future broker would
require a separate ADR.
