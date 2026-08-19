# Card #13 — Outcome C spike: prove browser-executed REWE transport

Closed out 2026-08-18. Accepted — see
[`ADR-0002`](../adr/0002-browser-executed-rewe-transport.md).

## Execution graph (as run)

```
1. [agent] Audit dirty worktree, classify reuse/replace          -> Done
2. [command] Checkpoint: commit staged outcome-B work as-is      -> Done (4 commits, split feat/docs/chore)
3. [agent] Design typed operation protocol + TabBinding          -> Done (contract.md, frozen)
4. [agent] Rewrite extension (content script + service worker)   -\ parallel, worktree-isolated
5. [agent] Rewrite Go side (protocol/server/host/validator/auth) -/ against the frozen contract
6. [command] Verify: gofmt/vet/build/test/race                   -> clean on both nodes independently
   [fan-in]  Merge both branches, reconcile fan-out gap           -> 1 real bug found (failure-code
                                                                      vocabulary mismatch) and fixed
7. [command+agent] Review: /code-review (Standards+Spec, parallel) + gpt-5.6-sol independent pass
                                                                   -> Standards+Spec both landed with
                                                                      real findings, fixed. gpt-5.6-sol
                                                                      never completed (route failure,
                                                                      5 attempts, 2 sessions) — recorded,
                                                                      not treated as clean
8. [human gate] Live proof with Dennis                            -> full success after 3 more real bugs
                                                                      found and fixed during the gate
9. [agent] Write ADR-0002, update docs, re-scope #4/#6-#10        -> Done
10. [command] Move #13 to Done (local commit, no remote)          -> Done
```

Back-edge taken: node 6's fan-in found one bug (budget 3, used 1) — no
escalation needed. Node 8 (the live gate itself) surfaced three more issues
not caught by any prior verification, each fixed and re-tested in place
rather than escalated, since each had a clear, verifiable root cause.

## What the live gate actually found, in order

1. **Harness lifecycle bug**: `bridge-smoke` (one-shot CLI) closed its socket
   right after one `Accept()` call; the native host's poll loop — designed to
   run for the whole real-server lifetime — hit the dead socket on its next
   poll and crashed. Chrome surfaced this as "Native host has exited,"
   initially masking the real signal. Diagnosed, not fixed (harness-only,
   cosmetic once understood) — the real result printed before the crash.
2. **Missing driver, found before the gate but worth restating here**:
   `serveMCP()` never called `service.Accept()` anywhere — outcome B's
   push-triggered callback had no outcome-C replacement. Fixed pre-gate
   (`autoaccept.go`), which is why the gate could even reach a real result
   instead of hanging forever.
3. **Real parsing bug**: `decodeFavoriteListID` assumed a
   `{favoriteLists:{favorites:[...]}}` envelope that no prior attempt had
   ever gotten far enough to check against a real response. REWE's actual
   `/favorites` response is a top-level array. Found via a structural (never
   content) JSON-shape diagnostic added specifically for this debugging
   session, fixed, re-verified live.
4. **Endpoint discovery**: `products_search`'s real endpoint was unknown
   going into the gate (deliberately stubbed per the frozen contract).
   Found via Dennis's Chrome DevTools capture, cross-checked against
   `Tobi4s1337/karrt`'s independent reverse-engineering of the same API.
   `basket_get`'s endpoint was also researched this way but turned out to
   need a `basketId` dependency out of scope for a spike — deferred to card
   #8 rather than forced in.

## Final node statuses

All nodes reached a terminal state. No node was abandoned or left
inconsistent. `basket_get` and the tab-close/reopen + logged-out live tests
are explicit, recorded deferrals (cards #8 and #10 respectively) — not
silent gaps.

## Feedback: where the design and reality diverged

- **The frozen contract pinned wire *shape* but not failure-code
  *vocabulary*.** Both fan-out nodes independently guessed different string
  values for the same failure concepts (`unauthorized` vs `auth_invalid`,
  etc.), which the fan-in step had to reconcile. Next contract like this one
  should pin the literal string constants for cross-language shared
  vocabularies, not just the JSON field names and types.
- **A one-shot CLI harness modeling a long-lived server's transport is a
  real trap.** `bridge-smoke`'s socket-close-on-exit behavior produced a
  confusing "native host crashed" signal that had nothing to do with the
  actual operation result. Worth a design note for any future one-shot
  proof-of-life tool built against a server assumed to run indefinitely.
- **Assumptions written before any real data exists are exactly as reliable
  as that implies.** `decodeFavoriteListID`'s shape was speculative from the
  start (documented as such in the original card #5 work), and it was wrong
  on the first real check. Nothing to fix here — the sanitization discipline
  (never log/commit real payloads) is exactly why this had to wait for a
  live, human-supervised session to catch, and it did.
- **gpt-5.6-sol as a mandatory second-opinion pass needs a documented
  failure mode.** Five attempts across two sessions (quota, timeout, then
  what looked like an infrastructure error) never got a response. The card
  skill already anticipates this ("a route failure gets written on the
  card, never silently treated as clean") — this card is the first real
  instance of that clause actually firing, worth watching whether it
  recurs on future cards.
