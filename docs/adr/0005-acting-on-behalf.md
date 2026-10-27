# ADR-0005: Whom a capability acts for, as RFC 8693 `act`

**Status:** Accepted
**Date:** 2026-10-27
**Deciders:** Oleg Tkachuk
**Amends:** [ADR-0004](0004-token-format-v7.md)

## Context

A v7 capability is an RFC 9068 access token, and its `sub` named the
capability's holder: the agent. RFC 9068 §2.2 has `sub` name the resource
owner when one is involved, and RFC 8693 §4.1 names the party acting for it
in `act`, prior actors nested within. A capability an agent holds for a user,
then delegated to sub-agents, read by a validator of either RFC, named the
agent as the owner and no one as acting.

limes itself never read `sub`: the holder travels in `limes_principal`, and
delegation is followed through `limes_parent_id` and the store. The v7 format
was released in candidates only, so its claims can still change before 1.0
freezes them.

## Decision

1. **Two fields, the holder unchanged.** `Capability.Subject` stays the
   holder: everything limes checks, meters, lists and revokes keys on it.
   `Capability.OnBehalfOf` is the principal the holder acts for, of its
   tenant, set by `IssueRequest.OnBehalfOf`. `Capability.PriorActors` are the
   holders it was delegated through, nearest first.
2. **Delegation derives both.** A child acts for its parent's
   `OnBehalfOf`, with the parent's holder its nearest prior actor; a child
   of a capability acting for itself acts for the parent's holder. A
   `DelegateRequest` cannot name another.
3. **The v7 claims.** A capability acting for another has `sub` its
   subject, `limes_on_behalf_of` the whole principal, and `act` its holder,
   prior actors nested within. One acting for itself has `sub` its holder
   and neither claim. The verifier refuses a token whose `sub`, `act` and
   `limes_principal` disagree, whose `act` is deeper than any delegation, or
   whose prior actors have no parent. A v6 token has no member for either,
   and its signer leaves them out: they describe the grant and restrict
   nothing.
4. **The exchange.** A policy may have the capability act for the subject
   token's subject, and for no one else. Introspection answers `sub` and
   `act` as the token names them.

## Options considered

### Option A: `OnBehalfOf` beside the holder (chosen)

| Dimension | Assessment |
|---|---|
| Standards | `sub` and `act` as RFC 8693 and RFC 9068 read them |
| limes | Unchanged: the holder is still `Subject` and `limes_principal` |
| Cost | Two fields, two claims, one derivation at delegation |

### Option B: `act` with `sub` still the holder

**Cons:** RFC 8693 reads `act` as the party acting for `sub`. With `sub` the
holder, `act` can only repeat it or name someone as acting for the agent,
which inverts the roles.

### Option C: `sub` the user, the holder dropped from the token

**Cons:** limes would read its own holder from a claim it does not own, and
lose the agent's details, which `limes_principal` carries.

## Consequences

- A v7 token's `sub` is the user for a capability acting for one, so a
  gateway that keyed on `sub` as the agent reads `limes_principal` or `act`
  instead.
- Tokens of earlier v7 candidates verify only when they acted for no one;
  the golden v7 fixture is regenerated.
- `docs/standards.md` moves RFC 8693 in a token from "Not adopted" to the
  token format.
