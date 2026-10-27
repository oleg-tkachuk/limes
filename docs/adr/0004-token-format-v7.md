# ADR-0004: A capability format that is an RFC 9068 access token

**Status:** Accepted
**Date:** 2026-10-27
**Deciders:** Oleg Tkachuk

## Context

A capability token is typed `limes-cap-v6+jwt`. Following RFC 8725 §3.11,
the type is the capability's own, so that no other JWT signed by the same
key passes as one, and it names the format version, so that a token of
another version is refused before any claim is read under the wrong schema.

That type is also why the gateways, resource servers and identity providers
that validate OAuth access tokens do not read a capability. A validator of
RFC 9068, the JWT profile for access tokens, refuses one for exactly two
reasons: its `typ` is not `at+jwt`, and it carries no `client_id`
(`examples/internal/interop`, `TestACapabilityIsNotAnRFC9068AccessToken`).
Every other claim the profile requires is there.

1.0 freezes the format a verifier reads. A second format is a new token
type, accepted by verifiers before issuers mint it
(`VerifierConfig.AcceptTokenTypes`, `SignerConfig.TokenType`). Shipping that
type with 1.0 lets every 1.0 verifier read it, so an issuer can switch
whenever its readers need the profile.

## Decision

1. **`TokenTypeV7` is `at+jwt`.** Its claims are v6's, plus RFC 9068's
   `client_id` and `limes_v: 7`. `limes_v` names the format now that the
   type no longer can. Both are required of a v7 token and refused in a v6
   one.
2. **The client is the capability's own field.** `Capability.ClientID`, set
   by `IssueRequest.ClientID`, is inherited by a child unless
   `DelegateRequest.ClientID` names another, and is persisted by the stores.
   The exchange sets it from the subject token's client. A v7 signer refuses
   a capability with none; nothing is invented. A v6 token leaves it out:
   it describes the grant and restricts nothing, so dropping it widens
   nothing.
3. **Verifiers accept both types by default; signers still write v6.**
   Switching an issuer to v7 is a rollout step its operator takes, once
   every verifier of its tokens runs a version that reads v7.
4. **The discriminator is the schema and the key.** A v7 verifier reads an
   `at+jwt` strictly: `limes_v` must be 7, `client_id` present, and every
   `limes_` claim present with no member it does not define. An access token
   of an authorization server's making therefore fails to parse, and so does
   a v6 payload under the new type. The residual rule is operational: an
   issuer's signing key is never shared with an authorization server that
   mints `at+jwt`.

## Options considered

### Option A: a v7 typed `at+jwt` beside v6 (chosen)

| Dimension | Assessment |
|---|---|
| Interoperability | RFC 9068 validators read a v7 capability |
| Safety | Strict schema plus an unshared key, where v6 also has its own type |
| Cost | One more schema; a rollout, when an issuer wants it |

### Option B: keep v6 only

**Pros:** the type still separates capabilities from every other JWT.
**Cons:** no gateway or identity provider that validates RFC 9068 reads a
capability, and adding the format after 1.0 means verifiers rolled out
first, at the pace of every consumer.

### Option C: `application/limes-cap+jwt` with `client_id`

**Cons:** satisfies neither side. RFC 9068 validators still refuse the type,
and the type still names no version.

## Trade-off analysis

RFC 8725 §3.11 recommends explicit typing so that one kind of JWT cannot be
substituted for another under the same key. v7 gives up the capability's
own type for the access token's. It keeps the protection the type gave by
two other means: a schema strict enough that only a capability parses, and
a key no other issuer holds. The first is enforced in code and pinned by
tests. The second is the operator's, written in the guide's section on
keys. Without the
second, the first still refuses every access token that lacks the `limes_`
claims, which is every access token not minted as a capability.

## Consequences

- `docs/standards.md` describes both types, and the RFC 9068 paragraph says
  a v7 capability is an access token of the profile.
- A future format under `at+jwt` is read by dispatching on `limes_v`, as on
  `typ` before.
- Format changes planned for 1.0, such as the resource name prefixes, land
  in v7 alone, which has the members for them.
