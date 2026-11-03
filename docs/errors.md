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
`limes.ProblemOf(err)`, which knows the meter's and the exchange's codes as
well: there is one registry of codes. It gives the RFC 9457 body and reports false for an
error that is not limes's, which the consumer answers as its own failure,
500:

```go
p, ok := limes.ProblemOf(err)
if !ok {
	p = limes.ProblemFor(err) // an error not limes's: internal, 500
}
// HTTP: write p as application/problem+json with p.Status.
// Another transport: carry p.Code, and p.Status as its numeric class.
```

`limes.ProblemFor(err)` never reports false: an error that is not limes's —
a database's, a consumer's own — is `internal`, 500.

**The token endpoint** (`exchange.Handler`) answers in RFC 6749 §5.2's
format: `error`, `error_description`, and a status. An `Exchanger` called
in process answers alike with `exchange.ErrorOf(err)`, which reports false
for an error that is none of the exchange's: answer it `server_error`, 500
(see [the exchange's codes](#the-exchanges-codes)).

The body, for a capability that was revoked:

```json
{
  "type": "https://github.com/oleg-tkachuk/limes/problems/revoked",
  "title": "The access token is invalid",
  "status": 401,
  "reason": "revoked",
  "code": "revoked",
  "category": "unauthenticated"
}
```

| Member | |
|---|---|
| `type` | A URI ending in the refusal's `reason`; for a refusal with none, in its family's type (`invalid-dpop-proof`, `invalid-token`, `insufficient-scope`, `conflict`); for an error that is no refusal, in its code (`limes.ProblemTypeURI`). |
| `title` | Fixed per kind, safe to show. |
| `status` | The HTTP status: the error's numeric code. |
| `code` | The stable code (`limes.Code`): what a client branches on. |
| `category` | The code's `limes.Category`: what the caller does about it. |
| `reason` | For a refusal of a presented capability, its `limes.Reason`, which can be wider than the code: `tenant_erased` and `delegation_too_deep` are reported as `revoked`. |

There is no `detail`: an error's own text is not shown to the client.

## Every component

Every error a component of limes returns is one of the codes below, or
wraps one. That holds for the root module, `dpop`, `jwks`, `meter`,
`inspect`, `memstore`, `pgstore` and `pgstore/migrate`, `redisreplay`,
`otelmetric`, `exchange` and `exchange/jwtverify`, and `biscuit`.

- **Constructors** refuse a config they cannot work with as
  `invalid_config`: a required field missing, a negative bound, a key of
  another kind.
- **A call malformed in itself** is `invalid_request`: among them a
  `RevokeMatchingRequest` naming both a tenant and `AllTenants`, or
  neither, or a subject, an on-behalf-of or an agent run beside
  `AllTenants`; a `ReleaseMatchingRequest` the same, or with a capability
  beside `AllTenants`; and either naming no selector; and a
  `ListRecordsRequest` naming both a tenant and `AllTenants`, or neither.
- **A call under a narrower scope than it needs** is `scope_mismatch`:
  another tenant's, a read-only scope for a write, or one tenant's for a
  call across every tenant (`AllTenants`).
- **An invariant broken inside limes** is `internal`.
- **Each refusal** has its own code.

Each module's tests hold it to this: a test finds every error the
module's sources construct, and refuses one that names no sentinel. The
only exceptions are the few decoding helpers whose every caller wraps them
in a family, and each module's test lists those by name. An error from a
dependency — a database driver, an HTTP client — reaches a caller wrapped
in a code where limes knows what it means (`keys_unavailable` for an
issuer's keys out of reach), and as itself otherwise. `ProblemFor` answers
that one as `internal`.

The nested modules' own sentinels belong to these families, so each has
their code:

| Sentinel | Code |
|---|---|
| `pgstore.ErrSchemaMismatch` | `invalid_config` |
| `pgstore.ErrRuntimeRole` | `invalid_config` |
| `pgstore/migrate.ErrSchemaOwner` | `invalid_config` |
| `pgstore.ErrChargeTransaction` | `invalid_request` |
| `inspect.ErrInvalidKey` | `invalid_request` |

## Sentinel, code, reason and category

Each error answers four questions, each by its own name, and each read
from the error with one call:

| | Read with | For |
|---|---|---|
| Sentinel, `limes.Err…` | `errors.Is` | Branching in Go. Families nest: `meter.ErrLeaseExhausted` is also `limes.ErrBudgetExceeded`. |
| Code, `limes.Code` | `limes.CodeOf` | The wire: the one stable name of every error, which a client branches on. |
| Reason, `limes.Reason` | `limes.ReasonOf`, `limes.ReasonOfCode` | A refusal of a presented capability only: why, as RFC 6750 and RFC 9449 challenge it, and whether it is permanent (`Reason.Permanent`). |
| Category, `limes.Category` | `limes.CategoryOf`, `Problem.Category` | What the caller does about it, for a transport with fewer codes than limes. |

A refusal's code is its reason, but where the reason folds several codes
into one: `tenant_erased` and `delegation_too_deep` are refused as
`revoked`, `copy_limit_exceeded` and `lease_requests_exhausted` as
`request_limit_exceeded`, `lease_exhausted` as `budget_exceeded`.
`limes.ReasonOfCode(code)` reads that table,
so a consumer holding only a code — mapping it, or filling an error detail
— gets the reason without the error; a code cannot be derived from a
reason, since one reason stands for several. Send the code, and the reason
beside it where a challenge or an error detail wants one. An error that is
no refusal — `not_found`, `conflict` — has a code and a category and no
reason.

## Retrying

- **A 5xx** is the server's to fix, or its upstream's.
- **A refusal whose `reason` is permanent** (`limes.Reason.Permanent()`,
  marked *permanent* below) stands for as long as the capability does:
  stop, and get another capability.
- **Any other refusal** can lift. A budget frees as holds lapse, a nonce is
  signed afresh, an idempotency key can be added.
- **A 409** is a request that disagrees with what is on record: read it
  again, and decide.

`limes.Codes()` lists every code, the meter's and the exchange's among
them, so a consumer's test can prove its own mapping covers them all
(`limestest`, under [Categories](#categories)). A code's status, category and
reason are part of the contract ([compatibility.md](compatibility.md)):
changing one is a break, marked and listed in the release notes, and these
tables are what CI compares.

## Categories

Every code belongs to one `limes.Category`, the last column of the tables
below: what the caller does about it. A consumer whose transport has fewer
codes than limes maps the ten categories, read with `limes.CategoryOf(err)`,
rather than every code:

| Category | Meaning |
|---|---|
| `unauthenticated` | The credential is missing, unproven or no longer good: present another. A malformed one is `invalid`. |
| `denied` | The credential is good and does not allow the request. |
| `exhausted` | A request, budget, copy or lease limit refused it. |
| `invalid` | The request itself is wrong: sent again unchanged, it fails again. |
| `not_found` | What the request names is not on record. |
| `conflict` | The request disagrees with what is on record: read it again, and decide. |
| `already_exists` | What the request would create is on record already: read it rather than create it again. |
| `precondition` | The request is well formed, and the state it needs is not there — the store's configuration, the ledger, another capability's lease: it fails again until that state changes. |
| `unavailable` | A dependency failed on this request — did not answer, or answered a page it had already given: retry later. |
| `internal` | The server's configuration or a store failed: the server's to fix. |

The status stays the finer answer: within `exhausted`, a permanent refusal
(`request_limit_exceeded`) never lifts while a budget can.

`limes.CategoryOfCode(code)` answers a code's category, and
`limes.ProblemOfCode(code)` its whole problem, without an error to hand.

A mapping of the categories has a default branch, for a category added
after it was written; adding one is not a break
([compatibility.md](compatibility.md)). That branch answers from
`Problem.Status`, the error's own HTTP status, never as a success and
never as one fixed code: a client's error stays the client's.

A code answered with another status or category is a break, and it still
compiles. `limestest` turns it into a failing test of the consumer's: it
pins what the consumer's own transport answers each code with, and walks
the codes of the origins the consumer runs (`limes.CodesOf`):

```go
func TestErrorContract(t *testing.T) {
	codes := slices.Concat(limes.CodesOf(limes.OriginLimes), limes.CodesOf(limes.OriginMeter))
	// Pinned from the first failing run, which names every entry.
	limestest.CheckMapping(t, codes, map[limes.Code]connect.Code{
		"revoked": connect.CodeUnauthenticated,
		// …
	}, connectCodeOf)
	limestest.CheckCategories(t, func(c limes.Category) bool {
		_, ok := connectCodes[c]
		return ok
	})
}
```

`limestest.CheckCodes` is the same check of every code against its HTTP
status and category, for a consumer that answers in HTTP's terms.

A category says what retrying would do, not how bad the failure is, and
agrees with the status: every 502 and 503 is `unavailable`, a dependency
that failed and may answer the next time — the issuer's keys
(`keys_unavailable`), a store whose pages handed back a cursor already
fetched (`cursor_repeated`), which a walk begun again can get past once
whatever moved its listing settles, and a revocation upstream that names
no lineage (`no_revocation_lineage`), which `NewRevocationCache` refuses
at startup until the upstream it is given does. Every `unauthenticated` code is
a credential no longer good or never proven — expired, revoked, a bad
proof — which another credential may pass; a transport whose codes keep
those apart from a lack of permission maps them to its own
unauthenticated code, not to its permission-denied one.

## limes's codes

| Code | HTTP | Sentinel | `reason` | Challenge `error` | Title | Category |
|---|---|---|---|---|---|---|
| `malformed_authorization` | 400 | `limes.ErrMalformedAuthorization` | `malformed_authorization` (permanent) | `invalid_request` | The Authorization header is malformed | `invalid` |
| `no_credentials` | 401 | `limes.ErrNoCredentials` | `no_credentials` |  | Credentials are required | `unauthenticated` |
| `keys_unavailable` | 503 | `jwks.ErrUnavailable` | `keys_unavailable` |  | The issuer's keys cannot be fetched | `unavailable` |
| `nonce_required` | 401 | `dpop.ErrNonceRequired` | `nonce_required` | `use_dpop_nonce` | A DPoP proof with the server's nonce is required | `unauthenticated` |
| `proof_required` | 401 | `dpop.ErrRequired` | `proof_required` | `invalid_dpop_proof` | The DPoP proof is missing or invalid | `unauthenticated` |
| `proof_invalid` | 401 | `dpop.ErrInvalid` | `proof_invalid` | `invalid_dpop_proof` | The DPoP proof is missing or invalid | `unauthenticated` |
| `proof_replayed` | 401 | `dpop.ErrReplayed` | `proof_replayed` | `invalid_dpop_proof` | The DPoP proof is missing or invalid | `unauthenticated` |
| `auth_scheme_mismatch` | 401 | `limes.ErrAuthSchemeMismatch` | `auth_scheme_mismatch` | `invalid_token` | The access token is invalid | `unauthenticated` |
| `proof_of_possession` | 401 | `limes.ErrProofOfPossession` |  | `invalid_dpop_proof` | The DPoP proof is missing or invalid | `unauthenticated` |
| `biscuit_attenuation` | 401 | `limes.ErrBiscuitAttenuation` | `biscuit_attenuation` (permanent) | `invalid_token` | The access token is invalid | `unauthenticated` |
| `invalid_token` | 401 | `limes.ErrInvalidToken` | `invalid_token` (permanent) | `invalid_token` | The access token is invalid | `unauthenticated` |
| `expired` | 401 | `limes.ErrExpired` | `expired` (permanent) | `invalid_token` | The access token is invalid | `unauthenticated` |
| `not_yet_valid` | 401 | `limes.ErrNotYetValid` | `not_yet_valid` | `invalid_token` | The access token is invalid | `unauthenticated` |
| `tenant_erased` | 401 | `limes.ErrTenantErased` | `revoked` (permanent) | `invalid_token` | The access token is invalid | `unauthenticated` |
| `delegation_too_deep` | 401 | `limes.ErrDelegationTooDeep` | `revoked` (permanent) | `invalid_token` | The access token is invalid | `unauthenticated` |
| `revoked` | 401 | `limes.ErrRevoked` | `revoked` (permanent) | `invalid_token` | The access token is invalid | `unauthenticated` |
| `audience_mismatch` | 401 | `limes.ErrAudienceMismatch` | `audience_mismatch` (permanent) | `invalid_token` | The access token is invalid | `unauthenticated` |
| `unauthenticated` | 401 | `limes.ErrUnauthenticated` |  | `invalid_token` | The access token is invalid | `unauthenticated` |
| `op_not_allowed` | 403 | `limes.ErrOpNotAllowed` | `op_not_allowed` (permanent) | `insufficient_scope` | The token does not allow this request | `denied` |
| `resource_not_allowed` | 403 | `limes.ErrResourceNotAllowed` | `resource_not_allowed` (permanent) | `insufficient_scope` | The token does not allow this request | `denied` |
| `source_ip_not_allowed` | 403 | `limes.ErrSourceIPNotAllowed` | `source_ip_not_allowed` (permanent) | `insufficient_scope` | The token does not allow this request | `denied` |
| `idempotency_key_required` | 403 | `limes.ErrIdempotencyKeyRequired` | `idempotency_key_required` | `insufficient_scope` | The token does not allow this request | `denied` |
| `tainted_read_not_allowed` | 403 | `limes.ErrTaintedReadNotAllowed` | `tainted_read_not_allowed` | `insufficient_scope` | The token does not allow this request | `denied` |
| `caveat_violation` | 403 | `limes.ErrCaveatViolation` |  | `insufficient_scope` | The token does not allow this request | `denied` |
| `request_limit_exceeded` | 403 | `limes.ErrRequestLimitExceeded` | `request_limit_exceeded` (permanent) |  | The token's request or budget limit is exhausted | `exhausted` |
| `budget_exceeded` | 403 | `limes.ErrBudgetExceeded` | `budget_exceeded` |  | The token's request or budget limit is exhausted | `exhausted` |
| `tenant_budget_exceeded` | 403 | `limes.ErrTenantBudgetExceeded` | `tenant_budget_exceeded` |  | The token's request or budget limit is exhausted | `exhausted` |
| `unit_code_mismatch` | 409 | `limes.ErrUnitCodeMismatch` |  |  | The request conflicts with what is on record | `precondition` |
| `already_exists` | 409 | `limes.ErrAlreadyExists` |  |  | The request conflicts with what is on record | `already_exists` |
| `conflict` | 409 | `limes.ErrConflict` |  |  | The request conflicts with what is on record | `conflict` |
| `invalid_caveats` | 400 | `limes.ErrInvalidCaveats` |  |  | The caveats are invalid | `invalid` |
| `invalid_amount` | 400 | `limes.ErrInvalidAmount` |  |  | The amount is invalid | `invalid` |
| `effect_conflict` | 400 | `limes.ErrEffectConflict` |  |  | The declared effect conflicts with the operation | `invalid` |
| `cursor_mismatch` | 400 | `limes.ErrCursorMismatch` |  |  | The cursor is of another listing | `invalid` |
| `invalid_request` | 400 | `limes.ErrInvalidRequest` |  |  | The request is invalid | `invalid` |
| `delegation_too_wide` | 403 | `limes.ErrDelegationTooWide` |  |  | The delegation asks for more than its parent holds | `denied` |
| `unknown_kid` | 401 | `jwks.ErrUnknownKID` |  |  | The key id names no key | `unauthenticated` |
| `not_found` | 404 | `limes.ErrNotFound` |  |  | Not found | `not_found` |
| `unknown_tenant` | 404 | `limes.ErrUnknownTenant` |  |  | The tenant is unknown | `not_found` |
| `tenant_deleted` | 410 | `limes.ErrTenantDeleted` |  |  | The tenant is deleted | `not_found` |
| `scope_mismatch` | 500 | `limes.ErrScopeMismatch` |  |  | The call is scoped to another tenant than its request names | `internal` |
| `proof_unsupported` | 500 | `limes.ErrProofUnsupported` |  |  | The capability is bound to a key and no proof is checked | `precondition` |
| `no_revocation_lineage` | 502 | `limes.ErrNoRevocationLineage` |  |  | The revocation upstream names no lineage | `unavailable` |
| `copy_counters_not_metered` | 500 | `limes.ErrCopyCountersNotMetered` |  |  | Copy limits need a verifier that meters copies | `internal` |
| `cursor_repeated` | 502 | `limes.ErrCursorRepeated` |  |  | The store returned a cursor already fetched | `unavailable` |
| `invalid_config` | 500 | `limes.ErrInvalidConfig` |  |  | The server is misconfigured | `internal` |
| `internal` | 500 | `limes.ErrInternal` |  |  | Internal error | `internal` |

## The meter's codes

The meter's sentinels name their codes, and limes's functions answer them
as their own: `limes.CodeOf`, `limes.CategoryOf`, `limes.ProblemOf` and
`limes.Codes` know every one.

| Code | HTTP | Sentinel | `reason` | Challenge `error` | Title | Category |
|---|---|---|---|---|---|---|
| `copy_limit_exceeded` | 403 | `meter.ErrCopyLimitExceeded` | `request_limit_exceeded` (permanent) |  | The token's request or budget limit is exhausted | `exhausted` |
| `counter_overflow` | 409 | `meter.ErrCounterOverflow` |  |  | The counter has no room for the amount | `precondition` |
| `charge_not_found` | 404 | `meter.ErrChargeNotFound` |  |  | The charge is not on record | `not_found` |
| `refund_exceeds_charge` | 409 | `meter.ErrRefundExceedsCharge` |  |  | The refund exceeds what the charge took | `precondition` |
| `reservation_not_found` | 404 | `meter.ErrReservationNotFound` |  |  | The reservation is not on record, released or expired | `not_found` |
| `usage_not_found` | 404 | `meter.ErrUsageNotFound` |  |  | The capability has no usage on record | `not_found` |
| `tenant_budget_not_found` | 404 | `meter.ErrTenantBudgetNotFound` |  |  | The tenant has no budget on record | `not_found` |
| `tenant_budget_version_mismatch` | 409 | `meter.ErrTenantBudgetVersionMismatch` |  |  | The tenant budget changed since it was read | `conflict` |
| `external_ref_conflict` | 409 | `meter.ErrExternalRefConflict` |  |  | The external reference names another charge | `conflict` |
| `settle_conflict` | 409 | `meter.ErrSettleConflict` |  |  | The reservation was settled for another amount | `conflict` |
| `lease_exhausted` | 403 | `meter.ErrLeaseExhausted` | `budget_exceeded` (permanent) |  | The token's request or budget limit is exhausted | `exhausted` |
| `lease_requests_exhausted` | 403 | `meter.ErrLeaseRequestsExhausted` | `request_limit_exceeded` (permanent) |  | The token's request or budget limit is exhausted | `exhausted` |
| `lease_not_found` | 404 | `meter.ErrLeaseNotFound` |  |  | The capability holds no lease | `not_found` |
| `lease_nested` | 409 | `meter.ErrLeaseNested` |  |  | An ancestor of the capability holds a lease | `precondition` |
| `leasing_disabled` | 500 | `meter.ErrLeasingDisabled` |  |  | Leasing is not enabled on the store | `precondition` |
| `feature_disabled` | 500 | `meter.ErrFeatureDisabled` |  |  | A feature is disabled by the store's configuration | `precondition` |

## The exchange's codes

`exchange.ErrorOf` and `exchange.ErrorCodes` answer an exchange's refusals
in RFC 6749 §5.2's format, as the token endpoint does. The codes are RFC
6749's, RFC 8707's `invalid_target`, RFC 9396's
`invalid_authorization_details` and RFC 9449's DPoP codes.

They are limes's codes too: each sentinel of the exchange names its own,
so `limes.CodeOf`, `limes.CategoryOf` and `limes.ProblemOf` answer an
exchange's error as the endpoint does — with the same code and status,
ahead of any limes error it wraps — and `limes.Codes()` lists them, as
`limes.CodesOf(limes.OriginExchange)` does alone. One mapping of the
categories covers the exchange. A consumer that runs no exchange never
meets these codes, and may leave them out of a check of its own mapping.

| Code | HTTP | Sentinel | Description | Category |
|---|---|---|---|---|
| `unauthorized_client` | 400 | `exchange.ErrUnauthorizedClient` | The client may not exchange tokens. | `denied` |
| `invalid_client` | 401 | `exchange.ErrInvalidClient` | The client could not be authenticated; challenged in the scheme it used, or 400 when it named none. | `unauthenticated` |
| `unsupported_grant_type` | 400 | `exchange.ErrUnsupportedGrantType` | Only token exchange is supported. | `invalid` |
| `invalid_grant` | 400 | `exchange.ErrInvalidGrant` | The subject token was refused. | `unauthenticated` |
| `invalid_target` | 400 | `exchange.ErrInvalidTarget` | The resource or audience was refused. | `denied` |
| `invalid_target` | 400 | `exchange.ErrTargetExchanged` | The subject token was exchanged already, for another resource or audience: one capability per subject token (`Config.Exchanges`). | `denied` |
| `invalid_scope` | 400 | `exchange.ErrInvalidScope` | The scope was refused. | `denied` |
| `invalid_authorization_details` | 400 | `exchange.ErrInvalidAuthorizationDetails` | The authorization details were refused. | `denied` |
| `invalid_request` | 400 | `exchange.ErrInvalidRequest` | The request is malformed. | `invalid` |
| `server_error` | 500 | `exchange.ErrServerError` | The server could not decide on the request. | `internal` |

The endpoint answers a refused proof with RFC 9449's codes,
`dpop.ErrorCodeUseNonce` and `dpop.ErrorCodeInvalidProof`. They are no
`limes.Code`: in process, `dpop.ErrRequired` and its kin are limes's proof
codes, `unauthenticated`, above.

| Code | HTTP | Sentinel | Description |
|---|---|---|---|
| `use_dpop_nonce` | 400 | `dpop.ErrNonceRequired` | The DPoP proof must carry the server's nonce; a fresh one goes in `DPoP-Nonce`. |
| `invalid_dpop_proof` | 400 | `dpop.ErrRequired`, `dpop.ErrInvalid`, `dpop.ErrReplayed` | The DPoP proof is required, invalid, or used before. |
