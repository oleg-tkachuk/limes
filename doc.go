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
//   - Budget enforcement at the authorisation boundary: the Meter refuses a
//     charge once a ceiling is exhausted, rather than business logic
//     discovering it three calls deep.
//   - Mid-flight revocation: a compromised agent's capability is revoked,
//     together with everything it delegated.
//   - Sub-capabilities without an operator: an orchestrator narrows its own
//     authority for each sub-agent it spawns, through the Issuer with no admin
//     API in the path, or offline as a Biscuit block with no call at all.
//
// Wire format is JWT compact serialization with Ed25519 signatures (`alg:
// Ed25519`, RFC 9864), or a Biscuit v3 sealing such a token;
// docs/standards.md lists every standard followed. Verification is local:
// the signature is checked against a resolved public key, and the one store
// read, whether the capability is revoked, goes through a short-lived cache
// ([CachedRevocationChecker]), so consumers scale horizontally without
// contending on a shared store.
//
// # Packages
//
// Two standards the verifier relies on are packages of their own, usable
// without the rest: [github.com/oleg-tkachuk/limes/jwks] for key sets (RFC
// 7517) and [github.com/oleg-tkachuk/limes/dpop] for proof of possession
// (RFC 9449). [github.com/oleg-tkachuk/limes/inspect] reads a token without
// verifying it, for tooling. memstore, storetest and metertest serve stores;
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
//     in-memory map is a valid implementation.
//   - [UsageStore] — request and spend counters and the charges ledger; the
//     union of [Meter], [MeterReader], [TenantBudgets] and [UsageHousekeeping]. Every charge
//     and request also counts against each ancestor. Generic in TX, the
//     consumer's transaction handle; see "No-transaction mode" below.
//   - [KeyResolver] — public verification keys. [jwks.Static] ships as a
//     working implementation and supports rotation via SetKey / RemoveKey;
//     [jwks.Remote] fetches an issuer's JWKS for verifiers that run
//     apart from it.
//
// Four more are needed only with the feature they serve:
// [BiscuitRevocationStore] to accept Biscuit tokens, [CopyUsageReader] to
// show a Biscuit copy's own counters, [dpop.ReplayCache] to share DPoP replay
// state across replicas ([dpop.MemoryReplayCache] ships for one process), and
// [dpop.Nonces] to require server nonces ([dpop.HMACNonces] ships, and works
// across replicas holding the same keys).
//
// A Capability crosses the API by value, in and out: no caller can alter one
// another holds, and one that is not there is an error, never nil.
//
// Every amount is [Nanos], an int64 count of billionths of the unit
// (UnitCode, an ISO 4217 code; XXX for a budget that is not money), so spend
// sums, compares and refunds exactly, with no rounding left to the consumer.
//
// Every read names what it reads — Store.GetRecord a capability,
// MeterReader.GetUsage the counters, TenantBudgets.GetTenantBudget a ceiling —
// so one type may implement Store and UsageStore together, or each apart as
// memstore does.
//
// # No-transaction mode
//
// Meter.Charge and Meter.Settle take an onCharged callback that runs inside
// the charge, receiving the consumer's transaction handle, so a side effect
// such as an outbox write commits atomically with the spend. The module never
// constructs, inspects or constrains that handle — it only threads it back.
// That is what keeps this package free of any database dependency.
//
// A consumer with no transactional storage instantiates UsageStore[struct{}]
// and always passes nil for onCharged. Everything else behaves identically;
// only the atomic-side-effect guarantee is forgone, and that limitation is
// store-dependent rather than a degradation of the primitive.
//
// # Guarantees
//
// Rejections are matchable sentinels, so a consumer can map each to its own
// transport status, or let Verifier.ErrorResponse map it to HTTP. A
// charge rejected by any ceiling — the capability's, an ancestor's, the
// tenant's — leaves every counter and the ledger unmutated, so retrying after
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
// See README.md for a five-minute walkthrough and example/ for it as a
// runnable program.
package limes
