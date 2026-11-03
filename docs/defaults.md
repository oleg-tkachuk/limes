# Defaults

What each field of every `Config` does when it is left at its zero value.
These are part of the contract ([compatibility.md](compatibility.md)):
changing what a zero field does is a break, marked and listed in the
release notes, and these tables are what CI compares between releases.

A test holds the tables to the code: every exported field of every exported
`…Config` struct has its row, and every `Default…` constant a row names is
declared in that package. "Required" means the zero value is refused when
the value is built.

## limes

| Config | Field | Zero value |
|---|---|---|
| `limes.IssuerConfig` | `Signer` | Required. |
| `limes.IssuerConfig` | `Store` | Required. |
| `limes.IssuerConfig` | `IssuerName` | Required. |
| `limes.IssuerConfig` | `DefaultTTL` | `DefaultIssueTTL`, 15 minutes. |
| `limes.IssuerConfig` | `MaxTokenBytes` | `DefaultMaxTokenBytes`, 16 KiB. |
| `limes.IssuerConfig` | `Now` | `time.Now`. |
| `limes.IssuerConfig` | `Biscuit` | `Issuer.Biscuit` refuses: the issuer mints JWTs alone. |
| `limes.RevocationCacheConfig` | `Capabilities` | Required. |
| `limes.RevocationCacheConfig` | `RequireLineage` | Off: a lookup with no lineage is taken, and the cache clears whole on every `ClearRevoked`. |
| `limes.RevocationCacheConfig` | `Biscuits` | Every Biscuit question is refused. |
| `limes.RevocationCacheConfig` | `TTL` | `DefaultRevocationCacheTTL`, 2 seconds. |
| `limes.RevocationCacheConfig` | `SyncedTTL` | `TTL`. |
| `limes.RevocationCacheConfig` | `MaxEntries` | `DefaultRevocationCacheEntries`, 100 000 per kind of answer. |
| `limes.RevocationCacheConfig` | `MaxStale` | Never: the cache fails closed. |
| `limes.RevocationCacheConfig` | `Logger` | `slog.Default()`. |
| `limes.RevocationCacheConfig` | `Now` | `time.Now`. |
| `limes.VerifierConfig` | `Keys` | Required. |
| `limes.VerifierConfig` | `Revocations` | Required. |
| `limes.VerifierConfig` | `TrustedIssuers` | Required. |
| `limes.VerifierConfig` | `KeyIssuers` | No kid is bound: taken with one trusted issuer, refused with several unless `SharedKeys` is set. |
| `limes.VerifierConfig` | `SharedKeys` | Off. |
| `limes.VerifierConfig` | `Now` | `time.Now`. |
| `limes.VerifierConfig` | `Leeway` | `DefaultLeeway`, 30 seconds. |
| `limes.VerifierConfig` | `NoLeeway` | Off: `Leeway` applies. |
| `limes.VerifierConfig` | `MaxTokenBytes` | `DefaultMaxTokenBytes`, 16 KiB. |
| `limes.VerifierConfig` | `Biscuit` | A Biscuit is refused like any malformed token. |
| `limes.VerifierConfig` | `DPoP` | Every key-bound capability is refused with `ErrProofUnsupported`. |
| `limes.VerifierConfig` | `Realm` | `DefaultRealm`, `limes`. |
| `limes.VerifierConfig` | `Logger` | `slog.Default()`. |
| `limes.BiscuitConfig` | `Revocations` | Required. |
| `limes.BiscuitConfig` | `Form` | Required. |
| `limes.BiscuitConfig` | `MeterCopies` | Off: a Biscuit setting a copy's own limits is refused with `ErrCopyCountersNotMetered`. |
| `limes.SignerConfig` | `KeyID` | Required. |
| `limes.SignerConfig` | `Key` | Required. |
| `limes.SignerConfig` | `Algorithm` | `SigningAlgorithmEd25519`. |
| `dpop.VerifierConfig` | `Window` | `DefaultWindow`, 1 minute. |
| `dpop.VerifierConfig` | `Replay` | Required. |
| `dpop.VerifierConfig` | `Now` | `time.Now`. |
| `dpop.VerifierConfig` | `Nonces` | No nonce is required. |
| `dpop.VerifierConfig` | `MaxProofBytes` | `DefaultMaxProofBytes`, 4 KiB. |
| `dpop.VerifierConfig` | `MatchPathOnly` | Off: the whole URL is compared. |
| `dpop.MemoryReplayCacheConfig` | `MaxEntries` | `DefaultReplayCacheEntries`, 100 000. |
| `dpop.MemoryReplayCacheConfig` | `EntriesPerHolder` | `DefaultReplayEntriesPerHolder`, 1 000, or `MaxEntries` when smaller. |
| `dpop.MemoryReplayCacheConfig` | `Now` | `time.Now`. |
| `dpop.HMACNoncesConfig` | `Keys` | Required. |
| `dpop.HMACNoncesConfig` | `Lifetime` | `DefaultNonceLifetime`, 5 minutes. |
| `dpop.HMACNoncesConfig` | `Now` | `time.Now`. |
| `jwks.HandlerConfig` | `Keys` | Required. |
| `jwks.HandlerConfig` | `Algorithms` | Every key's entry names `AlgorithmEd25519`. |
| `jwks.HandlerConfig` | `Logger` | `slog.Default()`. |
| `jwks.HandlerConfig` | `MaxAge` | `DefaultMaxAge`, 5 minutes. |
| `jwks.HandlerConfig` | `Revalidate` | Off: clients keep the set for `MaxAge`. |
| `jwks.RemoteConfig` | `URL` | Required. |
| `jwks.RemoteConfig` | `Client` | A client with a `DefaultFetchTimeout` (10 seconds) timeout. |
| `jwks.RemoteConfig` | `RefreshInterval` | `DefaultRefreshInterval`, 5 minutes. |
| `jwks.RemoteConfig` | `MinRefreshInterval` | `DefaultMinRefreshInterval`, 10 seconds. |
| `jwks.RemoteConfig` | `MaxStale` | `DefaultMaxStale`, 1 hour. |
| `jwks.RemoteConfig` | `MaxDocumentBytes` | `DefaultMaxDocumentBytes`, 1 MiB. |
| `jwks.RemoteConfig` | `Now` | `time.Now`. |
| `jwks.StaticConfig` | `Keys` | An empty set: every kid is unknown. |
| `memstore.StoreConfig` | `Now` | `time.Now`. |
| `memstore.UsageStoreConfig` | `Records` | Required. |
| `memstore.UsageStoreConfig` | `Now` | `time.Now`. |
| `memstore.UsageStoreConfig` | `MaxCopiesPerCapability` | `meter.DefaultMaxCopiesPerCapability`, 1 024. |

## pgstore

| Config | Field | Zero value |
|---|---|---|
| `pgstore.Config` | `DB` | Required. |
| `pgstore.Config` | `Now` | `time.Now`. |
| `pgstore.Config` | `Scope` | `ScopeFromContext`: the schema's own policies, from `WithTenant`, `WithAllTenants` and `WithAllTenantsReadOnly`. |
| `pgstore.Config` | `CheckTenant` | Every tenant is one the store can mint for. |
| `pgstore.Config` | `CheckScope` | Under the default `Scope`, `CheckSchemaScope`; beside a `Scope` of the consumer's, no check. |
| `pgstore.Config` | `Logger` | `slog.Default()`. |
| `pgstore.Config` | `DisableLeases` | Off: the usage store opens leases, and reports `meter.FeatureLeases` enabled. |
| `pgstore.Config` | `MaxCopiesPerCapability` | `meter.DefaultMaxCopiesPerCapability`, 1 024. |
| `pgstore.ReplayConfig` | `DB` | Required. |
| `pgstore.ReplayConfig` | `EntriesPerHolder` | `dpop.DefaultReplayEntriesPerHolder`, 1 000. |
| `pgstore.ReplayConfig` | `Now` | `time.Now`. |
| `pgstore.ReplayConfig` | `Logger` | `slog.Default()`. |

## redisreplay

| Config | Field | Zero value |
|---|---|---|
| `redisreplay.Config` | `Client` | Required. |
| `redisreplay.Config` | `KeyPrefix` | `DefaultKeyPrefix`, `limes:dpop:replay:`. |
| `redisreplay.Config` | `EntriesPerHolder` | `dpop.DefaultReplayEntriesPerHolder`, 1 000. |
| `redisreplay.Config` | `Now` | `time.Now`. |

## exchange

| Config | Field | Zero value |
|---|---|---|
| `exchange.Config` | `Issuer` | Required. |
| `exchange.Config` | `Subjects` | Required. |
| `exchange.Config` | `Policy` | Required. |
| `exchange.Config` | `DPoP` | Every request with a proof is refused. |
| `exchange.Config` | `Capabilities` | No capability is narrowed; set with `CapabilityAudience` and `NarrowPolicy`, or none of them. |
| `exchange.Config` | `CapabilityAudience` | As `Capabilities`. |
| `exchange.Config` | `NarrowPolicy` | As `Capabilities`. |
| `exchange.Config` | `Exchanges` | Every exchange issues a capability of its own. |
| `exchange.Config` | `Now` | `time.Now`. |
| `exchange.Config` | `Logger` | `slog.Default()`. |
| `exchange.HandlerConfig` | `Exchanger` | Required. |
| `exchange.HandlerConfig` | `Authenticate` | Required. |
| `exchange.HandlerConfig` | `EndpointURL` | Required when the `Exchanger` checks DPoP. |
| `exchange.HandlerConfig` | `MaxBodyBytes` | `DefaultMaxBodyBytes`, 64 KiB. |
| `exchange.HandlerConfig` | `Logger` | `slog.Default()`. |
| `exchange.IntrospectionConfig` | `Verifier` | Required. |
| `exchange.IntrospectionConfig` | `Authenticate` | Required. |
| `exchange.IntrospectionConfig` | `Audiences` | Any authenticated client learns about any token. |
| `exchange.IntrospectionConfig` | `MaxBodyBytes` | `DefaultMaxBodyBytes`, 64 KiB. |
| `exchange.IntrospectionConfig` | `Logger` | `slog.Default()`. |
| `jwtverify.Config` | `Issuer` | Required. |
| `jwtverify.Config` | `Keys` | Required. |
| `jwtverify.Config` | `Types` | `TypeAccessToken` and `TypeAccessTokenMedia`. |
| `jwtverify.Config` | `ClientIDClaim` | `ClaimClientID`. |
| `jwtverify.Config` | `Algorithms` | `DefaultAlgorithms`. |
| `jwtverify.Config` | `ClockSkew` | None: exp, nbf and iat are held to the clock exactly. |
| `jwtverify.Config` | `Now` | `time.Now`. |

otelmetric and biscuit take no `Config`.
