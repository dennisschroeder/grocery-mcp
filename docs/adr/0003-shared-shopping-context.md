---
status: accepted
date: 2026-08-31
---

# Share shopping context through the elected local owner

ADR-0002 made browser execution shareable across multiple `serve` processes
but deliberately kept their shopping contexts isolated. In practice that
breaks the user workflow: Desktop, Cowork, Code, and ChatGPT can address the
same browser session while disagreeing about store, basket, and timeslot.

The elected owner now exposes a second, narrow capability beside browser
transport: an account-scoped shopping-context repository. It stores only
store ID, postal code, basket ID, and timeslot ID. Account IDs are hashed for
storage keys. Browser bindings, logical shop-session IDs, cookies, tokens,
basket contents, addresses, and prices remain process-memory-only.

Context mutations acquire an account-scoped advisory file lock before they
load state, call REWE, reconcile the result, and publish the new binding.
The OS releases the lock if a process dies. This serializes invalidating
transitions across processes, so a stale writer cannot restore a basket or
timeslot after another process changes the store. State replacement is
atomic and lives in the same user-private runtime directory as the owner
socket; followers use the versioned peer protocol to load and store it.

The extension's Native Messaging port remains independent of owner lifetime.
If an owner disappears, the native host keeps the Chrome port open, discards
an uncorrelatable late result, and polls the replacement owner. The interrupted
mutation remains ambiguous and is never replayed blindly.

This ADR supersedes ADR-0002 only where it says followers keep domain context
isolated. Browser operations remain fixed, typed, and allowlisted exactly as
ADR-0002 requires.
