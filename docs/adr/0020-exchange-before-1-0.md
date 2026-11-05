# ADR-0020: What the exchange freezes at 1.0

**Status:** Accepted
**Date:** 2026-11-05
**Deciders:** Oleg Tkachuk
**Amends:** [ADR-0010](0010-exchange-jose-subpackage.md)
**Relates to:** [ADR-0003](0003-oauth-token-exchange.md), [ADR-0006](0006-one-token-format.md)

## Context

The exchange module reaches v1 with limes. Two of its choices would be
frozen with it:

- **The issued token type.** A capability is issued with
  `issued_token_type` `exchange.IssuedTokenTypeCapability`, a URI of this
  module's own. Since ADR-0006 a capability is an RFC 9068 access token,
  for which RFC 8693 has a registered type,
  `urn:ietf:params:oauth:token-type:access_token`. The handler also reads
  the module's URI as a `subject_token_type`: it sends that request to
  `Exchanger.Narrow`, and an access token or a JWT to the identity
  provider's exchange.
- **jwx in `jwtverify`'s API.** `KeySource.Keys` returned a `jwk.Set`,
  `StaticKeys` took one, and `CachedKeys` took a `*jwk.Cache`. The exchange
  module's v1 line would have followed jwx v3: a jwx v4 could reach it only
  as a new package.

## Decision

1. **The issued token type stays the module's own URI.** It names a limes
   capability in both directions: a client learns from it what it was
   issued, and sends it back as the `subject_token_type` to narrow it.
   RFC 8693 §3 allows any absolute URI. It is an identifier, not a link:
   it need not resolve, and it does not change if the repository moves.
2. **`jwtverify` names no jwx type.** `KeySource` is a struct of this
   package. `StaticKeys` takes a JWK Set document (RFC 7517 §5), and
   `RemoteKeys(ctx, url, client)` keeps a cache of the document at `url`
   itself, with an `*http.Client` of the standard library. jwx stays the
   library the verifier reads keys and tokens with, inside the package.

## Options considered

### The issued token type

**Keep the module's URI (chosen).** No change; the type and the narrowing
route stay one value.

**Issue as `access_token`.** A generic RFC 8693 client would see the
registered type. Narrowing would then be chosen by the subject token's
`limes:` kid rather than by its type, and a client could no longer tell a
capability from the identity provider's access token by the response
alone.

### jwx in the API

**Hide it (chosen).** One breaking change before 1.0; jwx can then move
majors on the v1 line.

**Keep its types.** No change now; a jwx v4 would come as a package beside
`jwtverify` for the rest of v1.

**A nested module for `jwtverify`.** jwx would leave `exchange/go.mod` and
move on a release line of its own, but its types would stay in the API, at
the cost of another module to release.

## Consequences

- A consumer hands `StaticKeys` the document its keys come in, or a URL to
  `RemoteKeys`; a `KeySource` of its own is no longer possible.
- `exchange/go.mod` still requires jwx and httprc (ADR-0010's amendment);
  a consumer that verifies subject tokens on its own stack still resolves
  them, but none of their types reaches its code.
- `docs/migration.md` shows the change under the exchange release that
  makes it.
