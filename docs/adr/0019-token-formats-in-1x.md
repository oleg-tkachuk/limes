# ADR-0019: A new token format within 1.x

**Status:** Accepted
**Date:** 2026-11-05
**Deciders:** Oleg Tkachuk
**Amends:** [ADR-0006](0006-one-token-format.md)

## Context

ADR-0006 left one format, `limes_v: 7`, and said a later one "would be read
by dispatching on `limes_v`". Nothing in 1.x could do that:

- A verifier reads the claims strictly (`strictClaims` in `signer.go`): a
  member it does not know is refused, so any new caveat or claim needs a
  new `limes_v`.
- `decodeClaims` refuses every `limes_v` but 7.
- [compatibility.md](../compatibility.md) counted accepting what a
  function refused as a break, so a 1.x verifier that learned a new format
  would break the v1 promise; and so is changing a default, so a signer
  could not start writing one either.

The options ADR-0006 removed, `VerifierConfig.AcceptTokenTypes` and
`SignerConfig.TokenType`, were the old answer to this, and
`docs/api-review-v1.md` still promised them.

Strict reading stays: a caveat a verifier does not know is one it does not
enforce, so an unknown claim is never ignored.

## Decision

1. **Reading a new format is not a break.** A minor release may teach the
   verifier a new `limes_v`, each read under its own strict schema.
   compatibility.md says so, as the one exception to "accepting what it
   refused".
2. **Every format read stays read.** A format a v1 release reads is read
   by every later v1 release: `limes_v: 7` for the whole of v1. Refusing
   one is a break.
3. **A signer writes a new format only when told to.** The release that
   adds a format adds a `SignerConfig` field naming the format to write,
   whose zero value keeps `limes_v: 7`. The default does not change on v1,
   so what an issuer signs by default stays readable by every v1 verifier.
4. **Verifiers first, then issuers.** An operator upgrades every verifier
   that may see a token to the release that reads the format, then sets
   the issuers' field. A verifier older than that refuses the new format,
   as it refuses any token it cannot read.
5. **Another algorithm is another format.** Ed25519 is the only key a
   `KeyResolver` returns for all of 1.x (decision 2 of
   `docs/api-review-v1.md`); a format with another algorithm comes in the
   same way, with its key type.

No exported name is added now: the field comes with the first format that
needs it, which is an addition.

## Options considered

### Option A: a carve-out for reading a new format (chosen)

| Dimension | Assessment |
|---|---|
| Complexity | None now: a rule in compatibility.md and this record |
| Operator | Upgrades verifiers, then opts issuers in, once per format |
| Safety | Strict schema per format; the kid namespace (ADR-0006) unchanged |

**Pros:** a verifier reads the new format with no configuration; nothing
is frozen into the API before it is needed. **Cons:** a consumer whose own
tests pin that some `limes_v` is refused sees them fail after a minor.

### Option B: opt-in on both sides, added before 1.0

`VerifierConfig.AcceptFormats`, defaulting to 7 alone, and
`SignerConfig.Format`. **Pros:** no exception to the contract. **Cons:** the
verifier's default could never widen on v1, so every verifier in every
deployment would name each new format by hand for the rest of the major;
two exported names frozen before any format needs them.

### Option C: one format for all of 1.x

Option (a) of P5 in `docs/api-review-v1.md`: a new caveat waits for v2.
**Pros:** the simplest promise. **Cons:** with strict reading, no claim at
all can be added to a capability for the whole major.

## Trade-off analysis

Option B keeps the contract free of exceptions by making every operator
pay for every format, forever. Option A puts the cost where the risk is:
the issuer, who decides to write a format, and only once all its verifiers
read it. What a reader of the contract loses is the guarantee that a token
a verifier refuses today is refused tomorrow, which no consumer needs to
stay safe: the new format is signed under limes's own kid, read strictly,
and written only by an issuer told to.

## Consequences

- compatibility.md names the exception, and its "stored or signed form"
  rule speaks of what a release writes by default.
- `docs/api-review-v1.md` decisions 1 and 2, P5 and its plan's step 7
  describe this rule rather than `AcceptTokenTypes`.
- The first new format adds its schema beside v7's in `decodeClaims`, the
  `SignerConfig` field, tests that a v7 token is still read, and a row in
  `docs/standards.md`.
