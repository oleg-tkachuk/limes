# Standards

Which IETF standards limes implements, how, and where it chooses among what
a standard allows. Each section names the API that carries the standard and
the test that holds it there.

| Standard | What it governs here | API |
|---|---|---|
| [RFC 7519](https://www.rfc-editor.org/rfc/rfc7519), [RFC 8725](https://www.rfc-editor.org/rfc/rfc8725) | The capability token: a JWT, explicitly typed | `Signer`, `Verifier.Verify` |
| [RFC 9864](https://www.rfc-editor.org/rfc/rfc9864) | `alg: Ed25519`, and the deprecated `EdDSA` read and, by choice, written | tokens, JWKS, DPoP |
| [RFC 7493](https://www.rfc-editor.org/rfc/rfc7493) | I-JSON: the claims every JSON reader reads alike | claim parsing |
| [RFC 9449](https://www.rfc-editor.org/rfc/rfc9449), [RFC 7800](https://www.rfc-editor.org/rfc/rfc7800) | Proof of possession (DPoP), `cnf.jkt`, nonces, the `DPoP` scheme | `dpop.Verifier`, `dpop.Sign`, `dpop.HMACNonces` |
| [RFC 3986](https://www.rfc-editor.org/rfc/rfc3986) | URI normalisation for `htu` | DPoP checks |
| [RFC 9110](https://www.rfc-editor.org/rfc/rfc9110), [RFC 6750](https://www.rfc-editor.org/rfc/rfc6750) | The `Authorization` header and `WWW-Authenticate` challenges | `ParseAuthorization`, `ErrorResponse` |
| [RFC 9457](https://www.rfc-editor.org/rfc/rfc9457) | Problem details bodies | `ErrorResponse`, `WriteErrorResponse` |
| [RFC 7517](https://www.rfc-editor.org/rfc/rfc7517), [RFC 7638](https://www.rfc-editor.org/rfc/rfc7638), [RFC 8037](https://www.rfc-editor.org/rfc/rfc8037) | JWK Sets, thumbprints, Ed25519 keys | `jwks.Marshal`, `jwks.NewHandler`, `jwks.KeyID` |
| [RFC 9111](https://www.rfc-editor.org/rfc/rfc9111) | How long a fetched JWK Set stays fresh | `jwks.Remote` |
| [RFC 9562](https://www.rfc-editor.org/rfc/rfc9562) | UUIDv7 capability ids | `Issuer.Issue` |
| [RFC 8693](https://www.rfc-editor.org/rfc/rfc8693), [RFC 8707](https://www.rfc-editor.org/rfc/rfc8707), [RFC 9068](https://www.rfc-editor.org/rfc/rfc9068), [RFC 9396](https://www.rfc-editor.org/rfc/rfc9396), [RFC 9728](https://www.rfc-editor.org/rfc/rfc9728), [RFC 8414](https://www.rfc-editor.org/rfc/rfc8414) | An identity provider's access token exchanged for a capability, and the metadata an MCP server and the exchange publish | `exchange.Exchanger`, `exchange.Handler`, `exchange.NewResourceMetadataHandler` |
| [RFC 4648](https://www.rfc-editor.org/rfc/rfc4648) §5 | base64url without padding, everywhere | token segments, JWKS, DPoP, nonces |
| ISO 4217 | Budget units | `IsUnitCode` |

## The token

**RFC 7519, RFC 8725, RFC 9068.** A capability token is a JWS-signed JWT,
typed `at+jwt`: an OAuth access token in the JWT profile of RFC 9068, so
validators of that profile read it. The algorithm is fixed by the verifier
rather than read from the token (§3.1), and `iss` and `aud` are always
checked (§3.8, §3.9). The type names no format, so the claim `limes_v: 7`
does, and the claims are read strictly under it: a token without `limes_v`
7, a `client_id`, a `sub` and every `limes_` claim, or with a member the
format does not define, is refused. Every id in the claims (`jti`, `limes_parent_id`, a tenant, a run) is read
only in the canonical spelling the signer writes.

RFC 8725 §3.11's explicit typing kept other JWTs signed by the same key from
passing as a capability; `at+jwt` is every authorization server's type, so
the key does that instead. Its kid is in limes's namespace
(`jwks.KeyIDPrefix`, `limes:`): a signer will not sign under any other, and
a verifier refuses a token under any other before it looks a key up. A key
that signs capabilities never signs an authorization server's access tokens
([ADR-0006](adr/0006-one-token-format.md)). The retired formats, typed
`limes-cap-v6+jwt` and earlier, are refused. *Held by*
`TestVerifyRequiresCapabilityTyp`, `TestVerifyFixesTheAlgorithm`,
`TestVerify_UntrustedIssuer`, `TestVerify_AudienceMismatch`,
`TestATokenIsAnAccessToken`, `TestClaimsAreReadUnderTheirFormat`,
`TestClientIDIsRequired`, `TestSubjectIsRequired`,
`TestSignerWritesAnAccessTokenUnderALimesKid`,
`TestVerifyRefusesAKidOutsideTheNamespace`, `TestIsKeyID`,
`TestClaimIDsAreCanonical`, `TestRetiredTokensAreRefused` and `TestGoldenTokenStillVerifies`.

**RFC 9864.** JWKS entries and DPoP proofs carry the fully-specified
`alg: Ed25519`, and so do tokens by default. The polymorphic `EdDSA`, which
RFC 9864 deprecates, names the same algorithm over the same key, and is
read wherever it appears: in a token, a DPoP proof or a JWKS entry. A signer
writes it when `SignerConfig.Algorithm` is `SigningAlgorithmEdDSA`, for the
JOSE libraries, gateways and identity providers that know no other name.
A JWKS entry names the alg of the tokens its key signs: `Ed25519` unless
`jwks.MarshalAlgorithms` or `jwks.HandlerConfig.Algorithms` names `EdDSA`
for it, as a library that matches a key's `alg` to the token's, Node's jose
among them, finds a key only under the name its tokens use. No other name
is read or written, in any case but its own. DPoP challenges
advertise `algs="Ed25519 ES256"` (`dpop.SigningAlgorithms`); `EdDSA` is
never advertised. *Held by* `TestWritesTheFullySpecifiedAlgorithm`,
`TestMarshalWritesTheFullySpecifiedAlgorithm`,
`TestReadsEitherNameOfEd25519`, `TestSignerWritesTheConfiguredAlgorithm`,
`TestNewSignerRefusesAnotherAlgorithm`, `TestVerifyFixesTheAlgorithm`,
`TestReadsTheDeprecatedAlgorithmFromOthers`, `TestMarshalAlgorithms`,
`TestHandlerAlgorithms`.

**RFC 7493 (I-JSON).** The claims — the standard ones, and the
capability's own under `limes_` (`limes_principal`, `limes_caveats`,
`limes_parent_id`, `limes_bsk`) — are read with the semantics of
`encoding/json/v2`: no member twice (§2.3), names in their exact case, UTF-8
only (§2.1), and no member the format does not define — a caveat a verifier
does not know is one it cannot enforce. Every integer is within ±(2^53 − 1)
(§2.2, `MaxIJSONInteger`) — `Issue` refuses to sign one outside it, as
`Verify` refuses to read one; amounts, which need eighteen digits, are decimal
strings (`"max_budget": "1.5"`). A reader in any language therefore reads the
token the issuer signed. *Held by* `TestVerifyReadsClaimsStrictly`,
`TestTokenIntegersStayWithinIJSON`, `TestTokenClaimsAreUTF8`.

**Other JOSE libraries.** A library that is not limes verifies a capability
through the issuer's JWKS, checking `iss`, `aud` and `exp` itself, and reads
the `limes_` claims as JSON: jwx, which knows `Ed25519`, as it is, and
golang-jwt, which knows the algorithm only as `EdDSA`, once the RFC 9864 name
is registered for its Ed25519 method. A token whose signer writes `EdDSA`
verifies under golang-jwt with no alias, and under go-jose, which refuses
`Ed25519`. Node's jose verifies a capability under either name through a
JWKS whose entries name each key's alg, and finds no key for an `EdDSA` one
under an entry naming `Ed25519`. In turn, limes accepts the DPoP proofs
jwx signs under `Ed25519`, `EdDSA` and `ES256`. *Held by*
`TestJWXVerifiesACapability`, `TestJWXReadsTheCapabilityClaims`,
`TestJWXReadsTheJWKS`, `TestLimesAcceptsJWXProofs` and
`TestGolangJWTVerifiesWithAnAlias`, `TestGoJOSEVerifiesAnEdDSACapability`,
`TestGolangJWTVerifiesAnEdDSACapability` and
`TestJWXVerifiesAnEdDSACapability`, in `examples/internal/interop`, and
`interop.test.ts` beside its Node fixture.

**RFC 9068.** A validator of the profile finds nothing wrong with a
capability. An `at+jwt` jwx signs with the issuer's own key passes as one
only with a capability's payload and under a limes kid; an access token's
payload, a capability's under another type, or one under another kid, is
refused. *Held by* `TestACapabilityIsAnRFC9068AccessToken`,
`TestRFC9068ValidatorAcceptsAnAccessToken`, `TestLimesRefusesAnAccessToken`
and `TestLimesRefusesAnAccessTokenPayload`.

**RFC 8693 §4.1 in a token.** A capability acting for another
(`Capability.OnBehalfOf`) names that principal as `sub`, as RFC 9068 §2.2
has a resource owner named, the whole principal in `limes_on_behalf_of`,
and its holder in `act`, the holders it was delegated through nested within
(`Capability.PriorActors`). Delegation derives both: a child acts for its
parent's principal, or for the parent's holder when the parent acts for
itself. The holder stays `limes_principal`, which limes reads. A token whose
`sub`, `act` and `limes_principal` disagree, whose `act` nests more than
`MaxDelegationDepth` prior actors, or names prior actors without a parent, is
refused ([ADR-0005](adr/0005-acting-on-behalf.md)). *Held by*
`TestActingOnBehalfOf`, `TestActingForItself`,
`TestIssueRefusesOnBehalfOf`, `TestActingClaimsRefused`,
`TestExchangeActsOnBehalfOfTheSubject` and
`TestJWXReadsWhomACapabilityActsFor`.

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
  new one first (`dpop.SealNonceKeys`). Such a nonce is bound to no client or key and may be reused
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
- **Token endpoints (§5).** `dpop.Verifier.CheckTokenRequest` checks the
  proof that a token request carries the way `Check` checks any other, with
  two differences: the proof names no access token (no `ath`), and any key
  may sign it. `CheckedProof.JKT` is that key's thumbprint, to bind the
  issued token to. The proof counts against the authenticated client's share
  of the replay cache. `dpop.Sign` with no token signs such a proof.

*Held by* `TestDPoPRefusals`, `TestDPoPRefusesAPrivateJWK`,
`TestVerifyEnforcesProofOfPossession`, `TestBiscuitProofIsOverTheBiscuit`,
`TestNormalize`, `TestRemoveDotSegments`, `TestDPoPMatchesNormalizedURIs`,
`TestHMACNonces`, `TestVerifyRequiresTheServerNonce`,
`TestCheckTokenRequestBindsToTheProofKey`, `TestCheckTokenRequestRefusals`,
`TestCheckWantsATokenAndAProofOverIt`,
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
| a well-formed request that disagrees with what is on record (`ErrConflict`: `ErrAlreadyExists`, `ErrUnitCodeMismatch`, `meter.ErrExternalRefConflict`, `meter.ErrSettleConflict`, `meter.ErrTenantBudgetVersionMismatch`) | 409 | none |
| anything else — an outage, or a key-bound token at a verifier with no DPoP (`ErrProofUnsupported`) | 500 | none |

A verifier with DPoP configured also adds a `DPoP algs="…"` challenge to a
`Bearer` one, as §7.1 allows. The body is `application/problem+json` with a
type URI per refusal, ending in its `Reason`, which the body also carries as
a `reason` extension member (§3.2); a refusal with no reason has its family's
type. Descriptions are fixed per kind: an error's own
text, which can name kids, issuers or stores, never reaches the client, and
the values keep to the characters RFC 6750 §3 allows. *Held by*
`TestErrorResponse`, `TestErrorResponseLeaksNothing`,
`TestAuthParamKeepsTheQuotedStringWhole`, `TestWriteErrorResponse`,
`TestProblemNamesTheReason`.

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

## Token exchange

**RFC 8693, RFC 8707, RFC 9068, RFC 9396.** The `exchange` module
exchanges an identity provider's access token for a capability
([ADR-0003](adr/0003-oauth-token-exchange.md)), in process (`Exchanger`) or
at an RFC 8693 token endpoint (`Handler`):

- **The subject token (RFC 9068).** `JWTVerifier` accepts an access token
  of `typ: at+jwt` (or `application/at+jwt`), signed by an asymmetric
  algorithm under the identity provider's keys, from its issuer, live, and
  carrying every claim the profile requires: `iss`, `exp`, `aud`, `sub`,
  `client_id`, `iat`, `jti`. The signature is checked under the
  algorithm the header names, with the key its kid names only if that key
  is of the algorithm's type and curve and names no other algorithm, so a
  key set holding keys for several algorithms, an HMAC secret among them,
  lends none to another. A provider that types its tokens otherwise, or
  names the client in another claim, is read only by naming either in its
  config. A token bound otherwise than by a DPoP key (`cnf` without `jkt`,
  or with more) is refused: a bearer capability would drop the binding.
  Keys its `KeySource` cannot give, the identity provider's JWKS never
  fetched, are the server's failure, not a refusal of the token.
  `exchangetest.TestSubjectVerifier` holds a verifier of one's own to the
  same contract.
- **The audience.** A subject token is accepted only when its `aud` names
  the client exchanging it. The capability's audience is drawn from the
  `resource` (RFC 8707: absolute URIs, no fragment) and `audience`
  parameters asked for, whatever the policy answers. The capability ends no
  later than the subject token.
- **The request (RFC 8693 §2.1).** `grant_type` is token exchange;
  `subject_token_type` is an access token or a JWT; `requested_token_type`,
  when sent, is the capability's own URI; `actor_token` is not supported;
  no parameter but `resource` and `audience` is sent twice (RFC 6749 §3.2).
  `authorization_details` (RFC 9396) is a JSON array, handed to the policy,
  which alone reads it.
- **Acting for the subject.** A policy may have the capability act on
  behalf of the subject token's subject (`IssueRequest.OnBehalfOf`), whose
  token then names them as `sub` and the holder as `act`, and on behalf
  of no one else: a policy naming another is the server's fault.
- **The response.** A capability, `issued_token_type` its URI,
  `token_type` `DPoP` when bound and `Bearer` otherwise; `Cache-Control:
  no-store`. A refusal is RFC 6749 §5.2's JSON with RFC 8707's
  `invalid_target` and RFC 9449's `invalid_dpop_proof` and
  `use_dpop_nonce`, and a fixed description per code.
- **Proof of possession (RFC 9449 §5).** A proof binds the capability to its
  key, through `dpop.Verifier.CheckTokenRequest`, recorded before the
  capability is issued; a subject token bound to a key is exchanged only with
  a proof by that key.
- **A capability as the subject token.** `Narrow`, or the token endpoint
  given `subject_token_type` of the capability's URI, delegates a narrower
  capability from one addressed to the exchange (`Issuer.Delegate`): a
  parent bound to a key needs a proof over it and its child keeps the
  binding; an unbound parent's child is bound by a token request's proof.
- **Metadata (RFC 9728, RFC 8414).** `NewResourceMetadataHandler` serves an
  MCP server's protected resource metadata, `ResourceMetadataURL` names its
  well-known URL, and `ResourceChallenge` the 401 challenge that points to
  it for a request with no token. `WithResourceMetadata` adds that URL to
  each challenge the verifier answers a refused token with, keeping its
  error code, scheme and algorithms. `NewAuthorizationServerMetadataHandler` describes the token exchange
  as an authorization server with a token endpoint alone: token exchange
  its one grant, and no response type.

- **Introspection (RFC 7662).** `exchange.IntrospectionHandler`, over
  `Verifier.Introspect`, answers a gateway that does not read capability
  tokens itself. It authenticates its caller (§2.1). An inactive token
  (expired, revoked, lifted out of its Biscuit, not a token) gets
  `{"active": false}` and nothing more (§2.2). An active one gets its
  standard members, `sub` and `act` as the token names them, `client_id`,
  `token_type` and `cnf` for a bound one (RFC 9449 §6.2), and what it
  grants as one `authorization_details` entry (RFC 9396 §9.2): of type
  `exchange.AuthorizationDetailsTypeCaveats`, its other members those of
  the token's `limes_caveats` claim (`limes.MarshalCaveats`), each request
  and budget ceiling lowered to the tightest a Biscuit copy's blocks set.
  A copy with limits of its own on a verifier without `MeterCopies` is
  answered inactive, as `Verify` refuses it. A store that
  cannot answer is a 500, never an inactive token. With
  `IntrospectionConfig.Audiences`, a token whose `aud` names none of the
  calling client's audiences is answered inactive (§4). Neither audience nor
  proof of possession is checked: the answer is no authorization, and
  `Introspect` yields nothing `Delegate` takes.

*Held by* `TestJWTVerifierReadsAnAccessToken`, `TestJWTVerifierRefusals`,
`TestJWTVerifierHoldsTheKeyToTheHeaderAlg`, `TestJWTVerifierLendsNoHMACSecret`,
`TestExchangeHoldsThePolicyToTheRequest`,
`TestExchangeCapsTheLifetimeAtTheSubjectToken`, `TestExchangeRefusals`,
`TestExchangeBindsToTheProofKey`, `TestExchangeHoldsABoundSubjectToItsKey`,
`TestHandlerExchanges`, `TestHandlerRefusals`, `TestNarrowDelegates`,
`TestNarrowHoldsABoundParentToItsKey`, `TestNarrowRefusals`,
`TestWellKnownURLs`, `TestResourceMetadataHandler`,
`TestAuthorizationServerMetadataHandler`, `TestKeysOutageIsTheServers`,
`TestJWTVerifierKeepsTheContract`, `TestIntrospectionActive`,
`TestIntrospectionInactive`, `TestIntrospectionOutage`, `TestIntrospectionHoldsACopyToItsLimits`, `TestIntrospectionHoldsTheClientToItsAudiences`, `TestEffectiveCaveats`, `TestIntrospectHoldsACopyToItsLimits`, `TestIntrospectActive`,
`TestIntrospectInactive`, `TestIntrospectOutageIsNotInactive`, `TestMarshalCaveatsIsTheClaim`, and the fuzz target
`FuzzCheckForm`, in `exchange`; and the `mcp-exchange` example.

## Not adopted

limes is not an OAuth authorization server, and its tokens are read by
limes alone. The standards below serve interoperability with that
ecosystem; they were adopted in v0.3.0 and removed again, since nothing on
either side reads them. Each can return when something does.

- **RFC 9396 in a token.** Caveats travel in `limes_caveats`, not as an
  `authorization_details` entry, so a token holds them once; the token
  exchange reads the parameter in a request, and introspection answers with
  the caveats as an entry.
- **RFC 8707 in a token.** An audience is any non-empty string the services
  agree on, not necessarily an absolute URI; the token exchange asks a
  `resource` to be one.
- **RFC 8414 for limes itself.** Verifiers are configured with the JWKS
  URL; only the token exchange describes itself as an authorization server.
- **RFC 7009 (revocation endpoint).** Revocation is `Store.Revoke`; an
  HTTP surface for it is the consumer's to build.

- **The `Idempotency-Key` header** is an IETF draft
  (draft-ietf-httpapi-idempotency-key-header), not an RFC. The caveat
  `IdempotencyKeyRequired` takes whether a request carried a key from the
  consumer, whatever header it travelled in.
- **Biscuit** has a specification of its own (biscuitsec.org), not an RFC; the
  Biscuit form follows its v3 format through `biscuit-go`, as a container
  with a vocabulary of limes's own rather than a policy language; any Biscuit
  implementation can append a block of that vocabulary
  ([biscuit-profile.md](biscuit-profile.md)).
- **OAuth scopes** (RFC 6749 §3.3) are not used: what a token allows is its
  caveats, which say more than a list of scope strings.
