# Implementation plan

## Execution graph

```text
Project scaffold
      |
Session-refresh spike
      |
Architecture decision A / B / C
      |
      +--> Browser bridge + auth bootstrap
      |
      +--> ReweGateway contracts + fixture harness
                    |
             Session state machine
                    |
           Phase 1 ShoppingCore tools
                    |
       End-to-end verification and packaging
                    |
             Checkout protocol spike
                    |
       CheckoutGate + local approval flow
```

Spike 1's provisional outcome B failed card #4's live `rstp`-only API gate on
2026-08-18. Outcome C (browser-executed REWE requests, proven live by card
#13 on 2026-08-18 — see ADR-0002) is selected and accepted. Cards #4 and
#6–#10 are re-scoped to build on it.

Phase 1 must fail packaging verification if any order-placement, cancellation,
checkout, payment, or approval operation is registered or otherwise reachable.

## Ordered work

1. Complete the session-refresh spike and record the evidence without secrets. **Done: outcome C.**
2. Replace rejected ADR-0001 with a reviewed outcome-C architecture. **Done: ADR-0002, card #13.**
3. Define stable domain types, error classes, and the `ReweGateway` interface. **Done: card #5.**
4. Build the sanitized fixture and contract-test harness before adding endpoints. **Done: card #5.**
5. Implement Native Messaging and the browser-executed transport (typed
   operations, poll-based relay). **Done: card #13** — `session_identity` and
   `products_search` proven live; `basket_get` allowlisted, wiring deferred
   to card #8 (needs basket context binding, not a standalone read).
6. Implement the session state machine and refresh strategy selected by the
   spike. **Re-scoped for outcome C — see card #6.**
7. Implement Phase 1 tools in vertical slices: stores/search, basket, timeslots,
   then read-only orders and receipts. **Re-scoped for outcome C — see cards #7–#9.**
8. Verify installation, restart behavior, redaction, concurrent calls, and upstream failures.
9. Reverse-engineer checkout and payment/challenge behavior separately.
10. Implement Phase 2 only if checkout can remain out-of-band, human-approved,
    fail-closed, and reconcilable.

## Implementation entry criteria

Implementation beyond the spike may begin when:

- Spike outcome A or B is documented with sanitized evidence.
- Required cookie names and domains are allowlisted without storing values.
- The selected browser/session lifecycle is explicit.
- The first vertical slice has typed inputs, outputs, errors, and contract fixtures.
- No unresolved decision can change the process topology or authentication seam.
