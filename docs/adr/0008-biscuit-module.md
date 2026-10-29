# ADR-0008: The Biscuit form in a module of its own

**Status:** Accepted
**Date:** 2026-10-29
**Deciders:** Oleg Tkachuk
**Relates to:** [ADR-0007](0007-pgstore-components.md)

## Context

The root module requires three modules: biscuit-go, protobuf, which
biscuit-go serializes with, and x/text. Only the Biscuit form needs the first
two (`biscuit.go`, `biscuit_revocation.go`).

A consumer that verifies JWT capabilities and never sees a Biscuit links them
all the same. `VerifierConfig.Biscuit` is an optional field, and
`Verifier.Verify` dispatches to the Biscuit path when it is set. The whole
path is therefore reachable, and the linker cannot leave it out.

Measured on 2026-10-29: a JWT-only verifier (`NewVerifier`, `Verify`, built
with `-trimpath -ldflags=-s`) against the same root with the Biscuit form
stubbed out:

| | With Biscuit | Without |
|---|---|---|
| Binary | 6.78 MB | 5.03 MB (−26%) |
| Modules in its graph | 14 | 6 |
| Lines of its go.sum | 16 | 2 |
| Package `init`, average of 20 runs | 0.53 ms | 0.39 ms |
| Allocated by `init` | 118 KB | 84 KB |

ADR-0007 found that splitting packages saves a binary nothing, because the
linker already leaves out what it cannot reach. This case is the exception:
an optional field keeps the Biscuit path reachable, and the dependencies
come with it.

limes 1.0 freezes the root's API, so the seam has to be cut before then.

## Decision

1. **A nested module, `github.com/oleg-tkachuk/limes/biscuit`,** holds
   everything that reads or writes a Biscuit, and is the only module that
   requires biscuit-go:
   - building a Biscuit around a sealed JWT, and reading one back;
   - `biscuit.Attenuate`;
   - decoding a token and verifying its signature chain;
   - the fact vocabulary;
   - the cross-language interop tests.

   It is released on its own tag line, like the other nested modules, and
   stays 0.x past limes 1.0.
2. **The core keeps what does not depend on the encoding:**
   - `Attenuation` and the narrowing it does: folding a block into a
     `Capability` through `Narrows`, and the copy ceilings;
   - `VerifiedCapability.Copies`, `CopyCeiling` and `BiscuitCopy`;
   - the revocation contracts that pgstore and memstore implement:
     `BiscuitRevocationLookup`, `BiscuitRevocationStore`,
     `RevokeBiscuitRequest` and `BiscuitRevocation`;
   - `ErrBiscuitAttenuation`, `ErrCopyCountersNotMetered` and
     `MaxBiscuitBlocks`;
   - the `limes_bsk` claim of the sealed JWT.

   So the rule that a copy only narrows stays in the core, under the core's
   tests, and the stores need no new import.
3. **The seam is a `TokenForm` the core defines, given to both sides.**
   - `Seal(ctx, sign func(ctx, root ed25519.PublicKey) (string, error)) (string, error)`:
     the form chooses the root key, the core signs the JWT naming it, and
     the form builds the token around that JWT.
   - `Open(token) (sealed string, unseal func(root ed25519.PublicKey) (Unsealed, error), err error)`.
     `Unsealed` holds each block as an `Attenuation`, and the token's
     revocation ids, the sealing block's first.
   - `IssuerConfig.Biscuit` and `BiscuitConfig.Form` take it. `biscuit.Form()`
     returns the implementation.
   - The core verifies the sealed JWT, hands the form the root key that JWT
     names, and folds the blocks in itself.
   - What `unseal` returns is the token holder's input. The core refuses:
     - more than `MaxBiscuitBlocks` blocks;
     - revocation ids that are not one per block plus the sealing one, or
       one that is empty;
     - any block that `Attenuate` would refuse.

     A form's error becomes `ErrBiscuitAttenuation` or `ErrInvalidToken`,
     never the form's own sentinels.
   - A token with no `.` and no form configured is refused, as it is today.
4. **`Issuer.Biscuit` stays in the core.** It keeps the checks of what may
   be sealed (one this issuer issued, not read from a Biscuit, live) and the
   token size limit, so a form cannot widen a capability. It seals through
   the form `IssuerConfig.Biscuit` names, and refuses a root key that is not
   an Ed25519 public key.
5. **Enforced by tools.** `isolation_test.go` adds biscuit-go and protobuf
   to what the root module may not resolve. The biscuit module's own suite
   runs the vocabulary, malformed-token and interop tests, against the core
   through the seam.

## Options considered

### Option A: a nested module (chosen)

| Dimension | Assessment |
|---|---|
| JWT-only consumer | 26% smaller binary; 6 modules in its graph, not 14 |
| Biscuit consumer | One more import (`limes/biscuit`) and `biscuit.Form()` in its config |
| Cost | A new module and tag line, one core interface, the Biscuit tests moved |

### Option B: a package in the root module

This removes biscuit-go from a JWT-only binary just as well.

**Cons:** biscuit-go and protobuf would stay in the root's go.mod, so every
consumer would still resolve and audit them.

### Option C: a build tag (`limes_nobiscuit`)

**Cons:** the default stays heavy, and a library's consumer has to know the
tag exists. A tag also splits the tested configurations in two.

### Option D: leave it

**Cons:** every JWT-only consumer keeps paying for a form it never reads,
and after 1.0 the field cannot move.

## Consequences

- A consumer that accepts Biscuits sets `BiscuitConfig.Form` and
  `IssuerConfig.Biscuit` to `biscuit.Form()`, and calls `biscuit.Attenuate`
  where it called `limes.Attenuate`. `docs/migration.md` lists the change.
- It ships in two steps.
  - First the seam, in the root, with the Biscuit form still in the tree as
    the default when no form is given, and a limes release carrying it.
  - Then the module: it requires that release, and the root drops the form,
    `Attenuate` and the two dependencies.
- Tokens do not change: the same Biscuit, the same sealed JWT, the same
  facts. Tokens issued before the move verify after it.
- The new module needs a seed tag before the change that adds it merges,
  and a row in `.github/release/modules.cjs`.
- docs/biscuit-profile.md stays among the docs, and points at the module's
  interop tests and the fixtures it shares with SDKs, which move to
  `biscuit/testdata`: an SDK that copies them reads them from there.
- The core's tests of what a Biscuit grants run on a form of their own with
  no signatures, so the root resolves no biscuit-go even for its tests; the
  module tests the encoding, the chain and the interop fixtures.
