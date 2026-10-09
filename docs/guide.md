# Guide

Using limes, one concern at a time. [README.md](../README.md) has the five-minute
start, and [diagrams.md](diagrams.md) draws each path described here.

## Verify on every call

```go
cap, err := verifier.Verify(ctx, token, "my-service")
switch {
case errors.Is(err, limes.ErrExpired):          // TTL elapsed
case errors.Is(err, limes.ErrRevoked):          // pulled mid-flight
case errors.Is(err, limes.ErrAudienceMismatch): // token for another service
case errors.Is(err, limes.ErrInvalidSignature): // forged
}
```

Verification is **local** — the signature is checked against the resolved key,
with no round trip to the issuer, and nothing in the claims is read until it
has. Every rejection is a distinct sentinel; map each to whatever status your
transport uses. This module does not decide your HTTP or RPC codes.

The token must carry `typ: limes-cap+jwt` and a tenant, and may be at most
`VerifierConfig.MaxTokenBytes` long (16 KiB unless set). When several issuers share a verifier, set
`KeyIssuers` (kid → issuer) so one issuer's key cannot sign for another.
Verifiers running apart from the issuer resolve keys with
`RemoteJWKSResolver`, which caches the issuer's JWKS, picks up a rotated-in
kid on first sight (rate-limited), and fails closed after `MaxStale`:

```go
keys, err := limes.NewRemoteJWKSResolver(limes.RemoteJWKSConfig{
    URL: "https://issuer.example.com/.well-known/jwks.json",
})
verifier, err := limes.NewStandardVerifier(limes.VerifierConfig{
    Keys: keys, Revocations: revocations, TrustedIssuers: []string{"my-issuer"},
})
```

The issuer serves that document with `MarshalJWKS`; `ParseJWKS` reads one
back, for a resolver of your own. It skips an entry it cannot use — a foreign
`kty`, no kid, a malformed key — rather than refusing the set, so one bad
entry does not take every other key out of service mid-rotation.

### Inspect a token without trusting it

`Decode` reads a token's claims and checks nothing; `VerifySignature` checks
only its signature against one key. Both are for tooling — showing what a
token grants, or which key signed it. Neither applies expiry, audience or
revocation, so never authorise a request on either: that is `Verify`.

## Enforce the caveats on every operation

Verification proves the token is genuine; it does not know what the bearer is
about to do. Ask the caveats, through the one definition the module ships:

```go
if err := cap.Caveats.CheckSource(clientAddr); err != nil { /* per connection */ }

err := cap.Caveats.Check(limes.CheckRequest{
    Op:                limes.OpGet,
    Resource:          "corpus/public/2026/report.pdf",
    HasIdempotencyKey: req.Header.Get("Idempotency-Key") != "",
    ResourceTainted:   object.Flagged, // your taint signal, if you track one
})
switch {
case errors.Is(err, limes.ErrOpNotAllowed):
case errors.Is(err, limes.ErrResourceNotAllowed):
case errors.Is(err, limes.ErrIdempotencyKeyRequired):
case errors.Is(err, limes.ErrTaintedReadNotAllowed):
}
// every one of these also matches limes.ErrCaveatViolation
```

Resource prefixes match at a `/` segment boundary: `corpus/public` covers
`corpus/public/x` but not `corpus/public-secret`. An operation that cannot name
its resource (`Resource: ""`) is refused by a resource-restricted capability —
for an operation over a set, pass the prefix that bounds the set.
`AllowTaintedRead` can only act on a taint signal you supply; if you track
none, it protects nothing, and you should say so.

Your own operations are namespaced (`tool:retrieve`, `mcp:github/create_issue`)
and are treated as writes unless you declare otherwise, so an undeclared one
needs an idempotency key under `IdempotencyKeyRequired` and is never refused as
a tainted read. Declare the effect where you define the operation — a method
option, a tool registry — and pass it on every check:

```go
err := cap.Caveats.Check(limes.CheckRequest{
    Op:       "tool:retrieve",
    Effect:   limes.EffectRead, // from your operation's definition
    Resource: "corpus/public/",
})
```

A built-in operation keeps its own effect: declaring `put` a read returns
`ErrEffectConflict`, a programming error rather than a caveat violation. Gate
anything else on the effect through `CheckRequest.Mutating`, so it and `Check`
cannot disagree.

## Delegate — attenuate, never escalate

An orchestrator narrows its own authority and hands the result to a worker it
spawned, without calling back to any admin API:

```go
child, childToken, err := issuer.Delegate(ctx, limes.DelegateRequest{
    Parent:  cap,                       // the verified parent
    Subject: limes.Principal{ /* the sub-agent */ },
    TTL:     2 * time.Minute,
    Caveats: limes.Caveats{
        Ops:              []limes.Op{limes.OpGet},  // dropped OpShare
        ResourcePrefixes: []string{"corpus/public/2026/"},     // narrowed
        MaxBudgetAmount:  limes.MustParseAmount("2.00"),  // 25.00 → 2.00
        UnitCode:         "USD",                               // MUST match parent
    },
})
```

**The rule**: a child may only be narrower. Ask for an operation the parent
lacks, a wider scope, a bigger budget or a longer life, and you get
`ErrDelegationTooWide`. Ask for a different `UnitCode` and you get
`ErrUnitCodeMismatch` — units are never converted. Each child resource must be
reachable by the parent (a parent pinned to exact URIs admits no child
prefix), and each child network must lie inside a parent network.

To hand a child exactly the parent's caveats, say so with
`InheritCaveats: true`; caveats without any `Ops` are rejected rather than
silently replaced by the parent's. A parent that has expired, or that is
revoked (itself or any ancestor), cannot delegate.

Narrowing bounds each child. The tree as a whole is bounded at use: every
charge and request also counts against each ancestor, so a parent holding
25.00 that delegates 20.00 to each of two workers can still spend 25.00 in
total, not 40.00.

This is the property that makes the model safe to hand to an agent: it can
attenuate itself, but never escalate.

## Bind a capability to a key (DPoP)

A capability is a bearer token unless it is bound: whoever holds it can use
it, and an agent's tokens end up in prompts, tool output and logs. Binding it
to a key the agent keeps — RFC 9449 DPoP — makes a copy useless without the
key:

```go
jkt, _ := limes.KeyThumbprint(agentKey.Public()) // Ed25519 or P-256
cap, token, _ := issuer.Issue(ctx, limes.IssueRequest{ /* … */ ConfirmationJKT: jkt})

// Client: a fresh proof per request, signed over its method, URL and token.
proof, _ := limes.NewDPoPProof(agentKey, "POST", url, token, time.Now())

// Server, after Verify:
dpop := &limes.DPoPVerifier{Replay: limes.NewMemoryReplayCache(0)}
err := dpop.Check(ctx, cap, limes.DPoPRequest{Proof: proof, Method: "POST", URL: url, Token: token})
```

The token carries the thumbprint as `cnf.jkt` (RFC 7800), omitted when
unbound, so unbound tokens are byte-for-byte what they were. A delegated
child inherits its parent's binding or takes the sub-agent's own key, and
`Narrows` refuses a child that drops it. `Check` refuses a proof from another
key, for another method, URL or token, outside a minute of the verifier's
clock, or seen before; every refusal matches `ErrInvalidSignature`. The
replay cache is in-process: behind several replicas, a proof replayed to a
different one inside the window is not caught — share a `ReplayCache` to
close that.

## Narrow offline (Biscuit)

`Delegate` narrows a capability, but it needs the issuer. The Biscuit form
of the same capability (Biscuit v3) can be narrowed by whoever holds it, with
no key and no network, and a narrowing can never be undone:

```go
full, _ := issuer.Biscuit(cap) // same ID, caveats and expiry as cap

// An agent, offline, hands a worker a smaller token:
worker, _ := limes.Attenuate(full, limes.Attenuation{
    Ops:              []limes.Op{limes.OpGet},
    ResourcePrefixes: []string{"corpus/public/"},
    ExpiresAt:        time.Now().Add(10 * time.Minute),
})

// A verifier built with AcceptBiscuit: true returns the narrowed Capability.
c, err := verifier.Verify(ctx, worker, limes.AudiencePlaneData)
```

An attenuation block may restrict operations, resources, planes, the expiry,
and (on an unbound token) bind a key. The verifier folds each block in
through `Narrows`, so an attenuated token is checked exactly like a
delegated one. A block that asks for more than the token has, or holds
anything else (Datalog rules and checks included), makes the whole token
invalid. An attenuated copy spends its capability's budget and request
count, and revoking the capability revokes every copy.

A copy can also get limits of its own: `MaxRequests` and `MaxBudget` on
the `Attenuation`. Each is counted under the revocation id of the block that
set it, so siblings narrowed apart count apart, while the capability's own
limits still bound them all; a limit must fit every limit already in force.
The verifier hands them to the caller as `Capability.Copies`, and the `Meter`
counts them when they are passed on as `Copies` in `BumpRequest`,
`ChargeRequest` and `ReserveRequest`. A verifier admits such a copy only with
`MeterCopies` set, which says the `Meter` behind it counts copies; without it
the token is refused rather than accepted with its limits unkept.
`verifier.BiscuitCopy` names the limits in force on a copy, on any verifier,
and a `CopyUsageReader` (`memstore` implements one) reads what each has
counted. A copy with limits of its own cannot delegate — a server-side child
would count against the capability, never the copy — so `Delegate` refuses
it with `ErrDelegationTooWide`; its holder attenuates it instead.

One copy can also be revoked on its own. `verifier.BiscuitCopy(ctx, token)`
checks the token and names its capability and the revocation id of its last
block; listing that id through `BiscuitRevocationStore.RevokeBiscuit` stops
that copy and every copy attenuated from it, and leaves the copy it came from,
its siblings and the capability's JWT working. A verifier that accepts
Biscuits needs `BiscuitRevocations` — wrap the store in a
`CachedBiscuitRevocationChecker` and clear it with the other cache.

The Biscuit seals an ordinary signed token, so a KMS-held signing key works.
That token carries the root of the Biscuit's signature chain, whose private
half is discarded once the Biscuit is built. A sealed token presented on its
own is refused, so it cannot be lifted out to shed an attenuation, and it
cannot be re-wrapped in a fresh Biscuit.

## Charge against the budget

```go
receipt, err := usage.Charge(ctx, limes.ChargeRequest{
    CapabilityID: cap.ID, TenantID: tenantID,
    Amount: limes.MustParseAmount("0.35"), MaxBudget: cap.Caveats.MaxBudgetAmount,
    UnitCode: "USD", Op: "search", Actor: "orchestrator",
}, nil)
if errors.Is(err, limes.ErrBudgetExceeded) {
    // rejected at the auth boundary — before your business logic ran
}
```

Every ceiling is checked: each Biscuit copy's own (the `Copies` you pass),
the capability's, each ancestor's, then the tenant aggregate. If **any**
rejects, no counter moves, so a retry after rejection is safe.

`receipt.ChargeID` names the ledger row. Refund against it:

```go
usage.Refund(ctx, limes.RefundRequest{ChargeID: receipt.ChargeID, Amount: limes.MustParseAmount("0.10")}) // partial
usage.Refund(ctx, limes.RefundRequest{ChargeID: receipt.ChargeID})               // the rest
```

A refund returns spend to every counter the charge took it from, and never
more than the charge: `Amount: 0` refunds what is left, so a retried full
refund is a no-op.

## Amounts are exact

Every amount is `limes.Nanos`: an `int64` count of billionths of the
unit, so 0.35 USD is `350_000_000`. Sums, ceilings, holds and refunds are
integer arithmetic, exact however many charges they take: a tenth and two
tenths fit under a ceiling of three tenths, and ten tenths make one. Nothing
on your side needs rounding, summing apart or correcting.

- **Writing one.** `ParseAmount("0.35")` reads a decimal exactly and refuses
  anything finer than a nano rather than round it; `MustParseAmount` is the
  same for a literal in your code. A price that reaches you as a `float64`
  goes through `AmountFromFloat`, which rounds once, to the nearest nano.
- **Reading one.** `String()` writes the exact decimal (`"0.35"`);
  `Float64()` is for display and metrics only.
- **Range.** Up to `MaxNanos`, just under a billion units. A charge that
  would take a counter past it is refused with `ErrInvalidAmount`.
- **Units.** `UnitCode` is an ISO 4217 code: USD, EUR, UAH or GBP, or XXX,
  ISO 4217's code for "no currency", for a budget that is not money.

In a token the budget is still a plain JSON number of units
(`"MaxBudgetAmount": 1.5`), read and written without a float in between.


## Reserve before a cost is known

An LLM call is priced by the tokens it ends up using. Hold an estimate first,
then settle the actual cost:

```go
r, err := usage.Reserve(ctx, limes.ReserveRequest{
    CapabilityID: cap.ID, TenantID: tenantID,
    Amount: limes.MustParseAmount("0.50"), MaxBudget: cap.Caveats.MaxBudgetAmount, UnitCode: "USD",
    TTL: 2 * time.Minute,
})
// … the call runs …
receipt, err := usage.Settle(ctx, limes.SettleRequest{
    ReservationID: r.ID, Amount: actualCost, MaxBudget: cap.Caveats.MaxBudgetAmount,
}, nil)
```

A hold counts against every ceiling — the capability's, each ancestor's, the
tenant's — exactly as spend does, so two callers cannot both hold the last
budget. `Settle` charges the actual cost in place of the hold; above the hold,
the excess must fit, or the hold stays for you to settle lower or `Release`.
A hold never settled lapses after its TTL: run `ReleaseExpired` on a timer.

The trailing `nil` is an optional in-transaction callback. If your store has
transactions, pass a function and the module threads **your** handle back so
your side effects commit atomically with the spend:

```go
usage.Charge(ctx, req, func(ctx context.Context, tx MyTx) error {
    return myOutbox.Enqueue(ctx, tx, chargeEvent)   // commits with the charge
})
```

`Settle` takes the same callback. It must not call back into the `Meter`: a
store may hold its counters while the callback runs. The module never
inspects `MyTx` — it only hands it back. That is why this
library has no database dependency.

## Charge a cost reported after the fact

Some costs never pass through your verifier: a call made straight to a
provider whose price arrives later, in a usage record or on an event stream
delivered at least once. Two things change for such a cost.

**It may arrive twice.** Name it with the id your records already give it, and
the capability is charged once per name; a repeat returns the first receipt
with `Replayed` set and runs no callback:

```go
receipt, err := usage.Charge(ctx, limes.ChargeRequest{
    CapabilityID: capID, TenantID: tenantID, Amount: cost,
    MaxBudget:   rec.Caveats.MaxBudgetAmount, // the stored record, from Store.Get
    ExternalRef: event.CallID,
    Overrun:     limes.OverrunRecord,
}, nil)
```

A settle is idempotent on its reservation in the same way: settling one twice
returns the first charge. `ChargeByRef` reads a named charge back, so a
reporter can reconcile its own records with the ledger.

**It has already been spent.** Refusing it would only leave the ledger short.
`OverrunRecord` charges it past every ceiling it crosses and sets
`receipt.Overrun`; the crossed ceiling then refuses every later charge and
reservation made with the default `OverrunReject`, which is what stops the
next cost before it is incurred. Keep `OverrunReject` for anything you can
still decline.

A reporter with no token in hand reads the ceiling from the capability's
stored record (`Store.Get`), as the meter already does for every ancestor. A
price that is not known yet is not a price of zero: hold the estimate and
settle when it is known, or let the hold lapse and count against the ceilings
until `ReleaseExpired` runs.

## Check your own store

Much of each contract lives in request fields and in which sentinel comes
back, so a store that gets one wrong still compiles. Run the module's checks
from your store's tests — `storetest` for a `Store` (and a
`BiscuitRevocationStore`), `metertest` for a `Meter`:

```go
func TestMeterContract(t *testing.T) {
    metertest.Run(t, func(t *testing.T) metertest.Env[MyTx] {
        return metertest.Env[MyTx]{Ctx: ctx, Usage: newStore(t), Tenant: tenant,
            NewCapability: recordCapability}
    })
}
```

`memstore` runs the same checks, and so do the PostgreSQL stores of
[Paladin](https://github.com/oleg-tkachuk/paladin), the module's first consumer.

## Revoke

```go
records.Revoke(ctx, limes.RevokeRequest{
    ID: cap.ID, Reason: "agent looping", Actor: "operator@example.com",
    CascadeChildren: true,   // takes every sub-agent with it
})
```

Idempotent. Verifiers that cache revocation answers learn of it within the
cache TTL, or at once if you call `CachedRevocationChecker.Clear` when a
revocation is announced — on a Postgres `NOTIFY`, for example.
Revoking a capability revokes everything delegated from it, with or
without the flag: `IsRevoked` answers for the whole chain. `CascadeChildren`
also writes a revocation entry per descendant, so the audit trail names each
capability that was stopped.

## Bring your own storage

Implement these and you are done:

| Interface | Stores | Transactions needed? |
|---|---|---|
| `Store` | Capability records + revocations | No |
| `BiscuitRevocationStore` | Revoked Biscuit copies (only if you accept Biscuits) | No |
| `CopyUsageReader` | Reads Biscuit copies' own counters (only to show them) | No |
| `UsageStore[TX]` = `Meter[TX]` + `TenantBudgets` + `UsageHousekeeping` | Request and spend counters, the charges ledger, tenant ceilings | Only for atomic side effects |
| `KeyResolver` | Public verification keys | No |
| `ReplayCache` | DPoP proof ids seen (only for key-bound tokens; `MemoryReplayCache` ships, share one across replicas) | No |

Two obligations are easy to miss. `Store.IsRevoked` answers for the
capability's whole delegation chain, and `Meter` applies every charge and
request to each ancestor as well, reading the ancestor's ceilings from its
record. Code that only meters can depend on `Meter` alone; code that only
administers tenant ceilings on `TenantBudgets` alone.

`Store.Insert` should tell a missing tenant from a failure: return
`ErrUnknownTenant` for a tenant it does not hold and `ErrTenantDeleted` for one
in the trash. `Issue` and `Delegate` pass them through, wrapped, beside
`ErrInvalidRequest`, which every check on the request itself matches — a
missing tenant id, an empty audience entry, a negative TTL, a malformed
thumbprint, invalid caveats. Anything else they return is the store's or the
signer's failure, not the caller's.

Every input is a `…Request` and every read names what it reads (`Get`,
`GetUsage`, `GetTenantBudget`), so one type may implement `Store` and
`UsageStore` together, or each apart as `memstore` does.

`UsageStore` is generic in `TX` — *your* transaction type. Have none?
Instantiate `UsageStore[struct{}]` and always pass `nil` for the callback.
Everything works identically; you forgo the atomic-side-effect
guarantee.

The two listings, `ListByPrincipal` and `ListTenantBudgets`, are pages with an
opaque cursor, so an API can hand a page token to its client. To read a whole
listing in one go, range over `AllByPrincipal` or `AllTenantBudgets`: they
walk the pages for you, stop fetching when the loop breaks, and refuse a page
that returns the cursor it was fetched with (`ErrCursorRepeated`).

```go
for c, err := range limes.AllByPrincipal(ctx, store, req) {
    if err != nil { /* yielded once; the walk ends */ }
    // …
}
```

## Key rotation

1. Publish the new public key alongside the old (`SetKey`, or add it to the
   served JWKS — `MarshalJWKS` output is sorted by kid, so it is stable).
2. Switch the issuer to sign with the new key.
3. Withdraw the old key (`RemoveKey`) only after the **longest outstanding
   TTL** has elapsed.

Skipping the wait in step 3 invalidates tokens agents are still holding. Since
TTLs here are minutes, the wait is short — which is part of why short TTLs are
the default posture.

