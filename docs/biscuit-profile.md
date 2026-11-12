# The limes Biscuit profile

How a limes capability is carried in a Biscuit, and what a holder may add
to it with any Biscuit implementation — biscuit-go, biscuit-rust and its
CLI, biscuit-python, biscuit-wasm — so that an agent in another language
can narrow a limes token without limes.

## What the Biscuit is, and is not

The Biscuit form is a **container for offline narrowing, with a vocabulary
of limes's own**. It is not a policy language:

- The authority block holds one fact: the capability's ordinary token, a
  JWT the issuer signed. limes reads the capability from that token, not
  from Datalog.
- An attenuation block holds facts from the vocabulary below and nothing
  else. limes reads them as data and folds each block into a narrower
  capability by the rule a server-side delegation is checked by
  (`Narrows`).
- limes uses a Biscuit authorizer only to verify the chain of block
  signatures. It runs no Datalog: a rule or a check in a block is refused,
  never evaluated.

Any Biscuit implementation can therefore parse a limes Biscuit, verify its
signatures against the root key the sealed token names, and append a
block. Only limes can say what the token allows: the sealed token,
revocation, the copy counters and the tenant's records are limes's. A
verifier that is not limes must not accept a limes Biscuit as
authorization.

## The container

- Biscuit v3 serialization in base64url. limes writes it without padding,
  as biscuit-go does, and reads it with or without, since biscuit-rust and
  the libraries built on it pad; padding, when present, must be exactly
  what the length needs. The token has no `.`, which is how limes tells it
  from a JWT.
- Signatures are Ed25519. The root key is a one-off key whose public half
  the sealed token carries (`limes_bsk`). Its private half signed the
  authority block and was discarded, so nobody can wrap the same JWT in a
  fresh Biscuit. A block is appended with the token's proof key, as the
  Biscuit specification describes; no other key is needed.
- At most `MaxBiscuitBlocks` (32) attenuation blocks.
- A sealed token (its proof a final signature) can no longer be
  attenuated, per the Biscuit specification; limes verifies it as usual.
- Each block's revocation id is its signature, as the Biscuit
  specification defines. `RevokeBiscuit` revokes a copy by the id of its
  last block, which revokes every copy attenuated from it too.

## The authority block

Exactly one fact, and no rule or check:

| Fact | Term | Meaning |
|---|---|---|
| `limes_capability` | string | The sealed capability token (a JWT, `typ: at+jwt`). |

A holder never writes this block.

## Attenuation blocks

Each fact takes exactly one term, of the type given. A block may hold any
number of facts, in any order, and the same fact more than once.

| Fact | Term | Effect on the copy |
|---|---|---|
| `limes_op` | string | An operation the copy keeps. Repeated facts add up; the set replaces the capability's and must be within it. |
| `limes_resource_prefix` | non-empty string | A resource prefix the copy keeps. |
| `limes_resource_uri` | non-empty string | A resource URI the copy keeps. A block naming either resource fact replaces every resource caveat of the capability, its name prefixes (`ResourceNamePrefixes`) included; each entry must be within them. |
| `limes_plane` | string | An audience the copy keeps. The set replaces the capability's and must be within it. |
| `limes_expires` | date | An expiry. The earliest in the block wins; one later than the capability's changes nothing. |
| `limes_bind` | string | A JWK thumbprint (RFC 7638, base64url SHA-256) the copy is bound to, so it needs a DPoP proof by that key. Only an unbound capability can be bound offline; a bound one may restate its own key and no other. |
| `limes_max_requests` | integer ≥ 0 | A request limit of the copy's own, counted apart from its siblings. The last in the block wins. |
| `limes_max_budget_nanos` | integer ≥ 0 | A spending limit of the copy's own, in nanos (10⁻⁹) of the capability's unit, which the block does not name. The last in the block wins. |

A copy limit must be within the capability's own limit and every enclosing
copy's. A verifier refuses a token carrying a copy limit
(`ErrCopyCountersNotMetered`) unless its `BiscuitConfig.MeterCopies` says
the meter behind it counts copies.

A copy limit makes the copy one the meter counts. The capability's quota
(`Caveats.Copies`, signed in its token, which no block changes) bounds the
copies counted for it and for every capability delegated from it, as its
request and budget ceilings bound theirs; a delegated child's quota is
within its parent's. A copy once counted stays counted under the ancestors
after its own capability's usage is dropped. A meter also counts no more
copies per capability than its store's bound. Past either bound, a call is
refused with `meter.ErrCopyLimitExceeded`.

## What limes refuses

A token is refused as `ErrBiscuitAttenuation`, a kind of `ErrInvalidToken`,
when a block:

- holds a rule or a check;
- holds a fact outside the vocabulary, or a term its fact does not take
  (a string where a date is expected, a negative limit, an empty
  resource, a malformed thumbprint);
- would widen what the token allows: an operation, resource, audience or
  limit beyond the capability's or an enclosing copy's, or a new key for a
  bound token.

A token is refused as `ErrInvalidToken` when its container or signatures
do not verify, a fact has other than one term or a term that is neither
string, date nor integer, its authority block is not exactly one
`limes_capability` fact, or it has more than `MaxBiscuitBlocks` blocks.

A refusal never depends on what a block leaves out: a block that names
nothing narrows nothing.

## Writing a block in another language

Append a block holding facts of the vocabulary, as the library builds
blocks — in Datalog source, for instance:

```
limes_op("get");
limes_resource_prefix("corpus/public/");
limes_expires(2026-10-19T12:00:00Z);
limes_max_requests(50);
```

Write facts only. Rules and checks (`check if …`, `allow if …`) are
Biscuit language a limes verifier does not run, and it refuses a token
holding one. `TestAForeignBlockIsReadAsLimesWrote` and
`TestAForeignCheckIsRefused` (`biscuit/biscuit_interop_test.go`) hold
limes to this with blocks biscuit-cli appended to the shared seed
(`biscuit/testdata/biscuit/`).

## Versioning

The vocabulary is part of the token's wire format: on the v1 line a fact
is never renamed, retyped or given a looser meaning. Since a verifier
refuses a fact it does not know, a new fact is a new token format
(`limes_v`), read by verifiers before issuers mint it
([api-review-v1.md](api-review-v1.md), P5;
[ADR-0019](adr/0019-token-formats-in-1x.md)).
