# ADR-0010: The JWT subject verifier in a package of its own

**Status:** Accepted
**Date:** 2026-10-29
**Deciders:** Oleg Tkachuk
**Relates to:** [ADR-0003](0003-oauth-token-exchange.md), [ADR-0007](0007-pgstore-components.md)

## Context

Package `exchange` holds the Exchanger, its Policy, the RFC 8693 Handler,
introspection, metadata and narrowing. It also holds `JWTVerifier`, the
`SubjectVerifier` for RFC 9068 access tokens, with `KeySource`,
`StaticKeys` and `CachedKeys`. These four are the only code in the package
that imports `lestrrat-go/jwx/v3`, and through it `httprc/v3`.

A consumer that already verifies its identity provider's tokens, on go-oidc
and go-jose say, implements `SubjectVerifier` itself. It still links a
second JOSE stack and its HTTP cache, because the types share a package
with the Exchanger it needs.

## Decision

1. **`exchange/jwtverify`** holds the verifier. It is in the same module,
   so no new release line is created.

   | Before (`exchange`) | After (`exchange/jwtverify`) |
   |---|---|
   | `JWTVerifier` | `Verifier` |
   | `JWTVerifierConfig` | `Config` |
   | `NewJWTVerifier` | `New` |
   | `KeySource` | `KeySource` |
   | `StaticKeys` | `StaticKeys` |
   | `CachedKeys` | `CachedKeys` |
   | `DefaultAlgorithms` | `DefaultAlgorithms` |
   | `TypeAccessToken` | `TypeAccessToken` |
   | `TypeAccessTokenMedia` | `TypeAccessTokenMedia` |
   | `ClaimClientID` | `ClaimClientID` |

   Its refusals are still `exchange.ErrInvalidGrant`.

2. **`exchange` keeps `Subject` and `SubjectVerifier`,** the seam, and
   imports no JOSE library.

3. **A test holds it there.** `exchange/isolation_test.go` lists the deps of
   package `exchange` and fails on `lestrrat-go`, as the root's
   isolation_test.go does for its own.

4. **No alias is left behind.** An alias in `exchange` would import
   `jwtverify`, so the package would link jwx again, the very thing this
   change removes. The move is breaking, and docs/migration.md records it
   with the code before and after.

## Consequences

- **Import paths change.** A consumer of `exchange.NewJWTVerifier` changes
  one import and three names.
- **A consumer with its own verifier** links neither jwx nor httprc.
- **Tests in package `exchange` cannot import `jwtverify`,** which imports
  `exchange`. They verify their identity provider's tokens through a small
  test verifier of their own. The real verifier is tested in its own
  package.
