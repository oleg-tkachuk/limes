# The limes Biscuit profile

How a limes capability is carried in a Biscuit, and what a holder may add to
it with any Biscuit implementation — biscuit-go, biscuit-rust and its CLI,
biscuit-python, biscuit-wasm — so that an agent written in another language
can narrow a limes token without limes.

## What the Biscuit is, and is not, here

The Biscuit form is a **container for offline narrowing with a vocabulary of
limes's own**. It is not a policy language:

- The authority block holds one fact: the capability's ordinary token, a JWT
  the issuer signed. limes reads the capability from that token, not from
  Datalog.
- An attenuation block holds facts from the vocabulary below and nothing
  else. limes reads them as data and folds each block into a narrower
  capability with the same rule a server-side delegation is checked by
  (`Narrows`).
- limes uses a Biscuit authorizer only to verify the chain of block
  signatures. It runs no Datalog: a rule or a check in a block is refused,
  never evaluated.

So any Biscuit implementation can parse a limes Biscuit, verify its
signatures against the root key the sealed token names, and append a block.
Only limes can say what the token allows: the sealed token, revocation, the
copy counters and the tenant's records are limes's. A verifier that is not
limes's must not accept a limes Biscuit as authorization.

## The container

- Biscuit v3 serialization in base64url. limes writes it without padding,
  as biscuit-go does, and reads it with or without, since biscuit-rust and
  the libraries built on it pad; padding must be exactly what the length
  needs. The token has no `.`, which is how limes tells it from a JWT.
- Signatures are Ed25519. The root key is a one-off key whose public half the
  sealed token carries (`limes_bsk`); its private half signed the authority
  block and was discarded, so nobody can wrap the same JWT in a fresh
  Biscuit. A block is appended with the token's proof key, as the Biscuit
  specification describes; no other key is needed.
- At most `MaxBiscuitBlocks` (32) attenuation blocks.
- A token whose proof is sealed (`FinalSignature`) can no longer be
  attenuated, by the Biscuit specification; limes verifies it as usual.
- Each block's revocation id is its signature, as the Biscuit specification
  defines it. `RevokeBiscuit` revokes a copy by the id of its last block.

## The authority block

Exactly one fact, and no rule or check:

| Fact | Term | Meaning |
|---|---|---|
| `limes_capability` | string | The sealed capability token (a JWT, `typ: limes-cap-v5+jwt`). |

A holder never writes this block.

## Attenuation blocks

Each fact takes exactly one term, of the type given. A block may hold any
number of facts, in any order, and the same fact more than once.

| Fact | Term | Effect on the copy |
|---|---|---|
| `limes_op` | string | The operations the copy keeps. Repeated facts add up; the set replaces the capability's and must be within it. |
| `limes_resource_prefix` | non-empty string | A resource prefix the copy keeps. |
| `limes_resource_uri` | non-empty string | A resource URI the copy keeps. A block naming either resource fact replaces both lists of the capability; each entry must be within them. |
| `limes_plane` | string | An audience the copy keeps. The set replaces the capability's and must be within it. |
| `limes_expires` | date | An expiry. The earliest in the block wins; a later one than the capability's changes nothing. |
| `limes_bind` | string | A JWK thumbprint (RFC 7638, base64url SHA-256) the copy is bound to, so it needs a DPoP proof by that key. Only an unbound capability can be bound offline; a bound one may restate its own key and no other. |
| `limes_max_requests` | integer ≥ 0 | A request limit of this copy's own, counted apart from its siblings. The last in the block wins. |
| `limes_max_budget_nanos` | integer ≥ 0 | A spending limit of this copy's own, in nanos (10⁻⁹) of the capability's unit, which the block does not name. The last in the block wins. |

A copy limit must be within the capability's own limit and every enclosing
copy's. A verifier refuses a token carrying a copy limit unless its
`BiscuitConfig.MeterCopies` says the meter behind it counts copies
(`ErrCopyCountersNotMetered`), and a store counts at most a quota of copies
per capability (`ErrCopyLimitExceeded`).

## What limes refuses

A token is refused as `ErrBiscuitAttenuation` — a kind of `ErrInvalidToken` —
when a block:

- holds a rule or a check;
- holds a fact outside the vocabulary above, a fact with other than one
  term, or a term of another type (a string where a date is expected, a
  negative limit, an empty resource, a malformed thumbprint);
- would widen what the token allows: an operation, resource, audience, or
  limit beyond the capability's or an enclosing copy's, or a new key for a
  bound token.

A token whose container or signatures do not verify, whose authority block is
not exactly one `limes_capability` fact, or that has more than
`MaxBiscuitBlocks` blocks is refused as `ErrInvalidToken`. A refusal never
depends on what a block leaves out: a block that names nothing narrows
nothing.

## Writing a block in another language

Append a block with the facts above, as the library builds blocks — in
Datalog source, for instance:

```
limes_op("get");
limes_resource_prefix("corpus/public/");
limes_expires(2026-10-19T12:00:00Z);
limes_max_requests(50);
```

Rules and checks (`check if …`, `allow if …`) are the Biscuit language a
limes verifier does not run, and it refuses a token holding one: write facts
only. `TestAForeignBlockIsReadAsLimesWrote` and `TestAForeignCheckIsRefused`
(biscuit_interop_test.go) hold limes to this with blocks biscuit-cli appended
to the shared seed (testdata/biscuit/).

## Versioning

The vocabulary is part of the token's wire format: on the v1 line a fact is
never renamed, retyped or given a looser meaning. A new fact is a new token
type, accepted by verifiers before issuers mint it (`AcceptTokenTypes`,
docs/api-review-v1.md P5), since a verifier refuses a fact it does not know.
