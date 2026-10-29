# ADR-0012: A stable code and a problem details body for every error

**Status:** Accepted
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
