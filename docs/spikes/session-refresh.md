# Session-refresh spike

Status: complete on 2026-08-18

Decision: **C — stop and redesign the direct HTTP-session architecture**

## Question

Can the short-lived REWE session, especially the `rstp` cookie, be renewed by a
Go HTTP client after one manual browser login?

## Safety rules

- Never record cookie values, credentials, addresses, order data, or payment data.
- Do not commit HAR files or unsanitized traffic captures.
- Record cookie names, domains, flags, expiry deltas, request shapes, and sanitized response metadata only.
- Use a test basket with no checkout and perform only reversible operations.

## Procedure

1. A human completed the normal REWE login in an isolated browser profile.
2. The login used REWE's OpenID Connect authorization-code flow with PKCE at
   `account.rewe.de` and returned to `www.rewe.de`.
3. The authenticated shop was loaded immediately and reloaded four times after
   the reported `rstp` lifetime. Every read-only load showed an authenticated
   account without another credential entry.
4. Public REWE response headers and the attributed upstream implementation were
   inspected for cookie names, domains, expiry behavior, and refresh handling.
5. No cookie values, credentials, account data, HAR files, or response payloads
   were captured or stored.

## Sanitized evidence

- The browser remembered the human's REWE identity before the shop session was
  established; the user only had to approve continuing with that account.
- The browser remained authenticated after the approximately ten-minute
  `rstp` lifetime reported by the upstream implementation.
- A public unauthenticated request to `https://www.rewe.de/` set only the
  Cloudflare `__cf_bm` cookie for `rewe.de`; it did not expose an HTTP-only
  refresh exchange for an authenticated API session.
- At upstream commit `f5a7b80e3e9883fa47bad78bd62be1765bb905c1`, the login
  implementation deliberately navigates to `www.rewe.de` to obtain `rstp`,
  exports browser cookies whose domains contain `rewe`, and requires another
  browser login after a 401 or 403. It has no independent HTTP refresh flow.
- The upstream README reports that `rstp` expires in roughly ten minutes and
  identifies the lack of automatic refresh as a limitation.
- An independent INNOQ analysis found that `rstp` was the only REWE shop cookie
  required to authenticate direct API requests:
  <https://www.innoq.com/en/blog/2022/06/marktanalyse/>

No candidate HTTP-only refresh exchange was identified: the current upstream
client merely preserves `Set-Cookie` updates and fails on 401/403, while the
observed renewal path re-enters the REWE shop through the authenticated browser.
Copying live credential values into an ad-hoc experiment would violate this
spike's handling rules, so v1 rejects outcome A instead of claiming an unverified
HTTP refresh. Outcome B keeps the only observed refresh path—the browser—behind
a narrow bridge and requires a read-only validation before using its result.
The provisional decision remained gated until card #4 could prove `rstp`-only
direct API authentication without exposing credential values. That live gate
failed on 2026-08-18, selecting outcome C.

## Cookie and origin policy

The bridge uses the smallest supported static cookie-name allowlist:

| Name | Allowed domain | Purpose |
| --- | --- | --- |
| `rstp` | `rewe.de` or a subdomain | Short-lived REWE shop/API session |

The extension transfers only `rstp`, and only when all of these checks pass:

- the cookie name is exactly `rstp`;
- the cookie domain is exactly `rewe.de` or ends in `.rewe.de`;
- the cookie is `Secure`, unexpired, and applicable to `https://www.rewe.de/`;
- the native host requested the bundle for an explicit bootstrap or refresh;
- values travel only through Native Messaging and are never logged or persisted.

`account.rewe.de` remains part of the browser login flow, but identity-provider
cookies and non-authentication REWE cookies are never copied into `ReweGateway`.
This minimized session exposure and let the browser own OpenID Connect and
anti-bot state. The live proof established that `rstp` alone was insufficient.
Validation failed closed and the allowlist was not expanded.

## Rejected provisional refresh flow

```text
ReweGateway             BrowserBridge              REWE browser session
     |  401/403 on read       |                              |
     |----------------------->| refresh requested            |
     |                        |----------------------------->| load/reload /shop/
     |                        |<-----------------------------| authenticated page
     |                        | collect domain-allowlisted    |
     |<-----------------------| session bundle               |
     | verify live API + same bound operation context        |
     | retry original read once                              |
```

- V1 refresh is reactive to a 401/403 on a read. Proactive refresh remains
  disabled until current expiry metadata can be observed safely and reliably.
- Refresh is single-flight so concurrent MCP calls cannot trigger multiple
  browser navigations.
- A refreshed session must resolve to the same account and logical session
  identity as the previous active session. The operation's selected store,
  basket, and timeslot bindings are revalidated as applicable. A mismatch fails
  closed and invalidates that context and all of its dependents. Rotating `rstp`
  changes only the credential revision.
- Read-only operations may retry once after successful validation.
- Mutations are never retried blindly; they require idempotency or reconciliation.
- If the browser or bridge is unavailable, the state becomes
  `BridgeUnavailable` and the human only needs to start or reconnect Chrome. If
  the remembered Browser Identity is missing, expired, or rejected, the state
  becomes `ReauthRequired` and the human must log in again.

## Reproducibility notes

The observed browser bootstrap was repeated by opening a fresh shop tab in the
same isolated profile. It established authenticated state without credential
entry. After the reported lifetime, four consecutive read-only shop reloads
remained authenticated. On 2026-08-18 the extension delivered a live,
browser-sourced, memory-only `rstp` through the exact-origin native host and
user-only socket. The initial read-only direct API request rejected it as
unauthenticated. No Shop Session became `Active`, no later rotation proof was
attempted, and no secret or response body was recorded. This selects outcome C
and stops the direct-client implementation for replanning rather than broadening
the cookie allowlist.

## Exit outcomes

### A — HTTP refresh

The refresh exchange is reproducible without browser-only state. Chrome is used
only for login bootstrap; `ReweGateway` maintains the session.

### B — Browser-assisted refresh

Refresh depends on browser state but is reliable while Chrome is available. The
extension synchronizes updated allowlisted cookies through Native Messaging.

### C — Stop or redesign

Refresh depends unreliably on anti-bot behavior, browser fingerprinting, or an
unacceptable persistent browser. Stop the HTTP-client architecture and replan.

## Deliverables

- Sanitized request/response sequence diagram: above.
- Required cookie-name and domain allowlist: above.
- Session lifetime and refresh trigger observations: above.
- Selected outcome with reproducibility notes: C; provisional B failed card #4's
  live acceptance gate.
- Architecture and board dependencies: updated by this card.
