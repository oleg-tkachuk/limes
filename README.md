# limes

[![ci](https://github.com/oleg-tkachuk/limes/actions/workflows/ci.yaml/badge.svg?branch=main)](https://github.com/oleg-tkachuk/limes/actions/workflows/ci.yaml)
[![release](https://img.shields.io/github/v/release/oleg-tkachuk/limes?sort=semver)](https://github.com/oleg-tkachuk/limes/releases/latest)
[![go](https://img.shields.io/github/go-mod/go-version/oleg-tkachuk/limes)](go.mod)
[![license](https://img.shields.io/github/license/oleg-tkachuk/limes)](LICENSE)

Budgeted, delegable, individually revocable capabilities for agentic workloads,
as a Go library.

A capability is a short-lived signed token that carries its own authority:
which operations, on which resources, for how much money and how many
requests, until when, and on whose behalf. An agent can hand a sub-agent a
strictly narrower copy — through the issuer, or offline as a Biscuit block
with no call at all — every charge counts
against the whole delegation chain, and any capability can be revoked
mid-flight along with everything delegated from it.

The module ships the primitive and its contracts; you supply the storage. Its
resolved dependency graph holds no database driver and no storage SDK, and
`memstore`, an in-memory implementation, is enough to start.

*Limes* was the fortified frontier of the Roman Empire — a line that marks how
far authority reaches ([Wiktionary](https://en.wiktionary.org/wiki/Limes),
[Wikipedia](https://en.wikipedia.org/wiki/Limes_(Roman_Empire))).

```mermaid
flowchart LR
    op(["Operator"])
    orch(["Orchestrator agent"])
    worker(["Sub-agent"])
    svc["<b>Your service</b><br/>Authorize · Bump · Charge"]
    store[("<b>Your storage</b><br/>Store · UsageStore · KeyResolver")]

    op -- "Issue" --> orch
    orch -- "Delegate / Attenuate<br/>narrower only" --> worker
    orch -- "call + token" --> svc
    worker -- "call + token" --> svc
    svc --> store
    op -. "Revoke" .-> store

    classDef client fill:#E0F2FE,stroke:#0284C7,color:#0C4A6E
    classDef role fill:#DCFCE7,stroke:#16A34A,color:#14532D
    classDef store fill:#FEF3C7,stroke:#D97706,color:#78350F
    class op,orch,worker client
    class svc role
    class store store
```

## Install

```bash
go get github.com/oleg-tkachuk/limes@latest
```

## Five minutes

Setup errors are dropped below for brevity; each constructor returns one.

```go
ctx := context.Background()
tenantID, runID := uuid.New(), uuid.New() // github.com/google/uuid
key, _ := limes.GenerateKey()
kid, pub, priv := key.KeyID, key.Public, key.Private

records, _ := memstore.NewStore(memstore.StoreConfig{}) // your Store
usage, _ := memstore.NewUsageStore(memstore.UsageStoreConfig[struct{}]{Records: records})

signer, _ := limes.NewSigner(limes.SignerConfig{KeyID: kid, Key: priv})
issuer, _ := limes.NewIssuer(limes.IssuerConfig{
    Signer: signer, Store: records, IssuerName: "my-issuer",
})
keys, _ := jwks.NewStatic(jwks.StaticConfig{Keys: map[string]ed25519.PublicKey{kid: pub}})
verifier, _ := limes.NewVerifier(limes.VerifierConfig{
    Keys:           keys,
    Revocations:    records,
    TrustedIssuers: []string{"my-issuer"},
})

issued, _ := issuer.Issue(ctx, limes.IssueRequest{
    IssuedBy: limes.Principal{Subject: "operator@example.com"}, // who asked
    Subject: limes.Principal{
        Type: limes.PrincipalAgent, TenantID: tenantID,
        Subject: "research-orchestrator",
        Agent:   &limes.AgentPrincipal{AgentType: "my-agent", RunID: runID},
    },
    Audience: []string{"my-service"},
    TTL:      15 * time.Minute,
    Caveats: limes.Caveats{
        Ops:              []limes.Op{limes.OpGet, limes.OpShare},
        ResourcePrefixes: []string{"corpus/public/"},
        Requests:         limes.RequestCeiling{Max: 500},
        Budget:           limes.BudgetCeiling{Max: limes.MustParseAmount("25.00")},
        UnitCode:         "USD",
    },
})

// On every call: verify locally and enforce the caveats in one step, then meter.
granted, err := verifier.Authorize(ctx, limes.AuthorizeRequest{
    Verify: limes.VerifyRequest{Token: issued.Token, Audience: "my-service"},
    Check:  limes.CheckRequest{Op: limes.OpGet, Resource: "corpus/public/report.pdf"},
})
if err != nil {
    return err // a refusal, e.g. limes.ErrResourceNotAllowed outside corpus/public/
}
subject := granted.MeterSubject()
if _, err := usage.Bump(ctx, limes.BumpRequest{Subject: subject}); err != nil {
    return err // limes.ErrRequestLimitExceeded past the 500th request
}
if _, err := usage.Charge(ctx, limes.ChargeRequest{
    Subject: subject, Amount: limes.MustParseAmount("0.35"),
}, nil); err != nil {
    return err // limes.ErrBudgetExceeded past 25.00, with nothing counted
}
```

`IssueRequest` requires `Subject` with its `TenantID`, `IssuedBy` (a
`Subject` at least), `Audience` and `Caveats`; `TTL` defaults to fifteen
minutes. `issued.Token` is what the agent carries; `issued.Capability` is the
record you keep for audit. `verifier.ErrorResponse(ctx, err, limes.AuthSchemeBearer)`
turns a refusal into the HTTP status, `WWW-Authenticate` challenge and problem
body to answer with.
`go run ./example` runs issue, authorize, delegate, meter and revoke end to end;
[examples/](examples) takes each step on its own, pgstore included, logging
what happens.

## What it does

| Feature | What it does |
|---|---|
| **Verify** | Local Ed25519 JWT verification (`alg: Ed25519`, RFC 9864) against a resolved key — static, or an issuer's JWKS fetched by `jwks.Remote`, which revalidates by ETag and honours `Cache-Control`. Claims are read strictly: no unknown or duplicate member, integers within I-JSON's range. Rejections match sentinel errors (`errors.Is`) in families — `ErrUnauthenticated` over `ErrInvalidToken` and `ErrProofOfPossession` — and kinds that must not tell an attacker which check failed share `ErrInvalidToken`. |
| **Enforce** | `Authorize` verifies and checks every stateless caveat in one call: allowed ops, resource prefixes matched at `/` boundaries (a `.` or `..` segment never matches), idempotency keys for writes, tainted reads, source networks. Request and budget limits are the `Meter`'s. |
| **Delegate** | `Issuer.Delegate` issues a child that may only be narrower — operations, resources, limits, audience, and a validity window that starts no earlier and ends no later than the parent's; anything wider is `ErrDelegationTooWide`. The parent must allow `OpShare`, or the delegation is `ErrOpNotAllowed`. |
| **Attenuate offline** | The Biscuit v3 form of a capability can be narrowed by its holder with no key and no network; each copy can get its own limits and be revoked on its own. |
| **Bind to a key** | RFC 9449 DPoP: `Verify` refuses a key-bound capability without a fresh proof from that key, presented under the `DPoP` scheme; server nonces (`dpop.HMACNonces`) work across replicas without shared state. |
| **Meter** | Requests count against the capability and every ancestor; spend against those and the tenant, atomically with your own side effect. Reserve an estimate, settle the actual cost, refund a charge. Amounts are exact integers of billionths of an ISO 4217 unit. `Instrument` adds OpenTelemetry instruments. |
| **Revoke** | One call stops a capability and everything delegated from it; verifiers learn of it within a cache TTL, or at once on `Clear`, which `pgstore.RevocationWatcher` calls on every revocation. |
| **Report** | `LedgerReader.SumCharges` sums a tenant's charges over a period by unit, whole or by UTC hour, day, ISO week or month, and by operation, actor or capability, the largest first. `RecordCensus.CountRecords` counts capability records — active, expired, revoked, delegated, expiring soon — for one tenant or all, by tenant and principal type. |
| **Answer refusals** | `ErrorResponse` maps a refusal to its status, an RFC 6750 / RFC 9449 `WWW-Authenticate` challenge and an RFC 9457 problem body, without the error's own text. |

You implement `Store` and `UsageStore[TX]` — or use `memstore`, or `pgstore`
over PostgreSQL — and a `KeyResolver`, which `jwks.Static` and `jwks.Remote`
already are, and check your implementation from its own tests with `storetest`,
`metertest` and `jwkstest` (`dpoptest` for a DPoP replay cache or nonce source). `UsageStore` is generic in your transaction type, so a charge can
commit together with, say, an outbox write; with no transactions, use
`UsageStore[struct{}]`. Optional contracts serve one feature each:
`BiscuitRevocationStore` and `CopyUsageReader` for Biscuit copies,
`LedgerReader` for billing reads, `RecordCensus` for a census of records, and
`TenantRecordEraser` and
`TenantUsageEraser` to remove a tenant.

## Guarantees

- **Charges are atomic.** A failed side effect, or a rejection by any
  ceiling, leaves every counter and the ledger untouched, so a retry is safe.
- **Delegation and attenuation never widen.** A fuzz test
  (`FuzzNarrowsNeverWidens`) checks that whatever `Narrows` accepts reaches no
  resource the parent cannot; an offline Biscuit block that asks for more
  invalidates the token.
- **A bound token needs its key**, and its children stay bound.
- **The token format is pinned.** Golden fixtures in `testdata/` fail CI on any
  wire-format drift, because tokens outlive deployments.
- **No hidden dependencies.** `isolation_test.go` fails if a database driver or
  an object-storage SDK enters the resolved dependency graph.
- **The root module's tests need nothing**: no database, no network, no
  container. `pgstore`'s run against PostgreSQL in a container, and
  `redisreplay`'s against miniredis and, in CI, a real Redis.

## Versioning

Releases are `vX.Y.Z` tags, cut from Conventional Commits on `main` when CI
passes: `feat` is a minor; `fix`, `perf` and `security` a patch. The module is
pre-1.0, so a breaking change is a minor too — **the Go API may change between
minor versions**; read the release notes, which list breaking changes first,
before bumping.

`redisreplay` and `pgstore` are modules of their own, each with a release
line of its own — `redisreplay/vX.Y.Z`, `pgstore/vX.Y.Z` — cut by the same
rules from the commits that touch it, plus one: a module's own `build`
commit, such as `build(pgstore): require limes vX.Y.Z`, is a patch of it. A
commit releases, by its type, each module whose files it changes; one scoped
`pgstore` or `redisreplay` never releases limes, breaking or not.

The token wire format — claims, `typ: limes-cap-v4+jwt`, the Biscuit facts —
is a separate promise: a token is a credential that outlives the process that
issued it, so the format changes only as a deliberate breaking change that
moves the version in `typ`, with the golden fixtures regenerated in the same
commit. A verifier refuses a token of a version it does not accept
(`VerifierConfig.AcceptTokenTypes`) outright, rather than read its claims
under the wrong schema. Every 1.x verifier accepts `limes-cap-v4+jwt`, so a
later version ships in a minor release: verifiers accept it first, issuers
mint it after. Tokens are signed with Ed25519 throughout 1.x; another
algorithm would be another token version, never a runtime choice.

## Documentation

| Document | Covers |
|----------|--------|
| [docs/guide.md](docs/guide.md) | each concern in turn: verification, caveats, delegation, DPoP, Biscuit, metering, revocation, storage contracts, key rotation |
| [docs/standards.md](docs/standards.md) | the RFCs implemented — JWT, DPoP, JWKS, HTTP challenges and problem details — the choices made within them, and the OAuth standards deliberately left out |
| [docs/diagrams.md](docs/diagrams.md) | the contract boundary, a capability's lifecycle, the verification gates, delegation, Biscuit copies, the charge path, revocation |
| [docs/migration.md](docs/migration.md) | every breaking change from v0.3.0 on, of limes and of pgstore, release by release, as what it was and what it is now |
| [docs/api-review-v1.md](docs/api-review-v1.md) | the review of the API before 1.0: each finding, the decision taken and whether it is done |
| [CONTRIBUTING.md](.github/CONTRIBUTING.md) | building, testing and how a change lands |
| [SECURITY.md](.github/SECURITY.md) | reporting a vulnerability |

## Layout

| Path | Holds |
|------|-------|
| [`/`](.) | the `limes` package: issuer, verifier, caveats, Biscuit, metering, contracts |
| [jwks/](jwks) | key sets (RFC 7517): the JWKS document and handler, remote and static key resolvers |
| [dpop/](dpop) | proof of possession (RFC 9449): signing and checking proofs, replay cache, server nonces |
| [inspect/](inspect) | reading a token without verifying it, for tooling |
| [memstore/](memstore) | the in-memory reference implementation of every storage contract |
| [pgstore/](pgstore) | the reference PostgreSQL store, a module of its own: `Store`, `BiscuitRevocationStore`, `UsageStore[pgx.Tx]`, `CopyUsageReader`, a `Ledger`, a `Census` and both tenant erasers under row-level security per tenant, with `EraseTenant` and a `RevocationWatcher`; held to storetest and metertest; released as `pgstore/vX.Y.Z` |
| [redisreplay/](redisreplay) | a `dpop.ReplayCache` over Redis, a module of its own, for verifiers behind several replicas; released as `redisreplay/vX.Y.Z` |
| [internal/](internal) | the module's own shared packages, not importable from outside it: JOSE pieces (`jose`), HTTP header names (`httpx`), RFC 3986 URI normalisation (`rfc3986`), access to unexported root internals for sibling packages (`link`), and field coverage and per-check deadlines for the conformance suites (`fields`, `watchdog`) |
| [storetest/](storetest), [metertest/](metertest), [dpop/dpoptest](dpop/dpoptest), [jwks/jwkstest](jwks/jwkstest) | conformance suites to run from your own implementation's tests |
| [example/](example) | the five-minute walkthrough as a runnable program |
| [examples/](examples) | smaller programs, one topic each, that log every step to stdout: issuing and verifying, delegation, metering, and pgstore over PostgreSQL; a module of its own, never released |
| [testdata/](testdata) | golden tokens, and the Biscuit vocabulary and cases shared with SDKs in other languages |
| [tools/](tools) | the developer tools, pinned in modules of their own and run with `go tool -modfile=tools/go.mod` (yq: `tools/yq/go.mod`) |

## License

Apache-2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).
