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
// "answer 401". ErrMalformedAuthorization does not: it is a bad request,
// and matches ErrInvalidRequest.
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
default `limes-cap-v6+jwt`, the one format the package reads), a tenant and a principal of a
known kind (`PrincipalTypeUser`, `PrincipalTypeAgent` or `PrincipalTypeService`; agent
details only on an agent — `Principal.Validate`, which `Issue` and `Delegate`
run too; an agent's `AgentPrincipal` — `AgentType`, `AgentVersion`, `RunID`,
`ParentRunID`, `Model`, `MCPClient` — is signed into the token for audit and
lineage, and none of it is a caveat), its claims must
hold no member the verifier does not know (a caveat it could not enforce),
none twice and none in another case, and may be at most
`VerifierConfig.MaxTokenBytes` long (16 KiB unless set). When several issuers share a verifier, set
`KeyIssuers` (kid → issuer) so one issuer's key cannot sign for another —
or, for issuers that do share one trust boundary, `SharedKeys: true`; a
verifier trusting several issuers with neither is refused — an empty map
binds nothing, the same as none — and so is one setting both.
Verifiers running apart from the issuer resolve keys with
`jwks.Remote`. It fetches the issuer's JWKS on first use, keeps it as
long as the response's `Cache-Control` allows within `MinRefreshInterval`
(10s) and `RefreshInterval` (5m), revalidates by ETag, picks up a rotated-in
kid on first sight (at most once per `MinRefreshInterval`). A known key from
a set past its freshness but within `MaxStale` is served at once and the set
refreshed behind it, so a slow endpoint delays no request. A fetch runs apart
from the caller, bounded by `jwks.DefaultFetchTimeout`; a caller that gives up
waiting with no usable set gets `jwks.ErrUnavailable`, and so does a kid the
held set lacks while the endpoint is failing — it may be one rotated in since,
so it is not refused as unknown. It fails closed
with `jwks.ErrUnavailable` when it has never fetched a set, or once the last
good set is older than `MaxStale` (1h). A document whose entries are all
unusable counts as a failed fetch, so the last good set stays in service
rather than every key being withdrawn at once. `jwks.ErrUnavailable` is the
server's fault, not the token's, so it matches no `ErrUnauthenticated`
sentinel. The URL must be `https`, and a redirect to another scheme or host
is not followed: whoever answers the fetch chooses the keys every token is
checked against. The three intervals must
nest, or construction fails:

```go
keys, err := jwks.NewRemote(jwks.RemoteConfig{
    URL: "https://issuer.example.com/.well-known/jwks.json",
})
verifier, err := limes.NewVerifier(limes.VerifierConfig{
    Keys: keys, Revocations: revocations, TrustedIssuers: []string{"my-issuer"},
})
```

The issuer serves that document with `jwks.NewHandler` — `application/jwk-set+json`,
a strong ETag answering `If-None-Match` with 304, `Cache-Control: public,
max-age` in whole seconds (`DefaultMaxAge` unless `MaxAge` says otherwise;
`Revalidate` serves `max-age=0` and excludes a `MaxAge`; a negative `MaxAge`,
or one under a second, is refused) — over a function returning its keys, such as
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

`inspect.Decode` reads a JWT's claims, refusing only one that is malformed or
longer than `inspect.MaxTokenBytes` —
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
if err := cap.Caveats.CheckSource(clientAddr); err != nil { /* per connection: limes.ErrSourceIPNotAllowed */ }

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
with a `.` or `..` segment, plain or percent-encoded once or twice, is refused by a
resource-restricted capability and by `Validate`: what it names depends on how
you resolve it, not on its text. Pass the resource as your storage will
address it.
`AllowTaintedRead` can only act on a taint signal you supply; if you track
none, it protects nothing, and you should say so.

The built-in operations are the ones every resource server shares: `get`,
`put`, `list`, `delete`, `share` and `manage`. Your own operations — searching
an index, computing embeddings — are namespaced (`tool:retrieve`, `mcp:github/create_issue`)
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
delegated, err := issuer.Delegate(ctx, limes.DelegateRequest{ // .Capability, .Token
    Parent:  granted,                   // as the verifier accepted it
    Subject: limes.Principal{Type: limes.PrincipalTypeAgent, Subject: "sub-worker"},
    TTL:     2 * time.Minute,
    Caveats: limes.Caveats{
        Ops:              []limes.Op{limes.OpGet},  // dropped OpShare
        ResourcePrefixes: []string{"corpus/public/2026/"},     // narrowed
        Requests:         limes.RequestCeiling{Max: 50},                         // 500 → 50
        Budget:           limes.BudgetCeiling{Max: limes.MustParseAmount("2.00")}, // 25.00 → 2.00, in the parent's USD
    },
})
```

A ceiling's zero value allows nothing: a capability that should make requests
or spend names a `Max`, or `Unlimited: true` to lift the ceiling, and so does
one whose Biscuit copies should carry limits of their own (`Copies`, a
`CopyQuota`) — so a ceiling left out fails closed instead of opening up.

**The rule**: only a parent granted `OpShare` may delegate — without it,
`ErrOpNotAllowed` — and a child may only be narrower. `Issuer.Biscuit`
requires `OpShare` too: whoever holds a Biscuit form attenuates it and hands
it on offline, so a capability that must not be passed on gets none. Ask for an operation the parent
lacks, a wider scope, a bigger budget, a larger copy quota, a wider audience or another tenant —
or drop the parent's key binding or `IdempotencyKeyRequired`, or grant an
`AllowTaintedRead` it lacks — and you get `ErrDelegationTooWide`; a
TTL that would outlive the parent is cut to the parent's expiry, and a child
starts no earlier than its parent (its `NotBefore`, else its issuance). Ask for a budget in a different `UnitCode` and
you get `ErrUnitCodeMismatch` — units are never converted; name none and the
child takes its parent's. Each child resource must be
reachable by the parent (a parent pinned to exact URIs admits no child
prefix), and each child network must lie inside a parent network.

To hand a child exactly the parent's caveats, say so with
`InheritCaveats: true`; caveats without any `Ops` are rejected rather than
silently replaced by the parent's. A parent that has expired (`ErrExpired`),
that is revoked, itself or any ancestor (`ErrRevoked`), or whose issuer is not
this one (`ErrInvalidRequest`: its `iss` is not `IssuerConfig.IssuerName`)
cannot delegate; neither can one read from a Biscuit copy, whose holder
attenuates it instead.

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
issued, _ := issuer.Issue(ctx, limes.IssueRequest{ /* … */ ConfirmationJKT: jkt})
token := issued.Token

// Client: a fresh proof per request, signed over its method, URL and token.
proof, _ := dpop.Sign(ctx, agentKey, dpop.SignRequest{Method: "POST", URL: url, Token: token, IssuedAt: time.Now()})

// Server: the verifier checks the proof of every key-bound capability.
replay, _ := dpop.NewMemoryReplayCache(dpop.MemoryReplayCacheConfig{})
proofs, _ := dpop.NewVerifier(dpop.VerifierConfig{Replay: replay})
verifier, _ := limes.NewVerifier(limes.VerifierConfig{
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
nonces, err := dpop.NewHMACNonces(dpop.HMACNoncesConfig{Keys: dpop.SealNonceKeys(key)}) // key ≥ 32 bytes
proofs, err := dpop.NewVerifier(dpop.VerifierConfig{Replay: replay, Nonces: nonces})
```

A proof must then carry a nonce the server issued, so it cannot be signed
ahead of time; a refusal with `dpop.ErrNonceRequired` carries a fresh nonce in
the `DPoP-Nonce` header for the client to retry with (RFC 9449 §8) —
`ErrorResponse` sets it, or call `verifier.DPoPNonce(ctx)` yourself. The first key signs and every key checks, so rotate by
putting a new key first.

Present a token under the scheme its binding calls for: `ParseAuthorization`
reads `Authorization: DPoP <token>` or `Bearer <token>` (the scheme in any
case, the token as a token68), and with `VerifyRequest.Scheme` set, `Verify`
refuses a key-bound token presented as `Bearer` — a downgrade — or an unbound
one presented as `DPoP`, with `ErrAuthSchemeMismatch`. No header, or one
under another scheme, is `ErrNoCredentials` (401, a challenge with no error
code); a
Bearer or DPoP header whose credential is not a token68 is
`ErrMalformedAuthorization` (400), which matches `ErrInvalidRequest` as
every malformed request does — though `ErrorResponse` answers the rest of
that family 500, as the server's own call. A token that came from no `Authorization`
header — gRPC metadata, a message field — goes in with `AuthSchemeUnspecified`,
the zero `Scheme`: no scheme is checked, but a key-bound token still needs its
proof.

A verifier with no `DPoP` refuses every key-bound capability with
`ErrProofUnsupported` instead of accepting it as a bearer token, so a binding
cannot be lost to a forgotten call. It is the deployment's fault, not the
request's: it matches no `ErrUnauthenticated` sentinel and is answered 500. `dpop.Proof.URL` is the absolute URL the
client addressed, scheme and host included. Behind a proxy that rewrites the
host or strips a mount prefix, or for a server that sees only the path, set
`dpop.VerifierConfig.MatchPathOnly`: only the server's side of the comparison then
drops scheme, host and prefix — the proof's `htu` must still be an absolute
URI, and a request to the root path `/` must match it exactly.

The token carries the thumbprint as `cnf.jkt` (RFC 7800), absent from an
unbound token. A delegated
child inherits its parent's binding or takes the sub-agent's own key, and
`Narrows` refuses a child that drops it. `Verify` refuses a proof from another
key, for another method (compared exactly: `post` is not `POST`), URL or token, outside `dpop.VerifierConfig.Window` (a
minute by default) of the verifier's clock, or seen before; every refusal
matches `ErrProofOfPossession` as `Verify` returns it, and still matches its
`dpop` sentinel. A replay cache or nonce store that cannot answer is an outage
instead, its own error, never a refused proof. `dpop.MemoryReplayCache` is in-process and bounded
— 100 000 ids, and `dpop.DefaultReplayEntriesPerHolder` (1 000) per
capability, so one capability, whatever keys its copies are bound to, cannot
fill it and lock the others out; full, it refuses
rather than forgets:
behind several replicas, a proof replayed to a different one inside the
window is caught only by server nonces or a `dpop.ReplayCache` over a shared
store. The `redisreplay` module is one, over Redis, a module of its own so
limes never resolves a Redis client:

```go
// go get github.com/oleg-tkachuk/limes/redisreplay
replay, err := redisreplay.New(redisreplay.Config{Client: rdb}) // any redis.UniversalClient
```

It keeps one sorted set per capability, so one call is one atomic script on
one key, Redis Cluster included, and it decides what is live by the
verifier's clock, never the server's. Run it with `maxmemory-policy
noeviction`: full, Redis refuses the write and the proof is refused, where an
eviction policy would forget a live id. Redis replicates asynchronously, so
a failover can lose ids the old primary acknowledged and a proof can then be
replayed once inside its window; where that matters, keep `Window` short or
require server nonces.

## Narrow offline (Biscuit)

`Delegate` narrows a capability, but it needs the issuer. The Biscuit form
of the same capability (Biscuit v3) can be narrowed by whoever holds it, with
no key and no network, and a narrowing can never be undone. An agent in
another language narrows it with any Biscuit library, writing the facts
[biscuit-profile.md](biscuit-profile.md) lists:

```go
// granted: what the verifier accepted from the capability's JWT
full, _ := issuer.Biscuit(ctx, limes.BiscuitRequest{Capability: granted}) // same ID, caveats and expiry

// An agent, offline, hands a worker a smaller token:
worker, _ := limes.Attenuate(full.Token, limes.Attenuation{
    Ops:              []limes.Op{limes.OpGet},
    ResourcePrefixes: []string{"corpus/public/"},
    ExpiresAt:        time.Now().Add(10 * time.Minute),
})

// A verifier built with a VerifierConfig.Biscuit returns the narrowed capability.
granted, err := verifier.Verify(ctx, limes.VerifyRequest{Token: worker, Audience: "my-service"})
```

An attenuation block may restrict operations, resources, the audience, the expiry,
and (on an unbound token) bind a key. The verifier folds each block in
through `Narrows`, so an attenuated token is checked exactly like a
delegated one. A block that asks for more than the token has, or holds
anything else (Datalog rules and checks included), makes the whole token
invalid — `ErrBiscuitAttenuation`, which matches `ErrInvalidToken`; a later
`ExpiresAt` is ignored rather than refused. An attenuated copy spends its capability's budget and request
count, and revoking the capability revokes every copy.

A copy can also get limits of its own: `Requests` and `Budget` on the
`Attenuation` (nil sets none; a ceiling of 0 freezes the copy). A copy's
`Budget` names no `UnitCode`: it is in its capability's unit, which the block
does not carry, and the verifier gives it. Each is counted under its capability and the revocation id of the block that
set it, so siblings narrowed apart count apart, while the capability's own
limits still bound them all; a limit must fit every limit already in force,
and an `Unlimited` one is refused: a copy only narrows.
The verifier hands them to the caller as `VerifiedCapability.Copies`, and the `Meter`
counts them through the `MeterSubject` the verified capability gives, which
carries them. A verifier admits such a copy only with
`BiscuitConfig.MeterCopies` set, which says the `Meter` behind it counts copies; without it
the token is refused with `ErrCopyCountersNotMetered` rather than accepted
with its limits unkept — a configuration error, answered as 500, not an
invalid token, and returned only once the token has passed every other
check.
`verifier.BiscuitCopy` names the limits in force on a copy, on any verifier,
and a `CopyUsageReader` (`memstore` and `pgstore` implement one) reads what each has
counted: `CopyUsage` takes a `CopyUsageRequest` naming a capability and up to
`MaxCopyUsageIDs` revocation ids of its copies and returns, in a
`CopyUsageResult`, the counters of those used; a copy never used is absent. A holder mints copies offline, as many
as it likes, so the issuer bounds how many a meter counts for the capability
— `Caveats.Copies`, a `CopyQuota` signed into the token like the request and
budget ceilings: its zero value counts no copy with limits of its own,
`CopyQuota{Unlimited: true}` as many as the store allows, and a delegated
child counts no more than its parent. It bounds the copies of the
capability and of everything delegated from it together, as the request and
budget ceilings bound their requests and spend, so delegating does not
multiply it, and a child's copies stay counted once its usage is dropped. A
store counts no more than its own bound either —
`memstore.UsageStoreConfig.MaxCopiesPerCapability` or
`pgstore.Config.MaxCopiesPerCapability`, `DefaultMaxCopiesPerCapability`
(1 024) when unset. `Bump`, `Charge` and `Reserve` refuse a call that would
take the capability's own copies past that bound, or the copies under any
capability on the chain past its quota, with `ErrCopyLimitExceeded`, a kind
of `ErrRequestLimitExceeded`, counting nothing. A copy already counted goes on
being metered. It stays counted under every ancestor once its own
capability's usage is dropped (`DeleteUsage`, `PurgeOrphans`), as its requests
and spend do; dropping a capability's own usage restarts its count of copies
with its other counters.
A revocation id alone does not name a copy: biscuit-go signs a block without
the signature before it, so a holder of two Biscuits can graft a block of one
onto the other, and the same id then turns up under two capabilities. Each
counts it apart, and neither's metering, refund or deletion reaches the
other's. A copy cannot delegate — a server-side child would be an ordinary
token of the capability, which revoking the copy would not reach and the
copy's limits would not bound — so `Delegate` refuses any capability read
from a Biscuit (`VerifiedCapability.FromBiscuit`) with
`ErrInvalidRequest`; its holder attenuates it instead.

One copy can also be revoked on its own. `verifier.BiscuitCopy(ctx, token)`
checks the token is genuine — but not its expiry, audience or revocation, so a
copy can be revoked in any state — and returns the copy: its capability and
the revocation id of its last block, which only it pairs, so a caller cannot
name another tenant's copy under its own capability. Passing it to
`BiscuitRevocationStore.RevokeBiscuit` (`RevokeBiscuitRequest{Copy: …}`) stops
that copy and every copy attenuated from it, and leaves the copy it came from,
its siblings and the capability's JWT working. `GetBiscuitRevocation` reads
it back by the same copy. The revocation belongs to that capability and goes
only with its record, while a token carrying the id is refused under any
capability. A verifier that accepts
Biscuits needs `BiscuitConfig.Revocations`: pass the same `RevocationCache`
as `VerifierConfig.Revocations`, built with the store as
`RevocationCacheConfig.Biscuits` too, so one `Clear` reaches both kinds of
answer. `NewVerifier` refuses a cache built without it.

The Biscuit seals an ordinary signed token, so a KMS-held signing key works.
That token carries the root of the Biscuit's signature chain, whose private
half is discarded once the Biscuit is built. A sealed token presented on its
own is refused, so it cannot be lifted out to shed an attenuation, and it
cannot be re-wrapped in a fresh Biscuit.

## Charge against the budget

```go
receipt, err := usage.Charge(ctx, meter.ChargeRequest{
    Subject: granted.MeterSubject(),
    Amount:  limes.MustParseAmount("0.35"), Op: "index:search", Actor: "orchestrator",
}, nil)
if errors.Is(err, limes.ErrBudgetExceeded) {
    // rejected at the auth boundary — before your business logic ran
}
```

The subject carries the capability's id, tenant, ceilings, quota of copies,
unit and the Biscuit copy's limits, all as the token states them: a request has no field
to type a ceiling into, or to leave at zero and so lift one.

Every ceiling is checked: each Biscuit copy's own, the capability's, each ancestor's, then the tenant aggregate. If **any**
rejects, no counter moves, so a retry after rejection is safe. A copy's,
the capability's or an ancestor's ceiling refuses as `ErrBudgetExceeded`, the tenant's as
`ErrTenantBudgetExceeded`. Requests are counted apart, by `Meter.Bump` with
the same subject, against the request ceilings of the same copies, capability and ancestors; past one
it is `ErrRequestLimitExceeded`.

`receipt.ChargeID` names the ledger row. Refund against it:

```go
usage.Refund(ctx, meter.RefundRequest{ChargeID: receipt.ChargeID, Amount: limes.MustParseAmount("0.10")}) // partial
usage.Refund(ctx, meter.RefundRequest{ChargeID: receipt.ChargeID, Remaining: true})                       // the rest
```

A refund returns spend to every counter the charge took it from — except the
tenant aggregate when the charge fell in an accounting period since closed
with `ResetSpend` — and never more than the charge: `Remaining: true` refunds
what is left, so a retried full refund is a no-op; a request with neither an
`Amount` nor `Remaining`, or both, is `ErrInvalidRequest`, and more than is
left is `ErrRefundExceedsCharge`. `RefundReceipt.Amount` says what moved, and names
the charge's tenant and unit.

Every counter counts in one unit. A charge or reservation in another unit than
the capability, an ancestor or the tenant already counts in is
`ErrUnitCodeMismatch`, never a sum of two currencies. The unit is part of
the ceiling, as a currency is part of an amount: a `BudgetCeiling` that lets
anything be spent — a `Max` above zero, or `Unlimited` — names its
`UnitCode`, and one that does not is `ErrInvalidRequest` (`ErrInvalidCaveats`
in caveats). Only the zero ceiling, which spends nothing, may name none: such
a capability counts requests (`Bump`) but no amount — its `Charge`, `Reserve`
and `Settle` are `ErrInvalidRequest`. A tenant ceiling names its unit the same
way; the zero one, a freeze, keeps the unit the tenant counts in. The tenant
aggregate counts from its first charge or reservation, in that charge's unit,
whether or not a ceiling is set: `SetTenantBudget` later measures the new
ceiling against that spend and those holds, and refuses a ceiling in another
unit while anything is held in the old one, or spent in it unless `ResetSpend`
closes that spend. It is guarded by
`ExpectedVersion` — 0 while no ceiling has been set, else the version read —
and a conflict is `ErrTenantBudgetVersionMismatch`. Until one is set the
tenant's ceiling reads `Unlimited`; a ceiling of 0 freezes its spend.

To find the tenants near their ceiling, `ListTenantBudgets` takes a `Filter`:
`TenantBudgetFilterAll` (the default), `TenantBudgetFilterUnlimited`, or
`TenantBudgetFilterAtOrPast` with a `ThresholdBasisPoints` of
0..`BasisPointsPerWhole` — hundredths of a percent, so 110 is 1.1% — compared
in integers, without rounding.
Rows come most at risk first: ceilings of 0, then bounded ceilings by spent /
max descending, unclamped, then unlimited ones; ties by tenant id.
`TenantBudget.Utilization()` reports the same percentage clamped to 0..100,
and `limited` false for an unlimited tenant. A tenant never charged and never
given a ceiling has no row: `GetTenantBudget` is `ErrTenantBudgetNotFound`.

## Amounts are exact

Every amount is `limes.Nanos`: an `int64` count of billionths of the
unit, so 0.35 USD is `350_000_000`. Sums, ceilings, holds and refunds are
integer arithmetic, exact however many charges they take: a tenth and two
tenths fit under a ceiling of three tenths, and ten tenths make one. Nothing
on your side needs rounding, summing apart or correcting.

- **Writing one.** `ParseAmount("0.35")` reads a decimal exactly — digits, an
  optional fraction and an optional exponent (`1e-7`), no sign, so `-0` is refused too — and refuses anything finer than a nano
  rather than round it; `MustParseAmount` is the
  same for a literal in your code. A price that reaches you as a `float64`
  goes through `AmountFromFloat`, which rounds once, to the nearest nano.
- **Reading one.** `String()` writes the exact decimal (`"0.35"`);
  `Float64()` is for display and metrics only.
- **In configuration.** `Nanos` is a `TextMarshaler` and `TextUnmarshaler`
  that write and read the same exact decimal, so a YAML scalar or an
  environment variable carries an amount with no `float64` in between.
- **Range.** Up to `MaxNanos`, just under a billion units. An amount outside
  it is `ErrInvalidAmount`, which matches `ErrInvalidRequest`; a charge of a
  valid amount that would take a counter past it is `ErrCounterOverflow`.
- **Units.** `BudgetCeiling.UnitCode` is any ISO 4217 code in upper case (`IsUnitCode`);
  XXX (`AbstractUnitCode`), ISO 4217's code for "no currency", is the one for
  a budget that is not money.

In a token the budget is a decimal in a JSON string (`"max_budget": "1.5"`),
so a reader whose numbers are float64 — JavaScript's `JSON.parse` among them —
cannot round it.


## Reserve before a cost is known

An LLM call is priced by the tokens it ends up using. Hold an estimate first,
then settle the actual cost:

```go
subject := granted.MeterSubject()
r, err := usage.Reserve(ctx, meter.ReserveRequest{
    Subject: subject, Amount: limes.MustParseAmount("0.50"), TTL: 2 * time.Minute,
})
// … the call runs …
receipt, err := usage.Settle(ctx, meter.SettleRequest{
    Subject: subject, ReservationID: r.ID, Amount: actualCost,
}, nil)
```

A hold counts against every ceiling — each presented Biscuit copy's own
budget, the capability's, each ancestor's, the tenant's — exactly as spend does, so two callers cannot both hold the last
budget. `Settle` charges the actual cost in place of the hold; above the hold,
the excess must fit, or the hold stays for you to settle lower or `Release`.
A hold never settled lapses after its TTL (`DefaultReservationTTL`, five
minutes, unless the request says), or when its capability expires, if that
comes sooner. Revoking the capability does not end it: the cost may already be
incurred, and it can still be settled until then. Run `ReleaseExpired` on a timer, again
while a batch comes back full (`DefaultHousekeepingLimit` unless set). Settling
the zero reservation id, a hold of another capability than the subject's, or
one made under other Biscuit copies than the subject carries, is
`ErrReservationNotFound`: a hold is settled by the copy that made it. The receipt names the
charge's tenant and unit, on a refusal too, so a caller can attribute it from
the receipt alone. `GetReservation` reads a hold back while it
still counts — open, or expired and not yet released — and is
`ErrReservationNotFound` once settled or released; `ListReservations` pages a
capability's own holds.

The trailing `nil` is an optional in-transaction callback. If your store has
transactions, pass a function: its context carries the store's transaction,
which the store's own accessor reads, so your side effects commit atomically
with the spend. It also hands over the receipt the call will return, so the
side effect can name the charge:

```go
usage.Charge(ctx, req, func(ctx context.Context, c meter.Charged) error {
    tx, _ := pgstore.TxFrom(ctx) // commits with the charge
    return myOutbox.Enqueue(ctx, tx, c.Receipt.ChargeID, c.Receipt.SpentAmount)
})
```

The ledger row is written after the callback returns, so the callback cannot
read it back yet; the receipt is what it has.

`Settle` takes the same callback. It must not call back into the `Meter`: a
store may hold its counters while the callback runs. The module never names
the transaction's type — the store puts it in the callback's context and its
own accessor reads it — which is why this library has no database dependency.

## Charge a cost reported after the fact

Some costs never pass through your verifier: a call made straight to a
provider whose price arrives later, in a usage record or on an event stream
delivered at least once. Two things change for such a cost.

**It may arrive twice.** Name it with the id your records already give it, and
the capability is charged once per name; a repeat returns the first receipt
with `Replayed` set and runs no callback. A name arriving again with a
different amount, unit or tenant, or under another Biscuit copy, is
`ErrExternalRefConflict`: one name is one cost.

```go
subject, err := limes.StoredMeterSubject(ctx, records, capID) // no token in hand
receipt, err := usage.Charge(ctx, meter.ChargeRequest{
    Subject:     subject,
    Amount:      cost,
    ExternalRef: event.CallID,
    Overrun:     meter.OverrunPolicyRecord,
}, nil)
```

A settle is idempotent on its reservation in the same way: settling one twice
for the same amount returns the first charge, and for another amount is
`ErrSettleConflict`. A repeat is answered even once the capability's
record is purged. A new charge is not: the `Meter` refuses a capability not on
record with `ErrNotFound`, since no ancestor's ceiling could be read — so keep
records (`PurgeRequest.ExpiredFor`) as long as costs may still arrive. `ChargeByRef` reads a named charge back, so a
reporter can reconcile its own records with the ledger.

**It has already been spent.** Refusing it would only leave the ledger short.
`OverrunPolicyRecord` charges it past every ceiling it crosses and sets
`receipt.Overrun`; the crossed ceiling then refuses every later charge and
reservation made with the default `OverrunPolicyReject`, which is what stops the
next cost before it is incurred. Keep `OverrunPolicyReject` for anything you can
still decline.

A reporter with no token in hand takes the subject from the capability's
stored record (`StoredMeterSubject`, which reads it through a `RecordReader`, as any `Store` is one), as the meter
already reads every ancestor's ceiling. A price that is not known yet is not a price of zero: hold the estimate and
settle when it is known, or let the hold lapse and count against the ceilings
until `ReleaseExpired` runs.

## Observe the meter

`otelmetric.Instrument`, from the nested module
`github.com/oleg-tkachuk/limes/otelmetric`, wraps a `UsageStore` in
OpenTelemetry instruments — a module of its own, so limes itself resolves no
OpenTelemetry dependency: charge and
settle attempts, reservations and request bumps counted by outcome
(`allowed`, `overrun`, `budget_exceeded`, `tenant_budget_exceeded`,
`request_limit_exceeded` — the refusals named as their `Reason` — and
`error`); amounts charged and refunded, and a capability's spend after each
charge, by `unit_code`. Instruments are created per store, and an error
creating them is returned:

```go
usage, err := otelmetric.Instrument(store,
    otelmetric.WithMeterProvider(provider), // default: the global provider
    otelmetric.WithTenantAttribute(),       // opt-in: one series per tenant
)
```

A replayed charge or settle moved nothing and is not counted. `tenant_id` is
off by default because it multiplies every series by the number of tenants;
with `WithTenantAttribute`, every measurement — a refused charge or settle
included — carries the subject's tenant.

## Check your own store

Much of each contract lives in request fields and in which sentinel comes
back, so a store that gets one wrong still compiles. Run the module's checks
from your store's tests — `storetest` for a `Store` (and a
`BiscuitRevocationStore`), `metertest` for a `UsageStore`, `jwkstest` for a
`KeyResolver`, and `dpoptest` for a `dpop.ReplayCache` or `dpop.Nonces`:

```go
func TestMeterContract(t *testing.T) {
    metertest.Run(t, func(t *testing.T) metertest.Env {
        return metertest.Env{Ctx: ctx, Usage: newStore(t), Tenant: tenant,
            NewCapability: recordCapability,
            Advance: clock.Advance} // optional: reservation-expiry checks, and with Ledger and Now the ledger's bucket checks
    })
}
```

`Ctx` is the context of every call for `Tenant`. `NewCapability` records each
spec's ceilings and its quota of copies (`CapabilitySpec.Copies`) on the
record, as the `Meter` reads an ancestor's from it. `MaxCopiesPerCapability`
names the bound the store was built with; without it, the check of that bound
is skipped. A store that refers to its
tenants, or scopes each tenant's rows, also sets `NewTenant` (another tenant
it can charge), `TenantCtx` (the context a write for that tenant runs under)
and `ListCtx` (the context of `ListTenantBudgets`, a read across tenants). One
that scopes rows by the call's context sets `ScopesTenants` too, and the
suites check that a call for `Tenant` under another tenant's context returns
`ErrScopeMismatch` and moves nothing.
Without them the checks that need a second tenant use a random id and `Ctx`.
`DeleteCapability` removes a record as `Store.Purge` would, for the
`PurgeOrphans` checks; some remove an ancestor whose children stay, so a store
whose records refer to their parent unhooks the children first. `Ledger` turns
on the checks of a `LedgerReader`; with `Now` (the store's clock) and
`Advance` as well, the checks of its UTC buckets. `storetest.Env` takes the same `NewTenant`,
`TenantCtx` and `Advance`, so one setup can serve both suites, plus `Now` (the
store's clock, for the checks at an expiry's exact instant) and
`DeleteTenant` (moves a tenant to the trash, for the `ErrUnknownTenant` and
`ErrTenantDeleted` checks). Both suites take `ScopesTenants` for a store that
keeps tenants apart by the call's context: a call naming one tenant under
another's scope must then be `ErrScopeMismatch`. Both take `ReadOnlyCtx` too,
a context that reads every tenant's rows and writes none: the suites then
check that every write under it is `ErrScopeMismatch`, changes nothing and,
in `metertest`, runs no `onCharged`.

The checks include charges racing one ceiling, so run them against the
database itself: a store that reads a counter and writes it back outside one
statement or lock overspends, and fails them. Give the store a pool of one or
two connections there: a call holds one connection for its transaction, and
one that takes a second while it holds the first deadlocks once as many calls
run as the pool has connections. Every suite bounds each check by
`CheckTimeout` (a minute unless the Env sets it); a check that outlives it
stops the test binary with every goroutine's stack, naming the check.

The suites grow: a minor release may add a check of a promise the contract
already made, which a store that broke the promise then fails — that is the
suite finding a bug, not a break of the API. A new optional Env field skips
its checks while unset, so an Env written for an earlier release still
compiles and runs everything it ran before.

`memstore` and `pgstore` run the same checks from their own tests — `pgstore`
against PostgreSQL in a container, as a role that owns no table, on a pool of
two connections — `dpop` and `jwks` run theirs against their shipped
implementations, and `redisreplay` runs `dpoptest` against Redis.

`otelmetric.Instrument` wraps a `meter.UsageStore` and keeps every interface of `meter` the
store implements — the optional `CopyUsageReader` and `TenantUsageEraser`. A decorator of your
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
`GetRecord` reads the record back with `IssuedBy` and its own `Revocation`,
whose `Cascaded` says a cascading revoke wrote it; a capability stopped only
through an ancestor has none, so ask `IsRevoked` whether it verifies.
`ListByPrincipal` leaves expired and revoked records out unless
`IncludeExpired` or `IncludeRevoked` asks for them.
Verifiers that cache revocation answers learn of it within the cache TTL
(`DefaultRevocationCacheTTL`, two seconds), or at once if you call `RevocationCache.Clear` when a
revocation is announced — on a Postgres `NOTIFY`, for example. `Clear`
drops the whole cache, and there is no per-id drop: an answer cached for a
descendant is also an answer about the revoked ancestor. The next check of
each id then asks the store once, so batch a burst of signals into one
`Clear`. Build the cache with `NewRevocationCache` over your store, as
`Capabilities` and, if the verifier reads Biscuits, `Biscuits`; `Sweep` drops
its expired entries on a timer of yours, optional since lookups skip and replace
them anyway. Over `pgstore`, run a `pgstore.RevocationWatcher` beside each
verifier: the schema notifies `pgstore.RevocationChannel` on every
revocation, and the watcher clears the caches it is given on each
notification and after each connection it opens:

```go
w := pgstore.RevocationWatcher{
    // A connection of the watcher's own: LISTEN holds it for as long as it runs.
    Connect: func(ctx context.Context) (*pgx.Conn, error) {
        c, err := pool.Acquire(ctx)
        if err != nil {
            return nil, err
        }
        return c.Hijack(), nil
    },
    Caches:  []pgstore.Clearer{revocations},
    OnError: func(err error) { log.Printf("revocation watcher: %v", err) },
}
go w.Run(ctx) // reconnects with backoff until ctx ends
```

A statement that writes no new revocation entry notifies nothing; a Biscuit
copy's revocation notifies as a capability's does.
Revoking a capability revokes everything delegated from it, with or
without the flag: `IsRevoked` answers for the whole chain. `CascadeChildren`
also writes a revocation entry per descendant, so the audit trail names each
capability that was stopped. A chain is at most `MaxDelegationDepth` (64)
ancestors deep: `Store.Insert` refuses a child past it as `ErrDelegationTooDeep`,
writing nothing, so `Delegate` returns that error and no token
(`ErrDelegationTooDeep` matches `ErrRevoked`, and so answers as 401); a store
answers a deeper chain, written before `Insert` refused one, with that error — read as revoked — rather
than walk part of it and miss a revocation at its root.

Records do not stay forever. A housekeeping job calls
`Store.Purge(ctx, limes.PurgeRequest{ExpiredFor: retention})` on a timer, and
again while a batch comes back full (`Limit`, `DefaultHousekeepingLimit` unless
set): it removes the capabilities expired for longer than the retention, with
their revocation entries and those of their Biscuit copies, then
`UsageHousekeeping.PurgeOrphans` releases what the meter still holds for them.
It never removes a capability while a child of it stays on record: `Insert`
refuses a child that would outlive its parent, and among records expiring
together `Purge` takes the deepest first. The retention is at least
`MinPurgeRetention` (six minutes, the zero value's): a verifier accepts a
token for up to its `Leeway` past expiry, never more than `MaxLeeway`, by a
clock that may run up to `PurgeClockSkew` behind the store's, and a
revocation purged inside that window would let the revoked token through.

These bounds are constants, not settings, because the two sides that rely on
them are configured apart: the verifiers, in every service that accepts a
token, and the job that purges, beside the store. A store cannot see the
leeway of each verifier, so the only bound it can trust is one every verifier
is held to. Both bounds are floors and ceilings, not values: a verifier takes
any `Leeway` up to `MaxLeeway`, and `Purge` any `ExpiredFor` from
`MinPurgeRetention` up. Where clocks may drift further apart than
`PurgeClockSkew`, keep revocations longer by passing a longer `ExpiredFor`.

## Bring your own storage

On PostgreSQL, take `pgstore` (`github.com/oleg-tkachuk/limes/pgstore`, a
module of its own): it implements every contract below but `KeyResolver` and
the DPoP stores, passes `storetest` and `metertest`, and keeps tenants apart
with row-level security.

```go
// As the owner of the schema, granting the role the stores serve as.
err := ownerPool.AcquireFunc(ctx, func(c *pgxpool.Conn) error {
    return pgstore.Migrate(ctx, c.Conn(), pgstore.MigrateOptions{RuntimeRoles: []string{"app"}})
})
records, _ := pgstore.NewStore(pgstore.Config{DB: pool})
usage, _ := pgstore.NewUsageStore(pgstore.Config{DB: pool})
// A call for one tenant; housekeeping takes pgstore.WithAllTenants(ctx),
// and a read across tenants, ListTenantBudgets among them,
// pgstore.WithAllTenantsReadOnly(ctx), which admits no write: one is
// refused with ErrScopeMismatch before it begins.
err = records.Revoke(pgstore.WithTenant(ctx, tenant), limes.RevokeRequest{CapabilityID: id})
```

`Config.DB` is a `pgstore.DB` — `BeginTx` and `QueryRow`, which
`*pgxpool.Pool` and `*pgx.Conn` implement. Every transaction the store opens
is READ COMMITTED, whatever the database's default, so a call that waited on
a lock reads what committed meanwhile; a `DB` of your own passes on the
`pgx.TxOptions` it is given, or the store runs at the database's default
isolation.

A role that does not own the tables sees only the rows of the tenant a call
is scoped to, and none when it is scoped to no tenant; the revocation checks
see every tenant, as the verifier asks them before it knows the token's. A
call whose request names its tenant — `Insert`, `ListByPrincipal`, `Bump`,
`Charge`, `Reserve`, `Settle`, `Release`, `GetTenantBudget`, `SetTenantBudget`, the
tenant erasers, `Ledger.SumCharges` and `Census.CountRecords` for one tenant —
under a
scope that does not admit that tenant returns `ErrScopeMismatch`, never
`ErrNotFound` or `ErrInvalidRequest`. A call that names only an id
(`GetRecord`, `Revoke`, `Refund`, `GetUsage`, `GetCharge`, …) cannot
be checked so, and answers a row out of scope as not found.
`onCharged` runs on the store's own transaction, which `TxFrom` reads from
its context and which is `onCharged`'s only while it runs: kept past it, a
call fails with `pgx.ErrTxClosed`. It is the store's to end — `Commit` or
`Rollback` on it returns `ErrChargeTransaction`, and a charge whose
transaction `onCharged` ended with SQL of its own (`COMMIT`, `ROLLBACK`, or
either followed by `BEGIN`) is refused with `ErrChargeTransaction`; a
savepoint it begins is its own to end. Every time it writes comes from
`Config.Now`, never from the database's clock.

A tenant's ceiling is exact because every call that moves its spend or holds
— `Charge`, `Reserve`, `Settle`, `Refund`, `Release`, `ReleaseExpired`,
`SetTenantBudget` — locks the tenant's budget row until it commits. One
tenant's charges therefore commit one at a time, on whatever capabilities, and
no number of connections or replicas raises that: pgstore's benchmarks, with
PostgreSQL on the same host, put it at some 350–700 charges a second for one
tenant. Other tenants do not wait on it, nor do `Bump` and the revocation
checks. A network to the database adds its round trip to every statement of
the transaction, and `onCharged` runs while the lock is held, so keep it
short; a tenant that needs more is served by fewer, larger charges — a
`Reserve` settled once for many units of work — not by a setting.

Run `Migrate` as the role that owns the schema and serve as another one. The
policies bind the owner too (`FORCE ROW LEVEL SECURITY`), and `Migrate` scopes
each migration of its run to every tenant — one whose scope cannot be set
closes the connection, so it never runs unscoped and the run fails; but the
owner may turn them off, and a superuser or a role with `BYPASSRLS` is never
bound, so `CheckSchema` refuses a pool of any of the three — or of a role
owning anything in the schema, or able to act as one of these — with
`ErrRuntimeRole`. It checks the session's role and the role the connection
logged in as as well as the current one, since a session that switched with
`SET ROLE` or `SET SESSION AUTHORIZATION` can switch back. `MigrateOptions.RuntimeRoles` grants the
serving roles the schema, its tables and the tables of later versions, the
schema's functions — which no other role may call — and only reads of the
version table. Run it before any migration of yours that
refers to its tables. It holds the advisory lock every `tern` migration
takes, which is reentrant per session: a consumer migrating with `tern` calls
`Migrate` on the connection its own run holds. `CheckSchema` tells a readiness
check whether the schema is at the version the package queries, returning
`ErrSchemaMismatch` when it is not — behind, `Migrate` has not run; ahead, a
later pgstore migrated; `ReadSchemaVersion` reports both numbers (`Applied`,
`Expected`), with no more than SELECT on the version table. `Config.CheckTenant` runs in `Insert`'s
transaction, so a consumer that keeps its own tenants answers
`ErrUnknownTenant` and `ErrTenantDeleted` from the same snapshot the record is
written in; `Config.Scope` replaces `ScopeFromContext` for a consumer whose
policies read settings of their own; such a Scope gets no scope check unless
`Config.CheckScope` names one — `pgstore.CheckSchemaScope` for a Scope that
sets the schema's own settings, `pgstore.SettingTenant` and
`pgstore.SettingAllTenants`. A Scope that maps a read across every tenant
to `SettingAllTenantsRead` gets the read-only refusal of
`WithAllTenantsReadOnly`: a call that writes under it returns
`ErrScopeMismatch` and changes nothing.

Elsewhere, implement these and you are done:

| Interface | Stores | Transactions needed? |
|---|---|---|
| `Store` = `IssuanceStore` + admin | Capability records + revocations; an `Issuer` needs only `IssuanceStore` (`Insert`, `IsRevoked`) | No |
| `BiscuitRevocationStore` | Revoked Biscuit copies (only if you accept Biscuits) | No |
| `meter.CopyUsageReader` | Reads Biscuit copies' own counters (only to show them) | No |
| `meter.LedgerReader` | Sums a tenant's charges for billing and reports (optional; a type of its own beside `meter.UsageStore`) | No |
| `RecordCensus` | Counts capability records by disposition, for one tenant or every tenant (optional; a type of its own beside `Store`) | No |
| `TenantRecordEraser`, `meter.TenantUsageEraser` | Erase a tenant's records and usage (optional) | No |
| `meter.UsageStore` = `Meter` + `MeterReader` + `TenantBudgets` + `UsageHousekeeping` (only to meter) | Request and spend counters, the charges ledger, tenant ceilings; `DeleteUsage` and `PurgeOrphans` release a reaped capability's holds | Only for atomic side effects |
| `KeyResolver` | Public verification keys; an unknown kid is `jwks.ErrUnknownKID`, the token's fault (`ErrInvalidToken`), and any other error the resolver's own (`jwks.Static` and `jwks.Remote` ship; `jwkstest` checks yours) | No |
| `dpop.ReplayCache` | DPoP proof ids seen, per key, each counted against its holder: `Seen(ctx, dpop.ReplayEntry)` reports a holder past its share as seen, and returns an error when its store cannot answer — an outage, not a refused proof (only for key-bound tokens; `dpop.MemoryReplayCache` ships for one process — behind several replicas, the `redisreplay` module ships one over Redis, or implement it over another shared store) | No |
| `dpop.Nonces` | Server-provided DPoP nonces (optional; `dpop.HMACNonces` ships and needs no store) | No |

To bill or report from the charges ledger, read it through a
`LedgerReader` rather than a store's tables: `SumCharges` sums the charges of
`TenantID`, or of every tenant with `AllTenants`, taken in [`From`, `To`), by
the store's clock, grouped by unit and, as `SumChargesRequest` asks, by bucket
— the zero `ChargeBucketNone` for the period whole, or the UTC
`ChargeBucketHour`, `Day`, `Week` (ISO, from Monday 00:00) or `Month` — and by
each dimension `GroupBy` names: `ChargeDimensionTenant`, `Op`, `Actor`,
`Capability`. `Filter` keeps the charges of the tenants, operations, actors or
capabilities it lists, at most `MaxChargeFilterValues` each. Amounts in
different units are never summed together. Buckets come in time order and,
within each, the largest `Charged` first, ties by unit, tenant, operation,
actor and capability, bytewise; so with `ChargeBucketNone` the first page of
`Limit` rows is the top `Limit` — across tenants, grouped by tenant, the
largest spenders. `meter.AllChargeSums` walks every page. Each `ChargeSum` carries `Charged`, `Refunded` and
`Charges`; the net is `Charged − Refunded`. A refund counts with the charge it
returns, so a closed period's sums still move, and a sum past `MaxNanos` is
`ErrCounterOverflow`.
`memstore.NewLedger` and `pgstore.NewLedger` read their stores' ledgers, the
latter scoped to the tenant as every other read, and a sum across tenants only
under `WithAllTenantsReadOnly` (or `WithAllTenants`), as `ListTenantBudgets`:
under one tenant's scope it is `ErrScopeMismatch`. Who may read every tenant's
spend is the consumer's to decide, by who gets that scope; `metertest` checks a reader
its `Env.Ledger` names.

To report on capability records, count them through a `RecordCensus`:
`CountRecords` counts the records of `TenantID`, or of every tenant with
`AllTenants`, whole or by tenant and principal type, a page at a time in that
order. Each `RecordCount` has `Total` = `Active` + `Expired` + `Revoked`, each
record in one of them by the store's clock: revoked — itself or through an
ancestor on record — wins over expired, and expired — a zero `ExpiresAt`
included — over active. `Delegated` counts
the records with a parent, and `ExpiringSoon` the active ones expiring within
`ExpiringWithin`. `memstore.NewCensus` and `pgstore.NewCensus` ship one; with
pgstore, a census of every tenant runs under `WithAllTenantsReadOnly` or
`WithAllTenants`, and one under a narrower scope is `ErrScopeMismatch`.
`storetest` checks a census its `Env.Census` names, when the `Env` also sets
`Now`.

To test a report, backdate its charges through the store's clock, never by
writing a store's tables, whose columns are the store's own and change
without notice. A charge carries the store's time (`ChargeRecord.At`), so
build the stores on a clock the test sets (`Config.Now` in `pgstore`,
`StoreConfig.Now` and `UsageStoreConfig.Now` in `memstore`), start it at the
period's start and move it forward before each charge:

```go
at := start
cfg := pgstore.Config{DB: pool, Now: func() time.Time { return at }}
records, _ := pgstore.NewStore(cfg)
usage, _ := pgstore.NewUsageStore(cfg)

// A record that lives through the period, as a token in use would.
c := limes.Capability{ID: uuid.New(), ExpiresAt: start.Add(period), Subject: subject, Caveats: caveats}
_ = records.Insert(ctx, limes.InsertRequest{Capability: c})
meterSubject, _ := limes.StoredMeterSubject(ctx, records, c.ID)
for _, day := range []int{0, 1, 8} {
	at = start.AddDate(0, 0, day)
	_, _ = usage.Charge(ctx, meter.ChargeRequest{Subject: meterSubject, Amount: amount}, nil)
}
```

The capability must not have expired at a charge's time. Insert its record
with an `ExpiresAt` past the period, as above. Or issue it through an
`Issuer` whose `IssuerConfig.Now` is the same clock, set to the period's
start, with an `IssueRequest.TTL` as long as the period. A test that charges
through verified tokens gives `VerifierConfig.Now` that clock as well. Move
the clock forward only: holds, revocations and expiry are all measured by it.
`Example_backdatedCharges` runs the whole route on `memstore`.

To remove a tenant, a store may implement `TenantRecordEraser` and
`meter.TenantUsageEraser` (optional; `storetest` and `metertest` check them when
present, and `memstore` and `pgstore` implement both). `EraseTenantRecords`
revokes every capability of the tenant and removes only the records `Purge`
would remove: an id not on record is not revoked, so erasing a record whose
token may still verify would let it through again. `Purge` takes the rest as
it expires. `EraseTenantUsage` drops the tenant's counters, holds, charges
and aggregate — its billing history, which does not come back — and marks
the tenant erased: a request verified before the erase, or a late cost of a
record `Purge` has not taken yet, is refused `ErrTenantErased` (a kind of
`ErrRevoked`) by `Bump`, `Charge`, `Reserve` and `SetTenantBudget` rather
than counted again, and `Insert` refuses a new record of the tenant, which
the erase's revocation would have missed. `PurgeOrphans` forgets the mark
once none of the tenant's records is left.

Called apart, `EraseTenantUsage` goes first and `EraseTenantRecords` second:
the mark the first sets makes `Insert` refuse the tenant, so every record
the second finds is every record there will be, and it revokes them all.
`PurgeOrphans` forgets the mark once none of the tenant's records is left;
run between the two calls, it lets a new root record in, which the
records' erase then revokes with the rest.
The other way round, a record inserted between the two calls is on record
after the records' erase, unrevoked, and its token goes on verifying. In
`pgstore`, `Store.EraseTenant` does both in one transaction and runs a
callback of yours in it, so your own rows of the tenant go with them or not
at all; it is handed a `pgstore.Erased`, the transaction as `Tx` and the
result the call will return as `Result`. `EraseTenantRequest.Note` and `Actor` go on each revocation entry it
writes, and `EraseTenantResult` counts the capabilities `Revoked`, the records
`Removed` and the records `Kept`, revoked, for `Purge`; a second erase revokes
nothing the first did.

Two obligations are easy to miss. `Store.IsRevoked` answers for the
capability's whole delegation chain, and `Meter` applies every charge and
request to each ancestor as well, reading the ancestor's ceilings from its
record. Code that only meters can depend on `Meter` alone; code that only
administers tenant ceilings on `TenantBudgets` alone.

`Store.Insert` returns `ErrAlreadyExists` for an id already on record, refuses
a record with no id or under a parent not on record as `ErrInvalidRequest`,
a child expiring after its parent or in another tenant as
`ErrDelegationTooWide`, a child whose budget names another unit than its
parent's as `ErrUnitCodeMismatch`, and a child with more than `MaxDelegationDepth`
ancestors still on record as `ErrDelegationTooDeep`, writing nothing:
`Purge` and the `IsRevoked` walk rely on a whole lineage in one tenant, no
deeper than the walk reaches. It should tell a missing tenant
from a failure: return
`ErrUnknownTenant` for a tenant it does not hold and `ErrTenantDeleted` for one
in the trash. A store whose usage store erases tenants
(`meter.TenantUsageEraser`) refuses a record of a tenant marked erased with
`ErrTenantErased`, a kind of `ErrRevoked`, until `PurgeOrphans` forgets the
mark. A store that keeps each tenant's rows apart by the call's
context returns `ErrScopeMismatch` for a call scoped to another tenant than
the one its request names, rather than answer as if the rows were not there. `Issue` and `Delegate` pass them through, wrapped, beside
`ErrInvalidRequest`, which every check on the request itself matches — a
missing tenant id, an empty audience entry, a negative TTL or one ending within
the second it is issued (a token's times are whole seconds), a `NotBefore` at
or after the expiry, a malformed thumbprint, invalid caveats (`ErrInvalidCaveats`).
Anything else they return is the store's or the signer's failure, not the
caller's.

A read by id that finds nothing matches `ErrNotFound`, through a sentinel of
its own where the contract names one: `ErrChargeNotFound` (`Refund`,
`GetCharge`, `ChargeByRef`), `ErrReservationNotFound`, `ErrUsageNotFound` (a
capability never bumped or charged) and `ErrTenantBudgetNotFound`.
A well-formed write that disagrees with what is on record matches
`ErrConflict` — map the family to 409 and read again: `ErrAlreadyExists`
(`Insert`), `ErrUnitCodeMismatch` (`Insert`, `Delegate`, and a `Charge`,
`Reserve` or `Settle` in another unit than a counter already counts in),
`ErrExternalRefConflict` (`Charge`), `ErrSettleConflict` (`Settle`) and
`ErrTenantBudgetVersionMismatch` (`SetTenantBudget`). `ErrorResponse`
answers the family 409 too.

Every write and every listing takes a `…Request`, a read by id takes the id,
every list is a `Page`, and every read names what
it reads (`GetRecord`, `GetUsage`, `GetTenantBudget`), so one type may implement `Store` and
`meter.UsageStore` together, or each apart as `memstore` does.

`meter.UsageStore` names no transaction type: a store carries its own in
the callback's context. Have no transactions? Pass `nil` for the callback.
Everything works identically; you forgo the atomic-side-effect
guarantee.

Every listing — `ListByPrincipal`, `ListTenantBudgets`, `ListReservations`
(soonest to expire first, ties by id), `SumCharges` and `CountRecords` — is a
page with an opaque cursor, so an API can hand a page token to its client. To
read `ListByPrincipal` or `ListTenantBudgets` whole, range over `AllByPrincipal` or `meter.AllTenantBudgets`: they walk
the pages from `req.Cursor` afresh on every range, stop fetching when the loop
breaks, and refuse a page that returns a cursor already fetched
(`ErrCursorRepeated`). Spend moves between pages, so `AllTenantBudgets` may
yield a tenant twice or not at all.

```go
for c, err := range limes.AllByPrincipal(ctx, store, req) {
    if err != nil { /* yielded once; the walk ends */ }
    // …
}
```

## Key rotation

1. Publish the new public key alongside the old (`jwks.Static.SetKey`, or add it to the
   served JWKS — `jwks.Marshal` output is sorted by kid, so it is stable).
2. Switch the issuer to sign with the new key — no sooner than the
   `max-age` the JWKS is served with, so every verifier has fetched it.
3. Withdraw the old key (`jwks.Static.RemoveKey`) only after the **longest outstanding
   TTL** has elapsed.

Skipping the wait in step 3 invalidates tokens agents are still holding. Since
TTLs here are minutes, the wait is short — which is part of why short TTLs are
the default posture (`IssuerConfig.DefaultTTL`, fifteen minutes).

A compromised key is withdrawn at once, and the bound is the verifiers'
cache, not a TTL. A verifier on `jwks.Remote` stops accepting the key once it
fetches the document without it: within the `max-age` the document is served
with (`jwks.HandlerConfig.MaxAge`, at most `RemoteConfig.RefreshInterval`,
5 minutes by default), or within `RemoteConfig.MinRefreshInterval`, 10
seconds, with `HandlerConfig.Revalidate`. While it
cannot reach the endpoint it keeps the last set it fetched for up to
`RemoteConfig.MaxStale`, an hour by default, then refuses every token. So on
compromise also revoke the capabilities the key signed (`Store.Revoke`): a
revoked capability is refused whatever key verifies it. A document with no
`"keys"` array is a failed fetch, not a withdrawal; `{"keys":[]}` withdraws
every key.

Load a signing key with `LoadKey(path)` (an Ed25519 key, PKCS#8 PEM only); an
unreadable path, or none, is an error, never a fresh key. `GenerateKey` makes
an ephemeral one, which vanishes with the process — a deployment that may run
without a key file chooses between the two itself, and knows which it chose.
Either key's kid is `jwks.KeyID`'s: its RFC 7638 thumbprint, the same wherever
it is derived; set `SigningKey.KeyID` to name it otherwise.

`NewSigner(limes.SignerConfig{KeyID: kid, Key: limes.SealKey(key)})` signs
through any `crypto.Signer` whose key is Ed25519 — an `ed25519.PrivateKey`, or a
key held in a KMS or an HSM; the token format stays inside the module. A key is
held sealed — `limes.SealedKey` (`SigningKey.Private` is one already), and
`dpop.NonceKeys` for nonce keys — so no formatting of a value that holds it,
under any verb or from any field, prints it. `SealKey` keeps a copy of an
`ed25519.PrivateKey`, or of one behind a pointer, and `dpop.SealNonceKeys`
copies its keys, so clearing or reusing the caller's key afterwards leaves
the sealed one whole. A key that
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
`Bearer` or `DPoP` challenge naming the error code (and `algs` for DPoP, when the verifier checks proofs); a
refused operation is 403 `insufficient_scope`; an exhausted budget or request
limit is 403 with no challenge; a conflict with what is on record
(`ErrConflict`: an id already on record, a unit mismatch, an external
reference or settle naming another cost, a stale budget version) is 409; an
issuer's key set never fetched, out of
reach past `MaxStale`, or lacking the token's kid while the endpoint fails, is
503 — the server's trouble, not the token's; anything else, a store outage or
a verifier with no `DPoP` given a key-bound token (`ErrProofUnsupported`)
included, is 500. A refusal for want of a nonce carries a fresh one in `DPoP-Nonce`. The
body is `application/problem+json` with fixed text — the error's own
message, which can name kids or stores, stays in your log — and the
refusal's stable `Reason` (`budget_exceeded`, `revoked`, …) as its `reason`
member and the end of its type URI. `ReasonOf(err)` gives the same `Reason`
for your own wire format, and `Reason.Permanent()` whether the
same request with the same capability can ever succeed, so a caller knows to
stop rather than retry. The full table is in [diagrams.md](diagrams.md#answering-a-refusal).

## Defaults

| Setting | Default |
|---|---|
| `IssuerConfig.DefaultTTL` | `DefaultIssueTTL`, 15 minutes |
| `VerifierConfig.Leeway` | `DefaultLeeway`, 30 seconds; `VerifierConfig.NoLeeway: true` for none |
| `VerifierConfig.MaxTokenBytes` · `IssuerConfig.MaxTokenBytes` | `DefaultMaxTokenBytes`, 16 KiB |
| `VerifierConfig.Leeway` bound | `MaxLeeway`, 5 minutes |
| `VerifierConfig.AcceptTokenTypes` | `TokenTypeV6` (`limes-cap-v6+jwt`), the one format the package reads |
| `SignerConfig.TokenType` | `TokenTypeV6` |
| `VerifierConfig.Realm` | `DefaultRealm`, `limes` |
| `jwks.HandlerConfig.MaxAge` | `jwks.DefaultMaxAge`, 5 minutes; `HandlerConfig.Revalidate: true` for `max-age=0` |
| `ListByPrincipalRequest.Limit` · `ListTenantBudgetsRequest.Limit` · `ListReservationsRequest.Limit` · `SumChargesRequest.Limit` · `CountRecordsRequest.Limit` | `DefaultListLimit` 50, at most `MaxListLimit` 500 |
| `PurgeRequest` · `ReleaseExpiredRequest` · `PurgeOrphansRequest` | `DefaultHousekeepingLimit` 1 000 a batch; `PurgeRequest.ExpiredFor` `MinPurgeRetention`, 6 minutes, and no less |
| `MaxDelegationDepth` · `MaxBiscuitBlocks` | 64 ancestors · 32 attenuation blocks |
| `CopyUsageRequest.RevocationIDs` | at most `MaxCopyUsageIDs`, 500, of one `CapabilityID` |
| `RevocationCacheConfig` | `DefaultRevocationCacheTTL` 2 seconds · `DefaultRevocationCacheEntries` 100 000 of each kind, capabilities and Biscuit copies, spread over shards with a lock each — past the bound the oldest of the shard is dropped, the oldest of all in a cache of fewer than 512 entries, which is one shard; a negative TTL or bound is refused — for no cache, give the verifier the store itself |
| `jwks.RemoteConfig` | `jwks.DefaultFetchTimeout` 10s · `DefaultRefreshInterval` 5m · `DefaultMinRefreshInterval` 10s · `DefaultMaxStale` 1h · `DefaultMaxDocumentBytes` 1 MiB |
| `dpop.VerifierConfig.Window` | `dpop.DefaultWindow`, 1 minute; a proof's id is remembered for `Window` plus `dpop.ReplayClockSkew`, 5 seconds |
| `dpop.VerifierConfig.MaxProofBytes` | `dpop.DefaultMaxProofBytes`, 4 KiB; a `jti` past `dpop.MaxJTIBytes`, 128 bytes, is refused |
| `dpop.MemoryReplayCacheConfig` | `dpop.DefaultReplayCacheEntries`, 100 000 ids; `dpop.DefaultReplayEntriesPerHolder`, 1 000, per capability |
| `redisreplay.Config` | `redisreplay.DefaultKeyPrefix`, `limes:dpop:replay:`; `dpop.DefaultReplayEntriesPerHolder`, 1 000, per capability |
| `dpop.HMACNoncesConfig` | `dpop.DefaultNonceLifetime`, 5 minutes; `dpop.HMACNonceClockSkew`, 5 seconds, for a nonce issued ahead of this replica's clock; keys of at least `dpop.HMACNonceKeyBytes`, 32 |
| `DefaultReservationTTL` | 5 minutes |
| `memstore.UsageStoreConfig.MaxCopiesPerCapability` · `pgstore.Config.MaxCopiesPerCapability` | `meter.DefaultMaxCopiesPerCapability`, 1 024; negative is refused |
| `Caveats.Copies` | the zero `CopyQuota`: no copy with limits of its own is counted |
| `BudgetCeiling.UnitCode` left empty | allowed only on the zero ceiling, which spends nothing |
| `PurgeClockSkew` | 1 minute |
| `MaxExternalRefBytes` | 200 |
| `MaxNanos` | just under a billion units |
| `MaxIJSONInteger` | 2^53 − 1, for every integer in a token |
| `Env.CheckTimeout` of every conformance suite | `DefaultCheckTimeout`, 1 minute a check |

