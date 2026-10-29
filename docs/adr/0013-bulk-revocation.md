# ADR-0013: Revoking every capability of a subject, a client or a session

**Status:** Accepted
**Date:** 2026-10-29
**Deciders:** Oleg Tkachuk
**Relates to:** [ADR-0003](0003-oauth-token-exchange.md)

## Context

A store revokes one capability by id; its descendants stop with it. The only
action on more than one is erasing a whole tenant.

The events an operator acts on name no capability:

- a user leaves;
- a client is compromised;
- the identity provider ends a session, through OIDC back-channel logout
  for one.

Today a consumer has to list the capabilities and revoke them one by one,
holding the list correct against issuance happening meanwhile. ADR-0003
deferred revoking by session because no capability records its session.

## Decision

1. **`limes.BulkRevoker`**, an optional store interface beside `Store`:

   ```go
   RevokeMatching(ctx, RevokeMatchingRequest) (RevokeMatchingResult, error)
   ```

   - The request names a tenant, at least one selector, a note and an
     actor, and a limit.
   - The selectors are `Subject` (the principal authorised), `ClientID`
     and `Session`. Given together they all must match.
   - It writes a revocation entry for each matching capability that has
     none, as `Revoke` would. Every descendant stops through the lineage, as
     with `Revoke`.
   - It works in batches of the limit, as `Purge` does. The result counts
     what it revoked and says whether more may match, so a caller loops
     until none is left, and no single transaction holds a tenant's rows for
     long.
   - A capability issued while it runs, by a client still holding an old
     token, is the caller's to catch with another pass. The guide says so.
2. **The session is issuance metadata, not a claim.**
   - `IssueRequest.Session` and `InsertRequest.Session` record the identity
     provider's session the capability was issued under, beside `IssuedBy`,
     and `Record.Session` reads it back.
   - A child takes its parent's session, so a session's revocation reaches
     its delegations by lineage and by selector alike.
   - The token's wire format, `limes_v` and the golden fixtures do not
     change.
3. **The exchange records it.**
   - `exchange.Subject.SessionID` holds the subject token's `sid`, which
     OIDC uses for the session in logout and which `jwtverify` reads.
   - The Exchanger puts it on the capability.
   - A `SubjectVerifier` of the consumer's own sets it the same way.
4. **memstore and pgstore implement it.**
   - pgstore adds a `session` column and indexes on `(tenant_id, subject)`,
     `(tenant_id, client_id)` and `(tenant_id, session)`. The indexes are
     built `CONCURRENTLY`, outside a transaction, as every index on an
     existing table is.
   - `storetest` checks the contract when `Env.BulkRevoker` is set.

## Consequences

- **An operator revokes a user, a client or a session in one loop,** with
  no list of ids to hold correct.
- **A capability issued before this change has no session on record,** so
  revoking by session misses it. It still expires, and revoking by subject
  or client still reaches it.
- **`Record` gains a field and `InsertRequest` gains one.** A store of the
  consumer's own that ignores `Session` keeps working, but revokes nothing
  by it. `storetest` says so when its `BulkRevoker` is set.
