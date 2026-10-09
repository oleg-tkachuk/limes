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
storage behind them. `memstore` is the in-memory reference implementation, and
Paladin's PostgreSQL adapters are another. The two Biscuit contracts are needed
only by a consumer that accepts the Biscuit form, and `ReplayCache` only by
one that binds capabilities to keys. `storetest` and `metertest` check a
store against its contract from the store's own tests.

```mermaid
flowchart LR
    caller(["Consumer service<br/>orchestrator · tool · API"])

    subgraph mod ["limes (this module)"]
        direction TB
        issuer["<b>Issuer</b><br/>Issue · Delegate · Biscuit"]
        signer["<b>Signer</b><br/>NewEd25519Signer · JWT EdDSA"]
        narrows["<b>Narrows</b><br/>child ⊆ parent"]
        verifier["<b>StandardVerifier</b><br/>Verify(token, audience) · BiscuitCopy"]
        caveats["<b>Caveats</b><br/>Check · CheckSource · MatchResource"]
        cache["CachedRevocationChecker<br/>TTL · single-flight"]
        bcache["CachedBiscuitRevocationChecker<br/>TTL · per token"]
        attenuate["<b>Attenuate</b><br/>offline · no key"]
        metering["MeteringStore[TX]<br/>WithMetering · OTel counters"]
        static["StaticKeyResolver<br/>SetKey · RemoveKey"]
        remote["RemoteJWKSResolver<br/>ETag · MaxStale"]
        dpop["DPoPVerifier<br/>Check(proof) · NewDPoPProof"]
        suites["storetest · metertest<br/>conformance suites"]
    end

    subgraph contracts ["contracts the consumer implements"]
        direction TB
        store["<b>Store</b><br/>Insert · Get · GetRecord · IsRevoked · Revoke<br/>PurgeExpired · ListByPrincipal"]
        usage["<b>UsageStore[TX]</b><br/>Meter[TX]: Bump · Charge · Reserve · Settle · Refund · Release<br/>GetUsage · GetCharge · ChargeByRef · GetReservation · ListReservations<br/>TenantBudgets · UsageHousekeeping"]
        keys["<b>KeyResolver</b><br/>PublicKey(kid)"]
        brev["<b>BiscuitRevocationStore</b><br/>IsBiscuitRevoked · RevokeBiscuit<br/>GetBiscuitRevocation"]
        cusage["<b>CopyUsageReader</b><br/>CopyUsage(revocation ids)"]
        replay["<b>ReplayCache</b><br/>Seen(jti) · MemoryReplayCache ships"]
    end

    impl[("memstore · PostgreSQL · …")]
    jwks{{"Issuer JWKS endpoint<br/>MarshalJWKS"}}
    otel{{"OTel MeterProvider"}}

    caller --> issuer
    caller --> verifier
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
    caller -. "key-bound tokens" .-> dpop --> replay
    suites -. "check" .-> store
    suites -. "check" .-> usage
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
    class cache,metering,static,remote,bcache,attenuate,dpop,suites optional
    class store,usage,keys,impl,brev,cusage,replay store
    class jwks,otel external
```

Every read names what it reads — `Store.Get`, `Meter.GetUsage`,
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
    I->>I: Signer.Sign(cap)
    I-->>O: token

    O->>I: Delegate(DelegateRequest{Parent, Caveats})
    I->>St: Store.IsRevoked(parent)
    I->>I: Narrows(parent, child)
    I->>St: Store.Insert(child, parent.Subject)
    I-->>W: child token

    W->>S: request + child token
    S->>S: Verify(token, audience)
    S->>St: IsRevoked(child) — via CachedRevocationChecker
    S->>S: Caveats.CheckSource(addr) · Caveats.Check(op, resource)
    S->>St: Bump · Charge, or Reserve then Settle
    St-->>S: ChargeReceipt{ChargeID, Spent}
    S-->>W: response

    Op->>St: Store.Revoke(RevokeRequest{ID: parent, CascadeChildren})
    Note over S,St: the next IsRevoked for the child answers true<br/>once the cache entry expires, or at once after Clear / Invalidate
    W->>S: request + child token
    S-->>W: ErrRevoked
```

## Verification gates

`StandardVerifier.Verify` reads nothing from the claims until the signature
over them has verified, and asks the revocation store last so that cheap
rejections never reach it. Every exit is a sentinel the caller maps to its own
transport status.

```mermaid
flowchart TB
    tok(["token, audience"])
    size{"len ≤ MaxTokenBytes"}
    hdr{"header parses<br/>typ = TokenType · kid set"}
    key{"KeyResolver.PublicKey(kid)"}
    sig{"Ed25519 signature"}
    claims{"claims parse"}
    iss{"iss ∈ TrustedIssuers<br/>KeyIssuers[kid] = iss"}
    tenant{"Subject.TenantID set"}
    nbf{"now + Leeway ≥ NotBefore"}
    exp{"now − Leeway ≤ ExpiresAt"}
    aud{"audience ∈ Audience"}
    rev{"RevocationLookup.IsRevoked<br/>this capability or an ancestor"}
    ok(["Capability"])

    bad["ErrInvalidSignature"]
    early["ErrNotYetValid"]
    expired["ErrExpired"]
    mism["ErrAudienceMismatch"]
    revoked["ErrRevoked"]

    tok --> size -- yes --> hdr -- yes --> key -- found --> sig -- valid --> claims -- yes --> iss -- yes --> tenant -- yes --> nbf
    nbf -- yes --> exp -- yes --> aud -- yes --> rev -- no --> ok
    size -- no --> bad
    hdr -- no --> bad
    key -- "unknown kid" --> bad
    sig -- invalid --> bad
    claims -- no --> bad
    iss -- no --> bad
    tenant -- no --> bad
    nbf -- no --> early
    exp -- no --> expired
    aud -- no --> mism
    rev -- yes --> revoked

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class tok,ok client
    class size,hdr,sig,claims,iss,tenant,nbf,exp,aud role
    class key,rev store
    class bad,early,expired,mism,revoked external
```

An unknown kid, an untrusted issuer, a kid bound to another issuer and a
missing tenant all surface as `ErrInvalidSignature`, so security-sensitive
callers cannot branch on which one it was.

A Biscuit goes through the same gates, around them. Its authority block seals
an ordinary signed token, which is checked as above; that token names the key
rooting the Biscuit's chain, which is checked next, and then each attenuation
block is folded in. The capability that comes out is narrower than the sealed
one and carries the limits its blocks set; both revocation lists are asked
last.

```mermaid
flowchart TB
    tok(["Biscuit, audience"])
    on{"AcceptBiscuit"}
    sealed["the sealed token through the gates above<br/>it must carry BiscuitRoot"]
    chain{"signature chain verifies<br/>against BiscuitRoot"}
    subgraph fold ["each attenuation block, in order"]
        direction TB
        vocab{"only limes_* facts<br/>no rules · no checks"}
        narrow{"Narrows(cur, next)<br/>ops · resources · planes · expiry · binding"}
        lim{"sets max_requests / max_budget_nanos?"}
        meter{"MeterCopies"}
        fit{"within the capability's limit<br/>and every enclosing copy's"}
        copies["Capability.Copies gains<br/>{revocation id, limits}, innermost first"]
        vocab -- yes --> narrow -- yes --> lim
        lim -- yes --> meter -- yes --> fit -- yes --> copies
    end
    rev{"IsRevoked(capability)"}
    brev{"IsBiscuitRevoked(revocation ids)<br/>any block of this token"}
    ok(["Capability with Copies"])

    bad["ErrInvalidSignature"]
    att["ErrBiscuitAttenuation"]
    unmetered["ErrCopyCountersNotMetered"]
    revoked["ErrRevoked"]

    tok --> on -- yes --> sealed --> chain -- yes --> vocab
    lim -- no --> rev
    copies --> rev
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
    class on,sealed,chain,vocab,narrow,lim,meter,fit,copies role
    class rev,brev store
    class bad,att,unmetered,revoked external
```

The sealed token presented on its own is refused, so it cannot be lifted out
to shed an attenuation. `ErrBiscuitAttenuation` and
`ErrCopyCountersNotMetered` both match `ErrInvalidSignature`.

A key-bound capability (`ConfirmationJKT` set) passes one more gate after
`Verify`: `DPoPVerifier.Check` wants a proof signed by that key over the
request's method, URL and token, issued within `DefaultDPoPWindow` and not
seen before by the `ReplayCache`. Every refusal matches `ErrInvalidSignature`.

Verification proves the token is genuine. What the bearer may do with it is a
second, separate step, `Caveats.Check`, evaluated per operation:

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
    eff -- "built-in Op declared otherwise" --> e0

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

`Issuer.Delegate` fills what the request leaves out from the parent — tenant,
audience, key binding, and the caveats under `InheritCaveats` — refuses a
parent that is expired or revoked, clamps the child's expiry to the parent's,
and then requires `Narrows(parent, child)` to pass before anything is
persisted or signed.

```mermaid
flowchart TB
    req(["DelegateRequest<br/>Parent · Subject · Audience · Caveats or InheritCaveats<br/>TTL · NotBefore · ConfirmationJKT"])
    fill["unset tenant, audience, binding<br/>taken from the parent"]
    valid{"Parent.ID set · audience valid<br/>Caveats.Validate · thumbprint valid"}
    copyp{"Parent.Copies empty<br/>(not a copy with limits of its own)"}
    alive{"parent not expired<br/>Store.IsRevoked(parent) = false"}
    clamp["ExpiresAt = min(now + TTL, parent.ExpiresAt)"]

    subgraph nar ["Narrows(parent, child)"]
        direction TB
        n0["Audience ⊆ parent's · ExpiresAt ≤ parent's<br/>same tenant · a bound parent's child stays bound"]
        n1["Ops ⊆ parent.Ops"]
        n2["every resource reachable by the parent<br/>exact-URI parent admits no child prefix"]
        n3["MaxRequests ≤ parent's, never unlimited under a bounded parent"]
        n4["UnitCode = parent's"]
        n5["MaxBudgetAmount ≤ parent's, never unlimited under a bounded parent"]
        n6["AllowTaintedRead only if the parent has it<br/>IdempotencyKeyRequired never relaxed"]
        n7["SourceIPCIDR inside a parent network"]
        n0 --> n1 --> n2 --> n3 --> n4 --> n5 --> n6 --> n7
    end

    persist["Store.Insert(child, issuedBy = parent.Subject)<br/>Signer.Sign(child)"]
    ok(["child, childToken"])

    wide["ErrDelegationTooWide"]
    unit["ErrUnitCodeMismatch"]
    dead["ErrExpired · ErrRevoked"]
    inval["ErrInvalidRequest · ErrInvalidCaveats"]

    req --> fill --> valid -- yes --> copyp -- yes --> alive -- yes --> clamp --> n0
    n7 --> persist --> ok
    valid -- no --> inval
    alive -- no --> dead
    n4 -- differs --> unit
    nar -- widens --> wide
    copyp -- no --> wide

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class req,ok client
    class fill,valid,copyp,clamp,n0,n1,n2,n3,n4,n5,n6,n7 role
    class alive,persist store
    class wide,unit,dead,inval external
```

Narrowing bounds each child on its own. The tree as a whole is bounded at use:
every charge also counts against each ancestor, read from the ancestor's
stored record. A parent holding 25.00 that delegates 20.00 to each of two
workers can still spend 25.00 in total, not 40.00:

```mermaid
flowchart TB
    p["<b>orchestrator</b><br/>MaxBudgetAmount 25.00<br/>spent 25.00 = 15.00 + 10.00"]
    a["<b>worker A</b><br/>MaxBudgetAmount 20.00<br/>spent 15.00"]
    b["<b>worker B</b><br/>MaxBudgetAmount 20.00<br/>spent 10.00"]
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
    cap(["capability<br/>MaxRequests 100 · budget 10.00"])
    root["<b>root Biscuit</b><br/>authority block: id A"]
    w1["<b>worker 1</b><br/>ids A · B<br/>block B: max_requests 30"]
    w2["<b>worker 2</b><br/>ids A · C<br/>block C: max_budget 2.00"]
    sub["<b>sub-agent of worker 1</b><br/>ids A · B · D<br/>block D: max_requests 5"]
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

- **Revocation.** `BiscuitCopy` checks a copy as `Verify` does and names its
  capability and last block's id; `BiscuitRevocationStore.RevokeBiscuit` lists
  it. Revoking the capability still stops every copy and the JWT.
- **Limits.** A block may set `max_requests` and `max_budget_nanos`, within
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
    req(["ChargeRequest<br/>CapabilityID · TenantID · Amount · MaxBudget · UnitCode · Copies<br/>ExternalRef · Overrun"])
    sreq(["SettleRequest<br/>ReservationID · Amount · MaxBudget · Overrun"])
    hold{"reservation open"}
    val{"ValidateAmount · Overrun.Validate<br/>ValidateExternalRef · NormaliseUnitCode"}
    seen{"ExternalRef already charged<br/>to this capability"}

    subgraph stage ["stage — compute, publish nothing"]
        direction TB
        c0{"each Biscuit copy, innermost first:<br/>spend + reserved + Amount ≤ its budget"}
        c1{"own spend + reserved + Amount ≤ MaxBudget<br/>(ceiling from the verified token)"}
        c2{"each ancestor: spend + reserved + Amount<br/>≤ its MaxBudgetAmount<br/>(ceiling from the stored record)"}
        c3{"tenant spend + reserved + Amount<br/>≤ TenantBudget.MaxBudgetAmount"}
        cb{"onCharged(ctx, tx)<br/>consumer's side effect on its own TX"}
        c0 -- yes --> c1 -- yes --> c2 -- yes --> c3 -- yes --> cb
    end

    subgraph commit ["commit — all together"]
        direction TB
        w5["each copy's spend += Amount"]
        w1["capability spend += Amount"]
        w2["each ancestor spend += Amount"]
        w3["tenant spend += Amount"]
        w0["settling: the hold leaves reserved everywhere"]
        w4["ledger row: ChargeID · Ancestors · copy ids · Op · Actor<br/>ExternalRef · ReservationID · Overrun"]
    end

    ok(["ChargeReceipt{ChargeID, Spent, Overrun}"])
    replay(["ChargeReceipt{Replayed}<br/>the earlier charge; nothing moves"])
    refund["Refund(ChargeID, Amount)<br/>returns spend to every counter the charge took it from"]

    e0["ErrInvalidAmount · unit error"]
    e1["ErrBudgetExceeded"]
    e2["ErrBudgetExceeded: ancestor"]
    e3["ErrTenantBudgetExceeded"]
    e4["the callback's error"]
    e5["ErrReservationNotFound"]

    req --> val -- ok --> seen -- no --> c0
    seen -- yes --> replay
    sreq --> hold -- "yes: its own hold<br/>is not counted" --> c0
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

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    classDef optional fill:#F1F5F9,stroke:#64748B,color:#334155,stroke-dasharray:5 4
    classDef external fill:#FCE7F3,stroke:#DB2777,color:#831843
    class req,sreq,ok,replay client
    class val,seen,hold,c0,c1,c2,c3 role
    class cb optional
    class w5,w1,w2,w3,w0,w4,refund store
    class e0,e1,e2,e3,e4,e5 external
```

`Bump` takes the same order for requests: each copy's limit, then the
capability's, then each ancestor's. A reservation keeps its copies' budgets,
so `Settle` checks them without being handed the token again.

A settle rejected by a ceiling leaves the reservation in place, to be settled
lower or released with `Release`. Under `OverrunRecord` no ceiling rejects: a
check that fails marks the charge as an overrun and the stage goes on, since
the cost has already been incurred; the crossed ceiling then refuses whatever
follows under `OverrunReject`. A charge named by `ExternalRef`, or a settle of
a reservation already settled, returns the earlier charge and moves nothing. A hold never settled lapses at its
`ExpiresAt`, but keeps counting until `ReleaseExpired` runs, which errs towards
refusing spend rather than allowing too much.

A store with transactions runs stage and commit inside one, and passes its own
handle as `tx`; a store without them is instantiated as
`UsageStore[struct{}]` and the caller passes `nil` for `onCharged`.

## Revocation reaches the verifiers

Revocation is a write to the `Store`; verifiers see it through
`IsRevoked`, which answers for the whole delegation chain, usually behind a
`CachedRevocationChecker`.

```mermaid
flowchart LR
    op(["Revoke(RevokeRequest{ID, Reason, Actor, CascadeChildren})"])
    store[("<b>Store</b><br/>revoked set<br/>ParentID links")]
    cascade["CascadeChildren:<br/>a revocation entry per descendant,<br/>for the audit trail"]
    chain["IsRevoked(id):<br/>id or any ancestor revoked"]
    cache["<b>CachedRevocationChecker</b><br/>answer cached for TTL<br/>one upstream call per id at a time<br/>errors never cached · a Clear mid-lookup<br/>discards that lookup's answer"]
    notify{{"consumer's revocation signal<br/>e.g. a Postgres channel"}}
    bop(["RevokeBiscuit(RevokeBiscuitRequest{CapabilityID, RevocationID})"])
    blist[("<b>BiscuitRevocationStore</b><br/>revoked block ids")]
    bcache["<b>CachedBiscuitRevocationChecker</b><br/>answer per token, cached for TTL"]
    v1["StandardVerifier"]
    v2["StandardVerifier"]

    op --> store
    store -.-> cascade
    store --> chain --> cache
    cache --> v1
    cache --> v2
    notify -. "Clear() · Invalidate(id)" .-> cache
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
A `Clear` or `Invalidate` also detaches any lookup in flight, whose answer may
predate the revocation, so it is neither cached nor shared with later checks.
Revoked Biscuit copies travel the same way, on their own list and cache; one
signal clears both.

A verifier that runs apart from the issuer resolves keys with
`RemoteJWKSResolver`: it serves the cached key set for `RefreshInterval`,
revalidates with an ETag, refetches on an unknown kid at most once per
`MinRefreshInterval`, and fails closed with `ErrJWKSUnavailable` once the last
good set is older than `MaxStale`.
