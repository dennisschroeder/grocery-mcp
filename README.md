# grocery-mcp

Local MCP server for grocery shopping. The REWE-first implementation is built
in Go and draws on ideas from
[Tobi4s1337/karrt](https://github.com/Tobi4s1337/karrt).

## What it does

- Read/write access to REWE Pickup: store search, product search, basket,
  timeslots, and order history.
- `order_prepare`/`order_status` create a human-approved checkout snapshot on
  a local page. The actual REWE commit is deliberately stubbed and always
  fails closed — no tool on this server can place an order. See
  [`DESIGN.md`](DESIGN.md#checkout).
- REWE requests execute inside your own signed-in Chrome tab through a
  narrowly scoped extension, never as direct API calls with stored
  credentials — see
  [`ADR-0002`](docs/adr/0002-browser-executed-rewe-transport.md).

See [`docs/known-limitations.md`](docs/known-limitations.md) for exactly
which operations are live-tested today.

## Install

### Homebrew (macOS/Linux)

```sh
brew tap dennisschroeder/grocery-mcp
brew install grocery-mcp
```

### Build from source

Requires Go 1.26.

```sh
git clone https://github.com/dennisschroeder/grocery-mcp.git
cd grocery-mcp
go build -o ./bin/grocery-mcp ./cmd/grocery-mcp
```

Or download a prebuilt binary for your OS/architecture from
[Releases](https://github.com/dennisschroeder/grocery-mcp/releases).

### Chrome extension (one-time setup)

1. Open `chrome://extensions`, enable Developer mode, choose **Load
   unpacked**, and select the extension directory: `$(brew
   --prefix)/share/grocery-mcp/extension` for a Homebrew install, or this
   checkout's `extension/` directory for a source build.
2. Copy the 32-character extension ID Chrome assigns it.
3. Register the native host for that exact ID:

   ```sh
   grocery-mcp install-native-host --extension-id EXTENSION_ID
   ```

Full detail on the human-in-the-loop flow, plus a live acceptance gate
(`grocery-mcp bridge-smoke`) that proves the bridge end to end, is in
[`docs/browser-bridge.md`](docs/browser-bridge.md).

### Connect an MCP client

Point your MCP client at `grocery-mcp serve` (no arguments also serves).
Example for a client using a `command`/`args` config shape:

```json
{
  "mcpServers": {
    "grocery-mcp": {
      "command": "grocery-mcp",
      "args": ["serve"]
    }
  }
}
```

Check `auth_status`. Each MCP process validates automatically through an
already-connected extension. Call `auth_connect` only to request an explicit
retry; click the extension only when `action_required` is true.

## Documents

- [`DESIGN.md`](DESIGN.md): architecture and safety invariants
- [`CONTEXT.md`](CONTEXT.md): canonical project language
- [`docs/IMPLEMENTATION_PLAN.md`](docs/IMPLEMENTATION_PLAN.md): dependency-ordered delivery plan
- [`docs/adr/0001-browser-assisted-memory-only-sessions.md`](docs/adr/0001-browser-assisted-memory-only-sessions.md): rejected — direct-rstp-export decision
- [`docs/adr/0002-browser-executed-rewe-transport.md`](docs/adr/0002-browser-executed-rewe-transport.md): accepted — browser-executed transport decision
- [`docs/contracts/rewe-gateway.md`](docs/contracts/rewe-gateway.md): stable gateway and fixture rules
- [`docs/browser-bridge.md`](docs/browser-bridge.md): Chrome setup, human action, and live-proof procedure
- [`docs/known-limitations.md`](docs/known-limitations.md): current live-testing status, known gaps, reauthentication UX
- [`docs/spikes/session-refresh.md`](docs/spikes/session-refresh.md): first blocking investigation
- [`docs/spikes/checkout.md`](docs/spikes/checkout.md): checkout sequence reverse-engineering

## Scope constraints

- Unofficial, undocumented REWE interface; not affiliated with, endorsed by,
  or supported by REWE.
- macOS and Linux first; Windows later.
- No automated credential entry, CAPTCHA solving, or stored 2FA secrets.
- No cookies, tokens, addresses, receipts, HAR files, or private fixtures in Git.

## Disclaimer

This project talks to REWE's private, undocumented API. That very likely
falls outside REWE's terms of service for their official channels — using it
is at your own risk, including possible account action by REWE. This
repository does not encourage or provide any way to bypass CAPTCHAs,
automate account creation, or place orders (Phase 2's checkout flow requires
a human approval for every single order, by design). No warranty is provided;
see [`LICENSE`](LICENSE).

## License

[MIT](LICENSE)
