# Cards #7/#8/#9 — Phase 1 vertical slices (stores, basket, orders)

Closed out 2026-08-19. All three Done.

## Execution graph (as run)

```
1. [command] Freeze shared scaffolding (Shopping Context narrow gateway
   interfaces, Core's boundContext/rebindContext, Gateway struct, seven new
   Operation constants, content-script.js stub cases)             -> Done
2. [agent] Node "stores" (card #7)   -\ parallel, worktree-isolated,
3. [agent] Node "basket" (card #8)   -+ against the frozen contract
4. [agent] Node "orders" (card #9)   -/
5. [fan-in merge] Sequential git merge, three branches into
   feat/browser-bridge-auth                                        -> Done
   - content-script.js conflicted twice (nodes 2/3/4 all added
     functions to the same switch/file region) -> resolved by hand,
     pure concatenation, no logic changes
   - Duplicate Go symbols across isolated worktrees: two
     stubAuthenticator (shopping), two Money/moneyOutput (mcpserver)
     -> consolidated into shared files
6. [command] Verify: gofmt/vet/build/test/race, node --check         -> clean
7. [fan-in integration] Wire rewe.Gateway into main.go's NewCore,
   register all three tool sets in server.go; update the two
   pre-existing auth-only tests for the full 14-tool surface        -> Done
8. [review] /code-review (Standards + Spec, parallel) + gpt-5.6-sol   -> see below
9. [fix] Apply confirmed findings, re-verify, re-test                -> Done
10. [command] Move #7/#8/#9 to Done (local commit, no remote)        -> Done
```

## What review actually found

- **Standards**: no hard violations. Real, worth-fixing finding: three
  near-identical bridge-error classifiers (`mapBridgeError`,
  `classifyReadBridgeError`/`classifyMutationBridgeError`,
  `mapTransportError`) independently written by the three isolated nodes —
  each file's own comment already acknowledged the duplication as
  deliberate. `gateway_stores.go`'s variant was missing the read-vs-
  mutation "canceled" distinction basket's version correctly has (latent,
  not yet exploitable since stores has no mutations). Consolidated into
  `internal/rewe/bridge_errors.go`.
- **Spec**: one serious real bug. `Core.ApplyBasket` never called
  `rebindContext` after a successful mutation — the only Core method with a
  ShoppingContext-mutating gateway call that didn't (SelectStore,
  SelectTimeSlot both did). Since REWE only assigns a basket ID on the
  first successful add, and nothing captured it, `basket_get` would fail
  closed with `ValidationMissing` forever, even immediately after a
  successful add. Compounding: `ApplyBasket`'s gateway method returned a
  zero-value "authoritative updated basket" for delete-only mutations,
  since REWE's DELETE returns 204 with no body to decode a snapshot from.
  Both fixed. A third flag — the `?includeTimeslot=true` DELETE param
  Spec called unexplained scope creep — turned out to be correct
  (re-confirmed against `Tobi4s1337/karrt`'s actual source directly, not
  from memory); the real gap next to it was a missing `x-origin:
  BASKET_OVERVIEW` header, which got fixed alongside.
- **gpt-5.6-sol**: confirmed down at the infrastructure level this round —
  even a literal "reply with OK," no diff attached, failed with the same
  `codex call failed: exit status 1`. Not a payload-size issue (ruled out
  by the diagnostic), not fixable by retrying. Recorded as a route failure
  per the card skill, not treated as clean. The dispatching agent did an
  independent Claude-side pass in its place and surfaced one real,
  narrower design question (see below) but no new hard bugs beyond what
  Standards/Spec had already caught.

## Known, deliberately unresolved risks (documented on the board, not silent)

- **`Product.ID` / REWE `listingId` equivalence** (flagged by card #8's own
  implementing agent): `basket_apply` sends `Product.ID` to REWE as its
  `listingId`. `Product.ID` is populated by card #7's decoder from a flat
  `"id"` field; `karrt`'s own types distinguish `ProductId` from
  `ListingId`, and even card #7's `stores_search` fallback code hedges
  between `product.listing.id` and `product.id`. If wrong, every
  `basket_apply` fails closed (rejected outcomes), not silent corruption —
  but settling it needs real REWE traffic, which this wave deliberately
  did not attempt.
- **Partial-outcome loss on mid-batch auth failure**: a multi-item
  `basket_apply` that hits `auth_invalid`/`rate_limited` on item N>1
  correctly stops the loop but discards the outcomes already collected for
  items 1..N-1. Fixing it properly means deciding whether the MCP tool
  layer should forward a partial result alongside a non-nil error — an
  SDK-boundary question left open rather than guessed at.
- **`SelectTimeSlot` stubbed**: REWE's `POST /timeslot-reservations` needs
  a `customerId` with no known source — even `karrt`'s reference client
  ships this same gap (sends `""`  with a "will be populated from session"
  comment it never resolves). Fails closed with a typed `ValidationError`
  rather than guessing.
- **`stores_search` is a heuristic, not a real endpoint**: REWE has no
  dedicated store-search API; reuses `karrt`'s ZIP-scoped-product-search
  regex hack. Documented in the tool description, not presented as
  authoritative. Superseded on 2026-08-19 by a real, discovered store
  locator endpoint — see
  [`docs/known-limitations.md`](../known-limitations.md).
- **`receipt_get` is summary-only**: explicit product decision (2026-08-19)
  — REWE has no structured per-receipt endpoint, only a PDF this project
  doesn't parse.

None of the three verticals' new REWE integrations are live-tested yet —
that was explicitly out of scope for this wave (unlike `session_identity`/
`products_search` from card #13). That's the natural next checkpoint before
any of this ships as part of Phase 1 packaging (card #10).

## Feedback: where the design and reality diverged

- **Freezing the wire-shape contract wasn't enough for a same-file fan-out.**
  Card #13's fan-out avoided same-file conflicts by giving each node its
  own extension-side file section; this fan-out had three nodes all
  legitimately needing to add cases to the same `content-script.js` switch.
  Pre-stubbing every case (done in the freeze step) kept the conflicts
  small and mechanical, but they still happened — worth planning for
  explicitly next time rather than hoping git's 3-way merge sorts it out
  silently.
- **Symbol-level collisions need the same discipline as wire-protocol
  vocabulary.** Card #13's lesson was "pin the wire vocabulary, not just
  the shape." This fan-out's version: three isolated worktrees will
  independently invent identically-named, identically-shaped helper types
  for the same generic need (a stub authenticator, a Money type) — Go
  doesn't care until they're merged into one package. Worth either naming
  these explicitly as shared-from-the-start in the frozen contract, or just
  budgeting the fan-in time to reconcile them, which is what happened here.
- **A narrow interface freeze paid off.** Splitting `ReweGateway` into
  `StoresGateway`/`BasketGateway`/`OrdersGateway` meant each node's
  worktree actually compiled and tested standalone, with no fake dependency
  on the other two verticals existing. This is the piece of this wave's
  design worth reusing as-is for any future 3-way (or more) vertical
  fan-out in this codebase.
