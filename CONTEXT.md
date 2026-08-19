# Grocery Shopping

This context describes a human-directed grocery-shopping session whose browser
identity is reused by a local agent without transferring login credentials.

## Language

**Account Identity**:
The stable REWE account to which a shopping session belongs.
_Avoid_: User, customer, browser account

**Browser Identity**:
The authenticated REWE state retained by the human's browser and capable of
establishing a shop session.
_Avoid_: Stored login, MCP credentials

**Shop Session**:
One logical authenticated relationship with an Account Identity, spanning
short-lived credential rotations.
_Avoid_: Cookie, token

**Session Revision**:
One tab-binding generation within a Shop Session, proven by a live
browser-executed operation rather than a rotating secret (see ADR-0002).
_Avoid_: Credential Revision, Shop Session, login

**Shopping Context**:
The Account Identity, selected store, active basket, and selected timeslot that
are valid together for one operation.
_Avoid_: Global state, current settings

**Refresh**:
Replacement of a Credential Revision from the existing Browser Identity without
changing the Shop Session or Shopping Context.
_Avoid_: Login, reauthentication

**Reauthentication**:
Human action that restores the Browser Identity after it can no longer establish
a valid Shop Session.
_Avoid_: Refresh
