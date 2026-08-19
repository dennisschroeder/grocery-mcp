# ReweGateway contract

`ReweGateway` is the seam between `ShoppingCore` and REWE. Its interface uses
only stable types from `internal/shopping`; raw JSON, headers, cookies, response
bodies, and generic maps stay inside the REWE implementation.

The gateway owns transport, decoding, browser-assisted refresh, context
validation, and retry policy. Every authenticated operation receives the
expected `ShoppingContext`, so the implementation can fail closed when account,
session, store, basket, or timeslot bindings change.

## Failures

- `AuthError`: the Browser Identity is missing, expired, or rejected.
- `BridgeUnavailableError`: Chrome or the local bridge cannot be reached; it does
  not imply that the human must log in again.
- `RateLimitError`: the caller may use the typed retry delay.
- `ValidationError`: a request violates a stable domain rule.
- `UpstreamChangeError`: a critical response is malformed or incompatible.
- `AmbiguousResultError`: a mutation may have succeeded and needs reconciliation.

Errors expose safe operation names and classifications, never upstream response
bodies or authentication material.

## Contract fixtures

Fixtures are JSON, minimal, and deny-by-default. A fixture schema replaces every
reviewed scalar with an explicit synthetic value and drops every unlisted field;
no upstream scalar value can reach the output. Known credential and personal-data
field names are hard-denied in both the schema and the complete input, including
unlisted branches. Only the sanitizer's output may be written under `testdata`;
raw responses remain outside the repository.

Critical decoders tolerate additive fields but classify malformed JSON, type
changes (including `null`), missing required fields, and trailing values
distinctly within `UpstreamChangeError`.
Basket mutations return both the authoritative updated basket and a typed outcome
for every requested item change, including adjustments and rejections.
