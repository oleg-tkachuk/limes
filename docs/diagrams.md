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
storage behind them, or takes one of the stores this repository ships:
`memstore` in memory, and the nested modules `pgstore`, every store contract
over PostgreSQL with row-level security, `redisreplay`, a
`dpop.ReplayCache` over Redis, and `otelmetric`, OpenTelemetry instruments
around a `meter.UsageStore`. Each nested module has its own `go.mod` and its own tag
line, so the root module's dependency graph stays free of a driver and of
OpenTelemetry.
The two Biscuit contracts are needed only by a consumer that accepts the
Biscuit form, `dpop.ReplayCache` and `dpop.Nonces` only by one that binds
capabilities to keys, `meter.LedgerReader` only by one that bills or reports from the
charges ledger, `RecordCensus` only by one that reports on records, and the two tenant erasers only by one that removes a tenant. `storetest`, `metertest`, `dpop/dpoptest` and
`jwks/jwkstest` check an implementation against its contract from its own
tests — the Biscuit and copy contracts too, when the store under test
implements them — and bound every check by `Env.CheckTimeout`, so a store that
deadlocks fails the check instead of hanging the run.

```mermaid
flowchart LR
    caller(["Consumer service<br/>orchestrator · tool · API"])

    subgraph mod ["limes (this module)"]
        direction TB
        issuer["<b>Issuer</b><br/>Issue · Delegate · Biscuit"]
        signer["<b>Signer</b><br/>NewSigner · JWT Ed25519"]
        narrows["<b>Narrows</b><br/>child ⊆ parent"]
        verifier["<b>Verifier</b><br/>Authorize · Verify · BiscuitCopy<br/>ErrorResponse · DPoPNonce"]
        caveats["<b>Caveats</b><br/>Check · CheckSource · AllowsResource"]
        cache["RevocationCache<br/>TTL · bounded, oldest out · single-flight"]
        bcache["BiscuitRevocationCache<br/>per revocation id · TTL · bounded · single-flight"]
        attenuate["<b>Attenuate</b><br/>offline · no key"]
        static["jwks.Static<br/>SetKey · RemoveKey"]
        remote["jwks.Remote<br/>ETag · Cache-Control · MaxStale<br/>held keys served, refreshed behind"]
        dpop["dpop.Verifier<br/>VerifierConfig.DPoP · dpop.Sign"]
        suites["storetest · metertest<br/>dpoptest · jwkstest<br/>conformance suites · CheckTimeout"]
        jwksh["jwks.NewHandler<br/>ETag · max-age"]
        nonce["dpop.HMACNonces<br/>stateless DPoP nonces"]
    end

    subgraph contracts ["contracts the consumer implements"]
        direction TB
        store["<b>Store</b> = IssuanceStore + admin<br/>Insert · IsRevoked · GetRecord · Revoke<br/>Purge · ListByPrincipal"]
        usage["<b>meter.UsageStore</b><br/>Meter: Bump · Charge · Reserve · Settle · Refund · Release<br/>MeterReader: GetUsage · GetCharge · ChargeByRef · GetReservation · ListReservations<br/>TenantBudgets · UsageHousekeeping"]
        keys["<b>KeyResolver</b><br/>PublicKey(kid)"]
        brev["<b>BiscuitRevocationStore</b><br/>IsBiscuitRevoked · RevokeBiscuit<br/>GetBiscuitRevocation"]
        cusage["<b>meter.CopyUsageReader</b><br/>CopyUsage(CopyUsageRequest{CapabilityID, RevocationIDs})<br/>→ CopyUsageResult{Copies}"]
        replay["<b>dpop.ReplayCache</b><br/>Seen(ctx, ReplayEntry) · MemoryReplayCache ships"]
        nonces["<b>dpop.Nonces</b><br/>Nonce · Valid"]
        ledger["<b>meter.LedgerReader</b><br/>SumCharges(SumChargesRequest) → Page[ChargeSum]<br/>one tenant or every tenant · by unit · bucket none/hour/day/week/month<br/>GroupBy tenant · op · actor · capability · Filter"]
        census["<b>RecordCensus</b><br/>CountRecords(CountRecordsRequest) → Page[RecordCount]<br/>one tenant or all · by tenant · principal type<br/>active · expired · revoked · delegated · expiring soon"]
        erasers["<b>TenantRecordEraser · meter.TenantUsageEraser</b><br/>EraseTenantRecords · EraseTenantUsage"]
    end

    impl[("memstore · your own store")]
    pg["<b>pgstore</b> (nested module)<br/>Store · BiscuitRevocationStore · erasers<br/>UsageStore · CopyUsageReader · Ledger · Census<br/>Migrate · CheckSchema · EraseTenant<br/>WithTenant · WithAllTenants · WithAllTenantsReadOnly<br/>RevocationWatcher · row-level security"]
    rr["<b>redisreplay</b> (nested module)<br/>Cache: dpop.ReplayCache over Redis"]
    metering["<b>otelmetric</b> (nested module)<br/>Instrument: OTel counters · spend histogram<br/>WithMeterProvider · WithTenantAttribute"]
    pgdb{{"PostgreSQL"}}
    redis{{"Redis"}}
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
    verifier -. "VerifierConfig.Biscuit" .-> bcache --> brev
    caller -. "admin read" .-> cusage
    caller -. "billing read" .-> ledger
    caller -. "records report" .-> census
    caller -. "remove a tenant" .-> erasers
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
    suites -. "check" .-> brev
    suites -. "check" .-> cusage
    suites -. "check" .-> ledger
    suites -. "check" .-> census
    suites -. "check" .-> erasers
    store --> impl
    usage --> impl
    brev --> impl
    cusage --> impl
    ledger --> impl
    census --> impl
    erasers --> impl
    pg -. "implements" .-> store
    pg -. "implements" .-> usage
    pg -. "implements" .-> brev
    pg -. "implements" .-> cusage
    pg -. "implements" .-> ledger
    pg -. "implements" .-> census
    pg -. "implements" .-> erasers
    pg --> pgdb
    rr -. "implements" .-> replay
    rr --> redis

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef optional fill:#F1F5F9,stroke:#64748B,color:#334155,stroke-dasharray:5 4
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class caller client
    class issuer,signer,narrows,verifier,caveats role
    class cache,metering,static,remote,bcache,attenuate,dpop,suites,jwksh,nonce,pg,rr optional
    class store,usage,keys,impl,brev,cusage,replay,nonces,ledger,census,erasers store
    class jwks,otel,pgdb,redis external
```

The roles take narrower contracts than the stores implement: `IssuerConfig.Store`
is an `IssuanceStore` (Insert · IsRevoked), `VerifierConfig.Revocations` a
`RevocationLookup` and `BiscuitConfig.Revocations` a
`BiscuitRevocationLookup`. A `Meter` counts against a `MeterSubject`, which
only `VerifiedCapability.MeterSubject` or `StoredMeterSubject` over a
`RecordReader` builds, so a charge always names a capability the verifier or
the store vouched for.

Every read names what it reads — `Store.GetRecord`, `MeterReader.GetUsage`,
`TenantBudgets.GetTenantBudget` — so one type may implement `Store` and
`UsageStore` together, or each apart as `memstore` does. A store puts its
transaction in the context `Meter.Charge` and `Meter.Settle` hand
`onCharged`, read through its own accessor (`pgstore.TxFrom`); the module
never names a transaction type, which is why it has no database dependency.

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
    I->>I: Caveats.Validate · TTL → ExpiresAt, after IssuedAt
    I->>I: sign with the Signer, within MaxTokenBytes
    I->>St: Store.Insert(InsertRequest{cap, IssuedBy})
    I-->>O: token

    O->>I: Delegate(DelegateRequest{Parent: Verify(token), Caveats})
    I->>I: parent not a Biscuit copy · same issuer · allows OpShare
    I->>I: Subject · Audience · Caveats.Validate · parent not expired
    I->>St: Store.IsRevoked(parent)
    I->>I: Narrows(parent, child)
    I->>I: sign the child with the Signer
    I->>St: Store.Insert(InsertRequest{child, IssuedBy: parent.Subject}) — depth within MaxDelegationDepth
    I->>St: Store.IsRevoked(child)
    I-->>W: child token

    W->>S: request + child token
    S->>S: Authorize: Verify — signature · time · audience · DPoP
    S->>St: IsRevoked(child), inside Verify — via RevocationCache
    S->>S: Authorize: CheckSource(addr) · Check(op, resource)
    S->>St: Bump · Charge, or Reserve then Settle — against VerifiedCapability.MeterSubject()
    St-->>S: BumpReceipt · ChargeReceipt{ChargeID, SpentAmount} · Reservation
    S-->>W: response

    Op->>St: Store.Revoke(RevokeRequest{CapabilityID: parent, CascadeChildren})
    Note over S,St: the next IsRevoked for the child answers true<br/>once the cache entry expires, or at once after Clear
    W->>S: request + child token
    S-->>W: ErrRevoked
```

## Verification gates

`Verifier.Verify` reads nothing from the claims until the signature
over them has verified, checks a key-bound capability's proof of possession,
and asks the revocation store after every check that needs no I/O, so that
cheap rejections never reach it. A token carrying a Biscuit copy's own limits
is then refused at a verifier without `MeterCopies`, and only a token accepted
after all of that has its proof's id recorded in the replay cache: that room is
shared by every copy of the capability, so a revoked or refused token spends
none of it.
Every refusal is a sentinel the caller maps to its own transport status, or
hands to `ErrorResponse`. An outage is not a refusal: a key resolver other
than for an unknown kid, a revocation lookup, a replay cache or a nonce
source that cannot answer returns its error wrapped, and a key-bound
capability at a verifier with no `dpop.Verifier` is `ErrProofUnsupported` —
the deployment's fault, answered 500.

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
    iss{"iss ∈ TrustedIssuers<br/>KeyIssuers[kid] = iss, when KeyIssuers is set"}
    tenant{"Subject.TenantID set<br/>Subject.Validate · Caveats.Validate"}
    sealed{"names no Biscuit root, limes_bsk<br/>(not lifted out of a Biscuit)"}
    nbf{"now + Leeway ≥ NotBefore"}
    exp{"now − Leeway < ExpiresAt"}
    aud{"Audience ∈ token's audience"}
    scheme{"Scheme, if set, matches the binding<br/>DPoP for a key-bound token, Bearer otherwise"}
    bound{"key-bound: a dpop.Verifier<br/>is configured"}
    pop{"dpop.Verifier.Check<br/>the proof checks out"}
    rev{"RevocationLookup.IsRevoked<br/>this capability or an ancestor"}
    brev{"Biscuit: IsBiscuitRevoked<br/>any block of this copy"}
    meter{"carries a copy's own limits,<br/>and no MeterCopies"}
    rec{"key-bound: dpop.Verifier.Record<br/>the proof's id, in the replay cache"}
    ok(["VerifiedCapability"])

    bad["ErrInvalidToken"]
    early["ErrNotYetValid"]
    expired["ErrExpired"]
    mism["ErrAudienceMismatch"]
    wrong["ErrAuthSchemeMismatch"]
    dpopErr["ErrProofOfPossession<br/>dpop.ErrRequired · dpop.ErrInvalid<br/>dpop.ErrNonceRequired · dpop.ErrReplayed"]
    unsup["ErrProofUnsupported<br/>(500, the deployment's fault)"]
    keysDown["jwks.ErrUnavailable<br/>(503, the server's fault)"]
    outage["the store's, resolver's, replay cache's<br/>or nonce source's error, wrapped (500)"]
    revoked["ErrRevoked"]
    unmetered["ErrCopyCountersNotMetered<br/>(500, the deployment's fault)"]
    biscuit["the Biscuit path below"]
    noCreds["ErrNoCredentials"]
    invalid["ErrInvalidRequest<br/>(the server's own call)"]

    tok --> pre -- yes --> size -- yes --> form -- no --> hdr -- yes --> key -- found --> sig -- valid --> claims -- yes --> iss -- yes --> tenant -- yes --> sealed -- yes --> nbf
    form -- yes --> biscuit
    nbf -- yes --> exp -- yes --> aud -- yes --> scheme -- yes --> bound -- "unbound, or configured" --> pop -- "unbound, or valid proof" --> rev -- no --> brev -- "no, or not a Biscuit" --> meter -- no --> rec -- "unbound, or first use" --> ok
    pre -- "no token" --> noCreds
    pre -- "no audience" --> invalid
    size -- no --> bad
    hdr -- no --> bad
    key -- "unknown kid" --> bad
    key -- "JWKS unavailable" --> keysDown
    key -- "other resolver error" --> outage
    sig -- invalid --> bad
    claims -- no --> bad
    iss -- no --> bad
    tenant -- no --> bad
    sealed -- no --> bad
    nbf -- no --> early
    exp -- no --> expired
    aud -- no --> mism
    scheme -- "mismatch" --> wrong
    scheme -- "not Bearer, DPoP or unset" --> invalid
    bound -- "no dpop.Verifier" --> unsup
    pop -- no --> dpopErr
    pop -- "cannot answer" --> outage
    rev -- yes --> revoked
    rev -- "lookup fails" --> outage
    brev -- yes --> revoked
    brev -- "lookup fails" --> outage
    meter -- yes --> unmetered
    rec -- "seen before" --> dpopErr
    rec -- "cannot answer" --> outage

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class tok,ok client
    class pre,size,form,hdr,sig,claims,iss,tenant,sealed,nbf,exp,aud,scheme,bound,pop,meter role
    class key,rev,brev,rec store
    class bad,early,expired,mism,wrong,dpopErr,unsup,keysDown,outage,revoked,unmetered,biscuit,noCreds,invalid external
```

An unknown kid, an untrusted issuer, a kid bound to another issuer and a
missing tenant all match `ErrInvalidToken`; an unknown kid also matches
`jwks.ErrUnknownKID`, and the rest carry nothing more to branch on. An unreachable
JWKS is the server's fault, not the token's: it is `jwks.ErrUnavailable` alone,
which `ErrorResponse` answers with 503 — with `jwks.Remote`, so is a kid the
held set lacks while the endpoint is failing, and a caller that gave up
waiting with no set to serve. The DPoP refusals and
`ErrAuthSchemeMismatch` match `ErrProofOfPossession` — the token may be
genuine, the proof is what failed — and every refusal of the credentials,
token or proof, matches `ErrUnauthenticated`, as does `ErrNoCredentials`
from `ParseAuthorization`.

A Biscuit goes through the same gates, around them. Its authority block seals
an ordinary signed token, which passes the gates from the header to the tenant;
that token names the key rooting the Biscuit's chain, which is checked next,
and then each attenuation block is folded in. The time window, the audience,
the scheme and the proof of possession — over the Biscuit as presented — apply
once, to the narrowed capability that comes out; both revocation lists are
asked after them, and only then is a copy with counters refused at a verifier
that does not meter them; the proof's id is recorded last, as for any token.

```mermaid
flowchart TB
    tok(["Biscuit, audience"])
    on{"VerifierConfig.Biscuit set"}
    auth{"container decodes · ≤ MaxBiscuitBlocks blocks<br/>authority block: exactly one<br/>limes_capability fact, no rules, no checks"}
    sealed{"the sealed token: header · key · signature<br/>claims · issuer · tenant — and it names a root, limes_bsk"}
    chain{"signature chain verifies<br/>against that root"}
    subgraph fold ["each attenuation block, in order"]
        direction TB
        vocab{"only limes_* facts<br/>no rules · no checks · no rebinding"}
        narrow{"Narrows(cur, next)<br/>ops · resources · planes · expiry · binding"}
        lim{"sets limes_max_requests /<br/>limes_max_budget_nanos?"}
        fit{"within the capability's limit<br/>and every enclosing copy's"}
        copies["VerifiedCapability.Copies gains a CopyCeiling<br/>{RevocationID, Requests, Budget}, innermost first<br/>an axis the block left alone stays nil"]
        vocab -- yes --> narrow -- yes --> lim
        lim -- yes --> fit -- yes --> copies
    end
    cval{"folded caveats<br/>Caveats.Validate"}
    post["time window · audience · scheme<br/>DPoP over the Biscuit as presented"]
    rev{"IsRevoked(capability)"}
    brev{"IsBiscuitRevoked(revocation ids)<br/>any block of this token"}
    meter{"Copies set, and<br/>no MeterCopies?"}
    rec["key-bound: the proof's id recorded<br/>in the replay cache"]
    ok(["VerifiedCapability with Copies"])

    bad["ErrInvalidToken"]
    att["ErrBiscuitAttenuation"]
    unmetered["ErrCopyCountersNotMetered"]
    revoked["ErrRevoked"]

    tok --> on -- yes --> auth -- yes --> sealed -- yes --> chain -- yes --> vocab
    lim -- no --> cval
    copies --> cval
    copies -. "next block" .-> vocab
    lim -. "next block" .-> vocab
    cval -- yes --> post
    post --> rev
    auth -- no --> bad
    sealed -- no --> bad
    rev -- no --> brev -- no --> meter -- no --> rec --> ok
    on -- no --> bad
    chain -- no --> bad
    vocab -- no --> att
    narrow -- widens --> att
    fit -- no --> att
    cval -- no --> att
    meter -- yes --> unmetered
    rev -- yes --> revoked
    brev -- yes --> revoked

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class tok,ok client
    class on,auth,sealed,chain,vocab,narrow,lim,meter,fit,copies,cval,post role
    class rev,brev,rec store
    class bad,att,unmetered,revoked external
```

The sealed token presented on its own is refused, so it cannot be lifted out
to shed an attenuation. `ErrBiscuitAttenuation` matches `ErrInvalidToken`:
the token is not one this verifier accepts. `ErrCopyCountersNotMetered` does
not: the same token passes a verifier with `MeterCopies`, so the refusal is
the server's configuration, answered as an internal error.

## Proof of possession

A key-bound capability (`ConfirmationJKT` set) needs a DPoP proof (RFC 9449)
for every request; a verifier with no `dpop.Verifier` cannot check one and
answers `ErrProofUnsupported`. The `dpop` package checks the proof in this order and
returns its own sentinel; the verifier wraps each refusal in
`ErrProofOfPossession`, and passes a replay cache or nonce store that cannot
answer through as an outage:

```mermaid
flowchart TB
    p(["dpop.Request<br/>Proof{JWT · Method · URL} · Token · JKT · Holder"])
    has{"proof present<br/>(after Holder set, else an error)"}
    shape{"≤ MaxProofBytes · three segments · typ dpop+jwt<br/>a jwk with no private member"}
    jkt{"the jwk's RFC 7638 thumbprint<br/>= ConfirmationJKT"}
    psig{"signature by that key<br/>Ed25519 (or EdDSA) · ES256"}
    cl{"claims decode · jti set,<br/>≤ MaxJTIBytes"}
    htm{"htm = Method, exactly<br/>(a method is case-sensitive)"}
    htu{"htu = URL after RFC 3986 normalisation<br/>or, under MatchPathOnly, its path a suffix"}
    ath{"ath = hash of the token as presented"}
    iat{"|now − iat| ≤ Window<br/>(dpop.DefaultWindow, 1 minute)<br/>iat may be fractional"}
    nonce{"Nonces set → Nonces.Valid(nonce)"}
    jti{"ReplayCache.Seen(ReplayEntry{JKT, JTI, Holder,<br/>ExpiresAt: iat + Window + ReplayClockSkew})"}
    ok(["proof accepted"])

    req["dpop.ErrRequired"]
    inv["dpop.ErrInvalid"]
    nn["dpop.ErrNonceRequired<br/>answer with a fresh DPoP-Nonce"]
    rp["dpop.ErrReplayed"]
    down["the store's error, wrapped<br/>(an outage, not a refusal)"]

    p --> has -- yes --> shape -- yes --> jkt -- yes --> psig -- yes --> cl -- yes --> htm -- yes --> htu -- yes --> ath -- yes --> iat -- yes --> nonce -- "yes, or no Nonces" --> jti -- "new" --> ok
    has -- no --> req
    shape -- no --> inv
    jkt -- no --> inv
    psig -- no --> inv
    cl -- no --> inv
    htm -- no --> inv
    htu -- no --> inv
    ath -- no --> inv
    iat -- no --> inv
    nonce -- no --> nn
    nonce -- "cannot answer" --> down
    jti -- "seen, or no room for it" --> rp
    jti -- "cannot answer" --> down

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class p,ok client
    class has,shape,jkt,psig,cl,htm,htu,ath,iat role
    class nonce,jti store
    class req,inv,nn,rp,down external
```

`dpop.HMACNonces` makes a nonce of its issuance time and an HMAC of it, so replicas
holding the same keys accept each other's nonces with no shared state, given
clocks within `dpop.HMACNonceClockSkew`. `dpop.MemoryReplayCache` gives each holder — the
capability the token carries — a share of its own, so one past its share has
only its own proofs refused, however many keys its Biscuit copies are bound
to; a full cache refuses rather than forgets. `redisreplay.Cache` keeps the
same per-holder share in Redis, so replicas share one cache; it has no bound
on the whole. A proof past
`MaxProofBytes` is refused unread, and a `jti` past `dpop.MaxJTIBytes` is
refused as `dpop.ErrInvalid`, never stored.

## Answering a refusal

`Verifier.ErrorResponse` maps a refusal to how a protected resource
answers it, from the same table `ReasonOf` reads, and `WriteErrorResponse`
writes it as `application/problem+json` with `Cache-Control: no-store`. The
error's own text never reaches the client.

| Refusal | Status | `WWW-Authenticate` |
|---|---|---|
| `jwks.ErrUnavailable` | 503 | none |
| `ErrNoCredentials` | 401 | a challenge with no error code, only the realm (RFC 6750 §3.1): no header, or another scheme's |
| `ErrMalformedAuthorization` | 400 | `error="invalid_request"` under the client's scheme (Bearer when unknown) |
| `dpop.ErrNonceRequired` | 401 | `DPoP error="use_dpop_nonce"`, and a fresh `DPoP-Nonce` |
| `dpop.ErrRequired` · `dpop.ErrInvalid` · `dpop.ErrReplayed`, and any other `ErrProofOfPossession` | 401 | `DPoP error="invalid_dpop_proof", algs="Ed25519 ES256"` |
| `ErrAuthSchemeMismatch` · `ErrInvalidToken` · `ErrExpired` · `ErrNotYetValid` · `ErrRevoked` · `ErrAudienceMismatch`, and any other `ErrUnauthenticated` | 401 | `error="invalid_token"` under the client's scheme (Bearer when unknown) |
| any `ErrCaveatViolation` | 403 | `error="insufficient_scope"` |
| `ErrBudgetExceeded` · `ErrTenantBudgetExceeded` · `ErrRequestLimitExceeded` | 403 | none |
| anything else — a store or key resolver outage, `ErrProofUnsupported`, `ErrEffectConflict`, `ErrCopyCountersNotMetered` | 500 | none |

A verifier with DPoP configured adds a `DPoP algs="…"` challenge with no
error code beside a `Bearer` one, and every `DPoP` challenge it answers
carries `algs`. A challenge with an error code also carries
`error_description`, the refusal's fixed title — never the error's own. Every
challenge ends with `realm="…"`: `VerifierConfig.Realm`, or `DefaultRealm`.

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

`Issuer.Delegate` refuses a parent read from a Biscuit copy, one another
issuer minted, and one whose ops lack `OpShare` (`ErrOpNotAllowed`). It fills
what the request leaves out from the parent — tenant, audience, key binding,
and the caveats under `InheritCaveats` — refuses a parent that is expired or
revoked, clamps the child's window inside the parent's, and requires
`Narrows(parent, child)` to pass before anything is signed or persisted. A
Biscuit's holder attenuates the copy instead: a server-side child would be an
ordinary token of the capability, which revoking the copy would not reach and
the copy's limits would not bound.

```mermaid
flowchart TB
    req(["DelegateRequest<br/>Parent · Subject · Audience · Caveats or InheritCaveats<br/>TTL · NotBefore · ConfirmationJKT"])
    pid{"Parent.ID set"}
    copyp{"Parent not read from a Biscuit<br/>(FromBiscuit false, no Copies)"}
    own{"Parent.Issuer = this issuer"}
    share{"OpShare ∈ parent's Ops"}
    fill["unset tenant and audience taken from the parent<br/>InheritCaveats: the parent's caveats"]
    valid{"Subject.Validate · audience valid<br/>Caveats.Validate · thumbprint valid"}
    alive{"parent not expired<br/>Store.IsRevoked(parent) = false"}
    clamp["ExpiresAt = min(now + TTL, parent.ExpiresAt)<br/>unset binding taken from the parent"]
    start["starts no earlier than the parent:<br/>NotBefore raised to the parent's start<br/>(its NotBefore, else IssuedAt)"]

    subgraph nar ["Narrows(parent, child)"]
        direction TB
        n0["Audience ⊆ parent's · ExpiresAt ≤ parent's<br/>starts no earlier than the parent<br/>same tenant · a bound parent's child stays bound"]
        n1["Ops ⊆ parent.Ops"]
        n2["every resource reachable by the parent<br/>exact-URI parent admits no child prefix"]
        n3["Requests within the parent's: any under Unlimited,<br/>a bound no higher under a bound"]
        n4["Budget.UnitCode = parent's<br/>(none taken as the parent's)"]
        n5["Budget within the parent's, the same way"]
        n6["AllowTaintedRead only if the parent has it<br/>IdempotencyKeyRequired never relaxed"]
        n7["SourceIPCIDR inside a parent network"]
        n0 --> n1 --> n2 --> n3 --> n4 --> n5 --> n6 --> n7
    end

    sign["Issuer.sign: Signer.sign(ctx, child)<br/>token within MaxTokenBytes"]
    persist{"Store.Insert(InsertRequest{child,<br/>IssuedBy: parent.Subject})<br/>no more than MaxDelegationDepth ancestors"}
    depth{"Store.IsRevoked(child) returns no error<br/>(a backstop for a store that does not refuse;<br/>the child is already on record)"}
    ok(["IssueResult{Capability, Token}"])

    wide["ErrDelegationTooWide"]
    unit["ErrUnitCodeMismatch"]
    noshare["ErrOpNotAllowed"]
    dead["ErrExpired · ErrRevoked<br/>or the store's error"]
    inval["ErrInvalidRequest · ErrInvalidCaveats"]
    stored["the store's error<br/>ErrDelegationTooDeep, …"]

    req --> pid -- yes --> copyp -- yes --> own -- yes --> share -- yes --> fill --> valid -- yes --> alive -- yes --> clamp --> start --> n0
    n7 --> sign --> persist -- ok --> depth -- "no error" --> ok
    pid -- no --> inval
    copyp -- no --> inval
    own -- no --> inval
    share -- no --> noshare
    valid -- no --> inval
    clamp -- "negative TTL" --> inval
    start -- "window empty" --> inval
    alive -- no --> dead
    n4 -- differs --> unit
    nar -- widens --> wide
    sign -- "too long" --> inval
    persist -- refused --> stored
    depth -- "error, e.g. too deep" --> stored

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class req,ok client
    class pid,fill,valid,copyp,own,share,clamp,start,sign,n0,n1,n2,n3,n4,n5,n6,n7 role
    class alive,persist,depth store
    class wide,unit,noshare,dead,inval,stored external
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
- **No delegation from a copy.** `Issuer.Delegate` refuses any `Parent` read
  from a Biscuit (`VerifiedCapability.FromBiscuit`, with limits or without)
  as `ErrInvalidRequest`; see the delegation section above.

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
actual cost along the same path, releasing the hold and deleting the
reservation in the same commit. Every ceiling counts open holds as well as
spend, so two callers cannot both hold the last of a budget. A settle takes
its hold off before it measures, so one at or below the hold fits every
ceiling that holds it — even one lowered or crossed since — and only an
amount past the hold, or an ancestor that does not hold it, is checked.

```mermaid
flowchart TB
    req(["ChargeRequest<br/>Subject (id · tenant · ceilings · unit · copies) · Amount<br/>ExternalRef · Overrun"])
    sreq(["SettleRequest<br/>Subject · ReservationID · Amount · Overrun"])
    sval{"subject set, a unit named · Nanos.Validate · Overrun.Validate<br/>ReservationID not zero"}
    hold{"reservation open, unexpired,<br/>the subject's own, under the same copies"}
    val{"Nanos.Validate · Overrun.Validate · ValidateExternalRef<br/>subject set · a unit named"}
    seen{"ExternalRef already charged<br/>to this capability"}
    same{"same Amount · UnitCode · TenantID<br/>· Biscuit copies"}
    lin{"capability on record<br/>lineage read: its ancestors still on record"}

    subgraph stage ["stage — compute, publish nothing"]
        direction TB
        c00{"Amount in the unit the capability, each ancestor and the tenant count in<br/>no counter, copies included, past MaxNanos"}
        c0{"each Biscuit copy, innermost first:<br/>spend + reserved + Amount ≤ its budget"}
        c1{"own spend + reserved + Amount ≤ Subject.Budget<br/>(ceiling from the verified token or stored record)"}
        c2{"each ancestor: spend + reserved + Amount<br/>≤ its Budget<br/>(ceiling from the stored record)"}
        c3{"tenant spend + reserved + Amount<br/>≤ TenantBudget.Ceiling<br/>(Unlimited until one is set)"}
        cb{"onCharged(ctx with the store's tx, Charged{Receipt})<br/>consumer's side effect in the same transaction"}
        c00 -- yes --> c0 -- yes --> c1 -- yes --> c2 -- yes --> c3 -- yes --> cb
    end

    subgraph commit ["commit — all together"]
        direction TB
        w1["capability spend += Amount<br/>settling: its hold leaves reserved"]
        w2["each ancestor on the lineage as read now:<br/>spend += Amount"]
        w0["settling: each ancestor that held it<br/>and still has a row: its hold leaves reserved"]
        w3["tenant spend += Amount · settling: its hold leaves reserved<br/>(the row created, with no ceiling, if missing)"]
        w5["each copy's spend += Amount<br/>settling: its hold leaves reserved"]
        w4["ledger row: ChargeID · TenantID · Amount · UnitCode · At<br/>Ancestors · copy ids · Op · Actor · ExternalRef<br/>ReservationID · Overrun · tenant period"]
        w6["settling: the reservation is deleted"]
    end

    ok(["ChargeReceipt{ChargeID, SpentAmount, Overrun, TenantID, UnitCode}"])
    replay(["ChargeReceipt{Replayed}<br/>the earlier charge; nothing moves"])
    refund["Refund(RefundRequest{ChargeID, Amount or Remaining}) → RefundReceipt<br/>returns spend to every counter the charge took it from;<br/>the tenant's only within the same accounting period"]

    e0["ErrInvalidRequest (ErrInvalidAmount)<br/>ErrUnitCodeMismatch · ErrCounterOverflow"]
    e1["ErrBudgetExceeded"]
    e2["ErrBudgetExceeded: ancestor"]
    e3["ErrTenantBudgetExceeded"]
    e4["the callback's error"]
    e5["ErrReservationNotFound"]
    e6["ErrExternalRefConflict"]
    e7["ErrNotFound · ErrDelegationTooDeep"]

    req --> val -- ok --> seen -- no --> lin
    seen -- yes --> same -- yes --> replay
    same -- no --> e6
    sreq --> sval -- ok --> hold -- yes --> lin
    lin -- yes --> c00
    lin -- no --> e7
    sval -- "invalid" --> e0
    sval -- "zero id" --> e5
    hold -- "settled by this subject,<br/>under the same copies" --> replay
    hold -- "released · expired · another capability's<br/>· other copies" --> e5
    cb -- nil --> w1 --> w2 --> w0 --> w3 --> w5 --> w4 --> w6 --> ok
    ok -. "later" .-> refund
    val -- invalid --> e0
    c0 -- "no, OverrunPolicyReject" --> e1
    c1 -- "no, OverrunPolicyReject" --> e1
    c2 -- "no, OverrunPolicyReject" --> e2
    c3 -- "no, OverrunPolicyReject" --> e3
    cb -- error --> e4
    c00 -- no --> e0

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    classDef optional fill:#F1F5F9,stroke:#64748B,color:#334155,stroke-dasharray:5 4
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class req,sreq,ok,replay client
    class val,sval,seen,same,hold,lin,c00,c0,c1,c2,c3 role
    class cb optional
    class w1,w2,w0,w3,w5,w4,w6,refund store
    class e0,e1,e2,e3,e4,e5,e6,e7 external
```

`Bump` takes the same order for requests: each copy's limit, then the
capability's, then each ancestor's. A reservation keeps its copies' budgets,
so `Settle` checks them without being handed them again; the subject settling
must still carry the same copies.

A settle rejected by a ceiling leaves the reservation in place, to be settled
lower or released with `Release`. Under `OverrunPolicyRecord` no ceiling rejects: a
check that fails marks the charge as an overrun and the stage goes on, since
the cost has already been incurred; the crossed ceiling then refuses whatever
follows under `OverrunPolicyReject`. A mismatched unit or a counter past `MaxNanos`
is refused even then: neither is a ceiling an overrun may cross. A charge
named by `ExternalRef`, or a settle of a reservation already settled, returns
the earlier charge and moves nothing; the same `ExternalRef` with another
amount, unit, tenant or Biscuit copy is `ErrExternalRefConflict`: another copy
charges its own counters, which the earlier receipt never touched.

Holds and housekeeping, in `memstore` and `pgstore` as the contract has them:

- `Reserve` checks the same ceilings — never overrun — and holds the amount on
  the capability, each ancestor, the tenant (creating its row) and each copy;
  `Release` and `ReleaseExpired` take it off again. A hold never settled lapses
  at its `ExpiresAt` — `DefaultReservationTTL`, five minutes, by default, and
  never later than its capability's expiry — but keeps counting until
  `ReleaseExpired` runs, which errs towards refusing spend. A revoked
  capability's hold stays settleable until then.
- `DeleteUsage` releases the capability's holds before it drops its counters
  and its copies', and `PurgeOrphans` releases the holds of every capability
  no longer on record, a batch of `Limit` at a time: nothing could settle or
  release them afterwards. Either takes the dropped ids off the ancestor
  lists of the holds that remain.
- `SetTenantBudget` is guarded by `ExpectedVersion` (0 for a tenant with no
  ceiling yet), refuses a new unit while anything is held in the old one, or
  spent in it unless `ResetSpend` closes that spend, and with `ResetSpend` opens a new accounting period, so a refund of an
  earlier period's charge leaves the new period's spend alone.

A store with transactions runs stage and commit inside one, and hands
`onCharged` a context carrying it, beside the receipt the call will return
(`Charged`); `onCharged` may be `nil`. A store without them hands on the
caller's context; `memstore` still runs `onCharged` before it publishes.

`pgstore` runs each call in one transaction, `onCharged` on that same
`pgx.Tx`, so a rejection or a failing side effect rolls everything back.
`Bump`, `Charge`, `Reserve`, `Settle` and `Release` first refuse a scope that does not
admit the subject's tenant, as `ErrScopeMismatch`, before any row is read. It
locks in one order, so two calls never wait on each other in a cycle: the
reservation being settled or released, then the lineage's records `FOR SHARE`
in id order — a purge locks its batch in the same order — then the tenant's
row, the usage rows by capability id and the copies' rows by revocation id. An
`ExternalRef` is looked up again once the locks are held, since a concurrent
delivery may have committed in between. A call holds one connection, so a
small pool serves as many calls at once as it has connections.

## Revocation reaches the verifiers

Revocation is a write to the `Store`; verifiers see it through
`IsRevoked`, which answers for the whole delegation chain, usually behind a
`RevocationCache`.

```mermaid
flowchart LR
    op(["Revoke(RevokeRequest{CapabilityID, Note, Actor, CascadeChildren})"])
    store[("<b>Store</b><br/>revoked set<br/>ParentID links")]
    cascade["CascadeChildren:<br/>a revocation entry per descendant,<br/>MaxDelegationDepth generations down,<br/>for the audit trail; an earlier entry stands"]
    chain["IsRevoked(id):<br/>id revoked, or any ancestor still on record revoked<br/>past MaxDelegationDepth: ErrDelegationTooDeep<br/>pgstore: one statement, every tenant"]
    cache["<b>RevocationCache</b><br/>answer cached for TTL (default 2s)<br/>one upstream call per id at a time<br/>bounded: the oldest answer makes room<br/>errors never cached · a Clear mid-lookup<br/>discards that lookup's answer"]
    notify{{"revocation signal<br/>pgstore: RevocationWatcher LISTENs on<br/>RevocationChannel; memstore: none"}}
    bop(["RevokeBiscuit(RevokeBiscuitRequest{Copy: Verifier.BiscuitCopy(token)})"])
    blist[("<b>BiscuitRevocationStore</b><br/>revoked block ids")]
    bcache["<b>BiscuitRevocationCache</b><br/>per revocation id, in turn; stops at the first revoked<br/>cached for TTL · one upstream call per id at a time<br/>bounded · dropped by Clear"]
    v1["Verifier"]
    v2["Verifier"]

    op --> store
    store -.-> cascade
    store --> chain --> cache
    cache --> v1
    cache --> v2
    store -. "pgstore: statement trigger on an INSERT<br/>that adds a row to limes.revocations → pg_notify" .-> notify
    blist -. "pgstore: statement trigger on an INSERT<br/>that adds a row to limes.biscuit_revocations → pg_notify" .-> notify
    notify -. "Clear() once listening<br/>and on each notification" .-> cache
    bop --> blist --> bcache
    bcache --> v1
    bcache --> v2
    notify -. "Clear() once listening<br/>and on each notification" .-> bcache

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
a revocation signal, a verifier learns of a revocation within the cache TTL;
`pgstore`'s schema notifies a channel on every revocation, and its
`RevocationWatcher` clears the caches on each notification and after each
reconnect.
Each cache is dropped whole: an answer cached for a descendant is also an
answer about the revoked ancestor. A `Clear` also detaches any lookup in flight, whose answer may
predate the revocation, so it is neither cached nor shared with later checks.
Revoked Biscuit copies travel the same way, on their own list and cache; one
signal clears both.

In `pgstore`, `Revoke` and `RevokeBiscuit` run under the caller's tenant —
a capability it cannot see is `ErrNotFound` — while `IsRevoked` and
`IsBiscuitRevoked` are each one statement calling a `SECURITY DEFINER`
function of the schema, which sees every tenant: the verifier asks before it
knows the token's. A revocation entry is deleted with its record, so once
`Purge` reaps an ancestor the walk no longer reaches it — by then it has
expired, and so has every descendant.

A verifier that runs apart from the issuer resolves keys with
`jwks.Remote`. It asks for `application/jwk-set+json` (then `application/json`), keeps a set as
long as the response's `Cache-Control` says — `max-age` less `Age`, zero for
`no-cache` or `no-store` — within `MinRefreshInterval` and `RefreshInterval`
(`RefreshInterval` when the response says nothing), revalidates with an ETag,
refetches on an unknown kid at most once per `MinRefreshInterval`, and fails
closed with `jwks.ErrUnavailable` when it has never fetched a set, or once the
last good set is older than `MaxStale`. A known kid from a set past its
freshness but within `MaxStale` is served at once, its refresh started behind
the caller; an unknown kid while the endpoint is failing, or a caller that
gives up waiting with no set to serve, is `jwks.ErrUnavailable` too. Keys are
handed out as copies. A document with entries but no usable
Ed25519 key counts as a failed fetch, so the last good set stays in service;
two entries under one kid are both dropped. The issuer serves the set with `jwks.NewHandler`: a strong ETag
answering `If-None-Match` with 304, and `Cache-Control: public, max-age`.

## pgstore: a call under its tenant's scope

Every call of `pgstore` runs in a transaction that `Config.Scope` scopes first;
the default reads the scope a context carries. A call whose request names a
tenant is refused with `ErrScopeMismatch` when that scope does not admit the
tenant, before it reads a row, rather than answer as rows that are not there.
A call that writes is refused so under a read-only scope: before the
transaction under `WithAllTenantsReadOnly`, and once a `Config.Scope` of the
consumer's has run when it set `limes.all_tenants` to `read`. Row-level security then binds every statement: `tenant_visible` for reads,
`tenant_writable` for writes. The table owner, as `Migrate` runs, is not bound.

```mermaid
flowchart TB
    ctx(["ctx: WithTenant(id) · WithAllTenants · WithAllTenantsReadOnly · none"])
    ro{"the call writes,<br/>under WithAllTenantsReadOnly"}
    begin["BEGIN · Config.Scope(ctx, tx)<br/>default ScopeFromContext: set_config, local to the transaction<br/>limes.tenant_id = id · limes.all_tenants = on · = read"]
    roset{"the call writes, under a Config.Scope of the consumer's<br/>that set limes.all_tenants = read (one SELECT)"}
    named{"the request names a tenant<br/>Insert · ListByPrincipal · Bump · Charge · Reserve · Settle · Release<br/>Get/SetTenantBudget · erasers · SumCharges · CountRecords"}
    check{"Config.CheckScope admits it<br/>default: the context's scope is every tenant or that one;<br/>none: CheckSchemaScope asks the database"}
    rls["statements under row-level security<br/>read: tenant_visible — all_tenants on or read, or tenant_id matches<br/>write: tenant_writable — all_tenants on, or tenant_id matches"]
    commit(["COMMIT"])
    idonly["a call naming only an id<br/>(GetRecord · Revoke · Refund · GetUsage · …):<br/>a row out of scope is not found"]
    definer["IsRevoked · IsBiscuitRevoked<br/>one statement, no transaction:<br/>a SECURITY DEFINER function sees every tenant"]
    every{"the request reads every tenant<br/>CountRecords AllTenants"}
    evcheck{"the scope admits every tenant<br/>default: WithAllTenants or WithAllTenantsReadOnly;<br/>otherwise the database is asked"}
    mismatch["ErrScopeMismatch"]

    ctx --> ro -- no --> begin --> roset -- no --> named -- yes --> check -- yes --> rls --> commit
    roset -- yes --> mismatch
    named -- no --> every -- yes --> evcheck -- yes --> rls
    every -- no --> rls
    evcheck -- no --> mismatch
    rls -.-> idonly
    ro -- yes --> mismatch
    ctx -. "a verifier's lookups" .-> definer
    check -- no --> mismatch

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef optional fill:#F1F5F9,stroke:#64748B,color:#334155,stroke-dasharray:5 4
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class ctx,commit client
    class ro,begin,roset,named,check,every,evcheck role
    class idonly,definer optional
    class rls store
    class mismatch external
```

A consumer's own `Config.Scope` gets no check unless it also sets
`Config.CheckScope`; `CheckSchemaScope` serves one that sets the schema's own
settings.

## Erasing a tenant

`Store.EraseTenant` removes a tenant from `pgstore` in one transaction scoped
to it, and runs the consumer's `onErased` last in the same transaction, so the
consumer's rows of the tenant go with the store's or not at all. It locks in
the order the meter does, so it never waits on a meter call in a cycle.

```mermaid
flowchart TB
    req(["EraseTenantRequest{TenantID, Note, Actor} · onErased"])
    val{"Validate"}
    tx["inTenantTx: scoped, CheckScope admits the tenant"]
    holds["lock the tenant's holds, in id order"]
    recs["lock its records, in id order"]
    revoke["revoke every record not revoked yet<br/>(when any is newly revoked, the notify trigger fires: watchers Clear)"]
    remove["remove the records Purge would:<br/>expired before now − MinPurgeRetention"]
    kept["count the records kept: revoked, until Purge takes them"]
    usage["lock aggregate · counters · copies' counters<br/>delete holds · tenant budget · usage · copy usage · charges"]
    cb{"onErased(ctx, Erased{Tx, Result})"}
    ok(["EraseTenantResult{Revoked, Removed, Kept}"])
    back["rolled back: nothing erased"]

    req --> val -- ok --> tx --> holds --> recs --> revoke --> remove --> kept --> usage --> cb -- nil --> ok
    val -- invalid --> inval["ErrInvalidRequest"]
    tx -- "scope does not admit it" --> mismatch["ErrScopeMismatch"]
    cb -- error --> back

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef optional fill:#F1F5F9,stroke:#64748B,color:#334155,stroke-dasharray:5 4
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class req,ok client
    class val,tx role
    class cb optional
    class holds,recs,revoke,remove,kept,usage store
    class inval,mismatch,back external
```

A record kept stays revoked: an id not on record is not revoked, so removing
a live record early would let its token through. The separate
`EraseTenantRecords` and `UsageStore.EraseTenantUsage` do each half alone, and
`memstore` implements both.
