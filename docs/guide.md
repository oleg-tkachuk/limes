# Guide

Using limes, one concern at a time. [README.md](../README.md) has the
five-minute start, [diagrams.md](diagrams.md) draws each path described
here, and [standards.md](standards.md) maps each to the RFC it follows.
Every type, field and limit is in the [API reference](api/README.md).

## Authorize every call

```go
// Every field, not Header.Get's first: two Authorization fields, or two
// DPoP fields, are refused rather than one chosen (RFC 9449 §4.3, §7.2).
scheme, token, err := limes.ParseAuthorizationValues(r.Header.Values(limes.AuthorizationHeader))
if err != nil {
    // None (ErrNoCredentials): 401, a challenge with no error code.
    // Malformed, or more than one (ErrMalformedAuthorization): 400.
    limes.WriteErrorResponse(w, verifier.ErrorResponse(ctx, err, scheme))
    return
}
proof, err := dpop.SingleProof(r.Header.Values(dpop.Header))
if err != nil {
    // More than one DPoP field (dpop.ErrInvalid): 401, invalid_dpop_proof.
    limes.WriteErrorResponse(w, verifier.ErrorResponse(ctx, err, scheme))
    return
}
granted, err := verifier.Authorize(ctx, limes.AuthorizeRequest{
    Verify: limes.VerifyRequest{
        Token: token, Scheme: scheme, Audience: "https://data.example.com",
        // Needed when the capability is key-bound; ignored when it is not.
        DPoP: dpop.Proof{JWT: proof, Method: r.Method, URL: requestURL},
    },
    Check:      limes.CheckRequest{Op: limes.OpGet, Resource: "corpus/public/report.pdf"},
    ClientAddr: clientAddr, // netip.Addr
})
// verifier.ErrorResponse(ctx, err, scheme) answers a refusal the RFC 6750 /
// RFC 9449 / RFC 9457 way; or branch on the sentinel yourself:
switch {
case errors.Is(err, limes.ErrExpired):           // TTL elapsed
case errors.Is(err, limes.ErrNotYetValid):       // NotBefore ahead, past Leeway (30s by default)
case errors.Is(err, limes.ErrRevoked):           // pulled mid-flight
case errors.Is(err, limes.ErrAudienceMismatch):  // token for another service
case errors.Is(err, limes.ErrCaveatViolation):   // not allowed to do this
case errors.Is(err, limes.ErrProofOfPossession): // no valid proof that the bearer holds the key
case errors.Is(err, limes.ErrInvalidToken):      // forged, or not this verifier's to accept
case errors.Is(err, jwks.ErrUnavailable):        // the issuer's keys out of reach: the server's fault
}
// Every refusal Verify or Authorize returns, and ErrNoCredentials, also
// matches ErrUnauthenticated: the one check for "answer 401". SingleProof's
// dpop.ErrInvalid does not, though ErrorResponse answers it 401.
// ErrMalformedAuthorization does not either: it matches ErrInvalidRequest.
```

`Verify` and `Authorize` return a `VerifiedCapability`, which only the
verifier makes. `granted.Capability()` is a copy of the `Capability` to
read; `Issuer.Delegate` takes the `VerifiedCapability` itself, so a parent
is always one the verifier checked.

`Authorize` is `Verify` plus every caveat that needs no state: the
operation, the resource, the idempotency key, the tainted read and the
client's address. The `Meter` counts request and budget limits, inside your
own transaction ([Charge against the budget](#charge-against-the-budget)).
Call `Verify` alone only where there is no operation to check, such as
before a delegation.

Verification is local: the signature is checked against the resolved key,
with no call to the issuer, before any claim is read. Each refusal is a
distinct sentinel. Map it to your transport's status, or let
`ErrorResponse` answer it over HTTP ([Answer a refusal](#answer-a-refusal)).

The verifier accepts a token only when:

- it is typed `at+jwt` (`limes.TokenType`) and at most
  `VerifierConfig.MaxTokenBytes` long (16 KiB by default);
- its kid is in limes's namespace (`jwks.KeyIDPrefix`);
- `limes_v` names a format this package reads, and the claims carry a
  `client_id`, a tenant and a principal of a known kind
  (`PrincipalTypeUser`, `PrincipalTypeAgent` or `PrincipalTypeService`,
  with agent details only on an agent: `Principal.Validate`, which `Issue`
  and `Delegate` run too);
- the claims hold no member the verifier does not know (a caveat it could
  not enforce), none twice and none in another case.

An agent's `AgentPrincipal` — `AgentType`, `AgentVersion`, `RunID`,
`ParentRunID`, `Model`, `MCPClient` — is signed into the token for audit
and lineage. None of it is a caveat.

When one verifier trusts several issuers, set `KeyIssuers` (kid → issuer)
so one issuer's key cannot sign for another, or `SharedKeys: true` for
issuers in one trust boundary. `NewVerifier` refuses several issuers with
neither (an empty map counts as none), and refuses both together.

Every constructor in this guide — `NewVerifier`, `jwks.NewRemote`,
`jwks.NewHandler`, `dpop.NewVerifier`, `dpop.NewHMACNonces`,
`redisreplay.New`, `pgstore.NewReplayCache`, `otelmetric.Instrument` among
them — refuses a config it cannot work with as `limes.ErrInvalidConfig`
(code `invalid_config`, 500): the deployment's to fix, not the caller's.

### Resolve the issuer's keys

A verifier running apart from the issuer resolves keys with `jwks.Remote`:

```go
keys, err := jwks.NewRemote(jwks.RemoteConfig{
    URL: "https://issuer.example.com/.well-known/jwks.json", // https only
})
verifier, err := limes.NewVerifier(limes.VerifierConfig{
    Keys: keys, Revocations: revocations, TrustedIssuers: []string{"my-issuer"},
})
```

- **Fetch.** On first use, then as the response's `Cache-Control` allows,
  within `MinRefreshInterval` (10s) and `RefreshInterval` (5m). It
  revalidates by ETag. The intervals must nest: `MinRefreshInterval` ≤
  `RefreshInterval` ≤ `MaxStale`.
- **New kid.** Fetched on first sight, at most once per
  `MinRefreshInterval`.
- **Slow endpoint.** A known key past freshness but within `MaxStale` (1h)
  is served at once and refreshed behind it. A fetch runs apart from the
  caller, bounded by `jwks.DefaultFetchTimeout`.
- **Failure.** `jwks.ErrUnavailable` — the server's fault, matching no
  `ErrUnauthenticated` sentinel — when no set is held within `MaxStale`,
  when a caller gives up waiting with none, and for a kid the held set
  lacks while the endpoint is failing (it may be newly rotated in). A
  document with no usable entry counts as a failed fetch, so the last good
  set stays in service.
- **Origin.** A redirect to another scheme or host is refused: whoever
  answers the fetch chooses the keys every token is checked against.

The issuer serves the document with `jwks.NewHandler` over a function
returning its keys, such as `jwks.Static.Keys`:

```go
h, err := jwks.NewHandler(jwks.HandlerConfig{Keys: resolver.Keys, MaxAge: time.Minute})
mux.Handle("/.well-known/jwks.json", h)
```

It serves `application/jwk-set+json` with a strong ETag (304 on a matching
`If-None-Match`) and `Cache-Control: public, max-age` in whole seconds
(`DefaultMaxAge` unless `MaxAge` says otherwise). `Revalidate` serves
`no-cache` instead and excludes `MaxAge`; a negative `MaxAge`, or one under
a second, is refused.

`jwks.Marshal` writes the same document for a server of your own;
`jwks.Parse` reads one for a resolver of your own. `Parse` skips an entry
it cannot use — a foreign `kty` or `alg`, no kid, a `use` other than `sig`
or `key_ops` without `verify`, a malformed key, a member of the wrong JSON
type — so one bad entry does not take every other key out of service
mid-rotation. Two Ed25519 keys naming one kid are both skipped. A document
that is not JSON, names a member twice, or has no `"keys"` array is
refused; only `{"keys":[]}` withdraws every key.

### Tell a capability from an identity provider's token

A capability and an identity provider's access token are both `at+jwt`
(RFC 9068), so a route accepting both cannot tell them apart by type.
`limes.HasLimesKeyID(token)` reads the header alone and reports whether its
kid is in limes's namespace (`limes:`). Route on it, and let the chosen
verifier decide:

```go
if limes.HasLimesKeyID(token) {
	vc, err := verifier.Verify(ctx, limes.VerifyRequest{
		Token: token, Scheme: scheme, Audience: aud,
		DPoP: dpop.Proof{JWT: proof, Method: r.Method, URL: requestURL},
	})
	// …
} else {
	claims, err := oidcVerifier.Verify(ctx, token) // the provider's own verifier
	// …
}
```

`HasLimesKeyID` verifies nothing — a forged token names any kid — so each
branch verifies in full, and routing opens no way in:

- `Verifier.Verify` refuses a token whose kid is outside the namespace
  before looking up a key; `NewSigner` refuses such a kid too.
- A token naming a limes kid still needs a signature under a key the
  verifier holds, a trusted issuer, and claims read strictly.
- A capability routed to the provider's verifier is refused there: its
  issuer and keys are not the provider's.

### Inspect a token without trusting it

`inspect.Decode` reads a token's claims; `inspect.VerifySignature` checks
only its signature against one key. Both refuse a malformed token or one
longer than `inspect.MaxTokenBytes`, and check no expiry, audience,
revocation or proof. They are for tooling — showing what a token grants,
or which key signed it — and live in a package of their own so they are
never mistaken for authorization. Authorize only on `Verify`.

## Enforce the caveats on every operation

Verification proves a token genuine; it does not know what the bearer is
about to do. `Authorize` checks the caveats for you. Code that checks a
further operation under a capability it already verified calls the same
checks directly:

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

Resources:

- **`ResourcePrefixes`** match at a `/` segment boundary: `corpus/public`
  covers `corpus/public/x`, not `corpus/public-secret`.
- **`ResourceNamePrefixes`** match as plain string prefixes, across
  segments: `drafts/billing-` covers `drafts/billing-q3` and
  `drafts/billing-q3/notes`, not `drafts/billing`. A child's name prefix
  must start with one of its parent's, or lie past a parent segment
  prefix's boundary (`a/` under `a`; never `a`, which names `ab`).
- **`ResourceURIs`** pin exact resources.
- **No resource.** A resource-restricted capability refuses
  `Resource: ""`. For an operation over a set, pass the prefix that bounds
  the set.
- **Dot segments.** A resource with a `.` or `..` segment, plain or
  percent-encoded once or twice, is refused by a resource-restricted
  capability and by `Validate`: what it names depends on how you resolve
  it. Pass the resource as your storage will address it.

`AllowTaintedRead` acts only on a taint signal you supply. If you track
none, it protects nothing.

The built-in operations are those every resource server shares: `get`,
`put`, `list`, `delete`, `share` and `manage`. Your own operations are
namespaced (`tool:retrieve`, `mcp:github/create_issue`) and count as writes
unless declared otherwise: an undeclared one needs an idempotency key under
`IdempotencyKeyRequired`, and is never refused as a tainted read. Declare
the effect where you define the operation — a method option, a tool
registry — and pass it on every check:

```go
err := cap.Caveats.Check(limes.CheckRequest{
    Op:       "tool:retrieve",
    Effect:   limes.EffectRead, // from your operation's definition
    Resource: "corpus/public/",
})
```

A built-in operation keeps its own effect: declaring `put` a read is
`ErrEffectConflict`, a programming error (`ErrInvalidRequest`), not a
caveat violation. Gate anything else on the effect through
`CheckRequest.Mutating`, so it and `Check` cannot disagree.

## Delegate — attenuate, never escalate

An orchestrator narrows its own authority and hands the result to a worker
it spawned, with no call to an admin API:

```go
delegated, err := issuer.Delegate(ctx, limes.DelegateRequest{ // .Capability, .Token
    Parent:  granted, // as the verifier accepted it
    Subject: limes.Principal{Type: limes.PrincipalTypeAgent, Subject: "sub-worker"},
    TTL:     2 * time.Minute,
    Caveats: limes.Caveats{
        Ops:              []limes.Op{limes.OpGet},                                // dropped OpShare
        ResourcePrefixes: []string{"corpus/public/2026/"},                        // narrowed
        Requests:         limes.RequestCeiling{Max: 50},                          // 500 → 50
        Budget:           limes.BudgetCeiling{Max: limes.MustParseAmount("2.00")}, // 25.00 → 2.00, in the parent's unit
    },
})
```

A ceiling's zero value allows nothing. A capability that should make
requests, spend, or count Biscuit copies with limits of their own
(`Copies`, a `CopyQuota`) names a `Max`, or `Unlimited: true`. A ceiling
left out thus fails closed. `Issue` and `Delegate` refuse `Requests` that
allow none (`ErrInvalidCaveats`): no request could use the capability. A
zero `Budget` stands: a capability that may not spend.

**Only a parent granted `OpShare` may delegate** (else `ErrOpNotAllowed`),
and a child may only be narrower. `ErrDelegationTooWide` refuses a child
that asks for an operation the parent lacks, a wider resource scope, a
bigger budget or request ceiling, a larger copy quota, a wider audience or
another tenant; that drops the parent's key binding or
`IdempotencyKeyRequired`; or that grants an `AllowTaintedRead` the parent
lacks. Each child resource must be reachable by the parent (a parent
pinned to `ResourceURIs` admits no child prefix), and each child network
must lie inside a parent network.

- **Time.** A TTL that would outlive the parent is cut to the parent's
  expiry. A child starts no earlier than its parent (its `NotBefore`, else
  its issuance).
- **Unit.** A budget in another `UnitCode` is `ErrUnitCodeMismatch`; units
  are never converted. A budget naming no unit takes the parent's.
- **Inheriting.** `InheritCaveats: true` copies the parent's caveats.
  Caveats with no `Ops` are refused, never read as "inherit".
- **Parents that cannot delegate.** One expired (`ErrExpired`), revoked
  itself or through an ancestor (`ErrRevoked`), issued by another issuer
  (`ErrInvalidRequest`: its `iss` is not `IssuerConfig.IssuerName`), or read
  from a Biscuit copy (`ErrInvalidRequest`; its holder attenuates instead).

`Issuer.Biscuit` needs no `OpShare`: a copy attenuated from the Biscuit
form allows no more than the capability, whose bearer token could be handed
on as it is, and a copy with limits of its own counts against `Copies`. So
withholding `OpShare` does not keep a capability JWT-only: an endpoint that
turns a verified capability into its Biscuit form decides itself whom it
serves.

Narrowing bounds each child; use bounds the tree. Every charge and request
also counts against each ancestor, so a parent holding 25.00 that delegates
20.00 to each of two workers still spends at most 25.00 in total, not
40.00. An agent can attenuate itself, but never escalate.

## Bind a capability to a key (DPoP)

An unbound capability is a bearer token: whoever holds it can use it, and
an agent's tokens end up in prompts, tool output and logs. Binding it to a
key the agent keeps — RFC 9449 DPoP — makes a copy useless without the key:

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

The token carries the thumbprint as `cnf.jkt` (RFC 7800). A delegated
child keeps its parent's binding or takes the sub-agent's own key; `Narrows`
refuses a child that drops it.

`Verify` refuses a proof:

- signed by another key, or for another token;
- for another method (compared exactly: `post` is not `POST`) or URL;
- issued outside `dpop.VerifierConfig.Window` (a minute by default) of the
  verifier's clock;
- seen before.

Each refusal matches `ErrProofOfPossession` and its own `dpop` sentinel. A
replay cache or nonce store that cannot answer returns its own error: an
outage, never a refused proof.

A verifier with no `DPoP` refuses every key-bound capability with
`ErrProofUnsupported` rather than accept it as a bearer token, so a binding
cannot be lost to a forgotten line of config. That is the deployment's
fault: it matches no `ErrUnauthenticated` sentinel and is answered 500.

`dpop.Proof.URL` is the absolute URL the client addressed, scheme and host
included. Behind a proxy that rewrites the host or strips a mount prefix,
or on a server that sees only the path, set
`dpop.VerifierConfig.MatchPathOnly`: the server's side of the comparison
then drops scheme, host and prefix. The proof's `htu` must still be an
absolute URI, and the root path `/` must match exactly.

### Present it under the right scheme

`ParseAuthorization` reads `Authorization: DPoP <token>` or `Bearer <token>`
(the scheme in any case, the token as a token68). With
`VerifyRequest.Scheme` set, `Verify` refuses a key-bound token presented as
`Bearer` — a downgrade — or an unbound one presented as `DPoP`, with
`ErrAuthSchemeMismatch`.

- No header, or one under another scheme: `ErrNoCredentials` (401, a
  challenge with no error code).
- A `Bearer` or `DPoP` header whose credential is not a token68:
  `ErrMalformedAuthorization` (400). It matches `ErrInvalidRequest`, whose
  other members `ErrorResponse` answers 500, as the server's own mistakes.
- A token from no `Authorization` header — gRPC metadata, a message field —
  goes in with `AuthSchemeUnspecified`, the zero `Scheme`: no scheme is
  checked, but a key-bound token still needs its proof.

### Several replicas

Behind several replicas, give the `dpop.Verifier` server nonces, with the
same key on every replica:

```go
nonces, err := dpop.NewHMACNonces(dpop.HMACNoncesConfig{Keys: dpop.SealNonceKeys(key)}) // each key ≥ dpop.HMACNonceKeyBytes
proofs, err := dpop.NewVerifier(dpop.VerifierConfig{Replay: replay, Nonces: nonces})
```

A proof must then carry a nonce the server issued, so it cannot be signed
ahead of time. A refusal with `dpop.ErrNonceRequired` carries a fresh nonce
in the `DPoP-Nonce` header for the client to retry with (RFC 9449 §8):
`ErrorResponse` sets it, or call `verifier.DPoPNonce(ctx)`. The first key
signs and every key checks, so rotate by putting a new key first.

`dpop.MemoryReplayCache` is in-process and bounded: at most
`dpop.DefaultReplayCacheEntries` (100 000) ids, and
`dpop.DefaultReplayEntriesPerHolder` (1 000) per capability, so one
capability — whatever keys its copies are bound to — cannot fill it and
lock the others out. Full, it refuses rather than forgets. Across replicas,
a proof replayed to another replica inside the window is caught only by
server nonces or by a `dpop.ReplayCache` over a shared store.

The `redisreplay` module is one, over Redis. It is a module of its own, so
limes never resolves a Redis client:

```go
// go get github.com/oleg-tkachuk/limes/redisreplay
replay, err := redisreplay.New(redisreplay.Config{Client: rdb}) // any redis.UniversalClient
```

Run Redis with `maxmemory-policy noeviction`. Where a failover replaying
a proof once matters, keep `Window` short or require server nonces. The key
layout and the clock it decides by are in
[its reference](api/dpop.md#package-redisreplay).

A deployment that runs pgstore can keep the ids in PostgreSQL instead, in
one table every replica shares (`limes.dpop_seen_proofs`, created by
`pgstore/migrate`):

```go
replay, err := pgstore.NewReplayCache(pgstore.ReplayConfig{DB: pool}) // the runtime role's pool
// A job, now and then: delete expired ids a batch at a time.
for {
    n, err := replay.PurgeExpired(ctx)
    if err != nil || n == 0 {
        break
    }
}
```

It counts a holder's live ids and records the next under an advisory lock
on the holder, so concurrent proofs never pass its share. A holder refused
for its share, unlike a replay, is logged at WARN. A database that cannot
answer is an outage: `Verify` returns its error. The table has no
row-level security: the check runs before any tenant is known.

| Verifier | Replay cache |
|---|---|
| One process | `dpop.MemoryReplayCache` |
| Several replicas, with pgstore | `pgstore.ReplayCache` |
| Several replicas, with Redis and not pgstore, or a proof rate PostgreSQL should not carry | `redisreplay` |

To move proof ids from a table of your own to `pgstore.ReplayCache`, drop
that table: an id lives for the proof window alone, so the new table can
start empty. A proof accepted by the old table in the last window could be
replayed once to the new one, so switch every replica at once, or require
server nonces across the switch.

### The replay cache's share caps a holder's rate

Every replay cache gives each holder the same share of live ids,
`EntriesPerHolder`, and keeps an id for `Window` plus
`dpop.ReplayClockSkew` (5s). So one holder passes at most

```text
proofs per second ≤ EntriesPerHolder / (Window + ReplayClockSkew)
```

With the defaults that is 1 000 / 65 s, about 15 a second sustained; a
burst of up to 1 000 passes at once. Past it a fresh proof is refused with
`dpop.ErrReplayQuota`, unrecorded, and the cache logs the holder at WARN.
The code is `proof_quota_exceeded`, 429, category `exhausted` — never a
replay.

The holder is the capability at a resource server, and the client
(`Request.Client.ID`) at the exchange's token endpoint. There it caps how
many DPoP-bound exchanges one client makes a second, so a busy client meets
it first.

To raise it, raise `EntriesPerHolder` (and, for `dpop.MemoryReplayCache`,
`MaxEntries` with it: a full cache refuses every holder), or shorten
`Window`, which also narrows how far a client's clock may drift. The share
also bounds what one holder costs the cache, so size it for the busiest
holder you expect, not without bound.

## Narrow offline (Biscuit)

`Delegate` narrows a capability, but needs the issuer. The Biscuit form of
the same capability (Biscuit v3) can be narrowed by whoever holds it, with
no key and no network, and a narrowing cannot be undone. An agent in
another language narrows it with any Biscuit library, writing the facts
[biscuit-profile.md](biscuit-profile.md) lists.

The form lives in the module `github.com/oleg-tkachuk/limes/biscuit`, so a
service of JWT capabilities alone resolves neither biscuit-go nor protobuf.
Give `biscuit.Form()` to both sides: `IssuerConfig.Biscuit` and
`BiscuitConfig.Form`.

```go
// granted: what the verifier accepted from the capability's JWT
full, _ := issuer.Biscuit(ctx, limes.BiscuitRequest{Capability: granted}) // same ID, caveats and expiry

// An agent, offline, hands a worker a smaller token:
worker, _ := biscuit.Attenuate(full.Token, limes.Attenuation{
    Ops:              []limes.Op{limes.OpGet},
    ResourcePrefixes: []string{"corpus/public/"},
    ExpiresAt:        time.Now().Add(10 * time.Minute),
})

// A verifier built with a VerifierConfig.Biscuit returns the narrowed capability.
// verifier, _ := limes.NewVerifier(limes.VerifierConfig{ /* … */
//     Biscuit: &limes.BiscuitConfig{Revocations: cache, Form: biscuit.Form()}})
granted, err := verifier.Verify(ctx, limes.VerifyRequest{Token: worker, Audience: "my-service"})
```

An attenuation block may restrict the operations, resources, audience and
expiry, and bind a key to an unbound token. The verifier folds each block
in through `Narrows`, so an attenuated token is checked exactly like a
delegated one. A block that asks for more than the token has, or holds
anything else (Datalog rules and checks included), makes the whole token
invalid: `ErrBiscuitAttenuation`, which matches `ErrInvalidToken`. A later
`ExpiresAt` is ignored, not refused. A copy spends its capability's budget
and request count, and revoking the capability revokes every copy.

`BiscuitConfig.Revocations` is required: pass the same `RevocationCache`
as `VerifierConfig.Revocations`, built with the store as
`RevocationCacheConfig.Biscuits` too, so one `Clear` reaches both kinds of
answer. `NewVerifier` refuses a cache built without it.

The Biscuit seals an ordinary signed token, so a KMS-held signing key
works. That token carries the root of the Biscuit's signature chain, whose
private half is discarded once the Biscuit is built. A sealed token
presented on its own is refused, so it cannot be lifted out to shed an
attenuation, and it cannot be re-wrapped in a fresh Biscuit.

### A copy's own limits

`Attenuation.Requests` and `Attenuation.Budget` give a copy limits of its
own (nil sets none; a ceiling of 0 freezes the copy). A copy's `Budget`
names no `UnitCode`: it is in its capability's unit, which the verifier
supplies. Each limit is counted under the capability and the revocation id
of the block that set it, so siblings narrowed apart count apart while the
capability's own limits still bound them all. A limit must fit every limit
already in force, and an `Unlimited` one is refused: a copy only narrows.

- **Admitting them.** A verifier admits such a copy only with
  `BiscuitConfig.MeterCopies` set, which says the `Meter` behind it counts
  copies. Without it the token is refused with `ErrCopyCountersNotMetered`
  — a configuration error, answered 500 — once it has passed every other
  check.
- **Counting them.** The verifier returns the limits as
  `VerifiedCapability.Copies`, and `MeterSubject()` carries them to the
  `Meter`. `verifier.BiscuitCopy` names the limits in force on a copy, on
  any verifier. A `meter.CopyUsageReader` (memstore's and pgstore's usage
  stores) reads what each copy has counted: `CopyUsage` takes up to
  `meter.MaxCopyUsageIDs` revocation ids of one capability's copies and
  returns the counters of those used; a copy never used is absent.
- **How many.** A holder mints copies offline, as many as it likes, so the
  issuer bounds how many a meter counts: `Caveats.Copies`, a `CopyQuota`
  signed into the token. Its zero value counts no copy with limits of its
  own, `CopyQuota{Unlimited: true}` as many as the store allows, and a
  delegated child no more than its parent. It bounds the copies of the
  capability and everything delegated from it together, as the request and
  budget ceilings do. A store also counts no more than its own bound,
  `MaxCopiesPerCapability` (`meter.DefaultMaxCopiesPerCapability`, 1 024,
  in memstore's `UsageStoreConfig` and pgstore's `Config`). `Bump`,
  `Charge` and `Reserve` refuse a call that would pass either with
  `meter.ErrCopyLimitExceeded`, a kind of `ErrRequestLimitExceeded`, and
  count nothing. A copy already counted goes on being metered.
- **After a drop.** A copy stays counted under every ancestor once its own
  capability's usage is dropped (`DeleteUsage`, `PurgeOrphans`), as its
  requests and spend do; the dropped capability restarts its own count of
  copies with its other counters.
- **One id, two capabilities.** biscuit-go signs a block without the
  signature before it, so a holder of two Biscuits can graft a block of
  one onto the other, and one revocation id then turns up under two
  capabilities. Each counts it apart; neither's metering, refund or
  deletion reaches the other's.

A copy cannot delegate: a server-side child would be an ordinary token of
the capability, which revoking the copy would not reach and the copy's
limits would not bound. `Delegate` refuses a capability read from a Biscuit
(`VerifiedCapability.FromBiscuit`) with `ErrInvalidRequest`.

### Revoke one copy

`verifier.BiscuitCopy(ctx, token)` checks the token is genuine — not its
expiry, audience or revocation, so a copy can be revoked in any state — and
returns the copy: its capability and the revocation id of its last block,
which only it pairs, so a caller cannot name another tenant's copy under
its own capability. Pass it to `Revoker.RevokeBiscuit`
(`RevokeBiscuitRequest{Copy: …}`), which a `BiscuitRevocationStore`
carries out ([Revoke](#revoke)). That stops the copy and every copy
attenuated from it; the copy it came from, its siblings and the
capability's JWT keep working.

It is idempotent. The `RevokeBiscuitResult` says `AlreadyRevoked` when the
copy had an entry before the call, and carries the `Revocation` that
stands, the first written. `GetBiscuitRevocation` reads it back by the same
copy. The revocation belongs to that capability and goes only with its
record, while a token carrying the id is refused under any capability.

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

The subject carries the capability's id, tenant, ceilings, copy quota and
unit, and the Biscuit copy's limits, all as the token states them. A
request has no field to type a ceiling into, or to leave at zero and so
lift one.

`Charge` checks, in order, each Biscuit copy's own budget, the
capability's, each ancestor's (all `ErrBudgetExceeded`), then the tenant
aggregate (`ErrTenantBudgetExceeded`). If any refuses, no counter moves, so
a retry after a refusal is safe. `Meter.Bump` counts requests with the same
subject, against the request ceilings of the same copies, capability and
ancestors; past one it is `ErrRequestLimitExceeded`.

`receipt.ChargeID` names the ledger row. Refund against it:

```go
usage.Refund(ctx, meter.RefundRequest{ChargeID: receipt.ChargeID, Amount: limes.MustParseAmount("0.10")}, nil) // partial
usage.Refund(ctx, meter.RefundRequest{ChargeID: receipt.ChargeID, Remaining: true}, nil)                       // the rest
```

A refund returns spend to every counter the charge took it from, and never
more than the charge. It skips the tenant aggregate when the charge fell in
an accounting period since closed with `ResetSpend`, and a counter
restarted since (`DeleteUsage`). `Remaining: true` refunds what is left, so
a retried full refund is a no-op. A request with neither `Amount` nor
`Remaining`, or both, is `ErrInvalidRequest`; more than is left is
`meter.ErrRefundExceedsCharge`. `RefundReceipt.Amount` says what moved, and
the receipt names the charge's tenant and unit.

### One unit per counter

Every counter counts in one unit. A charge or reservation in another unit
than the capability, an ancestor or the tenant already counts in is
`ErrUnitCodeMismatch`, never a sum of two currencies.

- **The unit is part of the ceiling.** A `BudgetCeiling` that lets anything
  be spent — a `Max` above zero, or `Unlimited` — names its `UnitCode`;
  one that does not is `ErrInvalidRequest` (`ErrInvalidCaveats` in
  caveats). Only the zero ceiling may name none: such a capability counts
  requests (`Bump`) but no amount, and its `Charge`, `Reserve` and `Settle`
  are `ErrInvalidRequest`.
- **The tenant aggregate** counts from its first charge or reservation, in
  that unit, whether or not a ceiling is set. Until one is set its ceiling
  reads `Unlimited`; a ceiling of 0 freezes its spend.
- **`SetTenantBudget`** measures a new ceiling against the spend and holds
  already counted. It refuses a ceiling in another unit while anything is
  held in the old one, or spent in it and not reset (`ResetSpend`). It is
  guarded by `ExpectedVersion` — 0 while no ceiling has been set, else the
  `ResourceVersion` read — and a conflict is
  `meter.ErrTenantBudgetVersionMismatch`.

To find the tenants near their ceiling, `ListTenantBudgets` takes a
`Filter`: `meter.TenantBudgetFilterAll` (the default),
`TenantBudgetFilterUnlimited`, or `TenantBudgetFilterAtOrPast` with a
`ThresholdBasisPoints` of 0..`meter.BasisPointsPerWhole` (hundredths of a
percent: 110 is 1.1%), compared in integers. Rows come most at risk first:
ceilings of 0, then bounded ceilings by spent / max descending, unclamped,
then unlimited ones; ties by tenant id. `TenantBudget.Utilization()`
reports the same percentage clamped to 0..100, and `limited` false for an
unlimited tenant. A tenant never charged and never given a ceiling has no
row: `GetTenantBudget` is `meter.ErrTenantBudgetNotFound`.

## Amounts are exact

Every amount is `limes.Nanos`, an `int64` count of billionths of the unit:
0.35 USD is `350_000_000`. Sums, ceilings, holds and refunds are integer
arithmetic, exact however many charges they take: a tenth and two tenths
fit under a ceiling of three tenths, and ten tenths make one.

- **Writing one.** `ParseAmount("0.35")` reads a decimal exactly — digits,
  an optional fraction and an optional exponent (`1e-7`), no sign — and
  refuses anything finer than a nano rather than round it.
  `MustParseAmount` does the same for a literal in your code, panicking on
  what `ParseAmount` refuses. A
  price that arrives as a `float64` goes through `AmountFromFloat`, which
  rounds once, to the nearest nano.
- **Reading one.** `String()` writes the exact decimal (`"0.35"`);
  `Float64()` is for display and metrics only.
- **In configuration.** `Nanos` implements `encoding.TextMarshaler` and
  `TextUnmarshaler` with the same exact decimal, so a YAML scalar or an
  environment variable carries an amount with no `float64` in between.
- **Range.** Up to `MaxNanos`, just under a billion units. An amount
  outside it is `ErrInvalidAmount`, which matches `ErrInvalidRequest`; a
  valid amount that would take a counter past it is
  `meter.ErrCounterOverflow`.
- **Units.** `BudgetCeiling.UnitCode` is an ISO 4217 code in upper case
  (`IsUnitCode`). `XXX` (`AbstractUnitCode`), ISO 4217's "no currency", is
  the code for a budget that is not money.

In a token the budget is a decimal in a JSON string
(`"max_budget": "1.5"`), so a reader whose numbers are float64 — JavaScript's
`JSON.parse` among them — cannot round it.

## Reserve before a cost is known

An LLM call is priced by the tokens it ends up using. Hold an estimate
first, then settle the actual cost:

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

A hold counts against every ceiling — each presented Biscuit copy's, the
capability's, each ancestor's, the tenant's — exactly as spend does, so two
callers cannot both hold the last of a budget. `Settle` charges the actual
cost in place of the hold. Above the hold, the excess must fit, or the hold
stays for you to settle lower or `Release`.

- **Expiry.** A hold never settled lapses after its TTL
  (`meter.DefaultReservationTTL`, five minutes, unless the request says) or
  when its capability expires, whichever is sooner. In `pgstore` that is up
  to `limes.MaxLeeway` plus `limes.PurgeClockSkew` past the expiry, the
  longest a verifier may still accept the token. Revoking the capability
  does not end a hold: the cost may already be incurred, and can still be
  settled. Run `ReleaseExpired` on a timer, again while a batch comes back
  full (`DefaultHousekeepingLimit` unless set).
- **Which holds settle.** Settling the zero reservation id, a hold of
  another capability than the subject's, or one made under other Biscuit
  copies than the subject carries, is `meter.ErrReservationNotFound`: a
  hold is settled by the copy that made it.
- **Reading holds.** `GetReservation` reads a hold while it still counts —
  open, or expired and not yet released — and is
  `meter.ErrReservationNotFound` once settled or released.
  `ListReservations` pages a capability's own holds, soonest to expire
  first.
- **Receipts.** The receipt names the charge's tenant and unit, on a
  refusal by a ceiling or by the callback too, so a caller can attribute it
  from the receipt alone.

### Commit side effects with the charge

The trailing `nil` is an optional callback that runs inside the call. If
your store has transactions, its context carries the store's transaction,
which the store's own accessor reads, so your side effects commit
atomically with the spend. It is handed the receipt the call will return:

```go
usage.Charge(ctx, req, func(ctx context.Context, c meter.Charged) error {
    tx, _ := pgstore.TxFrom(ctx) // commits with the charge
    return myOutbox.Enqueue(ctx, tx, c.Receipt.ChargeID, req.Amount)
})
```

`Settle` takes the same callback; `Refund` takes its own, on the same terms,
and is handed a `meter.Refunded`:

```go
usage.Refund(ctx, req, func(ctx context.Context, r meter.Refunded) error {
    tx, _ := pgstore.TxFrom(ctx) // commits with the refund
    return myOutbox.Enqueue(ctx, tx, r.Receipt.ChargeID, r.Receipt.Amount)
})
```

- An error from the callback rolls the call back whole.
- It must not call back into the `Meter`: a store may hold its counters
  while it runs. Keep it short, on the store's own database, with no
  network call.
- Whether the call's own rows are there to read yet is the store's:
  `pgstore` writes them before the callback, so an outbox row may reference
  the ledger row; `memstore` after.
- It does not run on a replay, nor for a refund of nothing (`Remaining`
  once nothing is left). A keyed refund whose answer was lost
  (`meter.ErrOutcomeUnknown`) is retried under the same `RefundID`: it
  either replays — its event committed with it — or refunds and runs the
  callback then.

The module never names the transaction's type, which is why this library
has no database dependency. A store with no transactions takes `nil`.

## Charge a cost reported after the fact

Some costs never pass through your verifier: a call made straight to a
provider whose price arrives later, in a usage record or on an event stream
delivered at least once.

**It may arrive twice.** Name it with the id your records already give it.
The capability is charged once per name while that charge stays in the
ledger — at least `meter.MinChargeRetention`, 401 days. A repeat returns
the first receipt with `Replayed` set and runs no callback. A name arriving
again with a different amount, unit or tenant, or under other Biscuit
copies, is `meter.ErrExternalRefConflict`: one name is one cost.

```go
subject, err := limes.StoredMeterSubject(ctx, records, capID) // no token in hand
receipt, err := usage.Charge(ctx, meter.ChargeRequest{
    Subject:     subject,
    Amount:      cost,
    ExternalRef: event.CallID,
    Overrun:     meter.OverrunPolicyRecord,
}, nil)
```

**It has already been spent.** Refusing it would only leave the ledger
short. `OverrunPolicyRecord` charges it past every ceiling it crosses and
sets `receipt.Overrun`. The crossed ceiling then refuses every later charge
and reservation made with the default `OverrunPolicyReject`, which stops
the next cost before it is incurred. Keep `OverrunPolicyReject` for
anything you can still decline.

With no token in hand, `StoredMeterSubject` reads the subject from the
capability's record through a `RecordReader` (any `Store` is one), as the
meter reads every ancestor's ceiling. Keep records
(`PurgeRequest.ExpiredFor`) as long as costs may arrive: the `Meter`
refuses a new charge for a capability not on record with `ErrNotFound`,
since no ancestor's ceiling could be read. A repeat is still answered once
the record is purged, while the charge stays in the ledger.
`ChargeByRef` reads a named charge back, so a reporter can reconcile its
records with the ledger.

A settle is idempotent on its reservation in the same way: settling one
twice for the same amount returns the first charge, and for another amount
is `meter.ErrSettleConflict`. Past the ledger's retention the settled
reservation is `meter.ErrReservationNotFound`.

A price not known yet is not a price of zero: hold the estimate and settle
when it is known, or let the hold lapse and count against the ceilings
until `ReleaseExpired` runs. `Reserve` refuses a capability whose record
has expired (`ErrExpired`) — in `pgstore`, only once no verifier could
still accept its token — so charge a cost reported after that with
`OverrunPolicyRecord`.

## Retrying a lost answer

A store over a database can lose its answer after the call's commit has
left: the connection drops in a failover, or the context ends, before the
database replies. The call then returns an error matching
`meter.ErrOutcomeUnknown` (`outcome_unknown`, 503), wrapping the cause: it
may have committed, or not. Send it again only when it is keyed to its
cost, so that a repeat of one that committed is answered as a replay:

| Call | Key | A repeat | The same key for another request |
|---|---|---|---|
| `Charge` | `ExternalRef` | the first receipt, `Replayed` | `meter.ErrExternalRefConflict` |
| `Reserve` | `ReservationID` of your own | the hold on record, `Replayed`, its expiry unchanged | `meter.ErrReserveConflict` (another amount, unit, op, actor, tenant, capability or Biscuit copy, or a hold already settled) |
| `Refund` | `RefundID` of your own | the first receipt, `Replayed`, with what that refund returned | `meter.ErrRefundConflict` (another charge or amount) |
| `Settle` | its reservation | the first charge | `meter.ErrSettleConflict` |

A released hold frees its `ReservationID` to hold anew. A `RefundID` goes
with its charge when the ledger removes it.

```go
id := uuid.New() // kept with the work it pays for, before the first try
req := meter.ReserveRequest{Subject: subject, Amount: estimate, ReservationID: id}
hold, err := usage.Reserve(ctx, req)
if errors.Is(err, meter.ErrOutcomeUnknown) {
    hold, err = usage.Reserve(ctx, req) // a hold that committed comes back Replayed
}
```

An unkeyed call sent again may count twice; one not sent again may never
count. memstore commits in memory and never returns `ErrOutcomeUnknown`.

## Lease a sub-agent its budget

Every charge locks the counters of the whole chain and the tenant's
aggregate, so the charges of many sub-agents under one orchestrator queue
on the same rows. A usage store that is a `meter.Leaser` — memstore's and
pgstore's are — lets a capability hold part of its chain's budget in
advance ([ADR-0017](adr/0017-budget-leases.md)):

```go
leaser := usage.(meter.Leaser)
lease, err := leaser.Lease(ctx, meter.LeaseRequest{Subject: sub.MeterSubject(), Amount: limes.MustParseAmount("2.00")})
```

`Lease` holds the amount against every ancestor and the tenant at once,
checking every ceiling as a `Reserve` of it would. From then on, a `Charge`
of the leaseholder, or of a capability delegated from it, with the default
`OverrunPolicyReject`, locks only the counters up to the leaseholder and
the lease's balance; the ancestors above and the tenant are not touched.

- **Exhausted.** A charge the balance does not cover is
  `meter.ErrLeaseExhausted`, a kind of `ErrBudgetExceeded`: lease more and
  charge again. A charge refused as `limes.ErrConflict` met a lease moving
  under it, and moved nothing: send it again.
- **Settled.** `SettleLease` charges what the lease spent to the ancestors
  and the tenant and releases the rest. So do `ReleaseExpired` once it
  expires (`LeaseRequest.TTL`, `meter.DefaultLeaseTTL` unless set, at most
  the capability's own expiry), `ReleaseMatching` once its capability is
  revoked, `DeleteUsage` and `PurgeOrphans` before its counters go, and a
  `SetTenantBudget` that resets the spend, into the period it closes.
- **Reads.** Until settled, an ancestor's `GetUsage` and the tenant's
  aggregate show the lease as reserved and its spend not yet; the ledger,
  and `SumCharges`, have every charge at once.
- **Not under a lease.** A capability whose ancestor holds a lease cannot
  lease (`meter.ErrLeaseNested`), and a Biscuit copy cannot lease at all
  (`limes.ErrInvalidRequest`): lease for the capability it was attenuated
  from. A charge that records an overrun, a charge of a Biscuit copy with
  limits of its own, and `Reserve`, `Settle` and `Release` go through the
  whole chain as without a lease: correct, since the lease holds its share
  of every ceiling, but not faster.

Lease a few minutes of a sub-agent's expected spend, and top it up when
half is gone. A top-up — `Lease` on a capability whose lease is open —
settles that lease and opens a new one in the same call, so the ancestors'
and tenant's `SpentAmount`, which budget listings read, lags by at most one
lease. The new lease holds what the old one had left plus the top-up, for
the request's `TTL` from now, under a new `ID`. A refund of a charge taken
before the top-up goes to the chain the settlement charged; one taken
after, to the new lease. A top-up refused at a ceiling leaves the open
lease as it was.

A lease holds requests too, for a sub-agent whose every call is a `Bump`:

```go
lease, err := leaser.Lease(ctx, meter.LeaseRequest{Subject: sub.MeterSubject(), Requests: 100})
```

`Requests` are held against every ancestor's request ceiling at once, and
shown in their `GetUsage` as `ReservedRequests`, which a `Bump` outside the
lease counts as made. A `Bump` of the leaseholder, or of a capability
delegated from it, then counts only up to the leaseholder; one with no
leased requests left is `meter.ErrLeaseRequestsExhausted`, a kind of
`ErrRequestLimitExceeded`. Settled, the requests made count on the
ancestors and the rest are released. One lease may hold both an amount and
requests, each covering only its own calls: a lease of no amount covers no
`Charge`, one of no requests no `Bump`. A `Bump` of a Biscuit copy with
limits of its own goes through the whole chain.

pgstore opens leases unless `Config.DisableLeases` is set. A release before
pgstore/v0.58.0 reads none of their state, so set it while one still
writes the database, and before going back to one, then settle every lease
([migration notes](migration.md)). With it set, `Lease` is
`meter.ErrLeasingDisabled`; leases already open are still charged under and
settled.

## Observe the meter

The nested module `github.com/oleg-tkachuk/limes/otelmetric` — a module of
its own, so limes resolves no OpenTelemetry dependency — wraps a usage
store in OpenTelemetry instruments (`otelmetric.Instrument`): charges,
settles, reservations and request bumps by outcome and by the ceiling that
refused them, amounts by unit, and a capability's spend after each charge.
`otelmetric.NewStoreEvents` records `limes.tenant.wait`, how long each
metered call waited for its tenant's aggregate row: the length of a
tenant's queue, and so the sign that its calls should lease. The
instruments, attributes and options are in
[the package's reference](api/observing.md#package-otelmetric).

`otelmetric.Instrument` keeps every optional `meter` interface the wrapped
store implements — `CopyUsageReader`, `TenantUsageEraser`,
`MatchingReleaser`, `TenantReservationLister`, `Leaser` — and forwards
`meter.FeatureGate`. A decorator of your own must do the same: assert each optional interface on the store it
wraps and forward it, or a caller checking for it finds it gone.

## Observing the issuer, verifier and revoker

The issuer, the verifier and the revoker log through `log/slog`
(`IssuerConfig.Logger`, `VerifierConfig.Logger`, `RevokerConfig.Logger`;
nil logs to `slog.Default()`). A capability is named by its id, issuer,
kid, subject and tenant; no line holds a token, a key or a proof.

| Event | Level |
|---|---|
| `Issue`, `Delegate`, `Reissue`, `Biscuit` minted a token | INFO |
| The revoker's `Revoke`, `RevokeMatching`, `RevokeBiscuit` carried out, a repeat that wrote nothing among them | INFO |
| The issuer or the revoker's store refused a request (a `Code` of limes's) | WARN |
| A token accepted | DEBUG |
| A token refused after its signature verified: expired, revoked, unrecorded, another audience, no proof, outside its caveats | WARN |
| A token refused before: absent, malformed, forged, an unknown kid | DEBUG |
| A store, signer, key set or revocation lookup failed | ERROR |

Only the holder of a genuine token can cause a refusal after its signature
verified, so that is worth a warning. Garbage costs nothing to send, so it
stays at DEBUG, where a flood of it fills no log. Each line carries `op` and
`duration`, and a refusal its `code`, `reason` and `error`.

A revocation's line names the capability (a Biscuit copy by its capability
alone), or a bulk revoke's tenant and selectors, with the request's `actor`
and `note`, and what the store answered: `already_revoked` and the `prior`
disposition, or how many it `revoked`, in which `tenants`, and whether
there is `more`. A store call that returned an error may still have
written, so a `Revoker` given a cache clears it as on success, and its line
says `cache_cleared`. A `Revoker` built without the store a method needs
(`RevokerConfig.Bulk`, `RevokerConfig.Biscuits`) fails that method with
`ErrInvalidConfig`, logged at ERROR.

`IssuerConfig.Events`, `VerifierConfig.Events` and `RevokerConfig.Events`
call back beside the log, for metrics or an audit sink:

| Side | Callbacks |
|---|---|
| `IssuerEvents` | `OnIssued`, `OnRefused`, `OnFailed` |
| `VerifierEvents` | `OnVerified`, `OnRefused`, `OnFailed` |
| `RevokerEvents` | `OnRevoked`, `OnRefused`, `OnFailed` |

Each outcome calls exactly one callback; a nil one drops its events. An
audit consumer that must see every call sets every callback of the side it
audits. Each takes one event struct, so a field added later breaks none,
and each event carries `Duration`, the call's elapsed time on the monotonic
clock. Callbacks run on the caller's goroutine before the method returns:
they must not block or panic.
[ADR-0021](adr/0021-observing-issuer-and-verifier.md) records why.

```go
verifier, err := limes.NewVerifier(limes.VerifierConfig{
    // ...
    Logger: logger,
    Events: limes.VerifierEvents{
        OnRefused: func(ctx context.Context, e limes.VerifierErrorEvent) {
            refusals.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", string(e.Reason))))
        },
    },
})
```

## Check your own store

Much of each contract lives in request fields and in which sentinel comes
back, so a store that gets one wrong still compiles. Run the module's
suites from your store's tests:

| Suite | For |
|---|---|
| `storetest.Run` | a `Store`, and a `BiscuitRevocationStore`; `Env.Census`, `Lister`, `Exchanges` and `BulkRevoker` turn on the checks of those interfaces (`Census` and `Lister` with `Now` set) |
| `metertest.Run` | a `meter.UsageStore` |
| `metertest.RunCounter` | a store that is a `meter.RequestCounter` alone |
| `jwks/jwkstest.Run` | a `KeyResolver` |
| `dpop/dpoptest.RunReplayCache`, `RunNonces` | a `dpop.ReplayCache`, `dpop.Nonces` |

```go
func TestMeterContract(t *testing.T) {
    metertest.Run(t, func(t *testing.T) metertest.Env {
        return metertest.Env{Ctx: ctx, Usage: newStore(t), Tenant: tenant,
            NewCapability: recordCapability,
            Advance: clock.Advance} // optional: reservation-expiry checks, and with Ledger and Now the ledger's bucket checks
    })
}
```

`Ctx` is the context of every call for `Tenant`. `NewCapability` records
each spec's ceilings and copy quota (`spec.CopyQuota()`) on the record, as
the `Meter` reads an ancestor's from it. Every other field is optional and
skips its checks while unset; each is described on
[`metertest.Env`](api/conformance.md#metertest.Env) and
[`storetest.Env`](api/conformance.md#storetest.Env). The ones a store
usually needs:

- **A store that scopes tenants** — pgstore's row-level security, say —
  sets `NewTenant`, `TenantCtx`, `ListCtx`, `AllTenantsCtx` and
  `ScopesTenants`. The suites then check that a call for `Tenant` under
  another tenant's context is `ErrScopeMismatch` and moves nothing. With
  `ReadOnlyCtx`, a context that reads every tenant and writes none, they
  check that every write under it is `ErrScopeMismatch`, changes nothing
  and runs no `onCharged` or `onRefunded`.
- **Clocks.** `Now` and `Advance` read and move the store's clock. A store
  that measures expiry by another clock — `pgstore` with no `Config.Now`,
  by the database's — sets `ExpiryNow` and `AdvanceExpiry` too.
- **Optional interfaces.** `Ledger` turns on the `LedgerReader` checks,
  `Revoke` the `MatchingReleaser` ones, `DeleteCapability` the
  `PurgeOrphans` ones, `MaxCopiesPerCapability` the check of the store's
  copy bound, and `DeleteTenant` (`storetest`) those of `ErrUnknownTenant`
  and `ErrTenantDeleted`.

`storetest.Env` shares `NewTenant`, `TenantCtx`, `AllTenantsCtx`,
`Advance`, `Now`, `ScopesTenants` and `ReadOnlyCtx` with `metertest.Env`,
so one setup can serve both.

The checks race charges against one ceiling, so run them against the
database itself: a store that reads a counter and writes it back outside
one statement or lock overspends, and fails. Give the store a pool of one
or two connections there: a call that takes a second connection while it
holds the first deadlocks once as many calls run as the pool has
connections. Each check is bounded by `CheckTimeout` (a minute unless set);
one that outlives it stops the test binary with every goroutine's stack,
naming the check.

The suites grow: a minor release may add a check of a promise the contract
already made, which a store that broke the promise then fails. That is the
suite finding a bug, not a break of the API; [migration.md](migration.md)
lists each addition. A new optional `Env` field skips its checks while
unset, so an `Env` written for an earlier release still compiles and runs
everything it ran before.

`memstore` and `pgstore` run the same suites from their own tests —
`pgstore` against PostgreSQL in a container, as a role that owns no table,
on a pool of two connections — and `pgstore` and `redisreplay` run
`dpoptest` against their replay caches.

## Revoke

Revoke through a `Revoker`, the admin plane's component beside the issuer
and the verifier. It holds no signing key: a service that revokes needs the
store and nothing more. Each method carries the request to the store,
returns its answer unchanged, and logs it with an event — revocation is the
audit event that matters most.

```go
revoker, err := limes.NewRevoker(limes.RevokerConfig{
    Store:    records,          // a RevocationStore: any Store is one
    Bulk:     records,          // optional: RevokeMatching
    Biscuits: records,          // optional: RevokeBiscuit
    Cache:    cache,            // optional: this process's RevocationCache
    Logger:   logger,
})
result, err := revoker.Revoke(ctx, limes.RevokeRequest{
    CapabilityID: cap.ID, Note: "agent looping", Actor: "operator@example.com",
    CascadeChildren: true,   // also records each sub-agent's revocation, for the audit
})
```

Revoking a capability revokes everything delegated from it, with or
without `CascadeChildren`: `IsRevoked` answers for the whole chain. The
flag also writes an entry per descendant, so the audit trail names each
capability stopped.

- **Idempotent.** A repeat keeps the first entry. `result.AlreadyRevoked`
  says the capability had one before the call, and `result.Revocation` is
  the entry that stands, so a caller learns whether it stopped the
  capability without a read beforehand, which a concurrent revoke could
  outdate.
- **`result.PriorDisposition`** is the capability's disposition just
  before the call, by the census's rules and clock: only a revoke of an
  `active` one stopped something new. One already revoked through an
  ancestor, or expired, still gets its own entry, for the audit and in case
  the ancestor's is purged.
- **Not on record.** `ErrNotFound`, writing nothing.
- **Reading it back.** `GetRecord` returns the record with `IssuedBy` and
  its own `Revocation`, whose `Cascaded` says a cascading revoke wrote it.
  A capability stopped only through an ancestor has none: ask `IsRevoked`
  whether it verifies. `ListByPrincipal` leaves expired and revoked records
  out unless `IncludeExpired` or `IncludeRevoked` asks for them.
- **Cost.** A store may do work per descendant: pgstore marks each one
  revoked, so a revoke's cost grows with the subtree. It first takes the
  tenant's revoke lock alone, which every insert of a child shares, so
  delegations in the tenant wait for it; charges neither wait for it nor
  hold it back ([`pgstore.Store.Revoke`](api/storage.md#pgstore.Store.Revoke)).

Given a `Cache`, the revoker drops what each revocation outdates as soon as
the store has carried it out: `ClearRevoked` of the capability after
`Revoke`, reaching every descendant the cache holds an answer for, and
`Clear` after `RevokeMatching`, whose result names no capability, and after
`RevokeBiscuit`. That is this process's cache alone. A verifier in another
process learns of the revocation through `pgstore.RevocationWatcher`, or
once its cache's TTL runs out.

A chain is at most `MaxDelegationDepth` (64) ancestors deep. Past it
`Delegate` returns `ErrDelegationDepthExceeded` (403,
`delegation_depth_exceeded`) and no token; the parent stays valid, so
delegate from a shallower capability. A deeper chain, which only a store
that does not refuse one can hold, is refused at verification as
`ErrDelegationTooDeep`, a kind of `ErrRevoked` (401).

### Cache revocation answers

A verifier asks about revocation on every call. A `RevocationCache` keeps
the answers, so the per-call cost stays flat:

```go
revocations, err := limes.NewRevocationCache(limes.RevocationCacheConfig{
    Capabilities: records,
    Biscuits:     records, // if the verifier reads Biscuits
})
```

A revocation reaches a cached answer when the answer's TTL runs out
(`DefaultRevocationCacheTTL`, two seconds), or at once when you call
`Clear` or `ClearRevoked` as a revocation is announced.

- **`Clear`** drops the whole cache: an answer cached for a descendant is
  also an answer about the revoked ancestor.
- **`ClearRevoked(ids...)`** drops only what a revocation of `ids`
  outdates — their answers and those of every descendant the cache holds
  one for — when the store is a `RevocationLineageLookup`, which names the
  ancestors each answer covers (memstore's and pgstore's `Store` are).
  Over any other store it is `Clear`. Every other answer stays, and so do
  lookups in flight that touch no revoked id, so the notifications of a
  bulk revocation do not stop every other check from being cached.
- **A decorator** that embeds `limes.Store` has no `RevocationLineage` to
  promote, and silently turns `ClearRevoked` into `Clear`. Forward the
  method. Set `RevocationCacheConfig.RequireLineage` to have
  `NewRevocationCache` refuse an upstream without it
  (`ErrNoRevocationLineage`); `Stats().Lineage` says which the cache has,
  and `Stats().ClearsWithoutLineage` counts the `ClearRevoked` calls that
  cleared whole for want of it.

An answer is fresh for the TTL from when the store was asked, however long
the store took. Choose the TTL from how often one token is verified, not
from a request burst: the two-second default serves the requests of one
HTTP exchange, while an agent calling a tool once per model step, seconds
or minutes apart, finds every answer expired.

A long TTL is safe only while a revocation signal reaches the cache, so the
cache has two bounds:

- **`TTL`** serves answers while no signal vouches for the cache. Keep it
  as short as a revocation may go unseen.
- **`SyncedTTL`** (at least `TTL`, at most `MaxRevocationCacheSyncedTTL`,
  a day) serves them while every open revocation signal listens. Set it a
  little above the interval between two verifications of one token.

A signal is a `RevocationSignal`, which `pgstore.RevocationWatcher` opens on
every cache that takes one (`RevocationSignalTarget`) when `Run` starts, or
which a signal of your own — a message bus, another database — opens with
`RevocationCache.NewSignal`. Through it the source says `Listening` and
`NotListening` as its connection comes and goes, `Revoked` or `RevokedAll`
per notification, and `Renew` while it knows itself listening.

- **Leases.** `Listening(lease)` vouches for the cache for a lease, which
  `Renew` extends. A signal that stops without `NotListening` — its
  goroutine stuck or gone — lets the lease lapse, and the cache falls back
  to `TTL` (counted in `Stats().SyncsLost`).
- **Losing the signal.** `NotListening` cuts short the answers cached
  under `SyncedTTL` at once.
- **Regaining it.** `Listening` drops nothing: an answer read before it
  may have missed a revocation made while nothing listened, so it stays on
  `TTL`. Only answers read afterwards are served for `SyncedTTL`. A
  flapping connection thus leaves answers in place rather than send every
  verifier to the store as it recovers.
- **Several signals** — one watcher per database, say — hold `SyncedTTL`
  only while every one listens. One started later is not waited for, so
  start them together.

`Stats()` reports, for each kind of answer, `Hits`, `Misses`, `Lookups`,
`Evictions` (growing: `MaxEntries` is too small), `Entries` and `Stale`,
plus `Synced` and the signals' counts ([`RevocationCacheStats`](api/revocation.md#limes.RevocationCacheStats));
`otelmetric.InstrumentRevocationCache` exports them. `Sweep` drops expired
entries on a timer of yours; it is optional, since lookups skip and
replace them anyway.

The cache fails closed: an upstream that fails is a failed check, and the
verifier refuses. `MaxStale` (at most `MaxRevocationCacheStale`, an hour)
trades that for availability through a short store outage: an answer past
its TTL stands in for a failed lookup for as long again, counted in `Stale`
and logged at WARN. A revocation the store holds then goes unseen for up
to `MaxStale`. `ErrNotFound`, an answer, is never covered, nor a caller
whose context ended, and a revocation the cache already learned is never
undone by a stale answer ([`RevocationCacheConfig.MaxStale`](api/revocation.md#limes.RevocationCacheConfig)).

Point the store a `RevocationCache` reads, and the watcher's connection, at
the primary. A standby applies a revocation after the primary commits and
notifies it, so a cache cleared by the notification can read "not revoked"
from a standby still behind and keep it for its `SyncedTTL`; and a standby
refuses `LISTEN`, so a watcher pointed at one never listens. Reports —
`SumCharges`, `CountRecords`, `ListRecords` — may read a replica.

### Propagate revocations with pgstore

Run a `pgstore.RevocationWatcher` beside each verifier. The schema notifies
`pgstore.RevocationChannel` on every revocation, naming the capabilities
revoked. The watcher passes each notification to every `RevocationCache`
through its signal, which drops what it outdates; calls `ClearRevoked` on
any other cache that has it (`pgstore.RevokedClearer`); and calls `Clear`
on the rest, on a notification naming no id, and after each connection it
opens.

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

- **Reconnects** wait up to ten seconds between attempts. Before the
  first `LISTEN` and while it reconnects, nothing clears the cache: `TTL`
  alone bounds how long a revoked capability still verifies.
- **A dead connection** — a host gone, a network cut — is found by the
  heartbeat: the watcher pings a quiet connection every `Heartbeat` (30s by
  default) and drops one that does not answer within as long again. It
  renews its signals on every notification and every answered heartbeat,
  for three heartbeats at a time, so a dead connection vouches for the
  caches for at most twice the heartbeat.
- **What a notification names.** A statement that writes no new entry
  notifies nothing. A capability under a parent revoked before is not
  named (its answers name the parent), and a cascade names its top
  capability alone. Ids come `pgstore.MaxNotifiedRevocations` (100) to a
  notification, so a `RevokeMatching` batch at its default limit drops
  only what it outdates. A Biscuit copy's revocation, a statement revoking
  more than `pgstore.MaxNotifiedRevocationsPerStatement` (1 000)
  capabilities, and one revoking a capability the writer's row-level
  security hides notify with no id, and the watcher clears whole.

### Purge expired records

Records do not stay forever. A housekeeping job calls
`Store.Purge(ctx, limes.PurgeRequest{ExpiredFor: retention})` on a timer,
again while a batch comes back full (`Limit`, `DefaultHousekeepingLimit`
unless set). It removes the capabilities expired for longer than the
retention, with their revocation entries and those of their Biscuit
copies; then `UsageHousekeeping.PurgeOrphans` releases what the meter
still holds for them. It never removes a capability while a child stays on
record: `Insert` refuses a child that would outlive its parent, and among
records expiring together `Purge` takes the deepest first.

The retention is at least `MinPurgeRetention` (six minutes, the zero
value's). A verifier accepts a token up to its `Leeway` past expiry, never
more than `MaxLeeway`, by a clock that may run up to `PurgeClockSkew`
behind the store's; a revocation purged inside that window would let the
revoked token through. These bounds are constants, not settings, because
the verifiers and the purging job are configured apart, and a store cannot
see each verifier's leeway. Where clocks may drift further apart than
`PurgeClockSkew`, pass a longer `ExpiredFor`.

### A lookup that sees every record

A verifier reads revocation through its one `VerifierConfig.Revocations`,
whatever issuer minted the token, so that lookup must see the records of
every issuer in `TrustedIssuers`. `IsRevoked` answers a capability it has
no record of as not revoked: a token from an issuer that records in
another store would verify, and nothing revoked there would stop it. The
same holds for records a tenant-scoped role cannot read, and for those a
restore from an older backup lost.

So by default the verifier requires a record. A capability the lookup has
no record of is refused with `ErrUnrecorded` (code and reason
`unrecorded`, 401). It is not `ErrRevoked`, so an operator tells a blind
lookup from a stopped capability, and not permanent: the record may yet
appear. A Biscuit's capability is held to its record as a JWT's is; a
copy's own revocation ids need no record.

- **The lookup** must tell a capability not on record from one not
  revoked: a `RevocationLineageLookup`, as memstore's and pgstore's `Store`
  are, or a `RevocationCache` over one. `NewVerifier` refuses any other
  with `ErrInvalidConfig`. A type of yours that wraps the cache keeps the
  check when it forwards `RevocationLineage`, as in
  [Cache revocation answers](#cache-revocation-answers); over a cache whose
  upstream names no lineage, every verification fails with
  `ErrNoRevocationLineage`. A lookup of your own that answers an
  id not on record as not revoked, against the contract, defeats the check
  unseen; `storetest` checks a store for it.
- **The cache** keeps a no-record answer for its `TTL`, never its
  `SyncedTTL` — a revocation signal says nothing of a record appearing —
  apart from its other answers (`Stats().Unrecorded`). `Clear` drops it,
  and so does `ClearRevoked` of the id.
- **Replicas.** An `Issuer` returns a token only once its record is
  written, so a verifier reading the primary never refuses a token for
  want of its record. One reading a lagging replica refuses a fresh token
  until replication brings the record there, and for up to `TTL` after
  through a cache.

`VerifierConfig.AllowUnrecorded` accepts such a token instead, reading a
capability with no record as not revoked, and accepts a lookup that names
no lineage. Set it only where every trusted issuer records where the
lookup reads.

### Revoke a user, a client or a session

The events an operator acts on name no capability: a user leaves, a client
is compromised, the identity provider ends a session. A store that is a
`limes.BulkRevoker` — memstore's and pgstore's are — revokes every
capability of one tenant that matches the selectors:

| Selector | Matches |
|---|---|
| `Subject` | the principal a capability authorises |
| `OnBehalfOf` | every agent acting for a user |
| `ClientID` | the OAuth client |
| `Session` | the identity provider's session it was issued under |
| `RunID` | the agent run it was issued to (`Subject.Agent.RunID`): a run gone wrong, whose capabilities share no lineage |
| `SubjectIssuer` | the identity provider that authenticated its subject: beside `ClientID` or `Session`, one provider's where another names the same; alone, every capability of a removed provider |

Selectors given together must all match. At least one is required, and
either `TenantID` or `AllTenants`
([ADR-0013](adr/0013-bulk-revocation.md)).

```go
req := limes.RevokeMatchingRequest{TenantID: tenant, Session: sid, Note: "IdP logout", Actor: "backchannel-logout"}
for {
	res, err := revoker.RevokeMatching(ctx, req) // pgstore: under pgstore.WithTenant(ctx, tenant)
	if err != nil || !res.More {
		break
	}
}
```

- **Batches.** Each call writes an entry for at most `Limit` capabilities
  (default `DefaultHousekeepingLimit`) that have none, in order of tenant
  and id, and reports the entries written (`Revoked`), the tenants
  (`Tenants`) and whether more may match (`More`), so no transaction holds
  a tenant's rows for long. Descendants stop through their lineage, as
  with `Revoke`.
- **Races.** A capability issued while the loop runs — by a client still
  holding a token that can delegate — is caught by another pass. Run one
  after the identity provider has stopped issuing for the session.
- **Session and subject issuer are issuance metadata, never claims.**
  `IssueRequest.Session` and `IssueRequest.SubjectIssuer` record them,
  `Record.Session` and `Record.SubjectIssuer` read them back, and every
  delegation takes its parent's. An `exchange.Exchanger` records the
  subject token's `sid` and `iss`, and refuses a policy naming another
  issuer. An empty `SubjectIssuer` in a request matches any issuer, and
  none. A capability issued with neither is reached by subject or client.
  In pgstore, records written before migration 025 have no session, and
  only capabilities an `ExchangeStore` recorded an exchange of, with their
  delegations, have a subject issuer from before migration 039.
- **OIDC back-channel logout.** Verify the provider's logout token, then
  run the loop above with its `sid` and `iss`.
- **Release the holds it stopped.** A revoked capability's open holds keep
  counting against the ceilings until they expire, since a reserved cost
  may still settle. A usage store that is a `meter.MatchingReleaser` —
  memstore's and pgstore's are — releases them at once: `ReleaseMatching`
  with the revocation's own selectors and tenant (or `AllTenants`)
  releases the holds of revoked matches and everything delegated from
  them, never a live capability's
  ([ADR-0015](adr/0015-release-matching-holds.md)). It finds what is
  revoked, not what this call revoked, so a failed release is retried as
  it was. After a `Revoke` of one capability, `CapabilityID` and its
  tenant release its holds and its descendants'. `TenantBudget.ReservedAmount`
  and `Usage.ReservedAmount` sum what holds keep, and a
  `meter.TenantReservationLister` lists a tenant's holds one by one.
- **Preview and audit.** `CountRecordsRequest` and `ListRecordsRequest`
  take the same selectors, so a count names what a revocation would reach
  — tenant by tenant with `AllTenants` and `ByTenant` — and a listing names
  each record. Run again after, the same count reports them revoked.

A client registered once at an authorization server that serves many
tenants holds capabilities in each, and so may a session. Deleting the
client revokes them all with `AllTenants`, under a scope that writes every
tenant ([ADR-0014](adr/0014-bulk-revocation-across-tenants.md)):

```go
for {
	res, err := revoker.RevokeMatching(pgstore.WithAllTenants(ctx), limes.RevokeMatchingRequest{
		AllTenants: true, ClientID: id, Note: "client deleted", Actor: operator,
	})
	if err != nil {
		return err
	}
	if !res.More {
		break
	}
}
```

- **Only `ClientID`, `Session` and `SubjectIssuer` select across
  tenants.** A subject names a principal of one tenant, and the consumer
  mints run ids, so `Subject`, `OnBehalfOf` or `RunID` beside `AllTenants`
  is `ErrInvalidRequest`.
- **With more than one identity provider, name the issuer.** Two providers
  may register a client of the same id, or reuse a `sid`; without
  `SubjectIssuer` a call by client or session revokes both providers'.
- **pgstore refuses a narrower scope** — one tenant's, or
  `WithAllTenantsReadOnly` — as `ErrScopeMismatch`.
- **A batch writes in at most 16 tenants,** each under the revoke lock a
  revoke in that tenant takes, so a delegation there waits for it as for
  any revoke.

## Bring your own storage

On PostgreSQL, take `pgstore` (`github.com/oleg-tkachuk/limes/pgstore`, a
module of its own). It implements every contract below but `KeyResolver`
and `dpop.Nonces`, passes `storetest` and `metertest`, keeps tenants apart
with row-level security, and needs PostgreSQL 15 or later.

```go
// In the migration job, as the owner of the schema, granting the role the
// stores serve as (github.com/oleg-tkachuk/limes/pgstore/migrate).
err := ownerPool.AcquireFunc(ctx, func(c *pgxpool.Conn) error {
    return migrate.Run(ctx, c.Conn(), migrate.Options{RuntimeRoles: []string{"app"}})
})
records, _ := pgstore.NewStore(pgstore.Config{DB: pool})
usage, _ := pgstore.NewUsageStore(pgstore.Config{DB: pool})
// A call for one tenant; housekeeping takes pgstore.WithAllTenants(ctx),
// and a read across tenants, ListTenantBudgets among them,
// pgstore.WithAllTenantsReadOnly(ctx), which admits no write: one is
// refused with ErrScopeMismatch before it begins.
_, err = records.Revoke(pgstore.WithTenant(ctx, tenant), limes.RevokeRequest{CapabilityID: id})
```

### The schema

**One schema, named `limes`, whatever a consumer uses.** pgstore creates
every table its stores read, for a consumer that counts requests alone as
for one that charges. A table a consumer never writes costs a catalog entry
and nothing per call: `Bump` never touches `tenant_budgets`, `charges` or
`reservations`, no trigger fires on them, and autovacuum passes them over.
A counter-only profile would fork the migrations and their version
numbers, so there is none. The name is fixed: every function, policy and
migration names `limes.` outright, and the functions pin their
`search_path` (migration 007), so no table or function a role creates
earlier in a path stands in for pgstore's. Keep your own tables in a schema
of your own beside it.

**Migrations.** `pgstore/migrate` creates and upgrades the schema; only a
migration job imports it, so a serving binary links neither the
migrations nor `tern`.

- Run `migrate.Run` as the role that owns the schema, and serve as
  another. `migrate.Options.RuntimeRoles` grants the serving roles the
  schema, its tables (those of later versions too), its functions — which
  no other role may call — and only reads of the version table. Run it
  before any migration of yours that refers to its tables.
- The policies bind the owner too (`FORCE ROW LEVEL SECURITY`), and
  `migrate.Run` scopes each migration to every tenant. But the owner may
  turn them off, and a superuser or a `BYPASSRLS` role is never bound, so
  `CheckSchema` refuses with `ErrRuntimeRole` a pool of any of the three,
  of a role owning anything in the schema, or of one able to act as one of
  these. It checks the session's role and the login role as well as the
  current one, since a session that switched with `SET ROLE` can switch
  back.
- `migrate.Run` holds the advisory lock every `tern` migration takes,
  which is reentrant per session: a consumer migrating with `tern` calls
  it on the connection its own run holds.
- Each transactional migration runs `READ COMMITTED`, whatever the
  defaults. Every migration that builds an index on an existing table
  builds it `CONCURRENTLY` and blocks no writes, so a `migrate.Run` at
  startup blocks none.
- `CheckSchema` tells a readiness check whether the schema is at the
  version the package queries: `ErrSchemaMismatch` when it is behind
  (`migrate.Run` has not run) or ahead (a later pgstore migrated).
  `ReadSchemaVersion` reports both numbers (`Applied`, `Expected`), with no
  more than SELECT on the version table.

**Another migration tool.** Hand it the steps from `pgstore/migrate/ddl`,
which links no `tern` and no driver (`migrate.Steps`, `migrate.Setup` and
`migrate.Grants` are the same values): `ddl.Setup` once; each step's `SQL()`
in a transaction of its own, or, where `InTransaction` is false, its
`Statements()` one at a time outside one; then `ddl.Grants`. Never put two
transactional steps in one transaction: each opens by setting `READ
COMMITTED`, which PostgreSQL takes only as a transaction's first statement.
Each step scopes itself as `migrate.Run` would and records its version in
`limes.schema_version`, so `CheckSchema` reads it and `migrate.Run` can take
over. A tool that applies files (Atlas, golang-migrate, Flyway) gets them
written out once per pgstore upgrade, and checked in:

```go
steps, err := ddl.Steps()
// limes_000_setup.sql: ddl.Setup()
for _, s := range steps {
	body := s.SQL() // a transactional step: one file, one transaction
	if !s.InTransaction {
		// Atlas runs a file statement by statement outside a transaction
		// under this directive; another tool has its own.
		body = "-- atlas:txmode none\n\n" + strings.Join(s.Statements(), "\n")
	}
	// write body to "limes_" + s.Name (s.Name is such as 001_records.sql)
}
grants, err := ddl.Grants([]string{"app"}) // last, one transaction
```

Every statement ends with `;`. A vendored copy falls behind when a pgstore
upgrade brings a step: compare the files with `ddl.Steps()` in a test of
your own so the upgrade fails in CI, and a readiness check running
`CheckSchema` answers `ErrSchemaMismatch`, with `ReadSchemaVersion` naming
the applied version beside `ddl.Version()`. Run `storetest.Run` and
`metertest.Run` (or `RunCounter`) over the vendored schema, your own roles
and row-level security: they check the deployment, not only pgstore.

### Connections, scopes and transactions

`Config.DB` is a `pgstore.DB` — `BeginTx` and `QueryRow` — which
`*pgxpool.Pool` and `*pgx.Conn` implement. Every transaction the store
opens is READ COMMITTED, so a call that waited on a lock reads what
committed meanwhile; a `DB` of your own passes on the `pgx.TxOptions` it is
given, or runs at the database's default. A pool or connection (or a `DB`
that implements `pgstore.ConnLender`) lets the store send `BEGIN` with a
call's first statement and a meter call's `COMMIT` with its last: a
`Charge` takes four round trips, a `Bump` three. Any other `DB` — a wrapper
that embeds a pool among them — pays a round trip each for `BEGIN` and
`COMMIT`. The store logs which it took when it is built.

A role that does not own the tables sees only the rows of the tenant a call
is scoped to, and none when it is scoped to no tenant; the revocation
checks see every tenant, since the verifier asks before it knows the
token's.

- A call whose request names its tenant — `Insert`, `ListByPrincipal`,
  `Bump`, `RevokeMatching`, `Charge`, `Reserve`, `Settle`, `Release`,
  `GetTenantBudget`, `SetTenantBudget`, the tenant erasers,
  `Ledger.SumCharges`, `Census.ListRecords` and `Census.CountRecords` for
  one tenant — under a scope that does not admit that tenant returns
  `ErrScopeMismatch`, never `ErrNotFound` or `ErrInvalidRequest`.
- A call that names only an id (`GetRecord`, `Revoke`, `Refund`,
  `GetUsage`, `GetCharge`, …) cannot be checked so, and answers a row out
  of scope as not found.
- `Config.Scope` replaces `ScopeFromContext` for a consumer whose policies
  read settings of their own. Such a Scope gets no scope check unless
  `Config.CheckScope` names one: `pgstore.CheckSchemaScope` for a Scope
  that sets the schema's own settings, `pgstore.SettingTenant` and
  `pgstore.SettingAllTenants`. A Scope that maps a read across every
  tenant to `SettingAllTenantsRead` gets the read-only refusal of
  `WithAllTenantsReadOnly`.
- `Config.CheckTenant` runs in `Insert`'s transaction, so a consumer that
  keeps its own tenants answers `ErrUnknownTenant` and `ErrTenantDeleted`
  from the snapshot the record is written in.

`onCharged`, and a refund's `onRefunded`, run on the store's own
transaction, which `TxFrom` reads from the context and which is theirs only
while they run: kept past it, a call fails with `pgx.ErrTxClosed`. The
transaction is the store's to end. `Commit` or `Rollback` on it returns
`ErrChargeTransaction`, and a charge whose transaction the callback ended
with SQL of its own (`COMMIT`, `ROLLBACK`, or either followed by `BEGIN`) is
refused with `ErrChargeTransaction` and `meter.ErrOutcomeUnknown`, since
the charge's writes went with it. A savepoint it begins is its own to end.

Every time the store writes comes from `Config.Now`. With `Config.Now` nil,
holds' and leases' expiry is measured by the database's clock, `now()` as
the call's transaction began, so replicas whose clocks drift agree on which
holds are live. A capability's expiry is judged by each verifier's clock
and leeway, so the store ends a hold or lease, and refuses `Reserve` and
`Lease` with `ErrExpired`, only past the most a verifier may still accept
([Reserve before a cost is known](#reserve-before-a-cost-is-known)).

### A tenant's throughput

A tenant's ceiling is exact because the calls that move its spend or holds
lock its budget row until they commit, so one tenant's charges commit one
at a time, however many replicas send them ([pgstore's
throughput](api/storage.md#package-pgstore)). `onCharged` runs while the
lock is held, and costs `COMMIT` a round trip of its own, so keep it
short.

A tenant whose calls queue on its row shows it in `limes.tenant.wait`
([Observe the meter](#observe-the-meter)), and in pgstore's WARN line for
each call that waited over 100 ms: a wait growing towards the call's own
time is a queue, not a slow database. No setting raises the row's own rate.

- **With a ceiling**, serve the tenant with fewer calls on the row: a
  lease for each worker, which locks the row once to open and once to
  settle ([Lease a sub-agent its budget](#lease-a-sub-agent-its-budget);
  ADR-0017 measured some three times the charges a second), or a `Reserve`
  settled once for many units of work.
- **Without a ceiling**, nothing is checked, and pgstore takes the calls
  off the row unless `Config.DisableTenantStripes` is set: they read it
  without locking and add their change to one of the tenant's stripes,
  chosen by connection. Reads sum the row and the stripes. Setting a
  ceiling folds the stripes back first, waiting for the tenant's calls in
  flight up to `Config.TenantBudgetLockTimeout` (5s; past it a
  `meter.TenantBudgetBusyError` naming who held the tenant); from then on
  the tenant's calls lock the row again. A release before pgstore/v0.72.0
  reads the row without its stripes and would check a ceiling against less
  than was spent: set `DisableTenantStripes` while one still writes the
  database ([ADR-0022](adr/0022-unlimited-tenant-aggregate.md)).

### The contracts

Elsewhere, implement these:

| Interface | Stores | Transactions needed? |
|---|---|---|
| `Store` = `IssuanceStore` + admin | Capability records + revocations; an `Issuer` needs only `IssuanceStore` (`Insert`, `IsRevoked`), and a `RecordReader` too to `Reissue` | No |
| `BulkRevoker` | Revokes a tenant's capabilities by subject, on-behalf-of, client, session, agent run or subject issuer (optional; `storetest` checks it through `Env.BulkRevoker`) | No |
| `ExchangeStore` | Which capability each subject token was exchanged for (only for `exchange.Config.Exchanges`; `storetest` checks it through `Env.Exchanges`) | No |
| `BiscuitRevocationStore` | Revoked Biscuit copies (only if you accept Biscuits) | No |
| `meter.CopyUsageReader` | Reads Biscuit copies' own counters (only to show them) | No |
| `meter.LedgerReader` | Sums a tenant's charges for billing and reports (optional; a type of its own beside `meter.UsageStore`) | No |
| `RecordCensus` | Counts capability records by disposition, for one tenant or every tenant (optional; a type of its own beside `Store`) | No |
| `RecordLister` | Lists one tenant's capability records with their dispositions, filtered and paged either way (optional; a type of its own beside `Store`) | No |
| `RecordListingReader` | Reads one record with its disposition, by the rules `RecordLister` and `RecordCensus` share (optional; a type of its own beside `RecordLister`) | No |
| `RecordReporter` = `RecordLister` + `RecordListingReader` | Lists a tenant's records and reads one; `memstore.NewCensus` and `pgstore.NewCensus` return a `Census` that is both, and a `RecordCensus` | No |
| `TenantRecordEraser`, `meter.TenantUsageEraser` | Erase a tenant's records and usage (optional) | No |
| `meter.MatchingReleaser` | Releases the holds of capabilities a bulk revocation stopped (optional; `metertest` checks it through `Env.Revoke`) | No |
| `meter.TenantReservationLister` | Lists the holds that make up a tenant's `ReservedAmount`, soonest to expire first (optional) | No |
| `meter.UsageStore` = `Meter` + `MeterReader` + `TenantBudgets` + `UsageHousekeeping` (only to meter) | Request and spend counters, the charges ledger, tenant ceilings; `DeleteUsage` and `PurgeOrphans` release a reaped capability's holds | Only for atomic side effects |
| `KeyResolver` | Public verification keys; an unknown kid is `jwks.ErrUnknownKID`, the token's fault (`ErrInvalidToken`), and any other error the resolver's own (`jwks.Static` and `jwks.Remote` ship; `jwkstest` checks yours) | No |
| `dpop.ReplayCache` | DPoP proof ids seen, each counted against its holder: `Seen(ctx, dpop.ReplayEntry)` refuses a holder past its share with an error wrapping `dpop.ErrReplayQuota`, logged at WARN, and returns another error when its store cannot answer (only for key-bound tokens; [which to pick](#several-replicas)) | No |
| `dpop.Nonces` | Server-provided DPoP nonces (optional; `dpop.HMACNonces` ships and needs no store) | No |

Two obligations are easy to miss. `Store.IsRevoked` answers for the
capability's whole delegation chain, and `Meter` applies every charge and
request to each ancestor as well, reading the ancestor's ceilings from its
record. Code that only meters can depend on `Meter` alone; code that only
administers tenant ceilings, on `TenantBudgets` alone.

`Store.Insert` returns `ErrAlreadyExists` for an id already on record. It
refuses, writing nothing:

- a record with no id or no expiry, or under a parent not on record:
  `ErrInvalidRequest`;
- a child expiring after its parent, or in another tenant:
  `ErrDelegationTooWide`;
- a child whose budget names another unit than its parent's:
  `ErrUnitCodeMismatch`;
- a child with more than `MaxDelegationDepth` ancestors still on record:
  `ErrDelegationTooDeep`. `Purge` and the `IsRevoked` walk rely on a whole
  lineage in one tenant, no deeper than the walk reaches;
- a tenant it does not hold, or one in the trash: `ErrUnknownTenant`,
  `ErrTenantDeleted`;
- a call scoped to another tenant than its request names, in a store that
  scopes rows by context: `ErrScopeMismatch`, rather than answer as if the
  rows were not there.

`Issue` and `Delegate` pass these through, wrapped, but two the store
shares with the verify path: `ErrDelegationTooDeep` becomes
`ErrDelegationDepthExceeded`, and `ErrTenantErased` becomes `ErrTenantGone`,
since a mint presents no capability to call revoked. Every check on the
request itself matches `ErrInvalidRequest`: a missing tenant id, an empty
audience entry, a negative TTL or one ending within the second it is issued
(a token's times are whole seconds), a `NotBefore` at or after the expiry,
a malformed thumbprint, invalid caveats (`ErrInvalidCaveats`). Anything
else they return is the store's or the signer's failure.

**Error families.** A read by id that finds nothing matches `ErrNotFound`,
through a sentinel of its own where the contract names one:
`meter.ErrChargeNotFound` (`Refund`, `GetCharge`, `ChargeByRef`),
`meter.ErrReservationNotFound`, `meter.ErrUsageNotFound` (a capability never
bumped or charged), `meter.ErrTenantBudgetNotFound` and
`meter.ErrLeaseNotFound`. A well-formed write that disagrees with what is on
record matches `ErrConflict`; `ErrorResponse` answers the family 409. Read
again, and decide:

| Sentinel | From |
|---|---|
| `ErrAlreadyExists` | `Insert` |
| `ErrUnitCodeMismatch` | `Insert`, `Delegate`; a `Charge`, `Reserve` or `Settle` in another unit than a counter already counts in; a `SetTenantBudget` to another unit while anything is held in the old one, or spent in it and not reset |
| `meter.ErrExternalRefConflict` | `Charge` |
| `meter.ErrReserveConflict` | `Reserve` |
| `meter.ErrSettleConflict` | `Settle` |
| `meter.ErrRefundConflict` | `Refund` |
| `meter.ErrLeaseNested` | `Lease` |
| `meter.ErrTenantBudgetVersionMismatch` | `SetTenantBudget` |

**Shapes.** Every write and every listing takes a `…Request`, a read by id
takes the id, every list is a `Page`, and every read names what it reads
(`GetRecord`, `GetUsage`, `GetTenantBudget`), so one type may implement
`Store` and `meter.UsageStore` together, or each apart as `memstore` does.
`meter.UsageStore` names no transaction type: a store carries its own in
the callback's context ([Commit side effects with the charge](#commit-side-effects-with-the-charge)).

Every listing — `ListByPrincipal`, `ListTenantBudgets`, `ListReservations`
(soonest to expire first, ties by id), `SumCharges`, `CountRecords` and
`ListRecords` — is a page with an opaque cursor, so an API can hand a page
token to its client. To read `ListByPrincipal` or `ListTenantBudgets`
whole, range over `AllByPrincipal` or `meter.AllTenantBudgets`. They walk
the pages from `req.Cursor` afresh on every range, stop fetching when the
loop breaks, and refuse a page that returns a cursor already fetched
(`ErrCursorRepeated`). Spend moves between pages, so `AllTenantBudgets` may
yield a tenant twice or not at all.

```go
for c, err := range limes.AllByPrincipal(ctx, store, req) {
    if err != nil { /* yielded once; the walk ends */ }
    // …
}
```

### Read the ledger

Bill and report from the charges ledger through a `meter.LedgerReader`,
never a store's tables. `SumCharges` sums the charges of `TenantID`, or of
every tenant with `AllTenants`, taken in [`From`, `To`) by the store's
clock, grouped by unit and as `SumChargesRequest` asks:

- **by bucket:** the zero `ChargeBucketNone` for the period whole, or the
  UTC `ChargeBucketHour`, `ChargeBucketDay`, `ChargeBucketWeek` (ISO, from
  Monday 00:00) or `ChargeBucketMonth`;
- **by each dimension `GroupBy` names:** `ChargeDimensionTenant`,
  `ChargeDimensionOp`, `ChargeDimensionActor`, `ChargeDimensionCapability`;
- **filtered** by `Filter` to the tenants, operations, actors or
  capabilities it lists, at most `meter.MaxChargeFilterValues` each.

Amounts in different units are never summed together. Buckets come in time
order and, within each, the largest `Charged` first, ties by unit, tenant,
operation, actor and capability, bytewise; so with `ChargeBucketNone` the
first page of `Limit` rows is the top `Limit` — across tenants, grouped by
tenant, the largest spenders. `meter.AllChargeSums` walks every page. Each
`ChargeSum` carries `Charged`, `Refunded` and `Charges`; the net is
`Charged − Refunded`. A refund counts with the charge it returns, so a
closed period's sums still move; a sum past `MaxNanos` is
`meter.ErrCounterOverflow`.

`memstore.NewLedger` and `pgstore.NewLedger` read their stores' ledgers;
pgstore's is scoped to the tenant like every other read, and sums across
tenants only under `WithAllTenantsReadOnly` (or `WithAllTenants`), else
`ErrScopeMismatch`. Who may read every tenant's spend is yours to decide,
by who gets that scope. `metertest` checks the reader its `Env.Ledger`
names.

**Retention.** A store writes a ledger row for every charge, and removes
none but with its tenant. A row is more than history — an `ExternalRef`
replays its charge, a settled hold replays its receipt, and `Refund` finds
it — so those promises hold while the charge is in the ledger. A ledger may
remove charges older than at least `meter.MinChargeRetention`, 401 days: a
leap year of billing and 35 days of disputes. Past it a reference charges
anew, a settle finds no reservation, a refund is
`meter.ErrChargeNotFound`, and `SumCharges` sums what is left. Choose the
retention from the longest a client may retry or dispute a charge, and
keep the ledger in a warehouse of your own if reports must reach further.

`memstore.NewLedger` and `pgstore.NewLedger` are also a
`meter.LedgerHousekeeping`: a worker calls `PurgeCharges` until a batch
comes back short of its limit, as it calls `Purge`. `OlderThan` zero is
`MinChargeRetention`, and shorter is refused. pgstore removes the charges
its scope reaches — every tenant's under `WithAllTenants`, one tenant's
under `WithTenant`; a scope that admits no tenant is `ErrScopeMismatch` —
oldest first, and skips a charge a refund holds for the next batch. Run it
on the primary. A deletion of your own, in SQL, is held to the same floor.

### Report on records

**Count** them through a `RecordCensus`: `CountRecords` counts the records
of `TenantID`, or of every tenant with `AllTenants`, whole or by tenant and
principal type, a page at a time. Each `RecordCount` has `Total` =
`Active` + `Expired` + `Revoked`, each record in one of them by the store's
clock: revoked — itself or through an ancestor on record — wins over
expired, and expired — a zero `ExpiresAt` included — over active.
`Delegated` counts the records with a parent, and `ExpiringSoon` the active
ones expiring within `ExpiringWithin`.

**List** them through a `RecordLister`: `ListRecords` pages the records of
`TenantID`, or of every tenant with `AllTenants` (exactly one of the two),
in id order: `ListOrderAscending` (the zero value) or
`ListOrderDescending`, newest first, since a capability's id is a UUIDv7.

```go
page, err := census.ListRecords(ctx, limes.ListRecordsRequest{
	TenantID:     tenant,
	Dispositions: []limes.RecordDisposition{limes.RecordDispositionActive},
	Order:        limes.ListOrderDescending, // newest first
})
```

- **Filters**, all optional: `PrincipalType`; `Subject` exactly or
  `SubjectPrefix` (bytewise; not both); `Dispositions`, any of
  `RecordDispositionActive`, `Expired` and `Revoked`; and the bulk-revoke
  selectors `OnBehalfOf`, `ClientID`, `Session`, `SubjectIssuer`, `RunID`.
- **Each `RecordListing`** carries the `Record` — what `GetRecord` reads —
  and its `Disposition` by the census's rules: revoked through an ancestor
  on record too, unlike `ListByPrincipalRequest.IncludeRevoked`. A record
  with no expiry, which `Insert` refuses but an older pgstore row may hold,
  is expired; a record whose every Biscuit copy is revoked is still active,
  as its JWT still verifies. A revoked one also carries `StoppedBy`, the
  entry that revoked it — its own, or the nearest ancestor's — so a record
  stopped through an ancestor still says when and why.
- **Across tenants** every filter applies to all of them, each record
  naming its tenant in `Record.Capability.Subject.TenantID`: a platform
  operator's index.
- **Consistency.** At one instant, the records of each disposition in a
  full listing number what `CountRecords` counts. Each page is read on its
  own, though: a walk of many pages is no snapshot.
- **Cursors** are opaque and hold the order, the tenant and the filters
  they were handed out for. One of another order, tenant (or of every
  tenant in a tenant's listing, or the other way round) or filters is
  `ErrCursorMismatch`, a kind of `ErrInvalidRequest`; a caller that changes
  them starts from the first page. Any cursor the lister did not hand out
  is `ErrInvalidRequest`.

**Read one** through a `RecordListingReader`: `GetRecordListing(ctx, id)`
gives the `RecordListing` a listing gives, and `ErrNotFound` for a record
not on record or out of the call's scope. Working the disposition out from
`IsRevoked` and the expiry instead would be a second rule beside the
store's, on another clock. A reporter that lists and opens records holds
the `Census` as a `RecordReporter`, both in one.

`memstore.NewCensus` and `pgstore.NewCensus` return all three. pgstore
counts and lists every tenant only under `WithAllTenantsReadOnly` or
`WithAllTenants` (a narrower scope is `ErrScopeMismatch`), and one tenant
under that tenant's scope. It reads whether a record is revoked from a flag
on the record, which its revocation and its parent's set (migration 015),
so a page reads no revocation; the indexes of migrations 014 and 017 serve
the listing. `storetest` checks a census and a lister its `Env` names, when
the `Env` also sets `Now`, and that each record reads as its listing gives
it.

**Off the primary.** Build pgstore's `Census` and `Ledger` over a pool of a
read replica — a hot standby, whose every transaction is read-only — and
the stores over the primary's. Every report reads only: `CountRecords`,
`ListRecords`, `GetRecordListing` and `SumCharges`. A replica answers as of
its lag, so a report may miss the last charges and revocations; nothing a
request is decided on reads it. `PurgeCharges` writes, so the worker that
runs it holds a `Ledger` over the primary. The scope is set per transaction,
as on the primary, so `WithTenant` and `WithAllTenantsReadOnly` work there
unchanged.

```go
records, _ := pgstore.NewStore(pgstore.Config{DB: primary})
census, _ := pgstore.NewCensus(pgstore.Config{DB: replica})
ledger, _ := pgstore.NewLedger(pgstore.Config{DB: replica}) // SumCharges
purger, _ := pgstore.NewLedger(pgstore.Config{DB: primary}) // PurgeCharges
```

### Test a report

Backdate a report's charges through the store's clock, never by writing a
store's tables, whose columns change without notice. A charge carries the
store's time (`ChargeRecord.At`), so build the stores on a clock the test
sets (`Config.Now` in `pgstore`, `StoreConfig.Now` and
`UsageStoreConfig.Now` in `memstore`), start it at the period's start and
move it forward before each charge:

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

The capability must not have expired at a charge's time: insert its record
with an `ExpiresAt` past the period, as above, or issue it through an
`Issuer` whose `IssuerConfig.Now` is the same clock, with an
`IssueRequest.TTL` as long as the period. A test that charges through
verified tokens gives `VerifierConfig.Now` that clock too. Move the clock
forward only: holds, revocations and expiry are all measured by it.
`Example_backdatedCharges` runs the whole route on `memstore`.

### Erase a tenant

A store may implement `TenantRecordEraser` and `meter.TenantUsageEraser`
(optional; `memstore` and `pgstore` implement both, and `storetest` and
`metertest` check them when present).

- **`EraseTenantRecords`** revokes every capability of the tenant and
  removes only the records `Purge` would remove: an id not on record is not
  revoked, so erasing a record whose token may still verify would let it
  through again. `Purge` takes the rest as they expire.
- **`EraseTenantUsage`** drops the tenant's counters, holds, charges and
  aggregate — its billing history, which does not come back — returns how
  many of each it removed (`meter.EraseTenantUsageResult`), and marks the
  tenant erased. A request verified before the erase, or a late cost of a
  record `Purge` has not taken yet, is then refused `ErrTenantErased` (a
  kind of `ErrRevoked`) by `Bump`, `Charge`, `Reserve` and
  `SetTenantBudget`, and `Insert` refuses a new record of the tenant, which
  `Issue` and `Delegate` return as `ErrTenantGone` (410). `PurgeOrphans`
  forgets the mark once none of the tenant's records is left.

Called apart, `EraseTenantUsage` goes first and `EraseTenantRecords`
second: the mark makes `Insert` refuse the tenant, so the second call finds
every record there will be. The other way round, a record inserted between
the two calls stays on record unrevoked, and its token goes on verifying.
A `PurgeOrphans` run between the two lets a new root record in, which the
records' erase then revokes. In `pgstore`,
`Store.EraseTenant` does both in one transaction and runs a callback of
yours in it, so your own rows of the tenant go with them or not at all; it
is handed a `pgstore.Erased`, the transaction as `Tx` and the result as
`Result`. `EraseTenantRequest.Note` and `Actor` go on each revocation entry
it writes, and `EraseTenantResult` counts the capabilities `Revoked`, the
records `Removed` and the records `Kept`, revoked, for `Purge`; a second
erase revokes nothing the first did.

## Key rotation

1. Publish the new public key beside the old (`jwks.Static.SetKey`, or add
   it to the served JWKS; `jwks.Marshal` sorts by kid, so the output is
   stable).
2. Switch the issuer to sign with the new key no sooner than the `max-age`
   the JWKS is served with, so every verifier has fetched it.
3. Withdraw the old key (`jwks.Static.RemoveKey`) only after the **longest
   outstanding TTL** has elapsed.

Skipping the wait in step 3 invalidates tokens agents still hold. TTLs are
minutes (`IssuerConfig.DefaultTTL`, fifteen by default), so the wait is
short.

**A compromised key** is withdrawn at once, and the bound is the
verifiers' cache, not a TTL. A verifier on `jwks.Remote` stops accepting
the key once it fetches the document without it: within the `max-age` it is
served with (`jwks.HandlerConfig.MaxAge`, at most
`RemoteConfig.RefreshInterval`, 5 minutes by default), or within
`RemoteConfig.MinRefreshInterval`, 10 seconds, with
`HandlerConfig.Revalidate`. While it cannot reach the endpoint it keeps the
last set for up to `RemoteConfig.MaxStale` (an hour by default), then
refuses every token. So on compromise also revoke the capabilities the key
signed: a revoked capability is refused whatever key verifies it.

Not to wait for the cache, act on each verifier. `Remote.Block(kid)`
refuses the kid at once, as `jwks.ErrUnknownKID` (`ErrInvalidToken` to the
verifier), whatever the held set or the endpoint says, until
`Remote.Unblock`. `Remote.Refresh(ctx)` fetches the document now, past
`MinRefreshInterval`, and returns the fetch's error. Each is logged
(`RemoteConfig.Logger`), as is every token refused for a blocked kid:

```go
keys.Block(compromisedKID) // refused from now on, outage or not
if err := keys.Refresh(ctx); err != nil {
    log.Error("JWKS refresh", "error", err) // the block holds meanwhile
}
```

### Signing keys

`LoadKey(path)` loads an Ed25519 key, PKCS#8 PEM only; an unreadable path,
or none, is an error, never a fresh key. `GenerateKey` makes an ephemeral
one, gone with the process. A deployment that may run without a key file
chooses between the two itself. Either key's kid is `jwks.KeyID`'s: its RFC
7638 thumbprint in limes's namespace; set `SigningKey.KeyID` to name it
otherwise.

`NewSigner(limes.SignerConfig{KeyID: kid, Key: limes.SealKey(key)})` signs
through any `crypto.Signer` whose key is Ed25519 — an
`ed25519.PrivateKey`, or a key held in a KMS or an HSM. A key is held
sealed (`limes.SealedKey`; `SigningKey.Private` is one already, and
`dpop.NonceKeys` for nonce keys), so no formatting of a value holding it
prints it. `SealKey` and `dpop.SealNonceKeys` keep copies, so clearing or
reusing the caller's key afterwards leaves the sealed one whole. A key
that implements `ContextSigner` signs under the request's context, so a
slow or cancelled call is bounded by the request that asked for the token.

Tokens name the algorithm `Ed25519`, as RFC 9864 does. Where the services
reading them use a JOSE library, gateway or identity provider that knows
only the deprecated name, set `SignerConfig.Algorithm` to
`limes.SigningAlgorithmEdDSA`: the signature is the same, and every limes
verifier reads both names. Publish the key under the same name, through
`jwks.HandlerConfig.Algorithms` (or `jwks.MarshalAlgorithms`): a library
that matches a key's `alg` to the token's, Node's jose among them, finds no
key for an `EdDSA` token under an entry naming `Ed25519`. Switch by
rotating: sign with a new key named `EdDSA` while the old key, still named
`Ed25519`, stays in the set until its tokens expire.

```go
handler, _ := jwks.NewHandler(jwks.HandlerConfig{
    Keys:       func() map[string]ed25519.PublicKey { return keys },
    Algorithms: func() map[string]string { return map[string]string{newKID: jwks.AlgorithmEdDSA} },
})
```

### What a token claims

Every token is an RFC 9068 access token, typed `at+jwt`, which gateways
and identity providers that validate that profile read too. It names its
OAuth client (`IssueRequest.ClientID`, `client_id`) and its subject
(`Subject.Subject`, `sub`); `Issue` refuses a request without either. A
verifier refuses a token under a kid outside limes's namespace, and no key
that signs capabilities may also sign an authorization server's `at+jwt`
([ADR-0006](adr/0006-one-token-format.md)).

An agent working for a user holds a capability on the user's behalf:
`IssueRequest.OnBehalfOf` names the user, of the subject's tenant. The
token then names the user as `sub` and the agent as RFC 8693's `act`; a
delegated child keeps the user, with each holder it passed through nested
in `act`. `Delegate` derives this, and no request can change it. limes
still authorizes, meters and revokes by the holder, `Capability.Subject`
([ADR-0005](adr/0005-acting-on-behalf.md)).

A gateway that cannot read capability tokens can ask about one instead.
`Verifier.Introspect` says whether a token is active (genuine, from a
trusted issuer, within its window and not revoked) and what it carries;
`exchange.IntrospectionHandler` serves that as an RFC 7662 endpoint for
authenticated callers, with the caveats as an RFC 9396
`authorization_details` entry. It checks no audience and no proof of
possession, so its answer authorizes nothing: an active bound token is
reported bound, not proved. A Biscuit copy with limits of its own is
answered with them (`Introspection.Copies`), or, without
`BiscuitConfig.MeterCopies`, refused as `Verify` refuses it. A revocation
store that cannot answer is the endpoint's error, never an inactive token.

## Exchange an identity provider's token

`exchange.Exchanger` trades an access token from the client's identity
provider for a capability (RFC 8693,
[ADR-0003](adr/0003-oauth-token-exchange.md)). It verifies the token
through a `SubjectVerifier`. `exchange/jwtverify` supplies one over jwx for
RFC 9068 access tokens; a service that already verifies its provider's
tokens — on go-oidc and go-jose, say — implements `SubjectVerifier` on that
stack instead, so package `exchange` links no JOSE library of its own
([ADR-0010](adr/0010-exchange-jose-subpackage.md)).
`exchangetest.TestSubjectVerifier` holds such a verifier to the contract.

A `Policy` answers each grant with an `exchange.Decision`: the capability
to issue, and the scopes it grants of those asked, which the token
response states as its `scope`. Nothing is granted that the policy does not
name; a policy granting every scope asked says so:

```go
func policy(ctx context.Context, g exchange.Grant) (exchange.Decision, error) {
	return exchange.Decision{Issue: limes.IssueRequest{ /* … */ }, Scopes: g.Scopes}, nil
}
```

A request that asked for scopes and is granted none is refused with
`invalid_scope`, since a response without `scope` would state every scope
asked (RFC 8693 §2.2.1); one that asked for none is answered without
`scope`. A scope the request did not ask for is the policy's fault,
answered `server_error`. A `NarrowPolicy`'s `NarrowDecision.Scopes` is
held the same way.

By default each exchange issues a capability of its own, with ceilings of
its own: right for a client that exchanges once and keeps the result. A
service that exchanges where it receives the token — an MCP server that
exchanges per request, or once per replica — would multiply the ceilings
the policy meant for the token. `exchange.Config.Exchanges` issues one
capability per subject token instead
([ADR-0009](adr/0009-one-capability-per-subject-token.md)):

```go
ex, err := exchange.NewExchanger(exchange.Config{
	Issuer: issuer, Subjects: subjects, Policy: policy,
	Exchanges: store, // a limes.ExchangeStore: memstore's, or pgstore.ScopedExchanges(store)
})
```

- The store keeps which capability a subject token's issuer and `jti`, in
  the tenant the policy names, was exchanged for: the id, never a token.
  Later exchanges sign the token again from the record (`Issuer.Reissue`),
  so under the same key every exchange of one subject token returns the
  same token, and nothing stored can be replayed as a bearer.
- Exchanges racing across replicas converge on one capability. A
  capability revoked or expired is refused, never issued anew; a subject
  token without a `jti` is refused.
- A capability bound by DPoP stays bound to the first exchange's key:
  replicas that exchange one token share that key, or exchange as bearer
  within one trust domain. A proof by another key, none for a bound
  capability, or one for a bearer capability is refused, and so is an
  exchange asking for another audience than the capability's.
- A policy that names no TTL gives the capability the subject token's
  remaining life, not the issuer's default; one that names a shorter TTL
  leaves the rest of the token's life refused.
- The policy runs on every exchange, since it names the tenant.

Behind a token endpoint, whose requests name no tenant, wire pgstore's
store as `pgstore.ScopedExchanges(store)`, beside
`pgstore.ScopedIssuance(store)` for the `Issuer`. Each scopes its calls to
the tenant the key or the record names. The table that holds the mapping,
`limes.exchanges`, keeps each entry until its subject token expires, past
its capability's record. `storetest` checks an `ExchangeStore` through
`Env.Exchanges`, and `exchangetest.TestOnePerToken` checks an `Exchanger`
in this mode over your own store, roles and row-level security.

An `Exchanger` called in process answers a refusal as the endpoint does
with `exchange.ErrorOf(err)`: the RFC 6749 §5.2 `Error` (`Status`, `Code`,
a fixed `Description`), and false for an error that is none of the
exchange's, which the endpoint answers `server_error`, 500.
`exchange.ErrorCodes()` lists every code
([errors.md](errors.md#the-exchanges-codes)).

## Answer a refusal

`ErrorResponse` turns any error `Verify`, `Authorize` or the `Meter`
returns into the response RFC 6750, RFC 9449 and RFC 9457 describe, and
`WriteErrorResponse` writes it:

```go
if err != nil {
    limes.WriteErrorResponse(w, verifier.ErrorResponse(ctx, err, scheme))
    return
}
```

Each refusal's status and challenge — and the `DPoP-Nonce` a refusal for
want of a nonce carries — are in [errors.md](errors.md#formats). The body
is `application/problem+json` with fixed text (the error's own message,
which can name kids or stores, stays in your log), the error's stable
`code`, and for a refusal its `Reason` (`budget_exceeded`, `revoked`, …) as
the `reason` member and the end of its type URI. An error it does not
recognise — `ErrInvalidRequest` other than `ErrMalformedAuthorization`, a
store's failure — is answered 500.

- `ReasonOf(err)` gives the same `Reason` for your own wire format, and
  `Reason.Permanent()` whether the same request with the same capability
  can ever succeed, so a caller knows to stop rather than retry.
- An API of your own that is not a protected resource answers an error
  limes returned with `limes.ProblemOf(err)`
  ([errors.md](errors.md#formats)).

Branch on the sentinel with `errors.Is`, and put the code on the wire: the
text is never a contract.

## Defaults

Every `Config` field's zero value and every `Default…` constant are in
[defaults.md](defaults.md), which a test holds to the code; every limit —
the `Max…` and `Min…` constants — is in the [API reference](api/README.md),
with its value and what it bounds.
