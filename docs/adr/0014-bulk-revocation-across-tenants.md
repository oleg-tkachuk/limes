# ADR-0014: Revoking a client's or a session's capabilities in every tenant

**Status:** Accepted
**Date:** 2026-10-30
**Deciders:** Oleg Tkachuk
**Relates to:** [ADR-0003](0003-oauth-token-exchange.md), [ADR-0009](0009-one-capability-per-subject-token.md), [ADR-0013](0013-bulk-revocation.md)

## Context

A consumer may run one authorization server, or one identity provider, for
many tenants. Its OAuth client is registered once, and the token exchange
issues capabilities with that client's id in every tenant whose users it
acts for. A user's session at the provider can likewise reach capabilities
in more than one tenant.

`RevokeMatching` (ADR-0013) requires a tenant. The events that call for it —
a client deleted, a back-channel logout — name a client or a session and no
tenant. The consumer either lists every tenant and loops, which misses a
tenant created meanwhile, or leaves the capabilities live until they expire.

## Decision

1. **`RevokeMatchingRequest.AllTenants`**, as `CountRecordsRequest.AllTenants`:
   exactly one of `TenantID` and `AllTenants` is required.
2. **Across tenants, only `ClientID` and `Session` select.** Both are named
   by an issuer that knows no tenant, so one value may hold capabilities in
   many. `Subject` and `OnBehalfOf` name a principal only within its tenant:
   matched across tenants, one tenant's `user:42` would revoke another's.
   `Validate` refuses them beside `AllTenants`. This takes one issuer per
   deployment as given: the `client_id` and `session` columns store no
   issuer, as within one tenant today.
3. **pgstore runs it only under `WithAllTenants`.** A scope that admits one
   tenant, or reads only, is `ErrScopeMismatch`, as the census refuses one.
4. **A batch is short and spans few tenants.**
   - It selects at most `Limit` matches in order of tenant and id, locking
     nothing, and keeps those of the first 16 tenants (`maxTenantsPerBatch`).
   - It then takes each of those tenants' revoke locks in that order — the
     lock a revoke in one tenant takes, so a delegation in that tenant waits
     as it would for any revoke — and only then locks rows and writes an
     entry for each match that still has none, in its own tenant.
   - `More` reports a full selection or one cut at the tenant bound, never
     the entries written, which a revoke between the two statements may have
     made fewer.
   - Taking the locks in one order keeps two batches across tenants from
     deadlocking; the bound keeps the advisory locks of one transaction well
     within `max_locks_per_transaction`.
5. **The result names the tenants written in** (`RevokeMatchingResult.Tenants`),
   ascending, for an audit per tenant. A call in one tenant names it alone.
6. **Migration 027** indexes `(client_id, tenant_id, id)` and
   `(session, tenant_id, id)`, built `CONCURRENTLY`. The indexes of
   migration 026 lead with the tenant, which a call across tenants does not
   name.
7. **memstore implements it, and `storetest` checks it** when
   `Env.BulkRevoker` is set, under `Env.AllTenantsCtx`.

## Consequences

- **Deleting a client, or ending a session, is one loop** under
  `WithAllTenants`, with no list of tenants to hold correct.
- **A batch across tenants writes in at most 16 tenants,** so a client with
  one capability in each of many tenants takes a call per 16 of them.
- **A store of the consumer's own** that implements `BulkRevoker` still
  compiles, but fails the new checks until it honours `AllTenants`.
- **pgstore needs schema version 27.** A consumer that vendors the schema
  (ADR-0011) applies step 027 before the release that queries it.
