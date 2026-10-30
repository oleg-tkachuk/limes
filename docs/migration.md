# Migrating between releases

Every release that broke the API, newest first, each change as what it was
and what it is now. Before v1.0.0 a breaking change cuts a minor release, so
only minor releases (`v0.N.0`, `pgstore/v0.N.0`) appear here; patch releases
never break.

Names are those of the release that changed them. A name changed again later
appears again under that later release: to cross several releases, read
upwards from the one you are on.

A pull request that marks a break adds its entry above the newest one,
under the release it cuts: the minor after the newest tag. CI refuses a
breaking marker whose change leaves this file alone. From v1.0.0 on no
change may break the API, and this file stops growing.

## Release candidates of 1.0

limes stays on 0.x on `main` until the release that cuts 1.0, so every entry
below is filed under the 0.x release that made the change. A candidate,
`v1.0.0-rc.N`, tags `main` as it is: its API is that of the newest 0.x
release before it. The table names that release for each candidate, and
each nested module's release on `main` when the candidate was cut. A `+`
marks a candidate cut between two 0.x releases: it holds additions the next
release made and none of its breaks, since an entry lands with its break.
The release run that cuts a candidate proposes its row, and CI holds the
table to the tags (`.github/ci/candidates.ts`).

The nested modules require a 0.x release of limes, never a candidate. A
build that requires a candidate as well selects the candidate, since Go
orders every `v1.0.0-rc.N` above every 0.x, so require a nested module's
release only with a candidate at or above the row that lists it.

To move between two candidates, read the entries after the releases your
row names, up to and including those of the row you move to: limes's under
`vX.Y.Z`, a nested module's under `<module>/vX.Y.Z`. From rc.14 to rc.18,
that is `v0.133.0`, `pgstore/v0.44.0` and `pgstore/v0.45.0`.

| Candidate | limes | redisreplay | pgstore | otelmetric | exchange | biscuit |
| --- | --- | --- | --- | --- | --- | --- |
| `rc.23` | `v0.139.0` | `v0.3.1` | `v0.50.1` | `v0.11.1` | `v0.14.1` | `v0.1.8` |
| `rc.22` | `v0.138.0`+ | `v0.3.0` | `v0.50.0` | `v0.11.0` | `v0.14.0` | `v0.1.7` |
| `rc.21` | `v0.138.0` | `v0.3.0` | `v0.50.0` | `v0.11.0` | `v0.14.0` | `v0.1.7` |
| `rc.20` | `v0.135.0` | `v0.2.14` | `v0.48.1` | `v0.10.14` | `v0.11.0` | `v0.1.4` |
| `rc.19` | `v0.133.1` | `v0.2.12` | `v0.47.0` | `v0.10.12` | `v0.9.2` | `v0.1.2` |
| `rc.18` | `v0.133.1` | `v0.2.12` | `v0.46.1` | `v0.10.12` | `v0.9.2` | `v0.1.2` |
| `rc.17` | `v0.133.0` | `v0.2.11` | `v0.46.0` | `v0.10.11` | `v0.9.1` | `v0.1.1` |
| `rc.16` | `v0.132.1`+ | `v0.2.10` | `v0.45.0` | `v0.10.9` | `v0.9.0` | — |
| `rc.15` | `v0.132.1`+ | `v0.2.10` | `v0.45.0` | `v0.10.9` | `v0.9.0` | — |
| `rc.14` | `v0.132.1` | `v0.2.9` | `v0.43.0` | `v0.10.8` | `v0.8.1` | — |
| `rc.13` | `v0.132.0` | `v0.2.8` | `v0.42.2` | `v0.10.7` | `v0.8.0` | — |
| `rc.12` | `v0.131.2`+ | `v0.2.8` | `v0.42.2` | `v0.10.7` | `v0.6.0` | — |
| `rc.11` | `v0.131.2` | `v0.2.8` | `v0.42.2` | `v0.10.7` | `v0.6.0` | — |
| `rc.10` | `v0.131.0` | `v0.2.6` | `v0.42.0` | `v0.10.5` | `v0.5.0` | — |
| `rc.9` | `v0.129.0` | `v0.2.4` | `v0.41.1` | `v0.10.3` | `v0.3.0` | — |
| `rc.8` | `v0.127.0`+ | `v0.2.2` | `v0.40.1` | `v0.10.1` | `v0.2.0` | — |
| `rc.7` | `v0.127.0` | `v0.2.2` | `v0.40.1` | `v0.10.1` | `v0.1.1` | — |
| `rc.6` | `v0.127.0` | `v0.2.2` | `v0.40.1` | `v0.10.1` | `v0.1.1` | — |
| `rc.5` | `v0.126.0`+ | `v0.2.1` | `v0.40.0` | `v0.10.0` | — | — |
| `rc.4` | `v0.125.1`+ | `v0.1.74` | `v0.39.4` | `v0.9.3` | — | — |
| `rc.3` | `v0.125.1` | `v0.1.74` | `v0.39.3` | `v0.9.3` | — | — |
| `rc.2` | `v0.123.0` | `v0.1.71` | `v0.39.0` | `v0.8.0` | — | — |
| `rc.1` | `v0.117.0` | `v0.1.65` | `v0.35.1` | `v0.6.2` | — | — |

## pgstore/v0.52.0

- The schema is at version 28: migration 028 notifies a statement's
  revocations in parts of `MaxNotifiedRevocations` (100) ids, up to
  `MaxNotifiedRevocationsPerStatement` (1 000), where 021 cleared every
  cache whole past 100. `migrate.Run` applies it; a consumer that vendors
  the schema applies step 028 before this release. A watcher of your own
  reads each notification as before: a list of ids, or none.

## pgstore/v0.51.0

- The schema is at version 27: migration 027 indexes the records by client
  and by session ahead of the tenant, for a revocation across tenants, and
  builds the indexes `CONCURRENTLY`. `migrate.Run` applies it; a consumer
  that vendors the schema (`ddl.Steps()`) writes out step 027 and applies it
  before this release, whose `CheckSchema` refuses version 26 as
  `ErrSchemaMismatch`.
- `RevokeMatching` takes `AllTenants` under `WithAllTenants` alone, and
  refuses a narrower scope as `limes.ErrScopeMismatch`
  ([ADR-0014](adr/0014-bulk-revocation-across-tenants.md)). Deleting a
  client revokes its capabilities in every tenant:

  ```go
  for {
  	res, err := store.RevokeMatching(pgstore.WithAllTenants(ctx), limes.RevokeMatchingRequest{
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

## v0.140.0

- `RevokeMatchingRequest.AllTenants` revokes by `ClientID` or `Session` in
  every tenant, and `RevokeMatchingResult.Tenants` names the tenants a call
  wrote in. Nothing that compiled breaks, but a `BulkRevoker` of your own
  now fails `storetest` until it honours `AllTenants`: give the suite
  `Env.AllTenantsCtx` when the store scopes tenants.

  ```go
  // Before: one tenant at a time, from a list of tenants of your own.
  store.RevokeMatching(ctx, limes.RevokeMatchingRequest{TenantID: tenant, ClientID: id})

  // After: every tenant, in batches.
  store.RevokeMatching(ctx, limes.RevokeMatchingRequest{AllTenants: true, ClientID: id})
  ```

## pgstore/v0.48.0

- `pgstore/migrate/ddl` is new in this release, and its `Version` is a
  function, `ddl.Version()`, not a constant: a release that adds a migration
  then changes its value, never the API. No release before this one has the
  package, so nothing that compiled against a release breaks.

  ```go
  // A test of the consumer's own, holding its vendored copy to pgstore's.
  if len(vendored) != ddl.Version() { /* a step is missing */ }
  ```

## exchange/v0.10.0

- The JWT subject verifier moved to `github.com/oleg-tkachuk/limes/exchange/jwtverify`,
  so package `exchange` links no JOSE library for a consumer that verifies
  subject tokens on a stack of its own
  ([ADR-0010](adr/0010-exchange-jose-subpackage.md)). `exchange.JWTVerifier`
  is `jwtverify.Verifier`, `exchange.JWTVerifierConfig` is `jwtverify.Config`,
  `exchange.NewJWTVerifier` is `jwtverify.New`, and `KeySource`,
  `StaticKeys`, `CachedKeys`, `DefaultAlgorithms`, `TypeAccessToken`,
  `TypeAccessTokenMedia` and `ClaimClientID` keep their names in `jwtverify`.

  ```go
  // Before
  subjects, err := exchange.NewJWTVerifier(exchange.JWTVerifierConfig{
  	Issuer: idp, Keys: exchange.CachedKeys(cache, jwksURL),
  })

  // After
  subjects, err := jwtverify.New(jwtverify.Config{
  	Issuer: idp, Keys: jwtverify.CachedKeys(cache, jwksURL),
  })
  ```

## v0.133.0

- The Biscuit form moved to the module `github.com/oleg-tkachuk/limes/biscuit`,
  so limes no longer requires biscuit-go or protobuf. Tokens do not change.
  - `limes.Attenuate` is `biscuit.Attenuate`.
  - `BiscuitConfig.Form` is required: set it to `biscuit.Form()`. Without it
    `NewVerifier` refuses the config.
  - `IssuerConfig.Biscuit` names the form `Issuer.Biscuit` seals in: set it to
    `biscuit.Form()`. Without it `Issuer.Biscuit` refuses.
- The Biscuit fixtures moved with the form: `testdata/biscuit_vocabulary.json`
  and `testdata/biscuit/` are `biscuit/testdata/biscuit_vocabulary.json` and
  `biscuit/testdata/biscuit/`, under
  `go list -m -f '{{.Dir}}' github.com/oleg-tkachuk/limes/biscuit`. They are
  the cases an SDK in another language tests a copy of, and stay supported:
  this module is their source.

## pgstore/v0.45.0

- pgstore needs PostgreSQL 15 or later. Migration 022 creates
  `limes.capability_lineage`, a view that runs as its caller
  (`security_invoker`), which the meter and the erase read the records
  through; it fails on an earlier PostgreSQL.
- `limes.capability_lineage` keeps no rows of its own, but it has a
  `tenant_id` column. A tool that finds the tables holding a tenant's rows
  in `information_schema.columns` should keep to those whose
  `information_schema.tables.table_type` is `BASE TABLE`. From
  pgstore/v0.47.0, `pgstore.TenantTables` lists them, with what
  `Store.EraseTenant` does to each.

## pgstore/v0.44.0

- The migrations moved to `github.com/oleg-tkachuk/limes/pgstore/migrate`,
  which only a migration job imports, so a binary that serves links neither
  them nor `tern`. `pgstore.Migrate` is `migrate.Run`, `pgstore.MigrateOptions`
  is `migrate.Options`, and `pgstore.ErrSchemaOwner` is `migrate.ErrSchemaOwner`.
  `CheckSchema` and `ReadSchemaVersion` stay in `pgstore`. A database migrated
  before needs nothing: the migrations and the version table are the same.

## v0.131.0

- One token format: every capability is an RFC 9068 access token, typed
  `at+jwt`. `TokenTypeV6`, `TokenTypeV7`, the `TokenType` type,
  `SignerConfig.TokenType` and `VerifierConfig.AcceptTokenTypes` are gone;
  `limes.TokenType` is the constant `"at+jwt"`. A `limes-cap-v6+jwt` token
  is refused with `ErrInvalidToken`: issue it again.
- `IssueRequest.ClientID` and `IssueRequest.Subject.Subject` are required
  (RFC 9068's `client_id` and `sub`), as is `DelegateRequest.Subject.Subject`;
  a request without them is `ErrInvalidRequest`. A delegated child still
  takes its parent's client.
- A signer's kid is in limes's namespace: `jwks.KeyIDPrefix` (`limes:`) and a
  name. `jwks.KeyID` and `GenerateKey` write one; `NewSigner` refuses any
  other, and a verifier refuses a token under any other before it looks a
  key up. Rotate a key under a kid of your own choosing to one under
  `jwks.KeyID`, publishing both until the old tokens expire.

## exchange/v0.2.0

- `JWTVerifierConfig.Keys` is a `KeySource`, where it was a `jwk.Set`:
  `exchange.StaticKeys(set)` for a fixed set, `exchange.CachedKeys(cache,
  url)` over a `jwk.Cache`. Keys the source cannot give are the server's
  failure (500), no longer the token's (`invalid_grant`).

## v0.126.0

- Every identifier is the standard library's `uuid.UUID` (Go 1.27), where it
  was `github.com/google/uuid`'s: `Principal.TenantID`, `Capability.ID`,
  the store interfaces, `storetest` and `metertest`. Import `"uuid"` in
  place of `"github.com/google/uuid"`; a value from code that keeps the old
  package converts as `uuid.UUID(id)`, both being `[16]byte`. `uuid.Nil` is
  `uuid.Nil()`, `uuid.NewV7` returns no error, and the type has no `Scan`,
  `Value` or `Version`.
- The `go` directive is 1.27.0: Go 1.26 no longer builds the module.

## pgstore/v0.40.0

- `WithTenant`, `CheckSchemaScope`, `GetRecord`, `IsRevoked` and every other
  method take the standard library's `uuid.UUID`, as limes v0.126.0 does,
  and the module needs Go 1.27.

## otelmetric/v0.10.0

- The module needs Go 1.27, as limes v0.126.0 does.

## redisreplay/v0.2.0

- The module needs Go 1.27, as limes v0.126.0 does.

## v0.125.0

- `RecordLister.ListRecords` binds its cursor to the tenant too: one handed
  back for another tenant is `ErrCursorMismatch`, where it paged that
  tenant's records on from its id. memstore and pgstore do it, and
  `storetest` checks it, so a lister of your own that ignores the tenant
  fails it. A cursor handed out before the upgrade is refused once: start
  again from the first page.

## v0.124.0

- `RevocationCacheConfig.SyncedTTL` longer than
  `MaxRevocationCacheSyncedTTL` (24 hours) is refused by
  `NewRevocationCache`, where any length was taken. Pass a day or less.

## otelmetric/v0.9.0

- `Instrument`'s attributes are under `limes.`: `outcome` is
  `limes.outcome`, `unit_code` is `limes.unit_code`, and the opt-in
  `tenant_id` is `limes.tenant_id`, as the revocation cache's `kind` became
  `limes.revocation_cache.kind` in v0.8.0. Update the queries that group or
  filter by them.

## otelmetric/v0.8.0

- `InstrumentRevocationCache` renames what an exporter shows:
  `limes.revocation_cache.clears_revoked` is `…narrow_clears`,
  `…last_sync_loss` is `…last_sync_loss_time`, and the attribute `kind` is
  `limes.revocation_cache.kind`. The `lineage` and `synced` gauges carry no
  unit, so Prometheus no longer names them `…_ratio`. Update the dashboards
  and alerts that read them.

## pgstore/v0.39.0

- `pgstore.Syncer` is gone. `RevocationWatcher.Run` opens a
  `limes.RevocationSignal` on each cache that is a
  `limes.RevocationSignalTarget` and closes it when it returns; a cache of
  your own that implemented `Syncer` implements `NewSignal` instead, or is
  only cleared.

## v0.123.0

- `RevocationCache.Listening`, `NotListening` and `Signal`, and
  `RevocationCacheConfig.Signals`, are gone: each revocation signal holds a
  `RevocationSignal` of its own, from `RevocationCache.NewSignal`, with
  `Listening`, `NotListening`, `Revoked(ids...)`, `RevokedAll` and `Close`.
  The cache is synced while at least one signal is open and every open one
  listens, so a cache fed by several watchers needs no count. A watcher's
  `Signal()` naming no id is `RevokedAll()`.
- `ClearRevoked` and `RevocationSignal.Revoked` naming no id drop nothing,
  where `ClearRevoked()` over an upstream naming no lineage was a `Clear`.

## v0.122.0

- `RevokeResult.Before` is `RevokeResult.PriorDisposition`, and
  `RevocationCacheStats.ClearsRevoked` is `RevocationCacheStats.NarrowClears`:
  the old names read as a time and as a count of revoked entries. Rename the
  field; nothing else changes.

## pgstore/v0.38.0

- `(*Store).RevokeBiscuit` returns `(limes.RevokeBiscuitResult, error)` where
  it returned `error`, as `limes.BiscuitRevocationStore` now asks (v0.121.0).

## v0.121.0

- `BiscuitRevocationStore.RevokeBiscuit(ctx, req) error` is
  `RevokeBiscuit(ctx, req) (RevokeBiscuitResult, error)`, as `Store.Revoke`
  returns a `RevokeResult`: `AlreadyRevoked` says the copy had an entry
  before the call, and `Revocation` is the entry that stands, the first as
  before. `_, err := store.RevokeBiscuit(…)` keeps the old behaviour; a store
  of your own returns the result, which `storetest` now checks.

## v0.120.0

- `meter.MinChargeRetention` is 401 days where it was 400: a leap year of
  billing and 35 days of disputes. A `PurgeChargesRequest.OlderThan` of 400
  days is now `ErrInvalidRequest`; pass 401 days or more, or zero for the
  floor itself.

## pgstore/v0.37.0

- `(*Store).RevocationLineage` returns `(limes.RevocationAnswer, error)` where
  it returned `(bool, []uuid.UUID, error)`, as `limes.RevocationLineageLookup`
  now asks (v0.119.0).

## v0.119.0

- `RevocationLineageLookup.RevocationLineage(ctx, id) (bool, []uuid.UUID,
  error)` is `RevocationLineage(ctx, id) (RevocationAnswer, error)`, the
  answer's fields `Revoked` and `Ancestors`: a struct grows a field after
  1.0 where a list of results cannot. A decorator forwarding the method
  returns the answer as it gets it; a store of your own builds
  `RevocationAnswer{Revoked: …, Ancestors: …}`. `storetest` now checks a
  store that implements it.

## pgstore/v0.33.0

- `(*Store).Revoke` returns `(limes.RevokeResult, error)` where it returned
  `error`, as `limes.Store` now asks (v0.113.0).

## v0.113.0

- `Store.Revoke(ctx, req) error` is `Store.Revoke(ctx, req) (RevokeResult,
  error)`. `RevokeResult.AlreadyRevoked` says the capability had a
  revocation entry of its own before the call, and `RevokeResult.Revocation`
  is the entry that stands — the first, as before. A caller that read the
  record before revoking to learn this drops the read: `_, err :=
  records.Revoke(…)` keeps the old behaviour. A `Store` of your own returns
  the result; `storetest.Run` checks it for a first and a repeated revoke.

## pgstore/v0.30.0

- Nothing of pgstore's own API changed: this release follows limes v0.109.0,
  whose one `RevocationCache` answers for capabilities and Biscuit copies.
  `RevocationWatcher.Caches` holds that one cache where it held the two.

## v0.109.0

- `BiscuitRevocationCache`, `BiscuitRevocationCacheConfig` and
  `NewBiscuitRevocationCache` are gone: `RevocationCache` answers
  `IsBiscuitRevoked` too, and one `Clear` drops both kinds of answer.
  `RevocationCacheConfig.Upstream` is `Capabilities`, and its new `Biscuits`
  takes the Biscuit upstream — the same store, if it implements both. Pass
  the one cache as `VerifierConfig.Revocations` and
  `BiscuitConfig.Revocations`; `NewVerifier` refuses a cache without
  `Biscuits` as the latter. `MaxEntries` bounds each kind on its own.

## pgstore/v0.29.0

- `pgstore.DB` asks for `BeginTx(ctx, pgx.TxOptions)` in place of `Begin`:
  the store opens every transaction READ COMMITTED, whatever the database's
  default, so a call that waited on a tenant's erase lock sees the erase.
  `*pgxpool.Pool` and `*pgx.Conn` implement both; a `DB` of your own adds
  `BeginTx`.
- `CheckSchema` refuses a connection that logged in as a superuser, a
  `BYPASSRLS` role or an owner even after `SET SESSION AUTHORIZATION` to a
  runtime role, and `Migrate` stops at a migration it cannot scope to every
  tenant, closing the connection, rather than run it unscoped.

## v0.108.0

- A copy counted under a capability's `Caveats.Copies` stays counted under
  its ancestors once its capability's usage is dropped (`DeleteUsage`,
  `PurgeOrphans`), as its requests and spend do: dropping a child's usage no
  longer frees room in its root's quota. A `UsageStore` of your own keeps
  that count on each capability's counters; metertest checks it.
- pgstore keeps the count in `limes.usage.copies_under` (migration 013, which
  counts the copies already recorded), so admitting a copy reads the chain's
  rows rather than walking the subtree.
- Stop every pgstore older than pgstore/v0.28.3 before `Migrate` applies
  migration 013: it counts the copies recorded so far once, and a copy an
  older store admits after that is never counted under its ancestors.
- `DeleteUsage` of a capability that has delegated restarts its own count
  with its other counters, so the copies of its subtree no longer count
  against its quota; before, they were counted from the subtree each time.

## v0.107.0

- `Store.Insert` refuses a tenant whose usage `EraseTenantUsage` removed with
  `ErrTenantErased`, until `PurgeOrphans` forgets the mark: a record written
  after the erase would be one its revocation missed, metered from nothing.
  memstore and pgstore do it; a `limes.Store` of your own beside a
  `meter.TenantUsageEraser` does too, and metertest checks it.

## v0.106.0

- A capability's quota of Biscuit copies (`Caveats.Copies`) counts the copies
  of every capability delegated from it too, as its request and budget
  ceilings count their requests and spend: a child can no longer multiply
  the copies its root allows. A `UsageStore` of your own checks every
  capability on the chain — the subject's quota from its token, each
  ancestor's from its record — against the copies of its subtree:
  metertest checks it. `CopyQuota.Admits` reads a count against a quota.
  `metertest.CapabilitySpec` gains `Copies` (nil is Unlimited), which an
  `Env.NewCapability` writes into the record.
- pgstore reads an ancestor's quota from its record; a record written before
  v0.104.0 holds none and counts as the zero quota.

## pgstore/v0.27.0

- In `onCharged`, `TxFrom(ctx).Conn()` is nil: the connection is the store's.
  A charge whose transaction `onCharged` ended with SQL of its own (`COMMIT`,
  `ROLLBACK`) is refused with `ErrChargeTransaction` rather than written
  outside it.
- `CheckSchema` refuses a pool whose session role is a superuser, a
  `BYPASSRLS` role or an owner even when it `SET ROLE` to a runtime role, and
  `Migrate` scopes every migration of a run to every tenant, not only the
  first.

## v0.105.0

- `SignerConfig.Key` is a `SealedKey`: wrap a key with `limes.SealKey(key)`
  (an `ed25519.PrivateKey` or a KMS `crypto.Signer`). `SigningKey.Private` is
  a `SealedKey` already, so `SignerConfig{KeyID: k.KeyID, Key: k.Private}`
  is unchanged; code that read the raw `ed25519.PrivateKey` from it uses the
  key it loaded instead. A `SealedKey` is a `crypto.Signer` and prints as
  `[redacted]` however it is reached — under `%p`, from a field of a struct of
  yours, through slog's text handler — which a plain key field could not
  promise.
- dpop: `HMACNoncesConfig.Keys` is a `dpop.NonceKeys`: `dpop.SealNonceKeys(k1,
  k2)` in place of `[][]byte{k1, k2}`. It holds copies of the keys.

## pgstore/v0.26.0

- Migration `012_function_execute.sql` takes EXECUTE on the schema's
  functions — the revocation checks, which read every tenant's revocations,
  and the scope checks the policies call — away from every role, and
  `Migrate` grants it to `MigrateOptions.RuntimeRoles`. A role you grant by
  hand instead needs `GRANT EXECUTE ON ALL FUNCTIONS IN SCHEMA limes`.

## v0.104.0

- Tokens are `limes-cap-v6+jwt`, the one type a verifier accepts:
  `TokenTypeV6` replaces `TokenTypeV5`, and a v5 token is refused. Upgrade
  verifiers and issuers together and reissue the capabilities in flight.
- `Caveats.Copies`, a `CopyQuota` (`max_copies`, `unlimited_copies` on the
  wire), bounds how many Biscuit copies of a capability a meter counts — the
  copies whose blocks set limits of their own. Like `Requests` and `Budget`
  its zero value allows none: a capability that hands out Biscuits whose
  holders attenuate them with request or budget limits needs a quota, or
  `CopyQuota{Unlimited: true}`, or the first such copy is
  `ErrCopyLimitExceeded`. A delegated child's quota is within its parent's.
  `MeterSubject.CopyQuota` carries it to the meter.
- The store's `MaxCopiesPerCapability` is now the most it counts whatever a
  capability allows; the lower of the two applies. A `UsageStore` of your
  own reads the quota from the subject (`CopyQuota().Bounded(bound)`):
  metertest checks it.

## pgstore/v0.24.0

- The transaction `TxFrom` returns in `onCharged` is `onCharged`'s while it
  runs and no longer: kept past it, every call fails with `pgx.ErrTxClosed`.
  `Commit` and `Rollback` on it return `ErrChargeTransaction` (a savepoint
  from `Begin` is still yours to end), and calls from several goroutines are
  taken one at a time.

## v0.103.0

- `ErrUnitCodeMismatch` matches `ErrConflict`, and `ErrorResponse` answers it
  409 instead of 500: an amount or a child in another unit than the record's
  is a well-formed request that disagrees with what is on record. A caller
  mapping `ErrConflict` maps it too.

## v0.102.0

- `meter.SumChargesRequest`: `ByOp`, `ByActor` and `ByCapability` are gone;
  name the dimensions in `GroupBy` instead (`ChargeDimensionOp`,
  `ChargeDimensionActor`, `ChargeDimensionCapability`, and the new
  `ChargeDimensionTenant`). The request holds slices now, so it no longer
  compares with `==`.
- `SumCharges` sums every tenant's charges with `AllTenants` (exactly one of
  `TenantID` and `AllTenants`), keeps charges by `Filter`, and fills
  `ChargeSum.TenantID` when grouped by tenant. A `LedgerReader` of your own
  does the same, and refuses a sum across tenants under one tenant's scope:
  metertest checks it.

## pgstore/v0.23.0

- Migration `011_ledger_indexes.sql` indexes the charges by tenant and time
  and by time; run `Migrate` before deploying. `Ledger.SumCharges` with
  `AllTenants` runs only under `WithAllTenantsReadOnly` or `WithAllTenants`.

## pgstore/v0.22.0

- Migration `010_force_row_security.sql` makes row-level security bind the
  schema's owner too; run `Migrate` before deploying. A query of yours run as
  the owner sees no tenant's rows unless it sets `limes.all_tenants` (or
  `limes.tenant_id`); `Migrate` sets it for its own run.
- `CheckSchema` returns `ErrRuntimeRole` for a pool of a superuser, a role
  with `BYPASSRLS` or the schema's owner: serve as a role that owns nothing,
  as `MigrateOptions.RuntimeRoles` grants it.

## otelmetric/v0.3.0

- `Instrument` loses its type parameter: it takes and returns the
  non-generic `meter.UsageStore` of v0.101.0.

## v0.101.0

- `meter.UsageStore`, `meter.Meter` and `meter.Charged` lose their type
  parameter: `onCharged` is `func(ctx, meter.Charged) error`, and
  `Charged.Tx` is gone. The store's transaction travels in the context
  `onCharged` receives, read through the store's own accessor —
  `pgstore.TxFrom(ctx)` for pgstore. A store of your own puts its
  transaction in that context and exports an accessor for it.
- memstore: `UsageStore`, `UsageStoreConfig` and `Ledger` lose their type
  parameter, and `UsageStoreConfig.TxFactory` is gone: `onCharged` runs on
  the caller's context. `metertest.Env` and `metertest.Run` lose theirs too.

## pgstore/v0.21.0

- `UsageStore` implements the non-generic `meter.UsageStore`: `onCharged`
  reads the charge's transaction with `TxFrom(ctx)` instead of
  `Charged.Tx`.

## otelmetric/v0.2.0

- `Instrument` takes and returns a `meter.UsageStore`, which moved out of
  `limes` in v0.100.0, and forwards the optional interfaces of the package
  `meter`.

## v0.100.0

- Metering moved out of `limes` into the package
  `github.com/oleg-tkachuk/limes/meter`: `UsageStore`, `Meter`,
  `MeterReader`, `TenantBudgets`, `UsageHousekeeping`, `CopyUsageReader`,
  `LedgerReader`, `TenantUsageEraser`, `TenantBudgetLister`,
  `AllTenantBudgets`, `HeldCopy`, `HousekeepingLimit`, `ValidateExternalRef`
  and every metering request, receipt, record and constant. Import `meter`
  and rename `limes.X` to `meter.X`; nothing else about them changed.
- The meter's own sentinels moved with it: `ErrChargeNotFound`,
  `ErrReservationNotFound`, `ErrUsageNotFound`, `ErrTenantBudgetNotFound`,
  `ErrTenantBudgetVersionMismatch`, `ErrExternalRefConflict`,
  `ErrSettleConflict`, `ErrCopyLimitExceeded`, `ErrCounterOverflow` and
  `ErrRefundExceedsCharge` are `meter.Err…`, in the same families. The
  refusals `ErrorResponse` answers — `ErrRequestLimitExceeded`,
  `ErrBudgetExceeded`, `ErrTenantBudgetExceeded` — and `ErrInvalidAmount`
  stay in `limes`.
- `MeterSubject`, the ceilings and `Nanos` stay in `limes`; `MeterSubject`
  gains `Validate` and `ValidateAmounts`, and `AllPages` walks any paged list
  as `AllByPrincipal` does.

## pgstore/v0.20.0

- The usage store, ledger and erasers implement the contracts of the package
  `meter` (see v0.100.0): their methods take and return `meter.X` where they
  took `limes.X`.

## v0.99.0

- `Store.Insert` refuses a child whose budget names another unit than its
  parent's, as `ErrUnitCodeMismatch`: metering a child spends from every
  ancestor, so a lineage counts in one unit. `Delegate` already kept it so;
  a record written by `Insert` directly must too. A `Store` of your own
  refuses it as well: storetest checks it.

## v0.98.0

- A `UsageStore` counts at most a quota of Biscuit copies per capability —
  `DefaultMaxCopiesPerCapability` unless configured — and `Bump`, `Charge`
  and `Reserve` refuse a call that would count one more as
  `ErrCopyLimitExceeded`. memstore and pgstore take
  `MaxCopiesPerCapability` in their configuration; a store of your own
  enforces a quota too, and passes it to metertest as
  `Env.MaxCopiesPerCapability`.

## v0.97.0

- `Settle` of a reservation already settled for another amount returns
  `ErrSettleConflict` (an `ErrConflict`, answered 409) instead of the first
  receipt; the same amount is still a replay. A `UsageStore` of your own
  does the same: metertest checks it.

## v0.96.0

- `Issuer.Biscuit` refuses a capability whose caveats do not allow
  `OpShare`, as `ErrOpNotAllowed`, as `Delegate` does: a Biscuit form is
  handed on offline. Grant `OpShare` to a capability you mint a Biscuit for.

## v0.95.0

- `Reserve` refuses a capability whose record has already expired as
  `ErrExpired`, rather than holding until an instant already past. A
  `UsageStore` of your own does the same: metertest checks it.

## v0.94.0

- `EraseTenantUsage` marks the tenant erased: `Bump`, `Charge`, `Reserve`
  and `SetTenantBudget` for it return `ErrTenantErased`, which matches
  `ErrRevoked`, until `PurgeOrphans` finds none of its records left. A
  `TenantUsageEraser` of your own does the same: metertest checks it.

## pgstore/v0.18.0

- Migration `009_erased_tenants.sql` adds `limes.erased_tenants`; run
  `Migrate` before deploying. An erase and the meter calls of its tenant
  take a transaction-scoped advisory lock on the tenant, with the class key
  `0x6c696d65`, which no lock of your own should use.

## v0.93.0

- A Biscuit copy is counted, and its revocation kept, under its capability
  and revocation id together: a block's id alone can turn up on another
  capability's chain. `CopyUsageRequest` takes a `CapabilityID`, which
  `Validate` requires; `BiscuitRevocationStore.GetBiscuitRevocation` takes
  the `BiscuitCopy` that `RevokeBiscuit` took, not a revocation id. A store
  of your own keys its copy counters and copy revocations by both, keeps
  `IsBiscuitRevoked` matching an id under any capability, and drops only the
  purged or erased capability's own: metertest and storetest check it.

## pgstore/v0.17.0

- Migration `008_copy_key_by_capability.sql` keys `limes.copy_usage` on
  `(capability_id, revocation_id)` and `limes.biscuit_revocations` on
  `(revocation_id, capability_id)`; run `Migrate` before deploying.
  `Store.GetBiscuitRevocation` takes a `limes.BiscuitCopy`.

## v0.92.0

- `InsertRequest.Validate` refuses a capability with no `ExpiresAt` as
  `ErrInvalidRequest`, a child under any parent included: no verifier
  accepts one and `Purge` never removes it. A `Store` of your own calls
  `Validate` to pass storetest, which no longer expects such a record kept
  by `EraseTenantRecords` or counted by a census.

## v0.91.0

- `TokenTypeV4` is gone: a verifier accepts `limes-cap-v5+jwt` alone, and a
  `Signer` mints it by default. A v4 token still in flight is refused as
  `ErrInvalidToken`; issue it again. A `VerifierConfig.AcceptTokenTypes` or
  `SignerConfig.TokenType` naming v4 is refused when the verifier or signer
  is built.

## pgstore/v0.16.0

- `pgstore.SchemaVersion.Applied` and `Expected` are `int`, not `int32`, the
  width of tern's column: compare them as Go's integers.

## v0.90.0

- `RevokeRequest`, `DeleteUsageRequest`, `ChargeByRefRequest`,
  `PurgeOrphansRequest` and `ReleaseExpiredRequest` gain `Validate`, which
  the stores and the contract suites now apply: `Revoke` and `DeleteUsage`
  naming the zero id, and `ChargeByRef` with no capability or an empty
  reference, are `ErrInvalidRequest` — they were `ErrNotFound`, a no-op and
  `ErrChargeNotFound`. A `Store` or `UsageStore` of your own calls `Validate`
  to pass storetest and metertest. `HousekeepingLimit` resolves a batch's
  `Limit` as `PageLimit` resolves a page's.

## v0.89.0

- `ChargeBucketUnspecified` is gone: it meant what `ChargeBucketNone` does,
  which is now the zero value. The buckets are numbered shortest first —
  none, hour, day, week, month — so a bucket stored or sent as its number
  reads differently; store and send its name (`String`).

## v0.88.0

- `ListTenantBudgetsRequest.ThresholdPct float64` →
  `ThresholdBasisPoints int`, in hundredths of a percent, 0..the new
  `BasisPointsPerWhole`: write `ThresholdBasisPoints: 2900` for
  `ThresholdPct: 29`, `110` for `1.1`. `ExactPercent` is gone; a
  `UsageStore` of your own compares `spent × BasisPointsPerWhole ≥ max ×
  ThresholdBasisPoints` in integers wide enough for the product.

## v0.87.0

- `dpop.ProofRequest` → `dpop.SignRequest`: the request `dpop.Sign` takes,
  named for it, as `dpop.Request` is what `Verify` and `Check` take.
- `VerifierConfig.AcceptBiscuit`, `MeterCopies` and `BiscuitRevocations` →
  `VerifierConfig.Biscuit *BiscuitConfig`, with `Revocations` and
  `MeterCopies`: write `Biscuit: &limes.BiscuitConfig{Revocations: r}` for
  `AcceptBiscuit: true, BiscuitRevocations: r`. Nil refuses Biscuits, as
  `AcceptBiscuit: false` did; a copy setting with no Biscuit to apply to can
  no longer be written.

## v0.86.0

- `CachedRevocationChecker`, `NewCachedRevocationChecker` →
  `RevocationCache`, `NewRevocationCache`; `CachedBiscuitRevocationChecker`,
  `NewCachedBiscuitRevocationChecker` → `BiscuitRevocationCache`,
  `NewBiscuitRevocationCache`, named as their configs
  (`RevocationCacheConfig`, `BiscuitRevocationCacheConfig`) already were.

## v0.85.0

- `Instrument`, `InstrumentOption`, `WithMeterProvider` and
  `WithTenantAttribute` → the nested module
  `github.com/oleg-tkachuk/limes/otelmetric`: `otelmetric.Instrument`,
  `otelmetric.Option`, `otelmetric.WithMeterProvider`,
  `otelmetric.WithTenantAttribute`, so limes no longer resolves
  OpenTelemetry. Require the module and change the qualifier; the metric
  names and attributes are unchanged, and the instrumentation scope is now
  `github.com/oleg-tkachuk/limes/otelmetric`.

## v0.84.0

- A problem body's `type` named the kind of refusal — `invalid-token` for an
  expired token as for a forged one → it ends with the refusal's `Reason`
  (`…/problems/expired`), and the body carries the reason as `reason`. A
  refusal with no reason keeps its family's type. Match `reason`, or the new
  type URIs, where a client matched the old ones.

## v0.83.0

- `Capability.Copies` → `VerifiedCapability.Copies()`, and
  `Capability.BiscuitRoot` is gone: both were what the verifier read from a
  token, never part of a capability a store records or an issuer mints.
  Read a Biscuit copy's limits from what `Verify` returned; its
  `MeterSubject` carries them as before.

## v0.82.0

- `ErrExternalRefConflict` matched `ErrInvalidRequest`, though the request
  was well formed → it matches the new `ErrConflict`, as `ErrAlreadyExists`
  and `ErrTenantBudgetVersionMismatch` now do. A caller mapping
  `ErrInvalidRequest` to 400 maps `ErrConflict` to 409.

## v0.81.0

- `Capability.Generation`, `IssueRequest.Generation` and `DefaultGeneration`
  are gone: nothing read them. Drop the field from your requests; record a
  rotation in your own records if you kept one.
- `OpPresign`, `OpTag`, `OpSearch` and `OpEmbed` are gone, and
  `Caveats.Validate` refuses their bare names: a domain's operations are the
  consumer's. Name yours namespaced (`"index:search"`, `"model:embed"`) and
  declare `EffectRead` for those that read. A v4 token naming a bare one
  still verifies and is checked against that name, now with a consumer
  operation's effect — a write unless declared — and a child delegated from
  it cannot name it: issue such roots again under the new names.
- `AgentPrincipal.ParentAgentID` → `ParentRunID`, and `Delegate` sets it to
  the parent agent's `RunID`: a request naming another run is
  `ErrInvalidRequest`, where any value was signed as given. Leave it unset. A
  v5 token spells it `parent_run_id`; a v4 one keeps `parent_agent_id`.
- New token type `TokenTypeV5`: v4 without `limes_gen` and without those
  four operations. A verifier accepts v4 and v5 by default; a `Signer` mints
  v4 until `SignerConfig.TokenType` is `TokenTypeV5`. Upgrade every verifier
  first — one with `AcceptTokenTypes` set adds `TokenTypeV5` — then switch the
  issuers.

## pgstore/v0.15.0

- `UsageStore.ListTenantBudgets` under `WithTenant`, or with no scope,
  returned the rows that scope let through, read as every tenant's → it is
  `limes.ErrScopeMismatch`, as `CountRecords` across tenants is: list under
  `WithAllTenantsReadOnly` (or `WithAllTenants`).

## v0.80.0

- `UsageHousekeeping.ReleaseExpired` released the hold of a revoked
  capability before it expired → it releases expired holds only, as before
  v0.78.0: a revoked capability's hold can still be settled, since a cost
  already incurred belongs in the ledger (`StoredMeterSubject`). The hold
  still expires with its capability at the latest.

## v0.79.0

- `inspect.VerifySignature` answered a `pub` that is no Ed25519 public key
  with `limes.ErrInvalidToken`, as if the token were forged → it is the new
  `inspect.ErrInvalidKey`, and the token is not blamed: match it to tell a
  wrong key from a bad token.

## v0.78.0

- A reservation lasted `ReserveRequest.TTL` whatever its capability did → it
  expires when its capability's record does, if that comes sooner, and
  `UsageHousekeeping.ReleaseExpired` also releases the holds of a revoked
  capability or of one under a revoked ancestor. A `UsageStore` of your own
  does both to pass `metertest`; a caller that relied on a hold outliving its
  capability reserves again under a live one.

## v0.77.0

- `Caveats.UnitCode`, `SetTenantBudgetRequest.UnitCode` and
  `TenantBudget.UnitCode` → `BudgetCeiling.UnitCode`: the unit is part of the
  ceiling it bounds. Write `Budget: BudgetCeiling{Max: m, UnitCode: "USD"}` for
  `Budget: BudgetCeiling{Max: m}, UnitCode: "USD"`, `Ceiling:
  BudgetCeiling{Max: m, UnitCode: u}` in `SetTenantBudgetRequest`, and read
  `b.Ceiling.UnitCode` for `b.UnitCode`. The token's `unit_code` claim is
  unchanged.
- A `BudgetCeiling` that lets anything be spent — a `Max` above zero, or
  `Unlimited` — and names no unit is `ErrInvalidRequest`, in caveats
  `ErrInvalidCaveats`: an unlimited budget in no unit was accepted before,
  though nothing could be charged against it. A capability that is never
  charged takes the zero budget; one with an unlimited budget names its unit.
  `Verify` refuses a token carrying such a budget as `ErrInvalidToken`.
- A tenant ceiling no longer takes its unit from the first charge: a
  `SetTenantBudget` ceiling that lets anything be spent names it, and the zero
  ceiling keeps the tenant's. A tenant with no ceiling still counts in the
  unit of its first charge or hold, which `GetTenantBudget` reports as an
  unlimited ceiling in it. A tenant ceiling set without a unit under
  v0.75.0–v0.76.0 reads back with none; set it again with one.
- `BudgetCeiling.String` names the unit (`25 USD`, `unlimited USD`). A copy's
  budget (`CopyCeiling.Budget`, `HeldCopy.Budget`) carries its capability's
  unit; `Attenuation.Budget` names none, and one that does is refused.

## v0.76.0

- Caveats with a `Budget.Max` above zero and no `UnitCode` validated, though
  nothing could be charged against them → `Caveats.Validate` refuses them as
  `ErrInvalidCaveats`: `Issue` mints no such capability and `Verify` refuses
  a token carrying one as `ErrInvalidToken`. Name the unit the budget is in;
  an unlimited budget, or none, still needs no unit. A delegated child that
  names no unit still takes its parent's.

## v0.75.0

- `DefaultUnitCode`, `NormalizeUnitCode` and the rule that an empty unit is
  USD → removed: an empty `UnitCode` names no unit, and none is assumed.
  `Charge`, `Reserve` and `Settle` of a capability with no `UnitCode` are
  `ErrInvalidRequest`; `Bump` still counts its requests. A delegated child
  that names no unit takes its parent's, and `Narrows` matches an empty unit
  only with another. `SetTenantBudget` with no `UnitCode` leaves a new
  tenant's unit to its first charge or hold. Issue every capability that is
  charged with its `UnitCode` — `"USD"` where an empty one meant it — and
  read a unit with `IsUnitCode` in place of `NormalizeUnitCode`. Rows a
  store already holds keep the unit they were counted in.

## v0.74.0

- Constants were prefixed by their type's name or a stem of it → every
  enum's constants carry their type's name in full. `OverrunReject`,
  `OverrunRecord`, `OverrunUnspecified` → `OverrunPolicyReject`,
  `OverrunPolicyRecord`, `OverrunPolicyUnspecified`; `PrincipalUser`,
  `PrincipalAgent`, `PrincipalService`, `PrincipalUnspecified` →
  `PrincipalTypeUser`, `PrincipalTypeAgent`, `PrincipalTypeService`,
  `PrincipalTypeUnspecified`; `TenantBudgetsAll`, `TenantBudgetsAtOrPast`,
  `TenantBudgetsUnlimited`, `TenantBudgetsUnspecified` →
  `TenantBudgetFilterAll`, `TenantBudgetFilterAtOrPast`,
  `TenantBudgetFilterUnlimited`, `TenantBudgetFilterUnspecified`. Values
  and wire spellings are unchanged.

## v0.73.0

- `ChargeBucketNone` was the zero `ChargeBucket` → the zero is
  `ChargeBucketUnspecified`, which sums the period whole as `ChargeBucketNone`
  does, and `ChargeBucketNone` is a value of its own after `ChargeBucketWeek`.
  A request that names no bucket reads the same; a `switch` over buckets
  handles `ChargeBucketUnspecified` beside `ChargeBucketNone`, and a bucket
  stored by number is renumbered (`0` → `5`).

## v0.71.0

- `SigningKey.Public`, a field that could disagree with `Private` → a method
  that derives the public key from `Private`. Read `key.Public()` where you
  read `key.Public`, and build a `SigningKey` from its `KeyID` and `Private`
  alone.

## pgstore/v0.12.0

- `Store.EraseTenant` took `onErased func(ctx, tx pgx.Tx) error`, which saw
  nothing of the erase → it takes `onErased func(ctx, e pgstore.Erased) error`:
  `e.Tx` is the transaction the callback had, and `e.Result` the
  `EraseTenantResult` the call returns. Read `e.Tx` where you read `tx`.

## v0.70.0

- `ErrMalformedAuthorization` matched no family → it matches
  `ErrInvalidRequest`, as the type tree documents. `ErrorResponse` still
  answers it 400; a mapping of your own that sends `ErrInvalidRequest` to 500
  must test `ErrMalformedAuthorization` first.

## v0.68.0

- `PurgeResult`, `PurgeOrphansResult`, `ReleaseExpiredResult`,
  `EraseTenantResult`, `RecordCount` and `ChargeSum` compared with `==` →
  they no longer compare, as the other results already did not, so a field
  added to one in 1.x breaks no caller. Compare their fields, or use
  `reflect.DeepEqual`; build them with field names, not positionally.

## v0.67.0

- `Meter.Charge` and `Meter.Settle` took `onCharged func(ctx, tx TX) error`,
  which saw nothing of the charge → they take
  `onCharged func(ctx, c limes.Charged[TX]) error`: `c.Tx` is the handle the
  callback had, and `c.Receipt` the receipt the call returns, its `ChargeID`
  among it. Change `func(ctx context.Context, tx MyTx) error` to
  `func(ctx context.Context, c limes.Charged[MyTx]) error` and read `c.Tx`.

## pgstore/v0.11.0

- `UsageStore.Charge` and `UsageStore.Settle` take
  `onCharged func(ctx, c limes.Charged[pgx.Tx]) error`, as `limes.Meter` now
  does: the transaction is `c.Tx`.

## v0.66.0

- `dpop.VerifierConfig.MatchPathOnly` let a request to the root path `/`
  match any proof whose `htu` path ends in a slash → the root matches only a
  proof for the root. A server mounted under a prefix takes no proofs at its
  root under `MatchPathOnly`.
- A resource with a dot segment encoded twice (`%252e%252e`, or `..%252f`)
  was allowed → it is refused as one encoded once is: `Caveats.Validate`
  refuses a prefix or URI with one, and no request naming one matches a
  restricted capability.
- `inspect.Decode` and `inspect.VerifySignature` read a token of any length →
  one longer than `inspect.MaxTokenBytes` (`limes.DefaultMaxTokenBytes`) is
  `ErrInvalidToken`, unread.

## v0.65.0

- `jwks.NewRemote` accepted any URL and its client followed any redirect →
  it refuses a `RemoteConfig.URL` that is not `https`, and a fetch refuses a
  redirect to another scheme or host. A `RemoteConfig.Client` of your own is
  copied, not changed, and its `CheckRedirect` still runs within that.
- `ReleaseRequest` named the reservation alone, and `Meter.Release` released
  any → it takes `Subject`, the `MeterSubject` that reserved, as
  `SettleRequest` does: a request without one is `ErrInvalidRequest`, and a
  reservation of another capability, tenant or Biscuit copy is left held.
  memstore and `metertest` check it.
- `Store.Insert` accepted a child with no expiry under a parent that has one
  → it refuses one as `ErrDelegationTooWide`. `storetest` checks it.

## pgstore/v0.10.0

- `UsageStore.Release` released any reservation, in any tenant → it needs
  `ReleaseRequest.Subject`, runs in that subject's tenant and leaves a
  reservation of another capability or copy held, as `Settle` does.
- `Store.Insert` accepted a child with no expiry under a parent that has one,
  which `Purge` then failed on → it refuses one as `ErrDelegationTooWide`.

## pgstore/v0.9.0

- A call that writes under a `Config.Scope` of your own that sets
  `limes.all_tenants` to `read` was left to the schema's policies: `Insert`,
  `Revoke` and `RevokeBiscuit` failed with PostgreSQL's row-level security
  error, the rest answered as rows not found or — `Purge`, `ReleaseExpired`,
  `PurgeOrphans`, `DeleteUsage` and the erasers among them — succeeded and
  changed nothing, and a write to a
  tenant that `limes.tenant_id` named beside it went through → each returns
  `limes.ErrScopeMismatch` before it writes, as under
  `WithAllTenantsReadOnly`. Scope a call that writes to a tenant, or to every
  tenant with `on`.

## pgstore/v0.7.0

- `Migrate(ctx, pool)` → `Migrate(ctx, conn, MigrateOptions{})`: it takes one
  `*pgx.Conn` (from a pool, `pool.AcquireFunc` and `c.Conn()`), and
  `MigrateOptions.RuntimeRoles` grants the roles the stores serve as, which
  replaces your own `GRANT` statements.

## pgstore/v0.2.0

- A call whose request names its tenant (`Insert`, `ListByPrincipal`, `Bump`,
  `Charge`, `Reserve`, `Settle`, `GetTenantBudget`, `SetTenantBudget`) under
  a scope that did not admit that tenant answered as if its rows were not
  there — `ErrInvalidRequest` for a child whose parent it could not see,
  `ErrTenantBudgetNotFound`, an empty page, or PostgreSQL's row-level
  security error → it returns `limes.ErrScopeMismatch` and changes nothing.
  Scope each call to the tenant its request names. A `Config.Scope` of your
  own gets no check unless `Config.CheckScope` names one.

## v0.57.0

- `Store.Insert` accepted a child with more than `MaxDelegationDepth`
  ancestors on record, and `Delegate` refused it only once it was written →
  `Insert` refuses one as `ErrDelegationTooDeep` and writes nothing.
  `storetest` checks it; `metertest.Env.NewCapability` returns the store's
  error, which a check now matches.
- `TenantBudgetFilter`'s zero was `TenantBudgetsAll` → it is
  `TenantBudgetsUnspecified`, which lists every tenant as before, and
  `TenantBudgetsAll`, `TenantBudgetsAtOrPast` and `TenantBudgetsUnlimited`
  each moved up by one. Code that names the filters needs no change; a store
  that passes the filter on as a number maps it by name instead.

## v0.55.0

- `Store.Insert` accepted a child in another tenant than its parent → it
  refuses one as `ErrDelegationTooWide`, as `Delegate` always has.
  `storetest` checks it.

## v0.53.0

- `Store.Insert` accepted any record → it refuses a capability with no id or
  with a parent not on record (`ErrInvalidRequest`), and a child expiring
  after its parent (`ErrDelegationTooWide`). `storetest` checks it.

## v0.52.0

- `CopyCeiling.Requests`, `CopyCeiling.Budget` (values) → pointers, nil when
  the block set none.
- `Reservation.Copies` (`[]CopyCeiling`) → `[]HeldCopy`.
- `CopyUsageReader.CopyUsage(ctx, revocationIDs)` returning `[]CopyUsage` →
  `CopyUsage(ctx, CopyUsageRequest)` returning `CopyUsageResult`.

## v0.51.0

- `Delegate` from a parent without `OpShare` → `ErrOpNotAllowed`.

## v0.50.0

- `RefundReceipt.RefundedAmount` → `RefundReceipt.Amount`.
- `BiscuitCopy.Limits` → `BiscuitCopy.Copies`.

## v0.49.0

- `jwks.RemoteConfig.MaxDocumentBytes` (`int64`) → `int`.

## v0.48.0

- `Settle` under other Biscuit copies than the reservation was made under →
  `ErrReservationNotFound`.

## v0.47.0

- `ListTenantBudgetsRequest.UnlimitedOnly` → `Filter`: `TenantBudgetsAll`,
  `TenantBudgetsAtOrPast` or `TenantBudgetsUnlimited`. `ThresholdPct` applies
  only to `TenantBudgetsAtOrPast`.

## v0.46.0

- `Issue`, `Delegate` returning the token → `IssueResult{Capability, Token}`.
- `Issuer.Biscuit(ctx, capability)` → `Biscuit(ctx, BiscuitRequest{Capability})`
  returning `BiscuitResult{Token}`.

## v0.45.0

- `ChargeRecord`, `ChargeReceipt`, `RefundReceipt`, `BumpReceipt`,
  `TenantBudget`, `Usage`, `Revocation`, `Problem`, `Principal` and
  `AgentPrincipal` compared with `==` → no longer comparable; compare fields,
  or use `reflect.DeepEqual` in tests.

## v0.44.0

- `Verify`'s refusals matched the request sentinel of their cause → they match
  only `ErrInvalidToken` or `ErrBiscuitAttenuation`.

## v0.43.0

- `BiscuitCopy` fields → methods; only `Verifier.BiscuitCopy` makes one.
- `RevokeBiscuitRequest` takes the copy as `Copy`.

## v0.42.0

- `LoadPrivateKey`, `Signer.KeyID`, `Op.Mutating`, `SigningKey.Generated`,
  `dpop.ErrNoReplayCache` → removed.
- `dpop.ProofRequest.Now` → `IssuedAt`.
- `StoredMeterSubject(ctx, Store, id)` → takes a `RecordReader`, which `Store`
  embeds.
- `ChargeRecord` gains `TenantID`, `Op`, `Actor` and `At`; a store must keep
  them, and `metertest` checks it.
- Metric outcomes `cap_exceeded`, `tenant_exceeded`, `limit_exceeded` →
  `budget_exceeded`, `tenant_budget_exceeded`, `request_limit_exceeded`.

## v0.41.0

- `metertest.CapabilitySpec` gains `Tenant`, which `Env.NewCapability` must
  honour for a tenant from `NewTenant`.

## v0.40.0

- `dpop.Request`, `dpop.ReplayEntry` gain `Holder`, which `Verify` requires.
- `MemoryReplayCacheConfig.EntriesPerKey` → `EntriesPerHolder`;
  `DefaultReplayEntriesPerKey` → `DefaultReplayEntriesPerHolder`;
  `dpoptest.ReplayEnv.EntriesPerKey` → `EntriesPerHolder`.
- Proofs past `MaxProofBytes` and jtis past `MaxJTIBytes` → refused.

## v0.39.0

- `ErrInvalidAmount` and `NormalizeUnitCode`'s error → also match
  `ErrInvalidRequest`.
- A charge taking a counter past `MaxNanos` → `ErrCounterOverflow`.
- `SetTenantBudget` → refuses what `SetTenantBudgetRequest.Validate` refuses,
  a nil `TenantID` included.

## v0.38.0

- `Bump`, `Charge`, `Reserve`, `Settle` for a capability not on record →
  `ErrNotFound`. `metertest` checks it.

## v0.37.0

- `jwks.HandlerConfig.MaxAge` of zero meant `max-age=0` → it means
  `DefaultMaxAge`; set `Revalidate` for `max-age=0`.
- `VerifierConfig.AcceptTokenTypes` (`[]string`) → `[]TokenType`;
  `TokenTypeV4` is a `TokenType`. An empty `AcceptTokenTypes` accepts
  `TokenTypeV4`, and an empty `KeyIssuers` binds no kid, where both were
  refused.
- `dpop.Sign(key, req)` → `dpop.Sign(ctx, key, req)`, and `ProofRequest.Now` is
  required. `NewMemoryReplayCache` refuses `EntriesPerKey` above `MaxEntries`.
- `LoadKey(path, kid)` → `LoadKey(path)`, deriving the kid; set
  `SigningKey.KeyID` to name it otherwise.
- `memstore.NewUsageStore` without `Records` → refused.

## v0.36.0

- `StandardVerifier`, `NewStandardVerifier` → `Verifier`, `NewVerifier`.
- `UsageHousekeeping.Delete` → `DeleteUsage`.
- `BumpReceipt.Requests` → `RequestCount`.
- `Revocation.Cascade` → `Cascaded`.
- `dpop.ReplayEntry.Expires` → `ExpiresAt`.
- `DefaultPurgeLimit` → `DefaultHousekeepingLimit`.

## v0.35.0

- `ListTenantBudgets` returning `Page[TenantBudgetSummary]` →
  `Page[TenantBudget]`.
- `TenantBudgetSummary`, `ListTenantBudgetsRequest.ExcludeInactive`,
  `memstore.LedgerEntry`, `UsageStore.Ledger` → removed.

## v0.34.0

- `ErrCopyCountersNotMetered` matched `ErrBiscuitAttenuation` and
  `ErrInvalidToken` → it matches neither; `ErrorResponse` answers it with 500
  and `ReasonOf` gives it no reason.

## v0.33.0

- Reasons `invalid_biscuit`, `source_not_allowed` (`ReasonInvalidBiscuit`,
  `ReasonSourceNotAllowed`) → `biscuit_attenuation`, `source_ip_not_allowed`
  (`ReasonBiscuitAttenuation`, `ReasonSourceIPNotAllowed`). `ErrAuthSchemeMismatch` gives
  `auth_scheme_mismatch`, `dpop.ErrNonceRequired` gives `nonce_required`.
- `Record.MeterSubject()` → `StoredMeterSubject(ctx, store, capabilityID)`.
- `Signer.Sign` → unexported; mint tokens with `Issuer`.
- An empty `VerifyRequest.Audience`, a malformed `Attenuation`, an
  out-of-range budget ceiling and `Delegate` from a Biscuit parent matched
  `ErrAudienceMismatch`, `ErrBiscuitAttenuation` or `ErrDelegationTooWide` →
  they match `ErrInvalidRequest`.

## v0.32.0

- `metertest.Env.NewCapability(parent, budget)` →
  `NewCapability(CapabilitySpec)`, carrying the parent and both ceilings.

## v0.31.0

- `ParseAuthorization` with an empty header or a foreign scheme:
  `ErrMalformedAuthorization` → `ErrNoCredentials`. `Verify` with an empty
  token: `ErrInvalidToken` → `ErrNoCredentials`.
- `UsageHousekeeping.Delete(ctx, capabilityID)` → takes a `DeleteUsageRequest`.
- `MeterReader.ChargeByRef(ctx, capabilityID, externalRef)` → takes a
  `ChargeByRefRequest`.

## v0.30.0

- `jwks.NewStatic(map)` → `jwks.NewStatic(StaticConfig)` returning
  `(*Static, error)`.
- `jwks.Static.SetKey` → returns an error.
- `jwks.NewHandler` → returns `*Handler`.

## v0.29.0

- `MinPurgeRetention` (5m) → `MaxLeeway + PurgeClockSkew` (6m); a
  `PurgeRequest.ExpiredFor` between 5m and 6m is refused.
- `NewStandardVerifier` → refuses several `TrustedIssuers` with neither
  `KeyIssuers` nor `SharedKeys`, and with both.

## v0.28.0

- `memstore.New[TX]()` → `memstore.NewStore(StoreConfig)`, returning an
  error; `Store` is no longer generic.
- `memstore.NewUsage[TX](records)` →
  `memstore.NewUsageStore[TX](UsageStoreConfig[TX])`, returning an error.
- `WithClock`, `WithTxFactory` → the `Now` and `TxFactory` config fields.

## v0.27.0

- `Caveats.MaxRequests`, `Caveats.MaxBudgetAmount` (zero meant unlimited) →
  `Caveats.Requests` (`RequestCeiling`) and `Caveats.Budget`
  (`BudgetCeiling`), each with `Max` and `Unlimited`. **A zero ceiling now
  allows nothing**: set `Unlimited` where zero meant no limit.
- `CopyCeiling` likewise; `Attenuation` takes `*RequestCeiling` and
  `*BudgetCeiling`, nil for none.
- `MeterSubject.Requests()`, `Budget()` → return ceilings.
- `TenantBudget`, `SetTenantBudgetRequest` take `Ceiling` (`BudgetCeiling`);
  a tenant charged before any ceiling counts `Unlimited`.
- `metertest.Env.NewCapability` → takes a `BudgetCeiling`.
- `TokenTypeV3` → `TokenTypeV4`; v3 tokens are refused.

## v0.26.0

- `Signer.Sign` → returns `ErrInvalidCaveats` or `ErrInvalidRequest` for what
  `Issue` would refuse; a token whose caveats do not validate is
  `ErrInvalidToken`.

## v0.25.0

- `Delegate` from a parent read from a Biscuit → always
  `ErrDelegationTooWide`, not only when it carries limits of its own.
- `Issuer.Biscuit(ctx, Capability)` → takes a `VerifiedCapability`.

## v0.23.0

- `Store.IsRevoked` → must answer for `MaxDelegationDepth` ancestors and
  return `ErrDelegationTooDeep` for a deeper chain; `Delegate` returns
  `ErrDelegationTooDeep` past that depth.
- `VerifierConfig.Leeway` above `MaxLeeway` (5m) → refused.
- `PurgeRequest.ExpiredFor` of zero → means `MinPurgeRetention`; a shorter
  non-zero retention is `ErrInvalidRequest`.

## v0.22.0

- `Principal`, `AgentPrincipal`, `Caveats`, `Capability` with json tags → no
  json tags; `json.Marshal` of them no longer writes the claim format. A store
  that serialises them keeps a representation of its own; `storetest` checks
  every field survives it.

## v0.21.0

- `ListByPrincipal` with a principal type outside user, agent and service, and
  `Verify` with a scheme other than Bearer or DPoP → `ErrInvalidRequest`.
- `OverrunReject` = 0, `OverrunRecord` = 1 → `OverrunReject` = 1,
  `OverrunRecord` = 2; remap a stored or serialised numeric policy. The zero
  value still rejects.

## v0.20.0

- `AuthorizeRequest{VerifyRequest: …, CheckRequest: …}` →
  `AuthorizeRequest{Verify: …, Check: …}`; `req.Token` → `req.Verify.Token`.
- `Limit` on `ListByPrincipalRequest`, `ListTenantBudgetsRequest`,
  `ListReservationsRequest`, and `PageLimit` → `int`.
- `PeriodEnd` on `TenantBudget` and `SetTenantBudgetRequest` → `time.Time`,
  zero for none.

## v0.19.0

- `CopyCeiling.MaxBudget`, `Attenuation.MaxBudget`, `MeterSubject.MaxBudget` →
  `MaxBudgetAmount`.
- `ChargeReceipt.Spent` → `SpentAmount`.
- `ChargeRecord.Refunded`, `RefundReceipt.Refunded` → `RefundedAmount`.
- `ChargeRecord.ChargeID` → `ID`.
- `RevokeRequest.ID` → `CapabilityID`.
- `Attenuation.Planes` → `Audience`.
- `Reason` on `RevokeRequest`, `Revocation`, `RevokeBiscuitRequest`,
  `BiscuitRevocation` → `Note`.
- `Op` on `ChargeRequest`, `ReserveRequest`, `Reservation` (`string`) → `Op`.
- `NormaliseUnitCode` → `NormalizeUnitCode`.
- `TenantBudgetSummary.UtilisationPct` → removed; call `TenantBudget.Utilization()` (the summary's `Budget` field).
- `RefundRequest{ChargeID}` with no `Amount` (a full refund) →
  `ErrInvalidRequest`; pass `Remaining: true`.

## v0.18.0

- `storetest.Env` → holds func fields and is no longer comparable.

## v0.17.0

- `CapabilityID`, `TenantID`, `MaxRequests`, `MaxBudget`, `UnitCode`, `Copies`
  on `BumpRequest`, `ChargeRequest`, `ReserveRequest`, `SettleRequest` →
  `Subject`, from `VerifiedCapability.MeterSubject()` or
  `Record.MeterSubject()`.

## v0.16.0

- `Meter.GetCharge`, `ChargeByRef`, `GetReservation`, `ListReservations`,
  `GetUsage` → `MeterReader`'s.
- `Bump` → returns `BumpReceipt`.
- `Release(ctx, reservationID)` → takes a `ReleaseRequest`.
- `ListReservations` → takes `ListReservationsRequest`, returns
  `Page[Reservation]`.
- `PurgeOrphans`, `ReleaseExpired` → take `PurgeOrphansRequest`,
  `ReleaseExpiredRequest` and return results.
- `metertest.Env` gains an optional `DeleteCapability`.
- `TenantBudgetCursor`, `DecodeTenantBudgetCursor` → removed; a store writes
  its `ListTenantBudgets` cursor in a format of its own.

## v0.15.0

- `Store.Insert(ctx, capability, issuedBy)` →
  `Insert(ctx, InsertRequest{Capability, IssuedBy})`.
- `Store.Get` → removed; `GetRecord` returns the capability with its record.
- `IssuerConfig.Store` → an `IssuanceStore`, which every `Store` satisfies.
- `Store.ListByPrincipal`, `TenantBudgets.ListTenantBudgets`, and
  `CapabilityLister`, `TenantBudgetLister` → return `(Page[…], error)`.
- `Store.PurgeExpired(ctx, expiredFor)` →
  `Store.Purge(ctx, PurgeRequest{ExpiredFor, Limit})` returning
  `PurgeResult`; it removes the expired records as well, and refuses a
  negative retention or limit.

## v0.14.0

- `NewEd25519Signer` → `NewSigner(SignerConfig{KeyID, Key})`;
  `IssuerConfig.Signer` is `*Signer`. A key that needs the context implements
  `ContextSigner`.
- `StandardVerifier.Verify`, `Authorize` returning a `Capability` → return
  `VerifiedCapability`; read it with its `Capability` method, a copy.
- `DelegateRequest.Parent` → a `VerifiedCapability`.
- The `Verifier` interface → removed; use `*StandardVerifier`, or declare the
  interface the calling code needs.

## v0.13.0

- `dpop.Verifier{…}` → `dpop.NewVerifier(VerifierConfig)`, fields unexported;
  it takes its clock from its own `VerifierConfig.Now`.
- `NewMemoryReplayCache(maxEntries, opts…)` → takes `MemoryReplayCacheConfig`;
  `NewHMACNonces` → takes `HMACNoncesConfig`; both return an error.
  `ReplayCacheOption`, `WithEntriesPerKey` → removed.
- `jwks.Handler(keys, maxAge)` → `jwks.NewHandler(HandlerConfig)` returning
  `(http.Handler, error)`. `NewHandler` and `NewRemote` refuse a negative
  max-age, interval or document bound instead of using the default.
- `LoadOrGenerateKey` → `LoadKey(path, kid)`, which never generates; call
  `GenerateKey` for an ephemeral key.
- `NewStandardVerifier` → refuses a non-nil empty `KeyIssuers`.
- `ListByPrincipalRequest.Validate`, the new
  `ListTenantBudgetsRequest.Validate` → refuse a negative `Limit`, the latter
  also a `ThresholdPct` outside 0..100; the suites check a store refuses them.
- `NewCachedRevocationChecker(upstream, ttl, opts…)`, `NewCachedBiscuitRevocationChecker(upstream, ttl, opts…)`
  → take `RevocationCacheConfig`, `BiscuitRevocationCacheConfig` and return an
  error. `CacheOption`, `WithMaxEntries` → removed; a negative TTL no longer
  disables the cache.

## v0.12.0

- `Decode`, `VerifySignature` → `inspect.Decode`, `inspect.VerifySignature`
  in `github.com/oleg-tkachuk/limes/inspect`.
- `GenerateEd25519Keypair` → `GenerateKey`, returning `(SigningKey, error)`.
- `ValidateAmount(n)` → `n.Validate()`.
- `WithCacheClock`, `HasDotSegment`, `IsBiscuit`, `MediaTypeProblem`,
  `WWWAuthenticateHeader`, `TokenType` → unexported.
- `WithMetering`, `MeteringOption` → `Instrument`, `InstrumentOption`.

## v0.11.0

Proof of possession moved to `github.com/oleg-tkachuk/limes/dpop`, and lost
its `DPoP` prefix:

- `DPoPVerifier` → `dpop.Verifier`, checking a `dpop.Request`.
- `DPoPProof` → `dpop.Proof`, its `Proof` field `JWT`.
- `NewDPoPProof`, `DPoPProofRequest` → `dpop.Sign`, `dpop.ProofRequest`.
- `KeyThumbprint` → `dpop.Thumbprint`.
- `DPoPNonces`, `HMACNonces`, `NewHMACNonces`, `MemoryReplayCache`,
  `NewMemoryReplayCache`, `ReplayCache`, their options and constants →
  `dpop.Nonces`, `dpop.HMACNonces`, …; `DPoPHeader` → `dpop.Header`,
  `DPoPNonceHeader` → `dpop.NonceHeader`, `DefaultDPoPWindow` →
  `dpop.DefaultWindow`, `DPoPSigningAlgorithms` → `dpop.SigningAlgorithms`.
- `ErrDPoPRequired`, `ErrDPoPInvalid`, `ErrDPoPReplayed`,
  `ErrDPoPNonceRequired` → `dpop.ErrRequired`, `ErrInvalid`, `ErrReplayed`,
  `ErrNonceRequired`, matching `ErrProofOfPossession` only as the verifier
  returns them.
- `ReplayCache.Seen` → takes a `ReplayEntry`, returns `(bool, error)`;
  `Nonces.Valid` → returns `(bool, error)`.
- `PublicJWK`, `DPoPJWK`, `AccessTokenHash`, `DPoPTokenType` → unexported.

## v0.10.0

The key-set API moved to `github.com/oleg-tkachuk/limes/jwks`:

- `MarshalJWKS`, `ParseJWKS`, `JWKSHandler` → `jwks.Marshal`, `jwks.Parse`,
  `jwks.Handler`.
- `RemoteJWKSResolver`, `NewRemoteJWKSResolver`, `RemoteJWKSConfig` →
  `jwks.Remote`, `jwks.NewRemote`, `jwks.RemoteConfig`.
- `StaticKeyResolver`, `NewStaticKeyResolver` → `jwks.Static`,
  `jwks.NewStatic`.
- `DeriveKeyID` → `jwks.KeyID`.
- `ErrUnknownKID`, `ErrJWKSUnavailable` → `jwks.ErrUnknownKID`,
  `jwks.ErrUnavailable`.
- `JWKSDocument`, `JWK` → unexported.

## v0.9.0

- `ErrDPoPRequired`, `ErrDPoPInvalid`, `ErrDPoPReplayed`,
  `ErrDPoPNonceRequired`, `ErrAuthSchemeMismatch` matched
  `ErrInvalidSignature` → match `ErrProofOfPossession` or
  `ErrUnauthenticated`.
- `ErrChargeNotFound`, `ErrReservationNotFound`, `ErrUsageNotFound`,
  `ErrTenantBudgetNotFound` → also match `ErrNotFound`.
- `ErrInvalidSignature` → `ErrInvalidToken`; `ReasonInvalidSignature` →
  `ReasonInvalidToken`, reason `invalid_signature` → `invalid_token`.
- `ErrJWKSUnavailable`, and any `KeyResolver` error but `ErrUnknownKID`,
  matched `ErrInvalidSignature` → match it no longer; `ErrorResponse` answers
  a custom resolver's failure with 500.

## v0.8.0

- `Issue`, `Delegate`, `Verify` → refuse a principal whose `Type` is not
  `PrincipalUser`, `PrincipalAgent` or `PrincipalService`, or that carries
  `Agent` without being an agent. `Delegate` needs a `Subject` with a `Type`.

## v0.6.0

- `CachedRevocationChecker.Invalidate` → removed; call `Clear` on a revocation
  signal.

## v0.5.0

- `NewDPoPProof` upper-cased the method → writes it as given, and the verifier
  compares it exactly.
- `ReplayCache.Seen` → takes the proof key's thumbprint.

## v0.4.0

- `AuthorizationDetail`, `AuthorizationDetailsType` and their marshal and
  parse functions → removed; caveats travel in `limes_caveats`, with `Ops` as
  `"ops"`.
- `Capability.OnBehalfOf`, `Capability.Actor`, `Capability.Authority`,
  `Actor`, `MaxDelegationDepth` → removed; a delegated token's `sub` is its own principal's subject.
- `IssuerMetadata`, `NewIssuerMetadata`, `MetadataURL`, `DiscoverIssuer`,
  `MetadataHandler`, `RevocationHandler`, `RevocationConfig`,
  `IntrospectionHandler`, `Introspection`, `IntrospectionConfig`,
  `NewIntrospection`, `ClientAuthenticator`, `ClientAuthorizer`,
  `ErrClientUnauthorized`, `ErrInvalidMetadata`, `StandardVerifier.Introspect`
  → removed.
- Token `typ` `limes-cap-v2+jwt` → `limes-cap-v3+jwt`; v2 tokens are refused.
- `ValidateResourceIndicator` → removed; `Issue` and `Delegate` accept any
  non-empty audience entry.

## v0.3.0

- `AllowedUnitCodes`, `IsAllowedUnitCode` → `IsUnitCode`.
- Token `alg` `EdDSA` → `Ed25519`; `EdDSA` is refused.
- `Issue`, `Delegate` → refuse an audience that is not an absolute URI.
- `Issue` → requires `Subject.Subject`; a delegated token's `sub` is the root
  capability's subject (`Capability.OnBehalfOf`), and `Delegate` with no
  subject delegates to the parent's own.
- `MustLoadOrGenerate` → `LoadOrGenerateKey`, returning a `SigningKey`;
  derived kids are thumbprints (`DeriveKeyID`).
- `NewDPoPProof` → takes a `DPoPProofRequest`.
- `Signer.Sign`, `Issuer.Biscuit` → take a `context.Context`.
- Claim `limes_caveats` → `authorization_details`, `Caveats.Ops` encoded as
  `"actions"`.
- Token `typ` → `limes-cap-v2+jwt`, earlier ones refused;
  `Caveats.MaxRequests` → `int64`; `Nanos` encodes as a JSON string.
- `Verify` → checks the DPoP proof of a key-bound token itself, and refuses
  one at a verifier with no DPoP configured; the `Verifier` interface gains
  `Authorize`.
- `Verify` → takes a `VerifyRequest`; `DPoPVerifier.Check`, `DPoPRequest` and
  the `AudiencePlane*` constants → removed; `MatchPathOnly` → on
  `DPoPVerifier`.
- `WithMetering` → takes options and returns an error; `MeteringStore` →
  unexported; `Refund` → returns a `RefundReceipt`; `ChargeReceipt` gains
  `TenantID` and `UnitCode`; `tenant_id` is opt-in via `WithTenantAttribute`.
