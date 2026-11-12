# Defaults

What each `Config` field does at its zero value, and the value of every
`Default…` constant. Both are contract ([compatibility.md](compatibility.md)):
changing either is a break, marked and listed in the release notes.

`defaults_doc_test.go` holds the tables to the code. Every exported field of
every exported `…Config` struct has a row; so does every exported `Default…`
constant of a package a consumer imports, with its value as declared; every
`Default…` a row names is declared. Test-helper packages (`…test`) are left
out.

Between releases CI compares two columns: a field's **Default** and a
constant's **Value**. A Default cell holds the value the zero field stands
for, or one of `required` (zero is refused when the value is built), `off`,
`on`, `none` and `refused`. The **Zero value** column is prose and may be
reworded. A behaviour change that keeps the Default cell must still change
it, or be marked breaking by hand.

## limes

| Config | Field | Default | Zero value |
|---|---|---|---|
| `limes.IssuerConfig` | `Signer` | required |  |
| `limes.IssuerConfig` | `Store` | required |  |
| `limes.IssuerConfig` | `IssuerName` | required |  |
| `limes.IssuerConfig` | `DefaultTTL` | `DefaultIssueTTL` |  |
| `limes.IssuerConfig` | `MaxTokenBytes` | `DefaultMaxTokenBytes` |  |
| `limes.IssuerConfig` | `Now` | `time.Now` |  |
| `limes.IssuerConfig` | `Biscuit` | refused | `Issuer.Biscuit` refuses: the issuer mints JWTs alone. |
| `limes.IssuerConfig` | `Logger` | `slog.Default()` |  |
| `limes.IssuerConfig` | `Events` | none | No callback is made. |
| `limes.RevokerConfig` | `Store` | required |  |
| `limes.RevokerConfig` | `Bulk` | refused | `Revoker.RevokeMatching` fails with `ErrInvalidConfig`. |
| `limes.RevokerConfig` | `Biscuits` | refused | `Revoker.RevokeBiscuit` fails with `ErrInvalidConfig`. |
| `limes.RevokerConfig` | `Cache` | none | No cache is cleared: a revocation reaches a cached answer through a signal or the cache's TTL. |
| `limes.RevokerConfig` | `Logger` | `slog.Default()` |  |
| `limes.RevokerConfig` | `Events` | none | No callback is made. |
| `limes.RevocationCacheConfig` | `Capabilities` | required |  |
| `limes.RevocationCacheConfig` | `RequireLineage` | off | A lookup with no lineage is taken, and the cache clears whole on every `ClearRevoked`. |
| `limes.RevocationCacheConfig` | `Biscuits` | refused | Every Biscuit question is refused. |
| `limes.RevocationCacheConfig` | `TTL` | `DefaultRevocationCacheTTL` |  |
| `limes.RevocationCacheConfig` | `SyncedTTL` | `TTL` |  |
| `limes.RevocationCacheConfig` | `MaxEntries` | `DefaultRevocationCacheEntries` |  |
| `limes.RevocationCacheConfig` | `MaxStale` | none | Never: the cache fails closed. |
| `limes.RevocationCacheConfig` | `Logger` | `slog.Default()` |  |
| `limes.RevocationCacheConfig` | `Now` | `time.Now` |  |
| `limes.VerifierConfig` | `Keys` | required |  |
| `limes.VerifierConfig` | `Revocations` | required | A `RevocationLineageLookup`, unless `AllowUnrecorded` is set. |
| `limes.VerifierConfig` | `AllowUnrecorded` | off | A capability `Revocations` has no record of is refused with `ErrUnrecorded`, and `Revocations` must be a `RevocationLineageLookup` (a `RevocationCache` over one included), or `NewVerifier` refuses it. A lookup on a lagging replica refuses a fresh token until its record arrives. |
| `limes.VerifierConfig` | `TrustedIssuers` | required |  |
| `limes.VerifierConfig` | `KeyIssuers` | none | No kid is bound: taken with one trusted issuer, refused with several unless `SharedKeys` is set. |
| `limes.VerifierConfig` | `SharedKeys` | off |  |
| `limes.VerifierConfig` | `Now` | `time.Now` |  |
| `limes.VerifierConfig` | `Leeway` | `DefaultLeeway` |  |
| `limes.VerifierConfig` | `NoLeeway` | off | `Leeway` applies. |
| `limes.VerifierConfig` | `MaxTokenBytes` | `DefaultMaxTokenBytes` |  |
| `limes.VerifierConfig` | `Biscuit` | refused | A Biscuit is refused like any malformed token. |
| `limes.VerifierConfig` | `DPoP` | refused | Every key-bound capability is refused with `ErrProofUnsupported`. |
| `limes.VerifierConfig` | `Realm` | `DefaultRealm` |  |
| `limes.VerifierConfig` | `Logger` | `slog.Default()` |  |
| `limes.VerifierConfig` | `Events` | none | No callback is made. |
| `limes.BiscuitConfig` | `Revocations` | required |  |
| `limes.BiscuitConfig` | `Form` | required |  |
| `limes.BiscuitConfig` | `MeterCopies` | off | A Biscuit setting a copy's own limits is refused with `ErrCopyCountersNotMetered`. |
| `limes.SignerConfig` | `KeyID` | required |  |
| `limes.SignerConfig` | `Key` | required |  |
| `limes.SignerConfig` | `Algorithm` | `SigningAlgorithmEd25519` |  |
| `dpop.VerifierConfig` | `Window` | `DefaultWindow` |  |
| `dpop.VerifierConfig` | `Replay` | required |  |
| `dpop.VerifierConfig` | `Now` | `time.Now` |  |
| `dpop.VerifierConfig` | `Nonces` | none | No nonce is required. |
| `dpop.VerifierConfig` | `MaxProofBytes` | `DefaultMaxProofBytes` |  |
| `dpop.VerifierConfig` | `MatchPathOnly` | off | The whole URL is compared. |
| `dpop.MemoryReplayCacheConfig` | `MaxEntries` | `DefaultReplayCacheEntries` |  |
| `dpop.MemoryReplayCacheConfig` | `EntriesPerHolder` | `DefaultReplayEntriesPerHolder` | Or `MaxEntries` when smaller. Caps one holder at `EntriesPerHolder / (Window + ReplayClockSkew)` proofs a second: about 15 with the defaults. |
| `dpop.MemoryReplayCacheConfig` | `Now` | `time.Now` |  |
| `dpop.MemoryReplayCacheConfig` | `Logger` | `slog.Default()` |  |
| `dpop.HMACNoncesConfig` | `Keys` | required |  |
| `dpop.HMACNoncesConfig` | `Lifetime` | `DefaultNonceLifetime` |  |
| `dpop.HMACNoncesConfig` | `Now` | `time.Now` |  |
| `jwks.HandlerConfig` | `Keys` | required |  |
| `jwks.HandlerConfig` | `Algorithms` | `AlgorithmEd25519` | Every key's entry names it. |
| `jwks.HandlerConfig` | `Logger` | `slog.Default()` |  |
| `jwks.HandlerConfig` | `MaxAge` | `DefaultMaxAge` |  |
| `jwks.HandlerConfig` | `Revalidate` | off | Clients keep the set for `MaxAge`. |
| `jwks.RemoteConfig` | `URL` | required |  |
| `jwks.RemoteConfig` | `Client` | `DefaultFetchTimeout` | A client with that timeout. |
| `jwks.RemoteConfig` | `RefreshInterval` | `DefaultRefreshInterval` |  |
| `jwks.RemoteConfig` | `MinRefreshInterval` | `DefaultMinRefreshInterval` |  |
| `jwks.RemoteConfig` | `MaxStale` | `DefaultMaxStale` |  |
| `jwks.RemoteConfig` | `MaxDocumentBytes` | `DefaultMaxDocumentBytes` |  |
| `jwks.RemoteConfig` | `Now` | `time.Now` |  |
| `jwks.RemoteConfig` | `Logger` | `slog.Default()` |  |
| `jwks.StaticConfig` | `Keys` | none | An empty set: every kid is unknown. |
| `memstore.StoreConfig` | `Now` | `time.Now` |  |
| `memstore.UsageStoreConfig` | `Records` | required |  |
| `memstore.UsageStoreConfig` | `Now` | `time.Now` |  |
| `memstore.UsageStoreConfig` | `MaxCopiesPerCapability` | `meter.DefaultMaxCopiesPerCapability` |  |

| Constant | Value |
|---|---|
| `dpop.DefaultMaxProofBytes` | `4 << 10` |
| `dpop.DefaultNonceLifetime` | `5 * time.Minute` |
| `dpop.DefaultReplayCacheEntries` | `100_000` |
| `dpop.DefaultReplayEntriesPerHolder` | `1_000` |
| `dpop.DefaultWindow` | `time.Minute` |
| `jwks.DefaultFetchTimeout` | `10 * time.Second` |
| `jwks.DefaultMaxAge` | `DefaultRefreshInterval` |
| `jwks.DefaultMaxDocumentBytes` | `1 << 20` |
| `jwks.DefaultMaxStale` | `time.Hour` |
| `jwks.DefaultMinRefreshInterval` | `10 * time.Second` |
| `jwks.DefaultRefreshInterval` | `5 * time.Minute` |
| `limes.DefaultHousekeepingLimit` | `1_000` |
| `limes.DefaultIssueTTL` | `15 * time.Minute` |
| `limes.DefaultLeeway` | `30 * time.Second` |
| `limes.DefaultListLimit` | `50` |
| `limes.DefaultMaxTokenBytes` | `16 << 10` |
| `limes.DefaultRealm` | `"limes"` |
| `limes.DefaultRevocationCacheEntries` | `100_000` |
| `limes.DefaultRevocationCacheTTL` | `2 * time.Second` |
| `meter.DefaultLeaseTTL` | `5 * time.Minute` |
| `meter.DefaultMaxCopiesPerCapability` | `1024` |
| `meter.DefaultReservationTTL` | `5 * time.Minute` |

## pgstore

| Config | Field | Default | Zero value |
|---|---|---|---|
| `pgstore.Config` | `DB` | required |  |
| `pgstore.Config` | `Now` | `time.Now` |  |
| `pgstore.Config` | `Scope` | `ScopeFromContext` | The schema's own policies, from `WithTenant`, `WithAllTenants` and `WithAllTenantsReadOnly`. |
| `pgstore.Config` | `CheckTenant` | none | Every tenant is one the store can mint for. |
| `pgstore.Config` | `CheckScope` | `CheckSchemaScope` | Under the default `Scope`; beside a `Scope` of the consumer's, no check. |
| `pgstore.Config` | `Logger` | `slog.Default()` |  |
| `pgstore.Config` | `Events` | none | No callback is made. A wait for a tenant's aggregate past 100 ms is still logged at WARN. |
| `pgstore.Config` | `DisableLeases` | off | A lease opens only when `meter.Leaser.Lease` asks for one, and the store reports `meter.FeatureLeases` enabled. A store that opens none still pays one statement per charge and bump a lease could cover, a read of the chain's leases, in the batch the call already sends: no extra round trip. Set, the read stays: leases already open are still charged under. |
| `pgstore.Config` | `DisableTenantStripes` | off | A tenant with no ceiling is counted in stripes: its calls lock no aggregate row. Set, the store still folds stripes another store wrote, and sums them when it reads the aggregate. |
| `pgstore.Config` | `MaxCopiesPerCapability` | `meter.DefaultMaxCopiesPerCapability` |  |
| `pgstore.Config` | `TenantBudgetLockTimeout` | `5s` | How long `SetTenantBudget` waits for the tenant's calls in flight, and so the longest it stalls the tenant's new ones. |
| `pgstore.ReplayConfig` | `DB` | required |  |
| `pgstore.ReplayConfig` | `EntriesPerHolder` | `dpop.DefaultReplayEntriesPerHolder` | Caps one holder at `EntriesPerHolder / (Window + ReplayClockSkew)` proofs a second: about 15 with the defaults. |
| `pgstore.ReplayConfig` | `Now` | `time.Now` |  |
| `pgstore.ReplayConfig` | `Logger` | `slog.Default()` |  |

| Constant | Value |
|---|---|
| `pgstore.DefaultReplayPurgeBatch` | `10_000` |
| `pgstore.DefaultWatchHeartbeat` | `30 * time.Second` |

## redisreplay

| Config | Field | Default | Zero value |
|---|---|---|---|
| `redisreplay.Config` | `Client` | required |  |
| `redisreplay.Config` | `KeyPrefix` | `DefaultKeyPrefix` |  |
| `redisreplay.Config` | `EntriesPerHolder` | `dpop.DefaultReplayEntriesPerHolder` | Caps one holder at `EntriesPerHolder / (Window + ReplayClockSkew)` proofs a second: about 15 with the defaults. |
| `redisreplay.Config` | `Now` | `time.Now` |  |
| `redisreplay.Config` | `Logger` | `slog.Default()` |  |

| Constant | Value |
|---|---|
| `redisreplay.DefaultKeyPrefix` | `"limes:dpop:replay:"` |

## exchange

| Config | Field | Default | Zero value |
|---|---|---|---|
| `exchange.Config` | `Issuer` | required |  |
| `exchange.Config` | `Subjects` | required |  |
| `exchange.Config` | `Policy` | required |  |
| `exchange.Config` | `DPoP` | refused | Every request with a proof is refused. |
| `exchange.Config` | `Capabilities` | none | No capability is narrowed; set with `CapabilityAudience` and `NarrowPolicy`, or none of them. |
| `exchange.Config` | `CapabilityAudience` | none | As `Capabilities`. |
| `exchange.Config` | `NarrowPolicy` | none | As `Capabilities`. |
| `exchange.Config` | `Exchanges` | none | Every exchange issues a capability of its own. |
| `exchange.Config` | `Now` | `time.Now` |  |
| `exchange.Config` | `Logger` | `slog.Default()` |  |
| `exchange.HandlerConfig` | `Exchanger` | required |  |
| `exchange.HandlerConfig` | `Authenticate` | required |  |
| `exchange.HandlerConfig` | `EndpointURL` | required | When the `Exchanger` checks DPoP. |
| `exchange.HandlerConfig` | `MaxBodyBytes` | `DefaultMaxBodyBytes` |  |
| `exchange.HandlerConfig` | `Logger` | `slog.Default()` |  |
| `exchange.IntrospectionConfig` | `Verifier` | required |  |
| `exchange.IntrospectionConfig` | `Authenticate` | required |  |
| `exchange.IntrospectionConfig` | `Audiences` | required | Unless `AnyAudience`. |
| `exchange.IntrospectionConfig` | `AnyAudience` | off | A client learns only about tokens of the audiences `Audiences` gives it. |
| `exchange.IntrospectionConfig` | `MaxBodyBytes` | `DefaultMaxBodyBytes` |  |
| `exchange.IntrospectionConfig` | `Logger` | `slog.Default()` |  |
| `jwtverify.Config` | `Issuer` | required |  |
| `jwtverify.Config` | `Keys` | required |  |
| `jwtverify.Config` | `Types` | `TypeAccessToken`, `TypeAccessTokenMedia` |  |
| `jwtverify.Config` | `ClientIDClaim` | `ClaimClientID` |  |
| `jwtverify.Config` | `Algorithms` | `DefaultAlgorithms()` | `RS256`, `RS384`, `RS512`, `PS256`, `PS384`, `PS512`, `ES256`, `ES384`, `ES512`, `EdDSA`, `Ed25519`. |
| `jwtverify.Config` | `ClockSkew` | `limes.DefaultLeeway` |  |
| `jwtverify.Config` | `NoClockSkew` | off | `ClockSkew` applies. |
| `jwtverify.Config` | `Now` | `time.Now` |  |

| Constant | Value |
|---|---|
| `exchange.DefaultMaxBodyBytes` | `64 << 10` |

otelmetric and biscuit take no `Config`.
