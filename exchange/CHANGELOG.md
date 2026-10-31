# Changelog

Every release of the exchange module, newest first, from its release notes. Written by .github/ci/changelog.ts after each release; not edited by hand.

## [0.15.2](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.15.1...exchange%2Fv0.15.2) (2026-10-31)

### Dependencies

* **nested:** require limes v0.143.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([d5ea9e8](https://github.com/oleg-tkachuk/limes/commit/d5ea9e8020b4e8fabdcd05828fca8d62e416d349))

## [0.15.1](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.15.0...exchange%2Fv0.15.1) (2026-10-31)

### Dependencies

* **nested:** require limes v0.142.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([0f40a43](https://github.com/oleg-tkachuk/limes/commit/0f40a43e956e53350e48a54252f842f8f4251f44))

## [0.15.0](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.14.4...exchange%2Fv0.15.0) (2026-10-31)

### Features

* **exchange:** tell the client its token was exchanged already ([20365d3](https://github.com/oleg-tkachuk/limes/commit/20365d3d9994f0217a99c62e48332ad4775beea4))

## [0.14.4](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.14.3...exchange%2Fv0.14.4) (2026-10-30)

### Dependencies

* **nested:** require limes v0.141.1 in redisreplay, pgstore, otelmetric, exchange and biscuit ([9c8bb9c](https://github.com/oleg-tkachuk/limes/commit/9c8bb9c8362dca5ae232b11bb8780d42934f0a5f))

## [0.14.3](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.14.2...exchange%2Fv0.14.3) (2026-10-30)

### Dependencies

* **nested:** require limes v0.141.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([12e7580](https://github.com/oleg-tkachuk/limes/commit/12e75800dd62f8ea793bd9a7c6b3ea3c790367d9))

## [0.14.2](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.14.1...exchange%2Fv0.14.2) (2026-10-30)

### Dependencies

* **nested:** require limes v0.140.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([970e6a6](https://github.com/oleg-tkachuk/limes/commit/970e6a602e8e608b0ea377ce6da8be4ea4b2c223))

## [0.14.1](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.14.0...exchange%2Fv0.14.1) (2026-10-29)

### Bug Fixes

* **exchange:** build wrapped error text with errs.Errorf ([1938d77](https://github.com/oleg-tkachuk/limes/commit/1938d777a96eb618e9dea9d733b5667e7f677bb8))

### Dependencies

* **nested:** require limes v0.139.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([54109c4](https://github.com/oleg-tkachuk/limes/commit/54109c4018282ced403f4f4a0706ab07d648e4e1))

## [0.14.0](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.13.0...exchange%2Fv0.14.0) (2026-10-29)

### Features

* **exchange:** give every error a code ([9ea265e](https://github.com/oleg-tkachuk/limes/commit/9ea265e09afdc9f11bd7f167298b4a02ea5ebf09))

### Dependencies

* **nested:** require limes v0.138.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([ee2c884](https://github.com/oleg-tkachuk/limes/commit/ee2c8849e451c1141db9eab4ef2aebad879ed021))

## [0.13.0](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.12.0...exchange%2Fv0.13.0) (2026-10-29)

### Features

* **exchange:** answer an in-process exchange's errors as the token endpoint does ([7f3b47e](https://github.com/oleg-tkachuk/limes/commit/7f3b47e118eee166a8a877fdf16b1971d5a3a01c))

### Dependencies

* **nested:** require limes v0.137.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([fda9bc1](https://github.com/oleg-tkachuk/limes/commit/fda9bc1252d26c8f6693373b2cde5c279b5f8bcf))

## [0.12.0](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.11.0...exchange%2Fv0.12.0) (2026-10-29)

### Features

* **exchange:** record the subject token's session with its capability ([ee126c9](https://github.com/oleg-tkachuk/limes/commit/ee126c9d08d0361cfc589dde209cb8b512836684))

### Dependencies

* **nested:** require limes v0.136.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([62f5fa7](https://github.com/oleg-tkachuk/limes/commit/62f5fa782dea695e1091522eea0f6be92993105c))

## [0.11.0](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.10.1...exchange%2Fv0.11.0) (2026-10-29)

### Features

* **exchange:** issue one capability per subject token on request ([ba82e97](https://github.com/oleg-tkachuk/limes/commit/ba82e972e11ac59e93524f0065c13cd90a13ba97))

### Dependencies

* **nested:** require limes v0.135.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([9afd08b](https://github.com/oleg-tkachuk/limes/commit/9afd08b7ea891b967105f91e1694802c213cd8a6))

## [0.10.1](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.10.0...exchange%2Fv0.10.1) (2026-10-29)

### Dependencies

* **nested:** require limes v0.134.0 in redisreplay, pgstore, otelmetric, exchange and biscuit ([f043a34](https://github.com/oleg-tkachuk/limes/commit/f043a348f3ef97df23b295aeb0e05703fcbf639b))

## [0.10.0](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.9.2...exchange%2Fv0.10.0) (2026-10-29)

### ⚠ BREAKING CHANGES

* **exchange:** JWTVerifier, JWTVerifierConfig and NewJWTVerifier are
  jwtverify.Verifier, Config and New; KeySource, StaticKeys, CachedKeys,
  DefaultAlgorithms, TypeAccessToken(Media) and ClaimClientID moved with them.

### Features

* **exchange:** move the JWT subject verifier to exchange/jwtverify ([c306566](https://github.com/oleg-tkachuk/limes/commit/c306566aebc143390ce75e241ce5be074a5a2450))

## [0.9.2](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.9.1...exchange%2Fv0.9.2) (2026-10-29)

### Dependencies

* **nested:** require limes v0.133.1 in redisreplay, pgstore, otelmetric, exchange and biscuit ([1f03a8e](https://github.com/oleg-tkachuk/limes/commit/1f03a8e7dfdd589f553b87bb53d0e1b2566c7701))

## [0.9.1](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.9.0...exchange%2Fv0.9.1) (2026-10-29)

### Dependencies

* **nested:** drop biscuit-go and protobuf from the nested modules' graphs ([570efde](https://github.com/oleg-tkachuk/limes/commit/570efde387f5daaba18fdd52ec6cdafdbf888f17))
* **nested:** require limes v0.133.0 in redisreplay, pgstore, otelmetric and exchange ([d5373bc](https://github.com/oleg-tkachuk/limes/commit/d5373bc126382d781533d0c9f83fbb2793a9b365))

## [0.9.0](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.8.1...exchange%2Fv0.9.0) (2026-10-28)

### Features

* **exchange:** answer a server failure in Authenticate as server_error ([028b621](https://github.com/oleg-tkachuk/limes/commit/028b6217b669683e68f57a0c13cda374d85ea6ac))

### Bug Fixes

* **issuer:** refuse a capability no request can use ([363b8b1](https://github.com/oleg-tkachuk/limes/commit/363b8b17e77945520cf17b2ddb6ae069a8c2cad6))

### Dependencies

* **nested:** require limes v0.132.1 in redisreplay, pgstore, otelmetric and exchange ([629ddf8](https://github.com/oleg-tkachuk/limes/commit/629ddf8dad552d2cb97c2d7130d1b35b43866354))

## [0.8.1](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.8.0...exchange%2Fv0.8.1) (2026-10-28)

### Dependencies

* **nested:** require limes v0.132.0 in redisreplay, pgstore, otelmetric and exchange ([d104778](https://github.com/oleg-tkachuk/limes/commit/d1047787a3f4f763e193a4ad77bad24fc69ab375))

## [0.8.0](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.7.0...exchange%2Fv0.8.0) (2026-10-28)

### Features

* **exchange:** add the resource metadata URL to a verifier's challenges ([ced9ca0](https://github.com/oleg-tkachuk/limes/commit/ced9ca090af70e7cb1850703aabede5771785441))

### Bug Fixes

* **exchange:** answer the server's failure when no verifier issues the nonce asked for ([51fbccc](https://github.com/oleg-tkachuk/limes/commit/51fbccc3b860fcc0ca3a3ef1a310b6b4667813b5))
* **exchange:** challenge a client refused in the scheme it authenticated with ([684055a](https://github.com/oleg-tkachuk/limes/commit/684055adfb3e788c61f211512be6a26dac1a3948))
* **exchange:** refuse and answer authorization_details and scope as RFC 9396 and RFC 8693 ask ([c6a7527](https://github.com/oleg-tkachuk/limes/commit/c6a752738fed99a09243eae18fb3b1478aaa81da))
* **exchange:** treat an empty parameter as omitted and send Pragma: no-cache ([677178a](https://github.com/oleg-tkachuk/limes/commit/677178a3cbc39520f585646402f83318ba38b973))

## [0.7.0](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.6.1...exchange%2Fv0.7.0) (2026-10-28)

### Features

* **exchange:** answer an authenticated but unauthorized client as unauthorized_client ([e4d70dd](https://github.com/oleg-tkachuk/limes/commit/e4d70dd0289da307951bba0f66fd0c02ab1fbc8f))

## [0.6.1](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.6.0...exchange%2Fv0.6.1) (2026-10-28)

### Bug Fixes

* **exchange:** send the capabilities verifier's nonce on use_dpop_nonce ([6cd0213](https://github.com/oleg-tkachuk/limes/commit/6cd0213f448347bb260ebae026af3a8aa899f973))

## [0.6.0](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.5.1...exchange%2Fv0.6.0) (2026-10-28)

### Features

* **exchange:** limit what a client may learn by introspection to its audiences ([e2b70cd](https://github.com/oleg-tkachuk/limes/commit/e2b70cd41a487c2d93c67a5a9e25330c4de10984))

### Bug Fixes

* **exchange:** answer a nonce asked for by the capabilities' verifier without a DPoP verifier ([2df92a3](https://github.com/oleg-tkachuk/limes/commit/2df92a34fc53e4e431d60b17816ab0f11031fe06))

### Dependencies

* **nested:** require limes v0.131.2 in redisreplay, pgstore, otelmetric and exchange ([216e112](https://github.com/oleg-tkachuk/limes/commit/216e112da792b285bc127de73bc050494c805240))

## [0.5.1](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.5.0...exchange%2Fv0.5.1) (2026-10-28)

### Bug Fixes

* **exchange:** answer a Biscuit copy's introspection with its own limits ([cc6aa2f](https://github.com/oleg-tkachuk/limes/commit/cc6aa2f99b074903fe0ec6a6df5145ae983dba9a))
* **exchange:** verify a subject token under its header's alg and a key of that kind ([a38843a](https://github.com/oleg-tkachuk/limes/commit/a38843a33bd3bba684e67e8ccb1b8a981ab88371))

### Dependencies

* **nested:** require limes v0.131.1 in redisreplay, pgstore, otelmetric and exchange ([13fde12](https://github.com/oleg-tkachuk/limes/commit/13fde12c521612b0347f6f9e493031824b9370b1))

## [0.5.0](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.4.0...exchange%2Fv0.5.0) (2026-10-27)

### Features

* **exchange:** name sub and act in introspection, as the token does ([da17f73](https://github.com/oleg-tkachuk/limes/commit/da17f73b9ec901f1c38151caafbfce10c0ec3e5b))

### Dependencies

* **nested:** require limes v0.131.0 in redisreplay, pgstore, otelmetric and exchange ([66075e5](https://github.com/oleg-tkachuk/limes/commit/66075e53b124aaf011f2a9e89a30dadfe95b9f79))

## [0.4.0](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.3.0...exchange%2Fv0.4.0) (2026-10-27)

### Features

* **exchange:** answer introspection with the caveats as authorization_details ([a007c49](https://github.com/oleg-tkachuk/limes/commit/a007c496b3cd41b590bb53b85c0d03330db80546))

### Dependencies

* **nested:** require limes v0.130.0 in redisreplay, pgstore, otelmetric and exchange ([e5bb6f3](https://github.com/oleg-tkachuk/limes/commit/e5bb6f30613958e3e31da96687713404b19f8164))

## [0.3.0](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.2.0...exchange%2Fv0.3.0) (2026-10-27)

### Features

* **exchange:** name the subject token's client in the capability ([8f27c9a](https://github.com/oleg-tkachuk/limes/commit/8f27c9abf4c64807eac582351f57b493cd5a4d37))
* **exchange:** serve RFC 7662 token introspection ([ab86948](https://github.com/oleg-tkachuk/limes/commit/ab86948b9e6f6ed0cffcce40a5262c94615070fa))

### Dependencies

* **nested:** require limes v0.128.0 in redisreplay, pgstore, otelmetric and exchange ([aed9664](https://github.com/oleg-tkachuk/limes/commit/aed966419b3fc73000d172ad5cb9002412e52991))
* **nested:** require limes v0.129.0 in redisreplay, pgstore, otelmetric and exchange ([21409b6](https://github.com/oleg-tkachuk/limes/commit/21409b62c3629c3a77063cf71cdc27928dc40541))

## [0.2.0](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.1.1...exchange%2Fv0.2.0) (2026-10-27)

### ⚠ BREAKING CHANGES

* **exchange:** JWTVerifierConfig.Keys is a KeySource; wrap a jwk.Set in
  StaticKeys, or use CachedKeys over a jwk.Cache.

### Features

* **exchange:** add exchangetest, a contract suite for subject verifiers ([c4cf1aa](https://github.com/oleg-tkachuk/limes/commit/c4cf1aa196ee59029d32e4541b2b3f0e1065cc35))
* **exchange:** narrow a capability given as the subject token ([de3e132](https://github.com/oleg-tkachuk/limes/commit/de3e132e622c0b6e4619b91ab43e3d49fb0064ee))
* **exchange:** serve RFC 9728 and RFC 8414 metadata ([f8f07b4](https://github.com/oleg-tkachuk/limes/commit/f8f07b4a65a82f2edaf1d54ab92ad1ea5cb95b9f))
* **exchange:** take the identity provider's keys from a KeySource ([333a1a0](https://github.com/oleg-tkachuk/limes/commit/333a1a0eee18743cac5b50db923c74f5afaf1354))

## [0.1.1](https://github.com/oleg-tkachuk/limes/compare/exchange%2Fv0.1.0...exchange%2Fv0.1.1) (2026-10-27)

### Dependencies

* **nested:** require limes v0.127.0 in redisreplay, pgstore, otelmetric and exchange ([3d8c272](https://github.com/oleg-tkachuk/limes/commit/3d8c2725dcdf02d0a22b63f595bc378946108feb))

## [0.1.0](https://github.com/oleg-tkachuk/limes/releases/tag/exchange/v0.1.0)
