# Changelog

Every release of the pgstore module, newest first, from its release notes. Written by the release pull request before each release is tagged (.github/ci/release-plan.ts); not edited by hand.

## [0.58.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.57.0...pgstore%2Fv0.58.0) (2026-11-01)

### Features

* **pgstore:** lease requests beside budget ([e2a3bb5](https://github.com/oleg-tkachuk/limes/commit/e2a3bb52cf7a635d8dca0852933f0c94813f30e8))

### Dependencies

* require limes v0.146.0

## [0.57.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.56.0...pgstore%2Fv0.57.0) (2026-11-01)

### Features

* **pgstore:** add the lease columns and their expiry index ([188c673](https://github.com/oleg-tkachuk/limes/commit/188c6738084b5f8d6788b504da17a138f5fbc545))
* **pgstore:** lease a capability budget of its chain ([71a1dd0](https://github.com/oleg-tkachuk/limes/commit/71a1dd0c294106e4d021fb77d80ac69888c2090c))

### Dependencies

* require limes v0.145.0

## [0.56.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.55.0...pgstore%2Fv0.56.0) (2026-10-31)

### Features

* **pgstore:** lock a tenant's aggregate after the counters ([9719714](https://github.com/oleg-tkachuk/limes/commit/97197148ca4ead19655e6f09bda85f95fd0968aa))

### Bug Fixes

* **ci:** keep one blank line between changelog entries ([2731a80](https://github.com/oleg-tkachuk/limes/commit/2731a809301ea41dfa670e5db138a93b01488c13))

### Performance Improvements

* **pgstore:** lock and write a meter call's counters in batches ([df88713](https://github.com/oleg-tkachuk/limes/commit/df887136d37546cc57122dec2724d497662b9f32))
* **pgstore:** read a meter call's prelude in one batch ([089da44](https://github.com/oleg-tkachuk/limes/commit/089da44aef6a9c70838514970da27efa950a0739))
* **pgstore:** send a scope's statements with the call's first ([abd480f](https://github.com/oleg-tkachuk/limes/commit/abd480f512e5970292dc4e3cb344294e76a6f918))

## [0.55.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.54.1...pgstore%2Fv0.55.0) (2026-10-31)

### Features

* **pgstore:** list every tenant's capability records under the all-tenants scope ([5f5c254](https://github.com/oleg-tkachuk/limes/commit/5f5c254c7512e588219a4324545fc9256d43862f))

### Dependencies

* require limes v0.144.0

## [0.54.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.54.0...pgstore%2Fv0.54.1) (2026-10-31)

### Bug Fixes

* **pgstore:** implement pgx.BatchResults.Tx ([b575e73](https://github.com/oleg-tkachuk/limes/commit/b575e7329a05aa36255b8fd1528302734042ac1c))

## [0.54.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.53.0...pgstore%2Fv0.54.0) (2026-10-31)

### Features

* **pgstore:** release the holds of one revoked capability ([50c8359](https://github.com/oleg-tkachuk/limes/commit/50c8359cdc5fb5bcc765b0f4da48978b51c112c0))

### Dependencies

* **nested:** require limes v0.143.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([d5ea9e8](https://github.com/oleg-tkachuk/limes/commit/d5ea9e8020b4e8fabdcd05828fca8d62e416d349))

## [0.53.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.52.1...pgstore%2Fv0.53.0) (2026-10-31)

### Features

* **pgstore:** list a tenant's open reservations ([4cf503f](https://github.com/oleg-tkachuk/limes/commit/4cf503f001aece65e093026488b8e135544394c8))
* **pgstore:** release the holds of revoked matches in every tenant ([27a87f6](https://github.com/oleg-tkachuk/limes/commit/27a87f6bf19bb4003b266ccd8e2f9ff534a025e9))

### Dependencies

* **nested:** require limes v0.142.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([0f40a43](https://github.com/oleg-tkachuk/limes/commit/0f40a43e956e53350e48a54252f842f8f4251f44))

## [0.52.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.52.0...pgstore%2Fv0.52.1) (2026-10-30)

### Dependencies

* **nested:** require limes v0.141.1 in redisreplay, pgstore, otelmetric, exchange and biscuit ([9c8bb9c](https://github.com/oleg-tkachuk/limes/commit/9c8bb9c8362dca5ae232b11bb8780d42934f0a5f))

## [0.52.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.51.0...pgstore%2Fv0.52.0) (2026-10-30)

### Features

* **pgstore:** filter the census and the listing by revocation selectors ([ad13ba6](https://github.com/oleg-tkachuk/limes/commit/ad13ba6bc5eaa90b343894296875acf66db9c626))
* **pgstore:** notify a statement's revocations in parts ([0c88439](https://github.com/oleg-tkachuk/limes/commit/0c88439894dc69d1b2b32dab8be6ae066f42303f))
* **pgstore:** release the holds of a bulk revocation at once ([711b282](https://github.com/oleg-tkachuk/limes/commit/711b2825f079c1e96268173944d1344cfa659687))
* **pgstore:** revoke an agent run's capabilities ([e83054d](https://github.com/oleg-tkachuk/limes/commit/e83054d02b908589954e02d03aa874d3360cb75e))

### Bug Fixes

* **pgstore:** list each record with its session ([fad314a](https://github.com/oleg-tkachuk/limes/commit/fad314a1e57b213333d0f691cf007122e526df3f))

### Dependencies

* **nested:** require limes v0.141.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([12e7580](https://github.com/oleg-tkachuk/limes/commit/12e75800dd62f8ea793bd9a7c6b3ea3c790367d9))

## [0.51.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.50.1...pgstore%2Fv0.51.0) (2026-10-30)

### Features

* **pgstore:** revoke by client or session across tenants ([364fc0b](https://github.com/oleg-tkachuk/limes/commit/364fc0b53237a13af760ea5bf4e399ab845c6a23))

### Dependencies

* **nested:** require limes v0.140.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([970e6a6](https://github.com/oleg-tkachuk/limes/commit/970e6a602e8e608b0ea377ce6da8be4ea4b2c223))

## [0.50.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.50.0...pgstore%2Fv0.50.1) (2026-10-29)

### Dependencies

* **nested:** require limes v0.139.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([54109c4](https://github.com/oleg-tkachuk/limes/commit/54109c4018282ced403f4f4a0706ab07d648e4e1))

## [0.50.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.49.2...pgstore%2Fv0.50.0) (2026-10-29)

### Features

* **pgstore:** give every error a code ([50cd6e7](https://github.com/oleg-tkachuk/limes/commit/50cd6e771a7744366fece890d58b75256fbd371e))

## [0.49.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.49.1...pgstore%2Fv0.49.2) (2026-10-29)

### Dependencies

* **nested:** require limes v0.138.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([ee2c884](https://github.com/oleg-tkachuk/limes/commit/ee2c8849e451c1141db9eab4ef2aebad879ed021))

## [0.49.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.49.0...pgstore%2Fv0.49.1) (2026-10-29)

### Dependencies

* **nested:** require limes v0.137.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([fda9bc1](https://github.com/oleg-tkachuk/limes/commit/fda9bc1252d26c8f6693373b2cde5c279b5f8bcf))

## [0.49.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.48.1...pgstore%2Fv0.49.0) (2026-10-29)

### Features

* **pgstore:** revoke every capability of a subject, a client or a session ([a40cc6d](https://github.com/oleg-tkachuk/limes/commit/a40cc6d7a80a005bb6242b9ac160d992693ee6d8))

### Dependencies

* **nested:** require limes v0.136.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([62f5fa7](https://github.com/oleg-tkachuk/limes/commit/62f5fa782dea695e1091522eea0f6be92993105c))

## [0.48.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.48.0...pgstore%2Fv0.48.1) (2026-10-29)

### Dependencies

* **nested:** require limes v0.135.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([9afd08b](https://github.com/oleg-tkachuk/limes/commit/9afd08b7ea891b967105f91e1694802c213cd8a6))

## [0.48.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.47.1...pgstore%2Fv0.48.0) (2026-10-29)

### ⚠ BREAKING CHANGES

* **pgstore:** ddl.Version is ddl.Version(). No released pgstore has
  package ddl, so no build against a release breaks.

### Features

* **pgstore:** hand the migrations to a consumer's tool without tern ([5086dce](https://github.com/oleg-tkachuk/limes/commit/5086dced6563dbf752b0df65589e7f94ee235963))
* **pgstore:** record the capability a subject token was exchanged for ([c9f0777](https://github.com/oleg-tkachuk/limes/commit/c9f0777b9c1cdb320713bb83bff3f3da096a6a6b))

### Bug Fixes

* **pgstore:** keep migrate.Step a type of its own ([3b9aefd](https://github.com/oleg-tkachuk/limes/commit/3b9aefde46042afb62bcc33d4ae8b0e0e3424202))
* **pgstore:** make ddl.Version a function ([7c839fa](https://github.com/oleg-tkachuk/limes/commit/7c839fa71f78692727d8d45a2702c2323684f0db))

## [0.47.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.47.0...pgstore%2Fv0.47.1) (2026-10-29)

### Dependencies

* **nested:** require limes v0.134.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([f043a34](https://github.com/oleg-tkachuk/limes/commit/f043a348f3ef97df23b295aeb0e05703fcbf639b))

## [0.47.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.46.1...pgstore%2Fv0.47.0) (2026-10-29)

### Features

* **pgstore:** list the tables that hold a tenant's rows ([a78f4ae](https://github.com/oleg-tkachuk/limes/commit/a78f4ae8d1ce3b44a9b8d4a0f84c488bbe7b7028))

## [0.46.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.46.0...pgstore%2Fv0.46.1) (2026-10-29)

### Dependencies

* **nested:** require limes v0.133.1 in redisreplay, pgstore, otelmetric, exchange and biscuit ([1f03a8e](https://github.com/oleg-tkachuk/limes/commit/1f03a8e7dfdd589f553b87bb53d0e1b2566c7701))

## [0.46.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.45.2...pgstore%2Fv0.46.0) (2026-10-29)

### Upgrading

* **pgstore:** `pgstore.NewReplayCache` keeps DPoP proof ids in `limes.dpop_seen_proofs`, which migration 023 creates. A deployment that kept them in a table of its own can switch to it and drop that table: a proof id lives for the proof window alone, so the new table can start empty. Switch every replica at once, or require server nonces across the switch, so a proof accepted by the old table cannot be replayed once to the new one. docs/guide.md says which replay cache to pick.

### Features

* **pgstore:** add a DPoP replay cache over PostgreSQL ([da42827](https://github.com/oleg-tkachuk/limes/commit/da428276f0f6825a39667ddb5111ab7ec555719e))

## [0.45.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.45.1...pgstore%2Fv0.45.2) (2026-10-29)

### Dependencies

* **nested:** require limes v0.133.0 in redisreplay, pgstore, otelmetric and exchange ([d5373bc](https://github.com/oleg-tkachuk/limes/commit/d5373bc126382d781533d0c9f83fbb2793a9b365))

## [0.45.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.45.0...pgstore%2Fv0.45.1) (2026-10-29)

### Dependencies

* **nested:** drop biscuit-go and protobuf from the nested modules' graphs ([570efde](https://github.com/oleg-tkachuk/limes/commit/570efde387f5daaba18fdd52ec6cdafdbf888f17))

## [0.45.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.44.0...pgstore%2Fv0.45.0) (2026-10-29)

### Requirements

* **pgstore:** needs PostgreSQL 15 or later: migration 022 creates `limes.capability_lineage` with `security_invoker`, which fails on an earlier PostgreSQL (docs/migration.md)

### Features

* **pgstore:** read the records through a lineage view in the meter and the erase ([b50d4af](https://github.com/oleg-tkachuk/limes/commit/b50d4afb22564cb5fe0f683bbdae359d57b88761))

### Dependencies

* **pgstore:** run the test container from a module of its own ([ce46685](https://github.com/oleg-tkachuk/limes/commit/ce4668509152803201ace71845628efdb751db14))

## [0.44.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.43.1...pgstore%2Fv0.44.0) (2026-10-29)

### ⚠ BREAKING CHANGES

* **pgstore:** move the migrations to pgstore/migrate

### Features

* **pgstore:** move the migrations to pgstore/migrate ([7b3e884](https://github.com/oleg-tkachuk/limes/commit/7b3e884119e89f97ff83d8249aa64199486580c1))

## [0.43.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.43.0...pgstore%2Fv0.43.1) (2026-10-28)

### Dependencies

* **nested:** require limes v0.132.1 in redisreplay, pgstore, otelmetric and exchange ([629ddf8](https://github.com/oleg-tkachuk/limes/commit/629ddf8dad552d2cb97c2d7130d1b35b43866354))

## [0.43.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.42.3...pgstore%2Fv0.43.0) (2026-10-28)

### Features

* **pgstore:** record issuance in the capability's tenant ([0995c96](https://github.com/oleg-tkachuk/limes/commit/0995c9665aab64f0ab2e9888619ddb6ea80c7dc9))

## [0.42.3](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.42.2...pgstore%2Fv0.42.3) (2026-10-28)

### Dependencies

* **nested:** require limes v0.132.0 in redisreplay, pgstore, otelmetric and exchange ([d104778](https://github.com/oleg-tkachuk/limes/commit/d1047787a3f4f763e193a4ad77bad24fc69ab375))

## [0.42.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.42.1...pgstore%2Fv0.42.2) (2026-10-28)

### Bug Fixes

* **pgstore:** key a tenant's advisory locks by a 64-bit hash ([1a9441f](https://github.com/oleg-tkachuk/limes/commit/1a9441fc5c96d348afe1934a9f39f34f1197f80e))

### Dependencies

* **nested:** require limes v0.131.2 in redisreplay, pgstore, otelmetric and exchange ([216e112](https://github.com/oleg-tkachuk/limes/commit/216e112da792b285bc127de73bc050494c805240))

## [0.42.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.42.0...pgstore%2Fv0.42.1) (2026-10-28)

### Dependencies

* **nested:** require limes v0.131.1 in redisreplay, pgstore, otelmetric and exchange ([13fde12](https://github.com/oleg-tkachuk/limes/commit/13fde12c521612b0347f6f9e493031824b9370b1))

## [0.42.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.41.2...pgstore%2Fv0.42.0) (2026-10-27)

### Features

* **pgstore:** persist whom a capability acts for ([3665889](https://github.com/oleg-tkachuk/limes/commit/3665889571b8896bbc82b970af8c8c5b554f7bac))

### Dependencies

* **nested:** require limes v0.131.0 in redisreplay, pgstore, otelmetric and exchange ([66075e5](https://github.com/oleg-tkachuk/limes/commit/66075e53b124aaf011f2a9e89a30dadfe95b9f79))

## [0.41.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.41.1...pgstore%2Fv0.41.2) (2026-10-27)

### Dependencies

* **nested:** require limes v0.130.0 in redisreplay, pgstore, otelmetric and exchange ([e5bb6f3](https://github.com/oleg-tkachuk/limes/commit/e5bb6f30613958e3e31da96687713404b19f8164))

## [0.41.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.41.0...pgstore%2Fv0.41.1) (2026-10-27)

### Dependencies

* **nested:** require limes v0.129.0 in redisreplay, pgstore, otelmetric and exchange ([21409b6](https://github.com/oleg-tkachuk/limes/commit/21409b62c3629c3a77063cf71cdc27928dc40541))

## [0.41.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.40.1...pgstore%2Fv0.41.0) (2026-10-27)

### Features

* **pgstore:** persist a capability's client ([3cf4a2f](https://github.com/oleg-tkachuk/limes/commit/3cf4a2ff9b28886769143d9c8c32eb7abfaa623c))
* **pgstore:** persist resource name prefixes ([9a7e563](https://github.com/oleg-tkachuk/limes/commit/9a7e5630ad3910a008be95c8da9e63b2a16810de))

### Dependencies

* **nested:** require limes v0.128.0 in redisreplay, pgstore, otelmetric and exchange ([aed9664](https://github.com/oleg-tkachuk/limes/commit/aed966419b3fc73000d172ad5cb9002412e52991))

## [0.40.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.40.0...pgstore%2Fv0.40.1) (2026-10-27)

### Dependencies

* **nested:** require limes v0.127.0 in redisreplay, pgstore, otelmetric and exchange ([3d8c272](https://github.com/oleg-tkachuk/limes/commit/3d8c2725dcdf02d0a22b63f595bc378946108feb))

## [0.40.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.39.4...pgstore%2Fv0.40.0) (2026-10-27)

### ⚠ BREAKING CHANGES

* every uuid.UUID in the API is the standard library's,
  where it was github.com/google/uuid's, and the go directive is 1.27.0.

### Code Refactoring

* use the standard library's uuid ([df229dc](https://github.com/oleg-tkachuk/limes/commit/df229dcc24b7c5f2a8a949c11619040f165d84f2))

### Dependencies

* **nested:** require limes v0.126.0 in redisreplay, pgstore and otelmetric ([76e4da9](https://github.com/oleg-tkachuk/limes/commit/76e4da98fd7bab9481656c875f4f37d1a3c62ba9))

## [0.39.4](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.39.3...pgstore%2Fv0.39.4) (2026-10-25)

### Bug Fixes

* **pgstore:** bound a record listing's walk to its revoking ancestor ([b54a28f](https://github.com/oleg-tkachuk/limes/commit/b54a28ff5f88b112c1c10485f2a43b9ffffc09ee))
* **pgstore:** refuse PurgeCharges under a scope of no tenant ([9ff5d6a](https://github.com/oleg-tkachuk/limes/commit/9ff5d6af4a428b8d466914d0288601d9520c5298))

## [0.39.3](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.39.2...pgstore%2Fv0.39.3) (2026-10-25)

### Dependencies

* **nested:** require limes v0.125.1 in redisreplay, pgstore and otelmetric ([1787c9e](https://github.com/oleg-tkachuk/limes/commit/1787c9ebac5a24a4f7eb9950bacc12ca7ba90321))

## [0.39.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.39.1...pgstore%2Fv0.39.2) (2026-10-25)

### Bug Fixes

* **pgstore:** queue delegations behind a waiting revoke ([b24f1b2](https://github.com/oleg-tkachuk/limes/commit/b24f1b2a3dc8fc14b2e06a8f22eb6d33ef460083))
* **pgstore:** run every migration read committed ([1da63d0](https://github.com/oleg-tkachuk/limes/commit/1da63d024cb336a7d9a8d6e1c08056f050a41587))

### Dependencies

* **nested:** require limes v0.125.0 in redisreplay, pgstore and otelmetric ([419627b](https://github.com/oleg-tkachuk/limes/commit/419627b723b8fbeaf7b74d9986ca493ed41b5ada))

## [0.39.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.39.0...pgstore%2Fv0.39.1) (2026-10-25)

### Bug Fixes

* **pgstore:** bind a record listing's cursor to its tenant ([a24bc4c](https://github.com/oleg-tkachuk/limes/commit/a24bc4cd8c08bf8740e93d5a7f5a3ce27c73a67a))
* **pgstore:** hold a metered lineage for key share ([78636df](https://github.com/oleg-tkachuk/limes/commit/78636df5f065071d146ade33cb34e6e282c4d154))
* **pgstore:** hold a revoke's target for no key update ([00178ff](https://github.com/oleg-tkachuk/limes/commit/00178fff9526403ace92bf00c52be191e6c34325))
* **pgstore:** notify a revocation the writer cannot see ([629960a](https://github.com/oleg-tkachuk/limes/commit/629960afd06affec69b44453eca8e5b8150ff69b))

### Dependencies

* **nested:** require limes v0.124.0 in redisreplay, pgstore and otelmetric ([65a08f7](https://github.com/oleg-tkachuk/limes/commit/65a08f75110e9b564f4f3d1144fe8438f4f5a8c8))

## [0.39.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.38.0...pgstore%2Fv0.39.0) (2026-10-24)

### ⚠ BREAKING CHANGES

* **pgstore:** open a RevocationSignal per watcher run

### Features

* **pgstore:** open a RevocationSignal per watcher run ([6458f86](https://github.com/oleg-tkachuk/limes/commit/6458f864d7d06b54cf52f20229aeb9b80e5f53ae))

### Dependencies

* **nested:** require limes v0.123.0 in redisreplay, pgstore and otelmetric ([1fd8ec0](https://github.com/oleg-tkachuk/limes/commit/1fd8ec03f49bf5ca86871046b5101cd27784fc0d))

## [0.38.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.37.0...pgstore%2Fv0.38.0) (2026-10-24)

### ⚠ BREAKING CHANGES

* rename RevocationCacheStats.ClearsRevoked to NarrowClears
* rename RevokeResult.Before to PriorDisposition
* **pgstore:** report the standing entry from RevokeBiscuit

### Features

* **pgstore:** report the standing entry from RevokeBiscuit ([1d82062](https://github.com/oleg-tkachuk/limes/commit/1d820622fafa8d70c8e6808576ac3b0f5ec57665))

### Code Refactoring

* rename RevocationCacheStats.ClearsRevoked to NarrowClears ([f73211c](https://github.com/oleg-tkachuk/limes/commit/f73211c3414b84f271de3d1e8e757a99bfa3c91b))
* rename RevokeResult.Before to PriorDisposition ([0799984](https://github.com/oleg-tkachuk/limes/commit/0799984f056c20f201add1b4b61dad82f3117ea9))

### Dependencies

* **nested:** require limes v0.120.0 in redisreplay, pgstore and otelmetric ([afe2c54](https://github.com/oleg-tkachuk/limes/commit/afe2c541c0ea3613de7147177365ccfa1591d7e6))
* **nested:** require limes v0.121.0 in redisreplay, pgstore and otelmetric ([a6d6a5f](https://github.com/oleg-tkachuk/limes/commit/a6d6a5f608ab522e405499a766b3e328aab1b1e2))
* **nested:** require limes v0.122.0 in redisreplay, pgstore and otelmetric ([aca6cf2](https://github.com/oleg-tkachuk/limes/commit/aca6cf2da604388650c7b8fac15166af5d3158f6))

## [0.37.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.36.0...pgstore%2Fv0.37.0) (2026-10-24)

### ⚠ BREAKING CHANGES

* **pgstore:** return a limes.RevocationAnswer from RevocationLineage

### Features

* **pgstore:** return a limes.RevocationAnswer from RevocationLineage ([433fc91](https://github.com/oleg-tkachuk/limes/commit/433fc910afabca8a644de883a0357dd89344459f))

### Dependencies

* **nested:** require limes v0.119.0 in redisreplay, pgstore and otelmetric ([0e69094](https://github.com/oleg-tkachuk/limes/commit/0e69094f0dbc708719b7cc3805fe5b759ad2af1d))

## [0.36.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.35.1...pgstore%2Fv0.36.0) (2026-10-24)

### Features

* **pgstore:** purge charges past their retention by their time's index ([d5faa0c](https://github.com/oleg-tkachuk/limes/commit/d5faa0ca570f4cea70e50c77c8c46ed9176620a0))

### Dependencies

* **nested:** require limes v0.118.0 in redisreplay, pgstore and otelmetric ([b17ed0b](https://github.com/oleg-tkachuk/limes/commit/b17ed0b2d33abdf0d527d563de09dfb1725b3e64))

## [0.35.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.35.0...pgstore%2Fv0.35.1) (2026-10-24)

### Bug Fixes

* **pgstore:** find a watcher connection that died without a word ([99db3c8](https://github.com/oleg-tkachuk/limes/commit/99db3c8534e53b31a44b30e6d9c49a32fe5da910))
* **pgstore:** read a revoked capability's disposition under its row lock ([62571f7](https://github.com/oleg-tkachuk/limes/commit/62571f7ff8bfac20333f182c6b1b37566998fe85))
* **pgstore:** report a capability not on record as not found to RevocationLineage ([5b842a2](https://github.com/oleg-tkachuk/limes/commit/5b842a23256fcd5b250207afd5572317c61540c1))

### Performance Improvements

* **pgstore:** stop a listing's walk at the first ancestor with an entry ([8e2d0ee](https://github.com/oleg-tkachuk/limes/commit/8e2d0ee638771f2ed99868b8c18e2d695f4f1bec))

### Dependencies

* **nested:** require limes v0.117.0 in redisreplay, pgstore and otelmetric ([2e8e6ac](https://github.com/oleg-tkachuk/limes/commit/2e8e6ac21e0ebe16cffd1bba3479e4712c17c96d))

## [0.35.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.34.0...pgstore%2Fv0.35.0) (2026-10-22)

### Features

* **pgstore:** keep a revocation cache in step with the watcher's connection ([5e0f34d](https://github.com/oleg-tkachuk/limes/commit/5e0f34d0813bb9f19ff55ba842839f8bba844aaa))

### Dependencies

* **nested:** require limes v0.116.0 in redisreplay, pgstore and otelmetric ([6337464](https://github.com/oleg-tkachuk/limes/commit/633746442631b3d7ab0b6829a9859e90be57fe26))

## [0.34.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.33.0...pgstore%2Fv0.34.0) (2026-10-22)

### Features

* **pgstore:** report the disposition a revoked capability had ([1c20f0d](https://github.com/oleg-tkachuk/limes/commit/1c20f0dd6d222cf15696a88d03e40d375591c5b5))

### Dependencies

* **limes:** require limes v0.115.0 ([f4426e3](https://github.com/oleg-tkachuk/limes/commit/f4426e3d6784504618f568b7bb2864976fb8790e))

## [0.33.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.32.0...pgstore%2Fv0.33.0) (2026-10-22)

### ⚠ BREAKING CHANGES

* **pgstore:** return the revocation entry Revoke wrote or kept

### Features

* **pgstore:** name the revoked capabilities in each notification ([4508e27](https://github.com/oleg-tkachuk/limes/commit/4508e274113c6b92d19ab2340b9616228c2778f2))
* **pgstore:** refuse a cursor of another listing with ErrCursorMismatch ([e3ed8c8](https://github.com/oleg-tkachuk/limes/commit/e3ed8c856da5685e94c8b8c057483a3fafc226ad))
* **pgstore:** return the revocation entry Revoke wrote or kept ([f7ee78f](https://github.com/oleg-tkachuk/limes/commit/f7ee78f621f8e99d67d1024dbfbe233d352e58a0))
* **pgstore:** walk up to the revoking ancestor of a listed record ([0c46e82](https://github.com/oleg-tkachuk/limes/commit/0c46e8241b0b0b58b19a860b32f192274dd93460))

### Bug Fixes

* **pgstore:** build migration 014's indexes concurrently ([717253f](https://github.com/oleg-tkachuk/limes/commit/717253f352b90e32622a78dbe4521f6853949b3f))

### Dependencies

* **limes:** require limes v0.113.0 ([9a1245b](https://github.com/oleg-tkachuk/limes/commit/9a1245b61460aca21afb367ba8a089b1e44ca339))
* **limes:** require limes v0.114.0 ([d90005b](https://github.com/oleg-tkachuk/limes/commit/d90005be6ce176a24a2d8f5c77d406e8d9876825))

## [0.32.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.31.0...pgstore%2Fv0.32.0) (2026-10-21)

### Features

* **pgstore:** read one record with its disposition ([a3e92dd](https://github.com/oleg-tkachuk/limes/commit/a3e92ddecbf15a10fcc5152698ae46dcca11afbe))

### Bug Fixes

* **pgstore:** refuse a ListRecords cursor handed back with other filters ([a22cd08](https://github.com/oleg-tkachuk/limes/commit/a22cd083df8afcbc8f073a62e7e2de2dab50ed91))

### Performance Improvements

* **pgstore:** read whether a record is revoked from the record ([ca94962](https://github.com/oleg-tkachuk/limes/commit/ca9496275b166ef664fcff3622972075bbfd1c4b))

### Dependencies

* **pgstore:** require limes v0.112.0 ([1745737](https://github.com/oleg-tkachuk/limes/commit/1745737e58201ea47fb25fb0d8511848b623d0d4))

## [0.31.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.30.5...pgstore%2Fv0.31.0) (2026-10-21)

> **Upgrading:** migration 014 builds three indexes with a plain `CREATE INDEX`: writes to `limes.capabilities` and `limes.revocations` wait until they are built — about 5 s per million records — while reads, `IsRevoked` among them, go on. Run the first `Migrate` of this release on a large store at a quiet moment. Later migrations build their indexes `CONCURRENTLY`.

### Features

* **pgstore:** list a tenant's records under its scope, by index ([494f5fa](https://github.com/oleg-tkachuk/limes/commit/494f5fa707057387400614ef8377dc068c26a6ed))

### Dependencies

* **pgstore:** require limes v0.111.0 ([5a04414](https://github.com/oleg-tkachuk/limes/commit/5a04414e5e00ddd76f429e691332c6af29f933c3))

## [0.30.5](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.30.4...pgstore%2Fv0.30.5) (2026-10-20)

### Dependencies

* **pgstore:** require limes v0.110.0 ([74332a7](https://github.com/oleg-tkachuk/limes/commit/74332a7e76edb9179453b444016d3402f73349c4))

## [0.30.4](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.30.3...pgstore%2Fv0.30.4) (2026-10-20)

### Dependencies

* **pgstore:** require limes v0.109.2 ([1feeb2b](https://github.com/oleg-tkachuk/limes/commit/1feeb2ba914084d32d7926bccae525ffa8e52df1))

## [0.30.3](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.30.2...pgstore%2Fv0.30.3) (2026-10-20)

### Bug Fixes

* **pgstore:** lock every counter a release of holds writes in one order ([f8afb23](https://github.com/oleg-tkachuk/limes/commit/f8afb235d702a2b27beeff9d4ebe6ea4bc7b674d))

## [0.30.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.30.1...pgstore%2Fv0.30.2) (2026-10-20)

### Dependencies

* **pgstore:** require limes v0.109.1 ([132e84a](https://github.com/oleg-tkachuk/limes/commit/132e84a746b2dcd0a030f43bfd0f4d04c7539202))

## [0.30.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.30.0...pgstore%2Fv0.30.1) (2026-10-20)

### Dependencies

* **pgstore:** require limes v0.109.0 ([cd6c1fd](https://github.com/oleg-tkachuk/limes/commit/cd6c1fdc85167c9f4c0a9325e5ae7ab13925d5c6))

## [0.30.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.29.3...pgstore%2Fv0.30.0) (2026-10-20)

### ⚠ BREAKING CHANGES

* BiscuitRevocationCache, BiscuitRevocationCacheConfig and
  NewBiscuitRevocationCache are gone. RevocationCacheConfig.Upstream is now
  Capabilities, and its Biscuits field takes the Biscuit upstream; pass the
  one cache as both VerifierConfig.Revocations and BiscuitConfig.Revocations.

### Features

* one RevocationCache answers for capabilities and Biscuit copies ([b994327](https://github.com/oleg-tkachuk/limes/commit/b994327cdc6e1dec79afdcf51be8fdf4f8303450))

## [0.29.3](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.29.2...pgstore%2Fv0.29.3) (2026-10-19)

### Dependencies

* **pgstore:** require limes v0.108.3 ([40e5222](https://github.com/oleg-tkachuk/limes/commit/40e5222abef92618b0fb50ad73de3be2b7e4f673))

## [0.29.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.29.1...pgstore%2Fv0.29.2) (2026-10-19)

### Bug Fixes

* **pgstore:** bound migration 013's walk up the delegation chain ([0f03180](https://github.com/oleg-tkachuk/limes/commit/0f03180006c58a95b506960813692663b49302f0))
* **pgstore:** roll back a charge replayed once it holds its locks ([170392c](https://github.com/oleg-tkachuk/limes/commit/170392cb89e17a712dff5607d23d9e540c828dbd))

### Dependencies

* **pgstore:** require limes v0.108.2 ([63518bb](https://github.com/oleg-tkachuk/limes/commit/63518bb49a5162181c0588129a4b9ad06f5ce161))

## [0.29.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.29.0...pgstore%2Fv0.29.1) (2026-10-19)

### Dependencies

* **pgstore:** require limes v0.108.1 ([64d8d16](https://github.com/oleg-tkachuk/limes/commit/64d8d16e0f0a3d539dbab2ff1fae320d68163992))

## [0.29.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.28.3...pgstore%2Fv0.29.0) (2026-10-19)

### ⚠ BREAKING CHANGES

* **pgstore:** open every transaction read committed

### Bug Fixes

* **pgstore:** open every transaction read committed ([c0419e1](https://github.com/oleg-tkachuk/limes/commit/c0419e12a32c75b6fe52f5cc9d5e76fb33468431))
* **pgstore:** refuse a runtime role whose connection logged in as a role RLS does not hold ([9f2e9d0](https://github.com/oleg-tkachuk/limes/commit/9f2e9d09e11bdbcb0558b89b0d24322725992ef9))
* **pgstore:** stop a migration whose tenant scope cannot be set ([e4c870e](https://github.com/oleg-tkachuk/limes/commit/e4c870e2820e0fb8424840b01f69e568a95e7c74))

## [0.28.3](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.28.2...pgstore%2Fv0.28.3) (2026-10-19)

### Bug Fixes

* **pgstore:** count the copies under each capability on its usage row ([e9805aa](https://github.com/oleg-tkachuk/limes/commit/e9805aa68aeda4781205067f0cdcc7b14ef211eb))
* **pgstore:** refuse a charge whose onCharged ended the transaction and began another ([badc1e1](https://github.com/oleg-tkachuk/limes/commit/badc1e14f800e82c0f08a95037577b78d13e7121))

### Dependencies

* **pgstore:** require limes v0.108.0 ([1483843](https://github.com/oleg-tkachuk/limes/commit/148384347cb7aa4a3b48a784c9fd9aef2b487487))

## [0.28.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.28.1...pgstore%2Fv0.28.2) (2026-10-19)

### Dependencies

* **pgstore:** require limes v0.107.0 ([b4af052](https://github.com/oleg-tkachuk/limes/commit/b4af0520e1e852ca46b7bf72e3c8b28edee65c90))

## [0.28.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.28.0...pgstore%2Fv0.28.1) (2026-10-19)

### Bug Fixes

* **pgstore:** refuse Insert for a tenant whose usage was erased ([7f7547e](https://github.com/oleg-tkachuk/limes/commit/7f7547ec209a7cef326f25fdd2ad84c5c7f2cc71))

## [0.28.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.27.0...pgstore%2Fv0.28.0) (2026-10-19)

### Features

* **pgstore:** count Biscuit copies against every quota on the delegation chain ([c6888f3](https://github.com/oleg-tkachuk/limes/commit/c6888f3bc31dbde886dbf40f6faab4ecb864c7b8))

### Dependencies

* **pgstore:** require limes v0.106.1 ([f5dea5c](https://github.com/oleg-tkachuk/limes/commit/f5dea5c724ce24b7dbc0cda9b0021040451d46d1))

## [0.27.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.26.2...pgstore%2Fv0.27.0) (2026-10-19)

### ⚠ BREAKING CHANGES

* **pgstore:** the Tx onCharged gets returns a nil Conn, and a charge whose
  transaction onCharged ended is refused with ErrChargeTransaction.

### Bug Fixes

* **pgstore:** refuse a charge whose onCharged ended the transaction with SQL ([0f213ea](https://github.com/oleg-tkachuk/limes/commit/0f213ea41264d01a789677d3b37426537010fee3))
* **pgstore:** refuse a runtime role whose session belongs to a role RLS does not hold ([5949282](https://github.com/oleg-tkachuk/limes/commit/5949282a974866d384696166776f1064f4d88f7b))
* **pgstore:** scope every migration of a run to every tenant ([bcb6d00](https://github.com/oleg-tkachuk/limes/commit/bcb6d002314141329fb0f7f79c979fec9b1da6df))

## [0.26.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.26.1...pgstore%2Fv0.26.2) (2026-10-19)

### Dependencies

* **pgstore:** require limes v0.105.0 ([edcad68](https://github.com/oleg-tkachuk/limes/commit/edcad68bc3cde460f5df8e5f9e0902ed9291c140))

## [0.26.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.26.0...pgstore%2Fv0.26.1) (2026-10-19)

### Dependencies

* **pgstore:** require limes v0.104.1 ([5d967fc](https://github.com/oleg-tkachuk/limes/commit/5d967fc185524c9d3a7b17c888c58ddec4c782bb))

## [0.26.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.25.0...pgstore%2Fv0.26.0) (2026-10-19)

### ⚠ BREAKING CHANGES

* **pgstore:** migration 012 revokes EXECUTE from PUBLIC; a runtime role
  granted by hand needs EXECUTE on the schema's functions.

### Bug Fixes

* **pgstore:** let only runtime roles call the schema's functions ([c4f1392](https://github.com/oleg-tkachuk/limes/commit/c4f1392e295bbb7bd8861bceefd6cce1048b3761))

## [0.25.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.24.0...pgstore%2Fv0.25.0) (2026-10-19)

### Features

* **pgstore:** bound copies by the capability's quota under the store's ([71fe303](https://github.com/oleg-tkachuk/limes/commit/71fe303704c96fd69d1eafc5f599b743eb779beb))

### Dependencies

* **pgstore:** require limes v0.104.0 ([2c107e8](https://github.com/oleg-tkachuk/limes/commit/2c107e8ae9a97b57df29fd6c99b5e21b485d90e7))

## [0.24.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.23.1...pgstore%2Fv0.24.0) (2026-10-19)

### ⚠ BREAKING CHANGES

* **pgstore:** TxFrom's transaction refuses calls once onCharged returns
  (pgx.ErrTxClosed) and refuses Commit and Rollback (ErrChargeTransaction).

### Features

* **pgstore:** end the charge's transaction for onCharged when it returns ([e0fcf3c](https://github.com/oleg-tkachuk/limes/commit/e0fcf3ccdaf14e1fc3d662bbd637bd7f63df2578))

## [0.23.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.23.0...pgstore%2Fv0.23.1) (2026-10-19)

### Dependencies

* **pgstore:** require limes v0.103.0 ([bccf06c](https://github.com/oleg-tkachuk/limes/commit/bccf06cb2effa27ce87f5b0f821fbf015e878f78))

## [0.23.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.22.2...pgstore%2Fv0.23.0) (2026-10-19)

### ⚠ BREAKING CHANGES

* **pgstore:** migration 011 adds indexes on the charges table; run
  Migrate before deploying.

### Features

* **pgstore:** sum charges across tenants and index the ledger by time ([4a807cd](https://github.com/oleg-tkachuk/limes/commit/4a807cde7e4b037a3ee39861371de622f126bc14))

### Dependencies

* **pgstore:** require limes v0.102.0 ([c8c1cf7](https://github.com/oleg-tkachuk/limes/commit/c8c1cf7367cdea9c04cf9e4cddf8b43ef24940c6))

## [0.22.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.22.1...pgstore%2Fv0.22.2) (2026-10-19)

### Bug Fixes

* **pgstore:** refuse a runtime role that owns anything in the schema or can act as one ([bec962c](https://github.com/oleg-tkachuk/limes/commit/bec962cb8857baf59c31a17716c910f8ebcadccf))

### Dependencies

* **pgstore:** require limes v0.101.1 ([415d747](https://github.com/oleg-tkachuk/limes/commit/415d747d1d6c26502bb190499db4a8309b04c581))

## [0.22.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.22.0...pgstore%2Fv0.22.1) (2026-10-19)

### Bug Fixes

* **pgstore:** restore the session scope after Migrate when its context ends ([0398efa](https://github.com/oleg-tkachuk/limes/commit/0398efa29dfb0ec5d5dce447e07043b1c35e956b))

## [0.22.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.21.0...pgstore%2Fv0.22.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* **pgstore:** migration 010 forces row-level security on every table;
  run Migrate, and serve as a role that owns nothing.

### Features

* **pgstore:** bind the schema owner to row-level security ([365596a](https://github.com/oleg-tkachuk/limes/commit/365596a56138c2f8b6046e6bd77103a28bf129b0))

## [0.21.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.20.0...pgstore%2Fv0.21.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* **pgstore:** UsageStore implements the non-generic meter.UsageStore;
  onCharged reads its transaction with TxFrom(ctx).

### Features

* **pgstore:** read the charge's transaction with TxFrom ([6cf251c](https://github.com/oleg-tkachuk/limes/commit/6cf251c1ab25b9fb15cf1585e5b25f2e1dc51456))

### Dependencies

* **pgstore:** require limes v0.101.0 ([757a088](https://github.com/oleg-tkachuk/limes/commit/757a0889cedddebcef81d5311f0cb7f994437b97))

## [0.20.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.19.2...pgstore%2Fv0.20.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* **pgstore:** the usage store, ledger and erasers take and return
  meter types where they took limes ones.

### Code Refactoring

* **pgstore:** implement the meter package contracts ([b222972](https://github.com/oleg-tkachuk/limes/commit/b222972710c7bc763a04eeeb274163e1e9a06ba5))

### Dependencies

* **pgstore:** require limes v0.100.0 ([fb999d2](https://github.com/oleg-tkachuk/limes/commit/fb999d238075861a274da95a0f92da659382c883))

## [0.19.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.19.1...pgstore%2Fv0.19.2) (2026-10-18)

### Dependencies

* **pgstore:** require limes v0.99.1 ([4cd45f8](https://github.com/oleg-tkachuk/limes/commit/4cd45f817c8f29d1285265bc10474decf16b9391))

## [0.19.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.19.0...pgstore%2Fv0.19.1) (2026-10-18)

### Dependencies

* **pgstore:** require limes v0.99.0 ([339919a](https://github.com/oleg-tkachuk/limes/commit/339919a9aedfc955e5e1a07363fe78f6be46d50e))

## [0.19.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.18.6...pgstore%2Fv0.19.0) (2026-10-18)

### Features

* **pgstore:** enforce the quota of Biscuit copies per capability ([35dcbd1](https://github.com/oleg-tkachuk/limes/commit/35dcbd145a3fa70bd525e276cdc5d96260bec433))

### Bug Fixes

* **pgstore:** refuse inserting a child whose budget counts in another unit ([4c9a4a4](https://github.com/oleg-tkachuk/limes/commit/4c9a4a464d745fcaf816d842285fe4c2dbbc362b))
* **pgstore:** refuse settling a reservation again for another amount ([12fae89](https://github.com/oleg-tkachuk/limes/commit/12fae89b08ddac573e8153b76ddd076fe67bfa0f))

### Dependencies

* **pgstore:** require limes v0.98.0 ([8948118](https://github.com/oleg-tkachuk/limes/commit/8948118eae336f2616796e80a66b8cf09fb40eea))

## [0.18.6](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.18.5...pgstore%2Fv0.18.6) (2026-10-18)

### Dependencies

* **pgstore:** require limes v0.96.0 ([472f741](https://github.com/oleg-tkachuk/limes/commit/472f741095e2932e4b31eba927fe5aa69cb6c316))

## [0.18.5](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.18.4...pgstore%2Fv0.18.5) (2026-10-18)

### Dependencies

* **pgstore:** require limes v0.95.8 ([9c5dabe](https://github.com/oleg-tkachuk/limes/commit/9c5dabe7da39cca9f131da989eec9084031a896f))

## [0.18.4](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.18.3...pgstore%2Fv0.18.4) (2026-10-18)

### Dependencies

* **pgstore:** require limes v0.95.5 ([e75a769](https://github.com/oleg-tkachuk/limes/commit/e75a7693766158293ad06d9f70ba24f81104a04a))

## [0.18.3](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.18.2...pgstore%2Fv0.18.3) (2026-10-18)

### Dependencies

* **pgstore:** require limes v0.95.4 ([8cb6eea](https://github.com/oleg-tkachuk/limes/commit/8cb6eea95138ed6d416951243692de7971e3fa33))

## [0.18.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.18.1...pgstore%2Fv0.18.2) (2026-10-18)

### Bug Fixes

* **pgstore:** refuse a counter row hidden under another tenant ([e3222e0](https://github.com/oleg-tkachuk/limes/commit/e3222e0a40db5c79cbfc53a63230aa29e8915277))

## [0.18.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.18.0...pgstore%2Fv0.18.1) (2026-10-18)

### Dependencies

* **pgstore:** require limes v0.95.0 ([07ddda4](https://github.com/oleg-tkachuk/limes/commit/07ddda4f1dccf46a6f8645d220b6e9c9fbc3b700))

## [0.18.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.17.0...pgstore%2Fv0.18.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* **pgstore:** mark an erased tenant and refuse metering it

### Bug Fixes

* **pgstore:** mark an erased tenant and refuse metering it ([2b096cd](https://github.com/oleg-tkachuk/limes/commit/2b096cd82f5b20b7e777795fde98f04e38fd3594))
* **pgstore:** refuse a reservation for a capability already expired ([598657d](https://github.com/oleg-tkachuk/limes/commit/598657d9be4f7dc072672a943e3b9d1006b49cdd))

### Dependencies

* **pgstore:** require limes v0.94.0 ([9429aa3](https://github.com/oleg-tkachuk/limes/commit/9429aa379b064d488b5a85012e6f9fd39c4e6d37))

## [0.17.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.16.3...pgstore%2Fv0.17.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* **pgstore:** key copy counters and revocations by capability

### Bug Fixes

* **pgstore:** key copy counters and revocations by capability ([0e194a2](https://github.com/oleg-tkachuk/limes/commit/0e194a2fea1991c448c000973b1824204028913f))

### Dependencies

* **pgstore:** require limes v0.93.3 ([4e2d4b7](https://github.com/oleg-tkachuk/limes/commit/4e2d4b74c067e6463ffc1e06c3c4ebfb150cc953))

## [0.16.3](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.16.2...pgstore%2Fv0.16.3) (2026-10-18)

### Dependencies

* **pgstore:** require limes v0.92.0 ([47536e0](https://github.com/oleg-tkachuk/limes/commit/47536e05c0bd0d07f81497ba3e3e30d911c976dd))

## [0.16.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.16.1...pgstore%2Fv0.16.2) (2026-10-18)

### Dependencies

* **pgstore:** require limes v0.91.0 ([b608b34](https://github.com/oleg-tkachuk/limes/commit/b608b347947a3d2a7fc89b98391fae779ebc371c))

## [0.16.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.16.0...pgstore%2Fv0.16.1) (2026-10-18)

### Dependencies

* **pgstore:** bump golang.org/x/crypto v0.55.0 → v0.58.0 ([1aabf31](https://github.com/oleg-tkachuk/limes/commit/1aabf3102a100c8546f1e40a2654a63493939dcf))

## [0.16.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.15.5...pgstore%2Fv0.16.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* **pgstore:** list tenant budgets past a threshold in basis points
* **pgstore:** name the buckets as limes now numbers them
* **pgstore:** report the schema version as int
* **pgstore:** validate requests through limes

### Features

* **pgstore:** export the settings the row-level security policies read ([3c0b391](https://github.com/oleg-tkachuk/limes/commit/3c0b39164b48b74b207a9ff6e44c75b6225bdb15))

### Code Refactoring

* **pgstore:** list tenant budgets past a threshold in basis points ([7eb2201](https://github.com/oleg-tkachuk/limes/commit/7eb22019be2c92fe447971d1eb4c8252a3e622dc))
* **pgstore:** name the buckets as limes now numbers them ([c9a5921](https://github.com/oleg-tkachuk/limes/commit/c9a5921cd86199d436dbb565d4d541908510c979))
* **pgstore:** report the schema version as int ([cc4489e](https://github.com/oleg-tkachuk/limes/commit/cc4489e20feb9da08ab334cd42774cdf7e3cc70f))
* **pgstore:** validate requests through limes ([5579506](https://github.com/oleg-tkachuk/limes/commit/5579506d55b8cce5927ac2a736d82fb4b3d7fe43))

### Dependencies

* **pgstore:** require limes v0.86.0 ([2868c04](https://github.com/oleg-tkachuk/limes/commit/2868c0479efb648ebffb08217ea3f3df1cded92b))
* **pgstore:** require limes v0.88.0 ([dc5315c](https://github.com/oleg-tkachuk/limes/commit/dc5315cdc652e2f829dd636b63f7604345e0cfc5))
* **pgstore:** require limes v0.90.0 ([d2ddd63](https://github.com/oleg-tkachuk/limes/commit/d2ddd63d4772c92a3adff7c551d0e9b0c9f264fd))

## [0.15.5](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.15.4...pgstore%2Fv0.15.5) (2026-10-18)

### Dependencies

* **pgstore:** require limes v0.85.0 ([c544ea6](https://github.com/oleg-tkachuk/limes/commit/c544ea6eff104e0a3e17711f4afd5a2a9a521c5d))

## [0.15.4](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.15.3...pgstore%2Fv0.15.4) (2026-10-18)

### Dependencies

* **pgstore:** require limes v0.84.0 ([bb408a4](https://github.com/oleg-tkachuk/limes/commit/bb408a487272ea8e538b4ce5abd645d9aad96c65))

## [0.15.3](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.15.2...pgstore%2Fv0.15.3) (2026-10-18)

### Dependencies

* **pgstore:** require limes v0.82.0 ([cc84533](https://github.com/oleg-tkachuk/limes/commit/cc84533f2451ee41fdd7cf2d614931039887d0c5))

## [0.15.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.15.1...pgstore%2Fv0.15.2) (2026-10-18)

### Dependencies

* **pgstore:** require limes v0.81.0 ([5ed9a2c](https://github.com/oleg-tkachuk/limes/commit/5ed9a2cc879b54436b6ef3f61e311be2f6097e47))

## [0.15.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.15.0...pgstore%2Fv0.15.1) (2026-10-18)

### Dependencies

* **pgstore:** require limes v0.80.0 ([a7bb275](https://github.com/oleg-tkachuk/limes/commit/a7bb27599bcb5bdfd0657504d61a1348033cba12))

## [0.15.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.14.0...pgstore%2Fv0.15.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* **pgstore:** refuse ListTenantBudgets under a scope narrower than every tenant

### Bug Fixes

* **pgstore:** refuse ListTenantBudgets under a scope narrower than every tenant ([a10a39b](https://github.com/oleg-tkachuk/limes/commit/a10a39b9c2482bad20563f041cd2d10cc4f61e3b))

## [0.14.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.13.4...pgstore%2Fv0.14.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* **pgstore:** let a revoked capability's hold be settled until it expires

### Bug Fixes

* **pgstore:** let a revoked capability's hold be settled until it expires ([72e8df3](https://github.com/oleg-tkachuk/limes/commit/72e8df30fbfc6da9822e0119a08e96af217985c1))

## [0.13.4](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.13.3...pgstore%2Fv0.13.4) (2026-10-18)

### Bug Fixes

* **pgstore:** report a database Migrate never ran in as a schema mismatch ([c075bda](https://github.com/oleg-tkachuk/limes/commit/c075bdacb20ea7bd1acc78e5905645c2f1d53872))

## [0.13.3](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.13.2...pgstore%2Fv0.13.3) (2026-10-18)

### Bug Fixes

* **pgstore:** take every lock in the documented order ([c4ae4cd](https://github.com/oleg-tkachuk/limes/commit/c4ae4cd01161a9cff17e0722dead8e092a5cfba6))

## [0.13.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.13.1...pgstore%2Fv0.13.2) (2026-10-18)

### Dependencies

* **pgstore:** require limes v0.78.2 ([fcb1122](https://github.com/oleg-tkachuk/limes/commit/fcb11224b6351df45ae4946993e299e460f23bfb))

## [0.13.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.13.0...pgstore%2Fv0.13.1) (2026-10-18)

### Dependencies

* **pgstore:** require limes v0.78.0 ([6446d7e](https://github.com/oleg-tkachuk/limes/commit/6446d7eca0566c9a7d4d7b4338515946d206a0f0))

## [0.13.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.12.8...pgstore%2Fv0.13.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* **pgstore:** end a hold with its capability, and release a revoked one's

### Bug Fixes

* **pgstore:** end a hold with its capability, and release a revoked one's ([5dd36de](https://github.com/oleg-tkachuk/limes/commit/5dd36de51ae7438fdf86f6acde048207d2534b57))

### Dependencies

* **pgstore:** require limes v0.77.4 ([b5b9192](https://github.com/oleg-tkachuk/limes/commit/b5b9192d2b472f028e7fb0f1663f87158dc6f451))

## [0.12.8](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.12.7...pgstore%2Fv0.12.8) (2026-10-18)

### Bug Fixes

* **pgstore:** refuse a runtime role that is not one, and a schema another role owns ([6c4d6b5](https://github.com/oleg-tkachuk/limes/commit/6c4d6b536be466dfb3d81067becedcca9eba5b47))
* **pgstore:** resolve the policy functions' catalog names whatever the search path ([a7d79c1](https://github.com/oleg-tkachuk/limes/commit/a7d79c16e81463dd5099e33e97a238461f3c58c6))

## [0.12.7](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.12.6...pgstore%2Fv0.12.7) (2026-10-18)

### Bug Fixes

* **pgstore:** refuse an ExternalRef replay under other Biscuit copies ([bfce4d6](https://github.com/oleg-tkachuk/limes/commit/bfce4d6bf8a5bc2d6d643a6c97929913b1452511))
* **pgstore:** set the whole scope, so a role default cannot widen it ([a3d468a](https://github.com/oleg-tkachuk/limes/commit/a3d468a906ec7aadc138aaf651df8dd99841150a))

### Dependencies

* **pgstore:** require limes v0.77.3 ([5663282](https://github.com/oleg-tkachuk/limes/commit/5663282ae171454718fbc25cbce00cad26d33ab6))

## [0.12.6](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.12.5...pgstore%2Fv0.12.6) (2026-10-18)

### Dependencies

* **pgstore:** require limes v0.77.2 ([9789508](https://github.com/oleg-tkachuk/limes/commit/9789508fef68d20a6591f9fb1df2585aa97a7f38))

## [0.12.5](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.12.4...pgstore%2Fv0.12.5) (2026-10-18)

### Bug Fixes

* **pgstore:** keep a concurrent first charge when setting a tenant's first ceiling ([6581abf](https://github.com/oleg-tkachuk/limes/commit/6581abf5eccd8dc43b351481fa319d0a4b6eb866))

## [0.12.4](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.12.3...pgstore%2Fv0.12.4) (2026-10-17)

### Dependencies

* **pgstore:** require limes v0.77.0 ([117945c](https://github.com/oleg-tkachuk/limes/commit/117945cfca2fb61b4f46d297e623dd985fd1d352))

## [0.12.3](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.12.2...pgstore%2Fv0.12.3) (2026-10-17)

### Dependencies

* **pgstore:** require limes v0.76.0 ([8385fed](https://github.com/oleg-tkachuk/limes/commit/8385fede82bb87f4b6b38e4565dfd77b5d04313b))

## [0.12.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.12.1...pgstore%2Fv0.12.2) (2026-10-17)

### Bug Fixes

* **pgstore:** count in the subject's unit, and pin a tenant's on first use ([5c8ad79](https://github.com/oleg-tkachuk/limes/commit/5c8ad7937abafe10c87d1ceec082b67d855f1b0d))
* **pgstore:** sum a period whole under ChargeBucketUnspecified ([d45fd29](https://github.com/oleg-tkachuk/limes/commit/d45fd293b94fc663cf071797c42f7e40511cbaab))

### Dependencies

* **pgstore:** require limes v0.73.0 ([63e9843](https://github.com/oleg-tkachuk/limes/commit/63e984313e88bf818434acc810100a1d87180813))
* **pgstore:** require limes v0.74.0 ([3884035](https://github.com/oleg-tkachuk/limes/commit/3884035f841b2940ec40aeae6dcafe4a1cd9d50b))

## [0.12.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.12.0...pgstore%2Fv0.12.1) (2026-10-17)

### Dependencies

* **pgstore:** require limes v0.70.1 ([578a837](https://github.com/oleg-tkachuk/limes/commit/578a83727aa639b422dc3bee61ed8c3fbfad79c8))

## [0.12.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.11.1...pgstore%2Fv0.12.0) (2026-10-17)

### ⚠ BREAKING CHANGES

* **pgstore:** hand EraseTenant's callback the erase result

### Features

* **pgstore:** hand EraseTenant's callback the erase result ([07926b0](https://github.com/oleg-tkachuk/limes/commit/07926b01179382ba9a9522e06cb9793f4afba805))

## [0.11.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.11.0...pgstore%2Fv0.11.1) (2026-10-17)

### Dependencies

* **pgstore:** require limes v0.69.0 ([2e1abd1](https://github.com/oleg-tkachuk/limes/commit/2e1abd1afc06a8a15071e855d0e3e42c6091f9b4))

## [0.11.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.10.3...pgstore%2Fv0.11.0) (2026-10-17)

### ⚠ BREAKING CHANGES

* **pgstore:** hand onCharged the receipt of the charge it runs in

### Features

* **pgstore:** hand onCharged the receipt of the charge it runs in ([b45b48a](https://github.com/oleg-tkachuk/limes/commit/b45b48a7478e172a441f3312bfc1afceacf81436))

### Bug Fixes

* **pgstore:** settle and release a hold only under the tenant that made it ([88e20d7](https://github.com/oleg-tkachuk/limes/commit/88e20d790f109096de59631a45c0ef966c2a8d60))

### Dependencies

* **pgstore:** require limes v0.67.0 ([4efc127](https://github.com/oleg-tkachuk/limes/commit/4efc1277139bd257d861e16b30ba2efec8a64990))

## [0.10.3](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.10.2...pgstore%2Fv0.10.3) (2026-10-17)

### Dependencies

* **pgstore:** require limes v0.66.1 ([2176577](https://github.com/oleg-tkachuk/limes/commit/21765775495284410fef0e848bc7dc89b4ab7c76))

## [0.10.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.10.1...pgstore%2Fv0.10.2) (2026-10-17)

### Bug Fixes

* **deps:** require biscuit-go by its eclipse-biscuit module path ([df2a92b](https://github.com/oleg-tkachuk/limes/commit/df2a92b524b5c38522eb10bd9a6e2f48cf99526f))

## [0.10.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.10.0...pgstore%2Fv0.10.1) (2026-10-15)

### Dependencies

* **pgstore:** require limes v0.66.0 ([efc1bce](https://github.com/oleg-tkachuk/limes/commit/efc1bce2fddb6590716952322b0f0673cf289c0d))

## [0.10.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.9.3...pgstore%2Fv0.10.0) (2026-10-15)

### ⚠ BREAKING CHANGES

* **pgstore:** refuse a child with no expiry under a parent that has one
* **pgstore:** release a reservation only for its subject, in its tenant

### Bug Fixes

* **pgstore:** refuse a child with no expiry under a parent that has one ([613c4f2](https://github.com/oleg-tkachuk/limes/commit/613c4f27286aea0f6afb97e1096732954a2aab36))
* **pgstore:** release a reservation only for its subject, in its tenant ([45523d9](https://github.com/oleg-tkachuk/limes/commit/45523d9f4952d71260279f9cc44f19049e7e842a))

### Dependencies

* **pgstore:** require limes v0.65.0 ([ec95758](https://github.com/oleg-tkachuk/limes/commit/ec95758e28f11558ebbf5a7a8b72849fc8b52709))

## [0.9.3](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.9.2...pgstore%2Fv0.9.3) (2026-10-15)

### Dependencies

* **pgstore:** require limes v0.64.3 ([ae2659d](https://github.com/oleg-tkachuk/limes/commit/ae2659d737cebea9d47dbc25343c3ed6e0b0b853))

## [0.9.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.9.1...pgstore%2Fv0.9.2) (2026-10-14)

## [0.9.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.9.0...pgstore%2Fv0.9.1) (2026-10-14)

## [0.9.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.8.0...pgstore%2Fv0.9.0) (2026-10-14)

### ⚠ BREAKING CHANGES

* **pgstore:** refuse writes under a consumer's read-only scope

### Features

* **pgstore:** count records with NewCensus, every tenant only under a scope admitting all ([a89d171](https://github.com/oleg-tkachuk/limes/commit/a89d171dcc4c3d9cf6e081bedca5a70a2a7a5b40))

### Bug Fixes

* **pgstore:** refuse writes under a consumer's read-only scope ([1e8b7f8](https://github.com/oleg-tkachuk/limes/commit/1e8b7f859252a68c2958e5b37cadf7c834d6ad27))

## [0.8.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.7.1...pgstore%2Fv0.8.0) (2026-10-14)

### Features

* **pgstore:** sum charges by ISO week ([d7257c8](https://github.com/oleg-tkachuk/limes/commit/d7257c8ef0629a3d51b3a78f898e815aa0891dcf))

## [0.7.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.7.0...pgstore%2Fv0.7.1) (2026-10-14)

### Bug Fixes

* **pgstore:** order charge sums by amount, not by their text ([fc1a890](https://github.com/oleg-tkachuk/limes/commit/fc1a890b7f7e0bffd9ed81660bb819d8ea9fbac7))

## [0.7.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.6.0...pgstore%2Fv0.7.0) (2026-10-14)

### ⚠ BREAKING CHANGES

* **pgstore:** Migrate takes a *pgx.Conn and MigrateOptions instead of a *pgxpool.Pool.

### Features

* **pgstore:** migrate over one connection and grant the runtime roles ([b36ff3f](https://github.com/oleg-tkachuk/limes/commit/b36ff3f009418913b9fc254df00a76430cd84731))

## [0.6.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.5.0...pgstore%2Fv0.6.0) (2026-10-14)

### Features

* **pgstore:** sum a tenant's charges through a Ledger ([f398a24](https://github.com/oleg-tkachuk/limes/commit/f398a244a6ed1207cec083cfae69d4f118f1eed0))

## [0.5.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.4.0...pgstore%2Fv0.5.0) (2026-10-14)

### Features

* **pgstore:** notify every revocation and clear caches on it ([977a454](https://github.com/oleg-tkachuk/limes/commit/977a45428d16256b7d0735e4a0c4da4ee4d805b5))

## [0.4.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.3.0...pgstore%2Fv0.4.0) (2026-10-14)

### Features

* **pgstore:** add a read-only scope across every tenant ([3cac36c](https://github.com/oleg-tkachuk/limes/commit/3cac36cfae38b1861c61076a3e0f1744e71d4d2d))

### Bug Fixes

* **pgstore:** record the subject's unit on Bump ([7fba412](https://github.com/oleg-tkachuk/limes/commit/7fba412f53d9b1b6af8c81eed0d98f60f12d72e7))

## [0.3.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.2.0...pgstore%2Fv0.3.0) (2026-10-14)

### Features

* **pgstore:** erase a tenant's records and usage ([c913a26](https://github.com/oleg-tkachuk/limes/commit/c913a267b2d55d0d102a29ef0ed683edaa958846))

## [0.2.0](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.1.3...pgstore%2Fv0.2.0) (2026-10-14)

### ⚠ BREAKING CHANGES

* **pgstore:** such calls return limes.ErrScopeMismatch instead of ErrInvalidRequest, ErrTenantBudgetNotFound, an empty page or a row-level security error.

### Features

* **pgstore:** refuse a call scoped to another tenant than its request names ([8522d69](https://github.com/oleg-tkachuk/limes/commit/8522d698e9ed23606ec8c1f377d08950df03eee0))

## [0.1.3](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.1.2...pgstore%2Fv0.1.3) (2026-10-14)

### Bug Fixes

* **pgstore:** refuse a nil pool or connection at construction ([98cb34f](https://github.com/oleg-tkachuk/limes/commit/98cb34f7461506ce81a9732280f2456ffc35bcda))

## [0.1.2](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.1.1...pgstore%2Fv0.1.2) (2026-10-13)

### Bug Fixes

* **pgstore:** refuse a tenant budget filter the store does not name ([cb4c630](https://github.com/oleg-tkachuk/limes/commit/cb4c630d4cd3b672fc4a1a1e49ba24016c1575b9))

## [0.1.1](https://github.com/oleg-tkachuk/limes/compare/pgstore%2Fv0.1.0...pgstore%2Fv0.1.1) (2026-10-13)

### Bug Fixes

* **pgstore:** name a tenant budget filter in SQL rather than pass its number ([0dcd6cd](https://github.com/oleg-tkachuk/limes/commit/0dcd6cdd68734aaa6a6582de7da66aa2694aafba))
* **pgstore:** refuse a delegation past MaxDelegationDepth on insert ([2b5820a](https://github.com/oleg-tkachuk/limes/commit/2b5820a49ca272ed625b0fc53f1cad7b78f2f921))

## [0.1.0](https://github.com/oleg-tkachuk/limes/releases/tag/pgstore/v0.1.0)
