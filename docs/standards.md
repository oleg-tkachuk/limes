# Standards

Which IETF standards limes implements, how, and where it chooses among what
a standard allows. Each section names the API that carries the standard and
the test that holds it there.

| Standard | What it governs here | API |
|---|---|---|
| [RFC 7519](https://www.rfc-editor.org/rfc/rfc7519), [RFC 8725](https://www.rfc-editor.org/rfc/rfc8725) | The capability token: a JWT, explicitly typed | `Signer`, `StandardVerifier.Verify` |
| [RFC 9864](https://www.rfc-editor.org/rfc/rfc9864) | `alg: Ed25519` instead of the deprecated `EdDSA` | tokens, JWKS, DPoP |
| [RFC 7493](https://www.rfc-editor.org/rfc/rfc7493) | I-JSON: the claims every JSON reader reads alike | claim parsing |
| [RFC 9449](https://www.rfc-editor.org/rfc/rfc9449), [RFC 7800](https://www.rfc-editor.org/rfc/rfc7800) | Proof of possession (DPoP), `cnf.jkt`, nonces, the `DPoP` scheme | `DPoPVerifier`, `NewDPoPProof`, `HMACNonces` |
| [RFC 3986](https://www.rfc-editor.org/rfc/rfc3986) | URI normalisation for `htu` | DPoP checks |
| [RFC 9110](https://www.rfc-editor.org/rfc/rfc9110), [RFC 6750](https://www.rfc-editor.org/rfc/rfc6750) | The `Authorization` header and `WWW-Authenticate` challenges | `ParseAuthorization`, `ErrorResponse` |
| [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) | Problem details bodies | `ErrorResponse`, `WriteErrorResponse` |
| [RFC 7517](https://www.rfc-editor.org/rfc/rfc7517), [RFC 7638](https://www.rfc-editor.org/rfc/rfc7638), [RFC 8037](https://www.rfc-editor.org/rfc/rfc8037) | JWK Sets, thumbprints, Ed25519 keys | `MarshalJWKS`, `JWKSHandler`, `DeriveKeyID` |
| [RFC 9111](https://www.rfc-editor.org/rfc/rfc9111) | How long a fetched JWK Set stays fresh | `RemoteJWKSResolver` |
| [RFC 9562](https://www.rfc-editor.org/rfc/rfc9562) | UUIDv7 capability ids | `Issuer.Issue` |
| [RFC 4648](https://www.rfc-editor.org/rfc/rfc4648) §5 | base64url without padding, everywhere | — |
| ISO 4217 | Budget units | `IsUnitCode` |

## The token

**RFC 7519, RFC 8725.** A capability token is a JWS-signed JWT. Following
the JWT best current practice, it is explicitly typed — `typ:
limes-cap-v3+jwt` (§3.11), so no other JWT signed by the same key passes as a
capability — the algorithm is fixed by the verifier rather than read from the
token (§3.1), and `iss` and `aud` are always checked (§3.8, §3.9). The
version is part of `typ`: a token of another format version is refused
before any claim is read under the wrong schema.

**RFC 9864.** Tokens, JWKS entries and DPoP proofs carry the fully-specified
`alg: Ed25519`. The polymorphic `EdDSA` is deprecated by RFC 9864; a
capability token naming it is refused, while a DPoP proof or a JWKS entry
naming it is still read, since those come from clients and tools that may
predate the RFC. DPoP challenges advertise `algs="Ed25519 ES256"`
(`DPoPSigningAlgorithms`); `EdDSA` is never advertised.
*Held by* `TestWritesTheFullySpecifiedAlgorithm`,
`TestCapabilityTokenRefusesThePolymorphicAlgorithm`,
`TestReadsTheDeprecatedAlgorithmFromOthers`.

**RFC 7493 (I-JSON).** The claims — the standard ones, and the
capability's own under `limes_` (`limes_principal`, `limes_caveats`,
`limes_parent_id`) — are read with the semantics of
`encoding/json/v2`: no member twice (§2.3), names in their exact case, UTF-8
only (§2.1), and no member the format does not define — a caveat a verifier
does not know is one it cannot enforce. Every integer is within ±(2^53 − 1)
(§2.2, `MaxIJSONInteger`); amounts, which need eighteen digits, are decimal
strings (`"max_budget": "1.5"`). A reader in any language therefore reads the
token the issuer signed. *Held by* `TestVerifyReadsClaimsStrictly`,
`TestTokenIntegersStayWithinIJSON`, `TestTokenClaimsAreUTF8`.

## Proof of possession

**RFC 9449 (DPoP), RFC 7800.** A key-bound capability carries the RFC 7638
thumbprint of its key as `cnf.jkt`. `Verify` itself refuses it without a
valid proof — a verifier with no `DPoPVerifier` refuses every key-bound token
— so a binding cannot be lost to a forgotten call. The proof is checked as
§4.3 lists: `typ: dpop+jwt`, an asymmetric algorithm, a signature by the key
in its header, that key's thumbprint equal to `cnf.jkt`, no private key in
the `jwk` (item 7), `htm` and `htu` matching the request, `ath` the hash of
the token as presented (the Biscuit, when one is), `iat` within the window,
and a `jti` not seen before.

- **htu (§4.3 item 9, RFC 3986 §6.2.2–6.2.3).** Compared after
  normalisation: scheme and host in lower case, the default port dropped, an
  empty path as `/`, unreserved characters' percent-encodings decoded and
  every other one in upper case, dot segments removed (§5.2.4); no query or
  fragment. A URI with userinfo is refused (RFC 9110 §4.2.4). Behind a proxy,
  `DPoPVerifier.MatchPathOnly` compares the normalised paths alone, and lets
  the proof's path carry extra leading segments, as a proxy's mount prefix
  adds.
- **Nonces (§8, §9).** With `DPoPVerifier.Nonces` set, every proof must carry
  a nonce the server issued; one without is refused with
  `ErrDPoPNonceRequired` (`use_dpop_nonce`), and
  `StandardVerifier.DPoPNonce` gives the nonce to send in `DPoP-Nonce`.
  `HMACNonces` issues them statelessly — its issuance time and an HMAC of it
  — so replicas sharing a key share nonces without sharing state, and keys
  rotate by putting the new one first.
- **The scheme (§7.1, §7.2).** `ParseAuthorization` reads
  `Authorization: DPoP <token>` and `Authorization: Bearer <token>` (RFC 9110
  §11.6.2, token68). Given the scheme, `Verify` refuses a key-bound token
  presented as `Bearer` — the downgrade §7.2 names — and an unbound one
  presented as `DPoP`.
- **Replay.** `MemoryReplayCache` remembers `jti`s for one process. Across
  replicas, nonces or a shared `ReplayCache` close the window.

*Held by* `TestDPoPRefusals`, `TestDPoPRefusesAPrivateJWK`,
`TestVerifyEnforcesProofOfPossession`, `TestBiscuitProofIsOverTheBiscuit`,
`TestNormalizeURI`, `TestRemoveDotSegments`, `TestDPoPMatchesNormalizedURIs`,
`TestHMACNonces`, `TestVerifyRequiresTheServerNonce`,
`TestParseAuthorization`, `TestVerifyHoldsTheSchemeToTheBinding`, and the
fuzz targets `FuzzDPoPCheck` and `FuzzNormalizeURI`.

## Refusing a request

**RFC 6750, RFC 9449 §7.1, RFC 9457.** `StandardVerifier.ErrorResponse`
turns a refusal into the status, the challenges and the body every service
answers alike, and `WriteErrorResponse` writes them:

| Refusal | Status | Challenge |
|---|---|---|
| malformed `Authorization` | 400 | `Bearer error="invalid_request"` |
| no nonce, or a stale one | 401 | `DPoP error="use_dpop_nonce", algs="Ed25519 ES256"`, with a fresh `DPoP-Nonce` |
| no proof, an invalid or replayed one | 401 | `DPoP error="invalid_dpop_proof", algs="Ed25519 ES256"` |
| invalid, expired, not-yet-valid, revoked token; wrong audience or scheme; an unreachable JWKS | 401 | `error="invalid_token"` under the scheme presented (`Bearer` when unknown) |
| a caveat refuses the operation | 403 | `error="insufficient_scope"` |
| a request or budget limit is exhausted | 403 | none |
| anything else | 500 | none |

A verifier with DPoP configured also adds a `DPoP algs="…"` challenge to a
`Bearer` one, as §7.1 allows. The body is `application/problem+json` with a
type URI per kind of refusal. Descriptions are fixed per kind: an error's own
text, which can name kids, issuers or stores, never reaches the client, and
the values keep to the characters RFC 6750 §3 allows. *Held by*
`TestErrorResponse`, `TestErrorResponseLeaksNothing`,
`TestAuthParamKeepsTheQuotedStringWhole`, `TestWriteErrorResponse`.

## Keys

**RFC 7517, RFC 8037, RFC 7638.** `MarshalJWKS` writes a JWK Set of OKP
Ed25519 keys; `ParseJWKS` skips an entry it cannot use rather than refuse the
set. `DeriveKeyID` makes a key's kid its RFC 7638 thumbprint, which any JOSE
tool computes alike.

**Serving and fetching (RFC 7517 §8.5, RFC 9110 §13, RFC 9111).**
`JWKSHandler` serves a set as `application/jwk-set+json`, with a strong ETag
answering `If-None-Match` with 304 and `Cache-Control: max-age`.
`RemoteJWKSResolver` asks for that media type first, revalidates by ETag, and
keeps a set as long as the response's `max-age` less its `Age` says — never
longer than `RefreshInterval`, never shorter than `MinRefreshInterval`;
`no-cache` and `no-store` count as zero. *Held by* `TestResponseFreshness`,
`TestRemoteJWKSHonoursTheResponse`, `TestRemoteJWKSClampsFreshness`,
`TestJWKSHandler`.

## Identifiers and encodings

**RFC 9562.** A capability id is a UUIDv7, so ids sort by issuance and a
store's index on them stays tight. *Held by* `TestIssuedIDsAreVersion7`.

**ISO 4217.** A budget's unit is any ISO 4217 code in its upper-case
spelling, validated against `golang.org/x/text/currency`; `XXX` is the unit
of a budget that is not money.

## Not adopted

limes is not an OAuth authorization server, and its tokens are read by
limes alone. The standards below serve interoperability with that
ecosystem; they were adopted in v0.3.0 and removed again, since nothing on
either side reads them. Each can return when something does.

- **RFC 9396 (Rich Authorization Requests).** Caveats travel in
  `limes_caveats`, not as an `authorization_details` entry.
- **RFC 8693 (Token Exchange).** Delegation is followed through
  `limes_parent_id`; a delegated token's `sub` is its own principal's, and it
  has no `act` claim.
- **RFC 8707 (Resource Indicators).** An audience is any non-empty string the
  services agree on, not necessarily an absolute URI.
- **RFC 8414 (Authorization Server Metadata).** Its well-known document
  describes an OAuth authorization server and requires members — such as
  `response_types_supported` — that limes has nothing to put in. Verifiers
  are configured with the JWKS URL.
- **RFC 7009 and RFC 7662 (revocation and introspection endpoints).**
  Revocation is `Store.Revoke`, and a resource server verifies a token
  itself; an HTTP surface for either is the consumer's to build.

- **The `Idempotency-Key` header** is an IETF draft
  (draft-ietf-httpapi-idempotency-key-header), not an RFC. The caveat
  `IdempotencyKeyRequired` takes whether a request carried a key from the
  consumer, whatever header it travelled in.
- **Biscuit** has a specification of its own (biscuitsec.org), not an RFC; the
  Biscuit form follows its v3 format through `biscuit-go`.
- **OAuth scopes** (RFC 6749 §3.3) are not used: what a token allows is its
  caveats, which say more than a list of scope strings.
