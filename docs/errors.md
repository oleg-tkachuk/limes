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
- an RFC 9457 `application/problem+json` body, sent with
  `Cache-Control: no-store`. The error's own text never reaches the client.

The challenge is under the client's scheme, `Bearer` when it is unknown; a
refusal for want of credentials — no header, or another scheme's — carries
no error code, only the realm (RFC 6750 §3.1). A verifier with DPoP
configured adds a `DPoP algs="…"` challenge with no error code beside a
`Bearer` one, and every `DPoP` challenge it answers carries `algs`. A
challenge with an error code also carries `error_description`, the
refusal's fixed title. Every challenge ends with `realm="…"`:
`VerifierConfig.Realm`, or `DefaultRealm`. An error that is no refusal is
answered 500, with no challenge.

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
  "type": "https://github.com/oleg-tkachuk/limes/blob/main/docs/errors.md#revoked",
  "title": "The access token is invalid",
  "status": 401,
  "reason": "revoked",
  "code": "revoked",
  "category": "unauthenticated"
}
```

| Member | |
|---|---|
| `type` | This document, at the refusal's `reason`; for a refusal with none, at its family's type; for an error that is no refusal, at its code (`limes.ProblemTypeURI`). Each is an anchor of [Problem types](#problem-types), so the URI resolves to what it names. |
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

`tenant_erased` and `delegation_too_deep` answer a presented capability:
a metering call, a revocation lookup. Where a store gives the same answer to
a mint — `Store.Insert` refusing a child past `MaxDelegationDepth`, or a
record of an erased tenant — no capability was presented, and none of the
caller's stopped working, so the `Issuer` answers it as the mint's refusal:
`Delegate` returns `delegation_depth_exceeded` (403, `denied`; the parent
stays valid, delegate from a shallower one), `Issue` and `Delegate` return
`tenant_gone` (410, `not_found`). Neither matches `limes.ErrRevoked` nor
has a reason, and the store's own error stays in the text only.

## Retrying

- **A 5xx** is the server's to fix, or its upstream's.
- **A refusal whose `reason` is permanent** (`limes.Reason.Permanent()`,
  marked *permanent* below) stands for as long as the capability does:
  stop, and get another capability.
- **Any other refusal** can lift. A budget frees as holds lapse, a nonce is
  signed afresh, an idempotency key can be added.
- **A 409** is a request that disagrees with what is on record: read it
  again, and decide.
- **`outcome_unknown`** is a meter call whose commit left and whose answer
  was lost: it may have committed. Send it again unchanged only when it is
  keyed — `Charge` with an `ExternalRef`, `Reserve` with a `ReservationID`,
  `Refund` with a `RefundID`, or `Settle` — and a repeat of one that
  committed is answered as a replay. An unkeyed call sent again may count
  twice (docs/guide.md, "Retrying a lost answer").
- **`tenant_budget_busy`** is a `SetTenantBudget` that changed nothing: the
  tenant's calls in flight held it past the store's bound
  (pgstore's `Config.TenantBudgetLockTimeout`). Send it again. pgstore's
  error is a `meter.TenantBudgetBusyError`, which says how many calls held
  the tenant and how long the oldest it can see had run: one idle in its
  transaction is a caller holding it open, which no retry outlasts until
  that caller ends it, or the database's
  `idle_in_transaction_session_timeout` does.

`limes.Codes()` lists every code, the meter's among them and, in a
binary that links the exchange in, the exchange's, so a consumer's test can prove its own mapping covers them all
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
| `exhausted` | A request, budget, copy or lease limit refused it, or the replay cache's room for one holder's proofs. |
| `invalid` | The request itself is wrong: sent again unchanged, it fails again. |
| `not_found` | What the request names is not on record. |
| `conflict` | The request disagrees with what is on record: read it again, and decide. |
| `already_exists` | What the request would create is on record already: read it rather than create it again. |
| `precondition` | The request is well formed, and the state it needs is not on record — the ledger's room, the charge it refunds, another capability's lease: it fails again until that state changes. |
| `unavailable` | A dependency failed on this request — did not answer, or answered a page it had already given: retry later. |
| `internal` | The server's configuration or a store failed: the server's to fix. A feature the server does not run — leasing, a proof checker — is its configuration. |

Each category spans one class of status: every code of it is the client's,
a 4xx, or every one the server's, a 5xx. Whose fault an error is follows
from its category.

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
pins what the consumer's own transport answers each code with, over
every code but those of a component the consumer does not run — the
exchange's (`limestest.WithoutExchange`), the meter's
(`limestest.WithoutMeter`). Every code is checked by default, so a code
limes adds fails the check until it is pinned; no option leaves out a code
alone. A consumer that maps categories whole takes a code limes adds by
its category with `limestest.AcceptNewByCategory()`: such a code passes
when the consumer answers it as it answers every code pinned in that
category, while a pinned code moved to another category still fails. That
option is no filter, as `Without…` are: it judges every code more
leniently, and names the codes it passed only in the test's log, which
`go test -v` alone shows. A
failure
names the entry to pin, in Go's syntax, with a code that names itself
(`fmt.Stringer`) named beside its number:

```go
func TestErrorContract(t *testing.T) {
	// Pinned from the first failing run, which names every entry.
	limestest.CheckMapping(t, map[limes.Code]connect.Code{
		"revoked": connect.CodeUnauthenticated,
		// …
	}, connectCodeOf, limestest.WithoutExchange())
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
(`keys_unavailable`), the store or upstream that answers revocation
lookups (`revocation_unavailable`), a store whose pages handed back a cursor already
fetched (`cursor_repeated`), which a walk begun again can get past once
whatever moved its listing settles, and a revocation upstream that names
no lineage (`no_revocation_lineage`), which `NewRevocationCache` refuses
at startup until the upstream it is given does, and a verifier requiring a
record answers when its lookup wraps a cache over such an upstream; and a store whose answer to
a commit was lost (`outcome_unknown`), which a keyed call sent again
settles. Every `unauthenticated` code is
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
| `revocation_unavailable` | 503 | `limes.ErrRevocationUnavailable` | `revocation_unavailable` |  | The revocation status cannot be checked | `unavailable` |
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
| `unrecorded` | 401 | `limes.ErrUnrecorded` | `unrecorded` | `invalid_token` | The access token is invalid | `unauthenticated` |
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
| `proof_quota_exceeded` | 429 | `dpop.ErrReplayQuota` | `proof_quota_exceeded` |  | Too many DPoP proofs inside the replay window | `exhausted` |
| `unit_code_mismatch` | 409 | `limes.ErrUnitCodeMismatch` |  |  | The request conflicts with what is on record | `precondition` |
| `already_exists` | 409 | `limes.ErrAlreadyExists` |  |  | The request conflicts with what is on record | `already_exists` |
| `conflict` | 409 | `limes.ErrConflict` |  |  | The request conflicts with what is on record | `conflict` |
| `invalid_caveats` | 400 | `limes.ErrInvalidCaveats` |  |  | The caveats are invalid | `invalid` |
| `invalid_amount` | 400 | `limes.ErrInvalidAmount` |  |  | The amount is invalid | `invalid` |
| `effect_conflict` | 400 | `limes.ErrEffectConflict` |  |  | The declared effect conflicts with the operation | `invalid` |
| `cursor_mismatch` | 400 | `limes.ErrCursorMismatch` |  |  | The cursor is of another listing | `invalid` |
| `invalid_request` | 400 | `limes.ErrInvalidRequest` |  |  | The request is invalid | `invalid` |
| `delegation_too_wide` | 403 | `limes.ErrDelegationTooWide` |  |  | The delegation asks for more than its parent holds | `denied` |
| `delegation_depth_exceeded` | 403 | `limes.ErrDelegationDepthExceeded` |  |  | The delegation would exceed the maximum chain depth | `denied` |
| `unknown_kid` | 401 | `jwks.ErrUnknownKID` |  |  | The key id names no key | `unauthenticated` |
| `not_found` | 404 | `limes.ErrNotFound` |  |  | Not found | `not_found` |
| `unknown_tenant` | 404 | `limes.ErrUnknownTenant` |  |  | The tenant is unknown | `not_found` |
| `tenant_deleted` | 410 | `limes.ErrTenantDeleted` |  |  | The tenant is deleted | `not_found` |
| `tenant_gone` | 410 | `limes.ErrTenantGone` |  |  | The tenant is erased | `not_found` |
| `scope_mismatch` | 500 | `limes.ErrScopeMismatch` |  |  | The call is scoped to another tenant than its request names | `internal` |
| `proof_unsupported` | 500 | `limes.ErrProofUnsupported` |  |  | The capability is bound to a key and no proof is checked | `internal` |
| `no_revocation_lineage` | 502 | `limes.ErrNoRevocationLineage` |  |  | The revocation upstream names no lineage | `unavailable` |
| `copy_counters_not_metered` | 500 | `limes.ErrCopyCountersNotMetered` |  |  | Copy limits need a verifier that meters copies | `internal` |
| `cursor_repeated` | 502 | `limes.ErrCursorRepeated` |  |  | The store returned a cursor already fetched | `unavailable` |
| `invalid_config` | 500 | `limes.ErrInvalidConfig` |  |  | The server is misconfigured | `internal` |
| `internal` | 500 | `limes.ErrInternal` |  |  | Internal error | `internal` |

## The meter's codes

The meter's sentinels name their codes, and limes's functions answer them
as their own: `limes.CodeOf`, `limes.CategoryOf`, `limes.ProblemOf` and
`limes.Codes` know every one. A consumer that meters nothing never meets
them, and may leave them out of a check of its own mapping
(`limestest.WithoutMeter`).

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
| `reserve_conflict` | 409 | `meter.ErrReserveConflict` |  |  | The reservation id names another hold | `conflict` |
| `refund_conflict` | 409 | `meter.ErrRefundConflict` |  |  | The refund id names another refund | `conflict` |
| `outcome_unknown` | 503 | `meter.ErrOutcomeUnknown` |  |  | The store lost the answer to the call's commit | `unavailable` |
| `tenant_budget_busy` | 503 | `meter.ErrTenantBudgetBusy` |  |  | The tenant's calls in flight held its budget too long | `unavailable` |
| `lease_exhausted` | 403 | `meter.ErrLeaseExhausted` | `budget_exceeded` (permanent) |  | The token's request or budget limit is exhausted | `exhausted` |
| `lease_requests_exhausted` | 403 | `meter.ErrLeaseRequestsExhausted` | `request_limit_exceeded` (permanent) |  | The token's request or budget limit is exhausted | `exhausted` |
| `lease_not_found` | 404 | `meter.ErrLeaseNotFound` |  |  | The capability holds no lease | `not_found` |
| `lease_nested` | 409 | `meter.ErrLeaseNested` |  |  | An ancestor of the capability holds a lease | `precondition` |
| `leasing_disabled` | 500 | `meter.ErrLeasingDisabled` |  |  | Leasing is not enabled on the store | `internal` |
| `feature_disabled` | 500 | `meter.ErrFeatureDisabled` |  |  | A feature is disabled by the store's configuration | `internal` |

## The exchange's codes

`exchange.ErrorOf` and `exchange.ErrorCodes` answer an exchange's refusals
in RFC 6749 §5.2's format, as the token endpoint does. The codes are RFC
6749's, RFC 8707's `invalid_target`, RFC 9396's
`invalid_authorization_details` and RFC 9449's DPoP codes.

They are limes's codes too: each sentinel of the exchange carries its
own code, status, title and category, so `limes.CodeOf`,
`limes.CategoryOf` and `limes.ProblemOf` answer an exchange's error as the
endpoint does — with the same code and status, ahead of any limes error it
wraps — and the exchange registers them as it initialises, so
`limes.Codes()`, `limes.ProblemOfCode` and `limes.CategoryOfCode` answer
them in a binary that links the exchange in. limes holds no table of them:
the exchange adds a code without a release of limes. An error an
`Exchanger` returns that is no refusal of the client's — the issuer, a
store or the policy failing — wraps `exchange.ErrServerError`, so
`limes.ProblemOf` answers it `server_error`, 500, as the endpoint does,
whatever limes error it wraps. One
mapping of the categories covers the exchange. A consumer that runs no exchange never meets these codes,
and may leave them out of a check of its own mapping
(`limestest.WithoutExchange`).

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
`dpop.ErrorCodeUseNonce` and `dpop.ErrorCodeInvalidProof`, and a client
past its share of the replay cache with 429 and RFC 6749's
`temporarily_unavailable`, since RFC 9449 names no code for it. They are
no `limes.Code`, and need no mapping of their own: the errors behind them,
`dpop.ErrRequired` and its kin, are limes's proof codes (`proof_required`,
`proof_invalid`, `proof_replayed`, `nonce_required`, all
`unauthenticated`, and `proof_quota_exceeded`, `exhausted`), which
`limes.Codes()` lists and `limestest` checks. A
service that serves the token endpoint itself maps every error by
`limes.ProblemOf(err).Category`, as any other, and reads
`exchange.ErrorOf(err)` only for the RFC 6749 body it writes; its tests
pin `limes.Codes()`, and `exchange.ErrorCodes()` is the endpoint's wire,
not a second list to map.

| Code | HTTP | Sentinel | Description |
|---|---|---|---|
| `use_dpop_nonce` | 400 | `dpop.ErrNonceRequired` | The DPoP proof must carry the server's nonce; a fresh one goes in `DPoP-Nonce`. |
| `invalid_dpop_proof` | 400 | `dpop.ErrRequired`, `dpop.ErrInvalid`, `dpop.ErrReplayed` | The DPoP proof is required, invalid, or used before. |
| `temporarily_unavailable` | 429 | `dpop.ErrReplayQuota` | The client sent more DPoP proofs inside the replay window than the cache keeps room for; the same request signed afresh passes once older ids lapse. |

## Problem types

Every problem type URI is this document's address and a fragment, the
type's name, which is an anchor below: a code's row says where the code is
described, and a refusal with no `reason` is typed by its family. Each
type means the same for the whole of v1.

| Type | HTTP | Described in |
|---|---|---|
| <a id="malformed_authorization"></a>`malformed_authorization` | 400 | [limes's codes](#limess-codes) |
| <a id="no_credentials"></a>`no_credentials` | 401 | [limes's codes](#limess-codes) |
| <a id="keys_unavailable"></a>`keys_unavailable` | 503 | [limes's codes](#limess-codes) |
| <a id="revocation_unavailable"></a>`revocation_unavailable` | 503 | [limes's codes](#limess-codes) |
| <a id="nonce_required"></a>`nonce_required` | 401 | [limes's codes](#limess-codes) |
| <a id="proof_required"></a>`proof_required` | 401 | [limes's codes](#limess-codes) |
| <a id="proof_invalid"></a>`proof_invalid` | 401 | [limes's codes](#limess-codes) |
| <a id="proof_replayed"></a>`proof_replayed` | 401 | [limes's codes](#limess-codes) |
| <a id="auth_scheme_mismatch"></a>`auth_scheme_mismatch` | 401 | [limes's codes](#limess-codes) |
| <a id="proof_of_possession"></a>`proof_of_possession` | 401 | [limes's codes](#limess-codes) |
| <a id="biscuit_attenuation"></a>`biscuit_attenuation` | 401 | [limes's codes](#limess-codes) |
| <a id="invalid_token"></a>`invalid_token` | 401 | [limes's codes](#limess-codes) |
| <a id="expired"></a>`expired` | 401 | [limes's codes](#limess-codes) |
| <a id="not_yet_valid"></a>`not_yet_valid` | 401 | [limes's codes](#limess-codes) |
| <a id="tenant_erased"></a>`tenant_erased` | 401 | [limes's codes](#limess-codes) |
| <a id="delegation_too_deep"></a>`delegation_too_deep` | 401 | [limes's codes](#limess-codes) |
| <a id="revoked"></a>`revoked` | 401 | [limes's codes](#limess-codes) |
| <a id="unrecorded"></a>`unrecorded` | 401 | [limes's codes](#limess-codes) |
| <a id="audience_mismatch"></a>`audience_mismatch` | 401 | [limes's codes](#limess-codes) |
| <a id="unauthenticated"></a>`unauthenticated` | 401 | [limes's codes](#limess-codes) |
| <a id="op_not_allowed"></a>`op_not_allowed` | 403 | [limes's codes](#limess-codes) |
| <a id="resource_not_allowed"></a>`resource_not_allowed` | 403 | [limes's codes](#limess-codes) |
| <a id="source_ip_not_allowed"></a>`source_ip_not_allowed` | 403 | [limes's codes](#limess-codes) |
| <a id="idempotency_key_required"></a>`idempotency_key_required` | 403 | [limes's codes](#limess-codes) |
| <a id="tainted_read_not_allowed"></a>`tainted_read_not_allowed` | 403 | [limes's codes](#limess-codes) |
| <a id="caveat_violation"></a>`caveat_violation` | 403 | [limes's codes](#limess-codes) |
| <a id="request_limit_exceeded"></a>`request_limit_exceeded` | 403 | [limes's codes](#limess-codes) |
| <a id="budget_exceeded"></a>`budget_exceeded` | 403 | [limes's codes](#limess-codes) |
| <a id="tenant_budget_exceeded"></a>`tenant_budget_exceeded` | 403 | [limes's codes](#limess-codes) |
| <a id="proof_quota_exceeded"></a>`proof_quota_exceeded` | 429 | [limes's codes](#limess-codes) |
| <a id="unit_code_mismatch"></a>`unit_code_mismatch` | 409 | [limes's codes](#limess-codes) |
| <a id="already_exists"></a>`already_exists` | 409 | [limes's codes](#limess-codes) |
| <a id="conflict"></a>`conflict` | 409 | [limes's codes](#limess-codes) |
| <a id="invalid_caveats"></a>`invalid_caveats` | 400 | [limes's codes](#limess-codes) |
| <a id="invalid_amount"></a>`invalid_amount` | 400 | [limes's codes](#limess-codes) |
| <a id="effect_conflict"></a>`effect_conflict` | 400 | [limes's codes](#limess-codes) |
| <a id="cursor_mismatch"></a>`cursor_mismatch` | 400 | [limes's codes](#limess-codes) |
| <a id="invalid_request"></a>`invalid_request` | 400 | [limes's codes](#limess-codes) |
| <a id="delegation_too_wide"></a>`delegation_too_wide` | 403 | [limes's codes](#limess-codes) |
| <a id="delegation_depth_exceeded"></a>`delegation_depth_exceeded` | 403 | [limes's codes](#limess-codes) |
| <a id="unknown_kid"></a>`unknown_kid` | 401 | [limes's codes](#limess-codes) |
| <a id="not_found"></a>`not_found` | 404 | [limes's codes](#limess-codes) |
| <a id="unknown_tenant"></a>`unknown_tenant` | 404 | [limes's codes](#limess-codes) |
| <a id="tenant_deleted"></a>`tenant_deleted` | 410 | [limes's codes](#limess-codes) |
| <a id="tenant_gone"></a>`tenant_gone` | 410 | [limes's codes](#limess-codes) |
| <a id="scope_mismatch"></a>`scope_mismatch` | 500 | [limes's codes](#limess-codes) |
| <a id="proof_unsupported"></a>`proof_unsupported` | 500 | [limes's codes](#limess-codes) |
| <a id="no_revocation_lineage"></a>`no_revocation_lineage` | 502 | [limes's codes](#limess-codes) |
| <a id="copy_counters_not_metered"></a>`copy_counters_not_metered` | 500 | [limes's codes](#limess-codes) |
| <a id="cursor_repeated"></a>`cursor_repeated` | 502 | [limes's codes](#limess-codes) |
| <a id="invalid_config"></a>`invalid_config` | 500 | [limes's codes](#limess-codes) |
| <a id="internal"></a>`internal` | 500 | [limes's codes](#limess-codes) |
| <a id="copy_limit_exceeded"></a>`copy_limit_exceeded` | 403 | [The meter's codes](#the-meters-codes) |
| <a id="counter_overflow"></a>`counter_overflow` | 409 | [The meter's codes](#the-meters-codes) |
| <a id="charge_not_found"></a>`charge_not_found` | 404 | [The meter's codes](#the-meters-codes) |
| <a id="refund_exceeds_charge"></a>`refund_exceeds_charge` | 409 | [The meter's codes](#the-meters-codes) |
| <a id="reservation_not_found"></a>`reservation_not_found` | 404 | [The meter's codes](#the-meters-codes) |
| <a id="usage_not_found"></a>`usage_not_found` | 404 | [The meter's codes](#the-meters-codes) |
| <a id="tenant_budget_not_found"></a>`tenant_budget_not_found` | 404 | [The meter's codes](#the-meters-codes) |
| <a id="tenant_budget_version_mismatch"></a>`tenant_budget_version_mismatch` | 409 | [The meter's codes](#the-meters-codes) |
| <a id="external_ref_conflict"></a>`external_ref_conflict` | 409 | [The meter's codes](#the-meters-codes) |
| <a id="settle_conflict"></a>`settle_conflict` | 409 | [The meter's codes](#the-meters-codes) |
| <a id="reserve_conflict"></a>`reserve_conflict` | 409 | [The meter's codes](#the-meters-codes) |
| <a id="refund_conflict"></a>`refund_conflict` | 409 | [The meter's codes](#the-meters-codes) |
| <a id="outcome_unknown"></a>`outcome_unknown` | 503 | [The meter's codes](#the-meters-codes) |
| <a id="tenant_budget_busy"></a>`tenant_budget_busy` | 503 | [The meter's codes](#the-meters-codes) |
| <a id="lease_exhausted"></a>`lease_exhausted` | 403 | [The meter's codes](#the-meters-codes) |
| <a id="lease_requests_exhausted"></a>`lease_requests_exhausted` | 403 | [The meter's codes](#the-meters-codes) |
| <a id="lease_not_found"></a>`lease_not_found` | 404 | [The meter's codes](#the-meters-codes) |
| <a id="lease_nested"></a>`lease_nested` | 409 | [The meter's codes](#the-meters-codes) |
| <a id="leasing_disabled"></a>`leasing_disabled` | 500 | [The meter's codes](#the-meters-codes) |
| <a id="feature_disabled"></a>`feature_disabled` | 500 | [The meter's codes](#the-meters-codes) |
| <a id="unauthorized_client"></a>`unauthorized_client` | 400 | [The exchange's codes](#the-exchanges-codes) |
| <a id="invalid_client"></a>`invalid_client` | 401 | [The exchange's codes](#the-exchanges-codes) |
| <a id="unsupported_grant_type"></a>`unsupported_grant_type` | 400 | [The exchange's codes](#the-exchanges-codes) |
| <a id="invalid_grant"></a>`invalid_grant` | 400 | [The exchange's codes](#the-exchanges-codes) |
| <a id="invalid_target"></a>`invalid_target` | 400 | [The exchange's codes](#the-exchanges-codes) |
| <a id="invalid_scope"></a>`invalid_scope` | 400 | [The exchange's codes](#the-exchanges-codes) |
| <a id="invalid_authorization_details"></a>`invalid_authorization_details` | 400 | [The exchange's codes](#the-exchanges-codes) |
| <a id="server_error"></a>`server_error` | 500 | [The exchange's codes](#the-exchanges-codes) |
| <a id="use_dpop_nonce"></a>`use_dpop_nonce` | 400 | [The exchange's codes](#the-exchanges-codes) |
| <a id="invalid_dpop_proof"></a>`invalid_dpop_proof` | 400 | [The exchange's codes](#the-exchanges-codes) |
| <a id="temporarily_unavailable"></a>`temporarily_unavailable` | 429 | [The exchange's codes](#the-exchanges-codes) |
| <a id="keys-unavailable"></a>`keys-unavailable` | 503 | A refusal with no `reason`: The issuer's keys cannot be fetched |
| <a id="revocation-unavailable"></a>`revocation-unavailable` | 503 | A refusal with no `reason`: The revocation status cannot be checked |
| <a id="malformed-authorization"></a>`malformed-authorization` | 400 | A refusal with no `reason`: The Authorization header is malformed |
| <a id="no-credentials"></a>`no-credentials` | 401 | A refusal with no `reason`: Credentials are required |
| <a id="dpop-nonce-required"></a>`dpop-nonce-required` | 401 | A refusal with no `reason`: A DPoP proof with the server's nonce is required |
| <a id="invalid-dpop-proof"></a>`invalid-dpop-proof` | 401 | A refusal with no `reason`: The DPoP proof is missing or invalid |
| <a id="invalid-token"></a>`invalid-token` | 401 | A refusal with no `reason`: The access token is invalid |
| <a id="insufficient-scope"></a>`insufficient-scope` | 403 | A refusal with no `reason`: The token does not allow this request |
| <a id="budget-exhausted"></a>`budget-exhausted` | 403 | A refusal with no `reason`: The token's request or budget limit is exhausted |
| <a id="proof-quota-exceeded"></a>`proof-quota-exceeded` | 429 | A refusal with no `reason`: Too many DPoP proofs inside the replay window |
