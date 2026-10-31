# ADR-0015: Releasing the holds of a bulk revocation at once

**Status:** Accepted
**Date:** 2026-10-30
**Deciders:** Oleg Tkachuk
**Relates to:** [ADR-0013](0013-bulk-revocation.md), [ADR-0014](0014-bulk-revocation-across-tenants.md)

## Context

A revoked capability's open holds keep counting against its ancestors'
and its tenant's ceilings until they expire and `ReleaseExpired` runs —
five minutes by default. That is on purpose: a cost reserved may already be
incurred, and `Settle` of a revoked capability's hold is allowed so it
reaches the ledger.

An operator who revokes a compromised client or a runaway agent run often
knows no such cost will come, and wants the tenant's budget back now.

## Decision

1. **`meter.MatchingReleaser`**, an optional interface beside
   `UsageStore`, with `ReleaseMatching(ctx, ReleaseMatchingRequest)`. The
   request takes the selectors of `RevokeMatchingRequest` — subject,
   on-behalf-of, client, session, agent run — a tenant and a limit.
2. **It releases only holds of revoked capabilities.** A live match's
   hold is left: releasing it would drop a reservation a cost may still
   settle, and stop nothing. Run after `RevokeMatching`, it finishes what
   the revocation started.
3. **A match's descendants' holds go with it.** A hold names the
   ancestors it holds against, so a hold is released when its capability
   or one of those matches, as the revocation reaches descendants through
   the lineage.
4. **One tenant per call, or every tenant.** `TenantID` releases in one
   tenant, under its scope. `AllTenants` releases in every tenant, by
   `ClientID` and `Session` alone, as `RevokeMatchingRequest.AllTenants`
   revokes, and pgstore runs it only under `WithAllTenants`.
   `ReleaseMatchingResult.Tenants` names the tenants a hold was released
   in. A release keyed to the tenants a revocation reported could not be
   retried: once the revocation went through, repeating it revokes
   nothing and reports no tenant, and the holds would count until they
   expired. The release selects what is revoked, so it needs no such
   list.
5. **Batches, as every bulk call.** `Limit` bounds a call; `More` says a
   batch was full.
6. **memstore and pgstore implement it, and `metertest` checks it** when
   the usage store is a `MatchingReleaser` and the Env can revoke a record
   (`Env.Revoke`), with each capability's client recorded
   (`CapabilitySpec.ClientID`).

## Consequences

- **Deleting a client frees its tenants' budgets in two loops,**
  revocation then release, each under `AllTenants`, with no wait for the
  holds' expiry; either loop is retried as it was.
- **Across tenants, pgstore selects from every open hold,** whose
  number the holds' expiry keeps small: no index leads with the client.
  The batch's counters are row locks, so it needs no bound on its tenants,
  as a revocation's advisory locks do.
- **A cost that was in flight is lost** if its hold is released before it
  settles: `Settle` then finds no hold. The operator chooses the release;
  `ReleaseExpired` alone never makes that choice.
- **A usage store of the consumer's own** compiles as before and is not
  held to the new checks unless it implements `MatchingReleaser`.
