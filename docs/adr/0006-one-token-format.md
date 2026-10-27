# ADR-0006: One token format, under a limes kid

**Status:** Accepted
**Date:** 2026-10-27
**Deciders:** Oleg Tkachuk
**Amends:** [ADR-0004](0004-token-format-v7.md)

## Context

ADR-0004 added v7, an RFC 9068 access token typed `at+jwt`, beside v6,
typed `limes-cap-v6+jwt`, with verifiers reading both and signers writing v6
until an operator switched. Both formats were released in candidates only,
and no token of either has to outlive 1.0.

Two formats cost two schemas in the signer and the parser, a type option on
each side, and a rollout rule an operator must follow. The one thing v6 kept
that v7 does not is RFC 8725's explicit typing: a type no other JWT shares,
so a JWT of another kind signed by the same key is refused before any claim
is read.

## Decision

1. **One format.** Every capability is an `at+jwt` with `limes_v: 7`. v6,
   `SignerConfig.TokenType` and `VerifierConfig.AcceptTokenTypes` are
   removed; `limes.TokenType` is the constant `"at+jwt"`. A later format
   would be read by dispatching on `limes_v`.
2. **The profile's required claims are required.** RFC 9068 makes
   `client_id` and `sub` required, so `IssueRequest.ClientID` and
   `Subject.Subject` are, and a token without either is refused.
3. **The kid takes over from the type.** A signing key's kid is in limes's
   namespace: `jwks.KeyIDPrefix` (`limes:`) and a name, as `jwks.KeyID`
   writes it. `NewSigner` refuses any other kid, and a verifier refuses a
   token under any other before it looks a key up.

## Options considered

### Option A: one format and a kid namespace (chosen)

| Dimension | Assessment |
|---|---|
| Standards | Every capability is an RFC 9068 access token |
| Safety | Strict schema, a dedicated key, and a kid limes alone signs under |
| Cost | Every issuer names a client and a subject; keys move to the namespace |

### Option B: keep v6 beside v7

**Pros:** an operator who does not need RFC 9068 keeps explicit typing.
**Cons:** two formats frozen by 1.0 for the rest of the major version, and a
choice most operators should not have to make.

### Option C: one format, no kid rule

**Cons:** nothing in the token says it was signed by a key of limes's; the
key rule lives in documentation alone.

## Trade-off analysis

The kid does not do all that the type did. It refuses a token signed under
another issuer's kid, and it marks every key a limes issuer publishes; it
cannot stop an operator copying a limes key into an authorization server,
which then signs under the limes kid. That remains the rule of ADR-0004, now
the only way to defeat both the kid and the strict schema, and a key copied
so is a key compromised.

## Consequences

- Tokens of earlier candidates, v6 and v7 alike, are refused: their kids
  are outside the namespace, or their type is retired.
- `docs/standards.md` describes one format, and the key rule with the kid.
- `docs/migration.md` lists the removed options and the new requirements.
