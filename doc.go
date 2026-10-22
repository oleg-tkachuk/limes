// Package limes is an object-capability authorisation primitive designed
// for agentic workloads.
//
// A Capability is a short-lived, signed token granting its bearer a specific
// set of operations on a specific set of resources, with explicit budget and
// lifetime caveats. It is delegable — a parent issues a strictly narrower
// child to a sub-agent — and individually revocable. Compared with long-lived
// API keys or role-based access it gives:
//
//   - Per-call attribution: every call carries the capability ID, so audit and
//     cost roll up by it, down to which run of which agent spent what.
//   - Budget enforcement at the authorisation boundary: the meter
//     ([github.com/oleg-tkachuk/limes/meter]) refuses a charge once a
//     ceiling is exhausted, rather than business logic discovering it three
//     calls deep.
//   - Mid-flight revocation: a compromised agent's capability is revoked,
//     together with everything it delegated.
//   - Sub-capabilities without an operator: an orchestrator narrows its own
//     authority for each sub-agent it spawns, through the Issuer with no admin
//     API in the path, or offline as a Biscuit block with no call at all.
//
// Wire format is JWT compact serialization with Ed25519 signatures (`alg:
// Ed25519`, RFC 9864), or a Biscuit v3 sealing such a token;
// docs/standards.md lists every standard followed. Verification is local:
// the signature is checked against a resolved public key, and the store
// reads, whether the capability is revoked and, for a Biscuit, whether its
// copy is, go through one short-lived cache ([RevocationCache]), so
// consumers scale horizontally without contending on a shared store.
//
// # Packages
//
// Two standards the verifier relies on are packages of their own, usable
// without the rest: [github.com/oleg-tkachuk/limes/jwks] for key sets (RFC
// 7517) and [github.com/oleg-tkachuk/limes/dpop] for proof of possession
// (RFC 9449). [github.com/oleg-tkachuk/limes/meter] counts requests and
// spend against a capability's ceilings and keeps the charges ledger; a
// consumer that only verifies tokens never imports it.
// [github.com/oleg-tkachuk/limes/inspect] reads a token without verifying
// it, for tooling. memstore, storetest and metertest serve stores;
// dpop/dpoptest and jwks/jwkstest check a replay cache, a nonce source and a
// key resolver. Two modules of their own ship stores with a driver the root
// module does not resolve: github.com/oleg-tkachuk/limes/pgstore, every store
// contract over PostgreSQL, and github.com/oleg-tkachuk/limes/redisreplay, a
// replay cache over Redis.
//
// # What you implement
//
// Three contracts are required; a consumer that implements them needs nothing
// else:
//
//   - [Store] — capability records and revocations; an [Issuer] needs only its
//     [IssuanceStore] half. IsRevoked answers for the
//     capability's whole delegation chain, at most [MaxDelegationDepth]
//     ancestors deep. No transactions required; an
//     in-memory map is a valid implementation. A store that scopes calls to
//     a tenant refuses one whose request names another with
//     [ErrScopeMismatch]. A store paired with a meter.TenantUsageEraser
//     refuses Insert for a tenant whose usage was erased with
//     [ErrTenantErased].
//   - [github.com/oleg-tkachuk/limes/meter.UsageStore] — request and
//     spend counters and the charges ledger, for a consumer that meters;
//     see package meter.
//   - [KeyResolver] — public verification keys. [jwks.Static] ships as a
//     working implementation and supports rotation via SetKey / RemoveKey;
//     [jwks.Remote] fetches an issuer's JWKS for verifiers that run
//     apart from it.
//
// More are needed only with the feature they serve:
// [BiscuitRevocationLookup] to accept Biscuit tokens and
// [BiscuitRevocationStore] to revoke one copy of one, [RecordCensus] to count
// capability records by disposition across tenants, [RecordLister] to list a
// tenant's records with their dispositions and [RecordListingReader] to read
// one with its disposition, [TenantRecordEraser] to
// remove a tenant, [dpop.ReplayCache] to share DPoP replay
// state across replicas ([dpop.MemoryReplayCache] ships for one process), and
// [dpop.Nonces] to require server nonces ([dpop.HMACNonces] ships, and works
// across replicas holding the same keys).
//
// A Capability crosses the API by value, in and out: no caller can alter one
// another holds, and one that is not there is an error, never nil.
//
// Every amount is [Nanos], an int64 count of billionths of the unit
// (BudgetCeiling.UnitCode, an ISO 4217 code; XXX for a budget that is not
// money), so spend sums, compares and refunds exactly, with no rounding left
// to the consumer.
//
// Every read names what it reads — Store.GetRecord a capability,
// meter.MeterReader.GetUsage the counters — so one type may implement Store
// and meter.UsageStore together, or each apart as memstore does.
//
// # Guarantees
//
// Rejections are matchable sentinels, so a consumer can map each to its own
// transport status, or let Verifier.ErrorResponse map it to HTTP. A
// charge rejected by any ceiling — a Biscuit copy's, the capability's, an
// ancestor's, the tenant's — leaves every counter and the ledger unmutated, so retrying after
// a rejection is safe. Delegation needs [OpShare] on the parent and can only
// narrow: any widening returns
// [ErrDelegationTooWide], and a differing unit code returns
// [ErrUnitCodeMismatch] rather than converting.
//
// # Enforcing caveats
//
// [Caveats.Check] and [Caveats.CheckSource] are the single definition of what
// the caveats mean at use time; delegation narrowing ([Narrows]) uses the same
// resource matcher ([MatchResource]), so the two cannot disagree.
//
// See README.md for a five-minute walkthrough and examples/quickstart for it
// as a runnable program.
package limes
