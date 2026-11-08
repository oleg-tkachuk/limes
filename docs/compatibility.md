# Compatibility

What limes promises a consumer between releases, what counts as breaking
that promise, and how a break is announced.

## The contract

It covers every module of the repository alike: limes, pgstore,
redisreplay, otelmetric, exchange and biscuit.

- **The Go API** of every module: its exported names, their signatures,
  and the types they take and return. A signature is the contract whole,
  as the Go module tooling compares it: a function taken as a value has
  its type, so a parameter made variadic, or one added at the end, breaks
  that use even where every call still compiles.
- **The error contract** ([errors.md](errors.md)): every code, the HTTP
  status it is answered with, its category, and the reason a refusal is
  challenged with. Which codes `limes.Codes()` lists is contract; the
  order it lists them in is not, and may change in any release.
- **Defaults**: what a zero `Config` field, or an option left unset, does;
  [defaults.md](defaults.md) lists every `Config` field and every
  `Default…` constant's value.
- **Documented behaviour**: what a function's comment says it accepts,
  refuses and returns. A function that passed an input and now refuses it
  has changed, even when its signature has not.
- **What is stored and signed**: the token formats and the database
  schema, read by every replica of a rolling upgrade.

## Breaking

Any of these breaks a consumer:

- removing or renaming an exported name, or changing its signature;
- removing an error code, or answering one with another HTTP status,
  category or reason;
- answering an error with another code, a nested module's error among
  them: `limes.CodeOf` and its kin are limes's functions, so the break is
  limes's, and marked on the root module's commit, even when the change
  that causes it is a nested module's;
- changing a default;
- moving the line between what a documented function accepts and what it
  refuses, either way: refusing what it accepted, or accepting what it
  refused — but for a new token format, below;
- a stored form, or a form signed by default, that an earlier release
  cannot read.

These do not:

- a verifier reading a token format, a `limes_v`, it refused before. Every
  format a release reads stays read for the whole of the major, and a
  signer writes a new one only when its configuration names it, so
  verifiers are upgraded first and issuers opted in after
  ([ADR-0019](adr/0019-token-formats-in-1x.md));
- adding a name, a code, a category, a field whose zero value keeps the
  old behaviour, or an option;
- rewording an error's text, which is never a contract, or the prose of a
  row of [defaults.md](defaults.md): CI compares its Default and Value
  columns alone.

A consumer must therefore map a code or a category it does not know
rather than fail on it, and the default to map it by is its status:
`Problem.Status`, which every code has. A mapping of the categories
answers one added after it was written from that status, so a refused
request is never answered as a success, nor a client's error as the
server's. [`limestest`](../limestest) holds a consumer's mapping to the
codes and categories it was written against, so a code answered otherwise,
or a category added, fails the consumer's tests rather than its traffic.

## Before 1.0

A break is allowed, and announced three ways: the commit is marked
breaking (`!` or a `BREAKING CHANGE:` footer), which cuts a minor release;
[migration.md](migration.md) says what it was and what it is; and the
release notes list it, with every error code added, removed or answered
otherwise under **Codes**, every default changed or removed under
**Defaults**, and every change to this policy under **Contract**. CI holds the first two to the change: a Go API break, a change
to the error contract in [errors.md](errors.md), or a change to a default in
[defaults.md](defaults.md), with no marker fails, and so does a marker with
no migration entry. What a documented function accepts and refuses is held
by its tests alone.

Until v1.0.0 no signature is settled: any exported name of any module may
change in a minor release, marked so. That holds between the candidates
of 1.0 too, since a candidate is the newest 0.x release's API: read the
migration guide from the release one names to the release the next one
names. A consumer that holds a function as a value, rather than calls it,
meets such a change even where every call still compiles.

## From 1.0

Every module reaches v1 together with limes, and from then on gives the
same promise: no break lands on the v1 line of any of them. A name that is to go is marked
`// Deprecated:` with what replaces it, and stays, working, for the whole
of v1; it goes only with v2, under a new module path. A signature does not
change on v1 either: a new form of a function is a new function beside
it. An error code keeps
its status, category and reason for the whole of v1.

A released nested module builds against every later limes of its major:
a v1.0 of the exchange beside a v1.N of limes. The nested modules import
three of limes's internal packages — `internal/errs`, `internal/errcode`
and `internal/jose` — so those keep every name and signature for the
whole of v1, as an exported API does. CI builds each nested module's
newest release, and once it is at v1 every release of its major, against
limes as it is (`task verify:nested-internals`), and names the internal
symbol a change broke.

Every interface a consumer implements keeps its method set for the whole
of v1 ([ADR-0001](adr/0001-architecture-before-1-0.md)): a method added to
one breaks every implementation written against it. They are:

- the store contracts: `limes.Store` and each interface it is made of
  (`IssuanceStore`, `RecordReader`, `RevocationStore`), `CapabilityLister`,
  `RevocationLookup`, `RevocationLineageLookup`, `BiscuitRevocationLookup`,
  `BiscuitRevocationStore`, `BulkRevoker`, `TenantRecordEraser`,
  `RecordCensus`, `RecordLister`, `RecordListingReader`, `RecordReporter`
  and `ExchangeStore`;
- the verifier's and the issuer's plug-ins: `KeyResolver`, `TokenForm`,
  `ContextSigner` and `RevocationSignalTarget`, and `dpop.ReplayCache` and
  `dpop.Nonces`;
- the meter's: `meter.UsageStore` and each interface it is made of
  (`Meter`, with `RequestCounter` and `BudgetMeter`, `MeterReader`,
  `TenantBudgets`, `UsageHousekeeping`), `meter.LedgerReader` and
  `meter.LedgerHousekeeping`, and the optional `meter.Leaser`,
  `meter.CopyUsageReader`, `meter.TenantUsageEraser`,
  `meter.TenantBudgetLister`, `meter.TenantReservationLister`,
  `meter.MatchingReleaser` and `meter.FeatureGate`.

A capability added later arrives as an interface of its own, optional,
which a caller discovers by a type assertion on the store, as
`meter.Leaser`, `meter.CopyUsageReader` and `meter.TenantUsageEraser`
are; a store without it goes on compiling and serving everything else.

Two of their terms are settled as they stand, by decision rather than by
oversight. `Store.ListByPrincipal` stays a required method of `Store`,
not an optional interface beside it: every store lists a principal's
capabilities, which is what a caller holding only a `Store` relies on. And a `meter.ReserveRequest.ReservationID` keys one hold only
while that hold, or the charge that settled it, is on record: once the
hold is released — by `Release`, by `ReleaseExpired` past its expiry, or
with its capability's usage — the same id may hold anew, as
`meter.UsageStore.Reserve` says. A caller that must never reuse an id
mints a fresh one per hold.
