# Standards

Which IETF standards limes implements, how, and where it chooses among what
a standard allows. Each section names the API that carries the standard and
the test that holds it there.

| Standard | What it governs here | API |
|---|---|---|
| [RFC 7519](https://www.rfc-editor.org/rfc/rfc7519), [RFC 8725](https://www.rfc-editor.org/rfc/rfc8725) | The capability token: a JWT, explicitly typed | `Signer`, `Verifier.Verify` |
| [RFC 9864](https://www.rfc-editor.org/rfc/rfc9864) | `alg: Ed25519` instead of the deprecated `EdDSA` | tokens, JWKS, DPoP |
| [RFC 7493](https://www.rfc-editor.org/rfc/rfc7493) | I-JSON: the claims every JSON reader reads alike | claim parsing |
| [RFC 9449](https://www.rfc-editor.org/rfc/rfc9449), [RFC 7800](https://www.rfc-editor.org/rfc/rfc7800) | Proof of possession (DPoP), `cnf.jkt`, nonces, the `DPoP` scheme | `dpop.Verifier`, `dpop.Sign`, `dpop.HMACNonces` |
| [RFC 3986](https://www.rfc-editor.org/rfc/rfc3986) | URI normalisation for `htu` | DPoP checks |
| [RFC 9110](https://www.rfc-editor.org/rfc/rfc9110), [RFC 6750](https://www.rfc-editor.org/rfc/rfc6750) | The `Authorization` header and `WWW-Authenticate` challenges | `ParseAuthorization`, `ErrorResponse` |
| [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) | Problem details bodies | `ErrorResponse`, `WriteErrorResponse` |
| [RFC 7517](https://www.rfc-editor.org/rfc/rfc7517), [RFC 7638](https://www.rfc-editor.org/rfc/rfc7638), [RFC 8037](https://www.rfc-editor.org/rfc/rfc8037) | JWK Sets, thumbprints, Ed25519 keys | `jwks.Marshal`, `jwks.NewHandler`, `jwks.KeyID` |
| [RFC 9111](https://www.rfc-editor.org/rfc/rfc9111) | How long a fetched JWK Set stays fresh | `jwks.Remote` |
| [RFC 9562](https://www.rfc-editor.org/rfc/rfc9562) | UUIDv7 capability ids | `Issuer.Issue` |
| [RFC 4648](https://www.rfc-editor.org/rfc/rfc4648) §5 | base64url without padding, everywhere | token segments, JWKS, DPoP, nonces |
| ISO 4217 | Budget units | `IsUnitCode` |

## The token

**RFC 7519, RFC 8725.** A capability token is a JWS-signed JWT. Following
the JWT best current practice, it is explicitly typed — `typ:
limes-cap-v5+jwt`, or the `limes-cap-v4+jwt` it replaces (§3.11), so no other JWT signed by the same key passes as a
capability — the algorithm is fixed by the verifier rather than read from the
token (§3.1), and `iss` and `aud` are always checked (§3.8, §3.9). The
version is part of `typ`: a token of another format version is refused
before any claim is read under the wrong schema. *Held by*
`TestVerifyRequiresCapabilityTyp`, `TestVerifyFixesTheAlgorithm`,
`TestVerify_UntrustedIssuer`, `TestVerify_AudienceMismatch`.

**RFC 9864.** Tokens, JWKS entries and DPoP proofs carry the fully-specified
`alg: Ed25519`. The polymorphic `EdDSA` is deprecated by RFC 9864; a
capability token naming it is refused, while a DPoP proof or a JWKS entry
naming it is still read, since those come from clients and tools that may
predate the RFC. DPoP challenges advertise `algs="Ed25519 ES256"`
(`dpop.SigningAlgorithms`); `EdDSA` is never advertised.
*Held by* `TestWritesTheFullySpecifiedAlgorithm`,
`TestMarshalWritesTheFullySpecifiedAlgorithm`,
`TestCapabilityTokenRefusesThePolymorphicAlgorithm`,
`TestReadsTheDeprecatedAlgorithmFromOthers`.

**RFC 7493 (I-JSON).** The claims — the standard ones, and the
capability's own under `limes_` (`limes_principal`, `limes_caveats`,
`limes_parent_id`, `limes_bsk`, and in v4 `limes_gen`) — are read with the semantics of
`encoding/json/v2`: no member twice (§2.3), names in their exact case, UTF-8
only (§2.1), and no member the format does not define — a caveat a verifier
does not know is one it cannot enforce. Every integer is within ±(2^53 − 1)
(§2.2, `MaxIJSONInteger`) — `Issue` refuses to sign one outside it, as
`Verify` refuses to read one; amounts, which need eighteen digits, are decimal
strings (`"max_budget": "1.5"`). A reader in any language therefore reads the
token the issuer signed. *Held by* `TestVerifyReadsClaimsStrictly`,
`TestTokenIntegersStayWithinIJSON`, `TestTokenClaimsAreUTF8`.

## Proof of possession

**RFC 9449 (DPoP), RFC 7800.** A key-bound capability carries the RFC 7638
thumbprint of its key as `cnf.jkt`. `Verify` itself refuses it without a
valid proof — a verifier with no `dpop.Verifier` refuses every key-bound token
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
  `dpop.VerifierConfig.MatchPathOnly` compares the normalised paths alone, and lets
  the proof's path carry extra leading segments, as a proxy's mount prefix
  adds — but for the root path `/`, which must match exactly.
- **Nonces (§8, §9).** With `dpop.VerifierConfig.Nonces` set, every proof must carry
  a nonce the server issued; one without is refused with
  `dpop.ErrNonceRequired` (`use_dpop_nonce`), and
  `Verifier.DPoPNonce` gives the nonce to send in `DPoP-Nonce`.
  `dpop.HMACNonces` issues them statelessly — its issuance time and an HMAC of it
  — so replicas sharing a key, with clocks within `dpop.HMACNonceClockSkew` of each
  other, share nonces without sharing state, and keys rotate by putting the
  new one first. Such a nonce is bound to no client or key and may be reused
  until it expires: it bounds how long before use a proof can be signed, and
  no more.
- **The scheme (§7.1, §7.2).** `ParseAuthorization` reads
  `Authorization: DPoP <token>` and `Authorization: Bearer <token>` (RFC 9110
  §11.6.2, token68). Given the scheme, `Verify` refuses a key-bound token
  presented as `Bearer` — the downgrade §7.2 names — and an unbound one
  presented as `DPoP`.
- **Replay.** `dpop.MemoryReplayCache` remembers `jti`s for one process, keyed by
  the proof key's thumbprint, each capability within its own share
  (`dpop.MemoryReplayCacheConfig.EntriesPerHolder`, default
  `dpop.DefaultReplayEntriesPerHolder`), so one capability — however many keys
  its copies are bound to — cannot crowd another's proofs out. Across replicas, nonces or a shared
  `dpop.ReplayCache` close the window.

*Held by* `TestDPoPRefusals`, `TestDPoPRefusesAPrivateJWK`,
`TestVerifyEnforcesProofOfPossession`, `TestBiscuitProofIsOverTheBiscuit`,
`TestNormalize`, `TestRemoveDotSegments`, `TestDPoPMatchesNormalizedURIs`,
`TestHMACNonces`, `TestVerifyRequiresTheServerNonce`,
`TestParseAuthorization`, `TestVerifyHoldsTheSchemeToTheBinding`, and the
fuzz targets `FuzzDPoPCheck`, `FuzzNormalize`, `FuzzParseAuthorization`
and `FuzzHMACNonceValid`.

## Refusing a request

**RFC 6750, RFC 9449 §7.1, RFC 9457.** `Verifier.ErrorResponse`
turns a refusal into the status, the challenges and the body every service
answers alike, and `WriteErrorResponse` writes them:

| Refusal | Status | Challenge |
|---|---|---|
| no credentials | 401 | `Bearer realm="…"` with no error code (RFC 6750 §3.1), and `DPoP algs="…"` when configured |
| malformed `Authorization` | 400 | `error="invalid_request"` under the scheme presented: `DPoP`, or else `Bearer`, with `DPoP algs="…"` beside it when configured |
| no nonce, or a stale one | 401 | `DPoP error="use_dpop_nonce", algs="Ed25519 ES256"`, with a fresh `DPoP-Nonce` |
| no proof, an invalid or replayed one | 401 | `DPoP error="invalid_dpop_proof", algs="Ed25519 ES256"` |
| invalid, expired, not-yet-valid, revoked token; wrong audience or scheme | 401 | `error="invalid_token"` under the scheme presented (`Bearer` when unknown) |
| the issuer's JWKS never fetched, unreachable past `MaxStale`, or lacking the token's kid while the endpoint fails | 503 | none |
| a caveat refuses the operation | 403 | `error="insufficient_scope"` |
| a request or budget limit is exhausted | 403 | none |
| anything else — an outage, or a key-bound token at a verifier with no DPoP (`ErrProofUnsupported`) | 500 | none |

A verifier with DPoP configured also adds a `DPoP algs="…"` challenge to a
`Bearer` one, as §7.1 allows. The body is `application/problem+json` with a
type URI per kind of refusal. Descriptions are fixed per kind: an error's own
text, which can name kids, issuers or stores, never reaches the client, and
the values keep to the characters RFC 6750 §3 allows. *Held by*
`TestErrorResponse`, `TestErrorResponseLeaksNothing`,
`TestAuthParamKeepsTheQuotedStringWhole`, `TestWriteErrorResponse`.

## Keys

**RFC 7517, RFC 8037, RFC 7638.** `jwks.Marshal` writes a JWK Set of OKP
Ed25519 keys; `jwks.Parse` skips an entry it cannot use rather than refuse the
set, and uses no key whose kid two entries name; a document that is not JSON,
or names a member twice, is refused. `jwks.KeyID` makes a key's kid its RFC 7638 thumbprint, which any JOSE
tool computes alike.

**Serving and fetching (RFC 7517 §8.5, RFC 9110 §13, RFC 9111).**
`jwks.NewHandler` serves a set as `application/jwk-set+json`, with a strong ETag
answering `If-None-Match` with 304 and `Cache-Control: max-age`.
`jwks.Remote` asks for that media type first, revalidates by ETag, and
keeps a set as long as the response's `max-age` less its `Age` says — never
longer than `RefreshInterval`, never shorter than `MinRefreshInterval`;
`no-cache` and `no-store` count as zero. *Held by* `TestResponseFreshness`,
`TestRemoteJWKSHonoursTheResponse`, `TestRemoteJWKSClampsFreshness`,
`TestJWKSHandler`, and the fuzz target `FuzzResponseFreshness`.

## Identifiers and encodings

**RFC 9562.** A capability id is a UUIDv7, so ids sort by issuance and a
store's index on them stays tight. *Held by* `TestIssuedIDsAreVersion7`.

**ISO 4217.** A budget's unit is any ISO 4217 code in its upper-case
spelling, validated against `golang.org/x/text/currency`; `XXX` is the unit
of a budget that is not money. The unit is part of a budget ceiling; one that
lets anything be spent names it, and no unit is ever assumed. *Held by*
`TestUnitCodes`, `TestABudgetNeedsAUnit`, `TestAnAmountNeedsAUnit`,
`TestAbstractUnitIsXXX`.

**RFC 4648 §5.** Every base64 value the module reads or writes — a token's
segments, a JWK's `x`, a DPoP proof, a nonce, a thumbprint — is base64url
without padding, and is read strictly: padding, the standard alphabet and
spare bits set in the last character are refused, so a value has one
spelling. *Held by* `TestVerifyReadsTokenSegmentsAsUnpaddedBase64URL`,
`TestWritesUnpaddedBase64URL`, `TestUnB64IsStrict`, `TestParseReadsXStrictly`.

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
