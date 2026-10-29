# Errors

Every error limes returns has a stable **code**: a snake_case name a client
branches on, never renamed, to which new ones may be added
([ADR-0012](adr/0012-error-codes.md)). The text of an error is for the
server's log. It names kids, issuers and stores, may be reworded in any
release, and is never a contract: do not parse it, and do not strip its
`limes: ` prefix to show it. In Go, branch on the sentinel with
`errors.Is`. On the wire, send the code.

## Formats

limes answers in the formats the standards for these systems define. It
builds in no RPC framework's own codes: HTTP's status and RFC 9457's body
are the common ground, and a consumer maps them onto its transport in one
place.

**A protected resource refusing a capability** answers with
`Verifier.ErrorResponse` and writes it with `WriteErrorResponse`:

- the HTTP status;
- a `WWW-Authenticate` challenge, with the error code RFC 6750 §3.1 or
  RFC 9449 §7.1 defines (the *Challenge `error`* column below);
- a `DPoP-Nonce` header when the refusal asks for a nonce;
- an RFC 9457 `application/problem+json` body.

**Any other API of the consumer's** — an admin endpoint that revokes, a
billing call that settles — answers an error limes returned with
`limes.ProblemOf(err)`, or `meter.ProblemOf(err)` for the meter's calls,
which knows the meter's codes as well. Each gives the same RFC 9457 body
and reports false for an error that is not limes's, which the consumer
answers as its own failure, 500:

```go
p, ok := meter.ProblemOf(err)
if !ok {
	p = limes.Problem{Type: "about:blank", Title: "Internal error", Status: http.StatusInternalServerError}
}
// HTTP: write p as application/problem+json with p.Status.
// Another transport: carry p.Code, and p.Status as its numeric class.
```

**The token endpoint** (`exchange.Handler`) answers in RFC 6749 §5.2's
format: `error`, `error_description`, and a status.

The body, for a capability that was revoked:

```json
{
  "type": "https://github.com/oleg-tkachuk/limes/problems/revoked",
  "title": "The access token is invalid",
  "status": 401,
  "reason": "revoked",
  "code": "revoked"
}
```

| Member | |
|---|---|
| `type` | A URI ending in the refusal's `reason`, or in the code for an error that is no refusal (`limes.ProblemTypeURI`). |
| `title` | Fixed per kind, safe to show. |
| `status` | The HTTP status: the error's numeric code. |
| `code` | The stable code (`limes.Code`): what a client branches on. |
| `reason` | For a refusal of a presented capability, its `limes.Reason`, which can be wider than the code: `tenant_erased` and `delegation_too_deep` are reported as `revoked`. |

There is no `detail`: an error's own text is not shown to the client.

## Retrying

- **A 5xx** is the server's to fix, or its upstream's.
- **A refusal whose `reason` is permanent** (`limes.Reason.Permanent()`,
  marked *permanent* below) stands for as long as the capability does:
  stop, and get another capability.
- **Any other refusal** can lift. A budget frees as holds lapse, a nonce is
  signed afresh, an idempotency key can be added.
- **A 409** is a request that disagrees with what is on record: read it
  again, and decide.

`limes.Codes()` and `meter.Codes()` list every code, so a consumer's test
can prove its own mapping covers them all.

## limes's codes

| Code | HTTP | Sentinel | `reason` | Challenge `error` | Title |
|---|---|---|---|---|---|
| `malformed_authorization` | 400 | `limes.ErrMalformedAuthorization` | `malformed_authorization` (permanent) | `invalid_request` | The Authorization header is malformed |
| `no_credentials` | 401 | `limes.ErrNoCredentials` | `no_credentials` |  | Credentials are required |
| `keys_unavailable` | 503 | `jwks.ErrUnavailable` | `keys_unavailable` |  | The issuer's keys cannot be fetched |
| `nonce_required` | 401 | `dpop.ErrNonceRequired` | `nonce_required` | `use_dpop_nonce` | A DPoP proof with the server's nonce is required |
| `proof_required` | 401 | `dpop.ErrRequired` | `proof_required` | `invalid_dpop_proof` | The DPoP proof is missing or invalid |
| `proof_invalid` | 401 | `dpop.ErrInvalid` | `proof_invalid` | `invalid_dpop_proof` | The DPoP proof is missing or invalid |
| `proof_replayed` | 401 | `dpop.ErrReplayed` | `proof_replayed` | `invalid_dpop_proof` | The DPoP proof is missing or invalid |
| `auth_scheme_mismatch` | 401 | `limes.ErrAuthSchemeMismatch` | `auth_scheme_mismatch` | `invalid_token` | The access token is invalid |
| `proof_of_possession` | 401 | `limes.ErrProofOfPossession` |  | `invalid_dpop_proof` | The DPoP proof is missing or invalid |
| `biscuit_attenuation` | 401 | `limes.ErrBiscuitAttenuation` | `biscuit_attenuation` (permanent) | `invalid_token` | The access token is invalid |
| `invalid_token` | 401 | `limes.ErrInvalidToken` | `invalid_token` (permanent) | `invalid_token` | The access token is invalid |
| `expired` | 401 | `limes.ErrExpired` | `expired` (permanent) | `invalid_token` | The access token is invalid |
| `not_yet_valid` | 401 | `limes.ErrNotYetValid` | `not_yet_valid` | `invalid_token` | The access token is invalid |
| `tenant_erased` | 401 | `limes.ErrTenantErased` | `revoked` (permanent) | `invalid_token` | The access token is invalid |
| `delegation_too_deep` | 401 | `limes.ErrDelegationTooDeep` | `revoked` (permanent) | `invalid_token` | The access token is invalid |
| `revoked` | 401 | `limes.ErrRevoked` | `revoked` (permanent) | `invalid_token` | The access token is invalid |
| `audience_mismatch` | 401 | `limes.ErrAudienceMismatch` | `audience_mismatch` (permanent) | `invalid_token` | The access token is invalid |
| `unauthenticated` | 401 | `limes.ErrUnauthenticated` |  | `invalid_token` | The access token is invalid |
| `op_not_allowed` | 403 | `limes.ErrOpNotAllowed` | `op_not_allowed` (permanent) | `insufficient_scope` | The token does not allow this request |
| `resource_not_allowed` | 403 | `limes.ErrResourceNotAllowed` | `resource_not_allowed` (permanent) | `insufficient_scope` | The token does not allow this request |
| `source_ip_not_allowed` | 403 | `limes.ErrSourceIPNotAllowed` | `source_ip_not_allowed` (permanent) | `insufficient_scope` | The token does not allow this request |
| `idempotency_key_required` | 403 | `limes.ErrIdempotencyKeyRequired` | `idempotency_key_required` | `insufficient_scope` | The token does not allow this request |
| `tainted_read_not_allowed` | 403 | `limes.ErrTaintedReadNotAllowed` | `tainted_read_not_allowed` | `insufficient_scope` | The token does not allow this request |
| `caveat_violation` | 403 | `limes.ErrCaveatViolation` |  | `insufficient_scope` | The token does not allow this request |
| `request_limit_exceeded` | 403 | `limes.ErrRequestLimitExceeded` | `request_limit_exceeded` (permanent) |  | The token's request or budget limit is exhausted |
| `budget_exceeded` | 403 | `limes.ErrBudgetExceeded` | `budget_exceeded` |  | The token's request or budget limit is exhausted |
| `tenant_budget_exceeded` | 403 | `limes.ErrTenantBudgetExceeded` | `tenant_budget_exceeded` |  | The token's request or budget limit is exhausted |
| `unit_code_mismatch` | 409 | `limes.ErrUnitCodeMismatch` |  |  | The request conflicts with what is on record |
| `already_exists` | 409 | `limes.ErrAlreadyExists` |  |  | The request conflicts with what is on record |
| `conflict` | 409 | `limes.ErrConflict` |  |  | The request conflicts with what is on record |
| `invalid_caveats` | 400 | `limes.ErrInvalidCaveats` |  |  | The caveats are invalid |
| `invalid_amount` | 400 | `limes.ErrInvalidAmount` |  |  | The amount is invalid |
| `effect_conflict` | 400 | `limes.ErrEffectConflict` |  |  | The declared effect conflicts with the operation |
| `cursor_mismatch` | 400 | `limes.ErrCursorMismatch` |  |  | The cursor is of another listing |
| `invalid_request` | 400 | `limes.ErrInvalidRequest` |  |  | The request is invalid |
| `delegation_too_wide` | 403 | `limes.ErrDelegationTooWide` |  |  | The delegation asks for more than its parent holds |
| `unknown_kid` | 401 | `jwks.ErrUnknownKID` |  |  | The key id names no key |
| `not_found` | 404 | `limes.ErrNotFound` |  |  | Not found |
| `unknown_tenant` | 404 | `limes.ErrUnknownTenant` |  |  | The tenant is unknown |
| `tenant_deleted` | 410 | `limes.ErrTenantDeleted` |  |  | The tenant is deleted |
| `scope_mismatch` | 403 | `limes.ErrScopeMismatch` |  |  | The call is scoped to another tenant than its request names |
| `proof_unsupported` | 500 | `limes.ErrProofUnsupported` |  |  | The capability is bound to a key and no proof is checked |
| `no_revocation_lineage` | 502 | `limes.ErrNoRevocationLineage` |  |  | The revocation upstream names no lineage |
| `copy_counters_not_metered` | 500 | `limes.ErrCopyCountersNotMetered` |  |  | Copy limits need a verifier that meters copies |
| `cursor_repeated` | 502 | `limes.ErrCursorRepeated` |  |  | The store returned a cursor already fetched |

## The meter's codes

`meter.ProblemOf` and `meter.CodeOf` add these to limes's.

| Code | HTTP | Sentinel | `reason` | Challenge `error` | Title |
|---|---|---|---|---|---|
| `copy_limit_exceeded` | 403 | `meter.ErrCopyLimitExceeded` | `request_limit_exceeded` (permanent) |  | The token's request or budget limit is exhausted |
| `counter_overflow` | 409 | `meter.ErrCounterOverflow` |  |  | The counter has no room for the amount |
| `charge_not_found` | 404 | `meter.ErrChargeNotFound` |  |  | The charge is not on record |
| `refund_exceeds_charge` | 409 | `meter.ErrRefundExceedsCharge` |  |  | The refund exceeds what the charge took |
| `reservation_not_found` | 404 | `meter.ErrReservationNotFound` |  |  | The reservation is not on record, released or expired |
| `usage_not_found` | 404 | `meter.ErrUsageNotFound` |  |  | The capability has no usage on record |
| `tenant_budget_not_found` | 404 | `meter.ErrTenantBudgetNotFound` |  |  | The tenant has no budget on record |
| `tenant_budget_version_mismatch` | 409 | `meter.ErrTenantBudgetVersionMismatch` |  |  | The tenant budget changed since it was read |
| `external_ref_conflict` | 409 | `meter.ErrExternalRefConflict` |  |  | The external reference names another charge |
| `settle_conflict` | 409 | `meter.ErrSettleConflict` |  |  | The reservation was settled for another amount |
