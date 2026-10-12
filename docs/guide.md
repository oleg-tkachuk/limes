# Guide

Using limes, one concern at a time. [README.md](../README.md) has the five-minute
start, [diagrams.md](diagrams.md) draws each path described here, and
[standards.md](standards.md) maps each to the RFC it follows.

## Authorize every call

```go
scheme, token, err := limes.ParseAuthorization(r.Header.Get(limes.AuthorizationHeader))
if err != nil {
    // No credentials (ErrNoCredentials): 401, a challenge with no error code.
    // A malformed header (ErrMalformedAuthorization): 400.
    limes.WriteErrorResponse(w, verifier.ErrorResponse(ctx, err, scheme))
    return
}
granted, err := verifier.Authorize(ctx, limes.AuthorizeRequest{
    Verify: limes.VerifyRequest{
        Token: token, Scheme: scheme, Audience: "https://data.example.com",
        // Needed when the capability is key-bound; ignored when it is not.
        DPoP: dpop.Proof{JWT: r.Header.Get(dpop.Header), Method: r.Method, URL: requestURL},
    },
    Check: limes.CheckRequest{Op: limes.OpGet, Resource: "corpus/public/report.pdf"},
    ClientAddr:   clientAddr,
})
// verifier.ErrorResponse(ctx, err, scheme) answers a refusal the RFC 6750 /
// RFC 9449 / RFC 9457 way; or branch on the sentinel yourself:
switch {
case errors.Is(err, limes.ErrExpired):          // TTL elapsed
case errors.Is(err, limes.ErrNotYetValid):      // NotBefore ahead, past Leeway (30s by default)
case errors.Is(err, limes.ErrRevoked):          // pulled mid-flight
case errors.Is(err, limes.ErrAudienceMismatch): // token for another service
case errors.Is(err, limes.ErrCaveatViolation):  // not allowed to do this
case errors.Is(err, limes.ErrProofOfPossession): // no valid proof that the bearer holds the key
case errors.Is(err, limes.ErrInvalidToken): // forged, or not this verifier's to accept
case errors.Is(err, jwks.ErrUnavailable):  // the issuer's keys out of reach: the server's trouble, not the token's
}
// Every refusal of the token or its proof — and ParseAuthorization's
// ErrNoCredentials — also matches ErrUnauthenticated: the one check for
// "answer 401". ErrMalformedAuthorization does not: it is a bad request.
```

`Verify` and `Authorize` return a `VerifiedCapability`: what the token
grants, which only the verifier makes. `granted.Capability()` is a copy of
the `Capability` to read — `cap` below — and `Issuer.Delegate` takes the
`VerifiedCapability` itself, so a parent is always one the verifier checked.

`Authorize` is `Verify` — the token itself — followed by every caveat that
needs no state: the operation, the resource, the idempotency key, the tainted
read and the client's address. Request and budget limits are counted by the
`Meter` (below), inside your own transaction. Call `Verify` alone only where
there is no operation to check, such as before a delegation.

Verification is **local** — the signature is checked against the resolved key,
with no round trip to the issuer, and nothing in the claims is read until it
has. Every rejection is a distinct sentinel; map each to whatever status your
transport uses, or let `ErrorResponse` answer it the HTTP way (below).

The token must carry a `typ` the verifier accepts (`AcceptTokenTypes`, by
default `limes-cap-v4+jwt`), a tenant and a principal of a
known kind (`PrincipalUser`, `PrincipalAgent` or `PrincipalService`; agent
details only on an agent — `Principal.Validate`, which `Issue` and `Delegate`
run too), its claims must
hold no member the verifier does not know (a caveat it could not enforce),
none twice and none in another case, and may be at most
`VerifierConfig.MaxTokenBytes` long (16 KiB unless set). When several issuers share a verifier, set
`KeyIssuers` (kid → issuer) so one issuer's key cannot sign for another —
or, for issuers that do share one trust boundary, `SharedKeys: true`; a
verifier trusting several issuers with neither is refused, and so is an
empty map, which would refuse every token.
Verifiers running apart from the issuer resolve keys with
`jwks.Remote`. It fetches the issuer's JWKS on first use, keeps it as
long as the response's `Cache-Control` allows within `MinRefreshInterval`
(10s) and `RefreshInterval` (5m), revalidates by ETag, picks up a rotated-in
kid on first sight (at most once per `MinRefreshInterval`), and fails closed
with `jwks.ErrUnavailable` when it has never fetched a set, or once the last
good set is older than `MaxStale` (1h). A document whose entries are all
unusable counts as a failed fetch, so the last good set stays in service
rather than every key being withdrawn at once. `jwks.ErrUnavailable` is the
server's fault, not the token's, so it matches no `ErrUnauthenticated`
sentinel. The three intervals must
nest, or construction fails:

```go
keys, err := jwks.NewRemote(jwks.RemoteConfig{
    URL: "https://issuer.example.com/.well-known/jwks.json",
})
verifier, err := limes.NewStandardVerifier(limes.VerifierConfig{
    Keys: keys, Revocations: revocations, TrustedIssuers: []string{"my-issuer"},
})
```

The issuer serves that document with `jwks.NewHandler` — `application/jwk-set+json`,
a strong ETag answering `If-None-Match` with 304, `Cache-Control: public,
max-age` (a negative `MaxAge` is refused) — over a function returning its keys, such as
`jwks.Static.Keys`:

```go
h, err := jwks.NewHandler(jwks.HandlerConfig{Keys: resolver.Keys, MaxAge: time.Minute})
mux.Handle("/.well-known/jwks.json", h)
```

`jwks.Marshal` writes the same document for a server of your own; `jwks.Parse`
reads one back, for a resolver of your own. It skips an entry it cannot use — a foreign
`kty` or `alg`, no kid, a malformed key — rather than refusing the set, so one bad
entry does not take every other key out of service mid-rotation. Two entries
naming one kid are both skipped: which one signs is not for the reader to
guess. A document that is not JSON, or names a member twice, is refused.

### Inspect a token without trusting it

`inspect.Decode` reads a JWT's claims, refusing only one that is malformed —
it checks no signature, time, audience or revocation; `inspect.VerifySignature`
checks only its signature against one key. They live in a package of their
own so they are never mistaken for the API that authorises. Both are for tooling — showing what a
token grants, or which key signed it. Neither applies expiry, audience or
revocation, so never authorise a request on either: that is `Verify`.

## Enforce the caveats on every operation

Verification proves the token is genuine; it does not know what the bearer is
about to do. `Authorize` asks the caveats for you; code that checks a second
operation under a capability it already verified asks them directly, through
the same one definition:

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
for an operation over a set, pass the prefix that bounds the set. A resource
with a `.` or `..` segment, plain or percent-encoded, is refused by a
resource-restricted capability and by `Validate`: what it names depends on how
you resolve it, not on its text. Pass the resource as your storage will
address it.
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
    Parent:  granted,                   // as the verifier accepted it
    Subject: limes.Principal{Type: limes.PrincipalAgent, Subject: "sub-worker"},
    TTL:     2 * time.Minute,
    Caveats: limes.Caveats{
        Ops:              []limes.Op{limes.OpGet},  // dropped OpShare
        ResourcePrefixes: []string{"corpus/public/2026/"},     // narrowed
        Requests:         limes.RequestCeiling{Max: 50},                         // 500 → 50
        Budget:           limes.BudgetCeiling{Max: limes.MustParseAmount("2.00")}, // 25.00 → 2.00
        UnitCode:         "USD",                               // the parent's ("" reads as USD)
    },
})
```

A ceiling's zero value allows nothing: a capability that should make requests
or spend names a `Max`, or `Unlimited: true` to lift the ceiling — so a ceiling
left out fails closed instead of opening up.

**The rule**: a child may only be narrower. Ask for an operation the parent
lacks, a wider scope or a bigger budget, and you get `ErrDelegationTooWide`; a
TTL that would outlive the parent is cut to the parent's expiry, and a child
starts no earlier than its parent (its `NotBefore`, else its issuance). Ask for a different `UnitCode` and you get
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
jkt, _ := dpop.Thumbprint(agentKey.Public()) // Ed25519 or P-256
cap, token, _ := issuer.Issue(ctx, limes.IssueRequest{ /* … */ ConfirmationJKT: jkt})

// Client: a fresh proof per request, signed over its method, URL and token.
proof, _ := dpop.Sign(agentKey, dpop.ProofRequest{Method: "POST", URL: url, Token: token, Now: time.Now()})

// Server: the verifier checks the proof of every key-bound capability.
replay, _ := dpop.NewMemoryReplayCache(dpop.MemoryReplayCacheConfig{})
proofs, _ := dpop.NewVerifier(dpop.VerifierConfig{Replay: replay})
verifier, _ := limes.NewStandardVerifier(limes.VerifierConfig{
    /* … */
    DPoP: proofs,
})
granted, err := verifier.Verify(ctx, limes.VerifyRequest{
    Token: token, Audience: "my-service",
    DPoP:  dpop.Proof{JWT: proof, Method: "POST", URL: url},
})
```

Behind several replicas, give the `dpop.Verifier` server nonces, with the same
key on every replica:

```go
nonces, err := dpop.NewHMACNonces(dpop.HMACNoncesConfig{Keys: [][]byte{key}}) // key ≥ 32 bytes
proofs, err := dpop.NewVerifier(dpop.VerifierConfig{Replay: replay, Nonces: nonces})
```

A proof must then carry a nonce the server issued, so it cannot be signed
ahead of time; a refusal with `dpop.ErrNonceRequired` is answered with
`verifier.DPoPNonce(ctx)` in the `DPoP-Nonce` header for the client to retry
with (RFC 9449 §8). The first key signs and every key checks, so rotate by
putting a new key first.

Present a token under the scheme its binding calls for: `ParseAuthorization`
reads `Authorization: DPoP <token>` or `Bearer <token>` (the scheme in any
case, the token as a token68), and with `VerifyRequest.Scheme` set, `Verify`
refuses a key-bound token presented as `Bearer` — a downgrade — or an unbound
one presented as `DPoP`, with `ErrAuthSchemeMismatch`. No header, or one
under another scheme, is `ErrNoCredentials` (401, a challenge with no error
code); a
Bearer or DPoP header whose credential is not a token68 is
`ErrMalformedAuthorization` (400).

A verifier with no `DPoP` refuses every key-bound capability with
`dpop.ErrRequired` instead of accepting it as a bearer token, so a binding
cannot be lost to a forgotten call. `dpop.Proof.URL` is the absolute URL the
client addressed, scheme and host included. Behind a proxy that rewrites the
host or strips a mount prefix, or for a server that sees only the path, set
`dpop.VerifierConfig.MatchPathOnly`: only the server's side of the comparison then
drops scheme, host and prefix — the proof's `htu` must still be an absolute
URI.

The token carries the thumbprint as `cnf.jkt` (RFC 7800), absent from an
unbound token. A delegated
child inherits its parent's binding or takes the sub-agent's own key, and
`Narrows` refuses a child that drops it. `Verify` refuses a proof from another
key, for another method (compared exactly: `post` is not `POST`), URL or token, outside `dpop.VerifierConfig.Window` (a
minute by default) of the verifier's clock, or seen before; every refusal
matches `ErrProofOfPossession` as `Verify` returns it, and still matches its
`dpop` sentinel. A replay cache or nonce store that cannot answer is an outage
instead, its own error, never a refused proof. `dpop.MemoryReplayCache` is in-process and bounded
— 100 000 ids, and `dpop.DefaultReplayEntriesPerKey` (1 000) per key, so one
key-bound token cannot fill it and lock the others out; full, it refuses
rather than forgets:
behind several replicas, a proof replayed to a different one inside the
window is caught only by server nonces or a `dpop.ReplayCache` over a shared
store.

## Narrow offline (Biscuit)

`Delegate` narrows a capability, but it needs the issuer. The Biscuit form
of the same capability (Biscuit v3) can be narrowed by whoever holds it, with
no key and no network, and a narrowing can never be undone:

```go
// granted: what the verifier accepted from the capability's JWT
full, _ := issuer.Biscuit(ctx, granted) // same ID, caveats and expiry

// An agent, offline, hands a worker a smaller token:
worker, _ := limes.Attenuate(full, limes.Attenuation{
    Ops:              []limes.Op{limes.OpGet},
    ResourcePrefixes: []string{"corpus/public/"},
    ExpiresAt:        time.Now().Add(10 * time.Minute),
})

// A verifier built with AcceptBiscuit: true returns the narrowed capability.
granted, err := verifier.Verify(ctx, limes.VerifyRequest{Token: worker, Audience: "my-service"})
```

An attenuation block may restrict operations, resources, planes, the expiry,
and (on an unbound token) bind a key. The verifier folds each block in
through `Narrows`, so an attenuated token is checked exactly like a
delegated one. A block that asks for more than the token has, or holds
anything else (Datalog rules and checks included), makes the whole token
invalid; a later `ExpiresAt` is ignored rather than refused. An attenuated copy spends its capability's budget and request
count, and revoking the capability revokes every copy.

A copy can also get limits of its own: `Requests` and `Budget` on the
`Attenuation` (nil sets none; a ceiling of 0 freezes the copy). Each is counted under the revocation id of the block that
set it, so siblings narrowed apart count apart, while the capability's own
limits still bound them all; a limit must fit every limit already in force.
The verifier hands them to the caller as `Capability.Copies`, and the `Meter`
counts them through the `MeterSubject` the verified capability gives, which
carries them. A verifier admits such a copy only with
`MeterCopies` set, which says the `Meter` behind it counts copies; without it
the token is refused with `ErrCopyCountersNotMetered` rather than accepted
with its limits unkept — a configuration error, answered as 500, not an
invalid token.
`verifier.BiscuitCopy` names the limits in force on a copy, on any verifier,
and a `CopyUsageReader` (`memstore` implements one) reads what each has
counted. A copy cannot delegate — a server-side child would be an ordinary
token of the capability, which revoking the copy would not reach and the
copy's limits would not bound — so `Delegate` refuses any capability read
from a Biscuit (`VerifiedCapability.FromBiscuit`) with
`ErrDelegationTooWide`; its holder attenuates it instead.

One copy can also be revoked on its own. `verifier.BiscuitCopy(ctx, token)`
checks the token is genuine — but not its expiry, audience or revocation, so a
copy can be revoked in any state — and names its capability and the revocation id of its last
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
    Subject: granted.MeterSubject(),
    Amount:  limes.MustParseAmount("0.35"), Op: "search", Actor: "orchestrator",
}, nil)
if errors.Is(err, limes.ErrBudgetExceeded) {
    // rejected at the auth boundary — before your business logic ran
}
```

The subject carries the capability's id, tenant, ceilings, unit and the
Biscuit copy's limits, all as the token states them: a request has no field
to type a ceiling into, or to leave at zero and so lift one.

Every ceiling is checked: each Biscuit copy's own, the capability's, each ancestor's, then the tenant aggregate. If **any**
rejects, no counter moves, so a retry after rejection is safe.

`receipt.ChargeID` names the ledger row. Refund against it:

```go
usage.Refund(ctx, limes.RefundRequest{ChargeID: receipt.ChargeID, Amount: limes.MustParseAmount("0.10")}) // partial
usage.Refund(ctx, limes.RefundRequest{ChargeID: receipt.ChargeID, Remaining: true})                       // the rest
```

A refund returns spend to every counter the charge took it from — except the
tenant aggregate when the charge fell in an accounting period since closed
with `ResetSpend` — and never more than the charge: `Amount: 0` refunds what
is left, so a retried full refund is a no-op, and more than is left is
`ErrRefundExceedsCharge`. `RefundReceipt.RefundedAmount` says what moved, and names
the charge's tenant and unit.

Every counter counts in one unit. A charge or reservation in another unit than
the capability, an ancestor or the tenant already counts in is
`ErrUnitCodeMismatch`, never a sum of two currencies; an empty `UnitCode`
means USD. The tenant aggregate counts from its first charge or reservation,
whether or not a ceiling is set: `SetTenantBudget` later measures the new
ceiling against that spend and those holds, and refuses a new `UnitCode`
while anything is held in the old one, or spent in it unless `ResetSpend`
closes that spend. It is guarded by
`ExpectedVersion` — 0 while no ceiling has been set, else the version read —
and a conflict is `ErrTenantBudgetVersionMismatch`. Until one is set the
tenant's ceiling reads `Unlimited`; a ceiling of 0 freezes its spend.

## Amounts are exact

Every amount is `limes.Nanos`: an `int64` count of billionths of the
unit, so 0.35 USD is `350_000_000`. Sums, ceilings, holds and refunds are
integer arithmetic, exact however many charges they take: a tenth and two
tenths fit under a ceiling of three tenths, and ten tenths make one. Nothing
on your side needs rounding, summing apart or correcting.

- **Writing one.** `ParseAmount("0.35")` reads a plain decimal exactly — no
  sign, so `-0` is refused too — and refuses anything finer than a nano
  rather than round it; `MustParseAmount` is the
  same for a literal in your code. A price that reaches you as a `float64`
  goes through `AmountFromFloat`, which rounds once, to the nearest nano.
- **Reading one.** `String()` writes the exact decimal (`"0.35"`);
  `Float64()` is for display and metrics only.
- **Range.** Up to `MaxNanos`, just under a billion units. A charge that
  would take a counter past it is refused with `ErrInvalidAmount`.
- **Units.** `UnitCode` is any ISO 4217 code in upper case (`IsUnitCode`);
  XXX, ISO 4217's code for "no currency", is the one for a budget that is not
  money.

In a token the budget is a decimal in a JSON string (`"max_budget": "1.5"`),
so a reader whose numbers are float64 — JavaScript's `JSON.parse` among them —
cannot round it.


## Reserve before a cost is known

An LLM call is priced by the tokens it ends up using. Hold an estimate first,
then settle the actual cost:

```go
subject := granted.MeterSubject()
r, err := usage.Reserve(ctx, limes.ReserveRequest{
    Subject: subject, Amount: limes.MustParseAmount("0.50"), TTL: 2 * time.Minute,
})
// … the call runs …
receipt, err := usage.Settle(ctx, limes.SettleRequest{
    Subject: subject, ReservationID: r.ID, Amount: actualCost,
}, nil)
```

A hold counts against every ceiling — the capability's, each ancestor's, the
tenant's — exactly as spend does, so two callers cannot both hold the last
budget. `Settle` charges the actual cost in place of the hold; above the hold,
the excess must fit, or the hold stays for you to settle lower or `Release`.
A hold never settled lapses after its TTL (`DefaultReservationTTL`, five
minutes, unless the request says): run `ReleaseExpired` on a timer, again
while a batch comes back full (`DefaultHousekeepingLimit` unless set). Settling
the zero reservation id, or a hold of another capability than the subject's,
is `ErrReservationNotFound`. The receipt names the
charge's tenant and unit, on a refusal too, so a caller holding only the
reservation id can attribute it.

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
with `Replayed` set and runs no callback. A name arriving again with a
different amount, unit or tenant is `ErrExternalRefConflict`: one name is one
cost.

```go
subject, err := limes.StoredMeterSubject(ctx, records, capID) // no token in hand
receipt, err := usage.Charge(ctx, limes.ChargeRequest{
    Subject:     subject,
    Amount:      cost,
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

A reporter with no token in hand takes the subject from the capability's
stored record (`StoredMeterSubject`, which reads it from the `Store`), as the meter
already reads every ancestor's ceiling. A price that is not known yet is not a price of zero: hold the estimate and
settle when it is known, or let the hold lapse and count against the ceilings
until `ReleaseExpired` runs.

## Observe the meter

`Instrument` wraps a `UsageStore` in OpenTelemetry instruments: charge and
settle attempts, reservations and request bumps counted by outcome
(`allowed`, `overrun`, `cap_exceeded`, `tenant_exceeded`, `limit_exceeded`,
`error`); amounts charged and refunded, and a capability's spend after each
charge, by `unit_code`. Instruments are created per store, and an error
creating them is returned:

```go
usage, err := limes.Instrument(store,
    limes.WithMeterProvider(provider), // default: the global provider
    limes.WithTenantAttribute(),       // opt-in: one series per tenant
)
```

A replayed charge or settle moved nothing and is not counted. `tenant_id` is
off by default because it multiplies every series by the number of tenants.

## Check your own store

Much of each contract lives in request fields and in which sentinel comes
back, so a store that gets one wrong still compiles. Run the module's checks
from your store's tests — `storetest` for a `Store` (and a
`BiscuitRevocationStore`), `metertest` for a `UsageStore`, `jwkstest` for a
`KeyResolver`, and `dpoptest` for a `dpop.ReplayCache` or `dpop.Nonces`:

```go
func TestMeterContract(t *testing.T) {
    metertest.Run(t, func(t *testing.T) metertest.Env[MyTx] {
        return metertest.Env[MyTx]{Ctx: ctx, Usage: newStore(t), Tenant: tenant,
            NewCapability: recordCapability,
            Advance: clock.Advance} // optional: runs the reservation-expiry checks
    })
}
```

`Ctx` is the context of every call for `Tenant`. A store that refers to its
tenants, or scopes each tenant's rows, also sets `NewTenant` (another tenant
it can charge), `TenantCtx` (the context a write for that tenant runs under)
and `ListCtx` (the context of `ListTenantBudgets`, a read across tenants).
Without them the checks that need a second tenant use a random id and `Ctx`.
`storetest.Env` takes the same `NewTenant` and `TenantCtx`, so one setup can
serve both suites.

The checks include charges racing one ceiling, so run them against the
database itself: a store that reads a counter and writes it back outside one
statement or lock overspends, and fails them.

`memstore` runs the same checks from its own tests, and `dpop` and `jwks` run
theirs against their shipped implementations.

`Instrument` wraps a `UsageStore` and keeps every interface of `limes` the
store implements — today the optional `CopyUsageReader`. A decorator of your
own should do the same: assert each optional interface on the store it wraps
and forward it, or a caller checking for it finds it gone.

## Revoke

```go
records.Revoke(ctx, limes.RevokeRequest{
    CapabilityID: cap.ID, Note: "agent looping", Actor: "operator@example.com",
    CascadeChildren: true,   // also records each sub-agent's revocation, for the audit
})
```

Idempotent; an id not on record is `ErrNotFound` and writes nothing.
Verifiers that cache revocation answers learn of it within the cache TTL
(`DefaultRevocationCacheTTL`, two seconds), or at once if you call `CachedRevocationChecker.Clear` when a
revocation is announced — on a Postgres `NOTIFY`, for example. `Clear`
drops the whole cache, and there is no per-id drop: an answer cached for a
descendant is also an answer about the revoked ancestor. The next check of
each id then asks the store once, so batch a burst of signals into one
`Clear`.
Revoking a capability revokes everything delegated from it, with or
without the flag: `IsRevoked` answers for the whole chain. `CascadeChildren`
also writes a revocation entry per descendant, so the audit trail names each
capability that was stopped. A chain is at most `MaxDelegationDepth` (64)
ancestors deep: `Delegate` refuses to go further with `ErrDelegationTooDeep`,
and a store answers a deeper chain with that error — read as revoked — rather
than walk part of it and miss a revocation at its root.

Records do not stay forever. A housekeeping job calls
`Store.Purge(ctx, limes.PurgeRequest{ExpiredFor: retention})` on a timer, and
again while a batch comes back full (`Limit`, `DefaultPurgeLimit` unless
set): it removes the capabilities expired for longer than the retention, with
their revocation entries, then `UsageHousekeeping.PurgeOrphans` releases what
the meter still holds for them. A child never outlives its parent, so no
ancestor of a live capability is ever purged. The retention is at least
`MinPurgeRetention` (six minutes, the zero value's): a verifier accepts a
token for up to its `Leeway` past expiry, never more than `MaxLeeway`, by a
clock that may run up to `PurgeClockSkew` behind the store's, and a
revocation purged inside that window would let the revoked token through.

## Bring your own storage

Implement these and you are done:

| Interface | Stores | Transactions needed? |
|---|---|---|
| `Store` = `IssuanceStore` + admin | Capability records + revocations; an `Issuer` needs only `IssuanceStore` (`Insert`, `IsRevoked`) | No |
| `BiscuitRevocationStore` | Revoked Biscuit copies (only if you accept Biscuits) | No |
| `CopyUsageReader` | Reads Biscuit copies' own counters (only to show them) | No |
| `UsageStore[TX]` = `Meter[TX]` + `MeterReader` + `TenantBudgets` + `UsageHousekeeping` | Request and spend counters, the charges ledger, tenant ceilings; `Delete` and `PurgeOrphans` release a reaped capability's holds | Only for atomic side effects |
| `KeyResolver` | Public verification keys | No |
| `dpop.ReplayCache` | DPoP proof ids seen, per key: `Seen(ctx, dpop.ReplayEntry)` reports a key past its share as seen, and returns an error when its store cannot answer — an outage, not a refused proof (only for key-bound tokens; `dpop.MemoryReplayCache` ships for one process — behind several replicas, implement it over a shared store) | No |
| `dpop.Nonces` | Server-provided DPoP nonces (optional; `dpop.HMACNonces` ships and needs no store) | No |

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

Every input is a `…Request`, every list a `Page`, and every read names what
it reads (`GetRecord`, `GetUsage`, `GetTenantBudget`), so one type may implement `Store` and
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
   served JWKS — `jwks.Marshal` output is sorted by kid, so it is stable).
2. Switch the issuer to sign with the new key — no sooner than the
   `max-age` the JWKS is served with, so every verifier has fetched it.
3. Withdraw the old key (`RemoveKey`) only after the **longest outstanding
   TTL** has elapsed.

Skipping the wait in step 3 invalidates tokens agents are still holding. Since
TTLs here are minutes, the wait is short — which is part of why short TTLs are
the default posture (`IssuerConfig.DefaultTTL`, fifteen minutes).

Load a signing key with `LoadKey(path, kid)` (PEM, PKCS#8 only); an
unreadable path, or none, is an error, never a fresh key. `GenerateKey` makes
an ephemeral one, marked `SigningKey.Generated` so you can log it loudly — a
deployment that may run without a key file chooses between the two itself. A
key with no kid named gets `jwks.KeyID`'s: its RFC 7638 thumbprint, the same
wherever it is derived.

`NewSigner(limes.SignerConfig{KeyID: kid, Key: key})` signs through any
`crypto.Signer` whose key is Ed25519 — an `ed25519.PrivateKey`, or a key held
in a KMS or an HSM; the token format stays inside the module. A key that
implements `ContextSigner` signs under the request's context, so a slow or
cancelled call is bounded by the request that asked for the token.

## Answer a refusal

`ErrorResponse` turns any error `Verify`, `Authorize` or the `Meter` returns
into the response RFC 6750, RFC 9449 and RFC 9457 describe, and
`WriteErrorResponse` writes it:

```go
if err != nil {
    limes.WriteErrorResponse(w, verifier.ErrorResponse(ctx, err, scheme))
    return
}
```

A malformed `Authorization` header is 400; a bad token or proof is 401, with a
`Bearer` or `DPoP` challenge naming the error code (and `algs` for DPoP); a
refused operation is 403 `insufficient_scope`; an exhausted budget or request
limit is 403 with no challenge; an issuer's key set never fetched, or out of
reach past `MaxStale`, is 503 — the server's trouble, not the token's; anything else, a
store outage included, is 500. A refusal for want of a nonce carries a fresh one in `DPoP-Nonce`. The
body is `application/problem+json` with a type per kind of refusal and fixed
text — the error's own message, which can name kids or stores, stays in your
log. The full table is in [diagrams.md](diagrams.md#answering-a-refusal).

## Defaults

| Setting | Default |
|---|---|
| `IssuerConfig.DefaultTTL` | `DefaultIssueTTL`, 15 minutes |
| `VerifierConfig.Leeway` | `DefaultLeeway`, 30 seconds |
| `VerifierConfig.MaxTokenBytes` | `DefaultMaxTokenBytes`, 16 KiB |
| `RevocationCacheConfig` · `BiscuitRevocationCacheConfig` | `DefaultRevocationCacheTTL` 2 seconds · `DefaultRevocationCacheEntries` 100 000, the oldest dropped first past it; a negative TTL or bound is refused — for no cache, give the verifier the store itself |
| `jwks.RemoteConfig` | `jwks.DefaultFetchTimeout` 10s · `DefaultRefreshInterval` 5m · `DefaultMinRefreshInterval` 10s · `DefaultMaxStale` 1h · `DefaultMaxDocumentBytes` 1 MiB |
| `dpop.VerifierConfig.Window` | `dpop.DefaultWindow`, 1 minute |
| `dpop.MemoryReplayCacheConfig` | `dpop.DefaultReplayCacheEntries`, 100 000 ids; `dpop.DefaultReplayEntriesPerKey`, 1 000, per key |
| `dpop.HMACNoncesConfig` | `dpop.DefaultNonceLifetime`, 5 minutes; `dpop.HMACNonceClockSkew`, 5 seconds, for a nonce issued ahead of this replica's clock; keys of at least `dpop.HMACNonceKeyBytes`, 32 |
| `DefaultReservationTTL` | 5 minutes |
| `MaxExternalRefBytes` | 200 |
| `MaxNanos` | just under a billion units |
| `MaxIJSONInteger` | 2^53 − 1, for every integer in a token |

