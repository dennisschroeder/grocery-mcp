# BrowserBridge setup and proof

The bridge deliberately includes one human gesture. Chrome does not allow a
local MCP process to install an unpacked extension, run a content script, or
trigger Native Messaging by itself. That boundary prevents the model or MCP
server from silently acquiring a browser session.

REWE sits behind Cloudflare bot management, which fingerprints TLS/JS
execution. A Go `net/http` client cannot reproduce that even holding a valid
session cookie — this was tried and failed live (ADR-0001, rejected). So
REWE requests execute *inside* the signed-in browser tab itself, through a
fixed, allowlisted set of typed operations run by an isolated content
script. Only structured JSON results — never cookies, tokens, or any
credential material — cross back to the MCP server. See
[`ADR-0002`](adr/0002-browser-executed-rewe-transport.md) for the accepted
design.

## Human-in-the-loop flow

```text
MCP client         Human          Chrome extension      native host        grocery-mcp        REWE tab
    |                 |                    |                   |                 |                |
    | auth_connect    |                    |                   |                 |                |
    |---------------------------------------------------------------------->| Bootstrapping     |
    | action_required |                    |                   |                 |                |
    |<----------------------------------------------------------------------|                    |
    |                 | click extension    |                   |                 |                |
    |                 |------------------->| connectNative      |                 |                |
    |                 |                    |------------------>| poll            |                |
    |                 |                    |                   |-------------->| queue session_identity
    |                 |                    |                   |<--------------| operation, req_id
    |                 |                    |<-- operation_req --|                 |                |
    |                 |                    | tabs.sendMessage --------------------------------->|
    |                 |                    |                   |                 |     fetch()    |
    |                 |                    |<---------------------------------------- result -----|
    |                 |                    |-- operation_resp ->|                 |                |
    |                 |                    |                   |-- result ------>| Active         |
    | auth_status     |                    |                   |                 |                |
    |---------------------------------------------------------------------->|                    |
    | Active          |                    |                   |                 |                |
    |<----------------------------------------------------------------------|                    |
```

The human acts only when `action_required` is true:

- once to install the unpacked extension and approve its REWE-only host
  permission (no cookie permission is ever requested);
- once to establish the Chrome Native Messaging port after Chrome or the
  extension starts;
- after `ReauthRequired`, to log into REWE normally. Another extension click
  is needed only if Chrome's native port is no longer connected.

There is no password, 2FA, CAPTCHA, checkout, or payment entry in the
extension. MCP-server restarts lose their local auth object, but the native
host keeps polling until Chrome closes its port. A new process can therefore
validate the same browser session automatically without another click.

## Multiple MCP processes

Claude Desktop, Cowork, and Code may each start their own `grocery-mcp serve`
process. They coexist behind one user-scoped browser transport:

```text
Desktop serve ─┐
Cowork serve  ─┼─ local call/cancel IPC ─> elected bridge owner ─> native host ─> extension
Code serve    ─┘                              (one of the serve processes)
```

An advisory lock at `bridge.lock` elects exactly one process to own
`bridge.sock`; only that owner may remove a stale socket or bind a new one.
Followers forward typed `call`/`cancel` messages to the owner, which places
them in the same globally correlated, serial browser queue as its own calls.
The Chrome extension and native-host poll protocol are unchanged.
The user-scoped runtime directory is `/tmp/grocery-mcp-<uid>` and deliberately
does not depend on `TMPDIR` or `XDG_RUNTIME_DIR`, which may differ between MCP
hosts and Chrome Native Messaging.

Every `serve` process owns its own Auth service and short-lived session ID.
Store, postal code, basket ID, and timeslot ID are account-scoped shared
context: followers load and update them through the owner. The owner stores
only those non-auth identifiers in `state.json` inside the user-private bridge
runtime directory (`0700` directory, `0600` file), so a new leader can resume
the same basket. Cookies, tokens, and `ShopSessionID` remain memory-only.

Closing a follower has no effect on the owner or other clients. After the
owner exits, the next call that cannot connect before sending any bytes may
acquire the lock and become owner. A call that was already sent when the
owner disappeared is never retried automatically because its browser-side
outcome may be ambiguous. The native host waits for the replacement owner
without a retry deadline and exits only when Chrome closes the port or the
host is shut down.

## Local installation

Requirements: Google Chrome, an existing human-controlled REWE login in the
same Chrome profile, and either Go 1.26 (to build) or a downloaded release
binary. Either way, a checkout of this repository is required regardless —
the Chrome extension is loaded from source (`extension/`), never from a
release artifact.

1. Get the binary, either by building it:

   ```sh
   mkdir -p bin
   go build -o ./bin/grocery-mcp ./cmd/grocery-mcp
   ```

   or by downloading the archive matching your OS/architecture from
   [Releases](https://github.com/dennisschroeder/grocery-mcp/releases) and
   extracting `grocery-mcp` into `./bin/`.

2. Open `chrome://extensions`, enable Developer mode, choose **Load unpacked**,
   and select this repository's `extension` directory.
3. Copy the 32-character extension ID from Chrome.
4. Register the native host for that exact origin:

   ```sh
   ./bin/grocery-mcp install-native-host --extension-id EXTENSION_ID
   ```

The installer writes only Chrome's native-host manifest. It stores the binary
path and exact extension origin, but no credential. The extension requests
`nativeMessaging` plus HTTPS host access limited to REWE, and runs a content
script scoped to `https://www.rewe.de/*`. It has no cookie-reading
permission, storage permission, password access, or general browsing
permission.

After editing any extension file, reload the unpacked extension in
`chrome://extensions` before testing — Chrome does not pick up changes
automatically.

## Live acceptance gate

```sh
./bin/grocery-mcp bridge-smoke --timeout 5m
```

When prompted, click the extension. This proves `session_identity`
end to end (real `GET /shop/api/favorites`, executed inside the tab) and
reports its response shape structurally (object keys, array lengths, scalar
types — never values). It also runs the full auth state machine and reports
the state Accept() reached (rather than failing the whole run on a rejected
state — a logged-out session correctly landing on `ReauthRequired` is a
pass, not an error).

To also prove any other operation, pass it via `-operations` as a JSON array
(one Chrome click still covers every operation queued this way):

```sh
./bin/grocery-mcp bridge-smoke -operations \
  '[{"operation":"products_search","params":{"term":"milch","market_id":"123456"}}]'
```

The CLI prints no account data, credential material, or response content —
only operation success/failure, byte counts, and structural shape (with one
narrow exception: `stores_search`'s `market_id`/`postal_code` values, since
those are public store identifiers, not account data).

## Acceptance record

Status: **outcome C proven live — accepted 2026-08-18; see
[`known-limitations.md`](known-limitations.md) for the current, full picture
after card #10's 2026-08-19 live-testing rounds** (restart/reconnect
resilience, tab close/reopen, logged-out fail-closed behavior, and which
operations are actually confirmed working against real REWE traffic today).

- `session_identity` (`GET /shop/api/favorites`) succeeded twice against a
  real, signed-in account: 5887 bytes of real JSON both times, correctly
  parsed into a `SessionIdentity`. The full auth state machine reached
  `Active`.
- `products_search` (`GET /shop/api/products`) succeeded once against a live
  search term and market ID: 15518 bytes of well-formed search results
  (products, facets, pagination).
- No credential values, account data, response bodies, or browser captures
  were logged or persisted at any point.
