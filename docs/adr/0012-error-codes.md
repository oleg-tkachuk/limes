# ADR-0012: A stable code and a problem details body for every error

**Status:** Accepted, amended on 2026-11-05 (below)
**Date:** 2026-10-29
**Deciders:** Oleg Tkachuk
**Relates to:** [ADR-0003](0003-oauth-token-exchange.md)

## Context

A capability presented to a protected resource and refused has a standard
answer: `Verifier.ErrorResponse` gives an RFC 6750 or RFC 9449 challenge and
an RFC 9457 problem details body whose `reason` is a stable `Reason`.

Everything else has no code:

- a store's errors: not found, conflict, an unknown or erased tenant, a call
  scoped to another tenant;
- the meter's errors: a charge not found, a settle conflicting, a refund
  past its charge;
- a request the caller got wrong: invalid caveats, a cursor of another
  listing.

These reach a consumer's own API — an admin endpoint that revokes, a
billing call that settles — and `ErrorResponse` answers each one as the
server's failure. A consumer that wants to say more reads the error's text.
One consumer did, in four places: it strips `limes: `, then maps reasons to
its API's codes by the text's prefix. The text was never a contract, and a
reworded message would silently change that consumer's answers.

The token endpoint (`exchange.Handler`) answers in RFC 6749 §5.2's error
codes. An `Exchanger` called in process has no such answer to give.

## Decision

1. **`limes.Code`** is a stable snake_case name for every exported sentinel
   of `limes`, `dpop` and `jwks`. A refusal's code is its `Reason`, where
   that names it. A sentinel that `Reason` folds into a wider one keeps a
   code of its own:
   - `tenant_erased` is reported as `revoked`;
   - `delegation_too_deep` is reported as `revoked`.

   Codes are never renamed; new ones may be added. `Codes()` lists every
   code, so a consumer can prove its own mapping is whole.
2. **`limes.ProblemOf(err)`** is the RFC 9457 problem details of any error
   limes returns:
   - `type`, a URI ending in the code;
   - a fixed `title`;
   - `status`, the HTTP status, as the error's numeric code;
   - `code`;
   - `reason` for a refusal.

   It carries no `detail`, because an error's text can name kids, issuers
   and stores. For a refusal, it is the body `ErrorResponse` writes,
   `code` added. For an error it does not know, it reports false, and the
   consumer answers 500.
3. **`meter.ProblemOf`** adds the meter's own codes, such as
   `charge_not_found` and `settle_conflict`. For anything else it falls back
   to `limes.ProblemOf`, since `meter` imports `limes` and not the other way
   round.
4. **`exchange.ErrorOf(err)`** is the RFC 6749 §5.2 error an exchange
   refused with: status, `error` and `error_description`. It is read from
   the table the Handler already answers from, so an in-process `Exchanger`
   answers as the token endpoint does.
5. **A test holds every exported sentinel to a code.** It reads each
   package's sentinels from its source, so a new one without a code fails
   the build.
6. **The text of an error is not a contract,** and the guide says so. Branch
   with `errors.Is`, and put `Code` or `ProblemOf` on the wire.

No transport's own codes, gRPC's or another RPC framework's, are built in.
HTTP's status and RFC 9457's body are the common ground, and a consumer maps
them onto its framework in one place.

## Consequences

- `Problem` gains a `code` member; the bodies `ErrorResponse` writes gain
  it too. Clients that ignore unknown members, as RFC 9457 §3.2 asks, see
  no change.
- `meter.ProblemOf` and `exchange.ErrorOf` are each module's to keep in
  step with its sentinels, held there by the same kind of test.

## Amendment, 2026-11-05

The registry grew past this decision. [docs/errors.md](../errors.md) and
[docs/compatibility.md](../compatibility.md) are the current authority;
in short:

- **One registry, in the root module.** The meter's and the exchange's
  codes are registered in `code.go` beside limes's own; `meter/code.go`
  re-exports the meter's as aliases, and a sentinel of a package limes
  cannot import names its code through `internal/errcode.Coded`.
  `meter.ProblemOf` does not exist: `limes.ProblemOf` answers every code.
  `exchange.ErrorOf` remains, for RFC 6749 §5.2.
- **More reads of the registry.** `Codes()`, `CodeOf`, `ProblemOf`,
  `ProblemFor` (any error, an unknown one as `internal`), `ProblemOfCode`
  and `ProblemTypeURI`.
- **Categories.** `limes.Category` (`category.go`: `unauthenticated`,
  `denied`, `exhausted`, `invalid`, `not_found`, `conflict`,
  `already_exists`, `precondition`, `unavailable`, `internal`) is what a
  caller does about a code; every code has one. `Categories()`,
  `CategoryOf`, `CategoryOfCode`, and `Problem.Category` on the wire.
- **A refusal's problem type ends in its reason, not its code.** A folded
  refusal such as `tenant_erased` has the type `…/problems/revoked` and
  carries its own code in `code`; an error that is no refusal has a type
  ending in its code.
- **The contract is wider than "codes are never renamed".** Each code's
  HTTP status, category and reason are contract, and which codes `Codes()`
  lists; removing a code or answering one otherwise is a break.
- **`limestest`** pins a consumer's mapping to that contract:
  `CheckMapping`, `CheckCodes`, `CheckCategories`, with the options
  `WithoutExchange`, `WithoutMeter` and `AcceptNewByCategory`.
- **A problem type resolves.** Since v0.166.0 a type URI is
  `https://github.com/oleg-tkachuk/limes/blob/main/docs/errors.md#<type>`,
  an anchor of the errors document's "Problem types" index, which
  `problem_types_test.go` holds to every type limes answers with.
