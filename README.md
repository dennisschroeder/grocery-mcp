# grocery-mcp

Local MCP server for grocery shopping. The REWE-first implementation is built
in Go and draws on ideas from
[Tobi4s1337/karrt](https://github.com/Tobi4s1337/karrt).

## Status

Outcome C (browser-executed REWE transport) is accepted — see
[`ADR-0002`](docs/adr/0002-browser-executed-rewe-transport.md). All Phase 1
verticals (stores/products, basket/timeslots, orders) are implemented.
`session_identity`, `orders_list`, `products_search`, `timeslots_list`,
`stores_search`, `timeslot_select`, and `basket_apply`'s add path are all
live-proven against a real, signed-in REWE account; restart/reconnect, tab
close/reopen, and logged-out fail-closed behavior are also live-proven.
`basket_get` and `basket_apply`'s update/remove paths are wired but not yet
live-tested. See [`docs/known-limitations.md`](docs/known-limitations.md)
for the full, current picture.

Phase 2 (checkout) is underway: `order_prepare`/`order_status` and a local,
human-approved `CheckoutGate` are implemented — see
[`docs/spikes/checkout.md`](docs/spikes/checkout.md) for the underlying
reverse-engineering. The actual commit to REWE is deliberately stubbed and
always fails closed pending its own live-verification pass; no tool on this
server can place an order.

## Delivery phases

1. Phase 1: browser-assisted authentication, session maintenance, stores,
   product search, basket, timeslots, and read-only order access.
2. Phase 2: checkout with an out-of-band human approval for every order.

No tool on this server, in either phase, can place an order — Phase 2's
commit path is a deliberate stub pending its own live-verification pass.

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
