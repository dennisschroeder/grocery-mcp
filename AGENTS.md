# Project instructions

## Workflow

- Work one card from the `grocery-mcp` agentic-kanban board at a time.
- Do not start a dependent card until its prerequisite card is Done.
- Use the local loop: code, format, vet, test, then review.
- Keep changes surgical and commit with Conventional Commits.

## Architectural invariants

- MCP tools call `ShoppingCore`; they do not handle cookies or raw REWE payloads.
- `ReweGateway` owns decoding, session refresh, and retry policy. The HTTP
  request itself executes inside the browser tab via `BrowserBridge` and a
  content script (ADR-0002), not as a Go `http.Client` call — REWE's bot
  management rejects a non-browser TLS client even with a valid cookie.
- Browser integration uses a narrowly permissioned MV3 extension, a content
  script fixed to a typed operation allowlist, and Chrome Native Messaging.
- Do not read Chrome cookie databases or connect to the default profile through CDP.
- Store sessions in memory by default. Never log authentication material.
- Bind account, store, basket, and session context; changing one invalidates dependent state.
- Reads may retry once after a successful refresh. Mutations require proven idempotency or reconciliation.

## Checkout safety

- Phase 1 must not expose a tool that can place an order.
- Every Phase 2 order requires a fresh, out-of-band human approval bound to the exact basket, price, store, and timeslot.
- The model must never receive a token that is independently sufficient to authorize spending.
- Never retry an order commit blindly. Represent unresolved outcomes as `Pending` or `Unknown` and reconcile them.

## Sensitive data

- Never commit cookies, credentials, addresses, receipts, HAR files, packet captures, or unsanitized REWE responses.
- Recorded fixtures must be minimal, sanitized, and reviewed before entering Git.
- Monetary values use integer cents; time-sensitive results include `observed_at`.
