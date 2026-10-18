package limes

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strconv"
	"time"
	"unicode"

	"github.com/google/uuid"
)

// UsageStore is everything a consumer implements for runtime accounting. It
// is the union of narrower contracts so that code which only meters (an
// interceptor, a handler) can depend on Meter alone, a reporter on
// MeterReader, and code which only administers tenant ceilings on
// TenantBudgets.
type UsageStore[TX any] interface {
	Meter[TX]
	MeterReader
	TenantBudgets
	UsageHousekeeping
}

// Meter enforces the request and budget ceilings (Caveats.Requests,
// Caveats.Budget) and keeps the
// charges ledger. Separated from Store so the issuance / revocation surface
// stays focused on identity, not runtime state.
//
// # Delegation trees
//
// A capability's counters cover its whole subtree: Bump and Charge
// apply to the capability AND to every ancestor in its delegation chain, and
// each ancestor's own ceiling — read from its stored record, not from the
// caller — must admit the increment. So an orchestrator holding 25.00 that
// delegates 2.00 to each of a hundred workers can still spend 25.00 in total,
// not 200.00. Narrowing at delegation bounds each child; this bounds the sum.
// An ancestor whose record is gone (Store.Purge, after expiry) no longer bounds
// anything — its children expired with it, since a child cannot outlive its
// parent. The capability itself must be on record: Bump, Charge, Reserve and
// Settle refuse one that is not — never recorded, or purged — with
// ErrNotFound, since none of its ancestors' ceilings could be read. A cost
// reported late is charged only while the record lasts, so keep records
// (PurgeRequest.ExpiredFor) as long as costs may still arrive.
//
// # Copies of a Biscuit
//
// A Biscuit's holder can give a copy limits of its own (CopyCeiling), which
// arrive through the Subject of BumpRequest, ChargeRequest and ReserveRequest
// (MeterSubject.Copies). Each is counted under its revocation id, apart from
// its siblings, and checked
// before the capability's ceilings; the capability's counters still take
// every copy's requests and spend. A charge or reservation records the copies
// it debited, so Refund, Settle, Release and ReleaseExpired return to them
// without being handed them again. A Meter that does not count copies must
// not be paired with a verifier that sets VerifierConfig.MeterCopies.
//
// All methods are concurrency-safe. A rejected call mutates nothing, so a
// retry after rejection is safe. Each method first refuses a request whose
// Validate does, before it reads anything: a store that calls Validate
// enforces a rule added to it without a change of its own.
//
// A store over a database holds at most one connection per call: the reads of
// the lineage, the ceilings and the ledger, the writes, and onCharged all run
// on the one transaction the call opens. A call that takes a second
// connection while it holds its transaction deadlocks once as many
// calls run as the pool has connections — each holds one and waits for
// another — which a large pool under a light load never shows. Run metertest
// against a pool of one or two connections to catch it.
type Meter[TX any] interface {
	// Bump increments the request counter of the capability and of
	// each ancestor, and returns the capability's new count. When any
	// ceiling in the chain would be crossed it returns
	// ErrRequestLimitExceeded and nothing is mutated.
	Bump(ctx context.Context, req BumpRequest) (BumpReceipt, error)

	// Charge adds req.Amount to the spend counters of the capability, of
	// each ancestor and of the tenant aggregate, writes one charges-ledger
	// row, and runs onCharged — ALL atomically. Checks, in order:
	//
	//   1. Each Biscuit copy's own budget (req.Subject.Copies), innermost
	//      first → ErrBudgetExceeded.
	//   2. The capability's own ceiling (req.Subject.Budget()) →
	//      ErrBudgetExceeded.
	//   3. Each ancestor's ceiling → ErrBudgetExceeded.
	//   4. The tenant aggregate ceiling → ErrTenantBudgetExceeded.
	//
	// Any rejection, or an error from onCharged, leaves every counter and
	// the ledger untouched. onCharged runs inside the charge, atomically
	// with it, once every ceiling has passed. It receives the consumer's
	// transaction handle and the receipt the call will return (Charged), so
	// a side effect such as an outbox write commits with the spend and names
	// the ChargeID. The ledger row is written after onCharged returns: it is
	// not there yet to read. Pass nil to skip it. It must not call back into the Meter: an implementation may
	// hold its counters while onCharged runs, so a nested call can wait on
	// itself.
	//
	// The returned receipt carries the ledger row's ID; Refund takes it.
	//
	// With req.ExternalRef set, Charge is idempotent on (CapabilityID,
	// ExternalRef): a charge whose pair is already in the ledger moves
	// nothing, does not run onCharged, and returns that charge's receipt
	// with Replayed set — also once the capability is no longer on record,
	// as Settle replays a settled hold. The pair names one cost: arriving again with a
	// different Amount, UnitCode or TenantID, or under other Biscuit copies
	// than req.Subject carries, it returns ErrExternalRefConflict and moves
	// nothing. With req.Overrun = OverrunPolicyRecord, a charge that
	// would cross a ceiling is committed anyway and the receipt reports it
	// (ChargeReceipt.Overrun); see OverrunPolicy.
	Charge(ctx context.Context, req ChargeRequest, onCharged func(ctx context.Context, c Charged[TX]) error) (ChargeReceipt, error)

	// Refund returns spend from one recorded charge to every counter that
	// charge debited (capability, ancestors, tenant aggregate), and records
	// the refund against the charge. req.Remaining refunds whatever
	// remains, which makes a full refund idempotent; a partial refund that
	// would take the total refunded past the charge returns
	// ErrRefundExceedsCharge and changes nothing. An unknown charge ID
	// returns ErrChargeNotFound, and a request naming neither an Amount nor
	// Remaining, or both, ErrInvalidRequest. The receipt carries the amount
	// actually refunded.
	//
	// A charge counted in a tenant accounting period that has since been
	// closed (SetTenantBudget with ResetSpend) is refunded to the
	// capability, its ancestors and its copies, but not to the tenant
	// aggregate: the current period never counted it.
	//
	// For a cost only known afterwards, prefer Reserve/Settle: a
	// reservation lapses on its own if the caller never settles it, where
	// an estimate charged up front stays charged.
	Refund(ctx context.Context, req RefundRequest) (RefundReceipt, error)

	// Reserve holds req.Amount against the capability, each ancestor and
	// the tenant aggregate, checking every ceiling against spend plus
	// what is already held. Holds count against later charges and
	// reservations exactly as spend does, until the reservation is
	// settled, released or expires. A hold expires req.TTL from now, or when
	// its capability's record does if that comes sooner: past it no one can
	// settle the hold. Rejections return the Charge sentinels and hold
	// nothing.
	Reserve(ctx context.Context, req ReserveRequest) (Reservation, error)

	// Settle ends a reservation and charges the actual cost in its place,
	// atomically: the hold is released and req.Amount is charged as
	// Charge would, with onCharged run inside. The actual cost may be
	// below, equal to or above the hold; above it, the excess must fit
	// every ceiling, or Settle returns the Charge sentinel and the
	// reservation stays in place for the caller to settle lower or
	// release — unless req.Overrun is OverrunPolicyRecord, when the cost is
	// charged anyway and the receipt reports it.
	//
	// Settle is idempotent on the reservation: settling one already
	// settled moves nothing, does not run onCharged, and returns the
	// receipt of the charge that settled it, with Replayed set. An unknown,
	// released or expired reservation returns ErrReservationNotFound, and so
	// does the zero id, which names none — never a direct charge. So does a
	// reservation made by another capability or tenant, or under other Biscuit copies
	// than req.Subject carries: a hold is settled by the copy that made it,
	// so a copy cannot settle a sibling's hold past its own limits.
	Settle(ctx context.Context, req SettleRequest, onCharged func(ctx context.Context, c Charged[TX]) error) (ChargeReceipt, error)

	// Release ends a reservation without charging. Idempotent: releasing
	// one that is gone is a no-op. So is releasing a reservation made by
	// another capability or tenant, or under other Biscuit copies than req.Subject
	// carries: it is none of this subject's, and stays held, so a copy
	// cannot free a sibling's hold to spend past its own limits.
	Release(ctx context.Context, req ReleaseRequest) error
}

// MeterReader reads back what the Meter recorded: charges, reservations and
// counters. A reporter or an admin view depends on it alone.
type MeterReader interface {
	// GetCharge returns one charge of the ledger, by the ChargeID its
	// receipt carried — the id Refund takes. ErrChargeNotFound when there
	// is none.
	GetCharge(ctx context.Context, chargeID uuid.UUID) (ChargeRecord, error)

	// ChargeByRef returns the charge a capability took under an external
	// reference (ChargeRequest.ExternalRef), so a reporter can reconcile
	// its own records with the ledger. ErrChargeNotFound when there is none,
	// an empty reference included: no charge is taken under no name.
	ChargeByRef(ctx context.Context, req ChargeByRefRequest) (ChargeRecord, error)

	// GetReservation returns a reservation still holding budget — open,
	// or expired and not yet released, since until ReleaseExpired runs it
	// still counts (ExpiresAt tells which). ErrReservationNotFound once it
	// is settled or released.
	GetReservation(ctx context.Context, reservationID uuid.UUID) (Reservation, error)

	// ListReservations returns, a page at a time, every reservation
	// GetReservation would return that was made against the capability
	// itself — not its delegated children — soonest to expire first, and
	// holds expiring at the same instant in ascending ID, compared bytewise.
	// It refuses what ListReservationsRequest.Validate refuses.
	ListReservations(ctx context.Context, req ListReservationsRequest) (Page[Reservation], error)

	// GetUsage returns the current snapshot. Returns ErrUsageNotFound when no
	// row exists for the capability — typically means it's never been
	// used (no requests, no charges). For a capability with delegated
	// children the counters include the children's usage.
	GetUsage(ctx context.Context, capID uuid.UUID) (Usage, error)
}

// TenantBudgets administers the tenant-aggregate ceilings Charge enforces.
type TenantBudgets interface {
	// GetTenantBudget returns the tenant-aggregate snapshot. Returns
	// ErrTenantBudgetNotFound when no row exists (tenant has no cap
	// configured AND has never been charged).
	//
	// The aggregate counts from the tenant's first charge or reservation,
	// whether or not a ceiling has been set: Charge and Reserve create the
	// row, with no ceiling and ResourceVersion 0, when it is missing. A
	// ceiling set afterwards is measured against that spend and those holds.
	GetTenantBudget(ctx context.Context, tenantID uuid.UUID) (TenantBudget, error)

	// SetTenantBudget upserts the tenant cap. ResetSpend=true rolls
	// the accounting period and zeroes the spend — operators call
	// this on each billing close. ResetSpend=false adjusts the cap
	// mid-cycle without affecting accumulated spend. It refuses what
	// SetTenantBudgetRequest.Validate refuses — a ceiling outside
	// 0..MaxNanos is ErrInvalidAmount — and changes nothing. A ceiling in a
	// unit other than the row's returns ErrUnitCodeMismatch while anything
	// is held in the old unit, or spent in it and not reset.
	SetTenantBudget(ctx context.Context, req SetTenantBudgetRequest) (TenantBudget, error)

	// ListTenantBudgets returns tenant budget rows; a consumer joins its
	// own tenant data to them by TenantID. Filter picks the rows:
	//   - TenantBudgetFilterUnspecified, TenantBudgetFilterAll: every one.
	//   - TenantBudgetFilterAtOrPast: rows with a ceiling — a bound or 0 — where
	//     spent / max * 100 >= ThresholdPct, exactly; a ceiling of 0 is
	//     spent through at any threshold. At a threshold of 0, every row
	//     with a ceiling.
	//   - TenantBudgetFilterUnlimited: rows whose ceiling is Unlimited.
	// Limit is the page size, as PageLimit reads it.
	// It refuses what ListTenantBudgetsRequest.Validate refuses.
	//
	// Rows come most at risk first. Ceilings of 0, which allow nothing,
	// lead; bounded ceilings follow by spent / max descending, compared
	// exactly as fractions — unclamped, so of two tenants past their
	// ceilings the further one leads; unlimited ceilings, with no utilization, trail. Within each,
	// ties go by tenant id. A page ends with a cursor that fetches
	// the next one, empty on the last page. Spend moves between pages, so
	// a tenant may show on two of them, or on none; a cursor is a place in
	// the order, not a snapshot.
	ListTenantBudgets(ctx context.Context, req ListTenantBudgetsRequest) (Page[TenantBudget], error)
}

// UsageHousekeeping reclaims usage rows of capabilities that are gone.
type UsageHousekeeping interface {
	// DeleteUsage drops the capability's row and the counters of its Biscuit
	// copies. Called when the capability is reaped. It releases the
	// capability's open reservations first,
	// returning their holds to its ancestors and its tenant: nothing could
	// settle or release them afterwards. A capability with no row is no
	// error: there is nothing to drop.
	DeleteUsage(ctx context.Context, req DeleteUsageRequest) error

	// PurgeOrphans deletes the usage rows of capabilities no longer on
	// record (Store.Purge), with their copies' counters, releasing their
	// reservations as DeleteUsage does, at
	// most req.Limit of them; the caller loops while a batch comes back
	// full.
	PurgeOrphans(ctx context.Context, req PurgeOrphansRequest) (PurgeOrphansResult, error)

	// ReleaseExpired releases the reservations past their expiry, at most
	// req.Limit of them; a hold expires with its capability at the latest
	// (Meter.Reserve). A revoked capability's hold is not released early: it
	// can still be settled, as a cost already incurred belongs in the ledger
	// (StoredMeterSubject). Run on a timer: until it runs, an expired hold
	// keeps counting against the ceilings, which errs towards refusing
	// spend, never towards allowing too much.
	ReleaseExpired(ctx context.Context, req ReleaseExpiredRequest) (ReleaseExpiredResult, error)
}

// BumpRequest is the input to Meter.Bump.
type BumpRequest struct {
	// Subject is the capability counted, with its ceiling and copy limits:
	// VerifiedCapability.MeterSubject. Ancestors' ceilings come from their
	// records.
	Subject MeterSubject
}

// Validate refuses a request whose subject names no capability, no tenant or
// an unknown unit.
func (r BumpRequest) Validate() error { return r.Subject.validate() }

// ChargeRequest is the input to Meter.Charge.
type ChargeRequest struct {
	// Subject is the capability charged — its tenant, its own ceiling, its
	// unit and the presented copy's limits: VerifiedCapability.MeterSubject.
	// No conversion happens: a counter the charge moves — the
	// capability's, an ancestor's, the tenant's — that already counts in
	// another unit refuses it with ErrUnitCodeMismatch, and so does a
	// reservation.
	Subject MeterSubject
	// Amount is the cost, in the subject's unit. Within 0..MaxNanos
	// (Nanos.Validate).
	Amount Nanos
	// Op and Actor are stamped on the ledger row for attribution only;
	// both may be empty and neither affects enforcement.
	Op    Op
	Actor string
	// ExternalRef names the cost in the consumer's own records — the id of
	// the call or the event that reported it — and makes the charge
	// idempotent on (capability, ExternalRef). Empty: every call charges.
	// At most MaxExternalRefBytes long.
	ExternalRef string
	// Overrun is what happens when Amount would cross a ceiling.
	Overrun OverrunPolicy
}

// Validate refuses an amount out of range, an unknown overrun policy, an
// external ref past MaxExternalRefBytes, and a subject that names no
// capability, no tenant or no unit, or an unknown unit.
func (r ChargeRequest) Validate() error {
	if err := r.Amount.Validate(); err != nil {
		return err
	}
	if err := r.Overrun.Validate(); err != nil {
		return err
	}
	if err := ValidateExternalRef(r.ExternalRef); err != nil {
		return err
	}
	return r.Subject.validateAmounts()
}

// ChargeRecord is one charge as the ledger holds it.
type ChargeRecord struct {
	ID           uuid.UUID
	CapabilityID uuid.UUID
	TenantID     uuid.UUID
	// Amount is what the charge took; RefundedAmount is what has been returned
	// from it since.
	Amount         Nanos
	RefundedAmount Nanos
	UnitCode       string
	// ExternalRef is the charge's ChargeRequest.ExternalRef; empty for none.
	ExternalRef string
	// Overrun reports that the charge crossed a ceiling (OverrunPolicyRecord).
	Overrun bool
	// Op and Actor are what the charge was for and who made it, as the
	// request named them; At is when it was committed.
	Op    Op
	Actor string
	At    time.Time
	_     noCompare
}

// MaxExternalRefBytes bounds ChargeRequest.ExternalRef: long enough for any
// UUID, ULID or provider call id, short enough to index.
const MaxExternalRefBytes = 200

// OverrunPolicy is what a charge does when its amount would cross a ceiling —
// a Biscuit copy's, the capability's, an ancestor's or the tenant's.
type OverrunPolicy uint8

const (
	// OverrunPolicyUnspecified takes the default, OverrunPolicyReject: a
	// caller that says nothing never commits past a ceiling.
	OverrunPolicyUnspecified OverrunPolicy = iota
	// OverrunPolicyReject refuses the charge with the ceiling's sentinel and
	// changes nothing. Use it for a cost not yet incurred: the refusal is
	// what stops it.
	OverrunPolicyReject
	// OverrunPolicyRecord commits the charge past the ceiling and reports it in
	// ChargeReceipt.Overrun. Use it for a cost already incurred — reported
	// after the fact, out of the request path — where refusing would only
	// leave the ledger short of what was spent. The crossed ceiling then
	// refuses every later charge and reservation under OverrunPolicyReject.
	OverrunPolicyRecord
)

// ChargeReceipt is the result of a committed charge.
type ChargeReceipt struct {
	// ChargeID identifies the ledger row; pass it to Meter.Refund.
	ChargeID uuid.UUID
	// SpentAmount is the capability's spend as this call returns (its subtree's,
	// when it has delegated children).
	SpentAmount Nanos
	// Replayed reports that the charge was already in the ledger — the same
	// ExternalRef, or a reservation already settled — so this call moved
	// nothing and ChargeID names the earlier charge.
	Replayed bool
	// Overrun reports that the charge crossed at least one ceiling, which
	// only OverrunPolicyRecord allows.
	Overrun bool
	// TenantID and UnitCode are the charge's tenant and unit, so a caller
	// holding only the receipt — logged, or passed on — can attribute it. A
	// refusal by a ceiling or by onCharged still sets them.
	TenantID uuid.UUID
	UnitCode string
	_        noCompare
}

// Charged is what Meter.Charge and Meter.Settle hand onCharged: the charge
// it runs inside, before the call commits it.
type Charged[TX any] struct {
	// Tx is the consumer's transaction handle the charge commits on.
	Tx TX
	// Receipt is the receipt the call returns once the charge commits:
	// ChargeID names the ledger row it writes. Replayed is never set, as a
	// replay runs no onCharged.
	Receipt ChargeReceipt
	_       noCompare
}

// RefundReceipt is the result of Meter.Refund.
type RefundReceipt struct {
	// ChargeID is the charge refunded from.
	ChargeID uuid.UUID
	// Amount is the amount this call returned; zero when nothing was
	// left to refund.
	Amount Nanos
	// TenantID and UnitCode are the charge's.
	TenantID uuid.UUID
	UnitCode string
	_        noCompare
}

// DefaultReservationTTL is how long a reservation lasts when the request
// does not say.
const DefaultReservationTTL = 5 * time.Minute

// ReserveRequest is the input to Meter.Reserve.
type ReserveRequest struct {
	// Subject is the capability the hold counts against
	// (VerifiedCapability.MeterSubject). The reservation keeps the
	// presented copy's limits, and Settle checks and charges them.
	Subject MeterSubject
	// Amount to hold, in the subject's unit. Within 0..MaxNanos
	// (Nanos.Validate).
	Amount Nanos
	// TTL is how long the hold lasts if never settled or released, at most
	// until its capability expires. 0 = DefaultReservationTTL; negative is
	// ErrInvalidRequest.
	TTL time.Duration
	// Op and Actor are carried onto the charge Settle records.
	Op    Op
	Actor string
}

// Validate refuses an amount out of range, a subject that names no
// capability, no tenant or no unit, or an unknown unit, and a negative TTL.
func (r ReserveRequest) Validate() error {
	if err := r.Amount.Validate(); err != nil {
		return err
	}
	if err := r.Subject.validateAmounts(); err != nil {
		return err
	}
	if r.TTL < 0 {
		return invalidRequest("reservation TTL %s is negative", r.TTL)
	}
	return nil
}

// Reservation is a committed hold, as Reserve made it and as GetReservation
// and ListReservations read it back.
type Reservation struct {
	ID           uuid.UUID
	CapabilityID uuid.UUID
	TenantID     uuid.UUID
	// Amount is held in UnitCode.
	Amount   Nanos
	UnitCode string
	// Op and Actor are what Settle will stamp on the charge.
	Op    Op
	Actor string
	// Copies are the Biscuit copies the hold is also on, innermost first,
	// with the budget each is held against.
	Copies    []HeldCopy
	ExpiresAt time.Time
}

// SettleRequest is the input to Meter.Settle.
type SettleRequest struct {
	// Subject is the capability settling, whose ceiling the charge meets
	// (VerifiedCapability.MeterSubject). A reservation of another
	// capability is ErrReservationNotFound.
	Subject       MeterSubject
	ReservationID uuid.UUID
	// Amount is the actual cost to charge. Within 0..MaxNanos (Nanos.Validate).
	Amount Nanos
	// Overrun is what happens when Amount would cross a ceiling.
	Overrun OverrunPolicy
}

// Validate refuses a subject that names no capability, no tenant or no
// unit, or an unknown unit, an amount out of range, and an unknown overrun
// policy. The zero ReservationID is not refused here: it names no
// reservation, which Settle reports as ErrReservationNotFound.
func (r SettleRequest) Validate() error {
	if err := r.Subject.validateAmounts(); err != nil {
		return err
	}
	if err := r.Amount.Validate(); err != nil {
		return err
	}
	return r.Overrun.Validate()
}

// RefundRequest is the input to Meter.Refund. It names either an Amount or
// Remaining, never both and never neither.
type RefundRequest struct {
	ChargeID uuid.UUID
	// Amount to refund, above zero and within MaxNanos (Nanos.Validate).
	Amount Nanos
	// Remaining refunds whatever of the charge is not yet refunded, which
	// makes a full refund idempotent: once nothing remains, it returns
	// nothing.
	Remaining bool
}

// Validate refuses a request that names no amount, or both an amount and
// Remaining.
func (r RefundRequest) Validate() error {
	switch {
	case r.Remaining && r.Amount != 0:
		return invalidRequest("refund: Amount %v and Remaining both set", r.Amount)
	case r.Remaining:
		return nil
	case r.Amount == 0:
		return invalidRequest("refund: neither an Amount nor Remaining")
	}
	return r.Amount.Validate()
}

// TenantBudget is the snapshot view of the tenant aggregate cap. Amounts are
// Nanos, exact; a store keeps them exactly too (an integer or decimal column),
// so that sums do not drift.
type TenantBudget struct {
	TenantID uuid.UUID
	// Ceiling bounds the tenant's spend in the current period, in the unit
	// the tenant counts in: Ceiling.UnitCode, which the first charge or
	// hold records when no ceiling named it. A tenant whose ceiling was
	// never set counts Unlimited.
	Ceiling     BudgetCeiling
	SpentAmount Nanos
	// ReservedAmount is held by the tenant's open reservations.
	ReservedAmount Nanos
	PeriodStart    time.Time
	// PeriodEnd is when the accounting window closes; zero for an
	// open-ended one.
	PeriodEnd time.Time
	// UpdatedAt is when the store last wrote the aggregate, by its clock.
	UpdatedAt time.Time
	// ResourceVersion is the OCC token a caller passes back as
	// SetTenantBudgetRequest.ExpectedVersion. Zero means no ceiling has
	// ever been set — the row, if there is one, holds only what Charge and
	// Reserve counted.
	ResourceVersion int64
	_               noCompare
}

// wholePct is a whole, in percent.
const wholePct = 100

// Utilization is how much of the ceiling is spent, in percent, clamped to
// 0..100: a tenant past its ceiling, or with a ceiling of 0, is at 100.
// limited is false for an unlimited tenant, which has no utilization to
// report; pct is then 0.
func (b TenantBudget) Utilization() (pct float64, limited bool) {
	switch {
	case b.Ceiling.Unlimited:
		return 0, false
	case b.Ceiling.Max == 0:
		return wholePct, true
	}
	return min(max(float64(b.SpentAmount)/float64(b.Ceiling.Max)*wholePct, 0), wholePct), true
}

// SetTenantBudgetRequest is the input for UsageStore.SetTenantBudget.
type SetTenantBudgetRequest struct {
	TenantID uuid.UUID
	// Ceiling is the tenant's new ceiling, in the unit it names: the zero
	// value freezes the tenant's spend, in whatever unit it counts, and
	// BudgetCeiling{Unlimited: true, UnitCode: u} lifts it.
	Ceiling BudgetCeiling
	// PeriodEnd pins a closing time for the accounting window; zero leaves
	// it open-ended (typical for "just set the cap, I'll roll later").
	PeriodEnd time.Time
	// ResetSpend=true zeroes the spend and rolls PeriodStart to
	// now. false leaves the counter alone — the cap changes mid-
	// window.
	ResetSpend bool
	// ExpectedVersion is the optimistic-concurrency guard, checked
	// atomically with the write. 0 asserts no ceiling has been set yet (the
	// budget is missing, or holds only counted spend) and is itself a
	// conflict if one has, so two operators editing one ceiling cannot
	// overwrite each other unseen.
	ExpectedVersion int64
}

// Validate refuses a request with no tenant, a ceiling that does not
// validate — among them one that lets something be spent and names no unit
// — or a negative ExpectedVersion, as ErrInvalidRequest. A store calls it before reading the
// tenant's row, so a malformed request is refused as one, never as a
// version conflict.
func (r SetTenantBudgetRequest) Validate() error {
	switch {
	case r.TenantID == uuid.Nil:
		return invalidRequest("set tenant budget: no tenant")
	case r.ExpectedVersion < 0:
		return invalidRequest("set tenant budget: ExpectedVersion %d is negative", r.ExpectedVersion)
	}
	return r.Ceiling.Validate()
}

// TenantBudgetFilter picks the rows ListTenantBudgets lists.
type TenantBudgetFilter uint8

const (
	// TenantBudgetFilterUnspecified takes the default, TenantBudgetFilterAll: a
	// request that names no filter lists every tenant.
	TenantBudgetFilterUnspecified TenantBudgetFilter = iota
	// TenantBudgetFilterAll lists every tenant: no filter.
	TenantBudgetFilterAll
	// TenantBudgetFilterAtOrPast lists tenants with a ceiling spent to
	// ListTenantBudgetsRequest.ThresholdPct or past it.
	TenantBudgetFilterAtOrPast
	// TenantBudgetFilterUnlimited lists tenants with no ceiling.
	TenantBudgetFilterUnlimited
)

// String names the filter for logs and errors.
func (f TenantBudgetFilter) String() string {
	switch f {
	case TenantBudgetFilterUnspecified:
		return "unspecified"
	case TenantBudgetFilterAll:
		return "all"
	case TenantBudgetFilterAtOrPast:
		return "at-or-past"
	case TenantBudgetFilterUnlimited:
		return "unlimited"
	}
	return fmt.Sprintf("TenantBudgetFilter(%d)", uint8(f))
}

// Validate refuses a filter this package does not define, as
// ErrInvalidRequest.
func (f TenantBudgetFilter) Validate() error {
	if f > TenantBudgetFilterUnlimited {
		return invalidRequest("list tenant budgets: unknown filter %s", f)
	}
	return nil
}

// ListTenantBudgetsRequest is the input shape for
// UsageStore.ListTenantBudgets.
type ListTenantBudgetsRequest struct {
	// Filter picks the rows; the zero value lists every tenant.
	Filter TenantBudgetFilter
	// ThresholdPct is the utilization TenantBudgetFilterAtOrPast lists from,
	// 0..100. Any other filter takes none.
	ThresholdPct float64
	// Limit is the page size; see PageLimit. Negative is refused.
	Limit int
	// Cursor is the one the previous page returned; empty for the first.
	Cursor string
}

// ExactPercent is pct as the decimal it is written as — the shortest decimal
// that reads back as the same float64 — so a threshold of 1.1 means eleven
// tenths exactly, not the binary fraction a hair above it. A store compares
// spent / max × 100 against it without rounding. It is nil for NaN or an
// infinity, which ListTenantBudgetsRequest.Validate refuses.
func ExactPercent(pct float64) *big.Rat {
	if math.IsNaN(pct) || math.IsInf(pct, 0) {
		return nil
	}
	r, _ := new(big.Rat).SetString(strconv.FormatFloat(pct, 'g', -1, 64))
	return r
}

// maxThresholdPct is the highest utilization a threshold can name.
const maxThresholdPct = wholePct

// Validate refuses a request no page can answer: a negative page size, an
// unknown filter, a threshold outside 0..100, or a threshold beside a filter
// that takes none.
func (r ListTenantBudgetsRequest) Validate() error {
	if err := r.Filter.Validate(); err != nil {
		return err
	}
	switch {
	case r.Limit < 0:
		return invalidRequest("list tenant budgets: limit %d is negative", r.Limit)
	case math.IsNaN(r.ThresholdPct) || r.ThresholdPct < 0 || r.ThresholdPct > maxThresholdPct:
		return invalidRequest("list tenant budgets: threshold %v%% is outside 0..%d", r.ThresholdPct, maxThresholdPct)
	case r.ThresholdPct != 0 && r.Filter != TenantBudgetFilterAtOrPast:
		return invalidRequest("list tenant budgets: a threshold with filter %s, which takes none", r.Filter)
	}
	return nil
}

// Usage is the snapshot view of a capability's runtime counters.
type Usage struct {
	CapabilityID uuid.UUID
	RequestCount int64
	SpentAmount  Nanos
	// ReservedAmount is held by open reservations (its subtree's, for a
	// capability with delegated children).
	ReservedAmount Nanos
	// UnitCode is the unit the counters count in: the subject's, recorded
	// by the first Bump, Charge or Reserve of a subject that names one, so
	// a capability that has only been bumped reads its unit too. Empty
	// while none has.
	UnitCode string
	_        noCompare
}

// CopyUsage is the counters of one Biscuit copy with limits of its own (see
// Meter, "Copies of a Biscuit"): the copy and every copy attenuated from it.
type CopyUsage struct {
	RevocationID   []byte
	CapabilityID   uuid.UUID
	RequestCount   int64
	SpentAmount    Nanos
	ReservedAmount Nanos
}

// CopyUsageReader reads the counters of Biscuit copies. It is separate from
// Meter so that a Meter which does not count copies implements nothing more.
type CopyUsageReader interface {
	// CopyUsage returns the counters of each copy named that has been used,
	// in no particular order; a copy never used has no counters and is
	// absent from the result. It refuses what CopyUsageRequest.Validate
	// refuses.
	CopyUsage(ctx context.Context, req CopyUsageRequest) (CopyUsageResult, error)
}

// MaxCopyUsageIDs is the most revocation ids one CopyUsage reads, as a page
// of any listing holds.
const MaxCopyUsageIDs = MaxListLimit

// CopyUsageRequest names the copies CopyUsage reads.
type CopyUsageRequest struct {
	// RevocationIDs are the copies' revocation ids: at least one, at most
	// MaxCopyUsageIDs, none empty.
	RevocationIDs [][]byte
}

// Validate refuses a request naming no copy, more than MaxCopyUsageIDs, or
// an empty revocation id, as ErrInvalidRequest.
func (r CopyUsageRequest) Validate() error {
	switch n := len(r.RevocationIDs); {
	case n == 0:
		return invalidRequest("copy usage: no revocation id")
	case n > MaxCopyUsageIDs:
		return invalidRequest("copy usage: %d revocation ids, past MaxCopyUsageIDs (%d)", n, MaxCopyUsageIDs)
	}
	for i, id := range r.RevocationIDs {
		if len(id) == 0 {
			return invalidRequest("copy usage: revocation id %d is empty", i)
		}
	}
	return nil
}

// CopyUsageResult is what CopyUsage read.
type CopyUsageResult struct {
	// Copies are the counters of the copies that have been used.
	Copies []CopyUsage
	_      noCompare
}

// Usage-related sentinels; ErrBudgetExceeded lives in types.go.
var (
	// ErrInvalidAmount — an amount that is negative or above MaxNanos, or a
	// decimal that is not one (ParseAmount). Refunds are explicit; a
	// negative charge is not one. A malformed request, so it matches
	// ErrInvalidRequest.
	ErrInvalidAmount error = &familyError{"limes: invalid amount", ErrInvalidRequest}

	// ErrCounterOverflow — an amount, each valid on its own, that would take
	// a counter past MaxNanos. The request is well formed; the counter has
	// no room for it.
	ErrCounterOverflow = errors.New("limes: counter would pass MaxNanos")

	// ErrChargeNotFound — Refund, GetCharge or ChargeByRef named a charge
	// with no ledger row.
	ErrChargeNotFound error = &familyError{"limes: charge not found", ErrNotFound}

	// ErrRefundExceedsCharge — Refund would return more than the charge
	// took, counting earlier refunds of the same charge.
	ErrRefundExceedsCharge = errors.New("limes: refund exceeds charge")

	// ErrReservationNotFound — Settle named a reservation that does not
	// exist, was released, or has expired. One already settled is not an
	// error: Settle returns its charge again (ChargeReceipt.Replayed).
	ErrReservationNotFound error = &familyError{"limes: reservation not found", ErrNotFound}

	// ErrRequestLimitExceeded — the capability, a Biscuit copy or an
	// ancestor used every request its ceiling allows; the next was blocked.
	ErrRequestLimitExceeded = errors.New("limes: request limit exceeded")

	// ErrUsageNotFound — no usage row exists for this capability.
	// Get-only; bump/charge always inserts on first hit.
	ErrUsageNotFound error = &familyError{"limes: usage row not found", ErrNotFound}

	// ErrTenantBudgetExceeded — the tenant aggregate cap has been
	// hit. Distinct from ErrBudgetExceeded because the remedy differs:
	// an operator raises a tenant's ceiling with SetTenantBudget, while a
	// capability's ceiling is in its token and lifting it means issuing a
	// new one.
	ErrTenantBudgetExceeded = errors.New("limes: tenant aggregate budget exceeded")

	// ErrTenantBudgetNotFound — Get-only; the tenant has no ceiling set
	// and has never been charged. A tenant charged before any ceiling was
	// set has a row, Unlimited, with ResourceVersion 0.
	ErrTenantBudgetNotFound error = &familyError{"limes: tenant budget row not found", ErrNotFound}

	// ErrTenantBudgetVersionMismatch — SetTenantBudget's OCC guard refused the
	// write: the stored resource_version is not the one the caller read, so
	// someone else changed the cap in between. The caller re-reads and decides,
	// rather than silently overwriting a change it never saw.
	ErrTenantBudgetVersionMismatch = errors.New("limes: tenant budget resource_version mismatch")
)

// ErrExternalRefConflict — Charge named an ExternalRef its capability already
// charged under, for a different amount, unit or tenant, or under other
// Biscuit copies. One reference names one cost; replaying the first receipt
// would report the second as booked. The error names the reference, never
// the charge it names.
// Matches ErrInvalidRequest.
var ErrExternalRefConflict error = &familyError{"limes: external ref already names a different charge", ErrInvalidRequest}

// Validate reports whether p is an OverrunPolicy this package defines.
// Implementations call it before touching state, so an unknown policy is never
// read as either one.
func (p OverrunPolicy) Validate() error {
	if p > OverrunPolicyRecord {
		return fmt.Errorf("%w: unknown overrun policy %s", ErrInvalidRequest, p)
	}
	return nil
}

// String names the policy for logs and errors.
func (p OverrunPolicy) String() string {
	switch p {
	case OverrunPolicyUnspecified:
		return "unspecified"
	case OverrunPolicyReject:
		return "reject"
	case OverrunPolicyRecord:
		return "record"
	default:
		return fmt.Sprintf("OverrunPolicy(%d)", uint8(p))
	}
}

// ValidateExternalRef reports whether ref is usable as
// ChargeRequest.ExternalRef: empty, or printable text within
// MaxExternalRefBytes.
func ValidateExternalRef(ref string) error {
	if len(ref) > MaxExternalRefBytes {
		return fmt.Errorf("%w: external ref is %d bytes, at most %d", ErrInvalidRequest, len(ref), MaxExternalRefBytes)
	}
	for _, r := range ref {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("%w: external ref %q contains a non-printable character", ErrInvalidRequest, ref)
		}
	}
	return nil
}

// BumpReceipt is what a Bump counted.
type BumpReceipt struct {
	// RequestCount is the capability's request count after this one.
	RequestCount int64
	_            noCompare
}

// ReleaseRequest names the reservation to release, and who releases it.
type ReleaseRequest struct {
	// Subject is the capability releasing, as it reserved
	// (VerifiedCapability.MeterSubject). A reservation of another
	// capability, or of other copies, is left held.
	Subject       MeterSubject
	ReservationID uuid.UUID
}

// Validate refuses a request whose subject names no capability, no tenant or
// an unknown unit.
func (r ReleaseRequest) Validate() error { return r.Subject.validate() }

// ListReservationsRequest asks for one page of a capability's reservations.
type ListReservationsRequest struct {
	CapabilityID uuid.UUID
	// Limit is the page size; see PageLimit. Negative is refused.
	Limit int
	// Cursor is the one the previous page returned; empty for the first.
	Cursor string
}

// Validate refuses a request with no capability or a negative page size.
func (r ListReservationsRequest) Validate() error {
	switch {
	case r.CapabilityID == uuid.Nil:
		return invalidRequest("list reservations: capability id required")
	case r.Limit < 0:
		return invalidRequest("list reservations: limit %d is negative", r.Limit)
	}
	return nil
}

// PurgeOrphansRequest is one batch of UsageHousekeeping.PurgeOrphans.
type PurgeOrphansRequest struct {
	// Limit bounds the rows deleted. Zero: DefaultHousekeepingLimit;
	// negative is refused.
	Limit int
}

// PurgeOrphansResult is what one PurgeOrphans deleted.
type PurgeOrphansResult struct {
	Purged int64
	_      noCompare
}

// ReleaseExpiredRequest is one batch of UsageHousekeeping.ReleaseExpired.
type ReleaseExpiredRequest struct {
	// Limit bounds the reservations released. Zero:
	// DefaultHousekeepingLimit; negative is refused.
	Limit int
}

// ReleaseExpiredResult is what one ReleaseExpired released.
type ReleaseExpiredResult struct {
	Released int64
	_        noCompare
}

// ChargeByRefRequest names one charge by the reference it was taken under.
type ChargeByRefRequest struct {
	// CapabilityID is the capability the charge was taken against.
	CapabilityID uuid.UUID
	// ExternalRef is the ChargeRequest.ExternalRef it was taken under.
	ExternalRef string
}

// DeleteUsageRequest names the capability whose usage row DeleteUsage drops.
type DeleteUsageRequest struct {
	CapabilityID uuid.UUID
}
