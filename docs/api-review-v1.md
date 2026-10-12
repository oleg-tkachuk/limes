# API review before v1.0

Review of the exported API of `github.com/oleg-tkachuk/limes` at v0.7.0. The
root package exports 173 top-level identifiers; `memstore`, `metertest` and
`storetest` add 9. After v1.0 every one of them, and every method of every
interface a consumer implements, is frozen until a `/v2` module path.

Each item gives the evidence, the change, and what it breaks. Items marked
**decision** have more than one defensible answer.

## Decisions

Taken on 2026-10-11, judged on the architecture alone — backward
compatibility is not a constraint before 1.0.

| # | Question | Decision | Why |
|---|---|---|---|
| 1 | Wire format through 1.x | `VerifierConfig.AcceptTokenTypes`; v4 (v3 before the explicit-unlimited change) accepted throughout 1.x, so a v4 ships in a minor: verifiers first, then issuers. | Versioned format with a controlled migration. Reading unknown claims leniently is refused: a caveat a verifier does not know is one it does not enforce. |
| 2 | Token algorithm | Ed25519 only for 1.x; another algorithm is another `typ`. | No runtime algorithm choice (RFC 8725); versioning the protocol replaces algorithm agility, and decision 1 carries the version. |
| 3 | What `Delegate` trusts | `Verify` returns a `VerifiedCapability` with unexported fields, and `Delegate` accepts only that type. | "Parse, don't validate": the compiler, not a doc comment, guarantees the parent was verified; a token's caveats are immutable, so no store read is needed beyond the revocation check `Delegate` already makes. |
| 4 | Proof-of-possession errors | A family of their own, `ErrProofOfPossession`, beside `ErrInvalidSignature` — renamed `ErrInvalidToken`, which it always meant, RFC 6750's `invalid_token` — and both under an umbrella `ErrUnauthenticated`. | `errors.Is` asserts identity: a missing or replayed proof is not an invalid signature. The overlap needed `reasonAliases`, made table order load-bearing and once made a nonce challenge read as permanent. |
| 5 | `Decode`, `VerifySignature` | A subpackage, `limes/inspect`. | A debugging tool kept apart from the API that authorises, as `net/http/httputil` is from `net/http`. |
| 6 | `TenantBudgetCursor` | Moves into memstore; every list cursor is the store's own and opaque. | One rule for both lists; the format is the store's implementation detail. |
| 7 | Package layout | `jwks` and `dpop` become leaf packages the root imports; the meter stays in the root. | Both implement standards (RFC 7517/7638, RFC 9449) without depending on `Capability`, so they split without cycles and are usable on their own. |
| P3 | Zero values | Adopted as written below. | |
| P4 | Configuration | Adopted as written below. | |

## 1. Defects — fixed in v0.8.0 (#27)

| # | Finding | Evidence | Change |
|---|---|---|---|
| D1 | A negative `IssuerConfig.DefaultTTL` is accepted, and every `Issue` without a TTL then mints an already-expired capability. | issuer.go:70 checks `== 0` only; `expiry` (issuer.go:171) refuses a negative *request* TTL but applies a negative default. | Refuse a negative `DefaultTTL` in `NewIssuer`. |
| D2 | A negative `VerifierConfig.Leeway` narrows the validity window; a negative `MaxTokenBytes` refuses every token; a zero leeway cannot be expressed (0 becomes 30s). | verifier.go:233-238 | Refuse negatives; see P3 for expressing zero. |
| D3 | `PrincipalType` is never validated: any string is signed and accepted, and `Agent` may be set on a non-agent principal. | No `Validate` on the type (types.go:138); `Issue` and the verifier check only `TenantID`. | `PrincipalType.Validate()`, called by `Issue`, `Delegate` and the verifier. Refuses tokens already minted with custom types. |
| D4 | `ErrMalformedAuthorization` has no `Reason`, though `ErrorResponse` answers it with 400; nothing pins the two ordered tables (`reasonRules`, `refusals`) against each other. | reason.go:48-77, challenge.go:85-102 | Add `ReasonMalformedAuthorization` and a test that every refusal row has a reason and the orders agree. |
| D5 | doc.go:47 says a capability is passed by value so "no caller can alter one another holds", but `Issue` keeps the caller's `Audience` and `Caveats` slices. | issuer.go:141-146 | Clone at the boundary. |
| D6 | `ReserveRequest.TTL` says "0 = default"; memstore treats any value ≤ 0 as the default, while `Issue` refuses a negative TTL. | usage.go:357, memstore.go:844 | Negative is `ErrInvalidRequest`; pin it in `metertest`. |

## 2. Interfaces a consumer implements — the tightest constraint

Adding a method to any of these after 1.0 breaks every implementation.

| # | Interface | Finding | Change | Breaks |
|---|---|---|---|---|
| I1 | `Signer` | signer.go:19 invites a KMS implementation, but `Sign(ctx, Capability)` must emit the whole JWT, and the claim encoding, I-JSON checks and validation are unexported: an outside implementation must re-create the wire format by hand. `KeyID()` is never called by the library. `NewEd25519Signer` returns the interface. | A concrete signer over `crypto.Signer` — `NewSigner(kid, crypto.Signer) (*Signer, error)` — with encoding kept in the library, as `NewDPoPProof` already does (dpop.go:243). | `Signer` implementers (none can be correct today), `NewEd25519Signer` callers. |
| I2 | `ReplayCache`, `DPoPNonces` | `Seen` and `Valid` return a bare `bool`: a Redis- or SQL-backed implementation can only fail closed by answering "seen", and the cause never reaches logs, metrics or `ErrorResponse`. `Seen` has three positional parameters and cannot grow. | `Seen(ctx, ReplayEntry) (bool, error)`; `Valid(ctx, nonce) (bool, error)`. | `MemoryReplayCache`, `HMACNonces`, consumer implementations. |
| I3 | `Verifier` | Only `StandardVerifier` implements it, no library code accepts it, and the real surface (`ErrorResponse`, `DPoPNonce`, `BiscuitCopy`) already lives outside it. A commitment nobody pays for. | Remove; consumers declare the narrow interface they mock. | External references to `limes.Verifier`. |
| I4 | `Store` | `Issuer` calls only `Insert` and `IsRevoked`; five of seven methods are admin API the library never calls. `Get` is a subset of `GetRecord`. `Insert` and `PurgeExpired` are positional. **No method reaps capability records**, yet issuer.go:24, usage.go:37 and `PurgeOrphans` assume records disappear — a SQL store grows without bound. | `Issuer` takes a narrow `IssuanceStore{Insert; IsRevoked}`; drop `Get`; request structs for `Insert` and `PurgeExpired` (with a `Limit`); a record-reaping method or a documented consumer duty ordered against `UsageHousekeeping`. | Every `Store` implementation. |
| I5 | `Meter` | 11 methods, five of them reads an interceptor never needs; `Bump` returns a bare `int64`; `Release` and `ChargeByRef` are positional; `ListReservations` is unbounded where both other lists page. | Split `MeterReader` from `Meter`; `Bump` returns a receipt; request structs for `Release` and `ListReservations` (paged). | Every `UsageStore`, `WithMetering`. |
| I6 | `UsageHousekeeping` | `Delete`, `PurgeOrphans`, `ReleaseExpired` take no limit; `PurgeOrphans` is untested by `metertest`; `Delete` of an unknown id is undocumented. | Request structs with `Limit`; a `metertest` check for `PurgeOrphans`. | Every `UsageStore`. |
| I7 | `KeyResolver` | Decided: Ed25519 for 1.x (decision 2). `PublicKey` returns `ed25519.PublicKey`, so a second token algorithm needs a new interface. | Either declare Ed25519 permanent for 1.x (the token `typ` already versions the format), or return `crypto.PublicKey` now. **Done:** Ed25519 is permanent for 1.x, stated on `KeyResolver` and in the README. | — / every resolver. |
| I8 | decorators | `WithMetering` needs a second wrapper type to forward one optional interface (metering_store.go:38-51); a second optional interface doubles them again. | Pick a rule: a fixed forwarding list pinned by a test, or an `Unwrap()` convention. **Chosen: the fixed list** — `Instrument` is the one decorator and `CopyUsageReader` the one optional interface; a test reads the package's interfaces from source and fails when the decorator hides one its store implements. | Nothing yet. |
| I9 | conformance | `storetest.Env` has one `Ctx`; `metertest.Env` has `TenantCtx`/`ListCtx`. `ReplayCache`, `DPoPNonces`, `KeyResolver` have no suite. | Align the two `Env`s; a suite for each interface a consumer implements. **Done:** `storetest.Env` gains `NewTenant` and `TenantCtx`; `dpop/dpoptest` checks `ReplayCache` and `Nonces`, `jwks/jwkstest` checks `KeyResolver`. | Code comparing `storetest.Env` values. |

## 3. Data types

| # | Finding | Change | Breaks |
|---|---|---|---|
| T1 | Every `Meter` request carries the ceilings (`MaxBudget`, `MaxRequests`, `Copies`) as caller-supplied copies of the verified capability; passing 0 drops the capability's own ceiling. | Requests carry a `MeterSubject` built only by `VerifiedCapability.MeterSubject` or `Record.MeterSubject`, so id, tenant, ceilings, unit and copies have one source. | Every `Meter` call site and implementation. |
| T2 | `Delegate` narrows against the `Capability` the caller hands in (issuer.go:236); a stale or unverified one is not compared with the stored record. Lower risk than it reads: whoever holds `*Issuer` can already `Issue` anything. | Decided: `VerifiedCapability` (decision 3). | `DelegateRequest.Parent`. |
| T3 | Names disagree: `MaxBudget` / `MaxBudgetAmount`; `Spent` / `SpentAmount`; `ID` / `ChargeID`; `Planes` (Attenuation, Biscuit fact) / `Audience`; `Reason` the free-text field / `Reason` the refusal code; `Op` typed in `CheckRequest`, `string` in `ChargeRequest`; British `NormaliseUnitCode`, `UtilisationPct` beside American `Authorize`. | One spelling per concept; American identifiers; `ID` on the entity, `XID` on a reference; the free-text field becomes `Note`. **Done**, with two sentinels gone: a full refund names `RefundRequest.Remaining`, and `TenantBudget.Utilization` reports whether there is a ceiling. | Field and function names across all packages. |
| T4 | `AuthorizeRequest` embeds `VerifyRequest` and `CheckRequest`: a field or method later added to both is an ambiguous selector — a compile break caused by an additive change. | Named fields `Verify`, `Check`. **Done.** | Every `AuthorizeRequest` literal. |
| T5 | `Limit int32` leaks a column type; optional times are `*time.Time` in one place (`PeriodEnd`) and the zero `time.Time` everywhere else; `ListTenantBudgetsRequest` fields are undocumented. | `int`; zero-is-absent; document. **Done.** | The two list requests. |
| T6 | `Caveats`, `Principal`, `AgentPrincipal` are both the Go API and the wire format (their JSON tags are the claims): a Go rename is a wire change. `Capability` carries a lone `json:"-"`. | Unexported wire structs, as `jwtClaims` already is. **Done:** `wirePrincipal`, `wireAgent`, `wireCaveats` in wire.go, convertible to the exported types; the golden token pins the bytes. | Nothing exported. |
| T7 | Enums follow three zero-value rules: `Effect` has `Unspecified`; `OverrunPolicy`'s zero is a real value (`Reject`) and it has no `String()`; `PrincipalType` and `AuthScheme` accept anything non-empty (`checkScheme`). | One rule — an explicit `Unspecified` zero — `String()` on every enum, unknown values refused. **Done**; the string-backed `PrincipalType` and `AuthScheme` print as themselves. | Numeric values of `OverrunPolicy`. |

## 4. Errors

| # | Finding | Change |
|---|---|---|
| E1 | The hierarchy (`caveatError` → `ErrCaveatViolation`, `popError` → `ErrInvalidSignature`, `requestError` → `ErrInvalidRequest`) is spread over 12 files; two tables encode the match order. | One table — sentinel, family, `Reason`, permanent, status — from which `reasonRules` and `refusals` are derived, and a golden test of the `errors.Is` matrix. apidiff guards identifiers, not these relations. **Done:** `refusalRules` in reason.go, pinned by errors_test.go. |
| E2 | Five unrelated not-found sentinels (`ErrNotFound`, `ErrChargeNotFound`, `ErrReservationNotFound`, `ErrUsageNotFound`, `ErrTenantBudgetNotFound`). | All match `ErrNotFound`, as the request errors match `ErrInvalidRequest`. Additive. **Done.** |
| E3 | `Delegate` returns `ErrExpired`/`ErrRevoked` about the *parent*; `ReasonOf` reports them as if the presented token were refused. | Kept: in a delegation the parent *is* the presented capability, and with decision 3 a verified one, so `expired` or `revoked` is the right reason. |
| E4 | Decided: a family of their own (decision 4). Proof-of-possession refusals match `ErrInvalidSignature`, so nonce and scheme refusals are carved back out through aliases and table order. | A separate `ErrProofOfPossession` family. Breaks consumers that map only `ErrInvalidSignature` — which the docs promise today. **Done**, under `ErrUnauthenticated` beside `ErrInvalidToken`. |

## 5. Unexport — no consumer needs them

High confidence, nothing outside the package uses them:
`WithCacheClock` (tests only; every other component takes a `Now` field),
`HasDotSegment`, `AccessTokenHash`, `PublicJWK`, `DPoPJWK`, `JWK`,
`JWKSDocument`, `DPoPTokenType`, `IsBiscuit`, `MediaTypeProblem`,
`WWWAuthenticateHeader`, `DPoPSigningAlgorithms`.

`TokenType` too (exporting it freezes a versioned value); `Decode` and
`VerifySignature` move to `inspect` (decision 5).

Keep — implementers need them, as memstore shows: `PageLimit`,
`DefaultListLimit`, `MaxListLimit`, `ValidateAmount`, `ValidateExternalRef`,
`MaxExternalRefBytes`, `NormaliseUnitCode`, `DefaultReservationTTL`,
`MaxNanos`, `NanosPerUnit`.

**Done:** every name above is unexported or gone; `PublicJWK` and `JWK` live
in `internal/jose`, `Decode` and `VerifySignature` in `inspect`, and
`NormaliseUnitCode` is `NormalizeUnitCode` (T3).

## 6. Merge and reshape

- `DeriveKeyID` re-implements `KeyThumbprint`'s RFC 7638 computation by hand;
  share one path and say they are equal.
- `JWK` and `DPoPJWK` become one unexported type.
- `GenerateEd25519Keypair` returns a four-tuple where `SigningKey` already
  holds the same; return `(SigningKey, error)`.
- `ValidateAmount` becomes `Nanos.Validate`, matching `OverrunPolicy.Validate`.
- `TenantBudgetCursor`: its format is published and memstore depends on it, but
  the `ListTenantBudgets` contract never requires it and `metertest` never
  round-trips it. Decided: it moves into memstore (decision 6).
- `NewDPoPProof` signs rather than constructs: `SignDPoPProof`.
- `WithMetering` is a decorator, not an option: rename.

**Done:** one thumbprint path in `internal/jose`, one JWK type, `GenerateKey`
returning the key, `Nanos.Validate`, the cursor in memstore, `dpop.Sign`, and
`Instrument`.

## 7. Policies to write down before tagging

**P1 — interfaces grow only by new interfaces.** A new capability is a new
single-method interface discovered by type assertion, as `CopyUsageReader`
already is; conformance suites skip what a store does not implement. Never a
method added to a published interface (go.dev/blog/module-compatibility).

**P2 — methods that may grow take a request and return a result struct**;
every I/O-backed method returns an error.

**P3 — zero values.** Zero means the documented default, and that default is
an exported `Default*` constant the field doc names (today `defaultLeeway`,
`defaultIssueTTL`, `defaultReplayCacheEntries` and the `defaultJWKS*` values
are unexported and their docs repeat the literal). Negative is an error, never
coerced. "Disabled", "unlimited" and "generate" are explicit — a field, a
constructor or a constant — never a reserved number or an empty string. Today:
cache `ttl < 0` disables, `WithMaxEntries(0)` silently does nothing,
`LoadOrGenerateKey("")` generates, `UtilisationPct` 0 means both "unused" and
"unlimited". **Done**, including the ceilings: `RequestCeiling` and
`BudgetCeiling` carry `Unlimited` explicitly and their zero value allows
nothing, on caveats, Biscuit copies, the meter subject and tenant budgets.

**P4 — configuration.** One shape: `NewX(XConfig) (*X, error)` for anything
with a default or a check, concrete return types, a `Now` field for the clock;
functional options only for decorators. Today there are four shapes and three
option types.

**P5 — wire format.** Decided: (b), decision 1. The verifier reads claims strictly and
accepts one `typ`, so any new caveat or Biscuit fact is refused by every
deployed verifier.
- (a) Freeze the claim set and the Biscuit vocabulary for all of 1.x; a new
  caveat means v2.
- (b) `VerifierConfig.AcceptTokenTypes`, v4 accepted throughout 1.x, so a v5
  ships in a minor: verifiers first, then issuers. Recommended. **Done:** `VerifierConfig.AcceptTokenTypes`; the format reached v4 before 1.0
  (explicit unlimited, P3), so `TokenTypeV4` is the one every 1.x verifier accepts.

**P6 — cutting 1.0.** Remove `BREAKING_IS_A_MINOR_BEFORE_1_0` from
`.github/release/release.config.cjs` and flip its case in
`release-rules.test.mjs`, in the same pull request as a commit carrying a
`BREAKING CHANGE:` footer — that commit cuts 1.0.0. Update README
"Versioning".

**P7 — after 1.0.** semantic-release would tag a break `v2.0.0`, which the Go
proxy ignores without a `/v2` module path. So `api-break-check.sh` fails any
incompatible change on the v1 line, marked or not; a v2 is a deliberate module
move.

## 8. Deferred

- Moving the meter out of the root: it leans on the capability types, so it
  stays until a v2 (decision 7 splits only `jwks` and `dpop`).
- A Postgres reference store in its own module, to prove `Store` and
  `UsageStore` against row-level security before they freeze.

## Plan

Each pull request is breaking and cuts a minor; the order puts code in its
final package before it is reshaped there. Steps 1–7 are done; step 8 waits
for the release decision.

1. **Errors** — `ErrUnauthenticated` over `ErrInvalidSignature` and the new
   `ErrProofOfPossession` family (decision 4); every not-found matching
   `ErrNotFound` (E2); `Delegate`'s parent errors wrapped (E3); one table from
   which `reasonRules` and `refusals` derive, and a golden `errors.Is` matrix
   (E1).
2. **Packages**, in three pull requests:
   - 2a — `jwks` (decision 7), over `internal/jose` and `internal/httpx`,
     which also merge the two thumbprint computations (section 6);
   - 2b — `dpop` (decision 7), with `ReplayCache` and `DPoPNonces` returning
     errors and `Seen` taking a request (I2); a leaf returns its own errors
     and the verifier adds the family;
   - 2c — `inspect` (decision 5), the unexport list (section 5) and the
     remaining merges (section 6).
3. **Configuration** — one constructor shape, exported `Default*` constants,
   negative values refused, "disabled" an explicit field (P3, P4).
4. **Issuance** — `VerifiedCapability` (decision 3); a concrete signer over
   `crypto.Signer` (I1); `Verifier` removed (I3).
5. **Stores** — `IssuanceStore`, `Get` dropped, request structs and record
   reaping (I4); `Meter` split, `Bump` receipt, paged `ListReservations` (I5);
   housekeeping limits (I6); the decorator rule (I8); both `Env`s aligned and
   suites for every implemented interface (I9); the cursor into memstore
   (decision 6); `MeterSubject` (T1).
6. **Data types** — names, `AuthorizeRequest` fields, limits and optional
   times, enums (T3–T5, T7).
7. **Wire format** — `AcceptTokenTypes` (decision 1), unexported wire structs
   (T6), Ed25519-only stated (decision 2).
8. **1.0** — P6, with P7 in the same change.
