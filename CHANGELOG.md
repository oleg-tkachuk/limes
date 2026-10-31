# Changelog

Every release of the limes module, newest first, from its release notes. Written by .github/ci/changelog.ts after each release; not edited by hand.

## [0.142.0](https://github.com/oleg-tkachuk/limes/compare/v0.141.1...v0.142.0) (2026-10-31)

### Features

* **meter:** list a tenant's open reservations ([9b51b3f](https://github.com/oleg-tkachuk/limes/commit/9b51b3feedfaeca823ab7169a03aaee77d225bf7))
* **meter:** release the holds of revoked matches in every tenant ([a044361](https://github.com/oleg-tkachuk/limes/commit/a0443614d3a90f57d67e5968d0f3244cf5597c07))

## [0.141.1](https://github.com/oleg-tkachuk/limes/compare/v0.141.0...v0.141.1) (2026-10-30)

### Bug Fixes

* **memstore:** list each record with its session ([f79a82f](https://github.com/oleg-tkachuk/limes/commit/f79a82f3c9881e6164f4cc0422d9c3a9ce47c389))

## [0.141.0](https://github.com/oleg-tkachuk/limes/compare/v0.140.0...v0.141.0) (2026-10-30)

### Features

* filter the census and the listing by revocation selectors ([88b3f29](https://github.com/oleg-tkachuk/limes/commit/88b3f2921db5519193f99300ce3bef776af04ba7))
* **meter:** release the holds of a bulk revocation at once ([105d498](https://github.com/oleg-tkachuk/limes/commit/105d4986198f5bdcedb4ae19cb9449055a8de214))
* revoke an agent run's capabilities ([63ad6a1](https://github.com/oleg-tkachuk/limes/commit/63ad6a1c3d9dfedd8fe8fb8fb73d5829370177c5))

## [0.140.0](https://github.com/oleg-tkachuk/limes/compare/v0.139.0...v0.140.0) (2026-10-30)

### Features

* revoke a client's or a session's capabilities in every tenant ([c30ac36](https://github.com/oleg-tkachuk/limes/commit/c30ac36396003853feef1367a821a9caba021f6c))

## [0.139.0](https://github.com/oleg-tkachuk/limes/compare/v0.138.0...v0.139.0) (2026-10-29)

### Features

* **errs:** build wrapped error text naming the limes prefix once ([da62473](https://github.com/oleg-tkachuk/limes/commit/da624737b98efeb49bd32e5b440b114d9a5621b1))

### Bug Fixes

* name the limes prefix once in a widening delegation ([5daa043](https://github.com/oleg-tkachuk/limes/commit/5daa043b205181e66df1fd58b8359023eb90672b))
* name the limes prefix once in every wrapped error ([0a02b26](https://github.com/oleg-tkachuk/limes/commit/0a02b2614965b513099b50c9e3f37233311037cd))

## [0.138.0](https://github.com/oleg-tkachuk/limes/compare/v0.137.0...v0.138.0) (2026-10-29)

### Features

* give every error of the root module a code ([d5241df](https://github.com/oleg-tkachuk/limes/commit/d5241df653a6217a930c23761f19b1770f993532))

## [0.137.0](https://github.com/oleg-tkachuk/limes/compare/v0.136.0...v0.137.0) (2026-10-29)

### Features

* give every error a stable code and RFC 9457 problem details ([23d17d5](https://github.com/oleg-tkachuk/limes/commit/23d17d53414def603867e5a7198fa9fe60987219))

## [0.136.0](https://github.com/oleg-tkachuk/limes/compare/v0.135.0...v0.136.0) (2026-10-29)

### Features

* revoke every capability of a subject, a client or a session ([0ffd79e](https://github.com/oleg-tkachuk/limes/commit/0ffd79e1f7110cc291f42a137ff1fe593d5eb891))

## [0.135.0](https://github.com/oleg-tkachuk/limes/compare/v0.134.0...v0.135.0) (2026-10-29)

### Features

* tell a capability from an access token by its kid alone ([cb99b03](https://github.com/oleg-tkachuk/limes/commit/cb99b03c14be0df1dd42063029789dbf95115cf4))

## [0.134.0](https://github.com/oleg-tkachuk/limes/compare/v0.133.1...v0.134.0) (2026-10-29)

### Features

* record the capability a subject token was exchanged for ([7ee7238](https://github.com/oleg-tkachuk/limes/commit/7ee7238ef66f0d9339474b6c65bda181825481d4))

## [0.133.1](https://github.com/oleg-tkachuk/limes/compare/v0.133.0...v0.133.1) (2026-10-29)

### Bug Fixes

* name a token refusal's family once ([940ae06](https://github.com/oleg-tkachuk/limes/commit/940ae06675c0f6ee8aebbd6c5377ff18ec0617d8))

## [0.133.0](https://github.com/oleg-tkachuk/limes/compare/v0.132.1...v0.133.0) (2026-10-29)

### ⚠ BREAKING CHANGES

* limes.Attenuate is biscuit.Attenuate in the module github.com/oleg-tkachuk/limes/biscuit; BiscuitConfig.Form is required, and IssuerConfig.Biscuit names the form Issuer.Biscuit seals in. Both take biscuit.Form().

### Features

* **biscuit:** add the Biscuit form as a module of its own ([6f0d17e](https://github.com/oleg-tkachuk/limes/commit/6f0d17e982ec3c55af1c128f778bb4c2a1723f82))
* **meter:** split RequestCounter and BudgetMeter out of Meter ([930b259](https://github.com/oleg-tkachuk/limes/commit/930b259bb373ac5fcb1fdf179375eda23e5285d1))
* seal and read the Biscuit form through a TokenForm ([8d29f43](https://github.com/oleg-tkachuk/limes/commit/8d29f432fc70f73c98eb0602cac5ee20aa0400dd))
* take the Biscuit form out of the root module ([f00036d](https://github.com/oleg-tkachuk/limes/commit/f00036d749e660cc79f4524bd56ff0eb646ef948))

## [0.132.1](https://github.com/oleg-tkachuk/limes/compare/v0.132.0...v0.132.1) (2026-10-28)

### Bug Fixes

* **examples:** give the mcp-exchange capabilities a request ceiling ([9ae97b8](https://github.com/oleg-tkachuk/limes/commit/9ae97b8f6a7c592c3c24b67204f2d22074f0b567))
* **issuer:** refuse a capability no request can use ([363b8b1](https://github.com/oleg-tkachuk/limes/commit/363b8b17e77945520cf17b2ddb6ae069a8c2cad6))

## [0.132.0](https://github.com/oleg-tkachuk/limes/compare/v0.131.2...v0.132.0) (2026-10-28)

### Features

* **jwks:** log dropped algorithm names and sets the handler cannot serve ([d4118e4](https://github.com/oleg-tkachuk/limes/commit/d4118e40c44b5fb65fbacc8529fa156ec23b0342))
* refuse a request carrying more than one Authorization or DPoP field ([f81a507](https://github.com/oleg-tkachuk/limes/commit/f81a50773bdce4dadc6078c8df1f0a4e555fbc23))

### Bug Fixes

* answer a use_dpop_nonce refusal with no nonce to give as the server's failure ([19e0600](https://github.com/oleg-tkachuk/limes/commit/19e06004559f61632441975cac36b1bb8ae75591))
* **dpop:** refuse a proof whose header names a critical extension ([293edb8](https://github.com/oleg-tkachuk/limes/commit/293edb80ec5eda2479edfdd60e7f36f8e6d49473))
* **jwks:** read a list-valued age by its first member ([790c2fa](https://github.com/oleg-tkachuk/limes/commit/790c2fa56ad7448ff05f1ddf816fee919c828629))
* **jwks:** serve no-cache for a set to revalidate on every use ([f84e583](https://github.com/oleg-tkachuk/limes/commit/f84e58371247e5bc91d58648ae578e9f712bc04a))
* **jwks:** skip a JWK of the wrong shape instead of refusing the set ([35aeaa6](https://github.com/oleg-tkachuk/limes/commit/35aeaa6d9fc071f162b0114035e86e739ba084a6))
* **jwks:** skip a JWK whose use or key_ops is not verifying ([f597b9d](https://github.com/oleg-tkachuk/limes/commit/f597b9de3cceeb30696234b002f7de79d3a7acb2))
* **jwks:** split cache-control on commas outside quoted strings only ([67d5b9d](https://github.com/oleg-tkachuk/limes/commit/67d5b9d1c44f091270b0e78d9b5b30d93b3fd8af))

## [0.131.2](https://github.com/oleg-tkachuk/limes/compare/v0.131.1...v0.131.2) (2026-10-28)

### Bug Fixes

* **jwks:** serve the set when a withdrawn key's algorithm is left named ([555a282](https://github.com/oleg-tkachuk/limes/commit/555a282b2b2812108ae7340bb1a4bd32a33ffba5))
* read a tenant and a run id in a token only as the signer spells them ([7da67a7](https://github.com/oleg-tkachuk/limes/commit/7da67a7a897280a579d230ca147813c1415c01e7))
* refuse a nil pointer as the verifier's Biscuit revocation lookup ([2c26928](https://github.com/oleg-tkachuk/limes/commit/2c269286f30b5af77597e4dbd382e5649a008f06))
* refuse a resource name prefix that ends inside a character ([415735f](https://github.com/oleg-tkachuk/limes/commit/415735f17fd187ee8e978a280d9986b62e6d81ef))

## [0.131.1](https://github.com/oleg-tkachuk/limes/compare/v0.131.0...v0.131.1) (2026-10-28)

### Bug Fixes

* introspect a Biscuit copy with the limits its blocks set ([f03e6dc](https://github.com/oleg-tkachuk/limes/commit/f03e6dc9c0367c574381c26d5702a2765cd89040))

## [0.131.0](https://github.com/oleg-tkachuk/limes/compare/v0.130.0...v0.131.0) (2026-10-27)

### ⚠ BREAKING CHANGES

* the limes-cap-v6+jwt format, SignerConfig.TokenType and VerifierConfig.AcceptTokenTypes are gone; every token is at+jwt. Issue requires a ClientID and a Subject.Subject, as RFC 9068 requires client_id and sub, and a signer's kid must carry jwks.KeyIDPrefix, which jwks.KeyID now writes.

### Features

* issue one token format, an RFC 9068 access token under a limes kid ([1af9db2](https://github.com/oleg-tkachuk/limes/commit/1af9db2ecbdf136e8a40ab4bd04461d68a58495c))
* name whom a capability acts for, as RFC 8693 act in v7 ([6f25664](https://github.com/oleg-tkachuk/limes/commit/6f2566472037e619d915813b0a1c382afda092d2))

## [0.130.0](https://github.com/oleg-tkachuk/limes/compare/v0.129.0...v0.130.0) (2026-10-27)

### Features

* marshal caveats as a token's claim carries them ([6d1b471](https://github.com/oleg-tkachuk/limes/commit/6d1b4717c6d9915da151fd9c68f7ae20532f7cc6))

## [0.129.0](https://github.com/oleg-tkachuk/limes/compare/v0.128.0...v0.129.0) (2026-10-27)

### Features

* introspect a token without authorizing it ([f4677d0](https://github.com/oleg-tkachuk/limes/commit/f4677d004d9b782e6795ae133d3e91b105463a37))

## [0.128.0](https://github.com/oleg-tkachuk/limes/compare/v0.127.0...v0.128.0) (2026-10-27)

### Features

* add token format v7, an RFC 9068 access token ([80d76bf](https://github.com/oleg-tkachuk/limes/commit/80d76bf1eaf65a00215b85386d63a51098c329c1))
* **jwks:** name each key's algorithm in the JWKS ([1783e8f](https://github.com/oleg-tkachuk/limes/commit/1783e8f708824e0751e651204635fbe388d96256))
* name resources by prefix across segments ([d33e708](https://github.com/oleg-tkachuk/limes/commit/d33e708843b8a6de9bf9499afe25220f3c69d997))

## [0.127.0](https://github.com/oleg-tkachuk/limes/compare/v0.126.0...v0.127.0) (2026-10-27)

### Features

* **dpop:** check a token endpoint's proof and bind to its key ([f92de3f](https://github.com/oleg-tkachuk/limes/commit/f92de3fd8cc2a2b02660235c4003ff062726266a))
* read and optionally write the EdDSA name of Ed25519 ([7770d0c](https://github.com/oleg-tkachuk/limes/commit/7770d0c9082efacfd52c41449c1b6e2051a9c848))

## [0.126.0](https://github.com/oleg-tkachuk/limes/compare/v0.125.1...v0.126.0) (2026-10-27)

### ⚠ BREAKING CHANGES

* every uuid.UUID in the API is the standard library's,
  where it was github.com/google/uuid's, and the go directive is 1.27.0.

### Code Refactoring

* use the standard library's uuid ([df229dc](https://github.com/oleg-tkachuk/limes/commit/df229dcc24b7c5f2a8a949c11619040f165d84f2))

## [0.125.1](https://github.com/oleg-tkachuk/limes/compare/v0.125.0...v0.125.1) (2026-10-25)

### Bug Fixes

* count a revocation signal after the clear it causes ([ae40ef4](https://github.com/oleg-tkachuk/limes/commit/ae40ef4451558721940ed05905da2f198aaccd7d))
* count a whole signal after the signal it is a part of ([0309e2f](https://github.com/oleg-tkachuk/limes/commit/0309e2f8bbdafd6d083e96c69557dc96d9b57d2f))

## [0.125.0](https://github.com/oleg-tkachuk/limes/compare/v0.124.0...v0.125.0) (2026-10-25)

### ⚠ BREAKING CHANGES

* bind a record listing's cursor to its tenant

### Bug Fixes

* bind a record listing's cursor to its tenant ([ed0bbbd](https://github.com/oleg-tkachuk/limes/commit/ed0bbbded82f1c98276a71f798bdf6a6e66f7613))
* refuse a nil pointer as the verifier's keys or revocations ([6ee707f](https://github.com/oleg-tkachuk/limes/commit/6ee707fe5820e6cec3c600695ff31ca5e03125b4))

## [0.124.0](https://github.com/oleg-tkachuk/limes/compare/v0.123.0...v0.124.0) (2026-10-25)

### ⚠ BREAKING CHANGES

* clear the cache when a lost signal closes, and bound SyncedTTL
* **otelmetric:** namespace the metering attributes under limes.

### Features

* **otelmetric:** namespace the metering attributes under limes. ([419dbbb](https://github.com/oleg-tkachuk/limes/commit/419dbbb731287c16e14708bb66dab09dac8faa5b))

### Bug Fixes

* clear the cache when a lost signal closes, and bound SyncedTTL ([da8158c](https://github.com/oleg-tkachuk/limes/commit/da8158c1e5f8a305e85a82d6ffc5ff47c885ed55))
* **pgstore:** hold a metered lineage for key share ([78636df](https://github.com/oleg-tkachuk/limes/commit/78636df5f065071d146ade33cb34e6e282c4d154))

## [0.123.0](https://github.com/oleg-tkachuk/limes/compare/v0.122.0...v0.123.0) (2026-10-24)

### ⚠ BREAKING CHANGES

* give each revocation signal a RevocationSignal of its own

### Features

* give each revocation signal a RevocationSignal of its own ([2f53644](https://github.com/oleg-tkachuk/limes/commit/2f53644ce3a68002082f2e65850c5efae1e48ae7))

## [0.122.0](https://github.com/oleg-tkachuk/limes/compare/v0.121.0...v0.122.0) (2026-10-24)

### ⚠ BREAKING CHANGES

* rename RevocationCacheStats.ClearsRevoked to NarrowClears
* rename RevokeResult.Before to PriorDisposition

### Code Refactoring

* rename RevocationCacheStats.ClearsRevoked to NarrowClears ([f73211c](https://github.com/oleg-tkachuk/limes/commit/f73211c3414b84f271de3d1e8e757a99bfa3c91b))
* rename RevokeResult.Before to PriorDisposition ([0799984](https://github.com/oleg-tkachuk/limes/commit/0799984f056c20f201add1b4b61dad82f3117ea9))

## [0.121.0](https://github.com/oleg-tkachuk/limes/compare/v0.120.0...v0.121.0) (2026-10-24)

### ⚠ BREAKING CHANGES

* **biscuit:** return RevokeBiscuitResult from RevokeBiscuit

### Features

* **biscuit:** return RevokeBiscuitResult from RevokeBiscuit ([6e7b42c](https://github.com/oleg-tkachuk/limes/commit/6e7b42c92100e22ae17a09b91a0cd1f48bb8872a))

## [0.120.0](https://github.com/oleg-tkachuk/limes/compare/v0.119.0...v0.120.0) (2026-10-24)

### ⚠ BREAKING CHANGES

* **meter:** keep a charge 401 days, covering a leap year's bill

### Bug Fixes

* **meter:** keep a charge 401 days, covering a leap year's bill ([b9c5c65](https://github.com/oleg-tkachuk/limes/commit/b9c5c650ea6f06c6456abc118a6d1e2afbc49081))

## [0.119.0](https://github.com/oleg-tkachuk/limes/compare/v0.118.0...v0.119.0) (2026-10-24)

### ⚠ BREAKING CHANGES

* **cache:** answer RevocationLineage with a RevocationAnswer

### Features

* **cache:** answer RevocationLineage with a RevocationAnswer ([36bc37f](https://github.com/oleg-tkachuk/limes/commit/36bc37f8fdc5723e39af400eeb6e8919a3ce9ea2))

## [0.118.0](https://github.com/oleg-tkachuk/limes/compare/v0.117.0...v0.118.0) (2026-10-24)

### Features

* **meter:** bound the ledger's promises by MinChargeRetention ([631fc3d](https://github.com/oleg-tkachuk/limes/commit/631fc3dfa0a4dc8a63adb373bb273fb521ab923f))
* **meter:** remove charges past their retention with PurgeCharges ([21631d7](https://github.com/oleg-tkachuk/limes/commit/21631d7b92d5c17f3a1200146d88d6e53a18ed4c))

## [0.117.0](https://github.com/oleg-tkachuk/limes/compare/v0.116.0...v0.117.0) (2026-10-24)

### Features

* **cache:** hold SyncedTTL only while every revocation signal listens ([fef093f](https://github.com/oleg-tkachuk/limes/commit/fef093f505c8f9862d367e8163e36dc6ce04490e))

### Bug Fixes

* **cache:** bound the expiry order and drop expired answers before live ones ([98b409d](https://github.com/oleg-tkachuk/limes/commit/98b409dd26b59d393a155807f62d392f7c130e79))
* **cache:** keep no answer for a capability not on record ([feb5a73](https://github.com/oleg-tkachuk/limes/commit/feb5a73a051d10a0f56fbaeded25ca2dd5b7bc01))
* **cache:** never read a part of Clears above Clears ([0158545](https://github.com/oleg-tkachuk/limes/commit/01585452612451d1666aefa107e6f7fa15bdf996))
* **store:** write a revoke whatever its disposition, and hold storetest to the entry ([98525e1](https://github.com/oleg-tkachuk/limes/commit/98525e14167a19fb7c32b1113b8985786f01f04b))

## [0.116.0](https://github.com/oleg-tkachuk/limes/compare/v0.115.0...v0.116.0) (2026-10-22)

### Features

* **cache:** count a revocation signal's notifications and losses ([928b9b9](https://github.com/oleg-tkachuk/limes/commit/928b9b9e2a3c693f08d8ed53c99f9bcc81376adb))

## [0.115.0](https://github.com/oleg-tkachuk/limes/compare/v0.114.0...v0.115.0) (2026-10-22)

### Features

* **cache:** count the whole clears ClearRevoked makes for want of lineage ([77e208a](https://github.com/oleg-tkachuk/limes/commit/77e208ae589ad885a85fd9f917cc18160325c570))
* **cache:** let a consumer require and observe revocation lineage ([a750344](https://github.com/oleg-tkachuk/limes/commit/a750344c800cdb17892c2afbee3340c175392e8b))
* **cache:** serve a longer TTL only while a revocation signal vouches ([9935127](https://github.com/oleg-tkachuk/limes/commit/9935127bb2046772b312e61494c6c0a76787392e))
* **store:** report a capability's disposition before its revoke ([741620a](https://github.com/oleg-tkachuk/limes/commit/741620a25278567eb72568787938e29b7fa4f174))

## [0.114.0](https://github.com/oleg-tkachuk/limes/compare/v0.113.0...v0.114.0) (2026-10-22)

### Features

* **cache:** drop only the answers a revocation outdates ([0cc29a2](https://github.com/oleg-tkachuk/limes/commit/0cc29a275e52be657a69cb981c334fbe5b76658d))
* **listing:** name the revocation that stopped a revoked record ([0ffa0a6](https://github.com/oleg-tkachuk/limes/commit/0ffa0a609716ba2a1ac8e31d9e54a4cedb0e9f51))

## [0.113.0](https://github.com/oleg-tkachuk/limes/compare/v0.112.0...v0.113.0) (2026-10-22)

### ⚠ BREAKING CHANGES

* **store:** report whether Revoke found the capability revoked already

### Features

* **listing:** add RecordReporter, a lister that also reads one record ([f6eb408](https://github.com/oleg-tkachuk/limes/commit/f6eb408112fcecd6d3302df871845236891c8ce6))
* **listing:** tell a cursor of another listing from a malformed one ([bee07a0](https://github.com/oleg-tkachuk/limes/commit/bee07a0252a2ae9f7abd6a9c28a54f1abce050fd))
* **store:** report whether Revoke found the capability revoked already ([2ead111](https://github.com/oleg-tkachuk/limes/commit/2ead111479c51c5de068ca62b3f32f478a3490b9))

## [0.112.0](https://github.com/oleg-tkachuk/limes/compare/v0.111.0...v0.112.0) (2026-10-21)

### Features

* read one record with its disposition through RecordListingReader ([3c42e81](https://github.com/oleg-tkachuk/limes/commit/3c42e8164af2f57bdb42e27e4cf23449d5a382df))

### Bug Fixes

* refuse a ListRecords cursor handed back with other filters ([30c1fb9](https://github.com/oleg-tkachuk/limes/commit/30c1fb931de7ea1ed8bf3e521a7ec27ccc870dd6))

### Performance Improvements

* **pgstore:** read whether a record is revoked from the record ([ca94962](https://github.com/oleg-tkachuk/limes/commit/ca9496275b166ef664fcff3622972075bbfd1c4b))

## [0.111.0](https://github.com/oleg-tkachuk/limes/compare/v0.110.0...v0.111.0) (2026-10-21)

### Features

* list a tenant's capability records with their dispositions ([d49c685](https://github.com/oleg-tkachuk/limes/commit/d49c685ddb50fa92797cb073b9a7de70efe0da3e))
* **otelmetric:** report a RevocationCache's counts as observable instruments ([aa1aca3](https://github.com/oleg-tkachuk/limes/commit/aa1aca36659e33b04832053515d65921212366a9))

## [0.110.0](https://github.com/oleg-tkachuk/limes/compare/v0.109.2...v0.110.0) (2026-10-20)

### Features

* **biscuit:** mint a Biscuit form without OpShare ([6410850](https://github.com/oleg-tkachuk/limes/commit/641085087db9de4b7834288b0d135289c4ac0545))
* **cache:** count a RevocationCache's hits, misses, lookups, evictions and clears ([06b61f1](https://github.com/oleg-tkachuk/limes/commit/06b61f1aab42cc41ee6405e90b5613a9e16e1b7f))

### Bug Fixes

* **caveats:** name the built-in operations when refusing an unknown one ([411a5d8](https://github.com/oleg-tkachuk/limes/commit/411a5d83d740e03d0534896eb2fbfa1dd9af8979))

## [0.109.2](https://github.com/oleg-tkachuk/limes/compare/v0.109.1...v0.109.2) (2026-10-20)

### Performance Improvements

* shard the revocation answer cache and read its hits under a shared lock ([355c0de](https://github.com/oleg-tkachuk/limes/commit/355c0de18a9dc6912ba3748f864faeb972afaa93))

## [0.109.1](https://github.com/oleg-tkachuk/limes/compare/v0.109.0...v0.109.1) (2026-10-20)

### Bug Fixes

* refuse a nil upstream or a nil RevocationCache before the first lookup ([c7b9023](https://github.com/oleg-tkachuk/limes/commit/c7b9023be8ede031f423b45a9ad1330fe0575990))

## [0.109.0](https://github.com/oleg-tkachuk/limes/compare/v0.108.3...v0.109.0) (2026-10-20)

### ⚠ BREAKING CHANGES

* BiscuitRevocationCache, BiscuitRevocationCacheConfig and
  NewBiscuitRevocationCache are gone. RevocationCacheConfig.Upstream is now
  Capabilities, and its Biscuits field takes the Biscuit upstream; pass the
  one cache as both VerifierConfig.Revocations and BiscuitConfig.Revocations.

### Features

* one RevocationCache answers for capabilities and Biscuit copies ([b994327](https://github.com/oleg-tkachuk/limes/commit/b994327cdc6e1dec79afdcf51be8fdf4f8303450))

## [0.108.3](https://github.com/oleg-tkachuk/limes/compare/v0.108.2...v0.108.3) (2026-10-19)

### Bug Fixes

* **memstore:** count a copy under the ancestors it was admitted against ([aa8ca99](https://github.com/oleg-tkachuk/limes/commit/aa8ca99b4af319d3e483a161a333ccac0df26055))

## [0.108.2](https://github.com/oleg-tkachuk/limes/compare/v0.108.1...v0.108.2) (2026-10-19)

### Bug Fixes

* seal a key passed behind a pointer as a copy ([b280a61](https://github.com/oleg-tkachuk/limes/commit/b280a61cb4b02b7ac878ca11307a0c38b8ee1e15))

## [0.108.1](https://github.com/oleg-tkachuk/limes/releases/tag/v0.108.1)

## [0.108.0](https://github.com/oleg-tkachuk/limes/compare/v0.107.0...v0.108.0) (2026-10-19)

### ⚠ BREAKING CHANGES

* **meter:** keep a copy counted under the ancestors once its usage is dropped

### Features

* **meter:** keep a copy counted under the ancestors once its usage is dropped ([819b551](https://github.com/oleg-tkachuk/limes/commit/819b5518aef04b4c4e02f102d0552c92a493b019))

## [0.107.0](https://github.com/oleg-tkachuk/limes/compare/v0.106.1...v0.107.0) (2026-10-19)

### ⚠ BREAKING CHANGES

* **meter:** refuse a record for a tenant whose usage was erased

### Bug Fixes

* **meter:** refuse a record for a tenant whose usage was erased ([e021858](https://github.com/oleg-tkachuk/limes/commit/e021858e8763baa44a532be112f1620fcdd72212))

## [0.106.1](https://github.com/oleg-tkachuk/limes/compare/v0.106.0...v0.106.1) (2026-10-19)

### Bug Fixes

* **dpop:** issue no nonce from a zero HMACNonces rather than panic ([a872712](https://github.com/oleg-tkachuk/limes/commit/a872712b05d8425c200e93a3dbd7eef2e82980df))

## [0.106.0](https://github.com/oleg-tkachuk/limes/compare/v0.105.0...v0.106.0) (2026-10-19)

### ⚠ BREAKING CHANGES

* **meter:** a UsageStore checks the subtree of every capability on the
  chain against its quota; see docs/migration.md.

### Features

* **meter:** count Biscuit copies against every quota on the delegation chain ([22adfbe](https://github.com/oleg-tkachuk/limes/commit/22adfbe858c7b81567aae540535eb9343f258529))

## [0.105.0](https://github.com/oleg-tkachuk/limes/compare/v0.104.1...v0.105.0) (2026-10-19)

### ⚠ BREAKING CHANGES

* **redact:** SignerConfig.Key and SigningKey.Private are a SealedKey
  (limes.SealKey), and HMACNoncesConfig.Keys a dpop.NonceKeys
  (dpop.SealNonceKeys).

### Bug Fixes

* **redact:** seal the keys of SigningKey, SignerConfig and HMACNoncesConfig ([61378bf](https://github.com/oleg-tkachuk/limes/commit/61378bfc6a4fad94ef2a1ac2737cf40457a05c48))

## [0.104.1](https://github.com/oleg-tkachuk/limes/compare/v0.104.0...v0.104.1) (2026-10-19)

### Performance Improvements

* **memstore:** count Biscuit copies per capability ([b637e6b](https://github.com/oleg-tkachuk/limes/commit/b637e6b698ad7248e5891ab0889d0b03186ec02e))

## [0.104.0](https://github.com/oleg-tkachuk/limes/compare/v0.103.0...v0.104.0) (2026-10-19)

### ⚠ BREAKING CHANGES

* **token:** tokens are limes-cap-v6+jwt and v5 is refused; a zero
  Caveats.Copies counts no copy with limits of its own. See docs/migration.md.

### Features

* **token:** make the quota of Biscuit copies a signed caveat ([59380db](https://github.com/oleg-tkachuk/limes/commit/59380db6346d33279b1a2307d57eb004baba9691))

## [0.103.0](https://github.com/oleg-tkachuk/limes/compare/v0.102.0...v0.103.0) (2026-10-19)

### ⚠ BREAKING CHANGES

* **errors:** ErrUnitCodeMismatch matches ErrConflict and ErrorResponse
  answers it 409.

### Bug Fixes

* **errors:** make ErrUnitCodeMismatch a conflict ([9e3122b](https://github.com/oleg-tkachuk/limes/commit/9e3122b8dc4bc0f4585f3d2902bc3e68ba0bcabd))

## [0.102.0](https://github.com/oleg-tkachuk/limes/compare/v0.101.1...v0.102.0) (2026-10-19)

### ⚠ BREAKING CHANGES

* **meter:** SumChargesRequest's ByOp, ByActor and ByCapability are
  replaced by GroupBy; see docs/migration.md.

### Features

* **meter:** sum the charges ledger across tenants by any dimension ([4687cea](https://github.com/oleg-tkachuk/limes/commit/4687cea1e3a7a8ff78c6fc51389c2db6b2f21746))

## [0.101.1](https://github.com/oleg-tkachuk/limes/compare/v0.101.0...v0.101.1) (2026-10-19)

### Bug Fixes

* **redact:** keep signing and nonce keys out of every carrier's formatting ([11da74d](https://github.com/oleg-tkachuk/limes/commit/11da74d19c8d18f99775be29ce0a68664559207a))

## [0.101.0](https://github.com/oleg-tkachuk/limes/compare/v0.100.0...v0.101.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* **meter:** Meter, UsageStore and Charged lose their type parameter and
  Charged.Tx; memstore and metertest drop theirs and TxFactory.

### Features

* **meter:** carry the store's transaction in the onCharged context ([88045d9](https://github.com/oleg-tkachuk/limes/commit/88045d98b2f1743b540ac89143e71255a7c333aa))

## [0.100.0](https://github.com/oleg-tkachuk/limes/compare/v0.99.1...v0.100.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* **meter:** the usage store, ledger, their types and sentinels moved
  from limes to limes/meter; see docs/migration.md.

### Features

* **meter:** move metering into the meter package ([0037d30](https://github.com/oleg-tkachuk/limes/commit/0037d3071235fcb3e73f93093754c031ed7e3c2c))

## [0.99.1](https://github.com/oleg-tkachuk/limes/compare/v0.99.0...v0.99.1) (2026-10-18)

### Bug Fixes

* **biscuit:** read padded base64url written by other implementations ([12b0c3d](https://github.com/oleg-tkachuk/limes/commit/12b0c3d3bda35b65dcd3db8d18cf8967ae9a1558))

## [0.99.0](https://github.com/oleg-tkachuk/limes/compare/v0.98.0...v0.99.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* refuse inserting a child whose budget counts in another unit

### Bug Fixes

* refuse inserting a child whose budget counts in another unit ([1718f64](https://github.com/oleg-tkachuk/limes/commit/1718f64bee16260d896202587af7738ffd626ffa))

## [0.98.0](https://github.com/oleg-tkachuk/limes/compare/v0.97.0...v0.98.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* count at most a quota of Biscuit copies per capability

### Features

* count at most a quota of Biscuit copies per capability ([09c8c2f](https://github.com/oleg-tkachuk/limes/commit/09c8c2f8fe4f963e69aeabe04f5c00cb45eda7cb))

## [0.97.0](https://github.com/oleg-tkachuk/limes/compare/v0.96.0...v0.97.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* refuse settling a reservation again for another amount

### Bug Fixes

* refuse settling a reservation again for another amount ([d38bd41](https://github.com/oleg-tkachuk/limes/commit/d38bd4150f17a31a1663c35db730c7c2b3e05ed7))

## [0.96.0](https://github.com/oleg-tkachuk/limes/compare/v0.95.8...v0.96.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* require OpShare for a capability's Biscuit form

### Bug Fixes

* require OpShare for a capability's Biscuit form ([77a383e](https://github.com/oleg-tkachuk/limes/commit/77a383e05317ea45f3538479360f24746b143b32))

## [0.95.8](https://github.com/oleg-tkachuk/limes/compare/v0.95.7...v0.95.8) (2026-10-18)

### Bug Fixes

* **ci:** fail the marker check when the release analyzer cannot run ([1bca72d](https://github.com/oleg-tkachuk/limes/commit/1bca72d5a55f69785425c090912a6840bb6b52ee))

## [0.95.7](https://github.com/oleg-tkachuk/limes/compare/v0.95.6...v0.95.7) (2026-10-18)

### Bug Fixes

* **ci:** keep the merge queue to pull requests merge-green will merge ([ba63121](https://github.com/oleg-tkachuk/limes/commit/ba63121fb531db91f86b1764d4a34be0988825b0))

## [0.95.6](https://github.com/oleg-tkachuk/limes/compare/v0.95.5...v0.95.6) (2026-10-18)

### Bug Fixes

* **dpop:** redact a copy of HMACNonces as the pointer is ([7018adf](https://github.com/oleg-tkachuk/limes/commit/7018adf85bdc31191aaade0528d377b39254fd52))

## [0.95.5](https://github.com/oleg-tkachuk/limes/compare/v0.95.4...v0.95.5) (2026-10-18)

### Bug Fixes

* **ci:** keep the merge queue moving past superseded runs and stale bases ([aa23e8a](https://github.com/oleg-tkachuk/limes/commit/aa23e8a1e9974262403328d299b041ae25e16d9c))

## [0.95.4](https://github.com/oleg-tkachuk/limes/compare/v0.95.3...v0.95.4) (2026-10-18)

### Bug Fixes

* **ci:** leave more source-changing paths to a person ([a38ee9b](https://github.com/oleg-tkachuk/limes/commit/a38ee9ba46990fedc88f3d26300c84b51ae5703e))
* refuse a delegated agent run that is its parent's own ([b38b351](https://github.com/oleg-tkachuk/limes/commit/b38b351abe6c35d42ed7ac090113891c074d8601))

## [0.95.3](https://github.com/oleg-tkachuk/limes/compare/v0.95.2...v0.95.3) (2026-10-18)

### Bug Fixes

* answer the ErrConflict family 409, not 500 ([79e69eb](https://github.com/oleg-tkachuk/limes/commit/79e69eb7e54ef487aff9fd16000c125d15610e08))

## [0.95.2](https://github.com/oleg-tkachuk/limes/compare/v0.95.1...v0.95.2) (2026-10-18)

### Bug Fixes

* read claim ids only as canonical UUIDs and refuse an empty cnf ([f59bfc4](https://github.com/oleg-tkachuk/limes/commit/f59bfc48852b3e4b0feb984715ab573cd2a8d4e4))

## [0.95.1](https://github.com/oleg-tkachuk/limes/compare/v0.95.0...v0.95.1) (2026-10-18)

### Bug Fixes

* seed a Biscuit set's ids no later than its answer expires ([abf907f](https://github.com/oleg-tkachuk/limes/commit/abf907f0c9d22dac0af9bc3ea05e47ac9b5f5c97))

## [0.95.0](https://github.com/oleg-tkachuk/limes/compare/v0.94.1...v0.95.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* refuse a reservation for a capability already expired

### Bug Fixes

* **jwks:** refuse a document with no keys array ([3c3bc2e](https://github.com/oleg-tkachuk/limes/commit/3c3bc2e2000250c73b7124ab972335d18991b30d))
* refuse a reservation for a capability already expired ([519f888](https://github.com/oleg-tkachuk/limes/commit/519f888e5dd0a0bb938702d4a04cbfff7a6d1ca2))

## [0.94.1](https://github.com/oleg-tkachuk/limes/compare/v0.94.0...v0.94.1) (2026-10-18)

### Bug Fixes

* **dpop:** redact HMAC nonce keys when formatted or logged ([e13adc1](https://github.com/oleg-tkachuk/limes/commit/e13adc1b0b53f67f3d6ca5c4d22f9066d865bb10))

## [0.94.0](https://github.com/oleg-tkachuk/limes/compare/v0.93.4...v0.94.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* **pgstore:** mark an erased tenant and refuse metering it
* refuse metering a tenant whose usage was erased

### Bug Fixes

* **pgstore:** mark an erased tenant and refuse metering it ([2b096cd](https://github.com/oleg-tkachuk/limes/commit/2b096cd82f5b20b7e777795fde98f04e38fd3594))
* refuse metering a tenant whose usage was erased ([c56bcbc](https://github.com/oleg-tkachuk/limes/commit/c56bcbc9f31bf667565744e74ec4df143f6c0a38))

## [0.93.4](https://github.com/oleg-tkachuk/limes/compare/v0.93.3...v0.93.4) (2026-10-18)

### Bug Fixes

* **ci:** treat a renamed CI definition as a change to CI ([8ae9790](https://github.com/oleg-tkachuk/limes/commit/8ae9790ec2b54f230b6fec1e5f03f67baa672b11))

## [0.93.3](https://github.com/oleg-tkachuk/limes/compare/v0.93.2...v0.93.3) (2026-10-18)

### Bug Fixes

* **ci:** count a breaking marker only where its module's release counts it ([16e7623](https://github.com/oleg-tkachuk/limes/commit/16e7623f99e4d0a4d31d5325887a9f00b1320c92))

## [0.93.2](https://github.com/oleg-tkachuk/limes/compare/v0.93.1...v0.93.2) (2026-10-18)

### Bug Fixes

* name an unknown auth scheme only when a space follows it ([f3e3c2f](https://github.com/oleg-tkachuk/limes/commit/f3e3c2f43568d8f7431901b795cc345291963917))

## [0.93.1](https://github.com/oleg-tkachuk/limes/compare/v0.93.0...v0.93.1) (2026-10-18)

### Bug Fixes

* check Authorize caveats before recording the DPoP proof ([8c0abe0](https://github.com/oleg-tkachuk/limes/commit/8c0abe07ab6fa539b276834b652293a9bc6ffe13))

## [0.93.0](https://github.com/oleg-tkachuk/limes/compare/v0.92.0...v0.93.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* count and revoke biscuit copies per capability
* **pgstore:** key copy counters and revocations by capability

### Bug Fixes

* count and revoke biscuit copies per capability ([357f769](https://github.com/oleg-tkachuk/limes/commit/357f7693662287f31574d4a50fd612b26d253c2c))
* **pgstore:** key copy counters and revocations by capability ([0e194a2](https://github.com/oleg-tkachuk/limes/commit/0e194a2fea1991c448c000973b1824204028913f))

## [0.92.0](https://github.com/oleg-tkachuk/limes/compare/v0.91.0...v0.92.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* **store:** refuse to record a capability that never expires

### Bug Fixes

* **store:** refuse to record a capability that never expires ([659a91d](https://github.com/oleg-tkachuk/limes/commit/659a91d21165dd3dea8d8843b7825b1a003aebb7))

## [0.91.0](https://github.com/oleg-tkachuk/limes/compare/v0.90.0...v0.91.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* **token:** retire token type v4

### Features

* **token:** retire token type v4 ([e36b3de](https://github.com/oleg-tkachuk/limes/commit/e36b3debf05a9a5646808915d6486518e4b9c76c))

## [0.90.0](https://github.com/oleg-tkachuk/limes/compare/v0.89.0...v0.90.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* validate the requests that named nothing to act on

### Features

* validate the requests that named nothing to act on ([c7d4581](https://github.com/oleg-tkachuk/limes/commit/c7d4581b6e0be68532ad19d36ff3649f163aa8b7))

## [0.89.0](https://github.com/oleg-tkachuk/limes/compare/v0.88.0...v0.89.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* make ChargeBucketNone the zero bucket and number buckets shortest first

### Code Refactoring

* make ChargeBucketNone the zero bucket and number buckets shortest first ([ec0c79e](https://github.com/oleg-tkachuk/limes/commit/ec0c79ecbb9b7e3ad4f1afeee860432125ba8078))

## [0.88.0](https://github.com/oleg-tkachuk/limes/compare/v0.87.0...v0.88.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* take a tenant budget threshold in integer basis points

### Code Refactoring

* take a tenant budget threshold in integer basis points ([b0b2956](https://github.com/oleg-tkachuk/limes/commit/b0b2956cb0a1c6c393cf7d2bfc3346b062695a48))

## [0.87.0](https://github.com/oleg-tkachuk/limes/compare/v0.86.0...v0.87.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* gather a verifier's Biscuit settings in BiscuitConfig
* **dpop:** rename ProofRequest to SignRequest

### Code Refactoring

* **dpop:** rename ProofRequest to SignRequest ([68e81c3](https://github.com/oleg-tkachuk/limes/commit/68e81c3a47a6842df447095842e9629e810fc03d))
* gather a verifier's Biscuit settings in BiscuitConfig ([72f6a0f](https://github.com/oleg-tkachuk/limes/commit/72f6a0f9478071043306051d63d813dc1ea3effb))

## [0.86.0](https://github.com/oleg-tkachuk/limes/compare/v0.85.0...v0.86.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* rename the revocation caches after their configs

### Code Refactoring

* rename the revocation caches after their configs ([05a066e](https://github.com/oleg-tkachuk/limes/commit/05a066ea6266431f9b4b5aa6f995cec4c580dca4))

## [0.85.0](https://github.com/oleg-tkachuk/limes/compare/v0.84.0...v0.85.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* move Instrument to the otelmetric module

### Code Refactoring

* move Instrument to the otelmetric module ([83f2a55](https://github.com/oleg-tkachuk/limes/commit/83f2a55820b29d4cd03210c714e1f552e3b3e7a6))

## [0.84.0](https://github.com/oleg-tkachuk/limes/compare/v0.83.0...v0.84.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* name a refusal's reason in its problem body and type

### Features

* name a refusal's reason in its problem body and type ([eacf88a](https://github.com/oleg-tkachuk/limes/commit/eacf88a48e2c30bb5f4580c26d06397a6f1c69d0))

## [0.83.0](https://github.com/oleg-tkachuk/limes/compare/v0.82.0...v0.83.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* move a Biscuit copy's limits and root off Capability

### Code Refactoring

* move a Biscuit copy's limits and root off Capability ([45e45f0](https://github.com/oleg-tkachuk/limes/commit/45e45f0245a5c8d514437103d27e10762fbe1ce2))

## [0.82.0](https://github.com/oleg-tkachuk/limes/compare/v0.81.0...v0.82.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* add ErrConflict and move the conflicts under it

### Code Refactoring

* add ErrConflict and move the conflicts under it ([b227260](https://github.com/oleg-tkachuk/limes/commit/b22726000ca348a02412bd61b3f5d9207cbdacec))

## [0.81.0](https://github.com/oleg-tkachuk/limes/compare/v0.80.0...v0.81.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* **token:** add token type v5 without limes_gen and the domain operations
* **token:** derive a delegated agent's ParentRunID from its parent's run
* **pgstore:** refuse ListTenantBudgets under a scope narrower than every tenant

### Features

* **token:** add token type v5 without limes_gen and the domain operations ([549eff7](https://github.com/oleg-tkachuk/limes/commit/549eff7a255e21435c7222f37a352484c4210944))
* **token:** derive a delegated agent's ParentRunID from its parent's run ([7dae06b](https://github.com/oleg-tkachuk/limes/commit/7dae06b5a7ec7b8df4ab331155413256263e278f))

### Bug Fixes

* **pgstore:** refuse ListTenantBudgets under a scope narrower than every tenant ([a10a39b](https://github.com/oleg-tkachuk/limes/commit/a10a39b9c2482bad20563f041cd2d10cc4f61e3b))

## [0.80.0](https://github.com/oleg-tkachuk/limes/compare/v0.79.1...v0.80.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* **meter:** let a revoked capability's hold be settled until it expires

### Bug Fixes

* **meter:** let a revoked capability's hold be settled until it expires ([62bc3b9](https://github.com/oleg-tkachuk/limes/commit/62bc3b94dab83be5056b7c8f4e0761c60e5c782d))

## [0.79.1](https://github.com/oleg-tkachuk/limes/compare/v0.79.0...v0.79.1) (2026-10-18)

### Bug Fixes

* **examples:** print a ceiling's unit once ([c560319](https://github.com/oleg-tkachuk/limes/commit/c560319aa02d8d8f5c87c98bb867f8da47dca0f3))
* **memstore:** date the tenant aggregate when a hold changes it ([92893d2](https://github.com/oleg-tkachuk/limes/commit/92893d2d5e3c5dd4f0749c724d944b488ca85f65))

## [0.79.0](https://github.com/oleg-tkachuk/limes/compare/v0.78.2...v0.79.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* **inspect:** tell a wrong public key from a forged token

### Bug Fixes

* **inspect:** tell a wrong public key from a forged token ([c4655c8](https://github.com/oleg-tkachuk/limes/commit/c4655c85b9d8a7dffe4addfe9bd44b19c8e4e817))

## [0.78.2](https://github.com/oleg-tkachuk/limes/compare/v0.78.1...v0.78.2) (2026-10-18)

### Performance Improvements

* **revocation:** ask about a token's uncached Biscuit ids in one lookup ([32b9893](https://github.com/oleg-tkachuk/limes/commit/32b989353cff48a5a66f9b2085a09dcab9204bd3))

## [0.78.1](https://github.com/oleg-tkachuk/limes/compare/v0.78.0...v0.78.1) (2026-10-18)

### Bug Fixes

* **resource:** read a segment up to its path parameters when looking for a dot segment ([3779676](https://github.com/oleg-tkachuk/limes/commit/3779676c912ef4a6b5beb763a25e57218d206edc))

## [0.78.0](https://github.com/oleg-tkachuk/limes/compare/v0.77.4...v0.78.0) (2026-10-18)

### ⚠ BREAKING CHANGES

* **meter:** end a hold with its capability, and release a revoked one's

### Bug Fixes

* **meter:** end a hold with its capability, and release a revoked one's ([8672959](https://github.com/oleg-tkachuk/limes/commit/867295993ddcdaba150183346f2778a597930450))

## [0.77.4](https://github.com/oleg-tkachuk/limes/compare/v0.77.3...v0.77.4) (2026-10-18)

### Bug Fixes

* **meter:** refuse an ExternalRef replay under other Biscuit copies ([6e5fb00](https://github.com/oleg-tkachuk/limes/commit/6e5fb005fdfc38fb4a6d96fcd30d7c38bd757d45))
* **verifier:** refuse an unmetered copy before recording its DPoP proof ([96f66fb](https://github.com/oleg-tkachuk/limes/commit/96f66fbdfe55b572bce81cfb031a26322773485e))

## [0.77.3](https://github.com/oleg-tkachuk/limes/compare/v0.77.2...v0.77.3) (2026-10-18)

### Bug Fixes

* **redact:** keep private keys out of encoding/json and slog ([9bef80c](https://github.com/oleg-tkachuk/limes/commit/9bef80cd481dbbcb6150116ff40ac59786660fca))

## [0.77.2](https://github.com/oleg-tkachuk/limes/compare/v0.77.1...v0.77.2) (2026-10-18)

### Bug Fixes

* **authorization:** keep the credential out of an unknown-scheme error ([bb7f9bb](https://github.com/oleg-tkachuk/limes/commit/bb7f9bbd61c1247b7f1ad2aa6a16a56c392825cd))

## [0.77.1](https://github.com/oleg-tkachuk/limes/compare/v0.77.0...v0.77.1) (2026-10-18)

### Bug Fixes

* **resource:** refuse a dot segment however its second encoding is spelled ([29a39c5](https://github.com/oleg-tkachuk/limes/commit/29a39c514111493850ff9dd0b354898b030daf6e))

## [0.77.0](https://github.com/oleg-tkachuk/limes/compare/v0.76.0...v0.77.0) (2026-10-17)

### ⚠ BREAKING CHANGES

* carry the unit in BudgetCeiling

### Features

* carry the unit in BudgetCeiling ([7472e73](https://github.com/oleg-tkachuk/limes/commit/7472e73a67f9c597221826841f0564730c319650))

## [0.76.0](https://github.com/oleg-tkachuk/limes/compare/v0.75.0...v0.76.0) (2026-10-17)

### ⚠ BREAKING CHANGES

* refuse a budget that names no unit

### Bug Fixes

* refuse a budget that names no unit ([31ba33c](https://github.com/oleg-tkachuk/limes/commit/31ba33cd175d7fb8b0aa5f99f93e826b7689815c))

## [0.75.0](https://github.com/oleg-tkachuk/limes/releases/tag/v0.75.0)

## [0.74.0](https://github.com/oleg-tkachuk/limes/compare/v0.73.0...v0.74.0) (2026-10-17)

### ⚠ BREAKING CHANGES

* name every enum constant after its type

### Code Refactoring

* name every enum constant after its type ([b09342a](https://github.com/oleg-tkachuk/limes/commit/b09342a5746ec6b865fa3199fb89cf8690bb2c2b))

## [0.73.0](https://github.com/oleg-tkachuk/limes/compare/v0.72.0...v0.73.0) (2026-10-17)

### ⚠ BREAKING CHANGES

* give ChargeBucket an explicit unspecified zero

### Bug Fixes

* give ChargeBucket an explicit unspecified zero ([63594ca](https://github.com/oleg-tkachuk/limes/commit/63594ca6937924e7431f0e3e0615bfecd684cbe4))

## [0.72.0](https://github.com/oleg-tkachuk/limes/compare/v0.71.0...v0.72.0) (2026-10-17)

### Features

* **dpop:** split Verify into Check and Record ([6583cf5](https://github.com/oleg-tkachuk/limes/commit/6583cf58d2bf17660a092fcc22712eef9786ed5e))

### Bug Fixes

* **verify:** check the dpop proof before the revocation lookup ([5ca48cd](https://github.com/oleg-tkachuk/limes/commit/5ca48cdefd70eaab5e7870a2ef0302d43d6f77d4))

## [0.71.0](https://github.com/oleg-tkachuk/limes/compare/v0.70.1...v0.71.0) (2026-10-17)

### ⚠ BREAKING CHANGES

* derive SigningKey's public key from its private key
* **pgstore:** hand EraseTenant's callback the erase result

### Features

* **pgstore:** hand EraseTenant's callback the erase result ([07926b0](https://github.com/oleg-tkachuk/limes/commit/07926b01179382ba9a9522e06cb9793f4afba805))

### Bug Fixes

* derive SigningKey's public key from its private key ([081e59c](https://github.com/oleg-tkachuk/limes/commit/081e59c34a167d9c0c1fefa880fb4bc3e3167a15))

## [0.70.1](https://github.com/oleg-tkachuk/limes/compare/v0.70.0...v0.70.1) (2026-10-17)

### Bug Fixes

* **memstore:** drop Capability.Copies on Insert ([b931fe3](https://github.com/oleg-tkachuk/limes/commit/b931fe332a93a61e1c46d7976ca30ea9e33706e1))

## [0.70.0](https://github.com/oleg-tkachuk/limes/compare/v0.69.0...v0.70.0) (2026-10-17)

### ⚠ BREAKING CHANGES

* make ErrMalformedAuthorization match ErrInvalidRequest

### Bug Fixes

* make ErrMalformedAuthorization match ErrInvalidRequest ([1d9d473](https://github.com/oleg-tkachuk/limes/commit/1d9d473e32781906194166b4ee7b555eb097d288))

## [0.69.0](https://github.com/oleg-tkachuk/limes/compare/v0.68.0...v0.69.0) (2026-10-17)

### ⚠ BREAKING CHANGES

* make the remaining growable results not comparable

### Features

* make the remaining growable results not comparable ([4be1f77](https://github.com/oleg-tkachuk/limes/commit/4be1f77bc5b6a63ba3bcce7f98903de62bc894b4))

## [0.68.0](https://github.com/oleg-tkachuk/limes/compare/v0.67.0...v0.68.0) (2026-10-17)

### Features

* **meter:** validate the meter's requests through Validate ([bfd93c2](https://github.com/oleg-tkachuk/limes/commit/bfd93c253116f45c855e45f25ce5aa73bd5238f9))

## [0.67.0](https://github.com/oleg-tkachuk/limes/compare/v0.66.1...v0.67.0) (2026-10-17)

### ⚠ BREAKING CHANGES

* **pgstore:** hand onCharged the receipt of the charge it runs in
* **meter:** hand onCharged the receipt of the charge it runs in

### Features

* **meter:** hand onCharged the receipt of the charge it runs in ([268510f](https://github.com/oleg-tkachuk/limes/commit/268510f78a7f6d3613b4efebbefea5333fe90306))
* **pgstore:** hand onCharged the receipt of the charge it runs in ([b45b48a](https://github.com/oleg-tkachuk/limes/commit/b45b48a7478e172a441f3312bfc1afceacf81436))

### Bug Fixes

* **jwks:** mask the key set URL's password in every error ([6efda3d](https://github.com/oleg-tkachuk/limes/commit/6efda3dfebe5fbd9862b5c1e34b28c728131b0b0))
* **memstore:** settle a hold only under the tenant that made it ([e010a08](https://github.com/oleg-tkachuk/limes/commit/e010a08d184a41aa22f42d862293e4eae7a8695d))
* refuse a mistyped part of a linked MeterSubject ([2a1e9a0](https://github.com/oleg-tkachuk/limes/commit/2a1e9a0487e6c13ebd1abd258d0680cc17fdd365))

## [0.66.1](https://github.com/oleg-tkachuk/limes/compare/v0.66.0...v0.66.1) (2026-10-17)

### Bug Fixes

* **deps:** require biscuit-go by its eclipse-biscuit module path ([df2a92b](https://github.com/oleg-tkachuk/limes/commit/df2a92b524b5c38522eb10bd9a6e2f48cf99526f))

## [0.66.0](https://github.com/oleg-tkachuk/limes/compare/v0.65.0...v0.66.0) (2026-10-15)

### ⚠ BREAKING CHANGES

* **dpop:** match the root path only exactly under MatchPathOnly
* **resource:** refuse a dot segment encoded twice
* **inspect:** refuse a token over MaxTokenBytes unread

### Bug Fixes

* **dpop:** match the root path only exactly under MatchPathOnly ([5c7fc46](https://github.com/oleg-tkachuk/limes/commit/5c7fc464da204fc3c523e608f2662c58cf8be691))
* **inspect:** refuse a token over MaxTokenBytes unread ([5a968c4](https://github.com/oleg-tkachuk/limes/commit/5a968c49584ac7f5316a187fd74c0da5b878098c))
* **resource:** refuse a dot segment encoded twice ([c85eb24](https://github.com/oleg-tkachuk/limes/commit/c85eb24428c7cc4e2cb1d92bf82f53c8c0ba1904))
* **signer:** refuse a signature the key's public key does not verify ([a1160c8](https://github.com/oleg-tkachuk/limes/commit/a1160c849df14ef757a1d9a3d11a3b2cbeb2ac05))

## [0.65.0](https://github.com/oleg-tkachuk/limes/compare/v0.64.3...v0.65.0) (2026-10-15)

### ⚠ BREAKING CHANGES

* **jwks:** fetch a key set over https only, and only from its origin
* **store:** refuse a child with no expiry under a parent that has one
* **meter:** release a reservation only for the subject that made it

### Bug Fixes

* **jose:** refuse a line break in a base64url segment ([84bcdca](https://github.com/oleg-tkachuk/limes/commit/84bcdca8e04e5bb6e9cb8599606f770ba737e957))
* **jwks:** fetch a key set over https only, and only from its origin ([84b662b](https://github.com/oleg-tkachuk/limes/commit/84b662b2c502a1af21307743106ee4a1d547b14d))
* **meter:** release a reservation only for the subject that made it ([50ef174](https://github.com/oleg-tkachuk/limes/commit/50ef1740a858615eba1981ce4b6281a31e668cbc))
* **signer:** print a signing key's private half as redacted ([3934160](https://github.com/oleg-tkachuk/limes/commit/3934160047bad3be231b3082aad7fda494df64bd))
* **store:** refuse a child with no expiry under a parent that has one ([cc8e85e](https://github.com/oleg-tkachuk/limes/commit/cc8e85e2a10f1c558cdd328bbe16c4fb1c804503))

## [0.64.3](https://github.com/oleg-tkachuk/limes/compare/v0.64.2...v0.64.3) (2026-10-15)

### Bug Fixes

* **verifier:** check revocation before recording a DPoP proof ([3faefe3](https://github.com/oleg-tkachuk/limes/commit/3faefe3980f2a5991e4d2665655ce6e88098daac))

## [0.64.2](https://github.com/oleg-tkachuk/limes/compare/v0.64.1...v0.64.2) (2026-10-14)

### Bug Fixes

* **jwks:** refuse a handler max-age under a second ([cd8f065](https://github.com/oleg-tkachuk/limes/commit/cd8f065718cb333245a94d22423e9bd16d66cce4))

## [0.64.1](https://github.com/oleg-tkachuk/limes/compare/v0.64.0...v0.64.1) (2026-10-14)

### Bug Fixes

* **release:** release a nested module's own build change as a patch ([2f84522](https://github.com/oleg-tkachuk/limes/commit/2f845229b05f949fc4ebfd0dde6d1ca15d1bf416))

## [0.64.0](https://github.com/oleg-tkachuk/limes/compare/v0.63.0...v0.64.0) (2026-10-14)

### Features

* add RecordCensus to count capability records by disposition ([c177588](https://github.com/oleg-tkachuk/limes/commit/c177588117f531f4472d0265b2773e331b090d06))
* **memstore:** count records with NewCensus ([6c1ab88](https://github.com/oleg-tkachuk/limes/commit/6c1ab88ccfe1cd037ff1b061cdb87ddaac654999))
* **storetest:** check a RecordCensus the Env names ([bd4c8ff](https://github.com/oleg-tkachuk/limes/commit/bd4c8fff968bc0507cc02d390fcaa79ec19eeae6))

## [0.63.0](https://github.com/oleg-tkachuk/limes/compare/v0.62.0...v0.63.0) (2026-10-14)

### Features

* **metertest:** hold a store to refusing every write under a read-only scope ([66277f3](https://github.com/oleg-tkachuk/limes/commit/66277f35612d3680a4bbef8e4b3f262e5a06de25))
* **storetest:** hold a store to refusing every write under a read-only scope ([0ac0fb9](https://github.com/oleg-tkachuk/limes/commit/0ac0fb97195d7b5ddd9ddc53732e80cd97e0473c))

## [0.62.0](https://github.com/oleg-tkachuk/limes/compare/v0.61.1...v0.62.0) (2026-10-14)

### Features

* **ledger:** add an ISO week bucket to SumCharges ([38c0d4e](https://github.com/oleg-tkachuk/limes/commit/38c0d4e945eec6a4ce7cc350e1ab56013364a556))

## [0.61.1](https://github.com/oleg-tkachuk/limes/compare/v0.61.0...v0.61.1) (2026-10-14)

### Bug Fixes

* **metertest:** check SumCharges orders sums by amount, not by digits ([e0439d4](https://github.com/oleg-tkachuk/limes/commit/e0439d4d8e7858452f200640bb5111a49b3e51e0))

## [0.61.0](https://github.com/oleg-tkachuk/limes/compare/v0.60.1...v0.61.0) (2026-10-14)

### Features

* add LedgerReader to sum a tenant's charges ([0a5a1e0](https://github.com/oleg-tkachuk/limes/commit/0a5a1e04752a069a73a97300d9491b279d317eb3))

## [0.60.1](https://github.com/oleg-tkachuk/limes/compare/v0.60.0...v0.60.1) (2026-10-14)

### Bug Fixes

* **memstore:** record the subject's unit on Bump ([2acdb23](https://github.com/oleg-tkachuk/limes/commit/2acdb233c2de0752d57d4605607e8d245cc11740))

## [0.60.0](https://github.com/oleg-tkachuk/limes/compare/v0.59.0...v0.60.0) (2026-10-14)

### Features

* add optional erasers of a tenant's records and usage ([a97dc3e](https://github.com/oleg-tkachuk/limes/commit/a97dc3e62ed549e61bb91e308b091530fc8d388c))

## [0.59.0](https://github.com/oleg-tkachuk/limes/compare/v0.58.0...v0.59.0) (2026-10-14)

### Features

* **storetest:** check that a call scoped to another tenant is refused ([d8eccb5](https://github.com/oleg-tkachuk/limes/commit/d8eccb5046796df06a056e25f6be897a39a70738))

## [0.58.0](https://github.com/oleg-tkachuk/limes/compare/v0.57.0...v0.58.0) (2026-10-14)

### Features

* add ErrScopeMismatch for a call scoped to another tenant ([ad20188](https://github.com/oleg-tkachuk/limes/commit/ad201880444ffec77e4dea19b663f04e1341d3e6))

## [0.57.0](https://github.com/oleg-tkachuk/limes/compare/v0.56.0...v0.57.0) (2026-10-13)

### ⚠ BREAKING CHANGES

* **store:** Store.Insert refuses a child with more than MaxDelegationDepth ancestors on record as ErrDelegationTooDeep; storetest and metertest check it.
* **meter:** TenantBudgetsUnspecified is the zero and lists every tenant; TenantBudgetsAll, TenantBudgetsAtOrPast and TenantBudgetsUnlimited each moved up by one.

### Features

* **pgstore:** add a PostgreSQL store of capability records ([4145be8](https://github.com/oleg-tkachuk/limes/commit/4145be8cb2acc1bbdd33a5d7dd9c0301bc653247))

### Bug Fixes

* **meter:** give TenantBudgetFilter an explicit unspecified zero ([ec5a66a](https://github.com/oleg-tkachuk/limes/commit/ec5a66a27823c274723e03695798f45718f8741c))
* **store:** refuse a delegation past MaxDelegationDepth on insert ([57db0e0](https://github.com/oleg-tkachuk/limes/commit/57db0e02b33763d9a059ad7a63d0065fae2e3a07))

## [0.56.0](https://github.com/oleg-tkachuk/limes/compare/v0.55.0...v0.56.0) (2026-10-13)

### Features

* **redisreplay:** add a dpop.ReplayCache over redis ([9f98f38](https://github.com/oleg-tkachuk/limes/commit/9f98f38ffccae4dbb4ec8315dd3f3cf8a59ef16b))

## [0.55.0](https://github.com/oleg-tkachuk/limes/compare/v0.54.0...v0.55.0) (2026-10-13)

### ⚠ BREAKING CHANGES

* **store:** Store.Insert refuses a child in another tenant than its
  parent as ErrDelegationTooWide; storetest checks it.

### Bug Fixes

* **store:** refuse a child recorded in another tenant than its parent ([b950195](https://github.com/oleg-tkachuk/limes/commit/b950195594bcaf7ab57ef8bfbff0c202893922db))

## [0.54.0](https://github.com/oleg-tkachuk/limes/compare/v0.53.2...v0.54.0) (2026-10-13)

### Features

* bound each contract check, so a deadlocked store fails fast ([cae2a10](https://github.com/oleg-tkachuk/limes/commit/cae2a10c3e9aae3d9ea8054b470ce82a7938cb00))

## [0.53.2](https://github.com/oleg-tkachuk/limes/compare/v0.53.1...v0.53.2) (2026-10-12)

### Bug Fixes

* **memstore:** keep wall times, so a page of holds survives a clock step ([44a0965](https://github.com/oleg-tkachuk/limes/commit/44a0965d1d67d289479a0aedde72f3e9cb56e798))

## [0.53.1](https://github.com/oleg-tkachuk/limes/compare/v0.53.0...v0.53.1) (2026-10-12)

### Bug Fixes

* **jwks:** free the fetch slot before answering the caller ([4161bde](https://github.com/oleg-tkachuk/limes/commit/4161bde3955a3756746d342ce7cd5c130c87fbdd))
* **jwks:** read a key's x as strict base64url ([ad696da](https://github.com/oleg-tkachuk/limes/commit/ad696da17429cc365f2d0eb7cb24861a99d0272a))

## [0.53.0](https://github.com/oleg-tkachuk/limes/compare/v0.52.8...v0.53.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **store:** Store.Insert refuses a capability with no id or a parent
  not on record (ErrInvalidRequest) and a child expiring after its parent
  (ErrDelegationTooWide); storetest checks it.

### Bug Fixes

* **store:** refuse an insert that breaks the lineage ([248b729](https://github.com/oleg-tkachuk/limes/commit/248b7296146de8df29be0ff6cfe478277896bd26))

## [0.52.8](https://github.com/oleg-tkachuk/limes/compare/v0.52.7...v0.52.8) (2026-10-12)

### Bug Fixes

* **meter:** attribute a refused settle to the subject's tenant ([8bcd092](https://github.com/oleg-tkachuk/limes/commit/8bcd092447bf8b4a5136ba60178c44f4e9e2fc27))

## [0.52.7](https://github.com/oleg-tkachuk/limes/compare/v0.52.6...v0.52.7) (2026-10-12)

### Bug Fixes

* **memstore:** replay a charge whose capability is gone ([2c191a2](https://github.com/oleg-tkachuk/limes/commit/2c191a22d623ad5add842aa48015a6e7a919b628))

## [0.52.6](https://github.com/oleg-tkachuk/limes/compare/v0.52.5...v0.52.6) (2026-10-12)

### Bug Fixes

* **delegate:** compare a child's start with its parent's exactly ([b5cf2ba](https://github.com/oleg-tkachuk/limes/commit/b5cf2ba85f94c1e2c295a58e3226e9bb26e7d624))

## [0.52.5](https://github.com/oleg-tkachuk/limes/compare/v0.52.4...v0.52.5) (2026-10-12)

### Bug Fixes

* **jwks:** answer a caller timing out on the key endpoint as unavailable ([10e292d](https://github.com/oleg-tkachuk/limes/commit/10e292d27d2291fe8fec023458f486c832b64a4d))

## [0.52.4](https://github.com/oleg-tkachuk/limes/compare/v0.52.3...v0.52.4) (2026-10-12)

### Bug Fixes

* **jwks:** hand out keys a caller cannot change the resolver's through ([cf8ac89](https://github.com/oleg-tkachuk/limes/commit/cf8ac89d47d2c3cb3b4254e42611ef19a18e8ba5))

## [0.52.3](https://github.com/oleg-tkachuk/limes/compare/v0.52.2...v0.52.3) (2026-10-12)

### Bug Fixes

* answer a key-bound token at a verifier without DPoP as its own fault ([b898c53](https://github.com/oleg-tkachuk/limes/commit/b898c53fe286d513e760564df6a2d5166d3208a0))
* **jwks:** report an unknown kid during an outage as unavailable ([996ca7b](https://github.com/oleg-tkachuk/limes/commit/996ca7b5503fe6fd287a2ce895018ff29bacfb8d))
* **memstore:** settle against the lineage it is counted on ([a0c5959](https://github.com/oleg-tkachuk/limes/commit/a0c5959fb8e74bc478c61981db04b0b9236963d2))

## [0.52.2](https://github.com/oleg-tkachuk/limes/compare/v0.52.1...v0.52.2) (2026-10-12)

### Bug Fixes

* **issuer:** refuse a TTL that ends within the second it starts ([6825d56](https://github.com/oleg-tkachuk/limes/commit/6825d564038e0d3559bc1752e69d09ed44c580b4))
* let a page walk be ranged again from where it started ([503bb2a](https://github.com/oleg-tkachuk/limes/commit/503bb2a4b71873e73372c00fdef6d80c24fe91ed))
* **memstore:** never purge a capability while a child of it stays ([cc79d6c](https://github.com/oleg-tkachuk/limes/commit/cc79d6cb7bf09985a8d5162c678f619b6d26aef6))

## [0.52.1](https://github.com/oleg-tkachuk/limes/compare/v0.52.0...v0.52.1) (2026-10-12)

### Bug Fixes

* **jwks:** serve a held key while the set refreshes behind it ([3b5d7e9](https://github.com/oleg-tkachuk/limes/commit/3b5d7e9b08a792782244d0cd5122bc666fba0b65))

## [0.52.0](https://github.com/oleg-tkachuk/limes/compare/v0.51.0...v0.52.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **biscuit:** CopyCeiling.Requests and Budget are pointers, nil when the
  block set none; Reservation.Copies is []HeldCopy.
* **meter:** CopyUsageReader.CopyUsage takes a CopyUsageRequest and
  returns a CopyUsageResult.

### Bug Fixes

* **jwks:** read a document under a MaxDocumentBytes of math.MaxInt ([c00f163](https://github.com/oleg-tkachuk/limes/commit/c00f1630e9cadcfebf8baf3d1c7706e517150fc6))

### Code Refactoring

* **biscuit:** model a copy's absent ceiling as nil ([4e0ae09](https://github.com/oleg-tkachuk/limes/commit/4e0ae09bedf7f7f9451442c235fe17d101d4694b))
* **meter:** read copy usage through a bounded request ([5ac77eb](https://github.com/oleg-tkachuk/limes/commit/5ac77eb701bef22c3ed89ba4ef011de42a23bea3))

## [0.51.0](https://github.com/oleg-tkachuk/limes/compare/v0.50.2...v0.51.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **issuer:** Delegate returns ErrOpNotAllowed for a parent whose
  caveats do not include OpShare.

### Bug Fixes

* **issuer:** delegate only from a parent granted OpShare ([709d5f1](https://github.com/oleg-tkachuk/limes/commit/709d5f1cf36f2d43736ff40e438c29da2117ee61))

## [0.50.2](https://github.com/oleg-tkachuk/limes/compare/v0.50.1...v0.50.2) (2026-10-12)

### Bug Fixes

* **issuer:** record a capability's times as its token carries them ([e7e8893](https://github.com/oleg-tkachuk/limes/commit/e7e889317041a99adab6b9c045fb3b47da4e3032))
* **meter:** match every copy of a hold, on settle and on replay ([78cf5aa](https://github.com/oleg-tkachuk/limes/commit/78cf5aaeba65bc87e4662c20648b36f846c2bb42))

## [0.50.1](https://github.com/oleg-tkachuk/limes/compare/v0.50.0...v0.50.1) (2026-10-12)

### Bug Fixes

* **meter:** compare tenant thresholds and ranking exactly ([f9769bc](https://github.com/oleg-tkachuk/limes/commit/f9769bc0f67e8fa1c9f79d54305e7a68c7fc8197))

## [0.50.0](https://github.com/oleg-tkachuk/limes/compare/v0.49.0...v0.50.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* RefundReceipt.RefundedAmount is Amount;
  BiscuitCopy.Limits is Copies.

### Features

* **verifier:** allow an exact time window with NoLeeway ([b2d4883](https://github.com/oleg-tkachuk/limes/commit/b2d48839bf0f8e9abe0215633637a0251030ed8e))

### Code Refactoring

* give one name to a refund's amount and a copy's limits ([9271cf3](https://github.com/oleg-tkachuk/limes/commit/9271cf3e5739d67a9f345d929ea8d5ee4b70febe))

## [0.49.0](https://github.com/oleg-tkachuk/limes/compare/v0.48.0...v0.49.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **jwks:** jwks.RemoteConfig.MaxDocumentBytes is an int.

### Code Refactoring

* **jwks:** count a document's bytes in an int ([81a79d3](https://github.com/oleg-tkachuk/limes/commit/81a79d397a778c22d3c410c2e54c2937ec028f9a))

## [0.48.0](https://github.com/oleg-tkachuk/limes/compare/v0.47.0...v0.48.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **meter:** Settle returns ErrReservationNotFound when req.Subject
  carries other Biscuit copies than the reservation was made under.

### Bug Fixes

* **authorization:** return the scheme with a malformed credential ([89739ab](https://github.com/oleg-tkachuk/limes/commit/89739abb8cdc75783b21a76cc9b948eaf17e7273))
* **biscuit:** bound the attenuation blocks a token may carry ([341d08c](https://github.com/oleg-tkachuk/limes/commit/341d08c22a3c9f03c3295ef8a4bac4b9290e3bf9))
* **meter:** settle a hold only under the copies that made it ([311f658](https://github.com/oleg-tkachuk/limes/commit/311f6587c87e99eb475dbd5802ee099d3d6f69c1))

## [0.47.0](https://github.com/oleg-tkachuk/limes/compare/v0.46.0...v0.47.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **meter:** ListTenantBudgetsRequest.UnlimitedOnly is replaced by
  Filter (TenantBudgetsAll, TenantBudgetsAtOrPast, TenantBudgetsUnlimited);
  ThresholdPct applies only to TenantBudgetsAtOrPast.

### Code Refactoring

* **meter:** pick listed tenant budgets with an explicit filter ([f60834a](https://github.com/oleg-tkachuk/limes/commit/f60834af5cdba4cd19274893193f134482f0bf70))

## [0.46.0](https://github.com/oleg-tkachuk/limes/compare/v0.45.0...v0.46.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **issuer:** Issue and Delegate return IssueResult{Capability, Token};
  Biscuit takes BiscuitRequest{Capability} and returns BiscuitResult{Token}.

### Code Refactoring

* **issuer:** return minted capabilities in result structs ([87de0b8](https://github.com/oleg-tkachuk/limes/commit/87de0b8bd9e7e816d807dbed574f2febefde6604))

## [0.45.0](https://github.com/oleg-tkachuk/limes/compare/v0.44.1...v0.45.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* ChargeRecord, ChargeReceipt, RefundReceipt, BumpReceipt,
  TenantBudget, Usage, Revocation, Problem, Principal and AgentPrincipal
  can no longer be compared with ==.

### Code Refactoring

* keep the structs that may grow out of == ([244592c](https://github.com/oleg-tkachuk/limes/commit/244592c2d3de4d4a5a351da34094f57aba8c8e34))

## [0.44.1](https://github.com/oleg-tkachuk/limes/compare/v0.44.0...v0.44.1) (2026-10-12)

### Bug Fixes

* **memstore:** refuse a zero subject in Settle and meet thresholds exactly ([85a4353](https://github.com/oleg-tkachuk/limes/commit/85a4353a62525ba817ba9446658f5f4c2b5244e5))

## [0.44.0](https://github.com/oleg-tkachuk/limes/compare/v0.43.0...v0.44.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **verifier:** Verify's refusals match ErrInvalidToken or
  ErrBiscuitAttenuation only, no longer the request sentinels of their cause.

### Bug Fixes

* **pages:** stop a walk on any cursor already fetched ([dee3f10](https://github.com/oleg-tkachuk/limes/commit/dee3f101cbb1919463bae43f3c434474ebf847cf))
* **verifier:** refuse a token without its cause's request family ([de10122](https://github.com/oleg-tkachuk/limes/commit/de1012254f93e5d947d8b21ffd5a3d0f7b86a901))

## [0.43.0](https://github.com/oleg-tkachuk/limes/compare/v0.42.0...v0.43.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **biscuit:** BiscuitCopy's fields are methods and only
  Verifier.BiscuitCopy makes one; RevokeBiscuitRequest takes it as Copy.

### Bug Fixes

* **biscuit:** revoke only a copy the verifier read ([3ca507a](https://github.com/oleg-tkachuk/limes/commit/3ca507acde28ccfb3617a4885ed94008b4aca16f))

## [0.42.0](https://github.com/oleg-tkachuk/limes/compare/v0.41.0...v0.42.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* LoadPrivateKey, Signer.KeyID, Op.Mutating,
  SigningKey.Generated and dpop.ErrNoReplayCache are removed;
  dpop.ProofRequest.Now is IssuedAt.
* **meter:** StoredMeterSubject takes a RecordReader, which Store
  embeds; ChargeRecord gains TenantID, Op, Actor and At, which metertest
  checks.
* **metrics:** the outcome values cap_exceeded, tenant_exceeded and
  limit_exceeded are budget_exceeded, tenant_budget_exceeded and
  request_limit_exceeded.

### Features

* **meter:** read stored subjects through a RecordReader and return who charged ([c71c049](https://github.com/oleg-tkachuk/limes/commit/c71c0494e1755af8ddb3962c4ae220ff15c11ba7))

### Bug Fixes

* **inspect:** report an unreadable token as invalid ([3010f27](https://github.com/oleg-tkachuk/limes/commit/3010f27a8a56601618da3ac13ac7b7643b7a2392))
* **metrics:** name metered refusals as their reasons ([ee18019](https://github.com/oleg-tkachuk/limes/commit/ee180195be7f19336258359f55d4fbd204eb17e7))

### Code Refactoring

* unexport what only the module itself uses ([7692ce3](https://github.com/oleg-tkachuk/limes/commit/7692ce3e690ca8dfba0979824f46327164aaa1f5))

## [0.41.0](https://github.com/oleg-tkachuk/limes/compare/v0.40.1...v0.41.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **metertest:** CapabilitySpec gains Tenant, which Env.NewCapability
  must honour for a tenant from NewTenant.

### Tests

* **metertest:** hold stores to holds, units, ranking and batches ([b80596b](https://github.com/oleg-tkachuk/limes/commit/b80596bfb313520692d2f3a1dda2a6bba2fac0d6))

## [0.40.1](https://github.com/oleg-tkachuk/limes/compare/v0.40.0...v0.40.1) (2026-10-12)

### Bug Fixes

* **biscuit:** refuse unmetered copy limits only after every other check ([1a2a6fe](https://github.com/oleg-tkachuk/limes/commit/1a2a6fe505cc1c1770f3ea9653e33b7d11175ccb))
* **jose:** decode base64url strictly ([e50a687](https://github.com/oleg-tkachuk/limes/commit/e50a68782010821e18964ca47ce6e321902b6e23))

## [0.40.0](https://github.com/oleg-tkachuk/limes/compare/v0.39.0...v0.40.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **dpop:** dpop.Request and dpop.ReplayEntry carry a Holder, which
  Verify requires; MemoryReplayCacheConfig.EntriesPerKey is EntriesPerHolder
  and DefaultReplayEntriesPerKey is DefaultReplayEntriesPerHolder. Proofs
  past MaxProofBytes and jtis past MaxJTIBytes are refused.

### Bug Fixes

* **dpop:** share the replay cache per capability and bound what it keeps ([0e706eb](https://github.com/oleg-tkachuk/limes/commit/0e706ebad53e95f8adf740123eb8df11b746caa0))

## [0.39.0](https://github.com/oleg-tkachuk/limes/compare/v0.38.0...v0.39.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **meter:** ErrInvalidAmount and NormalizeUnitCode's error match
  ErrInvalidRequest; a charge that would take a counter past MaxNanos is
  ErrCounterOverflow. SetTenantBudget refuses what the new
  SetTenantBudgetRequest.Validate refuses, a nil TenantID included.

### Bug Fixes

* **meter:** refuse malformed amounts and units as invalid requests ([66475ff](https://github.com/oleg-tkachuk/limes/commit/66475ff044cc9f7073d490a3d500d81e9bb4c037))

## [0.38.0](https://github.com/oleg-tkachuk/limes/compare/v0.37.0...v0.38.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **meter:** Bump, Charge, Reserve and Settle return ErrNotFound for
  a capability not on record; metertest checks it.

### Bug Fixes

* **meter:** refuse to meter a capability not on record ([6e448c4](https://github.com/oleg-tkachuk/limes/commit/6e448c4a08ef7891d67aa3c84b9d72c0bdd40ba7))

## [0.37.0](https://github.com/oleg-tkachuk/limes/compare/v0.36.0...v0.37.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **jwks:** a zero HandlerConfig.MaxAge is DefaultMaxAge; set
  Revalidate for max-age=0.
* **verifier:** AcceptTokenTypes is []TokenType and TokenTypeV4 a
  TokenType; an empty AcceptTokenTypes accepts TokenTypeV4 and an empty
  KeyIssuers binds no kid, where both were refused.
* **dpop:** dpop.Sign takes a context first and requires
  ProofRequest.Now; NewMemoryReplayCache refuses EntriesPerKey above
  MaxEntries.
* **issuer:** LoadKey takes only a path and derives the kid; set
  SigningKey.KeyID to name it otherwise.
* **memstore:** NewUsageStore refuses a UsageStoreConfig without Records.

### Features

* **dpop:** sign proofs under a context and refuse a zero time ([8a142dd](https://github.com/oleg-tkachuk/limes/commit/8a142dd95aa244798d758a1c5b227c4375b7d9e0))
* **jwks:** default the key set's max-age and revalidate explicitly ([ca81156](https://github.com/oleg-tkachuk/limes/commit/ca81156117adbd6378af7fcdf325b92c25dbd526))
* validate effects and authorization schemes ([4b56522](https://github.com/oleg-tkachuk/limes/commit/4b5652245d774d980d719dd86f4ce88b4f1d44b5))

### Code Refactoring

* **issuer:** name the default generation and derive every kid ([b7943fb](https://github.com/oleg-tkachuk/limes/commit/b7943fb73ee8989d2ee0dbec814356189d4b010f))
* **memstore:** require the record store a usage store meters by ([d8861dc](https://github.com/oleg-tkachuk/limes/commit/d8861dc7e1dc399113e6fe81a2daf5b0b1b90667))
* **verifier:** read an empty list as an unset one ([d68bfbd](https://github.com/oleg-tkachuk/limes/commit/d68bfbde402c6a9e80eb49bf45e89acae44b2907))

## [0.36.0](https://github.com/oleg-tkachuk/limes/compare/v0.35.0...v0.36.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* StandardVerifier and NewStandardVerifier are Verifier
  and NewVerifier; UsageHousekeeping.Delete is DeleteUsage;
  BumpReceipt.Requests is RequestCount; Revocation.Cascade is Cascaded;
  dpop.ReplayEntry.Expires is ExpiresAt; DefaultPurgeLimit is
  DefaultHousekeepingLimit.

### Code Refactoring

* give one name to each concept ([78579a6](https://github.com/oleg-tkachuk/limes/commit/78579a6bac26f47df1c592aa95e75c295435eefb))

## [0.35.0](https://github.com/oleg-tkachuk/limes/compare/v0.34.0...v0.35.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **meter:** ListTenantBudgets returns Page[TenantBudget];
  TenantBudgetSummary, ListTenantBudgetsRequest.ExcludeInactive,
  memstore.LedgerEntry and UsageStore.Ledger are removed.

### Code Refactoring

* **meter:** list tenant budgets without display fields ([2013a2a](https://github.com/oleg-tkachuk/limes/commit/2013a2a921b02838a4fde9421f311e0a2498b08b))

## [0.34.0](https://github.com/oleg-tkachuk/limes/compare/v0.33.1...v0.34.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **biscuit:** ErrCopyCountersNotMetered no longer matches
  ErrBiscuitAttenuation or ErrInvalidToken; ErrorResponse answers it with
  500 and ReasonOf gives it no reason.

### Bug Fixes

* **biscuit:** answer unmetered copy limits as a configuration error ([6e4bd3c](https://github.com/oleg-tkachuk/limes/commit/6e4bd3c040acfec6f19d6977d63dfcc9f4157eaf))
* **challenge:** name a realm in every challenge ([86ed1db](https://github.com/oleg-tkachuk/limes/commit/86ed1db85ee8694c66492632bf232cbbec1246bb))

### Performance Improvements

* **biscuit:** cache biscuit revocations per id ([3185ab4](https://github.com/oleg-tkachuk/limes/commit/3185ab49c1f47e72996c4b9369bffbffc3f8bad3))

## [0.33.1](https://github.com/oleg-tkachuk/limes/compare/v0.33.0...v0.33.1) (2026-10-12)

### Bug Fixes

* **meter:** rank ceilings of 0 first and unlimited last ([5f5fab9](https://github.com/oleg-tkachuk/limes/commit/5f5fab9f8fd8fc324cc39ce0488105653450772f))

## [0.33.0](https://github.com/oleg-tkachuk/limes/compare/v0.32.1...v0.33.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **reason:** invalid_biscuit is biscuit_attenuation,
  source_not_allowed is source_ip_not_allowed; ErrAuthSchemeMismatch gives
  auth_scheme_mismatch and dpop.ErrNonceRequired gives nonce_required.
* **meter:** Record.MeterSubject is removed; use
  StoredMeterSubject(ctx, store, capabilityID).
* **signer:** Signer.Sign is unexported; mint tokens with Issuer.
* these now match ErrInvalidRequest instead of
  ErrAudienceMismatch, ErrBiscuitAttenuation and ErrDelegationTooWide.

### Features

* **meter:** read a stored meter subject from the store ([0c1d227](https://github.com/oleg-tkachuk/limes/commit/0c1d227dd5a5873eaf10ac9e0c1d42cf872d5c37))
* **reason:** name reasons after their sentinels ([8dfa223](https://github.com/oleg-tkachuk/limes/commit/8dfa223d815f239087bbc927c379d7cde947e396))
* **signer:** sign only through an issuer ([4c39632](https://github.com/oleg-tkachuk/limes/commit/4c39632ec742f499aa7fdb388bea3d1022bf0789))

### Bug Fixes

* refuse the caller's own mistakes as invalid requests ([70a80d3](https://github.com/oleg-tkachuk/limes/commit/70a80d3c084b453c10bfc1adff19c00505a41e60))

## [0.32.1](https://github.com/oleg-tkachuk/limes/compare/v0.32.0...v0.32.1) (2026-10-12)

### Bug Fixes

* **memstore:** keep reservation cursors past year 2262 ([a64b612](https://github.com/oleg-tkachuk/limes/commit/a64b612a69d6aae34fbc9bc29d72f94d1166efb2))

## [0.32.0](https://github.com/oleg-tkachuk/limes/compare/v0.31.1...v0.32.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **metertest:** Env.NewCapability takes a CapabilitySpec carrying the
  parent and both ceilings.

### Features

* **metertest:** record capabilities from a CapabilitySpec ([4ad4920](https://github.com/oleg-tkachuk/limes/commit/4ad4920d0a13e1f84c36cd1a3199b714845e9b1a))

## [0.31.1](https://github.com/oleg-tkachuk/limes/compare/v0.31.0...v0.31.1) (2026-10-12)

### Bug Fixes

* **issuer:** refuse to delegate from another issuer's parent ([4e29052](https://github.com/oleg-tkachuk/limes/commit/4e29052b1bb6030cbdc8cf159af2bba966f9e6c3))

## [0.31.0](https://github.com/oleg-tkachuk/limes/compare/v0.30.0...v0.31.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* ParseAuthorization returns ErrNoCredentials, not
  ErrMalformedAuthorization, for an empty header or a foreign scheme; Verify
  returns it for an empty token instead of ErrInvalidToken.
* UsageHousekeeping.Delete takes a DeleteUsageRequest and
  MeterReader.ChargeByRef a ChargeByRefRequest.

### Bug Fixes

* a request with no credentials gets a 401 challenge, not a 400 ([93d5a5e](https://github.com/oleg-tkachuk/limes/commit/93d5a5e1973c8f6430162b8985eaf6eaaad24618))

### Code Refactoring

* Delete and ChargeByRef take request structs ([91fd88b](https://github.com/oleg-tkachuk/limes/commit/91fd88b2b08e8ee3a85a23463ad185c4fcf48a28))

## [0.30.0](https://github.com/oleg-tkachuk/limes/compare/v0.29.0...v0.30.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **jwks:** NewStatic(map) becomes NewStatic(StaticConfig) (*Static,
  error); SetKey returns an error; NewHandler returns *Handler.

### Code Refactoring

* **jwks:** NewStatic takes a config and refuses a key it cannot serve ([e0c4b2e](https://github.com/oleg-tkachuk/limes/commit/e0c4b2e06c0ffbb5847f1fb913e11fc06441f596))

## [0.29.0](https://github.com/oleg-tkachuk/limes/compare/v0.28.0...v0.29.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* MinPurgeRetention is MaxLeeway + PurgeClockSkew (6m); a
  PurgeRequest.ExpiredFor between 5m and 6m is now refused.
* NewStandardVerifier refuses several TrustedIssuers with
  neither KeyIssuers nor SharedKeys, and both together.

### Bug Fixes

* keep revocations past a verifier's leeway by any clock skew too ([fcd673f](https://github.com/oleg-tkachuk/limes/commit/fcd673f54131d3dd35e7dbb61a16ba888d051ab5))
* several trusted issuers need an explicit decision about keys ([12d44a0](https://github.com/oleg-tkachuk/limes/commit/12d44a0b627577bb22cf6461f2fec68c4d63d28a))

## [0.28.0](https://github.com/oleg-tkachuk/limes/compare/v0.27.1...v0.28.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **memstore:** memstore.New[TX]() becomes NewStore(StoreConfig) and
  NewUsage[TX](records) becomes NewUsageStore[TX](UsageStoreConfig[TX]), both
  returning an error; Store is no longer generic; WithClock and WithTxFactory
  are gone, replaced by the Now and TxFactory config fields.

### Code Refactoring

* **memstore:** config constructors, and a Store without a type parameter ([3eccc24](https://github.com/oleg-tkachuk/limes/commit/3eccc24dc702ffbbcf88fdf3af70de5b1d9c1ee3))

## [0.27.1](https://github.com/oleg-tkachuk/limes/compare/v0.27.0...v0.27.1) (2026-10-12)

### Bug Fixes

* **memstore:** read the lineage under the usage lock, and forget purged ancestors ([b450c97](https://github.com/oleg-tkachuk/limes/commit/b450c97c6678bf8ec4cfde65bcf482b7be49d7f1))

## [0.27.0](https://github.com/oleg-tkachuk/limes/compare/v0.26.0...v0.27.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* Caveats.MaxRequests and MaxBudgetAmount become Requests
  (RequestCeiling) and Budget (BudgetCeiling); CopyCeiling likewise;
  Attenuation takes *RequestCeiling and *BudgetCeiling, nil for none;
  MeterSubject.Requests and Budget return ceilings; TenantBudget and
  SetTenantBudgetRequest take Ceiling (BudgetCeiling), and a tenant charged
  before any ceiling counts Unlimited; metertest.Env.NewCapability takes a
  BudgetCeiling. A zero ceiling now allows nothing. TokenTypeV3 is replaced
  by TokenTypeV4, and tokens of v3 are refused.

### Code Refactoring

* an explicit Unlimited replaces zero as "no ceiling" ([4326c65](https://github.com/oleg-tkachuk/limes/commit/4326c65ee0d8cca3ef27b0624f4e2878da5b673e))

## [0.26.0](https://github.com/oleg-tkachuk/limes/compare/v0.25.0...v0.26.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* Signer.Sign returns ErrInvalidCaveats or ErrInvalidRequest
  for what Issue would refuse; a token whose caveats do not validate is
  ErrInvalidToken.

### Bug Fixes

* refuse caveats outside the rules wherever they turn up ([088fac2](https://github.com/oleg-tkachuk/limes/commit/088fac2075ccc02af788a320609dd17c7e5da3e1))

## [0.25.0](https://github.com/oleg-tkachuk/limes/compare/v0.24.3...v0.25.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* Delegate returns ErrDelegationTooWide for every parent
  read from a Biscuit, not only one with limits of its own.
* Issuer.Biscuit takes a VerifiedCapability instead of a
  Capability.

### Bug Fixes

* a Biscuit copy delegates nothing ([96b28a1](https://github.com/oleg-tkachuk/limes/commit/96b28a17c80526f9e7782ef65b1076cb65f2afd2))
* Issuer.Biscuit signs only a capability the verifier accepted ([ef9b678](https://github.com/oleg-tkachuk/limes/commit/ef9b67841a800888698030414b36db1eb1ce0159))

## [0.24.3](https://github.com/oleg-tkachuk/limes/compare/v0.24.2...v0.24.3) (2026-10-12)

### Bug Fixes

* **jwks:** run a fetch apart from its caller, so cancelling keeps the rate limit ([9b4536c](https://github.com/oleg-tkachuk/limes/commit/9b4536c02f4a10ae4ad20d34c163b97ec72cb3b6))

## [0.24.2](https://github.com/oleg-tkachuk/limes/compare/v0.24.1...v0.24.2) (2026-10-12)

### Bug Fixes

* **dpop:** remember a proof id past the last instant it is accepted ([057a2d8](https://github.com/oleg-tkachuk/limes/commit/057a2d84ec6437fd4bd01d93b4ca087cbf7e3b74))

## [0.24.1](https://github.com/oleg-tkachuk/limes/compare/v0.24.0...v0.24.1) (2026-10-12)

### Bug Fixes

* mint no token a verifier would refuse unread ([0f8d6db](https://github.com/oleg-tkachuk/limes/commit/0f8d6db00296c45c36f2ff20fb3348beed2ed52f))
* refuse resources and networks that cannot mean what they read ([a62acc6](https://github.com/oleg-tkachuk/limes/commit/a62acc639d321f61961697be85fedbde978a5c3b))

## [0.24.0](https://github.com/oleg-tkachuk/limes/compare/v0.23.2...v0.24.0) (2026-10-12)

### Features

* Capability.Clone and Principal.Clone ([8da0a11](https://github.com/oleg-tkachuk/limes/commit/8da0a11982a1db79dc1d9413fa2601bdb5542801))

### Bug Fixes

* **memstore:** keep records, counters and holds from changing behind the store ([43d4fc7](https://github.com/oleg-tkachuk/limes/commit/43d4fc70de47884916c0226105d8624bee87dfe7))

## [0.23.2](https://github.com/oleg-tkachuk/limes/compare/v0.23.1...v0.23.2) (2026-10-12)

### Bug Fixes

* build on 32-bit platforms ([ef8da3c](https://github.com/oleg-tkachuk/limes/commit/ef8da3c5c9fd8a262ce0d39e54fb89ec4fb97721))

## [0.23.1](https://github.com/oleg-tkachuk/limes/compare/v0.23.0...v0.23.1) (2026-10-12)

### Bug Fixes

* **jwks:** a caller's cancelled fetch no longer blocks the key set for everyone ([ee82b0a](https://github.com/oleg-tkachuk/limes/commit/ee82b0a5eb17239b801bb26a1aeddade6d24eece))
* **memstore:** settle within the hold whatever ceiling was crossed meanwhile ([ac8f2da](https://github.com/oleg-tkachuk/limes/commit/ac8f2da02a5f14e66f45c35ce997c510b5ec5867))

## [0.23.0](https://github.com/oleg-tkachuk/limes/compare/v0.22.1...v0.23.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* Store.IsRevoked must answer for MaxDelegationDepth
  ancestors and return ErrDelegationTooDeep for a deeper chain; Delegate
  returns ErrDelegationTooDeep past that depth.
* VerifierConfig.Leeway above MaxLeeway (5m) is refused;
  PurgeRequest.ExpiredFor of zero means MinPurgeRetention, and a shorter
  non-zero retention is ErrInvalidRequest.

### Bug Fixes

* keep revocations on record for as long as a verifier honours the token ([0cb2ca8](https://github.com/oleg-tkachuk/limes/commit/0cb2ca8da38942ed30f4c73b06b2f67ae61561f4))
* refuse delegation chains deeper than the revocation walk reaches ([a548f8f](https://github.com/oleg-tkachuk/limes/commit/a548f8f8cc06a484b389382843763a58acc1abd5))

## [0.22.1](https://github.com/oleg-tkachuk/limes/compare/v0.22.0...v0.22.1) (2026-10-12)

## [0.22.0](https://github.com/oleg-tkachuk/limes/compare/v0.21.0...v0.22.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* Principal, AgentPrincipal, Caveats and Capability carry
  no json tags; json.Marshal of them no longer writes the claim format.

### Features

* **verifier:** accept token formats through VerifierConfig.AcceptTokenTypes ([8145da3](https://github.com/oleg-tkachuk/limes/commit/8145da334270e9c1af13224f0738bfc9227ef98c))

### Code Refactoring

* the token's claims are unexported wire structs ([d619bac](https://github.com/oleg-tkachuk/limes/commit/d619bac0eb9204a0d25e64e8a881ef6b0ca3c9bf))

## [0.21.0](https://github.com/oleg-tkachuk/limes/compare/v0.20.0...v0.21.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* ListByPrincipal with a principal type outside user, agent
  and service, and Verify with a Scheme other than Bearer or DPoP, return
  ErrInvalidRequest.
* **meter:** OverrunReject is 1 and OverrunRecord 2; a stored or
  serialized numeric policy must be remapped. The zero value still rejects.

### Code Refactoring

* **meter:** OverrunPolicy has an explicit unspecified zero and a String ([854281a](https://github.com/oleg-tkachuk/limes/commit/854281a87f8cba9d6540cddde81f46fd5e032123))
* refuse unknown principal types and authorization schemes ([d4a3fe5](https://github.com/oleg-tkachuk/limes/commit/d4a3fe5d0186d54889550233213fbbb7ed9a68f4))

## [0.20.0](https://github.com/oleg-tkachuk/limes/compare/v0.19.0...v0.20.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* AuthorizeRequest{VerifyRequest: …, CheckRequest: …}
  becomes AuthorizeRequest{Verify: …, Check: …}, and promoted fields such as
  req.Token are reached as req.Verify.Token.
* Limit on ListByPrincipalRequest, ListTenantBudgetsRequest
  and ListReservationsRequest, and PageLimit, take an int; PeriodEnd on
  TenantBudget and SetTenantBudgetRequest is a time.Time, zero for none.

### Code Refactoring

* AuthorizeRequest names its Verify and Check parts ([5893617](https://github.com/oleg-tkachuk/limes/commit/5893617daa99f439d1f50087231dcfa12bc3d106))
* list limits are int and PeriodEnd is a plain time ([5ce24c4](https://github.com/oleg-tkachuk/limes/commit/5ce24c4c724937cfbb8bdf104837d03ab4d95419))

## [0.19.0](https://github.com/oleg-tkachuk/limes/compare/v0.18.0...v0.19.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* CopyCeiling.MaxBudget, Attenuation.MaxBudget and
  MeterSubject.MaxBudget become MaxBudgetAmount; ChargeReceipt.Spent becomes
  SpentAmount; ChargeRecord.Refunded and RefundReceipt.Refunded become
  RefundedAmount; ChargeRecord.ChargeID becomes ID; RevokeRequest.ID becomes
  CapabilityID; Attenuation.Planes becomes Audience; Reason on RevokeRequest,
  Revocation, RevokeBiscuitRequest and BiscuitRevocation becomes Note; Op on
  ChargeRequest, ReserveRequest and Reservation is an Op; NormaliseUnitCode
  becomes NormalizeUnitCode and TenantBudgetSummary.UtilisationPct becomes
  UtilizationPct.
* **meter:** RefundRequest{ChargeID} with no Amount is ErrInvalidRequest;
  pass Remaining: true for a full refund.
* **meter:** TenantBudgetSummary.UtilizationPct is removed; call
  Budget.Utilization().

### Code Refactoring

* **meter:** a full refund names Remaining instead of a zero amount ([c2e40f9](https://github.com/oleg-tkachuk/limes/commit/c2e40f9d8a1a36f939ce4cc373e28220afcb9444))
* **meter:** read utilization from TenantBudget.Utilization ([ce0c83d](https://github.com/oleg-tkachuk/limes/commit/ce0c83d97ddc98529e1cf6372895f4b5bf74d5b8))
* one spelling per concept across the API ([1f6b45f](https://github.com/oleg-tkachuk/limes/commit/1f6b45ffca434f9e51cc8ea2dd0a88b42ca7404a))

## [0.18.0](https://github.com/oleg-tkachuk/limes/compare/v0.17.0...v0.18.0) (2026-10-12)

### ⚠ BREAKING CHANGES

* **storetest:** storetest.Env holds func fields and is no longer comparable.

### Features

* **dpop:** add dpoptest, conformance suites for ReplayCache and Nonces ([26f4e9b](https://github.com/oleg-tkachuk/limes/commit/26f4e9b69be977102790a6adcceb10ed10116179))
* **jwks:** add jwkstest, a conformance suite for KeyResolver ([2e18d99](https://github.com/oleg-tkachuk/limes/commit/2e18d99a403ecb69c27c3c6eeebe0be37d7df844))
* **storetest:** give Env the tenant hooks metertest.Env has ([22c9b13](https://github.com/oleg-tkachuk/limes/commit/22c9b13e821d7f62a0ace5142f61abdf7d64c8be))

## [0.17.0](https://github.com/oleg-tkachuk/limes/compare/v0.16.0...v0.17.0) (2026-10-11)

### ⚠ BREAKING CHANGES

* BumpRequest, ChargeRequest, ReserveRequest and SettleRequest
  lose CapabilityID, TenantID, MaxRequests, MaxBudget, UnitCode and Copies; pass
  Subject: VerifiedCapability.MeterSubject() or Record.MeterSubject() instead.

### Features

* **meter:** add MeterSubject, built from a verified capability or a stored record ([0b504ed](https://github.com/oleg-tkachuk/limes/commit/0b504ed63e822c7e1ec963ade916b6923c7e8de6))

### Code Refactoring

* meter requests take a MeterSubject in place of their ceilings ([8f0cdf9](https://github.com/oleg-tkachuk/limes/commit/8f0cdf9bc85084c60309eeab518a14127b711102))

## [0.16.0](https://github.com/oleg-tkachuk/limes/compare/v0.15.0...v0.16.0) (2026-10-11)

### ⚠ BREAKING CHANGES

* Meter no longer has GetCharge, ChargeByRef, GetReservation,
  ListReservations or GetUsage — they are MeterReader's. Bump returns
  BumpReceipt; Release takes ReleaseRequest; ListReservations takes
  ListReservationsRequest and returns Page[Reservation]; PurgeOrphans and
  ReleaseExpired take PurgeOrphansRequest and ReleaseExpiredRequest and return
  results. metertest.Env gains an optional DeleteCapability.
* TenantBudgetCursor and DecodeTenantBudgetCursor are removed;
  a store writes its ListTenantBudgets cursor in a format of its own.

### Code Refactoring

* make the tenant budget cursor memstore's own ([233b561](https://github.com/oleg-tkachuk/limes/commit/233b561de8145d15e623a641f4abad17ffef51d3))
* split MeterReader from Meter, and give its calls requests, receipts and pages ([5365cec](https://github.com/oleg-tkachuk/limes/commit/5365cec26ee41eba20e6e1b67ea560b4f5fe3d9b))

## [0.15.0](https://github.com/oleg-tkachuk/limes/compare/v0.14.0...v0.15.0) (2026-10-11)

### ⚠ BREAKING CHANGES

* Store.Insert takes an InsertRequest{Capability, IssuedBy};
  Store.Get is removed (GetRecord returns the capability with its record);
  IssuerConfig.Store is an IssuanceStore, which every Store satisfies.
* Store.ListByPrincipal returns (Page[Capability], error) and
  TenantBudgets.ListTenantBudgets (Page[TenantBudgetSummary], error), and so do
  CapabilityLister and TenantBudgetLister.
* Store.PurgeExpired(ctx, expiredFor) is
  Store.Purge(ctx, PurgeRequest{ExpiredFor, Limit}) returning PurgeResult; it
  removes the expired capability records as well, and refuses a negative
  retention or limit.

### Code Refactoring

* give the Issuer an IssuanceStore, take an InsertRequest, and drop Get ([65f10f3](https://github.com/oleg-tkachuk/limes/commit/65f10f305cff3030d9ec789d41b6213bcb96591e))
* purge expired capability records with their entries, in batches ([fa7026f](https://github.com/oleg-tkachuk/limes/commit/fa7026f32816067e4b3ab2de2f90fb5be8da636e))
* return a Page from both list methods ([f15a8fa](https://github.com/oleg-tkachuk/limes/commit/f15a8fa48f3ff703bf5a2458bdeb4a5ca2a4e840))

## [0.14.0](https://github.com/oleg-tkachuk/limes/compare/v0.13.0...v0.14.0) (2026-10-11)

### ⚠ BREAKING CHANGES

* Signer is a struct built by NewSigner(SignerConfig{KeyID,
  Key}); NewEd25519Signer is removed, and IssuerConfig.Signer is *Signer. A
  key that needs the context implements ContextSigner.
* StandardVerifier.Verify and Authorize return
  VerifiedCapability; read the capability with its Capability method, which
  returns a copy. DelegateRequest.Parent is a VerifiedCapability.
* the Verifier interface is removed; use *StandardVerifier,
  or declare the interface the calling code needs.

### Code Refactoring

* drop the Verifier interface ([2a18e29](https://github.com/oleg-tkachuk/limes/commit/2a18e295242970cc5a32f96ae84621db4169ba16))
* make Signer a concrete type over crypto.Signer ([8169d8d](https://github.com/oleg-tkachuk/limes/commit/8169d8dd0157ea552430e1462bf6aa6c4900aaef))
* return a VerifiedCapability from the verifier, and delegate only from one ([cf22157](https://github.com/oleg-tkachuk/limes/commit/cf22157a3c65f9ed82bbb0a52c1956993720509d))

## [0.13.0](https://github.com/oleg-tkachuk/limes/compare/v0.12.0...v0.13.0) (2026-10-11)

### ⚠ BREAKING CHANGES

* dpop.Verifier is built by dpop.NewVerifier(VerifierConfig)
  and its fields are unexported; NewMemoryReplayCache takes a
  MemoryReplayCacheConfig and NewHMACNonces an HMACNoncesConfig, both returning
  an error; ReplayCacheOption and WithEntriesPerKey are removed; a dpop.Verifier
  takes its clock from VerifierConfig.Now, not from the limes VerifierConfig.
* jwks.Handler(keys, maxAge) is jwks.NewHandler(HandlerConfig)
  returning (http.Handler, error); NewHandler and NewRemote refuse a negative
  max-age, interval or document bound instead of using the default.
* LoadOrGenerateKey is replaced by LoadKey(path, kid), which
  never generates; call GenerateKey for an ephemeral key. NewStandardVerifier
  refuses a non-nil empty KeyIssuers. ListByPrincipalRequest.Validate and the
  new ListTenantBudgetsRequest.Validate refuse a negative Limit, the latter also
  a ThresholdPct outside 0..100, and the conformance suites check stores refuse
  them.
* NewCachedRevocationChecker and
  NewCachedBiscuitRevocationChecker take RevocationCacheConfig and
  BiscuitRevocationCacheConfig and return an error; CacheOption and
  WithMaxEntries are removed, and a negative TTL no longer disables the cache.

### Code Refactoring

* build the dpop verifier, replay cache and nonces from config structs ([8179148](https://github.com/oleg-tkachuk/limes/commit/817914874f520b951f8f928b8fdde6b625f44930))
* configure the key-set handler with a struct, and refuse negatives ([9d05e02](https://github.com/oleg-tkachuk/limes/commit/9d05e02e4261aa38376308424cd08aaedfd979b4))
* configure the revocation caches with a struct, and refuse a negative TTL ([0d4a3f9](https://github.com/oleg-tkachuk/limes/commit/0d4a3f9f03106f47e2e5af821261797e6066ddd3))
* export the root defaults, and refuse an empty key binding, a negative page and a missing key file ([455566e](https://github.com/oleg-tkachuk/limes/commit/455566e027d6e9bd026c19ecc5023dddf6ffa822))

## [0.12.0](https://github.com/oleg-tkachuk/limes/compare/v0.11.0...v0.12.0) (2026-10-11)

### ⚠ BREAKING CHANGES

* Decode and VerifySignature are inspect.Decode and
  inspect.VerifySignature, in github.com/oleg-tkachuk/limes/inspect.
* GenerateEd25519Keypair is GenerateKey, returning
  (SigningKey, error); ValidateAmount(n) is n.Validate().
* WithCacheClock, HasDotSegment, IsBiscuit, MediaTypeProblem,
  WWWAuthenticateHeader and TokenType are unexported.
* WithMetering is Instrument, and MeteringOption is
  InstrumentOption.

### Code Refactoring

* move Decode and VerifySignature into the inspect package ([0394177](https://github.com/oleg-tkachuk/limes/commit/0394177eadb09d9ffae16ce3274f6adb67fb09ef))
* name the metering decorator Instrument ([f75fddb](https://github.com/oleg-tkachuk/limes/commit/f75fddbc9afbba7d924fee652e3a01fc46815f83))
* return a SigningKey from GenerateKey, and validate an amount on Nanos ([230f0b1](https://github.com/oleg-tkachuk/limes/commit/230f0b150844df3c3874839d4df57a63723944ec))
* unexport what no consumer needs ([d9c5487](https://github.com/oleg-tkachuk/limes/commit/d9c54871992e2d10e334d6aa625dbe170481597e))

## [0.11.0](https://github.com/oleg-tkachuk/limes/compare/v0.10.0...v0.11.0) (2026-10-11)

### ⚠ BREAKING CHANGES

* proof of possession moved to
  github.com/oleg-tkachuk/limes/dpop: DPoPVerifier is dpop.Verifier, which
  checks a dpop.Request; DPoPProof is dpop.Proof, its Proof field JWT;
  NewDPoPProof and DPoPProofRequest are dpop.Sign and dpop.ProofRequest;
  KeyThumbprint is dpop.Thumbprint; DPoPNonces, HMACNonces, NewHMACNonces,
  MemoryReplayCache, NewMemoryReplayCache, ReplayCache and their options and
  constants are dpop's, with the DPoP prefix dropped (DPoPHeader is dpop.Header,
  DPoPNonceHeader dpop.NonceHeader, DefaultDPoPWindow dpop.DefaultWindow,
  DPoPSigningAlgorithms dpop.SigningAlgorithms). ErrDPoPRequired, ErrDPoPInvalid,
  ErrDPoPReplayed and ErrDPoPNonceRequired are dpop.ErrRequired, ErrInvalid,
  ErrReplayed and ErrNonceRequired, matching ErrProofOfPossession only as the
  verifier returns them. ReplayCache.Seen takes a ReplayEntry and returns
  (bool, error); Nonces.Valid returns (bool, error). PublicJWK, DPoPJWK,
  AccessTokenHash and DPoPTokenType are unexported.

### Code Refactoring

* move proof of possession into the dpop package ([5eda6d8](https://github.com/oleg-tkachuk/limes/commit/5eda6d8815eb6ce7ed04877bdc0763d3a57b6f77))

## [0.10.0](https://github.com/oleg-tkachuk/limes/compare/v0.9.0...v0.10.0) (2026-10-11)

### ⚠ BREAKING CHANGES

* the key-set API moved to github.com/oleg-tkachuk/limes/jwks:
  MarshalJWKS is jwks.Marshal, ParseJWKS jwks.Parse, JWKSHandler jwks.Handler,
  RemoteJWKSResolver and NewRemoteJWKSResolver jwks.Remote and jwks.NewRemote,
  RemoteJWKSConfig jwks.RemoteConfig, StaticKeyResolver and NewStaticKeyResolver
  jwks.Static and jwks.NewStatic, DeriveKeyID jwks.KeyID, ErrUnknownKID
  jwks.ErrUnknownKID, ErrJWKSUnavailable jwks.ErrUnavailable. JWKSDocument and
  JWK are unexported.

### Code Refactoring

* move key sets and key resolution into the jwks package ([8269b2d](https://github.com/oleg-tkachuk/limes/commit/8269b2d48741a0a341b4b08c568fd75ef206dcf0))

## [0.9.0](https://github.com/oleg-tkachuk/limes/compare/v0.8.0...v0.9.0) (2026-10-11)

### ⚠ BREAKING CHANGES

* ErrDPoPRequired, ErrDPoPInvalid, ErrDPoPReplayed,
  ErrDPoPNonceRequired and ErrAuthSchemeMismatch no longer match
  ErrInvalidSignature; match ErrProofOfPossession or ErrUnauthenticated instead.
  ErrChargeNotFound, ErrReservationNotFound, ErrUsageNotFound and
  ErrTenantBudgetNotFound now match ErrNotFound.
* ErrInvalidSignature is ErrInvalidToken, and
  ReasonInvalidSignature ("invalid_signature") is ReasonInvalidToken
  ("invalid_token").
* ErrJWKSUnavailable, and any KeyResolver error other than
  ErrUnknownKID, no longer matches ErrInvalidSignature; ErrorResponse answers a
  custom resolver's failure with 500.

### Code Refactoring

* give errors families that say what they are, read from one table ([86827ec](https://github.com/oleg-tkachuk/limes/commit/86827ecb28236c25551f2b15232bf5680b1ed8cd))
* refuse a token over a resolver outage as the server's fault ([738fbb3](https://github.com/oleg-tkachuk/limes/commit/738fbb3bebd060c7103241b2d9d48e9c9c05e8b1))
* rename ErrInvalidSignature to ErrInvalidToken ([62bcee9](https://github.com/oleg-tkachuk/limes/commit/62bcee9c7a933282452562f0fd2cdf2070d21574))

## [0.8.0](https://github.com/oleg-tkachuk/limes/compare/v0.7.1...v0.8.0) (2026-10-11)

### ⚠ BREAKING CHANGES

* Issue, Delegate and Verify refuse a principal whose Type is
  not PrincipalUser, PrincipalAgent or PrincipalService, or that carries Agent
  details without being an agent; Delegate needs a Subject with a Type.

### Bug Fixes

* copy the caller's slices into an issued or delegated capability ([7734d60](https://github.com/oleg-tkachuk/limes/commit/7734d6084cdbc26be156f93f4d49e87a9234d003))
* give a malformed Authorization header a Reason, and pin the two refusal tables together ([da2d24f](https://github.com/oleg-tkachuk/limes/commit/da2d24f55a7eeac59333d11a548583d03b71fa0b))
* **memstore:** refuse a negative reservation TTL ([5abac31](https://github.com/oleg-tkachuk/limes/commit/5abac31b65dc3757d3ba0a2bd42609d43c8e0277))
* refuse a negative default TTL, leeway or token size ([af27d02](https://github.com/oleg-tkachuk/limes/commit/af27d026ac1d715505f04cdc2c91153d45b80d01))
* require a principal of a known kind on every capability ([192e27b](https://github.com/oleg-tkachuk/limes/commit/192e27b31752d6b8ee233bf6f59ff7c017fe1176))

## [0.7.1](https://github.com/oleg-tkachuk/limes/compare/v0.7.0...v0.7.1) (2026-10-11)

### Bug Fixes

* **reason:** keep a nonce challenge and a wrong scheme from reading as permanent ([bfa3c03](https://github.com/oleg-tkachuk/limes/commit/bfa3c0390c5e068a2ea39d0fa8887037e8b2a149))

## [0.7.0](https://github.com/oleg-tkachuk/limes/compare/v0.6.0...v0.7.0) (2026-10-11)

### Features

* **metertest:** let a store that scopes its tenants run every check ([ad83ad5](https://github.com/oleg-tkachuk/limes/commit/ad83ad54fa7be0220b64c1960da0841f23760841))

## [0.6.0](https://github.com/oleg-tkachuk/limes/compare/v0.5.0...v0.6.0) (2026-10-11)

### ⚠ BREAKING CHANGES

* **cache:** CachedRevocationChecker.Invalidate is removed; call Clear on
  a revocation signal.

### Bug Fixes

* **cache:** drop CachedRevocationChecker.Invalidate, which missed descendants ([438d64a](https://github.com/oleg-tkachuk/limes/commit/438d64a838e71372a158ae3e49e9a7cf8ff0ee4b))

## [0.5.0](https://github.com/oleg-tkachuk/limes/compare/v0.4.0...v0.5.0) (2026-10-11)

### ⚠ BREAKING CHANGES

* **dpop:** NewDPoPProof writes the method as given rather than in
  upper case, and the verifier compares it exactly.
* **dpop:** ReplayCache.Seen takes the proof key's thumbprint.

### Bug Fixes

* answer an unreachable key set with 503, not invalid_token ([d722df5](https://github.com/oleg-tkachuk/limes/commit/d722df591179af07276851e1ea3d5c01570c737f))
* **cache:** keep the revocation cache's eviction order bounded ([b7b9a35](https://github.com/oleg-tkachuk/limes/commit/b7b9a35eeba63a27ddb214b3be7e08ea2ab61064))
* **cache:** share the retry after a cancelled lookup ([2379a4a](https://github.com/oleg-tkachuk/limes/commit/2379a4a5c29c5e0b16006d3de46746d00eb0e895))
* **dpop:** accept a nonce from a replica whose clock runs a little ahead ([68eccfa](https://github.com/oleg-tkachuk/limes/commit/68eccfa93bd168018edd83a9fb1e6a870eb4b933))
* **dpop:** give each key its own share of the replay cache ([7a027b5](https://github.com/oleg-tkachuk/limes/commit/7a027b56ef59bababbdffa41b05d2b1c6461e7b4))
* **dpop:** read proofs as RFC 9449, RFC 9110 and RFC 7638 define them ([4c0c2cd](https://github.com/oleg-tkachuk/limes/commit/4c0c2cd7e3d7c6600b1c662b2177a37fbb139fd8))
* **dpop:** read the proof's htu as a URI under MatchPathOnly too ([6a77d0c](https://github.com/oleg-tkachuk/limes/commit/6a77d0c0fa958e01355194759527389adfee2592))
* **jwks:** keep the last good key set when a fetch yields no usable key ([aa57124](https://github.com/oleg-tkachuk/limes/commit/aa571247cf26ce3eb43f8f320e32065907e995b3))
* **jwks:** read a key set by JSON's rules, and use no key whose kid is named twice ([dab3a34](https://github.com/oleg-tkachuk/limes/commit/dab3a34b845f87785360471aeb484aa39a69ece6))
* **jwks:** read Cache-Control as RFC 9111 has it, and never serve a negative max-age ([a8da41a](https://github.com/oleg-tkachuk/limes/commit/a8da41a5678031db966a0b444bba1b232881b4af))
* **memstore:** name the tenant and unit on a refused charge's receipt ([1933f71](https://github.com/oleg-tkachuk/limes/commit/1933f7112846114af5d32b64116b51361171f61c))
* refuse a minus sign on an amount, minus zero included ([b7a45b2](https://github.com/oleg-tkachuk/limes/commit/b7a45b226d6cb0d1ac8333066f0ab59975c9cfa6))
* **signer:** refuse to sign integers the verifier would refuse ([ee356f2](https://github.com/oleg-tkachuk/limes/commit/ee356f27053288e44bd2f7abd26c94d638d8b545))

## [0.4.0](https://github.com/oleg-tkachuk/limes/compare/v0.3.0...v0.4.0) (2026-10-10)

### ⚠ BREAKING CHANGES

* AuthorizationDetail, AuthorizationDetailsType and their
  marshal and parse functions are removed; caveats travel in limes_caveats,
  with Ops as "ops".
* Capability.OnBehalfOf, Capability.Actor, Actor and
  MaxDelegationDepth are removed; a delegated token's sub is again its own
  principal's subject.
* IssuerMetadata, DiscoverIssuer, MetadataHandler,
  RevocationHandler, IntrospectionHandler and StandardVerifier.Introspect are
  removed.
* tokens carry typ limes-cap-v3+jwt; v2 tokens are refused.
* ValidateResourceIndicator is removed; Issue and Delegate
  accept any non-empty audience entry.

### Features

* move the token format to v3 ([bd1af19](https://github.com/oleg-tkachuk/limes/commit/bd1af1988b253e8f8b7a3cec85350b65381330f0))

### Reverts

* accept any non-empty audience again ([8e6aaf1](https://github.com/oleg-tkachuk/limes/commit/8e6aaf1e7b2b4628b492422bf3ea9fc259c328f4))
* carry caveats in limes_caveats again ([4ed8dd7](https://github.com/oleg-tkachuk/limes/commit/4ed8dd7e50e5c23afd1f287cdc21d2a0310fbc8a))
* drop the RFC 8693 act claim ([507d3e5](https://github.com/oleg-tkachuk/limes/commit/507d3e53afe99f3bf188fc7280337d19923a21a3))

### Code Refactoring

* drop the issuer metadata, revocation and introspection endpoints ([53e71eb](https://github.com/oleg-tkachuk/limes/commit/53e71eba186e00b5ee7d8051a93490da64e3dc5c))

## [0.3.0](https://github.com/oleg-tkachuk/limes/compare/v0.2.0...v0.3.0) (2026-10-10)

### ⚠ BREAKING CHANGES

* AllowedUnitCodes and IsAllowedUnitCode are replaced by
  IsUnitCode.
* capability tokens carry alg Ed25519 and refuse EdDSA.
* Issue and Delegate refuse an audience that is not an
  absolute URI.
* Issue requires Subject.Subject; a delegated token's sub is
  the root capability's subject (Capability.OnBehalfOf), and Delegate with no
  subject delegates to the parent's own.
* MustLoadOrGenerate is replaced by LoadOrGenerateKey, which
  returns a SigningKey; derived kids are thumbprints (DeriveKeyID).
* **dpop:** NewDPoPProof takes a DPoPProofRequest.
* **signer:** Signer.Sign and Issuer.Biscuit take a context.Context.
* the limes_caveats claim is replaced by
  authorization_details, and Caveats.Ops encodes as "actions".
* tokens are typ limes-cap-v2+jwt and earlier ones are
  refused; Caveats.MaxRequests is int64; Nanos encodes as a JSON string.
* **verifier:** Verify takes a VerifyRequest; DPoPVerifier.Check, DPoPRequest
  and the AudiencePlane* constants are removed; MatchPathOnly moved to
  DPoPVerifier.
* **metering:** WithMetering takes options and returns an error;
  MeteringStore is unexported; Refund returns a RefundReceipt; ChargeReceipt
  carries TenantID and UnitCode; tenant_id is opt-in via WithTenantAttribute.

### Features

* accept any ISO 4217 unit code ([9081253](https://github.com/oleg-tkachuk/limes/commit/908125386c2fcc1470f0cb79aeaa0bfc13d899d9))
* answer refusals with RFC 6750 and RFC 9449 challenges and RFC 9457 problems ([d28e3fc](https://github.com/oleg-tkachuk/limes/commit/d28e3fc377b027c75f82c4491658d71c7b488665))
* carry caveats as RFC 9396 authorization details ([5dbbb86](https://github.com/oleg-tkachuk/limes/commit/5dbbb865f069acdbed7b8a2dbfe59bcaaa9462cb))
* derive a key's kid as its RFC 7638 thumbprint ([7d5613a](https://github.com/oleg-tkachuk/limes/commit/7d5613a5aa041019ed11ea91a6a6a87f95d46479))
* **dpop:** support server-provided nonces ([b3547a5](https://github.com/oleg-tkachuk/limes/commit/b3547a5446c70a757a01521defb06c7bb1124ade))
* express delegation as an RFC 8693 act claim ([ccc6a21](https://github.com/oleg-tkachuk/limes/commit/ccc6a21e0b39b73e70eaddc9c3eb201f5a8a886d))
* **jwks:** serve and fetch key sets as RFC 7517 and RFC 9111 describe ([8770196](https://github.com/oleg-tkachuk/limes/commit/877019675bc4676eb5a636664b9b2b583f990901))
* **metering:** bind instruments to the metered store and label every amount ([2ea657d](https://github.com/oleg-tkachuk/limes/commit/2ea657d881d7eeb1e898d88c15abe1625d956c6a))
* name Ed25519 as RFC 9864 fully specifies it ([b169010](https://github.com/oleg-tkachuk/limes/commit/b169010e772671825fe080f942f0ef4e6440b945))
* publish and discover issuer metadata as RFC 8414 defines it ([d480c21](https://github.com/oleg-tkachuk/limes/commit/d480c219b392a702c4197d931926cd63209fe52c))
* require audiences to be RFC 8707 resource indicators ([8fcb6c9](https://github.com/oleg-tkachuk/limes/commit/8fcb6c910caa4f8717b2eeb0acbc9b6c84247acc))
* serve RFC 7009 revocation and RFC 7662 introspection endpoints ([e2e8a9f](https://github.com/oleg-tkachuk/limes/commit/e2e8a9fe4dbef96d54b0de6e2a85dd9b0c4c6ece))
* **signer:** sign under the caller's context ([9a1ab62](https://github.com/oleg-tkachuk/limes/commit/9a1ab62329f1dbe94e303f73f43becb0ac381530))
* **verifier:** enforce proof of possession in Verify and add Authorize ([be3169f](https://github.com/oleg-tkachuk/limes/commit/be3169f65adf5a97f2ef20e23a32114b47538c43))
* **verifier:** read the Authorization header and hold its scheme to the binding ([9b828c3](https://github.com/oleg-tkachuk/limes/commit/9b828c3912c2782547ce7180aa869628f7aa2f9a))
* version the token format and read its claims strictly ([233fec3](https://github.com/oleg-tkachuk/limes/commit/233fec3edc873730aac97dad6a3779f76484f7bb))

### Bug Fixes

* **dpop:** compare htu after RFC 3986 normalisation ([7877521](https://github.com/oleg-tkachuk/limes/commit/78775218dafa51beb90ee560af0455fbefc295c3))
* **dpop:** refuse a proof whose jwk carries a private key ([2dca704](https://github.com/oleg-tkachuk/limes/commit/2dca704624218d3dba038d5c7c02176104a38539))
* issue capability ids as UUIDv7 ([79325ea](https://github.com/oleg-tkachuk/limes/commit/79325eaa5809161e1f960bd9d7d6ae2922a48a11))
* keep a token's integers within the I-JSON range ([a6b0188](https://github.com/oleg-tkachuk/limes/commit/a6b01887a3588af428c81d3d2e91d429972a4124))
* **memstore:** count tenant spend and holds before a ceiling is set ([d06cbf4](https://github.com/oleg-tkachuk/limes/commit/d06cbf45f0dd459b94509232c55a7a62ec719fb0))
* **memstore:** keep a refund of a closed period out of the current one ([924f2f4](https://github.com/oleg-tkachuk/limes/commit/924f2f4b8559238152349792b4b3ce00a8199567))
* **memstore:** refuse an amount in a unit its counters do not count in ([20da83d](https://github.com/oleg-tkachuk/limes/commit/20da83d7b6d414ce6f981026738484f369669a04))
* **memstore:** refuse settling the zero reservation id ([e4f0bb8](https://github.com/oleg-tkachuk/limes/commit/e4f0bb83d0dc3f0437a8eff26db20716cbcc7064))
* **memstore:** release a reaped capability's holds ([11132c9](https://github.com/oleg-tkachuk/limes/commit/11132c9016452a343d4ca7bfdc5849d032991eeb))
* read amounts as plain decimals only ([0fd804e](https://github.com/oleg-tkachuk/limes/commit/0fd804e8e25815a652b757c30075a3995e526ae2))
* refuse a delegation that starts before its parent ([776772a](https://github.com/oleg-tkachuk/limes/commit/776772adccda8c84d45ba91dc0c2391f8ee369c2))
* refuse a tenant budget cursor that is not a finite decimal ([ef410af](https://github.com/oleg-tkachuk/limes/commit/ef410afe1e9475f6d707a8af338155ef70c04bb6))
* refuse an external ref reused for a different cost ([8bede1c](https://github.com/oleg-tkachuk/limes/commit/8bede1ccd9318db3393e0ac5dcda8fdb4dc20372))
* refuse resources with dot segments ([6366af2](https://github.com/oleg-tkachuk/limes/commit/6366af237963e6c4b6994d0a78572b175ce9c06a))
* start a delegation no earlier than a parent issued ahead of the clock ([8058a49](https://github.com/oleg-tkachuk/limes/commit/8058a49d60299570dbd051dc0f386e53231fec04))

### Performance Improvements

* evict the oldest revocation answer when the cache is full ([4e6f137](https://github.com/oleg-tkachuk/limes/commit/4e6f137b6f4bfb945c70ed15966ae7c95a44a867))
* **memstore:** index the charges ledger ([787d5c2](https://github.com/oleg-tkachuk/limes/commit/787d5c2947985aee7d7b0051254b8fc23142b6f1))

## [0.2.0](https://github.com/oleg-tkachuk/limes/compare/v0.1.0...v0.2.0) (2026-10-09)

### Features

* walk paged listings with AllByPrincipal and AllTenantBudgets ([93fa8f9](https://github.com/oleg-tkachuk/limes/commit/93fa8f9177ca3c0eba6550bfb0cf6aa9499757f3))

## [0.1.0](https://github.com/oleg-tkachuk/limes/compare/v0.0.0...v0.1.0) (2026-10-09)

### Features

* import the capability module as limes ([a576e67](https://github.com/oleg-tkachuk/limes/commit/a576e67c90984e9d00c822c281719beceffcd9b4))
* name the wire format and metrics after limes ([bc38872](https://github.com/oleg-tkachuk/limes/commit/bc388723f13cab3600327f0666a5bf3cad6c52fe))
