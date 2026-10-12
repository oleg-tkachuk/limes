# Diagrams

Drawn from the code of this module. The prose that explains the behaviour is in
[README.md](../README.md) and the package documentation in
[doc.go](../doc.go); the identifiers on the diagrams are the exported names, so
a renamed type shows up as a stale label.

Colours follow the rest of the repository: blue for callers, green for what
this module ships, amber for the contracts a consumer implements and stores
behind them, grey dashed for optional pieces, pink for things outside the
process.

## Contract boundary

The module ships the primitive and its contracts; the consumer supplies the
storage behind them. `memstore` is the in-memory reference implementation; a
store over a database is the consumer's own. The two Biscuit contracts are
needed only by a consumer that accepts the Biscuit form, and `dpop.ReplayCache` and
`dpop.Nonces` only by one that binds capabilities to keys. `storetest`, `metertest`,
`dpoptest` and `jwkstest` check an implementation against its contract from its own tests.

```mermaid
flowchart LR
    caller(["Consumer service<br/>orchestrator · tool · API"])

    subgraph mod ["limes (this module)"]
        direction TB
        issuer["<b>Issuer</b><br/>Issue · Delegate · Biscuit"]
        signer["<b>Signer</b><br/>NewSigner · JWT Ed25519"]
        narrows["<b>Narrows</b><br/>child ⊆ parent"]
        verifier["<b>StandardVerifier</b><br/>Authorize · Verify · BiscuitCopy<br/>ErrorResponse · DPoPNonce"]
        caveats["<b>Caveats</b><br/>Check · CheckSource · MatchResource"]
        cache["CachedRevocationChecker<br/>TTL · bounded, oldest out · single-flight"]
        bcache["CachedBiscuitRevocationChecker<br/>per token · TTL · bounded · single-flight"]
        attenuate["<b>Attenuate</b><br/>offline · no key"]
        metering["Instrument<br/>OTel counters · spend histogram<br/>WithMeterProvider · WithTenantAttribute"]
        static["jwks.Static<br/>SetKey · RemoveKey"]
        remote["jwks.Remote<br/>ETag · Cache-Control · MaxStale"]
        dpop["dpop.Verifier<br/>VerifierConfig.DPoP · dpop.Sign"]
        suites["storetest · metertest<br/>dpoptest · jwkstest<br/>conformance suites"]
        jwksh["jwks.NewHandler<br/>ETag · max-age"]
        nonce["dpop.HMACNonces<br/>stateless DPoP nonces"]
    end

    subgraph contracts ["contracts the consumer implements"]
        direction TB
        store["<b>Store</b> = IssuanceStore + admin<br/>Insert · IsRevoked · GetRecord · Revoke<br/>Purge · ListByPrincipal"]
        usage["<b>UsageStore[TX]</b><br/>Meter[TX]: Bump · Charge · Reserve · Settle · Refund · Release<br/>MeterReader: GetUsage · GetCharge · ChargeByRef · GetReservation · ListReservations<br/>TenantBudgets · UsageHousekeeping"]
        keys["<b>KeyResolver</b><br/>PublicKey(kid)"]
        brev["<b>BiscuitRevocationStore</b><br/>IsBiscuitRevoked · RevokeBiscuit<br/>GetBiscuitRevocation"]
        cusage["<b>CopyUsageReader</b><br/>CopyUsage(revocation ids)"]
        replay["<b>dpop.ReplayCache</b><br/>Seen(ctx, ReplayEntry) · MemoryReplayCache ships"]
        nonces["<b>dpop.Nonces</b><br/>Nonce · Valid"]
    end

    impl[("memstore · PostgreSQL · …")]
    jwks{{"Issuer JWKS endpoint"}}
    otel{{"OTel MeterProvider"}}

    caller --> issuer
    caller -- "ParseAuthorization" --> verifier
    caller --> caveats
    caller --> metering
    caller -. "Biscuit holder" .-> attenuate
    issuer --> narrows
    issuer --> signer
    issuer --> store
    verifier --> keys
    verifier --> cache --> store
    verifier -. "AcceptBiscuit" .-> bcache --> brev
    caller -. "admin read" .-> cusage
    metering --> usage
    metering -. "metrics" .-> otel
    static -. "implements" .-> keys
    remote -. "implements" .-> keys
    remote -- "HTTP GET" --> jwks
    verifier -. "key-bound tokens" .-> dpop --> replay
    dpop -. "Nonces" .-> nonces
    nonce -. "implements" .-> nonces
    jwksh -- "serves" --> jwks
    suites -. "check" .-> store
    suites -. "check" .-> usage
    suites -. "check" .-> keys
    suites -. "check" .-> replay
    suites -. "check" .-> nonces
    store --> impl
    usage --> impl
    brev --> impl
    cusage --> impl

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef optional fill:#F1F5F9,stroke:#64748B,color:#334155,stroke-dasharray:5 4
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class caller client
    class issuer,signer,narrows,verifier,caveats role
    class cache,metering,static,remote,bcache,attenuate,dpop,suites,jwksh,nonce optional
    class store,usage,keys,impl,brev,cusage,replay,nonces store
    class jwks,otel external
```

Every read names what it reads — `Store.GetRecord`, `MeterReader.GetUsage`,
`TenantBudgets.GetTenantBudget` — so one type may implement `Store` and
`UsageStore` together, or each apart as `memstore` does. `TX` is the
consumer's transaction handle; the module only threads it back through
`Meter.Charge` and `Meter.Settle`, which is why it has no database dependency.

## Lifecycle of a capability

One orchestrator, one sub-agent, one service. Every call carries a token; the
service verifies it locally, enforces the caveats, then meters the call.

```mermaid
sequenceDiagram
    autonumber
    actor Op as Operator
    participant I as Issuer
    participant O as Orchestrator
    participant W as Sub-agent
    participant S as Service
    participant St as Store / UsageStore

    Op->>I: Issue(IssueRequest)
    I->>I: Caveats.Validate · TTL → ExpiresAt
    I->>St: Store.Insert(cap, IssuedBy)
    I->>I: Signer.Sign(ctx, cap)
    I-->>O: token

    O->>I: Delegate(DelegateRequest{Parent, Caveats})
    I->>St: Store.IsRevoked(parent)
    I->>I: Narrows(parent, child)
    I->>St: Store.Insert(child, parent.Subject)
    I->>I: Signer.Sign(ctx, child)
    I-->>W: child token

    W->>S: request + child token
    S->>S: Authorize: Verify — signature · time · audience · DPoP
    S->>St: IsRevoked(child), inside Verify — via CachedRevocationChecker
    S->>S: Authorize: CheckSource(addr) · Check(op, resource)
    S->>St: Bump · Charge, or Reserve then Settle
    St-->>S: ChargeReceipt{ChargeID, SpentAmount}
    S-->>W: response

    Op->>St: Store.Revoke(RevokeRequest{CapabilityID: parent, CascadeChildren})
    Note over S,St: the next IsRevoked for the child answers true<br/>once the cache entry expires, or at once after Clear
    W->>S: request + child token
    S-->>W: ErrRevoked
```

## Verification gates

`StandardVerifier.Verify` reads nothing from the claims until the signature
over them has verified, checks a key-bound capability's proof of possession,
and asks the revocation store last so that cheap rejections never reach it.
Every refusal is a sentinel the caller maps to its own transport status, or
hands to `ErrorResponse`; the one exception is a revocation lookup that fails,
which returns the store's error wrapped.

```mermaid
flowchart TB
    tok(["VerifyRequest<br/>Token · Audience · Scheme · DPoP"])
    pre{"token and audience set"}
    size{"len ≤ MaxTokenBytes"}
    form{"a Biscuit?"}
    hdr{"header: strict parse<br/>alg = Ed25519 · typ ∈ AcceptTokenTypes · kid set"}
    key{"KeyResolver.PublicKey(kid)"}
    sig{"Ed25519 signature"}
    claims{"claims: strict parse — no unknown or duplicate member<br/>integers within ±(2^53 − 1) · sub = principal's subject"}
    iss{"iss ∈ TrustedIssuers<br/>KeyIssuers[kid] = iss"}
    tenant{"Subject.TenantID set"}
    sealed{"no BiscuitRoot<br/>(not lifted out of a Biscuit)"}
    nbf{"now + Leeway ≥ NotBefore"}
    exp{"now − Leeway ≤ ExpiresAt"}
    aud{"Audience ∈ token's audience"}
    scheme{"Scheme, if set, matches the binding<br/>DPoP for a key-bound token, Bearer otherwise"}
    pop{"key-bound: a dpop.Verifier is configured<br/>and the proof checks out"}
    rev{"RevocationLookup.IsRevoked<br/>this capability or an ancestor"}
    brev{"Biscuit: IsBiscuitRevoked<br/>any block of this copy"}
    ok(["Capability"])

    bad["ErrInvalidToken"]
    early["ErrNotYetValid"]
    expired["ErrExpired"]
    mism["ErrAudienceMismatch"]
    wrong["ErrAuthSchemeMismatch"]
    dpopErr["ErrProofOfPossession<br/>dpop.ErrRequired · dpop.ErrInvalid<br/>dpop.ErrNonceRequired · dpop.ErrReplayed"]
    keysDown["jwks.ErrUnavailable<br/>(503, the server's fault)"]
    revoked["ErrRevoked"]
    biscuit["the Biscuit path below"]

    tok --> pre -- yes --> size -- yes --> form -- no --> hdr -- yes --> key -- found --> sig -- valid --> claims -- yes --> iss -- yes --> tenant -- yes --> sealed -- yes --> nbf
    form -- yes --> biscuit
    nbf -- yes --> exp -- yes --> aud -- yes --> scheme -- yes --> pop -- "unbound, or valid proof" --> rev -- no --> brev -- "no, or not a Biscuit" --> ok
    pre -- "no token" --> bad
    pre -- "no audience" --> mism
    size -- no --> bad
    hdr -- no --> bad
    key -- "unknown kid" --> bad
    key -- "JWKS unavailable" --> keysDown
    sig -- invalid --> bad
    claims -- no --> bad
    iss -- no --> bad
    tenant -- no --> bad
    sealed -- no --> bad
    nbf -- no --> early
    exp -- no --> expired
    aud -- no --> mism
    scheme -- no --> wrong
    pop -- no --> dpopErr
    rev -- yes --> revoked
    brev -- yes --> revoked

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class tok,ok client
    class pre,size,form,hdr,sig,claims,iss,tenant,sealed,nbf,exp,aud,scheme,pop role
    class key,rev,brev store
    class bad,early,expired,mism,wrong,dpopErr,keysDown,revoked,biscuit external
```

An unknown kid, an untrusted issuer, a kid bound to another issuer and a
missing tenant all match `ErrInvalidToken`; an unknown kid also matches
`jwks.ErrUnknownKID`, and the rest carry nothing more to branch on. An unreachable
JWKS is the server's fault, not the token's: it is `jwks.ErrUnavailable` alone,
which `ErrorResponse` answers with 503. The DPoP refusals and
`ErrAuthSchemeMismatch` match `ErrProofOfPossession` — the token may be
genuine, the proof is what failed — and every refusal of the credentials,
token or proof, matches `ErrUnauthenticated`, as does `ErrNoCredentials`
from `ParseAuthorization`.

A Biscuit goes through the same gates, around them. Its authority block seals
an ordinary signed token, which passes the gates from the header to the tenant;
that token names the key rooting the Biscuit's chain, which is checked next,
and then each attenuation block is folded in. The time window, the audience,
the scheme and the proof of possession — over the Biscuit as presented — apply
once, to the narrowed capability that comes out, and both revocation lists are
asked last.

```mermaid
flowchart TB
    tok(["Biscuit, audience"])
    on{"AcceptBiscuit"}
    auth{"authority block: exactly one<br/>limes_capability fact, no rules"}
    sealed["the sealed token: header · key · signature<br/>claims · issuer · tenant — and it carries BiscuitRoot"]
    chain{"signature chain verifies<br/>against BiscuitRoot"}
    subgraph fold ["each attenuation block, in order"]
        direction TB
        vocab{"only limes_* facts<br/>no rules · no checks"}
        narrow{"Narrows(cur, next)<br/>ops · resources · planes · expiry · binding"}
        lim{"sets limes_max_requests /<br/>limes_max_budget_nanos?"}
        meter{"MeterCopies"}
        fit{"within the capability's limit<br/>and every enclosing copy's"}
        copies["Capability.Copies gains<br/>{revocation id, limits}, innermost first"]
        vocab -- yes --> narrow -- yes --> lim
        lim -- yes --> meter -- yes --> fit -- yes --> copies
    end
    post["time window · audience · scheme<br/>DPoP over the Biscuit as presented"]
    rev{"IsRevoked(capability)"}
    brev{"IsBiscuitRevoked(revocation ids)<br/>any block of this token"}
    ok(["Capability with Copies"])

    bad["ErrInvalidToken"]
    att["ErrBiscuitAttenuation"]
    unmetered["ErrCopyCountersNotMetered"]
    revoked["ErrRevoked"]

    tok --> on -- yes --> auth -- yes --> sealed --> chain -- yes --> vocab
    lim -- no --> post
    copies --> post
    copies -. "next block" .-> vocab
    lim -. "next block" .-> vocab
    post --> rev
    auth -- no --> bad
    rev -- no --> brev -- no --> ok
    on -- no --> bad
    chain -- no --> bad
    vocab -- no --> att
    narrow -- widens --> att
    fit -- no --> att
    meter -- no --> unmetered
    rev -- yes --> revoked
    brev -- yes --> revoked

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class tok,ok client
    class on,auth,sealed,chain,vocab,narrow,lim,meter,fit,copies,post role
    class rev,brev store
    class bad,att,unmetered,revoked external
```

The sealed token presented on its own is refused, so it cannot be lifted out
to shed an attenuation. `ErrBiscuitAttenuation` and
`ErrCopyCountersNotMetered` both match `ErrInvalidToken`: the token is not
one this verifier accepts.

## Proof of possession

A key-bound capability (`ConfirmationJKT` set) needs a DPoP proof (RFC 9449)
for every request; a verifier with no `dpop.Verifier` refuses it outright with
`dpop.ErrRequired`. The `dpop` package checks the proof in this order and
returns its own sentinel; the verifier wraps each refusal in
`ErrProofOfPossession`, and passes a replay cache or nonce store that cannot
answer through as an outage:

```mermaid
flowchart TB
    p(["dpop.Proof<br/>Proof · Method · URL"])
    has{"proof present"}
    shape{"three segments · typ dpop+jwt<br/>a jwk with no private member"}
    jkt{"the jwk's RFC 7638 thumbprint<br/>= ConfirmationJKT"}
    psig{"signature by that key<br/>Ed25519 (or EdDSA) · ES256"}
    htm{"htm = Method, exactly<br/>(a method is case-sensitive)"}
    htu{"htu = URL after RFC 3986 normalisation<br/>or, under MatchPathOnly, its path a suffix"}
    ath{"ath = hash of the token as presented"}
    iat{"|now − iat| ≤ Window<br/>(dpop.DefaultWindow, 1 minute)<br/>iat may be fractional"}
    nonce{"Nonces set → a nonce they issued"}
    jti{"ReplayCache.Seen(jkt, jti, iat + Window)"}
    ok(["proof accepted"])

    req["dpop.ErrRequired"]
    inv["dpop.ErrInvalid"]
    nn["dpop.ErrNonceRequired<br/>answer with a fresh DPoP-Nonce"]
    rp["dpop.ErrReplayed"]

    p --> has -- yes --> shape -- yes --> jkt -- yes --> psig -- yes --> htm -- yes --> htu -- yes --> ath -- yes --> iat -- yes --> nonce -- yes --> jti -- "new" --> ok
    has -- no --> req
    shape -- no --> inv
    jkt -- no --> inv
    psig -- no --> inv
    htm -- no --> inv
    htu -- no --> inv
    ath -- no --> inv
    iat -- no --> inv
    nonce -- no --> nn
    jti -- "seen, or cannot tell" --> rp

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class p,ok client
    class has,shape,jkt,psig,htm,htu,ath,iat role
    class nonce,jti store
    class req,inv,nn,rp external
```

`dpop.HMACNonces` makes a nonce of its issuance time and an HMAC of it, so replicas
holding the same keys accept each other's nonces with no shared state, given
clocks within `dpop.HMACNonceClockSkew`. `dpop.MemoryReplayCache` gives each key a share
of its own, so one key past its share has only its own proofs refused; a full
cache refuses rather than forgets.

## Answering a refusal

`StandardVerifier.ErrorResponse` maps a refusal to how a protected resource
answers it, from the same table `ReasonOf` reads, and `WriteErrorResponse`
writes it as `application/problem+json` with `Cache-Control: no-store`. The
error's own text never reaches the client.

| Refusal | Status | `WWW-Authenticate` |
|---|---|---|
| `jwks.ErrUnavailable` | 503 | none |
| `ErrNoCredentials` | 401 | a bare challenge, no error code (RFC 6750 §3.1): no header, or another scheme's |
| `ErrMalformedAuthorization` | 400 | `error="invalid_request"` under the client's scheme (Bearer when unknown) |
| `dpop.ErrNonceRequired` | 401 | `DPoP error="use_dpop_nonce"`, and a fresh `DPoP-Nonce` |
| `dpop.ErrRequired` · `dpop.ErrInvalid` · `dpop.ErrReplayed`, and any other `ErrProofOfPossession` | 401 | `DPoP error="invalid_dpop_proof", algs="Ed25519 ES256"` |
| `ErrAuthSchemeMismatch` · `ErrInvalidToken` · `ErrExpired` · `ErrNotYetValid` · `ErrRevoked` · `ErrAudienceMismatch`, and any other `ErrUnauthenticated` | 401 | `error="invalid_token"` under the client's scheme (Bearer when unknown) |
| any `ErrCaveatViolation` | 403 | `error="insufficient_scope"` |
| `ErrBudgetExceeded` · `ErrTenantBudgetExceeded` · `ErrRequestLimitExceeded` | 403 | none |
| anything else — a store or key resolver outage, `ErrEffectConflict` | 500 | none |

A verifier with DPoP configured adds a bare `DPoP algs="…"` challenge beside a
`Bearer` one.

## What the bearer may do

Verification proves the token is genuine. What the bearer may do with it is a
second step, evaluated per operation: `Authorize` runs `Verify`, then
`Caveats.CheckSource(ClientAddr)`, then `Caveats.Check`:

```mermaid
flowchart LR
    req(["CheckRequest<br/>Op · Effect · Resource · HasIdempotencyKey · ResourceTainted"])
    eff{"Op.ResolveEffect(Effect)<br/>read or write"}
    op{"Op ∈ Ops"}
    res{"AllowsResource(Resource)<br/>prefix at a / boundary"}
    idem{"write and<br/>IdempotencyKeyRequired<br/>→ key present"}
    taint{"read of a tainted resource<br/>→ AllowTaintedRead"}
    ok(["nil"])

    e1["ErrOpNotAllowed"]
    e2["ErrResourceNotAllowed"]
    e3["ErrIdempotencyKeyRequired"]
    e4["ErrTaintedReadNotAllowed"]
    e0["ErrEffectConflict"]

    req --> eff -- resolved --> op -- yes --> res -- yes --> idem -- yes --> taint -- yes --> ok
    op -- no --> e1
    res -- no --> e2
    idem -- no --> e3
    taint -- no --> e4
    eff -- "unknown Effect, or a built-in Op declared otherwise" --> e0

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class req,ok client
    class eff,op,res,idem,taint role
    class e0,e1,e2,e3,e4 external
```

A built-in operation keeps its own effect; a consumer's operation is a write
unless its `Effect` says otherwise. `ErrEffectConflict` is a programming error,
not a caveat violation; each of the other four matches `ErrCaveatViolation`.
The source-address caveat
is per connection and checked by `Caveats.CheckSource`, which fails closed on
an unknown address with `ErrSourceIPNotAllowed`. Budget and request ceilings
are stateful and belong to the `UsageStore`.

## Delegation: attenuate, never escalate

`Issuer.Delegate` refuses a parent read from a Biscuit copy, fills what the request leaves out from the parent — tenant, audience,
key binding, and the caveats under `InheritCaveats` — refuses a parent that is
expired or revoked, clamps the child's window inside the parent's, and then
requires `Narrows(parent, child)` to pass before anything is persisted or
signed.

```mermaid
flowchart TB
    req(["DelegateRequest<br/>Parent · Subject · Audience · Caveats or InheritCaveats<br/>TTL · NotBefore · ConfirmationJKT"])
    pid{"Parent.ID set"}
    copyp{"Parent.Copies empty<br/>(not a copy with limits of its own)"}
    fill["unset tenant, audience, binding<br/>taken from the parent"]
    valid{"audience valid · Caveats.Validate<br/>thumbprint valid"}
    alive{"parent not expired<br/>Store.IsRevoked(parent) = false"}
    clamp["ExpiresAt = min(now + TTL, parent.ExpiresAt)"]
    start["starts no earlier than the parent:<br/>NotBefore raised to the parent's start<br/>(its NotBefore, else IssuedAt)"]

    subgraph nar ["Narrows(parent, child)"]
        direction TB
        n0["Audience ⊆ parent's · ExpiresAt ≤ parent's<br/>starts no earlier than the parent<br/>same tenant · a bound parent's child stays bound"]
        n1["Ops ⊆ parent.Ops"]
        n2["every resource reachable by the parent<br/>exact-URI parent admits no child prefix"]
        n3["Requests within the parent's: any under Unlimited,<br/>a bound no higher under a bound"]
        n4["UnitCode = parent's"]
        n5["Budget within the parent's, the same way"]
        n6["AllowTaintedRead only if the parent has it<br/>IdempotencyKeyRequired never relaxed"]
        n7["SourceIPCIDR inside a parent network"]
        n0 --> n1 --> n2 --> n3 --> n4 --> n5 --> n6 --> n7
    end

    persist["Store.Insert(child, issuedBy = parent.Subject)<br/>Signer.Sign(ctx, child)"]
    ok(["child, childToken"])

    wide["ErrDelegationTooWide"]
    unit["ErrUnitCodeMismatch"]
    dead["ErrExpired · ErrRevoked<br/>or the store's error"]
    inval["ErrInvalidRequest · ErrInvalidCaveats"]

    req --> pid -- yes --> copyp -- yes --> fill --> valid -- yes --> alive -- yes --> clamp --> start --> n0
    n7 --> persist --> ok
    pid -- no --> inval
    valid -- no --> inval
    start -- "window empty" --> inval
    alive -- no --> dead
    n4 -- differs --> unit
    nar -- widens --> wide
    copyp -- no --> wide

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class req,ok client
    class pid,fill,valid,copyp,clamp,start,n0,n1,n2,n3,n4,n5,n6,n7 role
    class alive,persist store
    class wide,unit,dead,inval external
```

Narrowing bounds each child on its own. The tree as a whole is bounded at use:
every charge also counts against each ancestor, read from the ancestor's
stored record. A parent holding 25.00 that delegates 20.00 to each of two
workers can still spend 25.00 in total, not 40.00:

```mermaid
flowchart TB
    p["<b>orchestrator</b><br/>Budget 25.00<br/>spent 25.00 = 15.00 + 10.00"]
    a["<b>worker A</b><br/>Budget 20.00<br/>spent 15.00"]
    b["<b>worker B</b><br/>Budget 20.00<br/>spent 10.00"]
    next(["worker B charges 1.00 more"])
    deny["ErrBudgetExceeded: ancestor<br/>B's own ceiling allows it, the parent's does not"]

    p -- "ParentID" --- a
    p -- "ParentID" --- b
    next --> b
    b -. "ancestor ceiling 25.00 + 1.00" .-> deny

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class next client
    class p,a,b role
    class deny external
```

## Biscuit copies: one tree, revoked and counted by block

Every attenuation appends a block, and every block has a revocation id — its
signature. A copy carries the ids of every block above it, so an id names a
copy *and everything attenuated from it*. The same ids key both of the things
a holder's copy can have on its own: a revocation, and limits.

```mermaid
flowchart TB
    cap(["capability<br/>Requests 100 · budget 10.00"])
    root["<b>root Biscuit</b><br/>authority block: id A"]
    w1["<b>worker 1</b><br/>ids A · B<br/>block B: limes_max_requests 30"]
    w2["<b>worker 2</b><br/>ids A · C<br/>block C: limes_max_budget_nanos 2000000000 (2.00)"]
    sub["<b>sub-agent of worker 1</b><br/>ids A · B · D<br/>block D: limes_max_requests 5"]
    jwt["the capability's JWT"]

    cap --- root
    cap --- jwt
    root -- "Attenuate" --> w1
    root -- "Attenuate" --> w2
    w1 -- "Attenuate" --> sub

    revB["RevokeBiscuit(worker 1)<br/>lists B: worker 1 and the sub-agent stop;<br/>root, worker 2 and the JWT do not"]
    cntD["a request by the sub-agent counts on D, on B<br/>and on the capability; each limit is checked"]

    w1 -.-> revB
    sub -.-> cntD

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef optional fill:#F1F5F9,stroke:#64748B,color:#334155,stroke-dasharray:5 4
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    class cap client
    class root,w1,w2,sub,jwt role
    class revB,cntD store
```

- **Revocation.** `BiscuitCopy` checks that a copy is genuine — its size, the
  sealed token's signature, issuer and tenant, and its chain and blocks — but
  not its expiry, audience, proof or revocation, since a copy can be revoked
  whatever its state; it names the capability and the last block's id, which
  `BiscuitRevocationStore.RevokeBiscuit` lists. Revoking the capability still
  stops every copy and the JWT.
- **Limits.** A block may set `limes_max_requests` and `limes_max_budget_nanos`, within
  every limit in force. The `Meter` counts each under the block's id, beside
  the capability's own counters, and a charge or reservation records the ids
  it debited so a refund, settle or release returns to them.
  `CopyUsageReader` reads the counters back.
- **Delegation from a copy** narrows from the copy as presented, not from the
  capability's record. A copy with limits of its own cannot delegate:
  `Issuer.Delegate` refuses a `Parent` with `Copies` (`ErrDelegationTooWide`),
  because a server-side child counts against the capability and never the
  copy, and would shed its limits.

## Charge: stage, then commit

Every amount on this path is `Nanos`, an `int64` count of billionths of the
unit, so each comparison and sum below is exact.

`Meter.Charge` checks every ceiling — each Biscuit copy's, the capability's,
each ancestor's and the tenant's — and runs the consumer's side effect before
any counter moves. A rejection at any stage, or a failing `onCharged`, leaves
the capability's counter, every ancestor's, the tenant aggregate and the
ledger exactly as they were, so a retry after a rejection is safe.

A cost known only afterwards goes through a reservation instead. `Reserve`
holds an estimate against the same ceilings, and `Settle` charges the
actual cost along the same path, releasing the hold in the same commit. Every
ceiling counts open holds as well as spend, so two callers cannot both hold
the last of a budget.

```mermaid
flowchart TB
    req(["ChargeRequest<br/>Subject (id · tenant · ceilings · unit · copies) · Amount<br/>ExternalRef · Overrun"])
    sreq(["SettleRequest<br/>Subject · ReservationID · Amount · Overrun"])
    sval{"Nanos.Validate · Overrun.Validate<br/>ReservationID not zero"}
    hold{"reservation open<br/>and the subject's own"}
    val{"Nanos.Validate · Overrun.Validate<br/>ValidateExternalRef · NormalizeUnitCode"}
    seen{"ExternalRef already charged<br/>to this capability"}
    same{"same Amount · UnitCode · TenantID"}

    subgraph stage ["stage — compute, publish nothing"]
        direction TB
        c00{"Amount in the unit every counter it moves counts in<br/>no counter past MaxNanos"}
        c0{"each Biscuit copy, innermost first:<br/>spend + reserved + Amount ≤ its budget"}
        c1{"own spend + reserved + Amount ≤ Subject.Budget<br/>(ceiling from the verified token or stored record)"}
        c2{"each ancestor: spend + reserved + Amount<br/>≤ its Budget<br/>(ceiling from the stored record)"}
        c3{"tenant spend + reserved + Amount<br/>≤ TenantBudget.Ceiling<br/>(Unlimited until one is set)"}
        cb{"onCharged(ctx, tx)<br/>consumer's side effect on its own TX"}
        c00 -- yes --> c0 -- yes --> c1 -- yes --> c2 -- yes --> c3 -- yes --> cb
    end

    subgraph commit ["commit — all together"]
        direction TB
        w5["each copy's spend += Amount"]
        w1["capability spend += Amount"]
        w2["each ancestor spend += Amount"]
        w3["tenant spend += Amount<br/>(the row created, with no ceiling, if missing)"]
        w0["settling: the hold leaves reserved everywhere"]
        w4["ledger row: ChargeID · TenantID · Amount · UnitCode · At<br/>Ancestors · copy ids · Op · Actor · ExternalRef<br/>ReservationID · Overrun · tenant period"]
    end

    ok(["ChargeReceipt{ChargeID, SpentAmount, Overrun, TenantID, UnitCode}"])
    replay(["ChargeReceipt{Replayed}<br/>the earlier charge; nothing moves"])
    refund["Refund(RefundRequest{ChargeID, Amount or Remaining}) → RefundReceipt<br/>returns spend to every counter the charge took it from;<br/>the tenant's only within the same accounting period"]

    e0["ErrInvalidAmount · ErrInvalidRequest<br/>ErrUnitCodeMismatch"]
    e1["ErrBudgetExceeded"]
    e2["ErrBudgetExceeded: ancestor"]
    e3["ErrTenantBudgetExceeded"]
    e4["the callback's error"]
    e5["ErrReservationNotFound"]
    e6["ErrExternalRefConflict"]

    req --> val -- ok --> seen -- no --> c00
    seen -- yes --> same -- yes --> replay
    same -- no --> e6
    sreq --> sval -- ok --> hold -- "yes: its own hold<br/>is not counted" --> c00
    sval -- "invalid" --> e0
    sval -- "zero id" --> e5
    hold -- "settled" --> replay
    hold -- "released · expired" --> e5
    cb -- nil --> w5 --> w1 --> w2 --> w3 --> w0 --> w4 --> ok
    ok -. "later" .-> refund
    val -- invalid --> e0
    c0 -- "no, OverrunReject" --> e1
    c1 -- "no, OverrunReject" --> e1
    c2 -- "no, OverrunReject" --> e2
    c3 -- "no, OverrunReject" --> e3
    cb -- error --> e4
    c00 -- no --> e0

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    classDef optional fill:#F1F5F9,stroke:#64748B,color:#334155,stroke-dasharray:5 4
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class req,sreq,ok,replay client
    class val,sval,seen,same,hold,c00,c0,c1,c2,c3 role
    class cb optional
    class w5,w1,w2,w3,w0,w4,refund store
    class e0,e1,e2,e3,e4,e5,e6 external
```

`Bump` takes the same order for requests: each copy's limit, then the
capability's, then each ancestor's. A reservation keeps its copies' budgets,
so `Settle` checks them without being handed the token again.

A settle rejected by a ceiling leaves the reservation in place, to be settled
lower or released with `Release`. Under `OverrunRecord` no ceiling rejects: a
check that fails marks the charge as an overrun and the stage goes on, since
the cost has already been incurred; the crossed ceiling then refuses whatever
follows under `OverrunReject`. A mismatched unit or a counter past `MaxNanos`
is refused even then: neither is a ceiling an overrun may cross. A charge
named by `ExternalRef`, or a settle of a reservation already settled, returns
the earlier charge and moves nothing; the same `ExternalRef` with another
amount, unit or tenant is `ErrExternalRefConflict`.

Holds and housekeeping, in `memstore` as the contract has them:

- `Reserve` checks the same ceilings — never overrun — and holds the amount on
  the capability, each ancestor, the tenant (creating its row) and each copy;
  `Release` and `ReleaseExpired` take it off again. A hold never settled lapses
  at its `ExpiresAt` (default `DefaultReservationTTL`, five minutes) but keeps
  counting until `ReleaseExpired` runs, which errs towards refusing spend.
- `Delete` releases the capability's holds before it drops its counters, and
  `PurgeOrphans` releases the holds of every capability no longer on record:
  nothing could settle or release them afterwards.
- `SetTenantBudget` is guarded by `ExpectedVersion` (0 for a tenant with no
  ceiling yet), refuses a new unit while anything is held in the old one, or
  spent in it unless `ResetSpend` closes that spend, and with `ResetSpend` opens a new accounting period, so a refund of an
  earlier period's charge leaves the new period's spend alone.

A store with transactions runs stage and commit inside one, and passes its own
handle as `tx`; a store without them is instantiated as
`UsageStore[struct{}]` and the caller passes `nil` for `onCharged`.

## Revocation reaches the verifiers

Revocation is a write to the `Store`; verifiers see it through
`IsRevoked`, which answers for the whole delegation chain, usually behind a
`CachedRevocationChecker`.

```mermaid
flowchart LR
    op(["Revoke(RevokeRequest{CapabilityID, Note, Actor, CascadeChildren})"])
    store[("<b>Store</b><br/>revoked set<br/>ParentID links")]
    cascade["CascadeChildren:<br/>a revocation entry per descendant,<br/>for the audit trail"]
    chain["IsRevoked(id):<br/>id or any ancestor revoked"]
    cache["<b>CachedRevocationChecker</b><br/>answer cached for TTL (default 2s)<br/>one upstream call per id at a time<br/>bounded: the oldest answer makes room<br/>errors never cached · a Clear mid-lookup<br/>discards that lookup's answer"]
    notify{{"consumer's revocation signal<br/>e.g. a Postgres channel"}}
    bop(["RevokeBiscuit(RevokeBiscuitRequest{CapabilityID, RevocationID})"])
    blist[("<b>BiscuitRevocationStore</b><br/>revoked block ids")]
    bcache["<b>CachedBiscuitRevocationChecker</b><br/>answer per token, cached for TTL<br/>bounded · dropped by Clear"]
    v1["StandardVerifier"]
    v2["StandardVerifier"]

    op --> store
    store -.-> cascade
    store --> chain --> cache
    cache --> v1
    cache --> v2
    notify -. "Clear()" .-> cache
    bop --> blist --> bcache
    bcache --> v1
    bcache --> v2
    notify -. "Clear()" .-> bcache

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef optional fill:#F1F5F9,stroke:#64748B,color:#334155,stroke-dasharray:5 4
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class op,bop client
    class v1,v2,cache,bcache role
    class cascade optional
    class store,chain,blist store
    class notify external
```

Revoking a capability stops everything delegated from it whether or not
`CascadeChildren` is set, because `IsRevoked` walks the ancestors; the flag
only adds the per-descendant entries that name each stopped capability. Without
a revocation signal, a verifier learns of a revocation within the cache TTL.
Each cache is dropped whole: an answer cached for a descendant is also an
answer about the revoked ancestor. A `Clear` also detaches any lookup in flight, whose answer may
predate the revocation, so it is neither cached nor shared with later checks.
Revoked Biscuit copies travel the same way, on their own list and cache; one
signal clears both.

A verifier that runs apart from the issuer resolves keys with
`jwks.Remote`. It asks for `application/jwk-set+json`, keeps a set as
long as the response's `Cache-Control` says — `max-age` less `Age`, zero for
`no-cache` or `no-store` — within `MinRefreshInterval` and `RefreshInterval`
(`RefreshInterval` when the response says nothing), revalidates with an ETag,
refetches on an unknown kid at most once per `MinRefreshInterval`, and fails
closed with `jwks.ErrUnavailable` when it has never fetched a set, or once the
last good set is older than `MaxStale`. A document with entries but no usable
Ed25519 key counts as a failed fetch, so the last good set stays in service;
two entries under one kid are both dropped. The issuer serves the set with `jwks.NewHandler`: a strong ETag
answering `If-None-Match` with 304, and `Cache-Control: public, max-age`.
